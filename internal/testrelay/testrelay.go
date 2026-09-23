// Package testrelay runs a real relay behind one httptest server per agent,
// each of which pins the caller's tailnet address, so client-side tests can
// exercise the full HTTP path without a tailnet. Test-only.
package testrelay

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mvanhorn/agent-tincan/internal/client"
	"github.com/mvanhorn/agent-tincan/internal/identity"
	"github.com/mvanhorn/agent-tincan/internal/identity/identitytest"
	"github.com/mvanhorn/agent-tincan/internal/policy"
	"github.com/mvanhorn/agent-tincan/internal/relay"
	"github.com/mvanhorn/agent-tincan/internal/store"
)

// Mesh is a relay with grokbot, instinct, and muse joined.
type Mesh struct {
	Server *relay.Server
	Store  *store.Store
	urls   map[string]string
}

var addrs = map[string]string{"grokbot": "100.0.0.2:1", "instinct": "100.0.0.3:1", "muse": "100.0.0.4:1", "admin": "100.0.0.1:1"}

// New starts the mesh.
func New(t *testing.T, cfg relay.Config) *Mesh {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	who := identitytest.New(map[string]identity.Node{
		addrs["admin"]:    {ID: "nMAC", Name: "macbook-pro-44"},
		addrs["grokbot"]:  {ID: "nGROK", Name: "grok-bot"},
		addrs["instinct"]: {ID: "nINST", Name: "instinct"},
		addrs["muse"]:     {ID: "nMUSE", Name: "muse"},
	})
	dir := identity.NewDirectory(st, who, identity.Config{Admins: []string{"macbook-pro-44"}})
	srv := relay.New(dir, st, cfg)
	srv.SetPreparer(policy.New(st, policy.Config{}))
	m := &Mesh{Server: srv, Store: st, urls: map[string]string{}}
	h := srv.Handler()
	for name, addr := range addrs {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.RemoteAddr = addr
			h.ServeHTTP(w, r)
		}))
		t.Cleanup(ts.Close)
		m.urls[name] = ts.URL
	}
	admin := m.Client(t, "admin")
	for _, name := range []string{"grokbot", "instinct", "muse"} {
		code, err := admin.Invite(t.Context(), name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.Client(t, name).Join(t.Context(), code); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

// Client returns a relay client that the relay sees as agent name.
func (m *Mesh) Client(t *testing.T, name string) *client.Relay {
	t.Helper()
	r, err := client.NewRelay(m.urls[name], "")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// URL returns the base URL that the relay attributes to agent name.
func (m *Mesh) URL(name string) string { return m.urls[name] }

// Short is a small wait used by tests.
const Short = 50 * time.Millisecond
