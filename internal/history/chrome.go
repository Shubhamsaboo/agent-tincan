package history

// The claude-chrome route reads chatgpt.com and claude.ai when the Tincan
// extension is not connected, with no human clicks: Claude Code's Claude in
// Chrome integration (`claude -p --chrome`) opens a tab in the user's own
// logged-in Chrome and runs one fixed JavaScript program that Tincan
// generates. The program fetches everything one query needs with the page's
// session, saves ONE file, tincan-history-<nonce>.json, to the Chrome
// download folder, and returns only "saved" or a short error code. The model
// driving Chrome sees the script and that code, never conversation content.
// Go then reads that one file, deletes it, and parses it with the same
// parsers and selection the extension route uses.
//
//	history service --stdin--> claude -p --chrome --> Chrome tab --fetch--> site
//	history service <--read+delete-- ~/Downloads/tincan-history-<nonce>.json

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"
)

// ErrClaudeChrome is a failure of the claude-chrome route itself: claude
// did not run, did not run the script, or its result file did not arrive.
var ErrClaudeChrome = errors.New("claude in chrome failed")

// Claude-chrome bounds.
const (
	// DefaultClaudeChromeTimeout bounds one claude run.
	DefaultClaudeChromeTimeout = 180 * time.Second
	// DefaultChromeFileWait bounds the wait for the result file after the
	// script reports it saved.
	DefaultChromeFileWait = 20 * time.Second
	// DefaultChromeMaxFile caps the result file.
	DefaultChromeMaxFile = 128 << 20
	// maxChromeDetails caps the conversations one run opens.
	maxChromeDetails = 20
	// maxChromeImageTotal caps the image bytes one run saves.
	maxChromeImageTotal = 40 << 20
	// maxClaudeOutput caps claude's stdout.
	maxClaudeOutput = 1 << 20
)

// ClaudeChromeTools are the only tools the claude run may use: the Claude
// in Chrome tab, navigation and JavaScript tools.
var ClaudeChromeTools = []string{
	"mcp__claude-in-chrome__tabs_context_mcp",
	"mcp__claude-in-chrome__tabs_create_mcp",
	"mcp__claude-in-chrome__navigate",
	"mcp__claude-in-chrome__javascript_tool",
	"mcp__claude-in-chrome__tabs_close_mcp",
}

//go:embed chromejs/common.js
var chromeCommonJS string

//go:embed chromejs/chatgpt.js
var chromeChatGPTJS string

//go:embed chromejs/claudeai.js
var chromeClaudeAIJS string

// chromeTemplate is the fixed program for src; __TINCAN_PARAMS__ is the
// only hole.
func chromeTemplate(src Source) (string, error) {
	var body string
	switch src {
	case SourceChatGPT:
		body = chromeChatGPTJS
	case SourceClaudeAI:
		body = chromeClaudeAIJS
	default:
		return "", fmt.Errorf("no claude-chrome script for source %q", src)
	}
	return strings.Replace(chromeCommonJS, "__TINCAN_SOURCE__", strings.TrimSpace(body), 1), nil
}

// chromeOrigin is the page the script runs in.
func chromeOrigin(src Source) string { return "https://" + siteOf(src) + "/" }

// chromePlan is what one claude-chrome run fetches: the List newest
// conversations and the first Details of them, or the one conversation ID;
// then up to Images images from those conversations, newest first.
type chromePlan struct {
	List    int
	Details int
	ID      string
	Images  int
}

// planFor sizes the one run a query gets. A plain latest read opens as
// many conversations as prompts were asked for; search and with_images
// open the recency window, capped at maxChromeDetails.
func planFor(q Query, w Window) chromePlan {
	var p chromePlan
	if q.wantsImages() {
		p.Images = MaxImages
	}
	if q.Mode == ModeConversation {
		p.ID = q.ConversationID
		return p
	}
	p.List = min(max(w.Max, 1), MaxListCount)
	if q.Mode == ModeLatest && !q.WithImages {
		p.Details = min(q.count(), maxChromeDetails, p.List)
	} else {
		p.Details = min(maxChromeDetails, p.List)
	}
	return p
}

var noncePattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// chromeParams are the script's only inputs.
type chromeParams struct {
	Nonce              string `json:"nonce"`
	List               int    `json:"list"`
	Details            int    `json:"details"`
	ID                 string `json:"id"`
	Images             int    `json:"images"`
	MaxImageBytes      int    `json:"max_image_bytes"`
	MaxTotalImageBytes int    `json:"max_total_image_bytes"`
}

// chromeScript returns the fixed program for src filled with p, after
// checking every value: a hex nonce, a conversation id of the native id
// shape, and integers in range.
func chromeScript(src Source, nonce string, p chromePlan) (string, error) {
	tmpl, err := chromeTemplate(src)
	if err != nil {
		return "", err
	}
	switch {
	case !noncePattern.MatchString(nonce):
		return "", errors.New("claude-chrome: invalid nonce")
	case p.ID != "" && !validNativeID(p.ID):
		return "", fmt.Errorf("claude-chrome: invalid conversation id %q", p.ID)
	case (p.ID != "") == (p.List > 0):
		return "", errors.New("claude-chrome: a run lists conversations or opens one, not both")
	case p.List < 0 || p.List > MaxListCount:
		return "", fmt.Errorf("claude-chrome: list count must be between 1 and %d", MaxListCount)
	case p.Details < 0 || p.Details > maxChromeDetails || p.Details > p.List:
		return "", fmt.Errorf("claude-chrome: details must be between 0 and %d and at most the list count", maxChromeDetails)
	case p.Images < 0 || p.Images > MaxImages:
		return "", fmt.Errorf("claude-chrome: images must be between 0 and %d", MaxImages)
	}
	b, err := json.Marshal(chromeParams{
		Nonce: nonce, List: p.List, Details: p.Details, ID: p.ID, Images: p.Images,
		MaxImageBytes: MaxImageBytes, MaxTotalImageBytes: maxChromeImageTotal,
	})
	if err != nil {
		return "", err
	}
	return strings.Replace(tmpl, "__TINCAN_PARAMS__", string(b), 1), nil
}

// chromeFileName is the one file a run saves.
func chromeFileName(nonce string) string { return "tincan-history-" + nonce + ".json" }

func newNonce() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// ChromeRunner runs one fixed script in a fresh tab of the user's Chrome
// and returns what the script returned.
type ChromeRunner interface {
	// Available reports whether the runner can run at all.
	Available() bool
	RunScript(ctx context.Context, origin, script string) (string, error)
}

// ClaudeChrome is the claude-chrome live route.
type ClaudeChrome struct {
	Runner ChromeRunner
	// DownloadDir returns Chrome's download folder; ChromeDownloadDir when
	// nil.
	DownloadDir func() (string, error)
	// Wait bounds the wait for the result file (DefaultChromeFileWait when
	// zero).
	Wait time.Duration
	// MaxFile caps the result file (DefaultChromeMaxFile when zero).
	MaxFile int64

	nonce func() (string, error)
	poll  time.Duration
}

// NewClaudeChrome returns the route running binary ("claude" on PATH when
// empty) from the default scratch dir.
func NewClaudeChrome(binary string) *ClaudeChrome {
	return &ClaudeChrome{Runner: &ClaudeRunner{Binary: binary, ScratchDir: DefaultScratchDir()}}
}

// DefaultClaudeBinary is $TINCAN_HISTORY_CLAUDE, else "claude".
func DefaultClaudeBinary() string {
	if b := os.Getenv("TINCAN_HISTORY_CLAUDE"); b != "" {
		return b
	}
	return "claude"
}

// Available reports whether the route can be tried: a runner whose claude
// binary exists.
func (c *ClaudeChrome) Available() bool { return c != nil && c.Runner != nil && c.Runner.Available() }

