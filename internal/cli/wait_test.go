package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mvanhorn/agent-tincan/internal/client"
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
