package history

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// ServiceLabel is the launchd label of the history service.
const ServiceLabel = "com.agenttincan.history"

// systemdUnit is the systemd user unit name of the history service.
const systemdUnit = "tincan-history.service"

// launchdTemplate is examples/history/com.agenttincan.history.plist; a
// test keeps the two identical. Placeholders: __TINCAN_BINARY__, __HOME__,
// __PATH__, each XML-escaped when filled.
const launchdTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!--
  Agent Tincan history agent: runs "tincan history serve" under launchd,
  outside any Codex sandbox. "tincan history install" copies this file into
  ~/Library/LaunchAgents with the real paths filled in, PATH included: it
  starts with the directories of codex (the query step) and claude (the
  Claude in Chrome route, normally ~/.local/bin). It does not load it;
  start it with:
    launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.agenttincan.history.plist
  See docs/adapters/history.md.
-->
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.agenttincan.history</string>
  <key>ProgramArguments</key>
  <array>
    <string>__TINCAN_BINARY__</string>
    <string>history</string>
    <string>serve</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>TINCAN_CONFIG</key>
    <string>__HOME__/.config/tincan/history.json</string>
    <key>PATH</key>
    <string>__PATH__</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>ThrottleInterval</key>
  <integer>10</integer>
  <key>ProcessType</key>
  <string>Background</string>
  <key>StandardOutPath</key>
  <string>__HOME__/Library/Logs/tincan-history.log</string>
  <key>StandardErrorPath</key>
  <string>__HOME__/Library/Logs/tincan-history.log</string>
</dict>
</plist>
`

// systemdTemplate is the Linux user unit. %h is systemd's home specifier.
const systemdTemplate = `[Unit]
Description=Agent Tincan history agent
After=network-online.target

[Service]
ExecStart="__TINCAN_BINARY__" history serve
Environment=TINCAN_CONFIG=%h/.config/tincan/history.json
Environment=PATH=__PATH__
Restart=always
RestartSec=10

[Install]
WantedBy=default.target
`

// basePath is the service's PATH after the codex and claude directories.
const basePath = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"

// ServiceOptions controls InstallService. Zero fields take the current
// user's values.
type ServiceOptions struct {
	GOOS   string
	Home   string
	Binary string
	// CodexDir is put first on the service's PATH so it finds codex (the
	// query extractor). Default: the directory of codex on PATH, if any.
	CodexDir string
	// ClaudeDir is put on the service's PATH so it finds claude (the Claude
	// in Chrome route). Default: the directory of claude on PATH, if any.
	// ~/.local/bin, where Claude Code's installer puts claude, is always on
	// the PATH.
	ClaudeDir string
	// UID fills the printed launchctl command (default: os.Getuid()).
	UID int
}

// ServiceResult says what InstallService wrote and the command that
// starts it. InstallService never starts or loads the service itself.
type ServiceResult struct {
	Path string
	Next string
}

// InstallService writes the history service definition: a launchd agent
// on macOS, a systemd user unit on Linux. It does not load or enable it.
func InstallService(o ServiceOptions) (ServiceResult, error) {
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if o.Home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return ServiceResult{}, err
		}
		o.Home = h
	}
	if o.Binary == "" {
		b, err := os.Executable()
		if err != nil {
			return ServiceResult{}, err
		}
		o.Binary = b
	}
	if !filepath.IsAbs(o.Binary) || strings.ContainsAny(o.Binary, "\"\n\r\x00%") {
		return ServiceResult{}, fmt.Errorf("service binary %q must be an absolute path without quotes, %% or newlines", o.Binary)
	}
	if o.CodexDir == "" {
		if p, err := exec.LookPath("codex"); err == nil {
			o.CodexDir = filepath.Dir(p)
		}
	}
	if o.ClaudeDir == "" {
		if p, err := exec.LookPath("claude"); err == nil {
			o.ClaudeDir = filepath.Dir(p)
		}
	}
	if o.UID == 0 {
		o.UID = os.Getuid()
	}
	path := servicePath(o.CodexDir, o.ClaudeDir, filepath.Join(o.Home, ".local", "bin"))
	switch o.GOOS {
	case "darwin":
		dst := filepath.Join(o.Home, "Library", "LaunchAgents", ServiceLabel+".plist")
		body := strings.NewReplacer(
			"__TINCAN_BINARY__", xmlEscape(o.Binary),
			"__HOME__", xmlEscape(o.Home),
			"__PATH__", xmlEscape(path),
		).Replace(launchdTemplate)
		if err := os.MkdirAll(filepath.Join(o.Home, "Library", "Logs"), 0o755); err != nil {
			return ServiceResult{}, err
		}
		if err := writeService(dst, body); err != nil {
			return ServiceResult{}, err
		}
		return ServiceResult{Path: dst, Next: fmt.Sprintf("launchctl bootstrap gui/%d %s", o.UID, dst)}, nil
	case "linux":
		dst := filepath.Join(o.Home, ".config", "systemd", "user", systemdUnit)
		body := strings.NewReplacer("__TINCAN_BINARY__", o.Binary, "__PATH__", path).Replace(systemdTemplate)
		if err := writeService(dst, body); err != nil {
			return ServiceResult{}, err
		}
		return ServiceResult{Path: dst, Next: "systemctl --user daemon-reload && systemctl --user enable --now " + systemdUnit}, nil
	}
	return ServiceResult{}, errors.New("no history service definition for " + o.GOOS + "; run tincan history serve under your own service manager")
}

// servicePath is dirs, in order, then basePath, without duplicates or
// directories that cannot be PATH entries.
func servicePath(dirs ...string) string {
	var parts []string
	for _, d := range append(dirs, strings.Split(basePath, ":")...) {
		if d != "" && filepath.IsAbs(d) && !strings.ContainsAny(d, ":\n\r") && !slices.Contains(parts, d) {
			parts = append(parts, d)
		}
	}
	return strings.Join(parts, ":")
}

func writeService(dst, body string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(dst, []byte(body), 0o644)
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
