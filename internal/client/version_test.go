package client_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mvanhorn/agent-tincan/internal/client"
)

// Every relay call names the build this client runs, once main has set
// client.Version; a program that never sets it sends no header. The roster
// decodes the relay's own build alongside the agents.
func TestClientSendsItsVersion(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"agents": []client.AgentInfo{{Name: "muse", Version: "0.5.1"}}, "relay_version": "0.5.2",
		})
	}))
	defer srv.Close()

	t.Cleanup(func() { client.Version = "" })
	client.Version = "0.5.2"
	r, err := client.NewRelayFor(client.Config{Relay: srv.URL, Agent: "grokbot"})
	if err != nil {
		t.Fatal(err)
	}
	ro, err := r.Roster(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Get(client.VersionHeader) != "0.5.2" || got.Get(client.AgentHeader) != "grokbot" {
		t.Fatalf("headers = %v", got)
	}
	if ro.RelayVersion != "0.5.2" || len(ro.Agents) != 1 || ro.Agents[0].Version != "0.5.1" {
		t.Fatalf("roster = %+v", ro)
	}

	client.Version = ""
	r, _ = client.NewRelayFor(client.Config{Relay: srv.URL, Agent: "grokbot"})
	if _, err := r.Agents(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := got[client.VersionHeader]; ok {
		t.Fatalf("a client without a version sent %q", got.Get(client.VersionHeader))
	}
}
