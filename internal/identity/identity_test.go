package identity_test

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/mvanhorn/agent-tincan/internal/identity"
	"github.com/mvanhorn/agent-tincan/internal/identity/identitytest"
)

var (
	macNode      = identity.Node{ID: "nMAC", Name: "macbook-pro-44", User: "mvanhorn@gmail.com"}
	grokNode     = identity.Node{ID: "nGROK", Name: "grok-bot", User: "mvanhorn@gmail.com"}
	instinctNode = identity.Node{ID: "nINST", Name: "e2b.local", User: "mvanhorn@gmail.com"}
	museNode     = identity.Node{ID: "nMUSE", Name: "muse", User: "mvanhorn@gmail.com"}
)

type fixture struct {
	dir   *identity.Directory
	who   *identitytest.Resolver
	clock *time.Time
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	now := time.Unix(1_790_000_000, 0)
	who := identitytest.New(map[string]identity.Node{
		"100.0.0.1:1": macNode, "100.0.0.2:1": grokNode, "100.0.0.3:1": instinctNode, "100.0.0.4:1": museNode,
	})
	dir := identity.NewDirectory(identity.NewMemoryStore(), who, identity.Config{
		Admins: []string{"macbook-pro-44"},
		Now:    func() time.Time { return now },
	})
	return fixture{dir: dir, who: who, clock: &now}
}

func (f fixture) invite(t *testing.T, name string) string {
	t.Helper()
	code, err := f.dir.Invite(context.Background(), "100.0.0.1:1", name)
	if err != nil {
		t.Fatalf("invite %s: %v", name, err)
	}
	return code
}

func TestThreeAgentsJoinAndAreAttributed(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, j := range []struct{ name, addr string }{{"grokbot", "100.0.0.2:1"}, {"instinct", "100.0.0.3:1"}, {"muse", "100.0.0.4:1"}} {
		code := f.invite(t, j.name)
		if got, err := f.dir.Join(ctx, j.addr, code); err != nil || got != j.name {
			t.Fatalf("join %s: got %q, %v", j.name, got, err)
		}
	}
	for addr, want := range map[string]string{"100.0.0.2:1": "grokbot", "100.0.0.3:1": "instinct", "100.0.0.4:1": "muse"} {
		if got, err := f.dir.Attribute(ctx, addr); err != nil || got != want {
			t.Errorf("attribute %s: got %q, %v; want %q", addr, got, err, want)
		}
	}
	agents, _ := f.dir.Agents(ctx)
	if len(agents) != 3 {
		t.Fatalf("want 3 agents, got %v", agents)
	}
}

func TestInviteCodeShape(t *testing.T) {
	code := newFixture(t).invite(t, "muse")
	if !regexp.MustCompile(`^[A-Z2-9]{4}-[A-Z2-9]{4}$`).MatchString(code) {
		t.Fatalf("code %q is not XXXX-XXXX", code)
	}
}

func TestInviteCodeIsSingleUse(t *testing.T) {
	f := newFixture(t)
	code := f.invite(t, "muse")
	if _, err := f.dir.Join(context.Background(), "100.0.0.4:1", code); err != nil {
		t.Fatal(err)
	}
	if _, err := f.dir.Join(context.Background(), "100.0.0.3:1", code); !errors.Is(err, identity.ErrBadInvite) {
		t.Fatalf("reused code: want ErrBadInvite, got %v", err)
	}
}

func TestInviteCodeExpires(t *testing.T) {
	f := newFixture(t)
	code := f.invite(t, "muse")
	*f.clock = f.clock.Add(identity.InviteTTL + time.Second)
	if _, err := f.dir.Join(context.Background(), "100.0.0.4:1", code); !errors.Is(err, identity.ErrBadInvite) {
		t.Fatalf("expired code: want ErrBadInvite, got %v", err)
	}
}

func TestUnknownCodeRejected(t *testing.T) {
	f := newFixture(t)
	if _, err := f.dir.Join(context.Background(), "100.0.0.4:1", "ABCD-EFGH"); !errors.Is(err, identity.ErrBadInvite) {
		t.Fatalf("want ErrBadInvite, got %v", err)
	}
}

