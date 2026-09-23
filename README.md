# Agent Tincan

Let your AI agents call each other. Grok Bot can ask Muse to make a phone call, Muse can tell Grok Bot how it went, and Instinct can hand either of them work. Your laptop can be off.

## The point

Personal agents now live in different places: a cloud VM, a sandbox that pauses, a container that can only dial out through a proxy, a chat app in someone else's cloud, a terminal on your Mac. None of them can reach the others directly, and a plain webhook can't reach an agent that accepts no inbound connections.

Agent Tincan puts a tiny relay on your Tailscale network. Every agent dials out to it, so nothing needs an open port. The relay knows who sent each request because Tailscale tells it which machine the request came from. There are no keys to manage. Agents you join trust each other like teammates.

## How it works

1. One always-on machine runs `tincan relay`. It joins your tailnet as its own device.
2. You join each agent with a one-time code: `tincan invite muse` on your laptop or phone, then `tincan join <code>` on Muse.
3. Agents get tools, through MCP or the CLI: `ask`, `check_inbox`, `reply`, `get_reply`, `list_agents`, `cancel`, `claim`, `trace`.
4. `ask` queues the request. A listening agent gets it within about a second. An agent that isn't listening gets nudged the way it wakes best: a webhook, an email, a background command finishing, or a Claude Code channel. Otherwise the request waits for its next turn.
5. Chains are tracked (Instinct to Muse to Grok Bot), loops are stopped with a hop limit and a cycle check, and every step lands in a tamper-evident log you can read with `tincan trace`.

ChatGPT can't join a tailnet, so the relay can also publish one OAuth-protected MCP endpoint through Tailscale Funnel for it.

## Quick start

See [docs/quickstart.md](docs/quickstart.md). Guides for specific agents are in [docs/adapters/](docs/adapters/): Grok Bot, e2b sandboxes, proxy-only sandboxes, ChatGPT, and Claude Code.

## Trust model

Joined agents act on each other's requests as if you asked. Read [docs/trust-model.md](docs/trust-model.md) before joining an agent that reads untrusted content and holds powers like spending money.

## Build

```bash
make build   # static ./tincan, CGO_ENABLED=0
make test
```

MIT licensed.
