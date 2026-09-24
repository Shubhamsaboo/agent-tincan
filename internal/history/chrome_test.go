package history

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const testNonce = "0123456789abcdef0123456789abcdef"

// goldenPlans are the plans the checked-in scripts under
// testdata/chrome were generated with; the node test runs those files.
var goldenPlans = map[Source]chromePlan{
	SourceChatGPT:  {List: 50, Details: 3, Images: MaxImages},
	SourceClaudeAI: {List: 50, Details: 3, Images: MaxImages},
}

func goldenPath(src Source) string { return filepath.Join("testdata", "chrome", string(src)+".js") }

// The node test (extension/test/chrome.test.js) runs these exact files, so
// they must be what the generator produces. TINCAN_UPDATE_GOLDEN=1
// rewrites them.
func TestChromeScriptGolden(t *testing.T) {
	for src, plan := range goldenPlans {
		got, err := chromeScript(src, testNonce, plan)
		if err != nil {
			t.Fatal(err)
		}
		if os.Getenv("TINCAN_UPDATE_GOLDEN") == "1" {
			if err := os.WriteFile(goldenPath(src), []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(goldenPath(src))
		if err != nil {
			t.Fatal(err)
		}
		if got != string(want) {
			t.Fatalf("%s differs from the generator; rerun with TINCAN_UPDATE_GOLDEN=1", goldenPath(src))
		}
	}
}

var paramsLine = regexp.MustCompile(`(?m)^  const P = (.*);$`)

func TestChromeScriptEmbedsOnlyValidatedValues(t *testing.T) {
	for _, src := range []Source{SourceChatGPT, SourceClaudeAI} {
		tmpl, err := chromeTemplate(src)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(tmpl, "__TINCAN_PARAMS__") != 1 || strings.Contains(tmpl, "__TINCAN_SOURCE__") {
			t.Fatalf("%s template holes", src)
		}
		got, err := chromeScript(src, testNonce, chromePlan{List: 50, Details: 2, Images: 8})
		if err != nil {
			t.Fatal(err)
		}
		m := paramsLine.FindStringSubmatch(got)
		if m == nil {
			t.Fatalf("no params line in %s script", src)
		}
		// The template with its one hole filled is the whole script.
		if strings.Replace(tmpl, "__TINCAN_PARAMS__", m[1], 1) != got {
			t.Fatal("script differs from its template outside the params")
		}
		want := `{"nonce":"` + testNonce + `","list":50,"details":2,"id":"","images":8,"max_image_bytes":10485760,"max_total_image_bytes":41943040}`
		if m[1] != want {
			t.Fatalf("params\n got %s\nwant %s", m[1], want)
		}
		conv, err := chromeScript(src, testNonce, chromePlan{ID: "c1a0d000-0000-4000-8000-000000000001"})
		if err != nil || !strings.Contains(conv, `"list":0,"details":0,"id":"c1a0d000-0000-4000-8000-000000000001","images":0`) {
			t.Fatalf("conversation plan: %v", err)
		}
		if !strings.Contains(got, "'"+siteOf(src)+"'") && !strings.Contains(got, "https://"+siteOf(src)) {
			t.Fatalf("%s script does not target %s", src, siteOf(src))
		}
	}
	bad := []struct {
		name  string
		nonce string
		plan  chromePlan
	}{
		{"traversal id", testNonce, chromePlan{ID: "../../backend-api/me"}},
		{"dotted id", testNonce, chromePlan{ID: "a.b"}},
		{"query id", testNonce, chromePlan{ID: "abc?x=1"}},
		{"quote id", testNonce, chromePlan{ID: `x"};alert(1)//`}},
		{"long id", testNonce, chromePlan{ID: strings.Repeat("x", 129)}},
		{"id and list", testNonce, chromePlan{ID: "abc", List: 5}},
		{"nothing", testNonce, chromePlan{}},
		{"list too big", testNonce, chromePlan{List: MaxListCount + 1}},
		{"negative list", testNonce, chromePlan{List: -1, ID: "abc"}},
		{"details over list", testNonce, chromePlan{List: 2, Details: 3}},
		{"details over cap", testNonce, chromePlan{List: 50, Details: maxChromeDetails + 1}},
		{"too many images", testNonce, chromePlan{List: 5, Images: MaxImages + 1}},
		{"negative images", testNonce, chromePlan{List: 5, Images: -1}},
		{"short nonce", "abc", chromePlan{List: 5}},
		{"script nonce", `0123456789abcdef0123456789abcde"`, chromePlan{List: 5}},
		{"upper nonce", strings.ToUpper(testNonce), chromePlan{List: 5}},
	}
	for _, b := range bad {
		if _, err := chromeScript(SourceChatGPT, b.nonce, b.plan); err == nil {
			t.Errorf("%s accepted", b.name)
		}
	}
	if _, err := chromeScript(SourceCodex, testNonce, chromePlan{List: 5}); err == nil {
		t.Error("codex has no claude-chrome script")
	}
}

func TestPlanFor(t *testing.T) {
	w := DefaultWindow()
	cases := []struct {
		q    Query
		want chromePlan
	}{
		{Query{Mode: ModeLatest}, chromePlan{List: 50, Details: 1}},
		{Query{Mode: ModeLatest, Count: 3, WantImages: true}, chromePlan{List: 50, Details: 3, Images: MaxImages}},
		{Query{Mode: ModeLatest, WithImages: true}, chromePlan{List: 50, Details: maxChromeDetails, Images: MaxImages}},
		{Query{Mode: ModeSearch, Terms: []string{"fox"}}, chromePlan{List: 50, Details: maxChromeDetails}},
		{Query{Mode: ModeConversation, ConversationID: "abc"}, chromePlan{ID: "abc"}},
	}
	for _, c := range cases {
		if got := planFor(c.q, w); got != c.want {
			t.Errorf("planFor(%+v) = %+v, want %+v", c.q, got, c.want)
		}
	}
	if got := planFor(Query{Mode: ModeSearch, Terms: []string{"x"}}, Window{Max: 5}); got.List != 5 || got.Details != 5 {
		t.Errorf("small window plan %+v", got)
	}
}

var nonceInScript = regexp.MustCompile(`"nonce":"([0-9a-f]{32})"`)

// fakeRunner stands in for claude and the browser: it records each run
// and, like the script, writes the result file into dir.
type fakeRunner struct {
	mu      sync.Mutex
	absent  bool
	origins []string
	scripts []string
	reply   string
	err     error
	dir     string
	// body returns the result file for nonce; nil writes nothing.
	body func(nonce string) []byte
}

func (f *fakeRunner) Available() bool { return !f.absent }

func (f *fakeRunner) RunScript(_ context.Context, origin, script string) (string, error) {
	f.mu.Lock()
	f.origins = append(f.origins, origin)
	f.scripts = append(f.scripts, script)
	f.mu.Unlock()
	m := nonceInScript.FindStringSubmatch(script)
	if m == nil {
		return "", errors.New("no nonce in script")
	}
	if f.body != nil {
		if err := os.WriteFile(filepath.Join(f.dir, chromeFileName(m[1])), f.body(m[1]), 0o600); err != nil {
			return "", err
		}
	}
	if f.err != nil {
		return "", f.err
	}
	if f.reply == "" {
		return "saved", nil
	}
	return f.reply, nil
}

func (f *fakeRunner) runs() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.scripts)
}