// The plan's naming lesson: store the name Matt chose, never the hostname.
func TestAgentKeepsInvitedNameNotHostname(t *testing.T) {
	f := newFixture(t)
	code := f.invite(t, "instinct")
	if _, err := f.dir.Join(context.Background(), "100.0.0.3:1", code); err != nil {
		t.Fatal(err)
	}
	got, _ := f.dir.Attribute(context.Background(), "100.0.0.3:1")
	if got != "instinct" {
		t.Fatalf("attributed as %q, want instinct (host announces e2b.local)", got)
	}
}

// Every node on the tailnet shares one login, so admin rights come from an
// explicit node list, not from the login.
func TestInviteOnlyFromAdminNodes(t *testing.T) {
	f := newFixture(t)
	if _, err := f.dir.Invite(context.Background(), "100.0.0.2:1", "evil"); !errors.Is(err, identity.ErrNotAdmin) {
		t.Fatalf("invite from grok-bot: want ErrNotAdmin, got %v", err)
	}
	if err := f.dir.Remove(context.Background(), "100.0.0.4:1", "grokbot"); !errors.Is(err, identity.ErrNotAdmin) {
		t.Fatalf("remove from muse: want ErrNotAdmin, got %v", err)
	}
}

func TestLocalAdminBypassesNodeCheck(t *testing.T) {
	f := newFixture(t)
	if _, err := f.dir.Invite(context.Background(), identity.LocalAdmin, "muse"); err != nil {
		t.Fatalf("local admin invite: %v", err)
	}
}

func TestRemoveStopsAttribution(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	code := f.invite(t, "muse")
	f.dir.Join(ctx, "100.0.0.4:1", code)
	if err := f.dir.Remove(ctx, "100.0.0.1:1", "muse"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.dir.Attribute(ctx, "100.0.0.4:1"); !errors.Is(err, identity.ErrNotJoined) {
		t.Fatalf("after remove: want ErrNotJoined, got %v", err)
	}
	if err := f.dir.Remove(ctx, "100.0.0.1:1", "muse"); !errors.Is(err, identity.ErrUnknownAgent) {
		t.Fatalf("second remove: want ErrUnknownAgent, got %v", err)
	}
}

// AE5: a tailnet node that never joined is not an agent.
func TestUnjoinedNodeIsRejected(t *testing.T) {
	f := newFixture(t)
	if _, err := f.dir.Attribute(context.Background(), "100.0.0.1:1"); !errors.Is(err, identity.ErrNotJoined) {
		t.Fatalf("want ErrNotJoined, got %v", err)
	}
	if _, err := f.dir.Attribute(context.Background(), "100.9.9.9:1"); err == nil {
		t.Fatal("unknown address should fail WhoIs")
	}
}

func TestReinviteMovesAgentToNewMachine(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.dir.Join(ctx, "100.0.0.3:1", f.invite(t, "instinct"))
	f.dir.Join(ctx, "100.0.0.4:1", f.invite(t, "instinct"))
	if got, _ := f.dir.Attribute(ctx, "100.0.0.4:1"); got != "instinct" {
		t.Fatalf("new machine attributed as %q", got)
	}
	if _, err := f.dir.Attribute(ctx, "100.0.0.3:1"); !errors.Is(err, identity.ErrNotJoined) {
		t.Fatalf("old machine should no longer be instinct, got %v", err)
	}
}

func TestOneMachineOneAgent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.dir.Join(ctx, "100.0.0.4:1", f.invite(t, "muse"))
	if _, err := f.dir.Join(ctx, "100.0.0.4:1", f.invite(t, "muse2")); !errors.Is(err, identity.ErrNodeTaken) {
		t.Fatalf("second name on same node: want ErrNodeTaken, got %v", err)
	}
}

func TestInvalidNamesRejected(t *testing.T) {
	f := newFixture(t)
	for _, name := range []string{"", "Muse", "has space", "a/b", "way-too-long-name-for-an-agent-xxxxxxxx"} {
		if _, err := f.dir.Invite(context.Background(), "100.0.0.1:1", name); err == nil {
			t.Errorf("name %q should be rejected", name)
		}
	}
}

// --listen mode must refuse to start when it cannot ask tailscaled WhoIs.
func TestProbeFailsWithoutTailscaled(t *testing.T) {
	r := identity.NewLocalResolverAt(filepath.Join(t.TempDir(), "no-such-tailscaled.sock"))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := r.Probe(ctx); err == nil {
		t.Fatal("probe should fail with no reachable tailscaled")
	}
}
