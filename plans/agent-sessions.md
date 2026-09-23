# Agent sessions (D74)

> Status: live — stages 0–3 implemented and released as v0.3.51 (2026-09-21).
> Auth is the home's, not the vault's (the vault-key path was reverted same-day).

A terminal session whose sandbox runs a coding-agent CLI instead of a
shell, driven over the Agent Client Protocol (ACP, JSON-RPC 2.0 over the
process's stdio, protocol version 1). Decision and rationale:
`DECISIONS.md` D74. Builder docs: `docs/overview/09-terminals.md` §Agent
sessions, `docs/bx.md` (`bx agent`), `docs/protocol.md` (`/term/sessions…`,
`/agent/providers`, `session` events, §Agent session events).

## Layout

```
internal/agent/          the model: Event + Log (ring, Since(cursor)), Permissions
                         (first answer wins, session rules), Driver, the provider table
internal/agent/acp/      the one Driver: JSON-RPC codec (rpc.go), the v1 subset
                         (types.go), the client state machine (client.go)
internal/agent/host/     bx __agent-host — PID 1 in the sandbox: spawns the agent
                         from _xbin/spawn, proxies frames, serves fs/* + terminal/*
                         itself, reaps (reaper.go)
internal/term/agent.go   kind=agent on Session: OpenAgent/createAgent, agentPump
                         (coalescing, lastActive), Prompt/Cancel/Permit/Events/Wait
internal/term/openopts.go the open-options block shared by both kinds
internal/server/agentapi.go the routes; SessionEvent → the `session` hub event
cmd/bx/agent.go          bx agent run|send|permit|attach|ls|stop (cmdExtra)
cmd/bx/agenthost.go      bx __agent-host
hack/fakeacp/            a scripted ACP agent (provider "fake" when XBIN_AGENT_FAKE=<path>)
hack/agent-smoke.sh      stage-0 go/no-go: the four adapters answer initialize in the rootfs
docker/rootfs.Dockerfile pins claude-agent-acp, codex-acp, gemini-cli
```

## Daemon ⇄ host wire

The sandbox leader's stdio, both directions JSON-RPC as-is. The daemon's
first frame is the notification `_xbin/spawn {argv, env, cwd}` — the
agent's full env (the sandbox env — which carries the per-user `$HOME` the
CLI reads its login from — plus the provider's non-secret Env); the host
starts the agent with exactly that env. There are no API keys: the agent
authenticates from its home, like a shell. The host answers `_xbin/hello
{version}`, then proxies every frame except agent→daemon requests with
method `fs/*` or `terminal/*`, which it answers itself. A non-frame line on
the agent's stdout reaches the daemon as `_xbin/log {text}`. The agent's
stderr passes through to the host's stderr, which the daemon keeps as the
session's text log (`GET …/log`). Non-isolated mode (dev, tests): the same
host runs as a plain child of xbind (`Setpgid`, killed as a group) and
makes itself a sub-reaper.

## ACP mapping (v1)

| ACP (direction) | xbind |
|---|---|
| `initialize` (→ agent) `clientCapabilities {fs:{readTextFile,writeTextFile:true}, terminal:true}`, `clientInfo {name:"xbin", version}` | driver `Start`; `status starting` |
| `authenticate` (→ agent) | never called — the CLI authenticates from its `$HOME`; a `-32000` on session/new → `status error` naming the login command to run in a terminal (Provider.LoginHint) |
| `session/new {cwd, mcpServers:[]}` (→ agent) | once per session; `modes` → `status idle {modes, currentMode}`; then `session/set_mode` if the requested mode differs from `currentModeId` (after the CLI loaded its settings) |
| `session/prompt {sessionId, prompt:[{type:"text",text}]}` (→ agent) | `AgentPrompt`: logs `message.delta{user}`, `status running`; the response `{stopReason}` → `turn.end`; one turn at a time (`agent.ErrBusy` → 409) |
| `session/cancel` (→ agent, notification) | `AgentCancel`: unfinished tool calls marked `cancelled` (`tool.update`), pending permissions answered `{outcome:"cancelled"}` + `permission.resolved{by:"cancel"}` — both logged before the agent hears it — then the notification; the prompt's `stopReason:"cancelled"` ends the turn |
| `session/update` `agent_message_chunk` / `user_message_chunk` / `agent_thought_chunk` (← agent) | `message.delta` (role agent/user) / `thought.delta`; `messageId` through; runs coalesced by the pump (32 ms) |
| `tool_call` / `tool_call_update` | `tool.call` / `tool.update` (fields 1:1; `status` defaults to `pending` on a call) |
| `plan` | `plan` |
| `usage_update` | `status {usage}` while running, folded into the next `turn.end.usage` |
| `current_mode_update` | `status {currentMode}` (SessionInfo.mode follows) |
| `session/new` → `configOptions`, `config_option_update` (← agent), `session/set_config_option` (→ agent) | the agent's settings (model, effort, mode as it advertises them): `status {options}` on idle and on every change; `Config.Options` applied after session/new (skipping ids the agent lacks); `AgentSetOption` → `POST …/options` / `bx agent set`; the response's full list replaces ours (SessionInfo.model follows the `model` option). Claude returns `models: null` — the model is a config option |
| `available_commands_update`, `session_info_update`, `config_option_update`, unknown `sessionUpdate`, `_`-prefixed notifications | ignored; unknown requests → `-32601` |
| `session/request_permission {toolCall, options}` (← agent, request) | `permission.request{pid}` pending; `status waiting_permission`; a matching session rule auto-answers (`permission.resolved{by:"auto"}`); else the first `AgentPermit` wins: reply `{outcome:{outcome:"selected", optionId}}`; `allow_always` records the rule (kind + title) |
| `$/cancel_request {requestId}` (← agent) | the referenced pending permission is replied `-32800` and `permission.resolved{by:"cancel"}` |
| `fs/read_text_file {path, line?, limit?}` (← agent) | **host**: absolute paths, 1-based `line`, `limit` lines; `-32602` / `-32002` |
| `fs/write_text_file {path, content}` (← agent) | **host**: `-32602` unless under the session cwd; parents created |
| `terminal/create {command, args, env, cwd?, outputByteLimit?}` | **host**: a PTY (creack/pty) in the sandbox, cwd default = session cwd, `TERM=dumb`; `{terminalId}` |
| `terminal/output` / `wait_for_exit` / `kill` / `release` | **host**: buffer truncated from the start at a character boundary (default 1 MiB); `{output, truncated, exitStatus?}`; kill = SIGKILL the group; release = kill + forget |
| anything else from the agent | forwarded unchanged (the host is transparent) |

## Provider table (`internal/agent/providers.go`)

| id | command | login (in a terminal; its $HOME serves the agent) | modes (default first; *explicit* never a default) |
|---|---|---|---|
| claude | `claude-agent-acp` | `claude /login` | default, acceptEdits, plan, auto, *bypassPermissions* |
| codex | `codex-acp` (`NO_BROWSER=1`) | `codex login` | read-only, agent, *agent-full-access* |
| gemini | `gemini --acp` | `gemini` (Login with Google) | default, autoEdit, plan, *yolo* |
| opencode | `opencode acp` | `opencode auth login` | the agent's |
| fake | `$XBIN_AGENT_FAKE` | none | ask, *yolo* (tests, harness) |

## Stages

- **0 — rootfs + static bx.** Done: `hack/agent-smoke.sh` — opencode,
  claude-agent-acp, codex-acp, gemini answer `initialize` (codex needs
  stdin held open: it exits on EOF before flushing).
- **1 — backend + bx.** Done: the packages above; tests in
  `internal/agent/{,acp,host}`, `internal/term/agent_test.go` (the real
  host + fakeacp end to end: env/home/settings, permissions incl. the
  session rule, terminal without the key, fs write scoping, coalescing,
  replay by cursor, cancel, kill leaves no process), `internal/server/
  agentapi_test.go`, `internal/auth/sessiongate_test.go`, `cmd/bx/agent_test.go`.
- **2 — claude and codex via the adapters, home login.** Verified on a
  box with the stage-0 rootfs: a `claude /login` in a shell terminal serves
  the agent with no vault key; an unsigned provider names its login command.
- **3 — the Agent tab** in the terminal window (`web/bx-agent.js`).

## Out of scope, flagged

ACP v2 (`fs`/`terminal` leave the protocol — the host becomes optional);
MCP servers handed to the agent (`mcpServers: []`); elicitation; image
prompts; per-user "always" rules across sessions.

Landed since (2026-09-22, `internal/term/history.go`): the event log is
persisted when a session ends or the daemon stops (`data/agent-history/`,
per user × tile, newest 20), listed and read back read-only
(`/agent/history*`), and `session/load` resumes a past session where the
agent advertises `loadSession` (`POST /term/sessions {resume}`).