// dumpFor builds the result file the script would save for src from the
// fixtures: the list, every fixture conversation, and PNGs whose tail is
// their file id.
func dumpFor(t *testing.T, src Source, nonce string) []byte {
	t.Helper()
	d := map[string]any{"nonce": nonce, "source": src, "details": map[string]json.RawMessage{}, "missing": []string{}, "files": map[string]any{}}
	var dir, listFile string
	var imgs []string
	switch src {
	case SourceChatGPT:
		dir, listFile = "chatgpt", "conversations.json"
		imgs = []string{"file-Sk3tchAbc123", "file-Gen3rated456", "file_00000000abcd1234"}
	case SourceClaudeAI:
		dir, listFile = "claudeai", "chat_conversations.json"
		imgs = []string{"f11e0000-0000-4000-8000-0000000000aa", "f11e0000-0000-4000-8000-0000000000bb"}
	}
	d["list"] = fixture(t, dir+"/"+listFile)
	matches, _ := filepath.Glob(filepath.Join("testdata", dir, "conversation-*.json"))
	for _, p := range matches {
		id := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), "conversation-"), ".json")
		d["details"].(map[string]json.RawMessage)[id] = fixture(t, dir+"/"+filepath.Base(p))
	}
	for _, id := range imgs {
		d["files"].(map[string]any)[id] = map[string]string{"mime": "image/png", "data": base64.StdEncoding.EncodeToString(append(fakePNG(64), id...))}
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newChromeRoute(t *testing.T, r ChromeRunner, dir string) *ClaudeChrome {
	return &ClaudeChrome{Runner: r, DownloadDir: func() (string, error) { return dir, nil }, Wait: 2 * time.Second, poll: 5 * time.Millisecond}
}

func chromeChatGPT(t *testing.T, ch Channel, fr *fakeRunner) *ChatGPT {
	r := newTestChatGPT(ch)
	r.Chrome = newChromeRoute(t, fr, fr.dir)
	return r
}

func chromeClaudeAI(t *testing.T, ch Channel, fr *fakeRunner) *ClaudeAI {
	r := newTestClaudeAI(ch)
	r.Chrome = newChromeRoute(t, fr, fr.dir)
	return r
}

func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 0 {
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Fatalf("download dir not cleaned up: %v", names)
	}
}

