package client

import (
	"fmt"
	"strings"

	"github.com/mvanhorn/agent-tincan/internal/envelope"
)

// FormatRequest renders an incoming request for the receiving model. Requests
// come from joined agents, which are trusted teammates, so the framing tells
// the agent to handle them as it would a request from Matt, while keeping the
// sender and chain visible.
func FormatRequest(req envelope.Request) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Request %s from %s (your teammate), via Agent Tincan.\n", req.ID, req.From)
	if len(req.Chain) > 1 {
		fmt.Fprintf(&b, "Chain so far: %s (hop %d).\n", strings.Join(req.Chain, " -> "), req.Hop)
	}
	b.WriteString("Handle it as you would a request from Matt. When you are done, reply with `tincan reply " + req.ID + " \"...\"` (or the reply tool).\n")
	b.WriteString("---\n")
	b.WriteString(req.Body)
	b.WriteString("\n---\n")
	return b.String()
}

// FormatResult renders the state of a request this agent sent.
func FormatResult(r Result) string {
	switch {
	case r.Reply != nil:
		return fmt.Sprintf("%s replied (%s):\n%s\n", r.Reply.From, r.Reply.Status, r.Reply.Body)
	case r.Done():
		return fmt.Sprintf("Request %s to %s ended: %s\n", r.Request.ID, r.Request.To, r.Status)
	default:
		return fmt.Sprintf("No reply yet from %s. Request id %s (status %s). Check later with get_reply or `tincan get %s`.\n",
			r.Request.To, r.Request.ID, r.Status, r.Request.ID)
	}
}
