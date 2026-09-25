package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/mvanhorn/agent-tincan/internal/identity"
)

// An agents table from before version tracking gains a version column on
// open. A version set on an agent survives a reopen and a rejoin that
// rewrites its row, an unknown name is ignored, and "" clears it.
func TestAgentVersionMigratesAndSurvivesRejoin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE agents (name TEXT PRIMARY KEY, node_id TEXT NOT NULL, node_name TEXT NOT NULL, joined_at INTEGER NOT NULL, kind TEXT, node_user TEXT, last_seen_at INTEGER)`,
		`INSERT INTO agents VALUES ('grokbot', 'nGROK', 'grok-bot', 1790000000000, 'webhook', NULL, NULL)`,
	} {
		if _, err := old.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	old.Close()

	s, c := open(t, path)
	ctx := context.Background()
	if vs, err := s.AgentVersions(ctx); err != nil || len(vs) != 0 {
		t.Fatalf("versions after migration = %v, %v", vs, err)
	}
	if err := s.SetAgentVersion(ctx, "grokbot", "0.5.2"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAgentVersion(ctx, "nobody", "0.5.2"); err != nil {
		t.Fatalf("unknown agent: %v", err)
	}
	if vs, _ := s.AgentVersions(ctx); len(vs) != 1 || vs["grokbot"] != "0.5.2" {
		t.Fatalf("versions = %v", vs)
	}
	// A rejoin rewrites the agent row; the build carries over like last_seen_at.
	if err := s.PutAgent(ctx, identity.Agent{Name: "grokbot", NodeID: "nGROK2", NodeName: "grok-bot-1", JoinedAt: c.t, Kind: "webhook"}); err != nil {
		t.Fatal(err)
	}
	if vs, _ := s.AgentVersions(ctx); vs["grokbot"] != "0.5.2" {
		t.Fatalf("versions after rejoin = %v", vs)
	}
	s.Close()
	s2, _ := open(t, path)
	if vs, err := s2.AgentVersions(ctx); err != nil || vs["grokbot"] != "0.5.2" {
		t.Fatalf("versions after reopen = %v, %v", vs, err)
	}
	if err := s2.SetAgentVersion(ctx, "grokbot", ""); err != nil {
		t.Fatal(err)
	}
	if vs, _ := s2.AgentVersions(ctx); len(vs) != 0 {
		t.Fatalf("versions after clearing = %v", vs)
	}
}
