package policy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mvanhorn/agent-tincan/internal/envelope"
	"github.com/mvanhorn/agent-tincan/internal/store"
)

type fixture struct {
	st  *store.Store
	pol *Policy
	now time.Time
}

func newFixture(t *testing.T, cfg Config) *fixture {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &fixture{st: st, now: time.Unix(1_790_000_000, 0)}
	cfg.Now = func() time.Time { return f.now }
	f.pol = New(st, cfg)
	return f
}

// send prepares and enqueues a request the way the relay does.
func (f *fixture) send(t *testing.T, from, to, parent string) (envelope.Request, error) {
	t.Helper()
	req := envelope.Request{From: from, To: to, Kind: envelope.KindAsk, Body: "x", ParentID: parent}
	if err := f.pol.Prepare(context.Background(), &req); err != nil {
		return req, err
	}
	return f.st.Enqueue(context.Background(), req, time.Hour)
}

func (f *fixture) claim(t *testing.T, id, agent string) {
	t.Helper()
	if _, err := f.st.Claim(context.Background(), id, agent, time.Hour); err != nil {
		t.Fatal(err)
	}
}

func TestNewChain(t *testing.T) {
	f := newFixture(t, Config{})
	req, err := f.send(t, "instinct", "muse", "")
	if err != nil {
		t.Fatal(err)
	}
	if req.Hop != 1 || len(req.Chain) != 1 || req.Chain[0] != "instinct" || req.TraceID != req.ID {
		t.Fatalf("new chain = %+v", req)
	}
}

// AE3: Instinct asks Muse, Muse (holding that claim) tells Grok Bot.
func TestChainContinuesThroughParent(t *testing.T) {
	f := newFixture(t, Config{})
	first, _ := f.send(t, "instinct", "muse", "")
	f.claim(t, first.ID, "muse")
	second, err := f.send(t, "muse", "grokbot", first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if second.Hop != 2 || second.TraceID != first.TraceID || len(second.Chain) != 2 || second.Chain[0] != "instinct" || second.Chain[1] != "muse" {
		t.Fatalf("continued chain = %+v", second)
	}
}

// A sender that forgets the parent still continues the chain it is handling.
func TestOpenClaimUsedWhenParentMissing(t *testing.T) {
	f := newFixture(t, Config{})
	first, _ := f.send(t, "grokbot", "instinct", "")
	f.claim(t, first.ID, "instinct")
	second, _ := f.send(t, "instinct", "muse", first.ID)
	f.claim(t, second.ID, "muse")
	third, err := f.send(t, "muse", "claude-code", "")
	if err != nil {
		t.Fatal(err)
	}
	if third.Hop != 3 || third.TraceID != first.TraceID || third.ParentID != second.ID {
		t.Fatalf("implicit parent = %+v", third)
	}
}

// AE4: a request back to an agent already in the chain is a cycle.
func TestCycleRejectedButNewChainAllowed(t *testing.T) {
	f := newFixture(t, Config{})
	first, _ := f.send(t, "grokbot", "instinct", "")
	f.claim(t, first.ID, "instinct")
	second, _ := f.send(t, "instinct", "muse", first.ID)
	f.claim(t, second.ID, "muse")
	if _, err := f.send(t, "muse", "grokbot", second.ID); !errors.Is(err, ErrCycle) {
		t.Fatalf("want ErrCycle, got %v", err)
	}
	// Muse finishes its claim, so a fresh request is a new chain.
	f.st.Reply(context.Background(), second.ID, "muse", envelope.Reply{Status: envelope.StatusAnswered, Body: "ok"})
	fresh, err := f.send(t, "muse", "grokbot", "")
	if err != nil || fresh.Hop != 1 {
		t.Fatalf("new chain from muse = %+v, %v", fresh, err)
	}
}

func TestHopLimit(t *testing.T) {
	f := newFixture(t, Config{HopLimit: 4})
	agents := []string{"a1", "a2", "a3", "a4", "a5", "a6"}
	parent := ""
	for i := range 4 {
		req, err := f.send(t, agents[i], agents[i+1], parent)
		if err != nil {
			t.Fatalf("hop %d rejected: %v", i+1, err)
		}
		f.claim(t, req.ID, agents[i+1])
		parent = req.ID
	}
	if _, err := f.send(t, agents[4], agents[5], parent); !errors.Is(err, ErrHopLimit) {
		t.Fatalf("hop 5: want ErrHopLimit, got %v", err)
	}
}

func TestParentMustBeHeldBySender(t *testing.T) {
	f := newFixture(t, Config{})
	first, _ := f.send(t, "grokbot", "instinct", "")
	if _, err := f.send(t, "muse", "claude-code", first.ID); !errors.Is(err, ErrBadParent) {
		t.Fatalf("parent addressed to someone else: want ErrBadParent, got %v", err)
	}
	if _, err := f.send(t, "muse", "claude-code", "nope"); !errors.Is(err, ErrBadParent) {
		t.Fatalf("unknown parent: want ErrBadParent, got %v", err)
	}
}

func TestRateLimit(t *testing.T) {
	f := newFixture(t, Config{PerMinute: 3})
	for i := range 3 {
		if _, err := f.send(t, "muse", "grokbot", ""); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	if _, err := f.send(t, "muse", "grokbot", ""); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("want ErrRateLimited, got %v", err)
	}
	if _, err := f.send(t, "instinct", "grokbot", ""); err != nil {
		t.Fatalf("other senders are not limited: %v", err)
	}
	f.now = f.now.Add(61 * time.Second)
	if _, err := f.send(t, "muse", "grokbot", ""); err != nil {
		t.Fatalf("after the window: %v", err)
	}
}

// An agent handling two requests at once has no single chain to continue, so
// a parentless ask starts a new chain instead of guessing (and must not be
// rejected as a cycle against the chain it guessed).
func TestTwoOpenClaimsStartNewChain(t *testing.T) {
	f := newFixture(t, Config{})
	a, _ := f.send(t, "grokbot", "muse", "")
	f.claim(t, a.ID, "muse")
	b, _ := f.send(t, "instinct", "muse", "")
	f.claim(t, b.ID, "muse")
	// Guessing either chain is wrong; guessing b's would also reject this as a cycle.
	req, err := f.send(t, "muse", "instinct", "")
	if err != nil {
		t.Fatalf("parentless ask with two open claims: %v", err)
	}
	if req.Hop != 1 || req.ParentID != "" || req.TraceID != req.ID {
		t.Fatalf("want a new chain, got %+v", req)
	}
}

// A notify the agent claimed long ago never closes, so it must not become the
// implicit parent of the agent's later asks.
func TestClaimedNotifyIsNotImplicitParent(t *testing.T) {
	f := newFixture(t, Config{})
	n := envelope.Request{From: "grokbot", To: "muse", Kind: envelope.KindNotify, Body: "fyi"}
	if err := f.pol.Prepare(context.Background(), &n); err != nil {
		t.Fatal(err)
	}
	n, err := f.st.Enqueue(context.Background(), n, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	f.claim(t, n.ID, "muse")
	req, err := f.send(t, "muse", "grokbot", "")
	if err != nil {
		t.Fatalf("ask after a claimed notify: %v", err)
	}
	if req.Hop != 1 || req.ParentID != "" {
		t.Fatalf("stale notify became the parent: %+v", req)
	}
}
