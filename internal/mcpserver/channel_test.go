package mcpserver_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mvanhorn/agent-tincan/internal/mcpserver"
	"github.com/mvanhorn/agent-tincan/internal/relay"
	"github.com/mvanhorn/agent-tincan/internal/testrelay"
)

// tap records every message the client side receives.
type tap struct {
	mcp.Transport
	mu   sync.Mutex
	seen []*jsonrpc.Request
}

type tapConn struct {
	mcp.Connection
	t *tap
}

func (t *tap) Connect(ctx context.Context) (mcp.Connection, error) {
	c, err := t.Transport.Connect(ctx)
	return &tapConn{Connection: c, t: t}, err
}

func (c *tapConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	msg, err := c.Connection.Read(ctx)
	if req, ok := msg.(*jsonrpc.Request); ok && req.Method == mcpserver.ChannelMethod {
		c.t.mu.Lock()
		c.t.seen = append(c.t.seen, req)
		c.t.mu.Unlock()
	}
	return msg, err
}

func (t *tap) channelEvents() []*jsonrpc.Request {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]*jsonrpc.Request(nil), t.seen...)
}

func TestChannelDeclaresCapabilityAndPushesEvents(t *testing.T) {
	m := testrelay.New(t, relay.Config{})
	srvT, cliT := mcp.NewInMemoryTransports()
	ch := mcpserver.NewChannelTransport(srvT)
	srv := mcpserver.NewWithOptions(m.Client(t, "muse"), "test", mcpserver.ChannelOptions())
	ss, err := srv.Connect(t.Context(), ch, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	tp := &tap{Transport: cliT}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "claude-code-ish"}, nil).Connect(t.Context(), tp, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	init := cs.InitializeResult()
	if _, ok := init.Capabilities.Experimental["claude/channel"]; !ok {
		t.Fatalf("claude/channel capability missing: %+v", init.Capabilities)
	}
	if init.ProtocolVersion > "2025-11-25" {
		t.Fatalf("negotiated %s; Claude Code skips channels at 2026-07-28", init.ProtocolVersion)
	}
	select {
	case <-ch.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("channel never became ready")
	}
	if err := ch.Push(t.Context(), "Request r1 from instinct (your teammate)...", map[string]string{"from": "instinct", "request_id": "r1"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(tp.channelEvents()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	evs := tp.channelEvents()
	if len(evs) != 1 {
		t.Fatalf("channel events = %d", len(evs))
	}
	var p struct {
		Content string            `json:"content"`
		Meta    map[string]string `json:"meta"`
	}
	json.Unmarshal(evs[0].Params, &p)
	if p.Meta["request_id"] != "r1" || p.Meta["from"] != "instinct" || p.Content == "" {
		t.Fatalf("event params = %s", evs[0].Params)
	}
	// Tools still work alongside the channel.
	if _, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_agents"}); err != nil {
		t.Fatal(err)
	}
}