// fetchSnapshot runs plan for src in Chrome and returns what the script
// saved.
func (c *ClaudeChrome) fetchSnapshot(ctx context.Context, src Source, plan chromePlan) (*chromeSnapshot, error) {
	mk := c.nonce
	if mk == nil {
		mk = newNonce
	}
	nonce, err := mk()
	if err != nil {
		return nil, err
	}
	script, err := chromeScript(src, nonce, plan)
	if err != nil {
		return nil, unavailable(src, ErrRejected, err.Error())
	}
	dirOf := c.DownloadDir
	if dirOf == nil {
		dirOf = ChromeDownloadDir
	}
	dir, err := dirOf()
	if err != nil {
		return nil, unavailable(src, ErrClaudeChrome, "no Chrome download folder: "+err.Error())
	}
	res, err := c.Runner.RunScript(ctx, chromeOrigin(src), script)
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled) && ctx.Err() != nil:
			return nil, err
		case errors.Is(err, context.DeadlineExceeded):
			return nil, unavailable(src, ErrTimeout, "")
		}
		return nil, unavailable(src, ErrClaudeChrome, clip(err.Error(), 200))
	}
	if err := chromeResult(src, res); err != nil {
		return nil, err
	}
	raw, err := c.takeFile(ctx, filepath.Join(dir, chromeFileName(nonce)))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, unavailable(src, ErrClaudeChrome, err.Error())
	}
	snap, err := parseChromeSnapshot(src, nonce, raw)
	if err != nil {
		return nil, unavailable(src, ErrClaudeChrome, "unreadable result file: "+err.Error())
	}
	return snap, nil
}

var codePattern = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)

// chromeResult maps the script's answer, as the model relayed it, to nil
// ("saved") or a typed error. Free text is never passed on.
func chromeResult(src Source, res string) error {
	code := strings.Trim(strings.TrimSpace(res), "`\"'. \n")
	if !codePattern.MatchString(code) {
		return unavailable(src, ErrClaudeChrome, "unexpected answer from Claude in Chrome")
	}
	switch code {
	case "saved":
		return nil
	case "not_logged_in":
		return unavailable(src, ErrNotLoggedIn, "")
	case "not_found":
		return fmt.Errorf("%s: %w", src, ErrNotFound)
	case "endpoint_changed":
		return unavailable(src, ErrEndpointChanged, "")
	case "blocked":
		return unavailable(src, ErrEndpointChanged, "blocked")
	case "network":
		return unavailable(src, ErrSourceFailed, "network error")
	case "script_error":
		return unavailable(src, ErrClaudeChrome, "the read script failed in the page")
	case "http_429":
		return unavailable(src, ErrSourceFailed, "rate limited, HTTP 429")
	}
	if status, ok := strings.CutPrefix(code, "http_"); ok && len(status) == 3 {
		return unavailable(src, ErrSourceFailed, "HTTP "+status)
	}
	return unavailable(src, ErrClaudeChrome, "unexpected answer from Claude in Chrome")
}

// takeFile waits for exactly path, reads it within the size cap and
// deletes it, whether or not it parses. Nothing else in the folder is
// read; Chrome's in-progress .crdownload file has another name and is
// never touched.
func (c *ClaudeChrome) takeFile(ctx context.Context, path string) ([]byte, error) {
	wait := c.Wait
	if wait <= 0 {
		wait = DefaultChromeFileWait
	}
	poll := c.poll
	if poll <= 0 {
		poll = 100 * time.Millisecond
	}
	limit := c.MaxFile
	if limit <= 0 {
		limit = DefaultChromeMaxFile
	}
	deadline := time.Now().Add(wait)
	for {
		st, err := os.Lstat(path)
		if err == nil {
			defer func() { _ = os.Remove(path) }()
			if !st.Mode().IsRegular() {
				return nil, errors.New("the result file is not a regular file")
			}
			if st.Size() > limit {
				return nil, fmt.Errorf("the result file is over %d bytes", limit)
			}
			return readCapped(path, limit)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("cannot read the Chrome download folder")
		}
		if time.Now().After(deadline) {
			return nil, errors.New("the result file did not appear in the Chrome download folder")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(poll):
		}
	}
}

// chromeFile is one image the script saved.
type chromeFile struct {
	MIME string `json:"mime"`
	Data string `json:"data"`
}

// chromeDump is the result file: the site's own list and detail answers,
// in the shapes the extension route returns, plus image bytes by file id.
type chromeDump struct {
	Nonce   string                     `json:"nonce"`
	Source  Source                     `json:"source"`
	List    json.RawMessage            `json:"list"`
	Details map[string]json.RawMessage `json:"details"`
	Missing []string                   `json:"missing"`
	Files   map[string]chromeFile      `json:"files"`
}

