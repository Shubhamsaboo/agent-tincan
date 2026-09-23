package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mvanhorn/agent-tincan/internal/client"
	"github.com/mvanhorn/agent-tincan/internal/envelope"
	"github.com/mvanhorn/agent-tincan/internal/relay"
	"github.com/mvanhorn/agent-tincan/internal/testrelay"
)

// A flaky tailnet path (Instinct saw this) must not end a background wait.
func TestWaitRetriesTransientErrors(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			http.Error(w, "bad gateway", http.StatusBadGateway)
		case 2:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"requests":[{"id":"r1","from":"grokbot","to":"muse","body":"call Joe's Garage"}]}`))
		}
	}))
	defer ts.Close()
	r, _ := client.NewRelay(ts.URL, "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reqs, err := waitForRequests(ctx, r, 0)
	if err != nil || len(reqs) != 1 || reqs[0].ID != "r1" {
		t.Fatalf("wait = %+v, %v after %d calls", reqs, err, calls.Load())
	}
}

func TestWaitStopsWhenNotJoined(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"not a joined agent"}`, http.StatusForbidden)
	}))
	defer ts.Close()
	r, _ := client.NewRelay(ts.URL, "")
	if _, err := waitForRequests(context.Background(), r, 0); !client.IsStatus(err, http.StatusForbidden) {
		t.Fatalf("want 403, got %v", err)
	}
}

func TestListenRunsCommandWithoutTakingRequests(t *testing.T) {
	m := testrelay.New(t, relay.Config{PollHold: 2 * time.Second})
	muse := m.Client(t, "muse")
	out := filepath.Join(t.TempDir(), "nudged")
	go func() {
		time.Sleep(200 * time.Millisecond)
		m.Client(t, "grokbot").Send(context.Background(), "muse", "call Joe's Garage", envelope.KindAsk, "")
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := listen(ctx, muse, `echo "$TINCAN_WAITING" > `+out, true); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(out)
	if strings.TrimSpace(string(got)) != "1" {
		t.Fatalf("command saw TINCAN_WAITING=%q", got)
	}
	reqs, err := muse.Poll(context.Background(), 0)
	if err != nil || len(reqs) != 1 {
		t.Fatalf("request should still be waiting for muse: %+v %v", reqs, err)
	}
}
