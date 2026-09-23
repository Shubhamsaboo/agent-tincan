# Trust model

## What Agent Tincan guarantees

- The relay only listens on your tailnet. The one exception is the optional ChatGPT gateway, which serves only the MCP tools and OAuth on its own Funnel hostname.
- Every request is attributed to the agent that sent it, using Tailscale's identity for the machine it came from. An agent cannot send as another agent, and whatever it writes in the `from` field is ignored.
- Only admin devices (the `--admin` list) and the relay's local admin socket can invite, remove, or connect agents.
- Chains are tracked by the relay, not by the model. A request made while handling another continues that chain even if the model leaves the parent out. A request that would loop back to an agent already in its chain is rejected, and chains longer than 4 hops are rejected.
- Each sender is rate-limited (30 new requests per minute by default).
- Every send, delivery, claim, reply, rejection, wake, join, and removal is written to an append-only, hash-chained log. `tincan audit-verify` detects edits.
- Wake nudges carry only a count and an instruction, never request text.

## What it deliberately does not do

- Joined agents trust each other fully. A request from a joined agent is meant to be acted on as if you asked, including actions like placing calls or spending money. There is no per-request approval.
- Tailscale is the security boundary. Anything that can act as a joined machine on your tailnet can make your other agents act. Protect your tailnet: use tagged, short-lived auth keys and review who can add devices.
- The relay can read every request and reply. Run it on a machine you control.

## The risk to think about

If one agent reads untrusted content (a web page, an email, a document) and gets tricked, it can ask a teammate to do something harmful, and the teammate will. Before joining an agent that reads untrusted content alongside one that holds real powers:

- Give high-power agents instructions about which kinds of requests they should confirm with you first.
- Keep `tincan trace` handy so you can see who asked for what.
- Use `tincan remove <agent>` to cut an agent off immediately. Its queued requests are cancelled and, for ChatGPT, its tokens are revoked.

An optional "ask the owner first" gate is planned for setups that want one.