// chromeSnapshot serves the fixed operations from one result file.
type chromeSnapshot struct {
	source Source
	dump   chromeDump
}

func parseChromeSnapshot(src Source, nonce string, raw []byte) (*chromeSnapshot, error) {
	var d chromeDump
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, errors.New("not JSON")
	}
	if d.Nonce != nonce || d.Source != src {
		return nil, errors.New("wrong nonce or source")
	}
	return &chromeSnapshot{source: src, dump: d}, nil
}

// errNotFetched means the run did not fetch what was asked: a conversation
// past the plan's details or an image past its cap.
var errNotFetched = errors.New("not in the claude-chrome result")

func (s *chromeSnapshot) request(_ context.Context, op Op, args OpArgs) (json.RawMessage, error) {
	switch op {
	case OpChatGPTList, OpClaudeAIList:
		if len(bytes.TrimSpace(s.dump.List)) == 0 || string(bytes.TrimSpace(s.dump.List)) == "null" {
			return nil, unavailable(s.source, ErrEndpointChanged, "no list in the result")
		}
		return s.dump.List, nil
	case OpChatGPTDetail, OpClaudeAIDetail:
		if d, ok := s.dump.Details[args.ID]; ok {
			return d, nil
		}
		if slices.Contains(s.dump.Missing, args.ID) {
			return nil, fmt.Errorf("%s: %w", s.source, ErrNotFound)
		}
		return nil, errNotFetched
	}
	return nil, unavailable(s.source, ErrRejected, "unknown operation")
}

func (s *chromeSnapshot) file(_ context.Context, _ Op, args OpArgs) ([]byte, error) {
	f, ok := s.dump.Files[args.FileID]
	if !ok {
		return nil, errNotFetched
	}
	b, ok := decodeBase64(f.Data)
	if !ok {
		return nil, errors.New("bad image encoding")
	}
	return b, nil
}

// ChromeDownloadDir is Chrome's download folder: download.default_directory
// from the Default profile's Preferences when set, else ~/Downloads.
func ChromeDownloadDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return chromeDownloadDir(runtime.GOOS, home), nil
}

func chromeDownloadDir(goos, home string) string {
	var prefs string
	switch goos {
	case "darwin":
		prefs = filepath.Join(home, "Library", "Application Support", "Google", "Chrome", "Default", "Preferences")
	case "linux":
		prefs = filepath.Join(home, ".config", "google-chrome", "Default", "Preferences")
	case "windows":
		prefs = filepath.Join(home, "AppData", "Local", "Google", "Chrome", "User Data", "Default", "Preferences")
	}
	if prefs != "" {
		if b, err := readCapped(prefs, 64<<20); err == nil {
			var p struct {
				Download struct {
					DefaultDirectory string `json:"default_directory"`
				} `json:"download"`
			}
			if json.Unmarshal(b, &p) == nil && filepath.IsAbs(p.Download.DefaultDirectory) {
				return filepath.Clean(p.Download.DefaultDirectory)
			}
		}
	}
	return filepath.Join(home, "Downloads")
}

// ClaudeRunner runs a script through `claude -p --chrome`: Claude Code
// with its Claude in Chrome integration, which needs Claude Code logged in
// with a claude.ai plan and the Claude in Chrome extension.
type ClaudeRunner struct {
	// Binary is the claude executable, "claude" (found on PATH) by default.
	Binary string
	// ScratchDir is the history service's own working directory; each run
	// gets a fresh empty subdirectory, removed afterwards.
	ScratchDir string
	// Timeout bounds one run (DefaultClaudeChromeTimeout when zero).
	Timeout time.Duration
}

func (r *ClaudeRunner) binary() string {
	if r.Binary == "" {
		return "claude"
	}
	return r.Binary
}

// Available implements ChromeRunner: the claude binary exists.
func (r *ClaudeRunner) Available() bool {
	_, err := exec.LookPath(r.binary())
	return err == nil
}

