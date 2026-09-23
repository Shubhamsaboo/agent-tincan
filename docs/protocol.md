# Agent Tincan protocol

Agents talk to the relay over plain HTTP on the tailnet. The relay identifies the sender of every request from Tailscale (`WhoIs`), so nothing in a request body can change who it is from.

## Request

| Field | Set by | Meaning |
|---|---|---|
| `id` | relay | Request id, assigned when queued. |
| `from` | relay | Sending agent, resolved from the tailnet node. A client-supplied value is ignored. |
| `to` | client | Target agent name. Must differ from the sender. |
| `parent_id` | client (the tincan client fills it in automatically) | The request the sender is currently handling, if any. The relay uses it to continue that request's chain. |
| `trace_id` | relay | Chain id, inherited from the parent or new. |
| `hop` | relay | Position in the chain: 1 for a new request, parent hop plus 1 otherwise. |
| `chain` | relay | Agents the request has passed through, oldest first. |
| `kind` | client | `ask` (expects a reply, the default) or `notify`. |
| `body` | client | The request text. Capped at 256 KB. |
| `created_at` | relay | When the relay queued it. |

## Reply

| Field | Set by | Meaning |
|---|---|---|
| `request_id` | relay | The request this answers. |
| `from` | relay | Replying agent, resolved from the tailnet node. |
| `status` | client | `answered` (default), `failed`, or `declined`. |
| `body` | client | The reply text. Capped at 256 KB. |
| `created_at` | relay | When the relay stored it. |

## Request states

`queued`, `delivered`, `claimed`, then one of `answered`, `failed`, `declined`, `cancelled`, or `expired`. A claimed request whose lease expires goes back to `queued`.
