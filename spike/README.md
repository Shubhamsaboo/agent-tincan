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

| Question | Grok Bot | Instinct | Muse |
|---|---|---|---|
| Connects | pending | pending | pending |
| Longest hold that survives | pending | pending | pending |
| `WhoIs` node | pending | pending | pending |
| Wake method | webhook (planned) | e2b resume (planned) | pending |

### Found during the local smoke test (2026-09-22, Mac to Mac)

- The poller and relay work end to end over the tailnet, and `WhoIs` through host tailscaled returns the node name, node id, and login.
- Every node on this tailnet (Macs, phone, grok-bot, instinct, muse) is owned by the same login and none is tagged. So "admin actions only from machines owned by Matt's login" (plan KTD2) cannot tell Matt's devices from agent machines. U3 instead keeps an explicit list of admin nodes in relay config, plus the relay host's local socket. This still satisfies R8 (only Matt can invite or remove).
