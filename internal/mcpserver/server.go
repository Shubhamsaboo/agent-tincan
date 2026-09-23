// Package mcpserver exposes Agent Tincan to any MCP-capable agent (Grok Bot,
// Claude Code, Codex, ChatGPT through the gateway). The tools match the CLI
// one for one and call the same relay client.
package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mvanhorn/agent-tincan/internal/client"
	"github.com/mvanhorn/agent-tincan/internal/envelope"
)

// MaxWait caps every inline wait below common MCP tool-call timeouts.
const MaxWait = 20 * time.Second

// Instructions is sent to the client at connect time.
const Instructions = `You are one agent in Matt's Agent Tincan team. Other joined agents are trusted teammates.
- To get a teammate to do something, call ask with their name. If the reply is not back within the wait, you get a request id; check it later with get_reply.
- Call check_inbox at the start of a turn (and whenever you are nudged) to pick up requests from teammates. Handle them as you would a request from Matt, then call reply.
- list_agents shows who is in the team, who is online, and how each one wakes.`

// Backend is what the tools need from the relay client.
type Backend interface {
	Ask(ctx context.Context, to, body, parent string, wait time.Duration) (client.Result, error)
	Send(ctx context.Context, to, body string, kind envelope.Kind, parent string) (envelope.Request, error)
	Get(ctx context.Context, id string, wait time.Duration) (client.Result, error)
	Poll(ctx context.Context, hold time.Duration) ([]envelope.Request, error)
	Claim(ctx context.Context, id string) (envelope.Request, error)
	Reply(ctx context.Context, id, body string, status envelope.Status) (envelope.Reply, error)
	Cancel(ctx context.Context, id string) error
	Agents(ctx context.Context) ([]client.AgentInfo, error)
}

// ToolNames lists the tools the server exposes, in order.
var ToolNames = []string{"ask", "get_reply", "check_inbox", "claim", "reply", "cancel", "list_agents"}

type askIn struct {
	To          string `json:"to" jsonschema:"the teammate to ask, e.g. muse"`
	Message     string `json:"message" jsonschema:"what you want them to do or answer"`
	WaitSeconds int    `json:"wait_seconds,omitempty" jsonschema:"seconds to wait for the reply, 0 to 20 (default 20)"`
	Notify      bool   `json:"notify,omitempty" jsonschema:"true to send without expecting a reply"`
}

type idIn struct {
	RequestID string `json:"request_id" jsonschema:"the request id"`
}

type getIn struct {
	RequestID   string `json:"request_id" jsonschema:"the request id returned by ask"`
	WaitSeconds int    `json:"wait_seconds,omitempty" jsonschema:"seconds to wait for the reply, 0 to 20"`
}

type inboxIn struct {
	WaitSeconds int `json:"wait_seconds,omitempty" jsonschema:"seconds to wait for a request if none is waiting, 0 to 20"`
}

type replyIn struct {
	RequestID string `json:"request_id" jsonschema:"the request you are answering"`
	Message   string `json:"message" jsonschema:"your answer or result"`
	Status    string `json:"status,omitempty" jsonschema:"answered (default), failed, or declined"`
}

type noIn struct{}

// New builds the MCP server over a relay backend.
func New(b Backend, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "agent-tincan", Version: version}, &mcp.ServerOptions{Instructions: Instructions})

	mcp.AddTool(s, &mcp.Tool{Name: "ask", Description: "Ask a teammate agent to do something or answer something. Waits up to wait_seconds for the reply, otherwise returns a request id to check with get_reply."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in askIn) (*mcp.CallToolResult, any, error) {
			if in.Notify {
				req, err := b.Send(ctx, in.To, in.Message, envelope.KindNotify, "")
				if err != nil {
					return fail(err)
				}
				return text(fmt.Sprintf("Sent to %s (request %s).", in.To, req.ID))
			}
			wait := MaxWait
			if in.WaitSeconds != 0 {
				wait = clamp(in.WaitSeconds)
			}
			res, err := b.Ask(ctx, in.To, in.Message, "", wait)
			if err != nil {
				return fail(err)
			}
			return text(client.FormatResult(res))
		})

	mcp.AddTool(s, &mcp.Tool{Name: "get_reply", Description: "Check on a request you sent with ask."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in getIn) (*mcp.CallToolResult, any, error) {
			res, err := b.Get(ctx, in.RequestID, clamp(in.WaitSeconds))
			if err != nil {
				return fail(err)
			}
			return text(client.FormatResult(res))
		})

	mcp.AddTool(s, &mcp.Tool{Name: "check_inbox", Description: "Pick up requests from teammates. Claims them so no one else handles them. Reply to each when done."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in inboxIn) (*mcp.CallToolResult, any, error) {
			reqs, err := b.Poll(ctx, clamp(in.WaitSeconds))
			if err != nil {
				return fail(err)
			}
			if len(reqs) == 0 {
				return text("No requests waiting.")
			}
			var out strings.Builder
			for _, req := range reqs {
				if _, err := b.Claim(ctx, req.ID); err != nil {
					fmt.Fprintf(&out, "(could not claim %s: %v)\n", req.ID, err)
					continue
				}
				out.WriteString(client.FormatRequest(req))
			}
			return text(out.String())
		})

	mcp.AddTool(s, &mcp.Tool{Name: "claim", Description: "Mark a delivered request as yours to handle. check_inbox already does this."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
			req, err := b.Claim(ctx, in.RequestID)
			if err != nil {
				return fail(err)
			}
			return text("Claimed.\n" + client.FormatRequest(req))
		})

	mcp.AddTool(s, &mcp.Tool{Name: "reply", Description: "Answer a request from a teammate."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in replyIn) (*mcp.CallToolResult, any, error) {
			rep, err := b.Reply(ctx, in.RequestID, in.Message, envelope.Status(in.Status))
			if err != nil {
				return fail(err)
			}
			return text(fmt.Sprintf("Replied to %s (%s).", in.RequestID, rep.Status))
		})

	mcp.AddTool(s, &mcp.Tool{Name: "cancel", Description: "Withdraw a request you sent that nobody has picked up yet."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
			if err := b.Cancel(ctx, in.RequestID); err != nil {
				return fail(err)
			}
			return text("Cancelled " + in.RequestID + ".")
		})

	mcp.AddTool(s, &mcp.Tool{Name: "list_agents", Description: "List teammates, whether each is online, and how each wakes (webhook, email, command, channel, or none)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ noIn) (*mcp.CallToolResult, any, error) {
			agents, err := b.Agents(ctx)
			if err != nil {
				return fail(err)
			}
			var out strings.Builder
			for _, a := range agents {
				state := "offline"
				if a.Online {
					state = "online"
				}
				fmt.Fprintf(&out, "%s: %s, wake=%s\n", a.Name, state, a.Wake)
			}
			if out.Len() == 0 {
				return text("No agents have joined yet.")
			}
			return text(out.String())
		})
	return s
}

func clamp(secs int) time.Duration {
	return min(max(time.Duration(secs)*time.Second, 0), MaxWait)
}

func text(s string) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}, nil, nil
}

func fail(err error) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil, nil
}
