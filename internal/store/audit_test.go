package store

import (
	"context"
	"strings"
	"testing"

	"github.com/mvanhorn/agent-tincan/internal/envelope"
)

func TestAuditChainVerifiesAndDetectsTampering(t *testing.T) {
	s, c := open(t, ":memory:")
	ctx := context.Background()
	for _, ev := range []string{"queued", "delivered", "claimed", "replied"} {
		if err := s.Audit(ctx, AuditEvent{Event: ev, RequestID: "r1", TraceID: "t1", Actor: "muse"}); err != nil {
			t.Fatal(err)
		}
		c.advance(1)
	}
	n, err := s.VerifyAudit(ctx)
	if err != nil || n != 4 {
		t.Fatalf("verify = %d, %v", n, err)
	}
	if _, err := s.DB().Exec(`UPDATE audit SET actor = 'grokbot' WHERE seq = 3`); err != nil {
		t.Fatal(err)
	}
	_, err = s.VerifyAudit(ctx)
	if err == nil || !strings.Contains(err.Error(), "entry 3") {
		t.Fatalf("tampered row not caught at entry 3: %v", err)
	}
}

func TestTraceListsChainInOrder(t *testing.T) {
	s, c := open(t, ":memory:")
	ctx := context.Background()
	first := ask(t, s, "instinct", "muse", "call the dentist")
	c.advance(1e6)
	second, err := s.Enqueue(ctx, first2(first), 0)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := s.Trace(ctx, first.TraceID)
	if err != nil || len(steps) != 2 || steps[0].Request.ID != first.ID || steps[1].Request.ID != second.ID {
		t.Fatalf("trace = %+v, %v", steps, err)
	}
	got := strings.Join(Participants(steps), ",")
	if got != "instinct,muse,grokbot" {
		t.Fatalf("participants = %s", got)
	}
}

func first2(parent envelope.Request) envelope.Request {
	return envelope.Request{From: "muse", To: "grokbot", Kind: envelope.KindAsk, Body: "new time Tue 3pm",
		ParentID: parent.ID, TraceID: parent.TraceID, Hop: 2, Chain: []string{"instinct", "muse"}}
}
