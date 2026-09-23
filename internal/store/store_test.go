package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mvanhorn/agent-tincan/internal/envelope"
	"github.com/mvanhorn/agent-tincan/internal/identity"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func open(t *testing.T, path string) (*Store, *clock) {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	c := &clock{t: time.Unix(1_790_000_000, 0)}
	s.SetClock(c.now)
	return s, c
}

func ask(t *testing.T, s *Store, from, to, body string) envelope.Request {
	t.Helper()
	req, err := s.Enqueue(context.Background(), envelope.Request{From: from, To: to, Kind: envelope.KindAsk, Body: body, Hop: 1, Chain: []string{}}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestIdentityStoreRoundTrip(t *testing.T) {
	s, c := open(t, ":memory:")
	ctx := context.Background()
	if err := s.PutAgent(ctx, identity.Agent{Name: "muse", NodeID: "nMUSE", NodeName: "muse", JoinedAt: c.t}); err != nil {
		t.Fatal(err)
	}
	// Moving muse to a new node replaces the old binding.
	s.PutAgent(ctx, identity.Agent{Name: "muse", NodeID: "nMUSE2", NodeName: "muse2", JoinedAt: c.t})
	agents, _ := s.Agents(ctx)
	if len(agents) != 1 || agents[0].NodeID != "nMUSE2" {
		t.Fatalf("agents = %+v", agents)
	}
	if ok, _ := s.DeleteAgent(ctx, "muse"); !ok {
		t.Fatal("delete should report a removed agent")
	}
	if ok, _ := s.DeleteAgent(ctx, "muse"); ok {
		t.Fatal("second delete should report nothing removed")
	}
	s.PutInvite(ctx, identity.Invite{Code: "AAAA-BBBB", Name: "muse", Expires: c.t})
	if _, ok, _ := s.TakeInvite(ctx, "AAAA-BBBB"); !ok {
		t.Fatal("first take should find the invite")
	}
	if _, ok, _ := s.TakeInvite(ctx, "AAAA-BBBB"); ok {
		t.Fatal("invite must be single use")
	}
}

// The SQLite store backs the identity directory end to end.
func TestDirectoryOnSQLite(t *testing.T) {
	s, _ := open(t, ":memory:")
	ctx := context.Background()
	who := fakeWho{"100.0.0.1:1": {ID: "nMAC", Name: "macbook-pro-44"}, "100.0.0.4:1": {ID: "nMUSE", Name: "muse"}}
	dir := identity.NewDirectory(s, who, identity.Config{Admins: []string{"macbook-pro-44"}})
	code, err := dir.Invite(ctx, "100.0.0.1:1", "muse")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dir.Join(ctx, "100.0.0.4:1", code); err != nil {
		t.Fatal(err)
	}
	if got, err := dir.Attribute(ctx, "100.0.0.4:1"); err != nil || got != "muse" {
		t.Fatalf("attribute = %q, %v", got, err)
	}
}

type fakeWho map[string]identity.Node

func (f fakeWho) WhoIs(_ context.Context, addr string) (identity.Node, error) {
	n, ok := f[addr]
	if !ok {
		return identity.Node{}, errors.New("unknown")
	}
	return n, nil
}

func TestAskClaimReplyGet(t *testing.T) {
	s, _ := open(t, ":memory:")
	ctx := context.Background()
	req := ask(t, s, "grokbot", "instinct", "what's in the report")
	if req.ID == "" || req.TraceID != req.ID || req.CreatedAt.IsZero() {
		t.Fatalf("enqueue did not assign id/trace/time: %+v", req)
	}
	got, err := s.Deliver(ctx, "instinct", 10, time.Minute)
	if err != nil || len(got) != 1 || got[0].ID != req.ID {
		t.Fatalf("deliver = %+v, %v", got, err)
	}
	if again, _ := s.Deliver(ctx, "instinct", 10, time.Minute); len(again) != 0 {
		t.Fatalf("a delivered request must not be delivered twice: %+v", again)
	}
	if _, err := s.Claim(ctx, req.ID, "instinct", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reply(ctx, req.ID, "instinct", envelope.Reply{Status: envelope.StatusAnswered, Body: "3 rows"}); err != nil {
		t.Fatal(err)
	}
	res, err := s.Get(ctx, req.ID, "grokbot")
	if err != nil || res.Status != envelope.StatusAnswered || res.Reply == nil || res.Reply.Body != "3 rows" || res.Reply.From != "instinct" {
		t.Fatalf("get = %+v, %v", res, err)
	}
	if _, err := s.Reply(ctx, req.ID, "instinct", envelope.Reply{Status: envelope.StatusAnswered, Body: "again"}); !errors.Is(err, ErrWrongState) {
		t.Fatalf("second reply: want ErrWrongState, got %v", err)
	}
}

func TestOnlyTheRightAgentMayAct(t *testing.T) {
	s, _ := open(t, ":memory:")
	ctx := context.Background()
	req := ask(t, s, "grokbot", "muse", "call Joe's Garage")
	if _, err := s.Claim(ctx, req.ID, "instinct", time.Minute); !errors.Is(err, ErrForbidden) {
		t.Errorf("instinct claim: want ErrForbidden, got %v", err)
	}
	if _, err := s.Reply(ctx, req.ID, "instinct", envelope.Reply{Status: envelope.StatusAnswered, Body: "x"}); !errors.Is(err, ErrForbidden) {
		t.Errorf("instinct reply: want ErrForbidden, got %v", err)
	}
	if _, err := s.Get(ctx, req.ID, "instinct"); !errors.Is(err, ErrNotFound) {
		t.Errorf("instinct get: want ErrNotFound, got %v", err)
	}
	if err := s.Cancel(ctx, req.ID, "instinct"); !errors.Is(err, ErrForbidden) {
		t.Errorf("instinct cancel: want ErrForbidden, got %v", err)
	}
	if err := s.Cancel(ctx, req.ID, "muse"); !errors.Is(err, ErrForbidden) {
		t.Errorf("target cancel: want ErrForbidden, got %v", err)
	}
	if err := s.Cancel(ctx, req.ID, "grokbot"); err != nil {
		t.Errorf("sender cancel: %v", err)
	}
	if _, err := s.Claim(ctx, req.ID, "muse", time.Minute); !errors.Is(err, ErrWrongState) {
		t.Errorf("claim after cancel: want ErrWrongState, got %v", err)
	}
}

func TestCannotCancelClaimed(t *testing.T) {
	s, _ := open(t, ":memory:")
	ctx := context.Background()
	req := ask(t, s, "grokbot", "muse", "x")
	s.Claim(ctx, req.ID, "muse", time.Minute)
	if err := s.Cancel(ctx, req.ID, "grokbot"); !errors.Is(err, ErrWrongState) {
		t.Fatalf("want ErrWrongState, got %v", err)
	}
}

// AE2: a request past its TTL expires and the sender sees that.
func TestTTLExpiry(t *testing.T) {
	s, c := open(t, ":memory:")
	ctx := context.Background()
	req := ask(t, s, "grokbot", "muse", "x")
	c.advance(time.Hour + time.Second)
	if got, _ := s.Deliver(ctx, "muse", 10, time.Minute); len(got) != 0 {
		t.Fatalf("expired request was delivered: %+v", got)
	}
	tr, err := s.Sweep(ctx)
	if err != nil || len(tr) != 1 || tr[0].Status != envelope.StatusExpired {
		t.Fatalf("sweep = %+v, %v", tr, err)
	}
	if res, _ := s.Get(ctx, req.ID, "grokbot"); res.Status != envelope.StatusExpired {
		t.Fatalf("status = %s, want expired", res.Status)
	}
}

// A request delivered but never claimed (poller crashed) must return to the
// queue, not strand.
func TestDeliveryLeaseReturnsToQueue(t *testing.T) {
	s, c := open(t, ":memory:")
	ctx := context.Background()
	req := ask(t, s, "grokbot", "muse", "x")
	s.Deliver(ctx, "muse", 10, time.Minute)
	c.advance(2 * time.Minute)
	if _, err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Deliver(ctx, "muse", 10, time.Minute)
	if len(got) != 1 || got[0].ID != req.ID {
		t.Fatalf("lease-expired request not redelivered: %+v", got)
	}
}

func TestClaimLeaseReturnsToQueue(t *testing.T) {
	s, c := open(t, ":memory:")
	ctx := context.Background()
	req := ask(t, s, "grokbot", "muse", "x")
	s.Claim(ctx, req.ID, "muse", time.Minute)
	c.advance(2 * time.Minute)
	s.Sweep(ctx)
	if res, _ := s.Get(ctx, req.ID, "grokbot"); res.Status != envelope.StatusQueued {
		t.Fatalf("status = %s, want queued", res.Status)
	}
}

func TestOnlyOneClaimWins(t *testing.T) {
	s, _ := open(t, ":memory:")
	ctx := context.Background()
	ask(t, s, "grokbot", "muse", "x")
	a, _ := s.Deliver(ctx, "muse", 10, time.Minute)
	b, _ := s.Deliver(ctx, "muse", 10, time.Minute)
	if len(a)+len(b) != 1 {
		t.Fatalf("two pollers both received the request: %d + %d", len(a), len(b))
	}
}

func TestSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.db")
	s, _ := open(t, path)
	req := ask(t, s, "grokbot", "muse", "x")
	s.Close()
	s2, _ := open(t, path)
	got, err := s2.Deliver(context.Background(), "muse", 10, time.Minute)
	if err != nil || len(got) != 1 || got[0].ID != req.ID {
		t.Fatalf("after restart deliver = %+v, %v", got, err)
	}
}

func TestCancelAllTo(t *testing.T) {
	s, _ := open(t, ":memory:")
	ctx := context.Background()
	ask(t, s, "grokbot", "muse", "a")
	ask(t, s, "instinct", "muse", "b")
	ask(t, s, "muse", "grokbot", "c")
	ids, err := s.CancelAllTo(ctx, "muse")
	if err != nil || len(ids) != 2 {
		t.Fatalf("cancelled %v, %v", ids, err)
	}
	if got, _ := s.Deliver(ctx, "grokbot", 10, time.Minute); len(got) != 1 {
		t.Fatalf("request to grokbot should survive: %+v", got)
	}
}

func TestOpenClaim(t *testing.T) {
	s, _ := open(t, ":memory:")
	ctx := context.Background()
	if _, ok, _ := s.OpenClaim(ctx, "muse"); ok {
		t.Fatal("no claim yet")
	}
	req := ask(t, s, "instinct", "muse", "call the dentist")
	s.Claim(ctx, req.ID, "muse", time.Minute)
	got, ok, err := s.OpenClaim(ctx, "muse")
	if err != nil || !ok || got.ID != req.ID {
		t.Fatalf("open claim = %+v %v %v", got, ok, err)
	}
}
