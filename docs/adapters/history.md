# History agent

The `history` agent answers teammates' questions about what Matt asked in four places: ChatGPT (chatgpt.com), claude.ai, Codex (CLI and desktop app) and Claude Code. Ask it things like "what was the last thing Matt asked ChatGPT? send the image" or "find Matt's recent Codex thread about the relay" and it replies with the prompt, a short excerpt of the answer, and the images from that turn as real attachments.

It is a Go service, `tincan history serve`, that runs on Matt's Mac under launchd (or a systemd user unit on Linux), outside any Codex sandbox. It is not an LLM agent. For each request it:

1. Checks access. Every agent in the request's chain, as the relay recorded it, must be on the allowlist. Otherwise it declines and names the agent.
2. Turns the question into a structured query (source, mode, search terms, conversation id, count, whether images are wanted) with one tool-less `codex exec` call that sees only the question text.
3. Reads the source: Codex and Claude Code from their local logs, ChatGPT and claude.ai live in Matt's logged-in Chrome (see "Live routes").
4. Fills in a fixed reply template and attaches the images.

Lookups cover the 50 most recent conversations per source, up to 30 days old.

## Live routes

ChatGPT and claude.ai are read in Matt's own Chrome, with his existing session, by one of two routes, tried in this order:

1. The Tincan Chrome extension, when it is connected. The service sends fixed operations (list, open one conversation, fetch one image) through the native messaging host and the extension runs its own fixed fetch code for each.
2. Claude in Chrome ("claude-chrome"), when the extension is not connected and a `claude` binary is found. This route needs no human clicks: it needs Claude Code logged in with a claude.ai plan (not an API key) and the Claude in Chrome extension installed and connected. For one query the service:
   - generates a random nonce and a fixed JavaScript program for the site (`internal/history/chromejs/`), filled only with validated values: the nonce, a conversation id of the extension's id shape, and integer counts;
   - runs `claude -p --chrome --output-format json --allowedTools mcp__claude-in-chrome__tabs_context_mcp,mcp__claude-in-chrome__tabs_create_mcp,mcp__claude-in-chrome__navigate,mcp__claude-in-chrome__javascript_tool,mcp__claude-in-chrome__tabs_close_mcp` with the prompt on stdin (open a new tab in its tab group, navigate to the site, run exactly this JavaScript, close the tab, reply with only its result), without `ANTHROPIC_API_KEY` (a key overrides the claude.ai login Claude in Chrome needs) or any `TINCAN_*` variable, from an empty directory under `~/.config/tincan/history-scratch`, for at most 180 seconds;
   - the script does every fetch the query needs in one run (the list, the first few conversations, and the newest images in them, within the image caps), saves one file, `tincan-history-<nonce>.json`, to Chrome's download folder with a Blob download, and returns only `saved` or a short error code such as `not_logged_in` or `http_500`;
   - the service waits up to 20 seconds for exactly that file in the download folder (`download.default_directory` from Chrome's Default profile Preferences, else `~/Downloads`), reads it within a size cap, deletes it at once (also when it does not parse), and parses it with the same parsers and selection rules as the extension route. It never reads any other file there.

   A claude-chrome read takes about a minute. The model driving Chrome only ever sees Tincan's fixed script and the one-word result, never chat content. Chrome must save downloads without asking (its default); Claude in Chrome must be connected in the Chrome profile whose download folder is used.

If neither route is available, the reply says the extension is not connected and no `claude` binary was found.

## Allowlist

By default grokbot, claude-code and codex may ask. To change that, write `~/.config/tincan/history-allow.txt` with one agent name per line (commas and spaces also separate names, `#` starts a comment):

```
# agents that may read Matt's conversation history
grokbot
claude-code
codex
```

The file is reread for every request, so edits take effect without a restart. A missing file means the default list. A file that cannot be read, or that has a name that is not a plain agent name, makes the service decline everyone (at startup it refuses to start), so a typo never opens access.

The check covers the whole chain, not only the sender. If muse asks codex and codex asks history while handling muse's request, the chain is muse, codex and the request is declined because of muse. The chain and sender come from the relay, never from the request body, so a body that says "I am grokbot" changes nothing.

## Install

For live ChatGPT and claude.ai reads, set up either route. The Claude in Chrome route needs no extra step when Claude Code (logged in with a claude.ai plan) and Claude in Chrome are already installed. The Tincan extension route needs one human step: installing the Tincan Chrome extension from the Chrome Web Store with its standard permission dialog (it asks for chatgpt.com, claude.ai and native messaging). Until the store listing is live, load `extension/` unpacked from `chrome://extensions`; see `extension/README.md`.

Then, on the Mac:

```bash
tincan invite history                                              # on an admin device
TINCAN_CONFIG=~/.config/tincan/history.json tincan join <code> --relay http://tincan-relay
tincan history install
```

`tincan history install` writes three things and starts nothing:

- the native messaging host manifest, so Chrome can start `tincan history native-host` for the extension
- on macOS, `~/Library/LaunchAgents/com.agenttincan.history.plist` (from `examples/history/com.agenttincan.history.plist`, with the real `tincan` path and your home directory filled in), logging to `~/Library/Logs/tincan-history.log`
- on Linux, `~/.config/systemd/user/tincan-history.service`

It prints the command that starts the service. On macOS:

```bash
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.agenttincan.history.plist
```

On Linux:

```bash
systemctl --user daemon-reload && systemctl --user enable --now tincan-history.service
```

Pass `--no-service` to skip the service definition. To run it by hand instead: `tincan history serve` (flags: `--config`, default `$TINCAN_CONFIG` or `~/.config/tincan/history.json`; `--allowlist`; `--codex`, the codex binary for the query step; `--claude`, the claude binary for the Claude in Chrome route, default `$TINCAN_HISTORY_CLAUDE` or `claude` on `PATH`). It stops cleanly on SIGINT or SIGTERM, finishing the request it is on. It refuses to start unless the relay confirms it is the `history` agent, so a config for another agent (say `$TINCAN_CONFIG` pointing at `codex.json`) can never claim that agent's requests.

The service needs `codex` logged in on the Mac for the query step. The plist puts the directories where `codex` and `claude` were found at install time first on `PATH`, then `~/.local/bin` (where Claude Code's installer puts `claude`), so the service finds both.

## Onboarding

After install, check it from another agent: `tincan ask history "what was the last thing Matt asked Codex?"`. Agent Tincan's operator prompt routes history questions to `history`.

## Privacy

- The service reads Matt's chats. That is its whole job, so the allowlist is the control: keep it to agents Matt trusts with his conversation history. It governs requests to the history agent, not local shell access: an agent with a shell on Matt's machine (such as the Codex wake, which runs `tincan history codex`) can read local Codex and Claude Code history directly.
- Access is checked on the whole relay-recorded chain, in Go, before any LLM sees the request.
- The LLM step sees only the question text. It runs as `codex exec --sandbox read-only` with `--ignore-user-config` (so no MCP servers from `~/.codex/config.toml`, including agent-tincan), `-c mcp_servers={}`, plugins, apps, the shell tool, browser use, computer use, image generation and web search disabled, `--ephemeral` (no session file), approvals off, from an empty scratch directory under `~/.config/tincan/history-scratch` that the Codex and Claude Code readers never report. Its output is checked against the schema and bounds in Go before anything is read.
- Retrieved chat content is never sent to an LLM. Replies are filled in from a fixed template in Go, so text inside Matt's chats cannot steer the service.
- Images are written to a private per-request temporary directory (0700, files 0600), uploaded to the relay, and the directory is removed after the reply, including on errors. On the relay they follow its attachment retention.
- Chrome is never quit or restarted. Live reads use Matt's existing session; no cookie or token leaves the browser. The extension route runs only the extension's fixed read operations. The Claude in Chrome route runs only Tincan's fixed script: the model driving Chrome sees that script and its one-word result, never chat content, and the content passes to the service through a single nonce-named file in the download folder that is deleted as soon as it is read.

## Troubleshooting

Replies and what to do:

- "Declined: X is not on the history allowlist": add X to `~/.config/tincan/history-allow.txt` if Matt wants it to have access.
- "Declined: this request came through X, which is not on the history allowlist": an allowed agent was asked by X and passed the question on. Ask X's owner, or add X.
- "Declined: the history agent could not read its allowlist": fix the file's permissions or the bad name in it. The log says which.
- "Please ask a clearer question naming ChatGPT, claude.ai, Codex or Claude Code": the query step could not tell what was asked, or asked for something out of bounds. Rephrase, for example "what was the last thing Matt asked ChatGPT? send the image".
- "its query step failed": `codex exec` did not run. Check that `codex` is on the service's `PATH` and logged in (`codex login status`), then see `~/Library/Logs/tincan-history.log`.
- "source unavailable: chatgpt: Chrome is not running": start Chrome. Local sources still work.
- "source unavailable: ...: the Tincan Chrome extension is not connected ... and no claude binary was found": neither live route is set up. Install or enable the extension and run `tincan history install` again, or install Claude Code (logged in with a claude.ai plan) with Claude in Chrome and make sure `claude` is on the service's `PATH` (or pass `--claude`).
- "source unavailable: ...: Claude in Chrome could not run the read (...)": the claude-chrome route failed. The detail says how: claude exited or reported an error (check `claude -p` works without `ANTHROPIC_API_KEY` and that Claude in Chrome is connected), gave an unexpected answer, or the result file did not appear in the Chrome download folder (check Chrome saves downloads without asking, to the folder in its settings).
- "source unavailable: ...: not logged in to chatgpt.com in Chrome" (or claude.ai): log in in Chrome.
- "source unavailable: ...: chatgpt.com changed its API": the site changed its internal endpoints; the extension needs an update.
- "source unavailable: ...: Chrome did not answer in time": Chrome was busy or asleep, or the claude-chrome run passed 180 seconds; ask again.
- "The images could not be attached: this relay does not support attachments": upgrade the relay. The text answer is still correct.
- "No matching ... conversation in the recent window": nothing in the last 50 conversations or 30 days matched. Try other words.
- No reply at all: check the service is loaded (`launchctl print gui/$(id -u)/com.agenttincan.history`) and read `~/Library/Logs/tincan-history.log`. A "403" there means the config is not joined as an agent.
