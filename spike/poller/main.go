// Command spike-poller runs on each agent for U1. It asks spike-relay who it
// thinks this machine is, then sweeps long-poll hold times and records which
// ones survive the path (for Muse, that path includes its HTTP proxy). It
// uses the same proxy-aware transport the real client will use.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mvanhorn/agent-tincan/internal/client"
)

type result struct {
	Agent   string          `json:"agent"`
	Check   string          `json:"check"`
	Secs    int             `json:"secs,omitempty"`
	OK      bool            `json:"ok"`
	Elapsed string          `json:"elapsed"`
	Error   string          `json:"error,omitempty"`
	Body    json.RawMessage `json:"body,omitempty"`
}

func main() {
	base := flag.String("url", "http://tincan-spike:8080", "spike-relay base URL")
	agent := flag.String("agent", "", "name of the agent this runs on (grokbot, instinct, muse)")
	sweep := flag.String("sweep", "10,20,30,45,60,90,120", "comma-separated hold times in seconds")
	flag.Parse()
	if *agent == "" {
		fmt.Fprintln(os.Stderr, "-agent is required")
		os.Exit(2)
	}

	c := client.New(client.APIClient)
	c.Timeout = 10 * time.Minute // the sweep, not the client, bounds each hold
	enc := json.NewEncoder(os.Stdout)
	proxy := os.Getenv("HTTP_PROXY") + os.Getenv("http_proxy")
	fmt.Fprintf(os.Stderr, "agent=%s url=%s http_proxy_set=%v\n", *agent, *base, proxy != "")

	emit := func(r result) {
		if err := enc.Encode(r); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	emit(get(c, *agent, "whois", 0, *base+"/whois"))
	for s := range strings.SplitSeq(*sweep, ",") {
		secs, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			fmt.Fprintf(os.Stderr, "bad sweep value %q\n", s)
			os.Exit(2)
		}
		emit(get(c, *agent, "hold", secs, fmt.Sprintf("%s/hold?secs=%d", *base, secs)))
	}
}

func get(c *http.Client, agent, check string, secs int, url string) result {
	start := time.Now()
	r := result{Agent: agent, Check: check, Secs: secs}
	resp, err := c.Get(url)
	r.Elapsed = time.Since(start).Round(time.Second).String()
	if err != nil {
		r.Error = err.Error()
		return r
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		r.Error = err.Error()
		return r
	}
	r.OK = resp.StatusCode == http.StatusOK
	if !r.OK {
		r.Error = resp.Status
	}
	r.Body = json.RawMessage(strings.TrimSpace(string(body)))
	return r
}
