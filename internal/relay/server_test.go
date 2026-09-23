package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mvanhorn/agent-tincan/internal/envelope"
	"github.com/mvanhorn/agent-tincan/internal/identity"
	"github.com/mvanhorn/agent-tincan/internal/identity/identitytest"
	"github.com/mvanhorn/agent-tincan/internal/store"
)

const (
	macAddr      = "100.0.0.1:1"
	grokAddr     = "100.0.0.2:1"
	instinctAddr = "100.0.0.3:1"
	museAddr     = "100.0.0.4:1"
	strangerAddr = "100.0.0.9:1"
)

type harness struct {
	t   *testing.T
	srv *Server
	h   http.Handler
	st  *store.Store
}

// newHarness starts a relay with grokbot, instinct, and muse joined, using a
// fake WhoIs so tests pick each caller's tailnet identity directly.
func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	who := identitytest.New(map[string]identity.Node{
		macAddr:      {ID: "nMAC", Name: "macbook-pro-44"},
		grokAddr:     {ID: "nGROK", Name: "grok-bot"},
		instinctAddr: {ID: "nINST", Name: "instinct"},
		museAddr:     {ID: "nMUSE", Name: "muse"},
		strangerAddr: {ID: "nLAPTOP", Name: "old-laptop"},
	})
	dir := identity.NewDirectory(st, who, identity.Config{Admins: []string{"macbook-pro-44"}})
	srv := New(dir, st, cfg)
	h := &harness{t: t, srv: srv, h: srv.Handler(), st: st}
	for name, addr := range map[string]string{"grokbot": grokAddr, "instinct": instinctAddr, "muse": museAddr} {
		var inv struct{ Code string }
		h.do(macAddr, "POST", "/v1/admin/invite", `{"name":"`+name+`"}`, http.StatusOK, &inv)
		h.do(addr, "POST", "/v1/join", `{"code":"`+inv.Code+`"}`, http.StatusOK, nil)
	}
	return h
}

func (h *harness) do(addr, method, path, body string, want int, out any) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = addr
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, req)
	if rec.Code != want {
		h.t.Fatalf("%s %s from %s: status %d, want %d: %s", method, path, addr, rec.Code, want, rec.Body.String())
	}
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			h.t.Fatalf("decode %s: %v: %s", path, err, rec.Body.String())
		}
	}
	return rec
}

func (h *harness) send(addr, to, body string) envelope.Request {
	h.t.Helper()
	var req envelope.Request
	h.do(addr, "POST", "/v1/send", `{"to":"`+to+`","body":"`+body+`"}`, http.StatusCreated, &req)
	return req
}

type pollResult struct {
	Requests []envelope.Request `json:"requests"`
}

// AE1: a poller already waiting gets a new request in well under a second.
func TestWaitingPollerGetsRequestFast(t *testing.T) {
	h := newHarness(t, Config{PollHold: 5 * time.Second})
	var wg sync.WaitGroup
	var got pollResult
	var took time.Duration
	wg.Go(func() {
		start := time.Now()
		h.do(instinctAddr, "GET", "/v1/poll", "", http.StatusOK, &got)
		took = time.Since(start)
	})
	time.Sleep(100 * time.Millisecond) // let the poll start waiting
	sent := h.send(grokAddr, "instinct", "what is in the report")
	wg.Wait()
	if len(got.Requests) != 1 || got.Requests[0].ID != sent.ID {
		t.Fatalf("poll got %+v", got)
	}
	if took > time.Second {
		t.Fatalf("delivery took %v, want under 1s after send", took)
	}
	if got.Requests[0].From != "grokbot" {
		t.Fatalf("from = %q, want grokbot", got.Requests[0].From)
	}
}

// AE2: sent while the target is away, delivered on its next poll.
func TestQueuedUntilNextPoll(t *testing.T) {
	h := newHarness(t, Config{})
	sent := h.send(grokAddr, "muse", "call Joe's Garage")
	var got pollResult
	h.do(museAddr, "GET", "/v1/poll?hold=0", "", http.StatusOK, &got)
	if len(got.Requests) != 1 || got.Requests[0].ID != sent.ID {
		t.Fatalf("poll got %+v", got)
	}
}

func TestPollTimesOutWithNoContent(t *testing.T) {
	h := newHarness(t, Config{PollHold: 200 * time.Millisecond})
	start := time.Now()
	h.do(museAddr, "GET", "/v1/poll", "", http.StatusNoContent, nil)
	if d := time.Since(start); d < 150*time.Millisecond || d > 2*time.Second {
		t.Fatalf("empty poll returned after %v", d)
	}
}

// Full ask, claim, reply, and a waiting get-reply on the sender side.
func TestAskClaimReplyWithWaitingSender(t *testing.T) {
	h := newHarness(t, Config{MaxWait: 5 * time.Second})
	sent := h.send(grokAddr, "instinct", "rows?")
	var res store.Result
	var wg sync.WaitGroup
	wg.Go(func() {
		h.do(grokAddr, "GET", "/v1/requests/"+sent.ID+"?wait=5", "", http.StatusOK, &res)
	})
	time.Sleep(100 * time.Millisecond)
	h.do(instinctAddr, "POST", "/v1/requests/"+sent.ID+"/claim", "", http.StatusOK, nil)
	h.do(instinctAddr, "POST", "/v1/requests/"+sent.ID+"/reply", `{"body":"3 rows"}`, http.StatusOK, nil)
	wg.Wait()
	if res.Status != envelope.StatusAnswered || res.Reply == nil || res.Reply.Body != "3 rows" {
		t.Fatalf("sender saw %+v", res)
	}
}