// claudeChromeArgs are the flags of one run. The prompt goes on stdin
// because --allowedTools takes a variable number of values.
func claudeChromeArgs() []string {
	return []string{"-p", "--chrome", "--output-format", "json", "--allowedTools", strings.Join(ClaudeChromeTools, ",")}
}

// claudeChromePrompt is the fixed instruction around the script.
func claudeChromePrompt(origin, script string) string {
	return "Use your Claude in Chrome browser tools for this task and nothing else.\n" +
		"1. Open a new tab in your tab group (tabs_create_mcp).\n" +
		"2. Navigate that tab to " + origin + " (navigate).\n" +
		"3. Run exactly the JavaScript below in that tab with javascript_tool. Do not change it, and do not run any other JavaScript.\n" +
		"4. Close the tab (tabs_close_mcp).\n" +
		"5. Reply with only the value the script returned, a single short word such as saved or not_logged_in, and nothing else.\n" +
		"Do not read, summarize or screenshot the page.\n\n" +
		"JavaScript:\n" + script + "\n"
}

// claudeChromeEnv is the service's environment minus ANTHROPIC_API_KEY
// (an API key overrides the claude.ai login that Claude in Chrome needs),
// any TINCAN_* variable, and CLAUDECODE (the marker of an enclosing Claude
// Code session).
func claudeChromeEnv(env []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if k == "ANTHROPIC_API_KEY" || k == "CLAUDECODE" || strings.HasPrefix(k, "TINCAN_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// RunScript implements ChromeRunner.
func (r *ClaudeRunner) RunScript(ctx context.Context, origin, script string) (string, error) {
	if r.ScratchDir == "" {
		return "", errors.New("no scratch dir for claude")
	}
	if err := os.MkdirAll(r.ScratchDir, 0o700); err != nil {
		return "", err
	}
	cwd, err := os.MkdirTemp(r.ScratchDir, "chrome-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(cwd) }()
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultClaudeChromeTimeout
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(rctx, r.binary(), claudeChromeArgs()...)
	cmd.Dir = cwd
	cmd.Env = claudeChromeEnv(os.Environ())
	cmd.Stdin = strings.NewReader(claudeChromePrompt(origin, script))
	var stdout cappedBuffer
	stdout.limit = maxClaudeOutput
	cmd.Stdout = &stdout
	var stderr tailBuffer
	cmd.Stderr = &stderr
	err = cmd.Run()
	if rctx.Err() != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("claude: %w", context.DeadlineExceeded)
	}
	res, perr := parseClaudeOutput(stdout.b)
	if errors.Is(perr, errNoClaudeResult) && err != nil {
		return "", fmt.Errorf("claude exited: %v", err)
	}
	return res, perr
}

// errNoClaudeResult means claude printed no result object.
var errNoClaudeResult = errors.New("claude printed no JSON result")

// claudeResult is the result object of `claude -p --output-format json`.
type claudeResult struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
}

// parseClaudeOutput reads the result from claude's JSON output: one result
// object, or an array of messages ending in one.
func parseClaudeOutput(b []byte) (string, error) {
	b = bytes.TrimSpace(b)
	var res claudeResult
	if bytes.HasPrefix(b, []byte("[")) {
		var msgs []json.RawMessage
		if err := json.Unmarshal(b, &msgs); err != nil {
			return "", errNoClaudeResult
		}
		found := false
		for _, m := range msgs {
			var r claudeResult
			if json.Unmarshal(m, &r) == nil && r.Type == "result" {
				res, found = r, true
			}
		}
		if !found {
			return "", errNoClaudeResult
		}
	} else if err := json.Unmarshal(b, &res); err != nil || res.Type != "result" {
		return "", errNoClaudeResult
	}
	if res.IsError {
		return "", fmt.Errorf("claude reported an error (%s): %s", res.Subtype, clip(res.Result, 160))
	}
	return res.Result, nil
}

// cappedBuffer keeps the first limit bytes written to it.
type cappedBuffer struct {
	b     []byte
	limit int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.limit - len(c.b); room > 0 {
		c.b = append(c.b, p[:min(room, len(p))]...)
	}
	return len(p), nil
}
