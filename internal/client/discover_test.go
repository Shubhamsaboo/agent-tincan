package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// fakeRelay answers hello with a proof under key, and agents.
func fakeRelay(t *testing.T, key string) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/hello":
			_ = json.NewEncoder(w).Encode(map[string]string{"service": HelloService, "proof": HelloProof(key, r.URL.Query().Get("nonce"))})
		case "/v1/whoami":
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "muse", "relay_key": key, "relay_urls": []string{"http://tincan-relay.example.ts.net"}})
		case "/v1/agents":
			_ = json.NewEncoder(w).Encode(map[string]any{"agents": []AgentInfo{{Name: "muse"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	return ts.URL
}

// deadURL is an address where nothing listens any more.
func deadURL(t *testing.T) string {
	ts := httptest.NewServer(http.NotFoundHandler())
	u := ts.URL
	ts.Close()
	return u
}

func savedConfig(t *testing.T, c Config) {
	t.Helper()
	t.Setenv("TINCAN_CONFIG", filepath.Join(t.TempDir(), "client.json"))
	t.Setenv("TINCAN_RELAY", "")
	if err := SaveConfig(c); err != nil {
		t.Fatal(err)
	}
}

func TestRelayMovedIsFoundAndSaved(t *testing.T) {
	const key = "k-real"
	old := deadURL(t)
	moved := fakeRelay(t, key)
	impostor := fakeRelay(t, "k-other")
	savedConfig(t, Config{Relay: old, Agent: "muse", RelayKey: key})
	r, err := NewRelayFor(Config{Relay: old, Agent: "muse", RelayKey: key})
	if err != nil {
		t.Fatal(err)
	}
	r.findRelays = func(context.Context, string) []string { return []string{impostor, moved} }

	agents, err := r.Agents(t.Context())
	if err != nil || len(agents) != 1 {
		t.Fatalf("call after the move: %v %v", agents, err)
	}
	if r.Base() != moved {
		t.Fatalf("base %s, want %s (the peer that proved the key, not the impostor)", r.Base(), moved)
	}
	c, err := LoadConfig()
	if err != nil || c.Relay != moved || c.RelayKey != key {
		t.Fatalf("saved config %+v %v", c, err)
	}
}

func TestRelayNotFollowedWithoutProof(t *testing.T) {
	old := deadURL(t)
	impostor := fakeRelay(t, "k-other")
	savedConfig(t, Config{Relay: old, RelayKey: "k-real"})
	r, _ := NewRelayFor(Config{Relay: old, RelayKey: "k-real"})
	r.findRelays = func(context.Context, string) []string { return []string{impostor} }
	if _, err := r.Agents(t.Context()); err == nil {
		t.Fatal("followed a peer that cannot prove the relay key")
	}
	if r.Base() != old {
		t.Fatalf("base changed to %s", r.Base())
	}
}

func TestRelayNotSearchedWithoutKey(t *testing.T) {
	old := deadURL(t)
	savedConfig(t, Config{Relay: old})
	r, _ := NewRelayFor(Config{Relay: old})
	searched := false
	r.findRelays = func(context.Context, string) []string { searched = true; return nil }
	_, _ = r.Agents(t.Context())
	if searched {
		t.Fatal("searched the tailnet without a relay key")
	}
}

func TestRelayErrorIsNotAMove(t *testing.T) {
	if unreachable(&APIError{Code: 403, Message: "not a joined agent"}) {
		t.Fatal("an answer from the relay is not a move")
	}
}

func TestLearnRelayKeySavesIt(t *testing.T) {
	url := fakeRelay(t, "k-learned")
	savedConfig(t, Config{Relay: url, Agent: "muse"})
	r, _ := NewRelayFor(Config{Relay: url, Agent: "muse"})
	LearnRelayKey(t.Context(), r)
	raw, _ := os.ReadFile(ConfigPath())
	var c Config
	_ = json.Unmarshal(raw, &c)
	if c.RelayKey != "k-learned" || c.Relay != url {
		t.Fatalf("config %+v", c)
	}
}

func TestRelayFoundAtItsAdvertisedNameWithoutTailscale(t *testing.T) {
	const key = "k-real"
	old := deadURL(t)
	named := fakeRelay(t, key)
	savedConfig(t, Config{Relay: old, RelayKey: key, RelayURLs: []string{named}})
	r, _ := NewRelayFor(Config{Relay: old, RelayKey: key, RelayURLs: []string{named}})
	r.findRelays = func(context.Context, string) []string { return nil } // a proxy-only sandbox
	if _, err := r.Agents(t.Context()); err != nil {
		t.Fatal(err)
	}
	if r.Base() != named {
		t.Fatalf("base %s, want the advertised %s", r.Base(), named)
	}
}

func TestProxyGatewayErrorsMeanUnreachable(t *testing.T) {
	for _, code := range []int{http.StatusBadGateway, http.StatusGatewayTimeout} {
		if !unreachable(&APIError{Code: code}) {
			t.Errorf("%d through a proxy should count as the relay not answering", code)
		}
	}
}

func TestLearnRelayInfoSavesURLsAndRefreshes(t *testing.T) {
	url := fakeRelay(t, "k")
	savedConfig(t, Config{Relay: url})
	r, _ := NewRelayFor(Config{Relay: url})
	LearnRelayKey(t.Context(), r)
	c, _ := LoadConfig()
	if c.RelayKey != "k" || c.RelayInfoAt.IsZero() || NeedsRelayInfo(c) {
		t.Fatalf("config %+v", c)
	}
	c.RelayInfoAt = c.RelayInfoAt.Add(-2 * relayInfoEvery)
	if !NeedsRelayInfo(c) {
		t.Fatal("day-old relay info should be refreshed")
	}
}

// readConfig reads a config file as saved, with no environment overrides.
func readConfig(t *testing.T, path string) Config {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// A service loads its config from --config, not ConfigPath(). The key it
// learns and the address of a moved relay go to that file, and the default
// config (another agent's) is never touched.
func TestNewRelayForFileWritesToItsOwnFile(t *testing.T) {
	const key = "k-svc"
	url := fakeRelay(t, key)
	savedConfig(t, Config{Relay: url, Agent: "codex"}) // ConfigPath(): another agent
	own := filepath.Join(t.TempDir(), "history.json")
	if err := SaveConfigTo(own, Config{Relay: url, Agent: "history"}); err != nil {
		t.Fatal(err)
	}
	r, err := NewRelayForFile(Config{Relay: url, Agent: "history"}, own)
	if err != nil {
		t.Fatal(err)
	}
	LearnRelayKey(t.Context(), r)
	if r.key != key {
		t.Fatalf("key learned %q", r.key)
	}
	if c := readConfig(t, own); c.RelayKey != key || c.Agent != "history" || len(c.RelayURLs) == 0 || c.RelayInfoAt.IsZero() {
		t.Fatalf("own config %+v", c)
	}
	if c := readConfig(t, ConfigPath()); c.RelayKey != "" || c.Agent != "codex" {
		t.Fatalf("ConfigPath() was written: %+v", c)
	}

	// The relay moves. Both files name the old address, so the old code
	// (which always wrote ConfigPath()) would have rewritten the wrong one.
	old := deadURL(t)
	if err := SaveConfig(Config{Relay: old, Agent: "codex"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveConfigTo(own, Config{Relay: old, Agent: "history", RelayKey: key}); err != nil {
		t.Fatal(err)
	}
	r, _ = NewRelayForFile(Config{Relay: old, Agent: "history", RelayKey: key}, own)
	r.findRelays = func(context.Context, string) []string { return []string{url} }
	if _, err := r.Agents(t.Context()); err != nil {
		t.Fatal(err)
	}
	if c := readConfig(t, own); c.Relay != url || c.RelayKey != key {
		t.Fatalf("own config after the move %+v", c)
	}
	if c := readConfig(t, ConfigPath()); c.Relay != old {
		t.Fatalf("ConfigPath() followed the move for another agent: %+v", c)
	}
}

// TINCAN_RELAY overrides the saved relay for one process only: nothing is
// written to any file, whichever constructor built the client.
func TestEnvRelayOverrideNeverWrites(t *testing.T) {
	const key = "k-env"
	url := fakeRelay(t, key)
	savedConfig(t, Config{Relay: url, Agent: "muse"})
	own := filepath.Join(t.TempDir(), "history.json")
	if err := SaveConfigTo(own, Config{Relay: url, Agent: "history"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TINCAN_RELAY", url)
	for name, build := range map[string]func() (*Relay, error){
		"NewRelayFor":     func() (*Relay, error) { return NewRelayFor(Config{Relay: url, Agent: "muse"}) },
		"NewRelayForFile": func() (*Relay, error) { return NewRelayForFile(Config{Relay: url, Agent: "history"}, own) },
	} {
		r, err := build()
		if err != nil {
			t.Fatal(err)
		}
		LearnRelayKey(t.Context(), r)
		if r.key != key {
			t.Fatalf("%s: the key is still handed out under TINCAN_RELAY, got %q", name, r.key)
		}
		if c := readConfig(t, ConfigPath()); c.RelayKey != "" {
			t.Fatalf("%s: ConfigPath() written under TINCAN_RELAY: %+v", name, c)
		}
		if c := readConfig(t, own); c.RelayKey != "" {
			t.Fatalf("%s: --config file written under TINCAN_RELAY: %+v", name, c)
		}
	}

	// A move is followed in memory but not written either.
	old := deadURL(t)
	t.Setenv("TINCAN_RELAY", old)
	if err := SaveConfigTo(own, Config{Relay: old, Agent: "history", RelayKey: key}); err != nil {
		t.Fatal(err)
	}
	r, _ := NewRelayForFile(Config{Relay: old, Agent: "history", RelayKey: key}, own)
	r.findRelays = func(context.Context, string) []string { return []string{url} }
	if _, err := r.Agents(t.Context()); err != nil || r.Base() != url {
		t.Fatalf("move: %v, base %s", err, r.Base())
	}
	if c := readConfig(t, own); c.Relay != old {
		t.Fatalf("--config file rewritten under TINCAN_RELAY: %+v", c)
	}
}