func TestClaudeChromeChatGPTRoundTrip(t *testing.T) {
	notConnected := errChannel(ErrExtensionNotConnected)
	fr := &fakeRunner{dir: t.TempDir()}
	fr.body = func(n string) []byte { return dumpFor(t, SourceChatGPT, n) }
	r := chromeChatGPT(t, notConnected, fr)

	convs, err := r.Read(context.Background(), Query{Source: SourceChatGPT, Mode: ModeLatest, WantImages: true}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(convs) != 1 || convs[0].Title != "Fox logo sketch" || len(convs[0].Messages) != 2 {
		t.Fatalf("latest %+v", convs)
	}
	c := convs[0]
	if c.Messages[0].Text != "make the fox ears bigger like this sketch" || c.Messages[1].Text != "Here is the fox with bigger ears." {
		t.Fatalf("messages %+v", c.Messages)
	}
	if got := imageSHAs(c, RoleUser); len(got) != 1 || got[0] != "file-Sk3tchAbc123" {
		t.Fatalf("attached images %v", got)
	}
	if got := imageSHAs(c, RoleAssistant); len(got) != 1 || got[0] != "file-Gen3rated456" {
		t.Fatalf("generated images %v", got)
	}
	if c.Messages[0].Images[0].Name != "sketch.png" || c.Messages[0].Images[0].MIME != "image/png" {
		t.Fatalf("image %+v", c.Messages[0].Images[0])
	}
	assertEmptyDir(t, fr.dir)
	if fr.origins[0] != "https://chatgpt.com/" || !strings.Contains(fr.scripts[0], `"list":50,"details":1,"id":"","images":8`) {
		t.Fatalf("run: %s %s", fr.origins[0], paramsLine.FindString(fr.scripts[0]))
	}

	convs, err = r.Read(context.Background(), Query{Source: SourceChatGPT, Mode: ModeSearch, Terms: []string{"sourdough"}, WantImages: true}, Options{})
	if err != nil || len(convs) != 1 || !strings.Contains(convs[0].Messages[0].Text, "nail polish") {
		t.Fatalf("search %+v %v", convs, err)
	}
	if got := imageSHAs(convs[0], RoleUser); len(got) != 1 || got[0] != "file_00000000abcd1234" {
		t.Fatalf("sediment pointer image %v", got)
	}

	convs, err = r.Read(context.Background(), Query{Source: SourceChatGPT, Mode: ModeConversation, ConversationID: "6a1f0c2e-1111-4a2b-9c3d-000000000001"}, Options{})
	if err != nil || len(convs) != 1 || len(convs[0].Messages) != 4 {
		t.Fatalf("conversation %+v %v", convs, err)
	}
	for _, m := range convs[0].Messages {
		if len(m.Images) > 0 {
			t.Fatal("images without want_images")
		}
	}
	if !strings.Contains(fr.scripts[2], `"list":0,"details":0,"id":"6a1f0c2e-1111-4a2b-9c3d-000000000001","images":0`) {
		t.Fatalf("conversation run params %s", paramsLine.FindString(fr.scripts[2]))
	}

	list, err := r.List(context.Background(), 10, Options{})
	if err != nil || len(list) != 2 || list[0].Title != "Fox logo sketch" {
		t.Fatalf("list %+v %v", list, err)
	}
	if !strings.Contains(fr.scripts[3], `"list":10,"details":0,"id":"","images":0`) {
		t.Fatalf("list run params %s", paramsLine.FindString(fr.scripts[3]))
	}
	assertEmptyDir(t, fr.dir)
}

func TestClaudeChromeClaudeAIRoundTrip(t *testing.T) {
	fr := &fakeRunner{dir: t.TempDir()}
	fr.body = func(n string) []byte { return dumpFor(t, SourceClaudeAI, n) }
	r := chromeClaudeAI(t, errChannel(ErrExtensionNotConnected), fr)
	convs, err := r.Read(context.Background(), Query{Source: SourceClaudeAI, Mode: ModeLatest, WithImages: true}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(convs) != 1 || convs[0].Messages[0].Text != "what is this creature in my photo?" || convs[0].Messages[1].Text != "That looks like an ochre sea star." {
		t.Fatalf("latest %+v", convs)
	}
	if got := imageSHAs(convs[0], RoleUser); len(got) != 1 || got[0] != "f11e0000-0000-4000-8000-0000000000aa" {
		t.Fatalf("attached image %v", got)
	}
	if got := imageSHAs(convs[0], RoleAssistant); len(got) != 1 || got[0] != "f11e0000-0000-4000-8000-0000000000bb" {
		t.Fatalf("reply image %v", got)
	}
	if convs[0].Messages[0].Images[0].Name != "tidepool.jpg" {
		t.Fatalf("image name %q", convs[0].Messages[0].Images[0].Name)
	}
	if fr.origins[0] != "https://claude.ai/" || !strings.Contains(fr.scripts[0], "out.source = 'claude-ai'") {
		t.Fatalf("run %s", fr.origins[0])
	}
	convs, err = r.Read(context.Background(), Query{Source: SourceClaudeAI, Mode: ModeSearch, Terms: []string{"Portland"}}, Options{})
	if err != nil || len(convs) != 1 || convs[0].ID != "c1a0d000-0000-4000-8000-000000000002" {
		t.Fatalf("search %+v %v", convs, err)
	}
	assertEmptyDir(t, fr.dir)
}

// A conversation past the plan's details is not in the file: the read
// answers from what was fetched rather than failing.
func TestClaudeChromeStopsAtFetchedDetails(t *testing.T) {
	fr := &fakeRunner{dir: t.TempDir()}
	fr.body = func(n string) []byte {
		var d map[string]any
		_ = json.Unmarshal(dumpFor(t, SourceChatGPT, n), &d)
		delete(d["details"].(map[string]any), "6a1f0c2e-1111-4a2b-9c3d-000000000002")
		b, _ := json.Marshal(d)
		return b
	}
	r := chromeChatGPT(t, errChannel(ErrExtensionNotConnected), fr)
	convs, err := r.Read(context.Background(), Query{Source: SourceChatGPT, Mode: ModeSearch, Terms: []string{"sourdough"}}, Options{})
	if err != nil || len(convs) != 0 {
		t.Fatalf("search past the fetched details: %+v %v", convs, err)
	}
}

func TestClaudeChromeErrorCodes(t *testing.T) {
	cases := []struct {
		reply string
		err   error
		kind  error
		want  string
	}{
		{reply: "not_logged_in", kind: ErrNotLoggedIn, want: "source unavailable: chatgpt: not logged in to chatgpt.com in Chrome"},
		{reply: "`not_logged_in`", kind: ErrNotLoggedIn, want: "source unavailable: chatgpt: not logged in to chatgpt.com in Chrome"},
		{reply: "endpoint_changed", kind: ErrEndpointChanged, want: "source unavailable: chatgpt: chatgpt.com changed its API"},
		{reply: "blocked", kind: ErrEndpointChanged, want: "source unavailable: chatgpt: chatgpt.com changed its API (blocked)"},
		{reply: "http_500", kind: ErrSourceFailed, want: "source unavailable: chatgpt: chatgpt.com request failed (HTTP 500)"},
		{reply: "http_429", kind: ErrSourceFailed, want: "source unavailable: chatgpt: chatgpt.com request failed (rate limited, HTTP 429)"},
		{reply: "network", kind: ErrSourceFailed, want: "source unavailable: chatgpt: chatgpt.com request failed (network error)"},
		{reply: "script_error", kind: ErrClaudeChrome, want: "source unavailable: chatgpt: Claude in Chrome could not run the read (the read script failed in the page)"},
		{reply: "I could not connect to the browser extension.", kind: ErrClaudeChrome, want: "source unavailable: chatgpt: Claude in Chrome could not run the read (unexpected answer from Claude in Chrome)"},
		{reply: "weird_code", kind: ErrClaudeChrome, want: "source unavailable: chatgpt: Claude in Chrome could not run the read (unexpected answer from Claude in Chrome)"},
		{err: errors.New("claude exited: exit status 1"), kind: ErrClaudeChrome, want: "source unavailable: chatgpt: Claude in Chrome could not run the read (claude exited: exit status 1)"},
		{err: context.DeadlineExceeded, kind: ErrTimeout, want: "source unavailable: chatgpt: Chrome did not answer in time"},
		{reply: "not_found", kind: ErrNotFound, want: "chatgpt: conversation not found"},
	}
	for _, c := range cases {
		fr := &fakeRunner{dir: t.TempDir(), reply: c.reply, err: c.err}
		r := chromeChatGPT(t, errChannel(ErrExtensionNotConnected), fr)
		_, err := r.Read(context.Background(), Query{Source: SourceChatGPT, Mode: ModeLatest}, Options{})
		if !errors.Is(err, c.kind) || err.Error() != c.want {
			t.Errorf("reply %q err %v:\n got %v\nwant %s", c.reply, c.err, err, c.want)
		}
	}
	// Saved, but the file never arrives.
	fr := &fakeRunner{dir: t.TempDir()}
	r := chromeChatGPT(t, errChannel(ErrExtensionNotConnected), fr)
	r.Chrome.Wait = 50 * time.Millisecond
	_, err := r.Read(context.Background(), Query{Source: SourceChatGPT, Mode: ModeLatest}, Options{})
	if !errors.Is(err, ErrClaudeChrome) || err.Error() != "source unavailable: chatgpt: Claude in Chrome could not run the read (the result file did not appear in the Chrome download folder)" {
		t.Fatalf("missing file: %v", err)
	}
}

func TestClaudeChromeDeletesFileOnParseError(t *testing.T) {
	for name, body := range map[string]func(string) []byte{
		"garbage":     func(string) []byte { return []byte("not json {") },
		"wrong nonce": func(string) []byte { return dumpFor(t, SourceChatGPT, strings.Repeat("f", 32)) },
		"wrong src":   func(n string) []byte { return dumpFor(t, SourceClaudeAI, n) },
	} {
		fr := &fakeRunner{dir: t.TempDir(), body: body}
		r := chromeChatGPT(t, errChannel(ErrExtensionNotConnected), fr)
		_, err := r.Read(context.Background(), Query{Source: SourceChatGPT, Mode: ModeLatest}, Options{})
		if !errors.Is(err, ErrClaudeChrome) || !strings.Contains(err.Error(), "unreadable result file") {
			t.Errorf("%s: err = %v", name, err)
		}
		assertEmptyDir(t, fr.dir)
	}
	// Over the size cap: refused and deleted.
	fr := &fakeRunner{dir: t.TempDir(), body: func(n string) []byte { return dumpFor(t, SourceChatGPT, n) }}
	r := chromeChatGPT(t, errChannel(ErrExtensionNotConnected), fr)
	r.Chrome.MaxFile = 100
	if _, err := r.Read(context.Background(), Query{Source: SourceChatGPT, Mode: ModeLatest}, Options{}); !errors.Is(err, ErrClaudeChrome) || !strings.Contains(err.Error(), "over 100 bytes") {
		t.Fatalf("oversize: %v", err)
	}
	assertEmptyDir(t, fr.dir)
}

// Only tincan-history-<nonce>.json is read: another run's file and
// Chrome's in-progress .crdownload are left alone, and the read waits
// for the finished file.
func TestClaudeChromeReadsOnlyTheNonceFile(t *testing.T) {
	dir := t.TempDir()
	other := filepath.Join(dir, chromeFileName(strings.Repeat("e", 32)))
	if err := os.WriteFile(other, dumpFor(t, SourceChatGPT, strings.Repeat("e", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(dir, "tincan-history.json")
	if err := os.WriteFile(unrelated, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	fr := &fakeRunner{dir: dir}
	var wg sync.WaitGroup
	fr.body = nil
	runner := &delayedRunner{fakeRunner: fr, write: func(nonce string) {
		final := filepath.Join(dir, chromeFileName(nonce))
		partial := final + ".crdownload"
		_ = os.WriteFile(partial, []byte("partial"), 0o600)
		wg.Go(func() {
			time.Sleep(100 * time.Millisecond)
			_ = os.WriteFile(partial, dumpFor(t, SourceChatGPT, nonce), 0o600)
			_ = os.Rename(partial, final)
		})
	}}
	r := newTestChatGPT(errChannel(ErrExtensionNotConnected))
	r.Chrome = newChromeRoute(t, runner, dir)
	convs, err := r.Read(context.Background(), Query{Source: SourceChatGPT, Mode: ModeLatest}, Options{})
	wg.Wait()
	if err != nil || len(convs) != 1 || convs[0].Title != "Fox logo sketch" {
		t.Fatalf("read %+v %v", convs, err)
	}
	ents, _ := os.ReadDir(dir)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	if want := []string{filepath.Base(other), filepath.Base(unrelated)}; !slices.Equal(names, want) {
		t.Fatalf("download dir now %v, want only the untouched files %v", names, want)
	}
}

type delayedRunner struct {
	*fakeRunner
	write func(nonce string)
}

func (d *delayedRunner) RunScript(ctx context.Context, origin, script string) (string, error) {
	if m := nonceInScript.FindStringSubmatch(script); m != nil {
		d.write(m[1])
	}
	return d.fakeRunner.RunScript(ctx, origin, script)
}

func TestLiveRouteSelection(t *testing.T) {
	q := Query{Source: SourceChatGPT, Mode: ModeLatest}
	// 1. Extension connected: claude is never run.
	fr := &fakeRunner{dir: t.TempDir(), body: func(n string) []byte { return dumpFor(t, SourceChatGPT, n) }}
	r := chromeChatGPT(t, chatgptFake(t), fr)
	if _, err := r.Read(context.Background(), q, Options{}); err != nil {
		t.Fatal(err)
	}
	if fr.runs() != 0 {
		t.Fatal("claude-chrome ran while the extension was connected")
	}
	// 2. Extension not connected, claude available: claude-chrome answers.
	r = chromeChatGPT(t, errChannel(ErrExtensionNotConnected), fr)
	if convs, err := r.Read(context.Background(), q, Options{}); err != nil || len(convs) != 1 {
		t.Fatalf("fallback: %+v %v", convs, err)
	}
	if fr.runs() != 1 {
		t.Fatalf("claude-chrome runs = %d", fr.runs())
	}
	// 3. Neither: the not-connected message, naming Claude in Chrome.
	absent := &fakeRunner{dir: t.TempDir(), absent: true}
	r = chromeChatGPT(t, errChannel(ErrExtensionNotConnected), absent)
	_, err := r.Read(context.Background(), q, Options{})
	if !errors.Is(err, ErrExtensionNotConnected) || err.Error() != notConnectedMsg || absent.runs() != 0 {
		t.Fatalf("neither route: %v", err)
	}
	r.Chrome = nil
	if _, err := r.List(context.Background(), 5, Options{}); err == nil || err.Error() != notConnectedMsg {
		t.Fatalf("no chrome route: %v", err)
	}
	// Chrome not running is not a reason to try Claude in Chrome.
	r = chromeChatGPT(t, errChannel(ErrChromeNotRunning), fr)
	if _, err := r.Read(context.Background(), q, Options{}); !errors.Is(err, ErrChromeNotRunning) || fr.runs() != 1 {
		t.Fatalf("chrome not running: %v (runs %d)", err, fr.runs())
	}
	// Invalid queries fail before either route.
	r = chromeChatGPT(t, errChannel(ErrExtensionNotConnected), fr)
	if _, err := r.Read(context.Background(), Query{Source: SourceChatGPT, Mode: ModeConversation, ConversationID: "a.b"}, Options{}); err == nil || fr.runs() != 1 {
		t.Fatalf("invalid id: %v (runs %d)", err, fr.runs())
	}
}

func TestParseClaudeOutput(t *testing.T) {
	if got, err := parseClaudeOutput([]byte(`{"type":"result","subtype":"success","is_error":false,"result":"saved","session_id":"x"}`)); err != nil || got != "saved" {
		t.Fatalf("object: %q %v", got, err)
	}
	if got, err := parseClaudeOutput([]byte(`[{"type":"system"},{"type":"assistant"},{"type":"result","is_error":false,"result":"not_logged_in"}]`)); err != nil || got != "not_logged_in" {
		t.Fatalf("array: %q %v", got, err)
	}
	if _, err := parseClaudeOutput([]byte(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"Invalid API key"}`)); err == nil || !strings.Contains(err.Error(), "Invalid API key") {
		t.Fatalf("is_error: %v", err)
	}
	for _, s := range []string{"", "saved", "{}", "[]", `{"type":"assistant"}`} {
		if _, err := parseClaudeOutput([]byte(s)); !errors.Is(err, errNoClaudeResult) {
			t.Errorf("%q: %v", s, err)
		}
	}
}

func TestChromeDownloadDir(t *testing.T) {
	home := t.TempDir()
	if got := chromeDownloadDir("darwin", home); got != filepath.Join(home, "Downloads") {
		t.Fatalf("default %s", got)
	}
	prefs := filepath.Join(home, "Library", "Application Support", "Google", "Chrome", "Default", "Preferences")
	if err := os.MkdirAll(filepath.Dir(prefs), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prefs, []byte(`{"download":{"default_directory":"/Volumes/Data/dl/","prompt_for_download":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := chromeDownloadDir("darwin", home); got != "/Volumes/Data/dl" {
		t.Fatalf("from preferences %s", got)
	}
	if err := os.WriteFile(prefs, []byte(`{"download":{"default_directory":"relative/dir"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := chromeDownloadDir("darwin", home); got != filepath.Join(home, "Downloads") {
		t.Fatalf("relative preference not ignored: %s", got)
	}
}

func TestClaudeChromeEnv(t *testing.T) {
	got := claudeChromeEnv([]string{"PATH=/bin", "ANTHROPIC_API_KEY=sk", "TINCAN_TOKEN=t", "CLAUDECODE=1", "HOME=/h", "ANTHROPIC_API_KEY_X=keep"})
	if want := []string{"PATH=/bin", "HOME=/h", "ANTHROPIC_API_KEY_X=keep"}; !slices.Equal(got, want) {
		t.Fatalf("env %v", got)
	}
}

// fakeClaude is a stand-in claude on PATH. It records its argv, stdin,
// working directory and environment, checks its working directory is
// empty, then plays the browser: it writes the result file for the nonce
// in the script into the download dir, and prints claude's JSON result.
const fakeClaude = `#!/bin/sh
rec="$FAKE_CLAUDE_RECORD"
for a in "$@"; do printf '%s\n' "$a"; done > "$rec/argv"
cat > "$rec/stdin"
pwd -P > "$rec/cwd"
ls -A > "$rec/cwd-contents"
env > "$rec/env"
nonce=$(grep -o '"nonce":"[0-9a-f]*"' "$rec/stdin" | head -n 1 | cut -d'"' -f4)
sed "s/NONCE_HERE/$nonce/" "$rec/dump" > "$FAKE_CLAUDE_DOWNLOADS/tincan-history-$nonce.json.crdownload"
mv "$FAKE_CLAUDE_DOWNLOADS/tincan-history-$nonce.json.crdownload" "$FAKE_CLAUDE_DOWNLOADS/tincan-history-$nonce.json"
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"saved","num_turns":6}'
`

func TestClaudeRunnerWithFakeClaude(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake")
	}
	bin, rec, dl, scratch := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(fakeClaude), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rec, "dump"), dumpFor(t, SourceChatGPT, "NONCE_HERE"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	t.Setenv("FAKE_CLAUDE_RECORD", rec)
	t.Setenv("FAKE_CLAUDE_DOWNLOADS", dl)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-must-not-pass")
	t.Setenv("TINCAN_TOKEN", "relay-token-must-not-pass")

	runner := &ClaudeRunner{ScratchDir: scratch, Timeout: 20 * time.Second}
	if !runner.Available() {
		t.Fatal("fake claude on PATH not found")
	}
	r := newTestChatGPT(errChannel(ErrExtensionNotConnected))
	r.Chrome = &ClaudeChrome{Runner: runner, DownloadDir: func() (string, error) { return dl, nil }, Wait: 5 * time.Second}
	convs, err := r.Read(context.Background(), Query{Source: SourceChatGPT, Mode: ModeLatest, WantImages: true}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(convs) != 1 || convs[0].Title != "Fox logo sketch" || len(imageSHAs(convs[0], RoleUser)) != 1 {
		t.Fatalf("read %+v", convs)
	}

	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(rec, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	wantArgv := "-p\n--chrome\n--output-format\njson\n--allowedTools\n" +
		"mcp__claude-in-chrome__tabs_context_mcp,mcp__claude-in-chrome__tabs_create_mcp,mcp__claude-in-chrome__navigate,mcp__claude-in-chrome__javascript_tool,mcp__claude-in-chrome__tabs_close_mcp\n"
	if got := read("argv"); got != wantArgv {
		t.Fatalf("argv:\n%s\nwant:\n%s", got, wantArgv)
	}
	stdin := read("stdin")
	script, _ := chromeScript(SourceChatGPT, nonceInScript.FindStringSubmatch(stdin)[1], chromePlan{List: 50, Details: 1, Images: MaxImages})
	for _, want := range []string{"Open a new tab in your tab group", "Navigate that tab to https://chatgpt.com/", "Run exactly the JavaScript below in that tab with javascript_tool", "Close the tab", "Reply with only the value the script returned", script} {
		if !strings.Contains(stdin, want) {
			t.Fatalf("prompt missing %q:\n%s", want, stdin)
		}
	}
	env := read("env")
	if strings.Contains(env, "ANTHROPIC_API_KEY") || strings.Contains(env, "TINCAN_") {
		t.Fatalf("claude env carries a key or tincan variable:\n%s", env)
	}
	if !strings.Contains(env, "FAKE_CLAUDE_RECORD=") {
		t.Fatal("ordinary environment not passed")
	}
	realScratch, _ := filepath.EvalSymlinks(scratch)
	cwd := strings.TrimSpace(read("cwd"))
	if filepath.Dir(cwd) != realScratch || !strings.HasPrefix(filepath.Base(cwd), "chrome-") {
		t.Fatalf("cwd %s, want a fresh dir under %s", cwd, realScratch)
	}
	if strings.TrimSpace(read("cwd-contents")) != "" {
		t.Fatalf("cwd not empty: %q", read("cwd-contents"))
	}
	if _, err := os.Stat(cwd); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("scratch cwd not removed")
	}
	assertEmptyDir(t, dl)

	t.Setenv("PATH", t.TempDir())
	if runner.Available() {
		t.Fatal("claude found on a PATH without it")
	}
}
