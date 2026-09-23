package mcpserver_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mvanhorn/agent-tincan/internal/client"
	"github.com/mvanhorn/agent-tincan/internal/envelope"
	"github.com/mvanhorn/agent-tincan/internal/mcpserver"
	"github.com/mvanhorn/agent-tincan/internal/relay"
	"github.com/mvanhorn/agent-tincan/internal/testrelay"
)

// session connects a real MCP client to a tincan MCP server acting as agent.
func session(t *testing.T, m *testrelay.Mesh, agent string) *mcp.ClientSession {
	t.Helper()
	srvT, cliT := mcp.NewInMemoryTransports()
	srv := mcpserver.New(m.Client(t, agent), "test")
	ss, err := srv.Connect(t.Context(), srvT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil).Connect(t.Context(), cliT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) string {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	if res.IsError {
		return "ERROR: " + b.String()
	}
	return b.String()
}

func TestToolListIsExactlyTheAgentTools(t *testing.T) {
	m := testrelay.New(t, relay.Config{})
	res, err := session(t, m, "grokbot").ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	want := slices.Clone(mcpserver.ToolNames)
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}
	for _, n := range names {
		if strings.Contains(n, "invite") || strings.Contains(n, "remove") {
			t.Fatalf("admin action exposed as tool: %s", n)
		}
	}
}

// AE3 over MCP: Instinct asks Muse, Muse checks its inbox, replies, and the
// chain is visible.
func TestAskInboxReplyOverMCP(t *testing.T) {
	m := testrelay.New(t, relay.Config{MaxWait: 3 * time.Second})
	inst, muse := session(t, m, "instinct"), session(t, m, "muse")

	out := call(t, inst, "ask", map[string]any{"to": "muse", "message": "call the dentist and reschedule", "wait_seconds": 1})
	if !strings.Contains(out, "No reply yet") {
		t.Fatalf("ask = %q", out)
	}
	inbox := call(t, muse, "check_inbox", nil)
	if !strings.Contains(inbox, "from instinct (your teammate)") || !strings.Contains(inbox, "call the dentist") {
		t.Fatalf("inbox = %q", inbox)
	}
	id := between(inbox, "Request ", " from")
	if got := call(t, muse, "reply", map[string]any{"request_id": id, "message": "done, Tue 3pm"}); !strings.Contains(got, "Replied") {
		t.Fatalf("reply = %q", got)
	}
	if got := call(t, inst, "get_reply", map[string]any{"request_id": id}); !strings.Contains(got, "done, Tue 3pm") {
		t.Fatalf("get_reply = %q", got)
	}
	if got := call(t, muse, "trace", map[string]any{"trace_id": id}); !strings.Contains(got, "hop 1: instinct -> muse [answered]") {
		t.Fatalf("trace = %q", got)
	}
	// A second check finds nothing: the request was claimed.
	if got := call(t, muse, "check_inbox", nil); !strings.Contains(got, "No requests waiting") {
		t.Fatalf("second inbox = %q", got)
	}
}

func TestListAgentsShowsWake(t *testing.T) {
	m := testrelay.New(t, relay.Config{})
	out := call(t, session(t, m, "grokbot"), "list_agents", nil)
	if !strings.Contains(out, "muse: offline, wake=none") {
		t.Fatalf("list_agents = %q", out)
	}
}

func TestErrorsComeBackAsToolErrors(t *testing.T) {
	m := testrelay.New(t, relay.Config{})
	out := call(t, session(t, m, "grokbot"), "ask", map[string]any{"to": "nobody", "message": "x", "wait_seconds": 1})
	if !strings.HasPrefix(out, "ERROR:") || !strings.Contains(out, "no such agent") {
		t.Fatalf("ask to unknown agent = %q", out)
	}
}

// Parity: the MCP ask and the client ask render the same result.
func TestMCPAndClientAgree(t *testing.T) {
	m := testrelay.New(t, relay.Config{MaxWait: time.Second})
	viaMCP := call(t, session(t, m, "grokbot"), "ask", map[string]any{"to": "muse", "message": "x", "wait_seconds": 1})
	res, err := m.Client(t, "grokbot").Ask(context.Background(), "muse", "x", "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	viaCLI := client.FormatResult(res)
	norm := func(s string) string { return strings.Split(strings.TrimSpace(s), " Request id")[0] }
	if norm(viaMCP) != norm(viaCLI) {
		t.Fatalf("mcp %q vs client %q", viaMCP, viaCLI)
	}
}

func TestNotifySendsWithoutWaiting(t *testing.T) {
	m := testrelay.New(t, relay.Config{})
	start := time.Now()
	out := call(t, session(t, m, "muse"), "ask", map[string]any{"to": "grokbot", "message": "new time Tue 3pm", "notify": true})
	if !strings.Contains(out, "Sent to grokbot") || time.Since(start) > 2*time.Second {
		t.Fatalf("notify = %q after %v", out, time.Since(start))
	}
	reqs, _ := m.Client(t, "grokbot").Poll(context.Background(), 0)
	if len(reqs) != 1 || reqs[0].Kind != envelope.KindNotify {
		t.Fatalf("grokbot got %+v", reqs)
	}
}

func between(s, a, b string) string {
	_, rest, _ := strings.Cut(s, a)
	out, _, _ := strings.Cut(rest, b)
	return out
}
