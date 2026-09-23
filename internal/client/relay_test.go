package client_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mvanhorn/agent-tincan/internal/client"
	"github.com/mvanhorn/agent-tincan/internal/envelope"
	"github.com/mvanhorn/agent-tincan/internal/relay"
	"github.com/mvanhorn/agent-tincan/internal/testrelay"
)

func TestAskGetsReplyInsideWindow(t *testing.T) {
	m := testrelay.New(t, relay.Config{PollHold: 5 * time.Second, MaxWait: 5 * time.Second})
	grok, inst := m.Client(t, "grokbot"), m.Client(t, "instinct")
	go func() {
		reqs, err := inst.Poll(context.Background(), 5*time.Second)
		if err != nil || len(reqs) != 1 {
			t.Errorf("instinct poll: %v %v", reqs, err)
			return
		}
		inst.Claim(context.Background(), reqs[0].ID)
		inst.Reply(context.Background(), reqs[0].ID, "3 rows", envelope.StatusAnswered)
	}()
	res, err := grok.Ask(context.Background(), "instinct", "what is in the report", "", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.Reply == nil || res.Reply.Body != "3 rows" || res.Reply.From != "instinct" {
		t.Fatalf("ask result = %+v", res)
	}
	if got := client.FormatResult(res); !strings.Contains(got, "instinct replied") {
		t.Fatalf("formatted = %q", got)
	}
}

func TestAskWithoutReplyReturnsRequestID(t *testing.T) {
	m := testrelay.New(t, relay.Config{MaxWait: time.Second})
	start := time.Now()
	res, err := m.Client(t, "grokbot").Ask(context.Background(), "muse", "call Joe's Garage", "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.Done() || res.Request.ID == "" || time.Since(start) > 3*time.Second {
		t.Fatalf("result = %+v after %v", res, time.Since(start))
	}
	if got := client.FormatResult(res); !strings.Contains(got, res.Request.ID) || !strings.Contains(got, "No reply yet") {
		t.Fatalf("formatted = %q", got)
	}
}

func TestRelayErrorsSurface(t *testing.T) {
	m := testrelay.New(t, relay.Config{})
	_, err := m.Client(t, "grokbot").Send(context.Background(), "nobody", "x", envelope.KindAsk, "")
	if !client.IsStatus(err, http.StatusNotFound) {
		t.Fatalf("want 404 APIError, got %v", err)
	}
	_, err = m.Client(t, "admin").Send(context.Background(), "muse", "x", envelope.KindAsk, "")
	if !client.IsStatus(err, http.StatusForbidden) {
		t.Fatalf("unjoined sender: want 403, got %v", err)
	}
}

func TestFormatRequestCarriesTeammateFraming(t *testing.T) {
	out := client.FormatRequest(envelope.Request{ID: "r9", From: "instinct", Hop: 2, Chain: []string{"instinct", "muse"}, Body: "new time Tue 3pm"})
	for _, want := range []string{"from instinct (your teammate)", "instinct -> muse", "as you would a request from Matt", "tincan reply r9", "new time Tue 3pm"} {
		if !strings.Contains(out, want) {
			t.Errorf("framing missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(strings.ToLower(out), "untrusted") {
		t.Error("teammate requests must not be framed as untrusted")
	}
}

// Muse's default proxy cannot reach the tailnet, so relay traffic must go
// through the proxy saved in config, not HTTP_PROXY.
func TestProxyOverrideRoutesRelayTraffic(t *testing.T) {
	var seen string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.String() // a forward proxy sees the absolute URL
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"agents":[{"name":"grokbot","online":true,"wake":"webhook"}]}`))
	}))
	defer proxy.Close()
	r, err := client.NewRelay("http://tincan-relay", proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	agents, err := r.Agents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if seen != "http://tincan-relay/v1/agents" || len(agents) != 1 || agents[0].Wake != "webhook" {
		t.Fatalf("proxy saw %q, agents %+v", seen, agents)
	}
	if _, err := client.NewRelay("", ""); err == nil {
		t.Fatal("empty relay URL should fail")
	}
}
