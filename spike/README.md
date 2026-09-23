# U1 spike: transport and identity on the live agents

Throwaway code. It answers four questions before the real relay (U5) is built:

1. Can Grok Bot, Instinct, and Muse each hold an HTTP long-poll to a relay on the Grok Bot VM?
2. How long can a poll be held through Muse's HTTP proxy before something cuts it?
3. Does the relay's `WhoIs` see a distinct tailnet node for each agent (Muse especially)?
4. How can Muse be woken without a human starting its turn?

Build with `make spike`. Binaries land in `spike/bin/` (gitignored).

## Run it

On the Grok Bot VM (it is already on the tailnet as `grok-bot`), either bind the host tailnet IP:

```bash
./spike-relay-linux-amd64 -listen 100.104.237.47 -port 8080
```

or start a separate tsnet node (prints a login URL, or set `TS_AUTHKEY`):

```bash
./spike-relay-linux-amd64 -hostname tincan-spike -dir ./spike-state
```

On each agent, run the poller against that address and save the output:

```bash
./spike-poller-linux-amd64 -url http://100.104.237.47:8080 -agent grokbot  > grokbot.jsonl
./spike-poller-linux-amd64 -url http://100.104.237.47:8080 -agent instinct > instinct.jsonl
./spike-poller-linux-amd64 -url http://100.104.237.47:8080 -agent muse     > muse.jsonl
```

On Muse, run it with Muse's normal environment so `HTTP_PROXY` (port 3130) is set. The poller prints whether it saw a proxy. Use `-sweep` to change hold times (default 10 to 120 seconds).

For question 4, find out on Muse: does a background process survive between turns, and is there a local command or API call that starts a Muse turn?

## Findings

Run 2026-09-22 with a small Python hold server on the Grok Bot VM (100.104.237.47:8080) standing in for spike-relay, because the private repo's binaries could not be fetched by the agents. Same questions, same method.

| Question | Grok Bot | Instinct | Muse |
|---|---|---|---|
| Connects | yes (local) | yes, direct over the tailnet | yes, through `hatch-egress-proxy:3130` (the Tailscale tunnel outside its container) |
| Longest hold that survived | n/a | 120 s (every step 10 to 120 s answered) | 30 s confirmed; the 60 s hold was still open when the server was stopped, so its outcome is unknown |
| `WhoIs` node | grok-bot, nSxH3XiLDr11CNTRL | instinct, ntiLg1kJwr11CNTRL | muse, neDzL7Y5AX11CNTRL |
| Wake without a human | relay calls its webhook (planned) | email to its inbox, or a scheduled wake it sets itself (no API). Background commands do not start a turn and die when a turn ends. | background shell command completion is delivered into a new turn, and background processes survive across turns |

Conclusions for the build:

- Stop conditions cleared: each agent reaches a relay on the Grok Bot VM, `WhoIs` returns a distinct node for each (Muse included, even through its tunnel proxy), and the Grok Bot VM kept a server up for the whole test.
- Keep the 25 s long-poll hold. It is under the 30 s Muse hold that was confirmed.
- Muse's normal egress proxy (`hatch-egress-proxy:3128`) rejects tailnet addresses immediately. Tailnet traffic must use the `:3130` tunnel proxy, so Muse's client config points `HTTP_PROXY` at `:3130` for the relay (or sets the relay URL's proxy explicitly).
- Muse wake: run `tincan wait` in the background. It holds the long-poll and exits as soon as a request arrives, and Muse's runtime delivers that completion into a new turn. A cron check every few minutes backs it up.
- Instinct wake: the relay emails Instinct's inbox through Grok Bot's existing AgentMail inbox, and Instinct sets its own recurring check as a backup. Instinct cannot keep a background listener alive, so it handles requests with a quick inbox check each turn rather than a held poll.
- Path stability: Instinct's second run, 13 minutes after a clean first run, mostly failed to connect through its SOCKS5 tailnet path. Instinct reported Grok Bot reachable only through DERP at about 214 ms, with no direct path. Treat every held poll as disposable: requests stay queued at the relay, clients reconnect with jittered backoff, and no agent depends on a long-lived connection to receive work.

### Found during the local smoke test (2026-09-22, Mac to Mac)

- The poller and relay work end to end over the tailnet, and `WhoIs` through host tailscaled returns the node name, node id, and login.
- Every node on this tailnet (Macs, phone, grok-bot, instinct, muse) is owned by the same login and none is tagged. So "admin actions only from machines owned by Matt's login" (plan KTD2) cannot tell Matt's devices from agent machines. U3 instead keeps an explicit list of admin nodes in relay config, plus the relay host's local socket. This still satisfies R8 (only Matt can invite or remove).
