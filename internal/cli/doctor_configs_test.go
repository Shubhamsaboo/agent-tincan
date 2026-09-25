package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mvanhorn/agent-tincan/internal/client"
	"github.com/mvanhorn/agent-tincan/internal/relay"
	"github.com/mvanhorn/agent-tincan/internal/testrelay"
)

// Hermes keeps its MCP servers in config.yaml. doctor reads the flat shape
// Hermes documents, with args as a flow or a block list, and a disabled
// server as such.
func TestYamlServers(t *testing.T) {
	es := yamlServers(`
# Hermes config
model: gpt
mcp_servers:
  agent-tincan:
    command: "tincan"   # on PATH
    args: ["mcp"]
    enabled: true
    env:
      TINCAN_CONFIG: "/Users/hermes/.hermes/tincan-hermes.json"
  'old tincan':
    command: /usr/local/bin/tincan
    args:
      - mcp
      - --channel
    enabled: false
  github:
    command: gh
other: true
`)
	if len(es) != 2 {
		t.Fatalf("got %d entries %+v, want the two tincan servers", len(es), es)
	}
	if es[0].Name != "agent-tincan" || es[0].Command != "tincan" || strings.Join(es[0].Args, " ") != "mcp" || es[0].broken {
		t.Fatalf("first entry %+v", es[0])
	}
	if es[1].Name != "old tincan" || es[1].Command != "/usr/local/bin/tincan" || strings.Join(es[1].Args, " ") != "mcp --channel" || !es[1].broken {
		t.Fatalf("second entry %+v, want the block-list args and disabled", es[1])
	}
}

// A YAML config passed with --config (or the Hermes one) is read as YAML,
// not judged as broken JSON.
func TestFindMCPConfigsReadsYAML(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "tincan")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("mcp_servers:\n  agent-tincan:\n    command: "+exe+"\n    args: [\"mcp\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var mine []mcpConfigEntry
	for _, e := range findMCPConfigs([]string{cfg}) {
		if e.File == cfg {
			mine = append(mine, e)
		}
	}
	if len(mine) != 1 || mine[0].Name != "agent-tincan" || mine[0].broken {
		t.Fatalf("yaml entries = %+v", mine)
	}
	if c := configCheck(mine, exe); c.Status != "ok" {
		t.Fatalf("yaml entry: got %+v", c)
	}
}

// Claude Code keeps a server map per project in ~/.claude.json. A server
// added in two directories lives in two maps that never load together, so
// it is not a duplicate; a server in the map loaded everywhere plus one in
// a project's map is.
func TestConfigDuplicatesCountPerScope(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "tincan")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	entry := `{"command":"` + exe + `","args":["mcp","--channel"]}`
	twoProjects := filepath.Join(dir, "two-projects.json")
	if err := os.WriteFile(twoProjects, []byte(`{"projects":{"/a":{"mcpServers":{"agent-tincan":`+entry+`}},"/b":{"mcpServers":{"agent-tincan":`+entry+`}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	userAndProject := filepath.Join(dir, "user-and-project.json")
	if err := os.WriteFile(userAndProject, []byte(`{"mcpServers":{"tincan":`+entry+`},"projects":{"/a":{"mcpServers":{"agent-tincan":`+entry+`}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	byFile := map[string][]mcpConfigEntry{}
	for _, e := range findMCPConfigs([]string{twoProjects, userAndProject}) {
		byFile[e.File] = append(byFile[e.File], e)
	}
	es := byFile[twoProjects]
	if len(es) != 2 || es[0].broken || es[1].broken {
		t.Fatalf("two project entries judged: %+v", es)
	}
	scopes := []string{es[0].Scope, es[1].Scope}
	if (scopes[0] != "projects./a" || scopes[1] != "projects./b") && (scopes[0] != "projects./b" || scopes[1] != "projects./a") {
		t.Fatalf("scopes = %v", scopes)
	}
	if c := configCheck(es, exe); c.Status != "ok" {
		t.Fatalf("two projects: got %+v", c)
	}
	es = byFile[userAndProject]
	if len(es) != 2 || !es[0].broken || !es[1].broken || !strings.Contains(strings.Join(es[0].Problems, " "), "one of 2") {
		t.Fatalf("user plus project entries judged: %+v", es)
	}
}

// With TINCAN_RELAY set the client never writes the config file, so the
// key the relay hands out is not saved. doctor used to blame an old relay
// for that; it says what is going on instead.
func TestDoctorRelayMovesUnderTincanRelayOverride(t *testing.T) {
	m := testrelay.New(t, relay.Config{})
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	relayMoves := func() check {
		t.Helper()
		for _, c := range runDoctor(context.Background(), "", nil).Checks {
			if c.Name == "relay moves" {
				return c
			}
		}
		t.Fatal("no relay moves check")
		return check{}
	}
	useConfig(t, client.Config{Relay: m.URL("grokbot"), Agent: "grokbot"})
	if c := relayMoves(); c.Status != "ok" {
		t.Fatalf("with a saved config: %+v", c)
	}
	useConfig(t, client.Config{Relay: m.URL("grokbot"), Agent: "grokbot"})
	t.Setenv("TINCAN_RELAY", m.URL("grokbot"))
	c := relayMoves()
	if c.Status != "warn" || !strings.Contains(c.Detail, "TINCAN_RELAY") || strings.Contains(c.Detail, "older than") || !strings.Contains(c.Fix, "rejoin") {
		t.Fatalf("under TINCAN_RELAY: %+v", c)
	}
}