// AE5: a tailnet machine that never joined is refused.
func TestUnjoinedMachineRejected(t *testing.T) {
	h := newHarness(t, Config{})
	h.do(strangerAddr, "POST", "/v1/send", `{"to":"grokbot","body":"hi"}`, http.StatusForbidden, nil)
	h.do(strangerAddr, "GET", "/v1/poll?hold=0", "", http.StatusForbidden, nil)
}

// AE6: claiming to be someone else changes nothing.
func TestSpoofedFromIsIgnored(t *testing.T) {
	h := newHarness(t, Config{})
	var req envelope.Request
	h.do(museAddr, "POST", "/v1/send", `{"from":"grokbot","to":"instinct","body":"hi"}`, http.StatusCreated, &req)
	if req.From != "muse" {
		t.Fatalf("from = %q, want muse", req.From)
	}
}

func TestThirdPartyCannotTouchRequest(t *testing.T) {
	h := newHarness(t, Config{})
	sent := h.send(grokAddr, "muse", "x")
	h.do(instinctAddr, "POST", "/v1/requests/"+sent.ID+"/claim", "", http.StatusForbidden, nil)
	h.do(instinctAddr, "POST", "/v1/requests/"+sent.ID+"/reply", `{"body":"x"}`, http.StatusForbidden, nil)
	h.do(instinctAddr, "GET", "/v1/requests/"+sent.ID, "", http.StatusNotFound, nil)
	h.do(instinctAddr, "POST", "/v1/requests/"+sent.ID+"/cancel", "", http.StatusForbidden, nil)
}

func TestSendToUnknownAgent(t *testing.T) {
	h := newHarness(t, Config{})
	h.do(grokAddr, "POST", "/v1/send", `{"to":"nobody","body":"x"}`, http.StatusNotFound, nil)
}

func TestSenderCancels(t *testing.T) {
	h := newHarness(t, Config{})
	sent := h.send(grokAddr, "muse", "x")
	h.do(grokAddr, "POST", "/v1/requests/"+sent.ID+"/cancel", "", http.StatusOK, nil)
	h.do(museAddr, "GET", "/v1/poll?hold=0", "", http.StatusNoContent, nil)
}

func TestRemoveCancelsQueuedRequests(t *testing.T) {
	h := newHarness(t, Config{})
	sent := h.send(grokAddr, "muse", "x")
	h.do(museAddr, "POST", "/v1/admin/remove", `{"name":"grokbot"}`, http.StatusForbidden, nil)
	h.do(macAddr, "POST", "/v1/admin/remove", `{"name":"muse"}`, http.StatusOK, nil)
	var res store.Result
	h.do(grokAddr, "GET", "/v1/requests/"+sent.ID, "", http.StatusOK, &res)
	if res.Status != envelope.StatusCancelled {
		t.Fatalf("status = %s, want cancelled", res.Status)
	}
	h.do(museAddr, "GET", "/v1/poll?hold=0", "", http.StatusForbidden, nil)
}

func TestAgentsListShowsPresence(t *testing.T) {
	h := newHarness(t, Config{})
	h.do(museAddr, "GET", "/v1/poll?hold=0", "", http.StatusNoContent, nil)
	var out struct{ Agents []AgentInfo }
	h.do(grokAddr, "GET", "/v1/agents", "", http.StatusOK, &out)
	online := map[string]bool{}
	for _, a := range out.Agents {
		online[a.Name] = a.Online
		if a.Wake != "none" {
			t.Errorf("%s wake = %q, want none by default", a.Name, a.Wake)
		}
	}
	if len(out.Agents) != 3 || !online["muse"] || online["instinct"] {
		t.Fatalf("agents = %+v", out.Agents)
	}
}

func TestLocalAdminSocketCanInvite(t *testing.T) {
	h := newHarness(t, Config{})
	req := httptest.NewRequest("POST", "/v1/admin/invite", strings.NewReader(`{"name":"claude-code"}`))
	req.RemoteAddr = "@"
	rec := httptest.NewRecorder()
	h.srv.AdminHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("local invite: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSweepRequeuesAndWakesPoller(t *testing.T) {
	h := newHarness(t, Config{PollHold: 5 * time.Second, DeliveryLease: time.Millisecond})
	sent := h.send(grokAddr, "muse", "x")
	h.do(museAddr, "GET", "/v1/poll?hold=0", "", http.StatusOK, nil) // delivered, never claimed
	time.Sleep(10 * time.Millisecond)
	var got pollResult
	var wg sync.WaitGroup
	wg.Go(func() { h.do(museAddr, "GET", "/v1/poll", "", http.StatusOK, &got) })
	time.Sleep(50 * time.Millisecond)
	h.srv.Sweep(context.Background())
	wg.Wait()
	if len(got.Requests) != 1 || got.Requests[0].ID != sent.ID {
		t.Fatalf("redelivery got %+v", got)
	}
}

func TestOversizeBodyRejected(t *testing.T) {
	h := newHarness(t, Config{})
	big := strings.Repeat("x", 2<<20)
	rec := h.do(grokAddr, "POST", "/v1/send", `{"to":"muse","body":"`+big+`"}`, http.StatusRequestEntityTooLarge, nil)
	_ = rec
}
