# AgTT × coding harnesses (ACP) — the spec and the unified API

> Status: **live** (D-harness — numbered at merge). The spec of the program
> on branch `agtt-harness` (from master 7b54e7fb = v0.3.64 + plans). Nothing
> here is built yet; every work package builds to §4–§7 and records what it
> changed under §10. Decisions of this program are "D-harness" until the
> integrator gives them a number at merge (after checking DECISIONS.md and
> the parallel partitions and dev-lifecycle efforts); the regressions fix
> (WP-R) records dated amendments under D133, D135 and D136 instead.

**Contents.** §1 Context · §2 The owner's decisions · §3 Architecture ·
§4 **The unified API** (routes, shapes, events, prefs) · §5 Sandbox-manager
contract additions · §6 `sdk/acp` · §7 The fakes · §8 Work packages ·
§9 Resolved (where the designs disagreed) · §10 Deviations.

## 1. Context

Two systems don't meet yet:

- **xbind's ACP client** — the shell's tile-coding Agent tab (D74, D75, D77,
  D124, D130). It drives Claude Code, Codex, Gemini CLI and opencode through
  their ACP adapters.
- **Coding sandboxes** (D115, D120, D121, D122). Only the builtin agent
  template tile (**AgTT**) consumes them, and its engine is its own LLM loop.

The owner wants AgTT to:

1. act as an ACP client, with web and native (iOS) UI;
2. have harnesses in the base coding-sandbox images;
3. offer a choice when a conversation starts: AgTT's own agent or a coding
   harness;
4. let the AgTT agent spawn harness agents with tasks and steer them, with
   good UI;
5. have a terminal, for harness sign-in and general use.

Harness agents get **no MCP** for now (`session/new {mcpServers: []}`, as
D74). The v0.3.64 agent regressions fix (WP-R) is also in scope and lands
first.

What the plan rests on (verified in the code and in the adapters in
`.rootfs`):

- `internal/agent` and `internal/agent/acp` use only the standard library at
  Go ≤ 1.24, so they move into `sdk/acp` (§6). `client.go` (775 lines) is
  split to fit the sdk's 800-line cap; `internal/agent/host` (pty) stays in
  xbind.
- claude-agent-acp 0.81.1 and codex-acp 1.13.1 implement mid-turn steering
  (`_session/steering`, outcomes `injected` | `promptRequired` |
  `startedNewTurn`, advertised as `InitializeResult._meta.steering.supported`);
  gemini 0.60 doesn't (the queue path).
- D77's terminal-output metadata works with `fs:false, terminal:false`.
- Sign-in without a terminal: codex offers `api-key` and a device code (only
  to a client advertising URL-mode elicitation); gemini offers an api-key;
  claude offers terminal auth (`claude-agent-acp --cli`).
- A non-tty exec's stdin is one POST per write (≤ `stdinMax`; a command that
  doesn't read for 30 s answers 503) and its output is long-polled by byte
  offset with stdout and stderr merged; output must be read as
  `encoding=base64` (text mode splits UTF-8). Execs are `lost` after an
  xbind restart. Every consumer sees every exec of a sandbox it may use.
- The rootfs pins claude-code, codex, claude-agent-acp, codex-acp,
  gemini-cli and opencode on PATH in tile sandboxes (namespace and VM);
  installs are best-effort, so consumers probe.
- Credentials live in the sandbox HOME: shared with everyone who may use the
  sandbox, copied into clones and snapshots. llm-gw can't be reached from a
  sandbox (out of scope).
- AgTT: `Config` is additive; the engine forks at `pass()`;
  `repairTranscript` would clobber in-flight harness calls; `recover()` and
  `hasWork()` miss harness work; today's binary ignores unknown inbox kinds
  (`sortInbox`), which keeps blue/green deploys safe; the subagent/link
  machinery is engine-agnostic; approvals park through `runs.pending`.

## 2. The owner's decisions

### 2.1 Asked and answered (2026-09-29)

- **Terminals are part of the sandbox interface.** Consumer *backends*
  (AgTT, sandbox-terminal, any future tile) can spawn PTYs in a sandbox
  through the manager and relay them to their own pages and native views,
  with the person **asserted**. Every manager implements this, including
  future cloud managers (`ssh -t`). This supersedes D121's "not chosen:
  relaying terminals through the backend". Web may still dial the manager
  directly (verified); native uses the relay.
- **Autonomy is a per-harness setting each person can change: "Auto" or
  "Always approve".**
  - It applies to their harness conversations and to children the AgTT
    agent spawns for them.
  - "Auto" is the provider's auto-edit mode: claude `acceptEdits`, codex
    `agent`, gemini `autoEdit`.
  - Every permission request becomes an **AgTT approval**: cards, Needs,
    push, and the child card or board.
  - Bypass modes stay owner-only and are never chosen by a model.
  - The live mode picker in a conversation can still change the mode.

### 2.2 Decided in the plan (from the designs)

- **Harness sessions render through AgTT's own chat model** (fold.js,
  chat-cards.js, native chat.js), not `/vendor/agent-cards.js`. The web view
  may import only the pure shell helpers `/vendor/agent-tools.js` and
  `/vendor/bx-code.js` (with a fallback, D130 rule).
- **Transport.** The baseline is the existing exec path: stdin plus a base64
  long-poll, stderr sent to a log file by a `sh -c` wrapper, stdin POSTs
  chunked to `stdinMax`, 503 retried. An additive contract capability
  `stdio` (a WebSocket for non-tty execs, separate stderr) is built
  alongside, and the pipe prefers it when offered.
- **Inside the sandbox:** no `bx __agent-host`; advertise `fs:false,
  terminal:false` plus the `_meta` flags (`terminal_output`,
  `terminal_output_delta`, `subagent-transcript`, `terminal-auth`) and
  `elicitation{form, url}`. URL elicitation is honoured only during an
  AgTT-started `authenticate`; any other is declined.
- **Spawning** is a `harness` argument on `subagent_spawn` (one delegation
  verb) plus `harness_mode`. `maxHarness` defaults to 3 running per tree. A
  spawn is refused near the sandbox's exec cap. The parent model never
  answers its children's permission requests.
- **Governance:** a new class toolset `harness`, which requires `sandbox`
  plus a non-`none` sandbox egress (web lane); a class field `harnesses:
  "all" | [ids]`; the built-in `coding` class gains `harness` with `"all"`.
- **Credentials** stay in the sandbox HOME. Logins on shared or team
  sandboxes are allowed after a warning and a confirm. `authenticate` covers
  api-key and device code; AgTT relays a key once and never stores it. A
  per-person vault key is deferred.
- **Privacy:** a private conversation's harness traffic can be read by the
  sandbox's co-users. The UI and API.md say so.
- **Conversation start:** a "Who answers" picker in the composer at home and
  in the new-chat dialog, plus the class. Once a conversation starts, both
  are fixed; so are its sandbox and cwd.
- **Environment:** the `coding-sandbox` default env gains `IS_SANDBOX=1`;
  tilesbx tty execs default to `TERM=xterm-256color`,
  `COLORTERM=truecolor` and `LANG=C.UTF-8`.
- **An idle harness is reclaimed** after 15 min (tile config
  `harnessIdleMin`), with a one-shot timer — no tickers.
- **D-numbers:** none for WP-R; "D-harness" here, numbered at merge.

### 2.3 Not in v1

MCP for harnesses; llm-gw for harness model traffic; a per-person vault key;
a class switch that forbids bypass modes (bypass is owner-only already);
per-turn "files changed" with a patch route (per-call diffs only); the
parent model answering a child's permission; live streaming of a
harness-internal subagent's own text (it lands when flushed, §3.4).

## 3. Architecture

```
AgTT page (web / native) ── AgTT backend ── harness engine (per run: acp.Client over a Pipe)
                                              │ sdk/acp (moved from xbind)    │ Pipe = exec + stdin + base64 long-poll
                                              │                                │        (or the stdio WebSocket when offered)
                                              ▼                                ▼
                        sandbox-manager contract ──► coding-sandbox ──► tilesbx runtime ──► sandbox:
                        (+ images[].harnesses, + stdio,                                      claude-agent-acp | codex-acp |
                         + consumer PTYs)                                                    gemini --acp | opencode acp
```

### 3.1 A harness run

A **harness run** is an AgTT run with `runs.engine = 'harness'` and, in its
config snapshot (additive `Config` fields):

```go
Engine  string         `json:"engine,omitempty"`  // "" | "harness" — fixed for the run's life
Harness *HarnessConfig `json:"harness,omitempty"`
type HarnessConfig struct {
	Provider string            `json:"provider"`          // a catalog id (§4.3.10)
	Mode     string            `json:"mode,omitempty"`    // the provider mode to (re)start in
	Options  map[string]string `json:"options,omitempty"` // config options applied after session/new|load
	Ref      string            `json:"ref"`               // the sandbox, fixed for the conversation
	Cwd      string            `json:"cwd,omitempty"`
	By       string            `json:"by,omitempty"`      // who started it (a user id, "el:<component>", "")
}
```

Tile-level config (GET/PUT `/config`, managers) gains `harnessIdleMin`
(default 15; 0 = never reclaim) and `maxHarness` (default 3, running harness
children per tree).

Storage (WP-A6; all additive, migration tested against old rows):

| Table / column | Keyed by | Holds |
|---|---|---|
| `runs.engine` | run | `''` or `'harness'`; immutable; read for dispatch and lists |
| `harness_sessions` | `run_id` | `root_id, ref, cwd, provider, argv, exec_id, client_id, gen, state, acp_session, loadable, steering, read_off, err_off, prompt_rpc, prompt_state ('' \| 'sending' \| 'sent'), turn, snapshot (acp.SessionState json), rules, plan, usage, counts, login, queue (permission/elicitation requests waiting behind the park), held (a prompt kept for retry), error, last_active_ms, created_ms, updated_ms` |
| `harness_prefs` | `(user, provider)` | `mode ('auto' \| 'approve'), updated_ms` — the per-person setting (§4.3.12) |
| `harness_seen` | `(ref, provider)` | `installed, signed_in, at` — what a probe or a session last learned about a sandbox |
| `harness_options` | `provider` | the last config options any session of it reported (the home model picker's list) |

`harness_sessions.state` is the storage state: `none | starting | live |
stopped | lost | failed | login`. The API presents it as `harness.state`
(§4.3.2).

### 3.2 Inbox kinds

Every input is still an inbox row (inbox.go), except `authenticate`, which
carries a secret and is never stored.

| Kind | Written by | The harness pass |
|---|---|---|
| `hprompt` (new) | `POST /runs/{id}/message` and `/answer` on a harness run; the first message of a new harness run (instead of a directly written user row); `subagent_message`; an approval's `feedback` | prompt, steer or queue (§3.5); writes the user row (+ `recordAsk`) when it is delivered |
| `hanswer` (new) | `POST /runs/{id}/harness/answer` | answers the parked elicitation |
| `hnote` (new; a row of `harness_notes`, not `inbox` — routes-fix) | a person's message to a harness **child**, written on the **parent** | delivered as a notice (§4.3.13) at the parent's next step boundary, or before its next turn's first message, after the inbox rows there when it was written; never starts a turn (not work for `hasWork`, and invisible to an older binary's) |
| `approve` | `POST /runs/{id}/approve` (body gains `option`, `feedback`) | answers the parked permission |
| `interrupt` | `/interrupt`, `message {interrupt:true}` | `session/cancel`; parks settle "(interrupted)" |
| `cancel` | `/cancel`, delete | `session/cancel`, kill the exec, status `canceled`, a child's link settles canceled |
| `wake` | `/resume` | (re)spawn when not live, then resend `held` |
| `compact` | `/compact` | sends `/compact` when the harness advertises it (else the route refused it) |

Today's binary ignores unknown kinds, so a harness run is never LLM-driven
by an older process during a blue/green overlap. Rolling back to a binary
without this program (documented caveat): its `hprompt` rows wait, but
that binary's pass would still reach its model loop for a harness run it
finds `running` (recover), one parked and answered (`approve`, a reply,
`/resume`) or one a person messages through it (its own `user` row). So a
harness run's `runs.turn_steps` is always at maxTurnSteps' ceiling (500),
kept there by two triggers (`harness_turn_cap_*`, also through such a
binary's own turn start): its `turn()` ends every turn it starts there at
once, at its step cap ("stopped after 500 steps in one turn
(maxTurnSteps)"), before any model call or compaction — only its
`repairTranscript` (the calls in flight read interrupted) and, for an
approved park, the call "run" as an unknown tool (an error result) happen
first. The adapter itself runs on unwatched until its sandbox stops, or a
newer binary takes it over again. Such a binary's `hasWork` counts every
undelivered inbox row and its `recover()` pokes each one's run, but it
never consumes a kind it doesn't know: `hprompt` and `hanswer` rows queued
at the rollback make its resume job (`@every 1m`) wake the tile every
minute until the conversation is deleted, or the rows are:
`DELETE FROM inbox WHERE kind IN ('hprompt','hanswer') AND delivered_at=0`.
`hnote`s, which an idle parent keeps by design, live in `harness_notes`
for that reason.

### 3.3 The engine fork and sessions

- `pass()` checks `run.Engine == "harness"` right after `sortInbox` and hands
  off to `harnessPass`; `turn()` gets a guard before `repairTranscript`,
  which returns early for harness runs (also at its other call site).
  Priority in `harnessPass`: cancel > interrupt > halt > answers (approve,
  hanswer) > wake/retry > prompts > compact.
- Harness runs never reach an LLM wire: compaction, memory, recall, skills,
  schedules, channels, auto-titling by the model and `/learn` are off. The
  title is the first message clipped, then the adapter's
  `session_info_update.title` while the title source is `clip`.
- Registry `e.harness[run]`; the keep-alive hold is wanted while it is
  non-empty. `recover()` gains `UNION SELECT run_id FROM harness_sessions
  WHERE state IN ('starting','live')`; `hasWork()` gains `prompt_state <>
  ''` and any running adapter (Afix: an idle one too — the resume job
  brings a next process that attaches it and re-arms its idle reclaim).
  `BeginShutdown` stops the pumps with `errHandoff` and never kills
  execs; stdin writes check ownership (the epoch) first.
- **ensure** (every prompt): `sandboxUse` (binder rights, class, taint,
  egress ≠ `none` — "Claude Code must reach its provider — ‹sandbox›'s
  egress is none"); probe (§5.1) when not known installed; spawn (§3.6);
  `Client.Start` with `ResumeID` when loadable.
- **attach** after a handoff: the pipe from `(exec_id, read_off)`,
  `Client.Start` with `Attach` = the stored snapshot plus the pending
  requests, and `Conn.Expect(prompt_rpc)`.
- **Consistency.** One consumer goroutine per session applies events in
  order. Durable events (everything but message/thought chunks and terminal
  output deltas) commit their rows, `read_off = ev.Wire.Off` and the snapshot
  in **one fenced transaction**. Chunks and output deltas update only memory
  (the draft; the in-memory tool row, published as a message upsert at most
  every 250 ms by a one-shot timer) and are stored with the next durable
  event; replaying from `read_off` rebuilds exactly what wasn't stored. A
  prompt left `sending` on reattach may never have reached the adapter: it
  fails ("send it again") — at most once, never double-sent.
- **lost / killed / exited:** a prompt in flight ends the turn with a note;
  pending parks settle "(interrupted)"; `state = lost` (xbind restart,
  sandbox stop, ring gap past resync) or `stopped` (idle reclaim, clean
  exit); the next prompt respawns, with `session/load` when loadable (the
  replay is ignored via `Wire.Replay`).
- **idle reclaim:** a one-shot timer at `last_active + harnessIdleMin`,
  armed only while the session is live with no turn and no park. An idle
  adapter holds off the sandbox's idle stop; this reclaims it.
- **Binder rights** are re-checked on every prompt, steer and approval, and
  at most once a minute on durable events; a failure sends `session/cancel`
  and kills the session. **Detach:** `storeBinding` stops harness sessions
  on a detached ref (like `stopDetachedJobs`).

### 3.4 Mapping ACP to AgTT rows

| ACP | AgTT |
|---|---|
| `agent_message_chunk` | the run's draft text (the existing `text` draft events); flushed to an assistant row at the next tool call, permission, elicitation, steer injection, turn end or loss |
| `agent_thought_chunk` | draft thinking; stored in the assistant row's `Meta.reasoning` at flush |
| chunk with `_meta…parentToolUseId` (a harness-internal subagent) | buffered per parent call, flushed to an assistant row with `Meta.harness.parent` when that call completes or makes its next call; not streamed live |
| `user_message_chunk` (echo, replay) | ignored |
| `tool_call` | flush, then in one transaction **one assistant row per call** (`toolCalls: [{id, name: "acp:<kind>", arguments}]`, §4.3.5) and its tool row, content `(running…)`, `Meta.harness` = §4.3.5 |
| `tool_call_update` | merged into the tool row's `Meta.harness` (status, diffs, output, exit code, title, rawInput — a changed title/rawInput also rewrites the assistant row's arguments); on completed / failed / cancelled the tool row's content is CAS'd to the result text |
| `plan` | `harness_sessions.plan` + a `harness` event; the turn's last plan is journaled at turn end as a step of kind `plan` (not shown in the chat) |
| `session/request_permission` | status `waiting_input`, `pendingState{kind:"approval", …}` (§4.3.4); the call's tool row reads `(awaiting your approval)` |
| `elicitation/create` (form) | `pendingState{kind:"question", …}` |
| `elicitation/create` (url) | honoured only during an AgTT `authenticate`: accepted, shown as `harness.login.device`; declined otherwise |
| `_auth/status_update{kind:none}`, a -32000 prompt error | `state = login`, status `waiting_input`, `pendingState{kind:"login"}`, the failed prompt kept in `held` |
| `current_mode_update`, `config_option_update`, `available_commands_update`, `usage_update`, `session_info_update` | the snapshot + a `harness` event |
| turn end | flush; `idle` (end_turn → answered; max_tokens / max_turn_requests / refusal → incomplete, with a note; cancelled → interrupted) or `error` (an adapter error); a child calls `settleOwnLink` with the turn's last text — this turn's only (after `harness_sessions.turn_seq`, its first row), `(no answer)` when it wrote none; queued prompts go next |

Stored call ids are `h<gen>:<acp toolCallId>` (a respawn may reuse an
adapter's ids); `parent` ids are mapped the same way. At most one park at a
time: further permission requests and elicitations wait in
`harness_sessions.queue`, in order, and each becomes the park when the one
before is answered.

### 3.5 Steering, interrupt, cancel

- A message while no turn runs is the next `session/prompt`.
- A message during a turn, when the adapter steers
  (`harness.steering`): `_session/steering` with `idleBehavior:
  promptRequired`. `injected` records the user row (+ ask ledger) at once;
  `promptRequired` sends it as the next prompt; `startedNewTurn` records it
  and follows the new turn.
- A message during a turn, without steering: queued until the turn ends
  (D81's step-boundary rule).
- `interrupt: true` on a message, or `/interrupt`: `session/cancel`; the
  message (if any) is the next prompt; `/interrupt` returns the caller's
  still-queued messages to the composer (today's rule).
- A message while an approval is parked answers it `reject_once` first (a
  question: `decline`), then is steered or queued — the LLM path's rule.
- Cancel: `session/cancel`, kill the exec, status `canceled`, a child's link
  settles canceled. On a run that rests with its adapter up (no turn, no
  park) the cancel row still goes (`cancelRuns`) and the pass stops the
  adapter (`stopped`, a note) — the status stays: there is no turn to
  cancel (Afix).

### 3.6 The pipe (WP-A7, `harness_pipe.go`, builds the `acp.Process`: Stdin, Stdout, Wait, Kill)

- **Start.** `POST …/execs` with `argv: ["sh", "-c", 'L="${HOME:-/tmp}/.cache/xbin-harness/$1.log"; shift; mkdir -p "${L%/*}"; exec "$@" 2>"$L"', "h", "<run>-<gen>", <provider argv…>]`,
  `stdin: true`, the binding's `cwd`, `label: "harness <provider> · #<root>"`,
  `clientId: "harness:<run>:<gen>"`, env = the provider's `Env` +
  `IS_SANDBOX=1` + `NO_COLOR=1`. With `stdio` offered: `split: true` and no
  wrapper — stderr is the exec's own stream (`…/output?stream=stderr`).
- **Read** (baseline). `…/output?since=<off>&max=1048576&waitMs=25000&encoding=base64`;
  re-poll at once after a full chunk, else after ≥ 20 ms. A gap (`start >
  since`) resyncs the decoder at the next newline and journals "N bytes of
  Claude Code's output were lost".
- **Write.** One writer; a frame is split into POSTs of ≤ `stdinMax`; a 503
  is retried with backoff for up to 2 min. `InlineBudget` is 1.5 MiB; bigger
  images go as `resource_link`s to files dropped through the files API.
- **End.** `exited` / `killed` is EOF; a 410 `lost` is EOF + lost.
  **Kill:** stdin eof, TERM after 3 s, `DELETE` after another 3 s.
- **stdio** (when `hello.caps` has it): one WebSocket (§5.3) from
  `(read_off, err_off)`; the newest attacher wins, which suits a handoff.

## 4. The unified API

Both halves build to this section: the backend (WP-A6, A8, A9, A10) serves
it, the UI (UI-U1…U9, web and native) consumes it, and UI-U1's
`test/backend.mjs` STUB serves exactly these shapes until WP-A9 lands.
Everything is **additive**: a built-in conversation answers exactly as
today, old tiles ignore the new fields, event types and routes.

### 4.1 Conventions

- Paths are the tile's API (`/api/<agtt>/…` from a page). Bodies are JSON;
  errors are `{error}` with the status given (AgTT's `xbin.WriteError`);
  sandbox refusals pass the contract's `{error, refusal, state?}` through
  (`writeSbxErr`).
- **Who** uses routes.go's needs: `any` (anyone but cron; the handler
  filters), `start`, `viewer`, `participant`, `owner` (of the run's root
  conversation), plus the extra checks stated. "A person" means `whoUser`
  without `viewedBy`. A run the caller may not see is 404; one they see but
  may not change is 403.
- **Owner-only** below means the root conversation's owner, a person.
- Harness-only routes on a built-in run answer 409 `not a coding-agent
  conversation`.

### 4.2 Routes

New routes are marked **new**; the others are existing routes that gain
fields or behaviour. "WP" is who builds it.

| Method & path | Who | Body / query | Answer | WP |
|---|---|---|---|---|
| `GET /harnesses` **new** | any | `?probe=<ref>` | the catalog (§4.3.10) | A6 |
| `GET /prefs/harness-mode` **new** | any; a person | — | `{modes: {"<provider>": "auto"\|"approve"}}` (set ones only) | A6 |
| `PUT /prefs/harness-mode/{provider}` **new** | any; a person | `{mode: "auto"\|"approve"}` | `{provider, mode}` | A6 |
| `POST /ask` | start | `+ harness: {provider, mode?, options?}` | the run (as today; `engine: "harness"`) | A9 |
| `POST /runs` | start | `+ harness: {provider, mode?, options?}`, `+ sandbox: {ref, cwd?}` | the run | A9 |
| `GET /runs/{id}/harness` **new** | viewer | — | `{harness, session, rules}` | A9 |
| `PATCH /runs/{id}/harness` **new** | participant; explicit mode: owner | `{mode?, option?: {id, value}}` | `{harness}` | A9 |
| `POST /runs/{id}/harness/answer` **new** | participant | `{park, action, content?}` | `{ok: "true"}` | A9 |
| `POST /runs/{id}/harness/authenticate` **new** | participant; a person with sandbox Use | `{method, apiKey?, confirm?}` | 200 `{ok, state}` \| 202 `{ok, device}` | A9 |
| `GET /runs/{id}/harness/log` **new** | viewer; a person with sandbox Use | `?max=<bytes ≤ 65536>` | `text/plain`, the stderr log's tail | A9 |
| `GET /runs/{id}/harness/terminal` **new** | participant; a person with sandbox Use | `?login=1&rows=&cols=&exec=` | WebSocket, the `/ws/term` wire | A9 |
| `GET /sandboxes/{ref}/terminal` **new** | any; a person with sandbox Use | `?cwd=&cmd=&rows=&cols=&exec=` | WebSocket, the `/ws/term` wire | A9 |
| `POST /runs/{id}/approve` | participant; explicit option: owner | `{approve?, park?, option?, feedback?, grant?}` | `{ok: "true"}` | A9 |
| `POST /runs/{id}/message` | participant | `{text, files?, clientId?, interrupt?}` | `{ok, inboxId, queued}` (as today) | A9 |
| `POST /runs/{id}/answer` | participant | `{text, …}` | as `/message` on a harness run | A9 |
| `POST /runs/{id}/interrupt`, `/cancel`, `/resume` | participant | as today | as today | A8/A9 |
| `DELETE /runs/{id}/inbox/{iid}` | participant | — | as today; also for `hprompt` rows | A9 |
| `GET /runs/{id}/view`, `/stream`, `GET /stream` | viewer | as today | + `run.engine`, `run.harness`, tool rows' `acp`, the `harness` event | A8/A9 |
| `GET /conversations`, `GET /needs` | any | as today | rows + `engine`, `harness`, `waiting`, `kids`; needs reason `login` | A9 |
| `GET /runs/{id}/tree` | viewer | — | nodes + `engine`, `harness` | A9 |
| `GET /classes`, `PUT /classes` | any / manager | classes + `harnesses`; toolset `harness` | as today | A6 |

#### 4.2.1 `GET /harnesses`

The coding agents the caller could start, and why not. Answers in ≤ 10 s:
managers' hellos come from the cache (`managerHello`); `?probe=<ref>` also
probes that sandbox now (§5.1) when it is running and the caller may use it
(a stopped sandbox is not started by a probe), and the answer carries the
result in `sandboxes[ref]`. Probes are cached per ref for 10 min.

#### 4.2.2 `/prefs/harness-mode`

The per-person Auto / Always approve setting (§4.3.12). `PUT` errors: 400
`mode is "auto" or "approve"`; 400 `no coding agent "<id>"`; 400 `<name> has
no auto mode — it asks as its own settings say` (`auto` for a provider whose
catalog `autoMode` is empty); 403 `the setting is a person's own`.

#### 4.2.3 `POST /ask`, `POST /runs` with `harness`

```json
{"text": "fix the flaky test", "harness": {"provider": "claude", "mode": "acceptEdits", "options": {"model": "opus"}},
 "sandbox": {"ref": "apps/coding-sandbox|sb-7f3a", "cwd": "/work/api"}, "class": "coding"}
```

- `harness.provider` (required): a catalog id available to the caller.
- `class`: optional; when given it must allow the harness; when absent the
  caller's default class if it allows it, else the first class they may use
  (in `GET /classes` order) that does.
- `sandbox`: the sandbox, **required** (the tile's defaults never hold one
  — `PUT /config` clears it); fixed for the conversation, together with
  `cwd`. `POST /runs` gains it as `/ask` has it (bound by `askSandbox`,
  as the owner-to-be) and keeps `goal` for the text. Its
  (manager, image) must be in the catalog's `images` for the harness
  (§4.3.10), and its egress (the less restrictive of
  `egress`/`egressNext`) must not be `none`. A running sandbox is probed
  now (cached) and refused when the harness is missing; a stopped one is
  probed at the first prompt, which fails the run's harness (`failed`,
  "‹sandbox› doesn't have ‹name› (claude-agent-acp not found)").
- `harness.mode`: a provider mode id; default: the caller's setting (§4.3.12)
  mapped to the provider's mode; an **explicit** mode — any the catalog
  doesn't know to be safe (§4.3.12) — only from a person (the
  conversation's owner-to-be).
- `harness.options`: config options (`{"model": "…", "effort": "…"}`)
  applied after `session/new`/`load`; one the adapter refuses is a journal
  note, not an error. They never carry the mode (else they would bypass
  the explicit-mode rule): a key `mode`, or one the catalog's last-seen
  `options` gives category `mode`, is 400 `harness.options: the mode is
  harness.mode`, and the engine skips (with a journal note) any option the
  live adapter reports as category `mode`.
- `hold`, `draft`, `files`, `title` work as today; the first message becomes
  the first prompt (an `hprompt` row, §3.2).
- Errors: 400 `harness.provider: no coding agent "x" (GET /harnesses lists
  them)`; 400 `class: the ‹class› class doesn't allow ‹name›`; 403 `no class
  you may use allows ‹name›`; 400 `harness.mode: one of …`; 403 `only a
  person can start ‹name› in ‹mode name›` (explicit mode); 400 `system: a
  coding agent keeps its own instructions — system is for the built-in
  agent`; 400 `model: a coding agent's model is harness.options.model`; 400
  `a coding agent needs a sandbox: sandbox {ref, cwd?} whose image has
  ‹name›`; 409 `‹name› must reach its provider — ‹sandbox›'s egress is
  none`; 409 `‹sandbox›'s image doesn't have ‹name›`; the binding's own
  refusals as today.
- Schedules, triggers and channels never start or drive a harness
  conversation: a `harness` in their bodies is 400 `schedules run the
  built-in agent` (and likewise for triggers).

#### 4.2.4 `GET|PATCH /runs/{id}/harness`

`GET` → `{"harness": <§4.3.2>, "session": {"gen", "execId", "acpSessionId",
"loadable", "steering", "startedAt", "lastActive"}, "rules": [{"kind",
"title"}]}` (`rules`: what `allow_always` answers remember in this
conversation).

`PATCH {mode?: "<mode id>", option?: {id, value}}` — one or both. On a live
session the handler calls the adapter (`session/set_mode`,
`session/set_config_option`) and answers the refreshed summary; either way
the choice is stored in `Config.Harness` for the next start. A config
option of category `mode` is never listed in `options` (the mode picker
covers it; the backend routes a mode change to whichever the adapter
speaks), and `option.id` must be one listed — so a mode change always
goes through `mode` and its owner-only rule for explicit modes (every
mode §4.3.12 doesn't open). A mode the adapter took with an option it
refused stores the mode and answers the option's refusal (Afix). Errors:
400 `mode: one of …` / `option: one of …` / `value: one of …`; 403 `only ‹owner› can switch ‹name› to ‹mode name›` (explicit mode);
502 `{error: <the adapter's words>}`; 503 with `Retry-After: 1` while
another process holds the session (a handoff).

#### 4.2.5 `POST /runs/{id}/harness/answer`

`{park, action: "accept" | "decline" | "cancel", content?}` answers the
parked question (`pendingState.kind == "question"`); `content` is the form's
values (an object) with `accept`. Queues an `hanswer` row. Errors: 400 `no
pending question`; 409 `that question is no longer pending — the agent is
asking something else now` (park mismatch); 400 `action is accept, decline
or cancel`; 400 `content: an object with the form's fields`.

#### 4.2.6 `POST /runs/{id}/harness/authenticate`

`{method, apiKey?, confirm?}`. `method` is an id from
`harness.login.methods` of kind `api-key` or `device-code` (terminal
methods use the terminal, §4.2.8). The request goes straight to the live
session in this process — **never an inbox row, never journaled, never
stored**; the key rides `authenticate._meta["api-key"].apiKey` once. The
engine starts the adapter (initialize only) when none is live.

- api-key → waits for the result (≤ 30 s): 200 `{ok: "true", state:
  "ready"}`, the held prompt resent.
- device-code → waits for the URL elicitation (≤ 30 s): 202 `{ok: "true",
  device: {url, message}}` — the requester's alone (routes-fix): kept in
  memory with the sign-in, served again only to them (their `GET
  /runs/{id}/harness`, or the same method asked again); stored and
  published is `harness.login.device: {by}`. The run leaves `login` by
  itself when the person finishes.
- Errors: 400 `method: one of …`; 400 `apiKey: needed for ‹method name›` /
  `apiKey: only for an API-key method`; 409 `‹name› is signed in` (state not
  `login`); 403 `only someone who may use ‹sandbox› can sign it in`; 409
  `{error: "anyone who may use ‹sandbox› acts as you with ‹name› there —
  confirm to sign in", confirm: true}` without `confirm: true` on a sandbox
  shared with others (team visibility, members or shares); 502 `{error:
  <the adapter's words>}`; 504 `‹name› didn't start its sign-in`; 503 as
  §4.2.4.

#### 4.2.7 `GET /runs/{id}/harness/log`

The tail (≤ `max`, default and cap 64 KiB) of the adapter's stderr:
`<sandbox home>/.cache/xbin-harness/<run>-<gen>.log` read through the files
API, or, for a split exec (§5.3), `…/output?stream=stderr`, with
`Sbx-User: <caller>`. A manager doesn't police an asserted person
(docs/sandbox-manager.md §Who is asking), so AgTT checks the caller's own
`sandboxAccess(caller).Use` first, fresh from the manager, as for the
terminals (the file lives in a HOME the sandbox's users can write). 403
`only a person who may use ‹sandbox› can read its log`; 404 `no log yet`.

#### 4.2.8 The terminal relays

Both are WebSocket upgrades that speak `/ws/term`'s wire end to end (the
contract's §Terminals: the session frame first, binary frames both ways,
`resize`/`ping`/`pong`/`exit`). The backend dials the manager's `tty`
route as the tile with `Sbx-User: <caller>` (**asserted**, D-harness,
superseding D121's "not chosen") through `xbin.RelayManagerTTY` (§5.2) and
relays byte for byte. The runtime refuses a person with `noTerminal` (D88)
at every attach (`forUser`). Refusals come before the upgrade, as JSON.

- `GET /runs/{id}/harness/terminal?login=1&rows=&cols=&exec=` — in the
  harness's sandbox, at its cwd: `login=1` runs `harness.login.command`,
  otherwise the login shell; `exec=<id>` attaches again to that tty exec
  (the session frame's `id`) instead of starting one. Needs participant
  access to the run **and** `sandboxAccess(caller).Use` on the sandbox
  (fresh from the manager). Native's sign-in and "Terminal" use it.
- `GET /sandboxes/{ref}/terminal?cwd=&cmd=&rows=&cols=&exec=` — any
  terminal in a sandbox the caller may use (`cmd` as the contract's `tty`
  route: the login shell unless given). Registered inside the existing
  `GET /sandboxes/{ref...}` handler: a path ending in `/terminal` is the
  relay (a sandbox id never contains `/`). Native's Sandboxes row and ▣
  Sandbox screen use it; web keeps dialing the manager directly (verified).
- Errors: 400 `a terminal is a WebSocket upgrade`; 403 `only a person can
  open a terminal`; 403 `you may not use ‹sandbox› — ask ‹binder›`; 404;
  409 (built-in run on the run relay); the manager's refusals (`unsupported`
  501 without `tty`, `state` 409 archived) passed through.

#### 4.2.9 `POST /runs/{id}/approve`

`{approve?, park?, option?, feedback?, grant?}`. On a harness park:

- `option` (an `optionId` of `pendingState.harness.options`) wins;
  otherwise `approve: true` picks the first non-`explicit` `allow_once`
  (else the first non-`explicit` `allow_*`; none → 400 `option: name one
  — every allow here raises ‹name› to an explicit mode`), `approve: false`
  picks `reject_once`, then `reject_always`, then the `cancelled` outcome.
- `feedback` (plan approval's "keep planning") is valid only with a reject
  verdict: the rejection is answered, then `feedback` is queued as the
  caller's next message (`hprompt`).
- An option whose `explicit` is true (it raises the session to a bypass
  mode) is **owner-only**. `switch_mode` answers never become rules.
- `grant` is ignored on a harness park.
- Errors (added to today's 400 `no pending approval` and 409 park
  mismatch): 400 `option: one of …`; 400 `option is for a coding agent's
  permission request` (on a built-in park); 400 `feedback goes with a
  rejection`; 403 `only ‹owner› can allow ‹option name›`.

#### 4.2.10 `POST /runs/{id}/message` (and `/answer`)

On a harness run the row is an `hprompt` (§3.2) and delivery follows §3.5.
`interrupt: true` interrupts the running turn first (only the caller's own
message jumps the queue; other people's queued messages stay). On a built-in
run `interrupt` is ignored (the message steers at the next step boundary, as
today). The answer is unchanged: `queued` says whether a turn or park was
active. `queued` in the view, `DELETE /runs/{id}/inbox/{iid}` and
`/interrupt`'s `returned` include `hprompt` rows like `user` rows.

When a **person** messages a harness **child** directly, an `hnote` is
written on its parent (§4.3.13).

#### 4.2.11 Existing routes on a harness run

| Route | On a harness run |
|---|---|
| `POST /runs/{id}/interrupt` | `session/cancel`; the caller's queued messages come back (today's rule) |
| `POST /runs/{id}/cancel`, `DELETE /runs/{id}` | `session/cancel` + kill the exec — also of an idle adapter (the run's status then stays, §3.5) |
| `POST /runs/{id}/resume` | respawn a fresh adapter when not live (it re-reads credentials) and resend `held` — the "Signed in? Retry" and "Retry resumes its session" buttons |
| `POST /runs/{id}/compact` | sends `/compact` when `harness.commands` has it; else 409 `‹name› has no /compact` |
| `PUT/DELETE /runs/{id}/memory`, `POST /runs/{id}/learn` | 409 `a coding agent has no memory` / `… can't learn a skill` |
| `PATCH /runs/{id}` `model` | 400 `a coding agent's model is an option: PATCH /runs/{id}/harness {option: {id: "model", …}}` |
| `PATCH /runs/{id}` `sandbox`, `detach` (of its sandbox) | 400 `a coding agent's sandbox is fixed for the conversation` |

### 4.3 Shapes

#### 4.3.1 `runSummary.engine` and the `Run` JSON

`runSummary` (run events, the view's `run`, conversation rows, a link's
`child`) and the `Run` JSON gain `engine: "" | "harness"`. Every place that
serves a harness run's summary also carries `harness` (§4.3.2); a built-in
run has no `harness` key.

#### 4.3.2 `harness` — the summary object

```json
{"provider": "claude", "name": "Claude Code",
 "state": "working", "error": "",
 "mode": {"current": "default",
          "available": [{"id": "default", "name": "Ask before acting", "description": "…"},
                        {"id": "bypassPermissions", "name": "Bypass permissions", "explicit": true}]},
 "options": [{"id": "model", "name": "Model", "category": "model", "description": "…", "currentValue": "opus",
              "options": [{"value": "opus", "name": "Opus", "description": "…"}]}],
 "commands": [{"name": "review", "description": "Review the pending changes", "hint": "what to focus on"}],
 "usage": {"used": 52000, "size": 200000, "cost": {"amount": 0.41, "currency": "USD"}},
 "plan": {"entries": [{"content": "Write the test", "status": "completed", "priority": "high"}]},
 "activity": {"kind": "tool", "title": "Run go test ./...", "at": 1790000100000},
 "counts": {"tools": 12, "files": 3, "add": 40, "del": 7},
 "pending": {"park": "Xq3…", "kind": "approval", "title": "Run go test ./..."},
 "login": {"command": "CLAUDE_CODE_REMOTE=1 claude /login",
           "methods": [{"id": "claude-login", "name": "Log in with Claude", "kind": "terminal"}],
           "device": {"by": "alice"}},
 "sandbox": {"ref": "apps/coding-sandbox|sb-7f3a", "name": "api-dev", "cwd": "/work/api", "shared": true},
 "steering": true, "title": "Fix the flaky test", "gen": 2}
```

(Every field shown at once; `pending`, `login`, `plan`, `usage` and
`activity` are present only while they apply, per the table.)

| Field | Meaning |
|---|---|
| `state` | `stopped` (no adapter now; the next prompt starts one — never started, idle-reclaimed, exited cleanly) · `starting` (spawn, initialize, session/new\|load) · `ready` (live, no turn) · `working` (a prompt in flight) · `login` (needs sign-in) · `lost` (cut off: xbind restart, sandbox stop, output gap; Retry/next prompt resumes) · `failed` (couldn't start: not installed, refused, crashed at init). From storage: `none`/`stopped` → `stopped`; `starting`; `live` → `ready`/`working` by `prompt_state`/turn; `login`; `lost`; `failed` |
| `error` | why, for `lost` and `failed` (words for people) |
| `mode` | the session's modes (else its config option of category `mode`, else the catalog's); `explicit`: owner-only — every mode §4.3.12's rule doesn't open (default-deny) |
| `options` | the adapter's config options (no category-`mode` option) |
| `commands` | slash commands (`available_commands_update`, normalized like xbind's) |
| `usage` | the last `usage_update`: context tokens `used`/`size`, cumulative `cost` when the adapter says |
| `plan` | the last ACP plan (absent: none this session) |
| `activity` | `kind`: `idle` · `thinking` (thought chunks) · `writing` (message chunks) · `tool` (a call in progress; `title` its summary) · `waiting` (a park); `at` when it began |
| `counts` | this conversation's harness calls, distinct files edited, lines added/deleted (across respawns) |
| `pending` | the park, compact: `kind` `approval` \| `question` \| `login`, `title` (the tool's title, the question's message, "Sign in to ‹name›"); the full card data is `pendingState.harness` |
| `login` | present while `state == "login"` (and during a sign-in): `command` for the login terminal (the adapter's terminal-auth argv shell-quoted when it offers one, else the catalog's `LoginCmd`); `methods` from the adapter's `authMethods`, `kind` `terminal` \| `api-key` \| `device-code` (others are not offered); `device` while a device-code sign-in waits: `{by}`, who started it — its `url` and `message` (the code) only in that person's own `GET /runs/{id}/harness` (routes-fix) |
| `sandbox` | the fixed sandbox and cwd; `shared`: others may use it (team visibility, members or shares) — the UI's privacy note |
| `steering` | the adapter steers mid-turn (§3.5) |
| `title` | the adapter's own session title |
| `gen` | the adapter generation (increments per spawn) |

#### 4.3.3 The `harness` stream event

`{"seq", "type": "harness", "run", "root", "ts", "data": <§4.3.2>}` — the
**whole** object, sent when anything in it changes that isn't already a
`run` event. It is coalesced per run like drafts (key `harness:<run>`): a
client that falls behind sees only the latest. Clients replace
`run.harness` with it. Run status changes stay `run` events (which also
carry `harness`).

#### 4.3.4 `pendingState` for a harness park

`runs.pending` (served as `runSummary.pendingState` and the view's
`run.pendingState`) — `kind` gains `question` and `login` for harness runs:

```json
{"kind": "approval", "park": "Xq3…",
 "toolCalls": [{"id": "h2:toolu_01", "type": "function", "function": {"name": "acp:execute", "arguments": "{…}"}}],
 "harness": {
   "callId": "h2:toolu_01",
   "options": [{"optionId": "allow", "name": "Allow", "kind": "allow_once"},
               {"optionId": "allow_always", "name": "Always allow", "kind": "allow_always"},
               {"optionId": "reject", "name": "Reject", "kind": "reject_once"}],
   "tool": {"title": "go test ./...", "kind": "execute", "name": "Bash", "label": "Run the tests",
            "command": "go test ./...", "rawInput": {…}, "content": […]},
   "rule": {"kind": "execute", "title": "go test ./..."},
   "defaultToNo": false, "description": "",
   "planApproval": false, "plan": "",
   "pid": "p3", "rpcId": "h12.2-17"}}
```

- **approval:** `options` are the harness's own (`explicit: true` on one
  that raises the session to an explicit mode — owner-only); `tool` is the
  call (`command` lifted from rawInput when it has one; `content` the ACP
  tool content, e.g. a diff, ≤ 64 KiB); `rule` is what `allow_always`
  would remember (absent for `switch_mode`, which never makes a rule);
  `defaultToNo`/`description` from the adapter's `_meta.permission`;
  `planApproval: true` with `plan` (markdown) for claude's ExitPlanMode
  (`switch_mode`) and codex's `plan_review`. `toolCalls` holds the call so
  existing readers (Needs, the child card) render something.
- **question:** `{"kind": "question", "park", "harness": {"eid", "callId?",
  "message", "schema"}}` — `schema` the form-mode JSON schema, including
  claude's "Other" (`_askUserQuestionCustomAnswer`) field.
- **login:** `{"kind": "login", "park", "harness": {"login": <§4.3.2
  login>}}`.
- `pid`, `rpcId` and `eid` are internal (clients ignore them).

#### 4.3.5 Tool rows and the `acp` lift

Each harness call is **one assistant row** plus **one tool row**:

- **The assistant row:** `content` the text flushed before the call,
  `Meta.reasoning` the thinking, `toolCalls: [{id: "h<gen>:<acp id>",
  type: "function", function: {name: "acp:<kind>", arguments}}]`.
  - `<kind>` is ACP's ToolKind: `read`, `edit`, `delete`, `move`, `search`,
    `execute`, `think`, `fetch`, `switch_mode` or `other` (absent or unknown
    → `other`).
  - `arguments` is (as every call's) a JSON string; it encodes an object:
    rawInput's top-level fields plus
    `summary` = the adapter's label, else its title, else the kind. A
    rawInput `summary` key is renamed `rawSummary`; a rawInput that isn't an
    object becomes `{summary, rawInput: <value>}`. `childDigest` and
    tool-heads read `summary`.
  - A text-only flush is an assistant row without `toolCalls`. Text and
    thinking of a harness-internal subagent carry `Meta.harness.parent`.
- **The tool row** (`role: "tool"`, `toolCallId` the call's id): `content`
  is `(running…)`, `(awaiting your approval)` while it is the park, then the
  result text — the text content; for an edit "edited ‹path› (+a −d)" per
  file; for execute the output's last 8 KiB and `\n[exit N]`; `error: …`
  when failed; `(cancelled)` when cancelled. Its `Meta.harness` holds the
  call.

`messageView` lifts `Meta.harness` to **`acp`**:

```json
{"kind": "execute", "title": "go test ./...", "label": "Run the tests", "tool": "Bash",
 "status": "completed", "parent": "h2:toolu_00", "subagent": false, "planReview": false,
 "locations": [{"path": "/work/api/x_test.go", "line": 12}], "files": ["/work/api/x_test.go"],
 "exitCode": 1, "output": "…", "outputTruncated": 0,
 "diffs": [{"path": "/work/api/x.go", "status": "modified", "add": 12, "del": 3,
            "patch": "--- a/work/api/x.go\n+++ b/work/api/x.go\n@@ …", "truncated": false}]}
```

| Field | Meaning |
|---|---|
| `kind`, `title` | ACP's |
| `label`, `tool` | the adapter's human description and programmatic name (D77's lifts) |
| `status` | `pending` \| `in_progress` \| `completed` \| `failed` \| `cancelled` |
| `parent` | the stored id of the harness-internal subagent call it runs under (claude's Task) |
| `subagent` | this call **is** such a subagent (Task/Agent) |
| `planReview` | codex's plan-review call |
| `locations` | ACP's `[{path, line?}]` |
| `files` | the distinct paths of `locations` and `diffs` |
| `exitCode`, `output` | a shell call's (D77 terminal output), ANSI kept, the last 64 KiB; `outputTruncated` the bytes dropped |
| `diffs` | per file of ACP `diff` content: `status` `added` \| `modified` \| `deleted`; exact `add`/`del`; `patch` a unified diff (3 lines of context) the **backend** computes, ≤ 64 KiB per file (`truncated: true` beyond, cut at a hunk boundary) |

On an assistant row, `acp` is `{parent}` when its text belongs to a
harness-internal subagent, else absent. The fold nests blocks whose
`acp.parent` is a call in the held pages; an orphan renders flat.

#### 4.3.6 `/tree` nodes

`GET /runs/{id}/tree` nodes gain `engine` and, for harness runs, `harness`
= §4.3.2 **without** `options`, `commands`, `mode.available` and
`login.methods` (the board's row: provider, name, state, error, mode.current,
usage, plan, activity, counts, pending, sandbox, title). The board re-reads
the tree on change like `treeDirty` (one request in flight).

#### 4.3.7 Links

A link's view (`links.go` `linkView`) already carries its `child` summary:
`child.engine` and `child.harness` identify a harness child. The spawn
call's placeholder, digest and delivery are the built-in ones (§4.4).

#### 4.3.8 Conversation rows

`GET /conversations` rows (and `/needs` items' `run`, search hits) gain,
besides `engine`/`harness`:

- `waiting: true` when the root or any run below it is `waiting_input`
  (the row glyph `?`);
- `kids: {harness, waiting}` — live harness descendants, and descendants in
  `waiting_input`; present only when either is non-zero.

Both are computed with one query per page, not per row.

#### 4.3.9 `/needs`

Reason `login` joins `question`, `approval` and `failed`: a run in the tree
parked on `pendingState.kind == "login"` ("needs you to sign in to ‹name›"),
for callers who may participate. The push (`needs_push.go`) says the same.

#### 4.3.10 The catalog (`GET /harnesses`)

```json
{"harnesses": [
  {"id": "claude", "name": "Claude Code",
   "available": false, "reason": "no-egress",
   "why": "needs internet access — Coding sandboxes offers none (bind its internet class)",
   "classes": ["coding"],
   "images": [{"provider": "apps/coding-sandbox", "manager": "Coding sandboxes", "image": "base", "advertised": true, "egress": ["internet"]}],
   "modes": [{"id": "default", "name": "Ask before acting"}, {"id": "bypassPermissions", "name": "Bypass permissions", "explicit": true}],
   "defaultMode": "default", "autoMode": "acceptEdits", "approveMode": "default", "planMode": "plan",
   "setting": "approve",
   "login": {"command": "CLAUDE_CODE_REMOTE=1 claude /login"},
   "options": [ConfigOption…],
   "sandboxes": {"apps/coding-sandbox|sb-7f3a": {"installed": true, "signedIn": false, "at": 1790000100000}}}]}
```

- One entry per catalog id any bound manager advertises (§5.1), plus the
  four of the sdk catalog even when none does (then unavailable).
- `available`/`reason`/`why` — evaluated in this order, the first that holds
  wins:
  1. `no-image` — `images` is empty ("no bound sandbox manager's image has
     it"); `manager-error` instead when a manager that didn't answer might
     (`why` = its hello error);
  2. `no-class` — no class the caller may use has toolset `harness` and
     allows it ("no class you may use allows coding agents");
  3. `no-egress` — no manager offering it offers an egress other than
     `none` that such a class allows ("needs internet access — ‹manager›
     offers none (bind its internet class)").
- `classes`: the classes the caller may use that allow it, the caller's
  default first.
- `images`: every (manager, image) that advertises it (`advertised:
  true`), plus every image of a manager whose hello predates
  `images[].harnesses` (no image carries the key — `advertised: false`: its
  sandboxes are built on the rootfs that pins the harnesses, and the probe
  decides), each with the manager's non-`none` egress values. The sandbox
  picker keeps sandboxes whose (provider, image) is listed and whose
  egress isn't `none`.
- `modes`, `defaultMode`, `autoMode`, `approveMode`, `planMode`: the sdk
  catalog's (§6); `autoMode` empty = no auto mode.
- `setting`: the caller's own Auto / Always approve (§4.3.12).
- `login.command`: the catalog's (or the hello entry's) login command.
- `options`: the config options the last session of it reported, any
  conversation (the home picker's model list); absent before any.
- `sandboxes`: what the agent last learned per sandbox, by probe or
  session; a sandbox absent = unknown.

#### 4.3.11 Classes

`agentClass` gains the toolset **`harness`** and the field **`harnesses`**
(a `classSet`: `"all"` or a list of ids; default `[]`). `normalize` (and
`PUT /classes`) refuse `harness` without `sandbox`, or with no non-`none`
value in `sandboxEgress`: `the harness toolset needs sandbox and an egress
other than none — a coding agent must reach its provider`. The built-in
`coding` class gains `harness` and `harnesses: "all"`. A harness
conversation or spawn requires both. `node()`'s same-class rule holds: a
harness child is its parent's class.

#### 4.3.12 Per-person preferences

| Key | Stored | Value | Read by |
|---|---|---|---|
| `prefs/agent` | xbind prefs (`/api/xbin/prefs/agent`), like `prefs/class` | `"agent"` (the built-in) or a harness id — the "Who answers" default | the UI |
| `prefs/harness-sandbox` | xbind prefs (`/api/xbin/prefs/harness-sandbox`) | `{"<provider>": "<ref>"}` — the sandbox last used per harness | the UI |
| `prefs/harness-mode/<provider>` | **AgTT's own store** (`harness_prefs`), `GET /prefs/harness-mode`, `PUT /prefs/harness-mode/{provider}` | `"auto"` \| `"approve"`; unset = `"approve"` | the engine and the UI |

`harness-mode` lives in AgTT because the engine needs it when the agent
spawns a child for someone, and xbind's prefs are per principal (a
backend reads only its own bucket). It is applied when a harness
conversation or child is **created** (into `Config.Harness.Mode`); changing
it later doesn't change existing conversations (their mode picker does).
Mapping to provider modes:

| Setting | claude | codex | gemini | opencode | fake |
|---|---|---|---|---|---|
| `approve` | `default` | `read-only` | `default` | (its own) | `ask` |
| `auto` | `acceptEdits` | `agent` | `autoEdit` | — (refused, §4.2.2) | `auto` |
| `plan` (spawn only) | `plan` | `read-only` | `plan` | (its own) | `ask` |

**Bypass modes are default-deny** (Afix). A mode is open to anyone who may
talk in the conversation only when the sdk catalog knows it never takes
the agent past its own asks — `acp.Provider.Safe`: a non-explicit entry of
`Modes`, or one of `SafeModes` (opencode's `build`, `plan`) — or it is the
mode the adapter opened its first session in by itself
(`harness_sessions.start_mode`: a harness the catalog lacks knows no
other). Every other mode — explicit ones, one a newer adapter reports that
the catalog doesn't list, every other mode of a harness the catalog lacks
— is **owner-only** (a person who owns the root conversation), everywhere a
mode is chosen: `PATCH /runs/{id}/harness {mode}` (also when it goes as the
config option of category `mode`), `POST /ask|/runs {harness.mode}` (a
person — the owner-to-be), A10's `harness_mode` (only ever the catalog's
settings), a stored option the live adapter reports as category `mode`
(skipped: `acp.Config.SkipModeOptions`, with a note), and a permission's
allow option that switches to such a mode — the mode it names (its id, or
the catalog's `acp.Provider.OptionModes`: claude-agent-acp's plan approval
says "Yes, and bypass permissions" as `exit-plan-bypass`), or, on a mode
switch (`switch_mode`: a plan approval), any `allow_always` whose mode the
catalog can't place. The summary's
`mode.available[].explicit` and a park's `options[].explicit` say it; the
UI's ⚠ follows them.

#### 4.3.13 The direct-steering notice

When a person messages a harness child (from its card, the board or its own
chat), its parent gets an `hnote` whose text is:

```
[direct message to #<child id> (<harness name>) from <who>]
<the message>
```

`<who>` is the sender's user id (else the caller tag). fold.js's `NOTICE`
regex gains `direct message to #`, so it renders as a notice. The parent's
model reads it at its next step boundary or next turn; it never starts a
turn.

### 4.4 The agent's tools (WP-A10)

`subagent_spawn` gains, **only** when the class holds `harness` and the
spawn sandbox (the active one, or `sandbox`) offers at least one harness
that the class allows and whose egress isn't `none`:

- `harness`: an enum of those ids. The description states the limit first:
  "it sees only the sandbox and the task, not this conversation"; that each
  harness is a full CLI costing hundreds of MB; to give parallel children a
  distinct `cwd` or a git worktree each.
- `harness_mode`: `"approve"` \| `"plan"` — **narrows** only. Absent: the
  conversation owner's setting (§4.3.12); the model can never widen it and
  never picks an explicit mode.
- `system` with `harness` is refused ("a coding agent keeps its own
  instructions").

The child: `engine = "harness"`, the task is its first `hprompt` (and the
ask ledger), mode as above; its turn end settles the link (answered,
incomplete, error with the error text, canceled). Budgets: `maxSpawn` and
`maxSpawnPerTurn` as today; `maxHarness` (3) running harness children per
tree ("3 coding agents already run in this conversation (the limit) — wait
for one or cancel one"); refused when the sandbox's running execs would
exceed `limits.execsRunning − 4` ("‹sandbox› runs N commands (its limit is
M) — a coding agent needs room").

`subagent_message` to a harness child queues an `hprompt` (sent as is — no
"[message from your parent]" wrapper); its reply says "steered into #N's
running turn", "queued until #N's current turn ends" or "sent as #N's next
prompt". `subagent_cancel`, `_wait`, `_status`, `_result` are unchanged
(status reads `harness.state`/`activity`). The digest line of a harness
child: `harness ‹provider› · 12 tool calls · $0.40`, plus "waiting for a
person to approve: ‹title›" / "waiting for a person to sign in". The parent
model is never offered a child's permission.

## 5. Sandbox-manager contract additions (WP-A4)

All optional or additive; protocol stays 1. Docs: `docs/sandbox-manager.md`
(the contract), `docs/protocol.md` + `internal/server/openapi.go` (the
runtime routes), `docs/sdk.md`, a changelog entry.

### 5.1 `hello.images[].harnesses` (A4a)

```json
"images": [{"id": "base", "title": "…", "default": true, "tools": ["git", "go"],
            "harnesses": [{"id": "claude"}, {"id": "codex"}, {"id": "gemini"}, {"id": "opencode"},
                          {"id": "mine", "title": "My agent", "argv": ["my-acp"], "login": "my-agent login"}]}]
```

- `id` `[a-z][a-z0-9-]{0,31}`. A catalog id (§6) needs nothing else; the
  consumer defaults `title`, `argv` and `login` from the sdk catalog. An
  unknown id needs `argv` (else it is ignored) and should have `title`.
- `argv`: the ACP adapter command inside the sandbox; `login`: the shell
  command a person runs in a terminal to sign it in.
- A manager whose hello has no `harnesses` on any image predates the field:
  a consumer treats its images as *unknown* (not as "none") and relies on
  the probe — existing coding-sandbox instances keep offering harnesses
  before they update.
- An advertisement, not a promise: installs are best-effort, so a consumer
  **probes** before use — `run` of `sh -c 'for b in …; do command -v "$b"
  >/dev/null 2>&1 && echo "$b"; done'` over the harnesses' `Bins` (the
  catalog's, else `argv[0]`), ≤ 10 s, in a running sandbox.
- coding-sandbox: `Image.Harnesses` (config); every image advertises the
  four by default (they are all built on the rootfs) and an image's
  `harnesses` field overrides it (`[]` = none). Saved configs keep working
  unchanged. AgTT stores a bound sandbox's harnesses in
  `SandboxBinding.Harnesses` (`[]string`, nil in a binding stored before).
- **Environment** (A4a): tilesbx tty execs default to
  `TERM=xterm-256color`, `COLORTERM=truecolor`, `LANG=C.UTF-8` (the exec's
  env wins); coding-sandbox's `defaultsOf` gains `IS_SANDBOX=1`.
- fakesandbox (and its byte-identical mirrors) advertise a harness `fake`
  when configured (§7.3).

### 5.2 Terminals for consumer backends (A4b)

- The `tty` routes (`GET …/tty`, `GET …/execs/{eid}/tty`) serve a consumer's
  **backend** as well as its pages: a backend dials with its own credential
  and `Sbx-User: <person>` (asserted). The person rules are **unchanged**
  (§Who is asking, §Partitions): on a backend call the manager records the
  asserted person (as for `execs`: the runtime's `forUser`) and does not
  police it — the consumer checks its own rules before it dials (AgTT:
  `sandboxAccess(caller).Use`, fresh from the manager, §4.2.8); a verified
  `X-XBin-User` wins over the header. The backend relays the socket to its
  page or app byte for byte. Every manager implements it (a cloud manager:
  `ssh -t`, for a page or a backend alike).
- A manager passes the person as `forUser` to the runtime, which refuses a
  person with `noTerminal` (D88) at every attach, verified or asserted.
- The SDK consumer helper (`sdk/sandbox_consumer_tty.go`):

  ```go
  // ManagerTTY is a terminal a consumer backend relays from a sandbox
  // manager: a new tty exec (Cwd, Cmd — the login shell when empty — Rows,
  // Cols) or, with Exec, an existing one; always for User (Sbx-User).
  type ManagerTTY struct {
  	User       string       // the person, asserted; required
  	Exec       string       // attach to this tty exec ("" = start one)
  	Cwd, Cmd   string
  	Rows, Cols int
  	Client     *http.Client // nil = xbin.Client() (the gateway)
  }
  // RelayManagerTTY relays the caller's terminal WebSocket (r) to the
  // manager at endpoint (its bound interface URL; the contract's routes are
  // under endpoint + "/sbx/"): GET /sbx/sandboxes/{sandboxID}/tty or
  // /execs/{Exec}/tty, byte for byte, the /ws/term wire end to end. A
  // refusal before the upgrade is written as the manager's JSON; r not an
  // upgrade is 400 invalid. It strips Cookie, Authorization, X-XBin-* and a
  // caller's Sbx-User before setting its own.
  func RelayManagerTTY(w http.ResponseWriter, r *http.Request, endpoint, sandboxID string, o ManagerTTY)
  ```

- Conformance (`sdk/sandboxcontract`): a section `consumer-tty` — a
  terminal started and attached again by a backend as an asserted person;
  the session frame and the wire as for a page; the asserted person
  recorded (not policed); a verified header wins over `Sbx-User`; a
  refusal (a missing sandbox or exec, not a WebSocket upgrade) comes before
  the upgrade, as JSON. Run against fakesandbox, coding-sandbox's fake
  backend and live tilesbx (which also refuses a `noTerminal` person).
- `docs/sandbox-manager.md` §Terminals loses "The xbin app's `terminal`
  primitive … can't reach a manager's": consumers relay; the cloud table's
  `tty` row reads "`ssh -t` (for a page or a consumer's backend),
  window-change on resize".

### 5.3 `stdio` — an optional capability (A4c)

Advertised as `"stdio"` in `hello.caps` (and `sandbox.caps`). Without it a
consumer uses the baseline (§3.6); a manager without it answers the route
`unsupported` (or `not-found`, older) and ignores `split`.

- `POST …/execs {…, split: true}` keeps stdout and stderr in separate rings
  (both from the exec's output budget); the exec gains `errTotal`.
- `GET …/execs/{eid}/output?stream=stdout|stderr` — `stderr` on a split
  exec reads stderr; on a non-split exec it is `invalid`. The default
  (`stdout`) is the combined stream of a non-split exec, as today.
- `GET …/execs/{eid}/stdio?since=<n>&errSince=<n>` — a WebSocket, for a
  non-`tty` exec (a tty exec is `invalid`: use `tty`):
  - server → client: first `{"op":"hello","id":"<eid>","total":N,"errTotal":M,"state":"running","stdin":true,"split":true}`;
    **binary frames** = stdout from `since` (preceded by
    `{"op":"gap","stream":"stdout","from":since,"to":<ring start>}` when
    `since` fell out of the ring); `{"op":"stderr","off":<offset>,"data":"<base64>"}`
    (split execs; its own `gap` with `"stream":"stderr"`);
    `{"op":"exit","code":0,"signal":"","total":N,"errTotal":M}` once the
    exec ended and all output is out, then a normal close (1000);
    `{"op":"pong","t":…}`; `{"op":"error","refusal":"invalid","error":"…"}`.
  - client → server: **binary frames** = stdin (each ≤ `limits.stdinMax`;
    only with `stdin: true`, else answered with `error` and dropped);
    `{"op":"eof"}` closes stdin; `{"op":"ping","t":…}`. Unknown ops are
    ignored both ways.
  - The server reads a stdin frame only when the command can take it (the
    socket's flow control replaces the 30 s / 503 rule).
  - **The newest attacher wins:** a second attach closes the first with
    code 4001 (`replaced`). Offsets are `…/output`'s; a reader may switch
    between the two.
  - Refusals come before the upgrade, as JSON (`not-found`, `invalid`,
    `unsupported`, `lost`).
- The runtime (xbind): `GET /sandboxes/<name>/execs/<id>/stdio?since=&errSince=`
  (manager; WebSocket), `split` on `POST …/execs`, `stream=` on output —
  `docs/protocol.md` + `openapi.go`.
- SDK: `ExecRequest.Split`, `OutputQuery.Stream`, `ExecInfo.ErrTotal`, the
  route builder `ExecStdio(id) SandboxRoute` (for `Forward`), and
  `(*Sandbox).DialStdio(ctx, execID string, since, errSince int64)
  (*ws.Conn, error)`.
- Built in tilesbx, the sdk, coding-sandbox (backends and fakes),
  fakesandbox and its mirrors; conformance section `stdio` (replay from an
  offset, gap, stderr split, exit, eof, newest wins).

## 6. `sdk/acp` (WP-A1, A2)

One ACP client, in `github.com/xbin-dev/xbin/sdk/acp`: standard library
only, Go ≤ 1.24, every non-test file ≤ 800 lines, **no `_xbin/*` names**.
xbind keeps compiling unchanged through aliases.

### 6.1 The move (A1)

| File | Holds |
|---|---|
| `rpc.go` | `Message`, `Error`, codes, `Encode`, `Decoder` (`NewDecoder`, `Next`), `Conn` (`NewConn`, `Call`, `CallCtx`, `Notify`, `Reply`, `Send`, `Serve`) |
| `types.go` | the v1 subset (as `internal/agent/acp/types.go`, minus the `_xbin/*` names) |
| `event.go` | `Event{Seq, TS, Type, Data, Wire *Wire \`json:"-"\`}`, `Ev*`, `Status*`, `New` — `Wire` is `json:"-"`, so xbind's logs and D75 history files don't change |
| `permissions.go` | `Permissions`, `PermissionOption`, `ToolCallRef`, `Pending`, `Resolution`, kind constants |
| `providers.go` | `Provider` (fields as today + `LoginCmd`, `Bins`, `AutoMode`, `ApproveMode`, `PlanMode`, all `json:"-"`, so xbind's providers JSON stays byte-identical), `Catalog()` (the four; no `os.Getenv`), `Lookup` (the four), `ResolveMode`, `Fake(argv)` (the test provider: modes `ask`, `auto`, `yolo` explicit; AgTT uses it for a hello entry `fake`) |
| `attachments.go` | moved as is |
| `config.go` | `Config` (today's fields + the seams below), `Process`, `Spawner`, `Elicitation`, the `Err*` values (all from `internal/agent/driver.go`, whose `Driver` interface stays in xbind) |
| `client.go`, `handshake.go`, `updates.go`, `status.go` | `client.go` split in four |
| `prompt.go`, `elicit.go`, `toolmeta.go`, `commands.go`, `abort.go` | moved as they are |

The catalog's additions: `LoginCmd` — `CLAUDE_CODE_REMOTE=1 claude /login`,
`codex login --device-auth`, `NO_BROWSER=true gemini`, `opencode auth
login`; `Bins` — `claude-agent-acp`, `codex-acp`, `gemini`, `opencode`;
`AutoMode`/`ApproveMode`/`PlanMode` per the table in §4.3.12.

Seams in `Config` (nil/zero = today's xbind behaviour):

```go
Caps         *ClientCaps  // nil = {fs rw, terminal, _meta{terminal_output, terminal_output_delta, subagent-transcript}, elicitation.form}
Drop         func(ctx context.Context, c *Conn, a Attachment) (string, error) // nil = attachments refused
AuthHint     func(msg string) string      // nil = "sign in: <LoginCmd>"
OnExt        func(m *Message) bool        // extension notifications (xbind: _xbin/log)
IDPrefix     string                       // "" = numeric request ids
InlineBudget int                          // 0 = MaxInlineImagesBytes
Attach       *SessionState                // non-nil: adopt a running session instead of the handshake
```

`ClientCaps{FS, Terminal bool; ElicitForm, ElicitURL, TerminalAuth bool;
Meta map[string]any}` (`TerminalAuth` sets `_meta["terminal-auth"]`, so an
adapter offers its terminal sign-in with the exact argv). AgTT passes `{FS: false, Terminal: false,
ElicitForm: true, ElicitURL: true, TerminalAuth: true, Meta:
{terminal_output, terminal_output_delta, subagent-transcript}}`.

xbind shims: `internal/agent` holds type/const aliases (`type Event =
acp.Event`, …) and keeps `Log`, `page.go`, the `Driver` interface,
`Providers()` and `Lookup` (catalog + the `XBIN_AGENT_FAKE` fake, which
keeps today's `ask`/`yolo` modes — it is not `acp.Fake`, so the providers
JSON with the fake set is unchanged too). `Provider.LoginHint`
becomes `agent.LoginHint(p, tile)` with the same words.
`internal/agent/acp` holds `type Client = sdkacp.Client` and `New()` =
`sdkacp.NewWith(xbindDefaults)` (`Drop` via `_xbin/attach`, `OnExt` for
`_xbin/log`, `AuthHint`), and keeps the `MXbin*` names, `SpawnParams`,
`HelloParams`, `LogParams`, `AttachParams` — `internal/term` needs no
edits. `internal/agent/host` and `cmd/bx/agenthost.go` change imports only.
xbind's `Log.Append` nils `Wire`.

**Proof A1 changes nothing:** xbind's existing tests pass unmodified
(`internal/agent/...`, `internal/term`, `internal/server` agentapi*,
`internal/push`); a wire-golden test asserts `initialize` and `session/new`
JSON with `Caps == nil` equal master's; the native capture fixtures
(`XBIN_AGENT_CAPTURE`) regenerated before and after diff empty; the
providers endpoint JSON is unchanged.

### 6.2 Extensions (A2)

| API | Contract |
|---|---|
| `NewConnWith(r, w, ConnOptions{IDPrefix})` | string request ids `"<prefix>-N"` (AgTT: `h<run>.<gen>`) |
| `(*Decoder).Offset() int64` | bytes consumed through the last returned frame's newline (not bufio's read-ahead); a skipped bad line counts |
| `(*Conn).Expect(id json.RawMessage) <-chan *Message` | adopt a call a predecessor process sent: its response is delivered here |
| `Event.Wire` | `{Off int64, RPCID json.RawMessage, Replay bool}` — the decoder offset after the frame, the request id it answers or asks, whether it is a `session/load` replay |
| `(*Client).State() SessionState` | JSON-serializable: sessionId, loadable, steering, promptCaps, modes, options, commands, agentInfo, authMethods, per-call tool status, turn, the in-flight prompt's rpc id |
| `Config.Attach` | `Start` rebuilds from a `SessionState` without a handshake (reattach mid-turn) |
| `(*Client).Steer(ctx, Prompt) (outcome string, err error)` | `_session/steering {sessionId, prompt, _meta:{steering:{idleBehavior:"promptRequired"}}}` → `injected` \| `promptRequired` \| `startedNewTurn`; `ErrSteeringUnsupported` when not advertised |
| `(*Client).AuthMethods() []AuthMethod` | from `initialize` (`AuthMethod` gains `Meta`) |
| `(*Client).Authenticate(ctx, methodID string, meta map[string]any) error` | `authenticate {methodId, _meta}`; clears the signed-out state on success |
| URL elicitation | with `Caps.ElicitURL`: `elicitation/create {mode:"url", url, message, elicitationId}` surfaces as an `elicitation.request` event with `mode:"url"`; `RespondElicitation` answers it; the agent's `elicitation/complete {elicitationId}` notification becomes `elicitation.resolved` |
| `(*Permissions).Restore(p Pending, rpcID json.RawMessage)`, `Rules() []Rule`, `SetRules([]Rule)` | persistence; `Request` is idempotent by rpc id (a replay after a handoff files nothing twice) |

Tests: reattach mid-turn; a restored permission answered; no id collisions
across generations; `Offset` with gaps and bad lines; steering outcomes;
authenticate; URL elicitation; the replay flag.

## 7. The fakes

### 7.1 `sdk/acp/acptest` (WP-A3)

hack/fakeacp's script engine as a library: `Serve(r io.Reader, w io.Writer,
o Options) error` and `Main()` (flags from `os.Args`, stdio);
`hack/fakeacp/main.go` becomes `func main() { acptest.Main() }`. A test
binary serves as the fake adapter by calling `acptest.Main()` from its
`TestMain` when `os.Args[1] == "acptest"` (fakesandbox runs argv on the
host). Every existing script stays **byte-identical**; new behaviour that
would change an existing output is behind a flag.

Flags (`Options` fields; unknown flags ignored):

| Flag | Behaviour |
|---|---|
| `--steer` | advertises `_meta.steering.supported`; serves `_session/steering`: during a turn → `injected` (the running turn's next chunk is "steered: ‹text›"); idle with `promptRequired` → `promptRequired`; idle otherwise → `startedNewTurn` (a turn answering "echo: ‹text›") |
| `--auto-mode` | `availableModes` gains `auto` (between `ask` and `yolo`): `auto` skips an **edit**'s permission request, still asks for others |
| `--require-login` | signed in only while `$HOME/.fakeacp/credentials` exists (read at start and after `authenticate`); signed out, a prompt pushes `_auth/status_update{kind:none}` and fails -32000. `authMethods`: `fake-login` (terminal auth, `args: ["login"]`), `fake-api-key` (`_meta["api-key"]`), `fake-device` (device code) |
| `--persist` | appends every session's updates to `$HOME/.fakeacp/sessions/<id>.jsonl`; `session/load` replays exactly those (as the real adapters do) instead of the canned turn |
| `--device-ms=N` | the device-code sign-in completes N ms after the client accepts the URL (default 1000) |

`authenticate`: `fake-api-key` with an empty key → -32602 `no key`, `bad` →
error `invalid API key`, else writes the credentials file (never the key)
and answers `{}`; `fake-device` without the client's `elicitation.url` →
error `device code needs URL elicitation`, else `elicitation/create
{mode:"url", url:"https://example.invalid/device", message:"Enter code
FAKE-1234 at https://example.invalid/device"}`, then after `--device-ms`
writes the file, sends `elicitation/complete` and answers `{}`.

The `login` subcommand (`fakeacp login`, the terminal sign-in): prints
`Open https://example.invalid/login and paste the code:`, reads a line;
`fake-code` → writes `$HOME/.fakeacp/credentials`, prints `Signed in.`,
exits 0; else prints `Wrong code.`, exits 1.

New prompt scripts:

| Script | Plays |
|---|---|
| `todo` | three ACP `plan` updates 200 ms apart (3 entries: pending → one in progress → all completed), then "todo done" |
| `stall` | a chunk "stalling", then nothing until `session/cancel` (handoff and reattach tests) |
| `cards` | one completed call of each kind — read, edit (a `diff` for `hello.txt`), delete, move, search, execute (terminal output + exit 0), fetch, think, other — then "cards done" |
| `perm-edit` | an edit tool_call + `request_permission` (skipped in `auto` and `yolo`); matched before `perm`, which the dispatch finds by substring |
| `steer…` | (a prefix) a `slow` turn that reports each steer it received |

### 7.2 `hack/fakeopenai` keywords

Checked **before** every other keyword (they contain `steer`, `fan out`,
`delegate`):

| Keyword | Plays |
|---|---|
| `harness spawn` | `subagent_spawn {task: "perm", label: "fake coder", harness: "fake", summary: "Ask the coding agent"}` (foreground) → "The coding agent said: ‹its first line›" |
| `harness fan out` | three background spawns `{harness: "fake", wait: false}` with tasks `perm`, `slow`, `todo` → "Started three coding agents." |
| `harness steer` | `subagent_message {id: <the newest harness child>, text: "steer: use tabs"}` → "Steered it." |

### 7.3 fakesandbox's harness `fake`

`fsbManager.Harnesses` (nil = none) is added to every image in hello;
`hack/fakesandbox` fills it from `FSB_HARNESS_FAKE="<fakeacp path>
[flags…]"` (space-separated argv, like `XBIN_AGENT_FAKE`) as `[{"id":
"fake", "title": "Fake agent (tests)", "argv": [...], "login": "<fakeacp
path> login"}]`. The UI harness pass sets `--steer --auto-mode
--require-login --persist` and signs in the sandboxes it doesn't use for
the sign-in shots by writing `~/.fakeacp/credentials`. fsb.go stays
byte-identical with its mirrors (`builtin-templates/agent/_backend/fsb_fake_test.go`,
`builtin-tiles/sandbox-terminal/backend/fsb_fake_test.go`).

## 8. Work packages

Branches: the integration branch `agtt-harness` (`/home/magik6k/buxon-agtt`);
`fix/agent-regressions` (`/home/magik6k/buxon-agentfix`); one worktree and
`wp/*` branch per WP. Harness ports: 9131 and 9136 for WP-R, 9141–9160 for
everything else. Each chain ends in a skeptical verifier; the integrator
merges between waves.

| WP | Wave | Delivers | Depends on |
|---|---|---|---|
| **R** regressions fix | 0 | the owner's 8 items (render_html, pinned task, finish card, preview_port, port-debug UI, stale sandbox, copied-from note, sandbox_copy blame); dated amendments under D133/D135/D136 | — |
| **S** this spec | 0 | `plans/agtt-harness.md` | — |
| **A1** sdk/acp move | 0 | §6.1 | — |
| **A4** contract, harnesses, terminals | 0 | §5.1 (a), §5.2 (b), §5.3 (c), in that order | — |
| **A12** rootfs bump (optional) | 0 | newer adapters (the memory recipe) | — |
| **A2** sdk/acp extensions | 1 | §6.2 | A1 |
| **A3** acptest | 1 | §7.1 | A1 |
| **U0** make room | 1 | `workflow.js` out of agent.js (≈ −130 lines), budget lowered | R |
| **U1** foundation | 1 | seams (`ui.ext` block/end/top/paint/newChat; native `ctx.ext` block/end/toolbar/menu/composer/screen), `model/harness.js`, `harness-store.js`, `harness-heads.js`, fold nesting + `NOTICE`, the STUB routes and fixtures of §4, native stub seeds; no visible UI | S (U0 for agent.js) |
| **A6** storage and gates | 2 | §3.1 storage + migration, §4.3.11 classes, `SandboxBinding.Harnesses`, `harness_catalog.go` (advertisement + probe), `GET /harnesses`, `/prefs/harness-mode` | R, A1, A4a |
| **A7** the pipe | 2 | §3.6 against fsb: reattach at an offset, gaps, lost, 503 retry, chunked frames; stdio when offered | A2 (A4c if present) |
| **A8** engine core | 3 | §3.2–§3.5, `harness_{engine,pass,map}.go`, the edits to actor/transcript/engine/owner/draft/sandbox_bind; events §4.3.1–§4.3.5 | A6, A7 |
| **A9** routes and login | 4 | §4.2 (but A6's), §4.3.6–§4.3.9, the relays via A4b, schedules refuse; API.md, changelog | A8 |
| **A10** subagent integration | 4 | §4.4, §4.3.13 | A8 |
| **U2** start a conversation | 4 | `#apick` (CC/CX/GM/OC), `#n-agent`, class auto-resolved, filtered sandbox picker, setup card, sidebar kind chips; native picker and sheet field | U1; STUB, then A9 |
| **U3** transcript | 4 | `harness-cards.js` (execute, edit diffs, read, search, fetch, nested Task), 📋 plan pin, usage badge; native cards | U1; STUB, then A8/A9 |
| **U4** asking and controls | 4 | permission cards (reject first when `defaultToNo`), plan approval + feedback, question forms, `#hctl`, slash menu, Auto / Always approve, Enter queues / ⌘⏎ interrupts; native | U1; STUB, then A9 |
| **U5** terminals and sign-in | 4 | `terminals.js` dock with tabs (web dials the manager; native the relay; the D96 native difference removed), login tab → "Signed in? Retry", sign-in card (shared-HOME warning + confirm), authenticate flows | U1; STUB, then A9 |
| **U8** managers | 4 | class-form checklist for coding agents; a Coding agents catalog tab | U1; STUB, then A6 |
| **U6** child cards | 5 | `harness-child.js`: identity, task, status line, counters, inline approval/question/sign-in, last 3 blocks, Open/Stop/Cancel/Message; direct steering + notice | U3, U4, A10 |
| **U7** Coding agents board | 5 | web right dock (stable order, "needs you" filter), native screen, top chip, Delegated in the pinned task, Needs reason `login` | U6 |
| **A11** smoke and docs | 5 | `hack/harness-smoke.sh` (fsb + acptest always; real adapters through coding-sandbox optionally — `initialize` + `session/new` or login = pass); the D-harness entry; docs (sandbox-manager, sdk, protocol, changelog, API.md) | A1–A10 |
| **U9** harness pass and shots | 5 | `passes/agentharness.js` (§7.3 + §7.2); shots: picker, conversation with cards, permission, plan approval, question, parent with 3 children + board, login terminal then Retry, native | A11, U2–U7 |
| review + gate | 6 | adversarial review (security/ACL, restart/handoff, compat/contract, UI/parity) + fixes; `make check` on Go 1.26, tile-check with `-race` for every tile, `make integration`; live coding-sandbox with the real adapters | all |

Rules for every WP: stay in your worktree; no push, tag or release; kill
only PIDs you started; targeted checks while iterating, `make check` once at
the end on Go 1.26; builder-visible changes get docs and a changelog entry
in the unreleased section; never break users (additive APIs, stored data
keeps working); a WP that adds a UI feature key implements it in both views
or lists it in `DIFFERENCES.<view>` with the reason; record deviations in
§10.

## 9. Resolved

Where the two designs (backend and UX) disagreed or left a name open, and
why this spec picked what it did:

1. **Answering a question:** `POST /runs/{id}/harness/answer {park, action,
   content?}`, not the UX's `POST /runs/{id}/answer` (it exists: a text
   answer to `ask_user`) nor the backend's `{eid, …}` — a park gives the
   same 409 "no longer pending" semantics as `approve`.
2. **Mode and options:** `PATCH /runs/{id}/harness {mode?, option?: {id,
   value}}` (the backend's route, the UX's body), not `PATCH /runs/{id}
   {harness: …}` — the change is an RPC to a live adapter that can fail
   (502) or need the owning process (503), and `handlePatchRun` stays as
   it is.
3. **Harness states:** the API has `stopped | starting | ready | working |
   login | lost | failed`; the backend's storage states map onto it
   (§4.3.2); the UX's `ended` is `stopped`, its `error` is `failed` (not to
   be confused with the run's `error` status).
4. **Sign-in state:** status `waiting_input` with `pendingState.kind
   "login"`, not the backend's status `error` — a sign-in is not a failure:
   a child's link stays open, and Needs, push, the child card and the board
   treat it like any other wait; Retry is `/resume`.
5. **Pending data:** the full card data only in `pendingState.harness`
   (runSummary and the view already carry `pendingState`); the UX's
   `harness.pending` is kept as the compact `{park, kind, title}` for tree
   nodes and rows, which carry no `pendingState`.
6. **Where `acp` lives:** everything on the tool row's `Meta.harness`
   (lifted to `acp`), not split between the assistant row (static facts)
   and the tool row (updates) — one object in one place, and the
   placeholder row exists from the call's start. Assistant rows carry only
   `acp.parent`.
7. **Tool arguments:** rawInput's own fields plus `summary` (the plan's
   shape); the UX's `tool` and `locations` live in `acp`, so they can't
   collide with rawInput keys.
8. **One catalog route:** `GET /harnesses[?probe=<ref>]`; the backend's
   `GET /sandboxes/{ref}/harnesses` is folded in (`sandboxes[ref]`).
9. **Class switch for bypass:** the UX's `harnessModes: "safe"` is not in
   v1 — bypass is owner-only already (§2.1).
10. **Native terminals:** the owner chose the relay (the UX recommended an
    app change). Two relays: the run's (login or a shell at the harness
    cwd) and the sandbox's (any `cmd`, like the contract's `tty`); the
    backend's "login command only" is lifted, since terminals are now part
    of the sandbox interface.
11. **Children's mode:** the owner's per-person setting replaces the
    backend's "always `DefaultMode`"; `harness_mode` only narrows
    (`approve` | `plan`).
12. **Where the setting is stored:** AgTT's own store behind
    `/prefs/harness-mode/<provider>` — xbind prefs are per principal and
    the engine must read the owner's setting at spawn; `prefs/agent` and
    `prefs/harness-sandbox` stay xbind prefs (UI only).
13. **The notice text:** `[direct message to #N (name) from who]` instead
    of `[who messaged #N (name) directly: …]` — fold's `NOTICE` keys on a
    fixed leading phrase.
14. **Parent notice delivery:** a new inbox kind `hnote` that never wakes
    the parent (the designs named the notice, not its carrier); older
    binaries ignore it.
15. **`stdio`:** built now as an optional capability (the backend proposed
    deferring it); the baseline pipe stays mandatory.
16. **`interrupt` on a message:** from the UX; ignored on a built-in run,
    so one client sends it everywhere.
17. **Approve body:** `{approve?, park?, option?, feedback?}` — the
    backend's `option` plus the UX's `feedback`, the latter only with a
    rejection.
18. **Model list before a session:** the catalog carries the last-seen
    `options` per provider (the UX's need; the backend had no source).
19. **Sign-in confirm on shared sandboxes** is enforced by `authenticate`
    (`confirm: true`), not only by the UI; the terminal sign-in can't be
    enforced and relies on the card.
20. **The terminal route under `/sandboxes/{ref...}`:** registered inside
    the existing wildcard handler (refs contain `/`), like the lifecycle
    actions.
21. **Who polices a relayed person** (verifier, 2026-09-29): AgTT, not the
    manager. The contract's person rules stay as they are — a manager
    records an asserted `Sbx-User` and leaves its rules to the consumer —
    so the terminal relays and the log route check the caller's
    `sandboxAccess(caller).Use` themselves; only the runtime's
    `noTerminal` (D88) applies to an asserted person as well. Policing
    asserted persons in the manager would change the contract every
    manager in the wild implements (fakesandbox and coding-sandbox's
    `personOK` pass a backend call through).

## 10. Deviations

WPs record here, one dated line each, anything they built differently from
this spec and why.

- 2026-09-30 (A2) `IDPrefix` and `Attach` stay in `ClientOptions` (A1's
  placement, set with `NewWith`), not `Config`; "`Caps.ElicitURL`" is
  `Caps.Elicitation.URL` (`&struct{}{}`) and `TerminalAuth` is
  `Caps.Meta["terminal-auth"]` — A1 made `Caps` the wire
  `ClientCapabilities`, so there is no separate `ClientCaps`.
- 2026-09-30 (A2) Added beyond §6.2: `ClientOptions.AwaitLogin` (codex-acp
  1.13.1 refuses `session/new` signed out, so `authenticate` must be
  possible before a session exists: `Start` returns the -32000 but leaves
  the agent up, `Authenticate` then opens the session); `Process.Off`,
  `ConnOptions.Offset` and `NewDecoderAt` (offsets stay absolute after a
  reattach); `acp.Gap{Lost}` / `ErrGap` (a pipe's reader returns `*Gap`
  from `Read` where the ring lost bytes; the decoder counts them, drops the
  line broken on each side, `Serve` reports it to `OnBad`); `AuthMethod.Args`.
- 2026-09-30 (A2) `Steer` answers `promptRequired` without asking the agent
  when the client has no turn running. codex-acp 1.13.1 ignores
  `idleBehavior` (a steer that lands just after the turn ended starts a
  detached turn: `startedNewTurn`, and no `turn.end` reports it) and may
  answer `outcome: "failed"` (returned with an error). `injected` and
  `startedNewTurn` emit a user `message.delta` with `steered: true`.
- 2026-09-30 (A2) URL elicitation: an accept emits `elicitation.resolved`
  (`accept`), the agent's `elicitation/complete` a second one (`action:
  "complete"`, `by: "agent"`); a `complete` before any answer resolves it
  and answers the request `cancel`. A url request outside a turn does not
  set status `waiting_permission`.
- 2026-09-30 (A2) `Request` (and question filing) is idempotent against
  requests still **pending** under the same non-empty rpc id, not against
  answered ones (one `Permissions` per agent process). `State()` is the
  client's state now and may be newer than the offset an embedder commits:
  A8 sets `SessionState.PromptRPC`/`Turn` from `harness_sessions.prompt_rpc`
  / `turn` before attaching when its own record says the prompt is still in
  flight.
- 2026-09-30 (A3) §7.1: the new scripts (`perm-edit`, `todo`, `stall`, `cards`, `steer…`) all match as prefixes and are checked before every older word, so a prompt that merely contains one (an "install" with `stall` in it) still plays what it did.
- 2026-09-30 (A3) §7.1 `--require-login`: a prompt while signed out also re-reads the credentials file (besides start and `authenticate`), so a terminal sign-in (`fakeacp login`) is picked up by the running agent — "Signed in? Retry" works without a restart.
- 2026-09-30 (A3) §7.1 `--persist`: sessions get their own ids (`fake-<hex>`, not `fake-1`) and the history also holds each prompt as a `user_message_chunk` (as the real adapters replay it); an unknown id answers -32002. `startedNewTurn`'s echo turn follows the steering answer and has no prompt to end.
- 2026-09-30 (A3) §7.1 error codes the spec left open: `fake-api-key` `bad` → -32603 `invalid API key`; `fake-device` without URL elicitation → -32600; a declined device URL → -32603; `authenticate fake-login` → -32602 (it is a terminal method). The device code's `elicitation/create` also carries `requestId` (the `authenticate` id) and `elicitationId: "fake-device-1"`, as codex-acp's does. `--device-ms=0` means at once.
- 2026-09-30 (A3) §7.1 `Options` gains `Getenv` (the scripts' HOME and FAKE_API_KEY; tests run many agents with their own HOME) and `Self` (the agent's own argv, for `fake-login`'s `_meta["terminal-auth"]`, sent when the client advertises `terminal-auth`); `Command(flags…)` builds a test binary's argv, `Login` is the subcommand's function, `*ExitError` is `crash`.
- 2026-09-30 (A6): `sdk/acp` gains `ApproveMode`, `PlanMode` (json `-`) and `Fake(argv)` here — §6.1 listed them, A1 shipped `AutoMode` only; values per §4.3.12 (opencode: none of the three).
- 2026-09-30 (A6): a harness id is the contract's grammar `[A-Za-z0-9][A-Za-z0-9._-]{0,31}` (what A4's `sandboxcontract` validates), not `[a-z][a-z0-9-]{0,31}` — an advertised id the contract admits isn't dropped by AgTT.
- 2026-09-30 (A6): a class with the `harness` toolset and no `harnesses` gets `"all"` (as `mcp`/`managers` do), and a `PUT /classes` that omits `harnesses` keeps the saved value of a class that had the toolset (one gaining it gets `"all"`) — today's class editor sends no `harnesses` and must neither empty nor widen a list. `harnesses` ids are checked against the sdk catalog, `fake`, bound managers' advertisements, and the class's already-saved ids (a manager that stops advertising one doesn't block edits). `clampTo` a no-egress lane drops `harness` too.
- 2026-09-30 (A6): `GET /harnesses` answers `probe: {ref, ran, cached?, error?}` beside `harnesses` (what `?probe=` did — a stopped, unusable or unseen sandbox is said there, not as an HTTP error; a malformed ref is 400). `sandboxes` lists only refs the caller may see. `installed`/`signedIn` are omitted while unknown (`harness_seen` stores −1); `installed` = every one of the harness's `Bins` found. The 10-min probe cache is per process (in memory); results persist in `harness_seen`. The probe runs through the contract's `POST /run` (`sh -c` over positional args), ≤ 8 s.
- 2026-09-30 (A6): storage shapes for A8: `harness_sessions.turn` is an INTEGER, `argv` JSON text, the other JSON columns TEXT as the engine writes them; `harness_options` has an `at` column; `harness_prefs.updated_ms`. `runStamp.Engine` (not inherited from the parent) sets `runs.engine`; `Run`/`runSummary` always carry `engine` (`""` for the built-in). `deleteOneRun` deletes the run's `harness_sessions` row (the engine must stop the adapter first — A8/A9).
- 2026-09-30 (A6): `manager-error` wins over `no-image` whenever `images` is empty and any bound manager's hello failed. For a non-person caller `setting` is `"approve"`.
- 2026-09-30 (A6 verify): for one of the four catalog ids the catalog's `name` and `login` win over a hello entry's `title`/`login` (docs/sandbox-manager.md says an entry's own fields override): the catalog's login carries the adapter's env (`CLAUDE_CODE_REMOTE=1`), while coding-sandbox advertises `claude /login` — to settle at the integration or in A11's live check. An advertised `argv` does win (what runs and what the probe looks for: `Bins` = its `argv[0]`).
- 2026-09-30 UI-U1: native `ctx.ext` also has `newChat` (the new-chat sheet's section and its part of the ask), like the web's — so U2 adds its sheet field without editing native/convs.js.
- 2026-09-30 UI-U1: the STUB serves every §4 route but the two terminal relays (§4.2.8): they are WebSocket upgrades, which its fake `xbin.fetch` can't answer — U5 stubs them in its own tests.
- 2026-09-30 (A7) §4.2.7: the log tail (`harnessLogTail`, harness_log.go) reads a split exec's stderr with `…/output?stream=stderr`, and the baseline wrapper's file through the contract's `run` (`tail -c` at `${HOME:-/tmp}/.cache/xbin-harness/<run>-<gen>.log`, resolved exactly as the wrapper resolved it), not the files API — the same file whatever the manager reports as `home`, in one call; a missing file is `errNoHarnessLog` ("no log yet"). A9's checks before it are unchanged.
- 2026-09-30 (A7) §3.6 beyond the spec: an exec the manager no longer knows (404 on `…/output` or the socket) is lost like a 410; a read the manager can't answer (503, 429, unreachable) is retried with backoff for up to 2 min before its error ends the stream; a stdio socket that drops is attached again from the offsets read so far, and stdin over it is acknowledged — each frame is followed by `{"op":"ping","t":<seq>}`, whose pong (a manager answers pings in order, after handing the command what came before) acknowledges it; what no pong acknowledged goes again on the next socket (at least once: a frame the command took whose pong the drop swallowed is sent twice); the socket is attached when the pipe starts or attaches, not at the first read; a manager that answers the socket `unsupported` though it advertised `stdio` gets the exec routes. `Process.Stderr` is nil on both transports: stdio's stderr frames only advance `ErrOff` (the log route reads stderr from the manager).
- 2026-09-30 (A7) the pipe's API for A8: `startHarnessPipe(ctx, hpTarget, hpSpawn)` and `attachHarnessPipe(ctx, hpTarget, execID, readOff, errOff)` (no error: a gone exec ends the stream at once with `Lost()`); `hpTarget.Guard` runs before every stdin write and before the reader attaches a dropped stdio socket again (the handoff epoch: a pipe that no longer owns the session ends with its error and lets the command go, as `Detach` — the newest attacher wins, so attaching would take the command from its new owner); `Detach()` lets the exec go untouched (a handoff; output read but not yet handed on is dropped — it is the successor's), `Kill()` is §3.6's eof → TERM → DELETE and `Stdin.Close()` sends the eof in the background (a write retrying a 503 holds the writer for up to 2 min); an exec Kill deleted has ended `killed`, not lost; `harness_pipe_stdio.go` holds the socket half. A `503` on `POST …/stdin` can't say how much of the chunk the command took (tilesbx writes part of it before its 30 s deadline), so a retry may repeat a prefix — the frame is then one bad line the adapter drops; the socket has no such case.
- 2026-09-30 (A7) §7.3: fakesandbox's `-fake-acp` flag (A4's) defaults to `$FSB_HARNESS_FAKE`, else A4's `$FSB_FAKE_ACP`; `Harnesses` nil still means the default `fake` (A4's choice), now titled "Fake agent (tests)" and signed in by `fakeacp login` (`<command> login` from the flag); fsb gains `StdinMax` (hello's `limits.stdinMax` and its enforcement, for chunking tests). sandbox-terminal v5 (its mirror of fsb.go).
- 2026-09-30 (U2) Additions beyond §8's row, for the start flow: `app.newClassId()` (the class a new ask starts in — the resolved one while a coding agent answers new chats; `picked()`/`classOf()` and `app.sbx`'s picker, create form and ask part use it), the sandbox picker's `opts.fits` (a coding agent's reasons), the create form filled in at home for the coding agent picked (a manager and image of its `images`, `internet`, `<id>-dev`), and the web seams' `ctx.sbxUI` (the Sandboxes dialog, so a module can open its create form). The ask never sends `harness.mode` — the backend applies the person's setting. U2's CSS is a `<style>` its module adds (no `harness.css`: the STUB serves only `.js`).
- 2026-09-30 (U3) Native plan and usage: a toolbar `badge` ("📋 3/7 · ctx 26%"; the cost only on Progress — a longer badge pushed the title and the ⋯ menu off a 390pt bar) and a pushed Progress screen (⋯ → Progress: the `plan` primitive, `counts`, usage), not a `plan` at the transcript's end nor the usage in the subtitle — the `end` seam belongs to a run's park (U1's rule: any answer there hides the built-in park card) and there is no subtitle seam.
- 2026-09-30 (U3) `harness.files` is the summary's `counts` (the web's top bar, native Progress) plus each edit card's per-call diffs — no per-turn "files changed" block (§2.3: not in v1).
- 2026-09-30 (U3) U1's `test/harness.mjs` block probe now draws the assistant's text: `ext.block` answers in registration order and the `acp:*` cards are `harness-cards.js`'s (imported by agent.js before a test can register).
- 2026-09-30 (U4) Question forms use the model's own `formFields` (`model/harness-ask.js`: the shell's `/vendor/agent-tools.js` shape plus `enumNames` and `default`), not the shell's module — one behaviour for both views and the node tests (the native view can't import `/vendor/agent-tools.js`).
- 2026-09-30 (U4) A question park in url mode (the WP asked for it; §4.3.4's `question` has no url) is read as `pendingState.harness {mode: "url", url, message}`: the page as a link (http(s) only) and Done (`accept {}`) / Cancel (`decline`). A8/A9: carry those two fields if a url elicitation ever becomes a question park.
- 2026-09-30 (U4) "Steered" is inferred from the stream — a queued message of an adapter that steers (`harness.steering`) left `queued` and then showed up as a user row — since §4 marks no user row as steered; a message that waits is said by the queued chip ("queued for ‹name›" / "steering").
- 2026-09-30 (U4) Native limits (no Swift changes): a multiple-choice field is a yes/no per choice (the `question` primitive draws flat primitives only); url mode is a `markdown` link plus a field-less `question` (a transcript holds no buttons); an explicit permission option is confirmed by a second `approval` (the primitive has no confirm). The per-person setting lives in the web's `#hctl` (a conversation's; at home the harness answering new chats) and the native home toolbar's Coding agents screen plus the Mode menu — settings proper are the managers'. The built-in model picker hides in a harness conversation (`rules.modelPicker`). The native toolbar holds the Mode menu and the Model picker; the other config options (effort…) are buttons in the Mode menu — a phone's bar already drops its ⋯ with two pickers (U4 verifier).
- 2026-09-30 (U5) Native terminals are one pushed screen at a time, not the web's tabs: the app's `terminal` primitive closes its socket when its screen goes and reports no session id, so the native view can neither keep a hidden shell connected nor pass `exec=` to reattach. The D96 difference `tools.sandboxes.terminal` is removed (native uses `GET /sandboxes/{ref}/terminal`); a new one, `tools.terminal.tabs`, says why the tabs are web-only. A relayed shell the app leaves runs on at the manager until it exits (the tile has no route to end an exec) — A9's relay may end a tty exec it started itself when its client goes (no `exec=`), which the contract allows.
- 2026-09-30 (U5) The web never uses the relays: every web terminal (shells, the login tab: `tty?cwd=&cmd=<login.command>`) dials the manager directly as the verified person, as §4.2.8 says. A coding agent's conversation gets a top-bar ">_ Terminal" (a shell at `harness.sandbox.cwd`; native: ⋯ → Terminal on the run relay) besides the ▣ popover's Open terminal. The sign-in card treats a sandbox as shared when `harness.sandbox.shared` says so or its `GET /sandboxes` row is team-visible, has members or shares; a 409 `{confirm: true}` from `authenticate` also turns the confirm on.
- 2026-09-30 (A8) `harness_sessions` gains `title` (the adapter's session title, §4.3.2 `title`), `shared` (§4.3.2 `sandbox.shared`, from the manager's sandbox at each spawn) and `draft` (the text and thinking read but not yet flushed to a row, JSON): every durable event stores the draft with `read_off`, so `read_off` always advances to the event and a successor restores the draft rather than re-reading chunks the committed events came after (§3.3 "stored with the next durable event").
- 2026-09-30 (A8) An attach's request-id prefix is `h<run>.<gen>e<epoch>a<n>`, not `h<run>.<gen>`: the successor talks to the same adapter process as its predecessor, whose ids (`h<run>.<gen>-N`) the adapter may still answer. Stored call ids stay `h<gen>:<id>` (the spawn's generation).
- 2026-09-30 (A8) `prompt_state` goes `sending` in the transaction that writes the user row and `sent` (with `prompt_rpc` and `turn`) the moment the `session/prompt` frame has been written to the adapter's stdin (the engine watches its writes) — so a handoff between the two finds `sending` and fails the prompt ("send it again"), never one the adapter didn't get marked `sent`.
- 2026-09-30 (A8) part (a) scope — the rest is part (b)'s: a permission request parks (§4.3.4, queued behind a park in force) and `POST /runs/{id}/approve {approve, park}` answers it with the first non-explicit allow (allow_once first) or reject; no `option`/`feedback`, no reject-on-reply. A form question parks as `question` (no `hanswer` handling yet); a url question is always declined. `/interrupt` and `message {interrupt: true}` send `session/cancel` (the adapter ends the turn); `/cancel` and `DELETE` also end the adapter; `/resume` respawns when none is live and resends `held`. A signed-out adapter fails the start with its login command (no `login` state yet); no steering (a message during a turn waits for its end), no idle reclaim (an idle adapter stays driven — and holds the keep-alive — until part (b)'s timer), no binder re-check on durable events, no stop on detach; a prompt's files are named in its text (no resource links yet).
- 2026-09-30 (A8) `pendingState.harness` leaves out false/empty fields (`defaultToNo`, `description`, `planApproval`, `plan`) rather than sending `false`/`""`; the summary's `activity` is present whenever this process drives the session (`idle` included) and absent otherwise; `hasWork()` also leaves out `hnote` rows (they never start a turn).
- 2026-09-30 (A8) A completed call without text, diffs or output has its title as its result (else "done"); a failed one `error: <its text, else its output's tail, else "the call failed">`.
- 2026-09-30 (A8, part b) §3.6 / A7's stdin at-least-once: over stdio only JSON-RPC responses and notifications go again after a drop; a **request** whose first chunk went on a socket that dropped before a pong acknowledged it is given up on (a newline takes its place, ending whatever part the command took) and reported (`hpTarget.Dropped`) — the engine ends its call with `acp.Client.Abandon` (added to `sdk/acp`: the call fails as if the agent had answered an error; a session/prompt's turn ends `error`), so a prompt fails "send it again" and is never sent twice. The exec routes' POST retry is unchanged.
- 2026-09-30 (A8, part b) codex's detached turn (`startedNewTurn`, no `turn.end`): the run follows it as working and ends its turn (`end_turn`, the draft the answer) once the adapter has sent nothing for 5 s; a message meanwhile waits (`Steer` answers `promptRequired` with no prompt of the client's in flight); an interrupt ends it in AgTT's view only (the client has no prompt to cancel).
- 2026-09-30 (A8, part b) Interrupt: the park settles `(interrupted)` in AgTT at once (before `session/cancel`); an adapter that hasn't ended the turn 15 s after `session/cancel` is killed (the turn ends with a note). A message while a **login** park waits stays queued (it isn't a rejection); `/resume` on a login park ends the signed-out adapter and starts a fresh one before resending `held`.
- 2026-09-30 (A8, part b) Login: `harness_sessions.state` `login` keeps its adapter (`hsRunning`: starting, live, login — `recover()` attaches login sessions too); a codex-like refusal of `session/new` signed out (`AwaitLogin`) parks on login with the adapter up. `login.methods[].kind`: `terminal` = `type: terminal` or `_meta["terminal-auth"]`; `api-key` = `_meta["api-key"]`; `device-code` = an id or name containing "device" (ACP names no kind). The engine side of `authenticate` is `Engine.harnessAuthenticate(ctx, run, method, apiKey) (*hAuthResult, error)` (errors `*hAuthErr{Code, Msg}` for the route); one sign-in per session at a time — **409** `a sign-in to ‹name› is already under way` (added).
- 2026-09-30 (A8, part b) Output lost (a ring gap) while a prompt is in flight or something is parked kills the adapter and stores the session `lost` (the next prompt resumes it); a gap while idle is only noted. A binder refusal (on a prompt, steer or answer — skipped right after the start that checked — or at most once a minute on durable events) and a detached sandbox stop the session with state `failed` and the why, through a new internal inbox kind `hstop` {reason} (older binaries ignore it).
- 2026-09-30 (A8, part b) A `user` inbox row on a harness run (an older binary's message, a parent's) is a prompt; one from a schedule, trigger, channel, watcher or `/learn` (and `watch` rows) is consumed with a note. Deleting a run tree ends its adapters on every path (`deleteRunTree`), not only `DELETE /runs/{id}`. A permission request a remembered rule answers never parks.
- 2026-09-30 (A8 verify) §4.3.4: a plan approval's `tool.content` is left out when `plan` holds the plan (claude's ExitPlanMode and codex's plan review carry the plan as the call's text content too) — the plan is said once, as the fixtures' `content: []` has it, and the card doesn't show it twice.
- 2026-09-30 (A9a) §5.2: `xbin.RelayManagerTTY` returns `ManagerTTYRelay{Session, Exited}` (the session frame's exec id; whether the exit frame passed) and `ManagerTTYOptions` (A4b's name for §5.2's `ManagerTTY`; `ExecID`, not `Exec`) gains `OnSession func(execID)`, called as the session frame passes, before the client has it — what the end-on-disconnect rule below needs; A4b had shipped neither.
- 2026-09-30 (A9a) §4.2.8, beyond the spec (U5's open issue): a relay that STARTED a terminal (no `exec=`) — a shell or a sign-in — DELETEs its exec (as the person) `ttyEndGrace` = 5 s after its client goes, unless the exit frame passed or a client attached to it again through a relay here (`exec=`, before or within the grace): that client keeps it for good. The registry is per process (a blue/green successor doesn't end its predecessor's terminals). `exec=` ignores `login`, `cwd`, `cmd`, `rows`, `cols` (the SDK refuses them on an attach; a reconnecting client may still send them) rather than refusing. Added refusals: 400 `rows: a number of character cells` (likewise `cols`); 409 `‹name› has no sign-in command to run in a terminal` for `login=1`. ‹binder› in `you may not use ‹sandbox› — ask ‹binder›` is who bound the sandbox into the conversation (`Config.Sandbox.By`, else `Harness.By`) when a person, else the sandbox's owner, else "this agent's managers" (the sandbox relay: its owner). `login=1`'s command is `harness_sessions.login`'s `command` (the §4.3.2 `login` object as A8 stores it) when set, else the catalog's `LoginCmd`, else the manager's advertised `login` for the sandbox's image.
- 2026-09-30 (A9a) §4.2.7: 404 `no log yet` also when the run has no `harness_sessions` row, or one never spawned (`gen` 0, no exec); `max` that isn't a positive number is 400 `max: a number of bytes, at most 65536`, one over the cap is the cap; a non-person gets the 403 (with the binding's name for ‹sandbox›); the tail is of the session's current `exec_id`/`gen`. As A7 built it, the baseline file is read through the contract's `run`, which starts a stopped sandbox.
- 2026-09-30 (UIint) Native bars: a coding agent's conversation carries its chip, plan and context at the start of the subtitle (a new native seam `subtitle`), not a toolbar badge — with Mode, Model and a badge, More fell off a 390pt bar in every state but a sign-in; the home's "Who answers" is a picker at the top of the home page, not in its toolbar (class + model + Who answers already pushed More off), and U4's Coding agents is an item of the main ⋯ menu (a new native seam `main`, home's and the drawer's). The web composer groups its pickers (`.cpicks`, where `#hctl` now goes) apart from the message row (`.cinput`), which wraps to a line of its own when both don't fit. The top bar hides Memory and Learn skill for a harness run, offers Compact only when `harness.commands` has `compact`, and Retry for `lost`/`failed`; a login park's composer and activity line say sign-in (no spinner); the sign-in card, the native Sign in… and ⋯ → Terminal need `access(v).talk`.
- 2026-09-30 (U67) Native child cards: Stop, Cancel and Message are not on the card and a sign-in is only a notice there — the app's `toolcard` holds no button or field. ↗ opens the child's own chat, whose composer messages it directly (the parent is told), whose Stop interrupts it, and whose ⋯ menu gains Cancel task (a `menu` hook for a harness child: confirmed); Sign in is U5's, in that chat. A parked child's card opens by itself so its permission or question (U4's primitives) shows; on the web everything is on the card.
- 2026-09-30 (U67) The card's summary is what the tile already holds — the link's `child` (§4.3.7) under the run and `harness` events (`Session.runs`) — not `/tree` (§4.3.6, the board's); its last 3 blocks are the child's newest page (`GET /runs/{child}/view?limit=8`, `Session.fetchView` gains `limit`), read once the card is open and on screen and then kept current by the stream. §4.3.8's `waiting` makes the row's `?` (`rules.rowGlyph`) and `kids.harness` its `⧉ N`; Needs' `login` reason stays U7's (§8).
- 2026-09-30 (U67) The board's rows: in a conversation, the harness runs below its root (a coding agent's own conversation is not a row of its own board — its top chip is U2's); at home, your coding-agent conversations that run or wait plus the harness runs in the trees of your rows with `kids.harness` (at most 12 trees). The tree (§4.3.6) is read once a coding agent is known to be there (the row's `kids`, a held link) and again only when the stream names a run or link it lacks — status, park and `harness` changes come from the run, link and `harness` events laid over the nodes (and a held link's `child`, which carries `pendingState`), not from a re-read per change; at home (the stream follows the run list only) a tree is read again when its root's row changes, so a child's change there shows at its root's next event. A parked row with only the compact `harness.pending` reads the child's newest page (`?limit=8`) once to answer it in place. The web rows are U6's cards (no separate steer box); neither view has the UX's per-row Terminal.
- 2026-09-30 (U67) Native board: the app's `badge` takes no tap, so the way in is a toolbar `button` while one needs you (not in a coding agent's own chat, whose bar already holds its badge, Mode and Model; at home while one runs or needs you) and ⋯ → Coding agents (N); Message is a pushed screen (a multiline field, Send / Send now), not a sheet — the seams have no sheet; a parked row holds its `approval` or `question` as row content, its sign-in only says to open it. Both views gain a `task` seam (the unfolded pinned task / the Task screen) for the Delegated section. Needs' `login` names the coding agent when the item carries `harness` or is the coding agent's own conversation, else "a coding agent needs you to sign in" (A9: a `harness` on a sub-run's item would name it).
- 2026-09-30 (A9b) §4.2.4: `PATCH` with neither field is 400 `mode or option: name one`; the adapter's answer is bounded (30 s → **504** `‹name› didn't answer`; `sdk/acp` `SetOption` now honours its ctx — it used `Call`); before any session `option.id` is checked against the options the harness last reported (`harness_options`, the catalog's); a signed-out adapter with no session yet only stores the choice; 503 also while another process is still starting the adapter (a running session row with no ACP session in its snapshot). A session with no adapter (`stopped`, `lost`, `failed`, `none`) shows the stored `Config.Harness` mode and options as current in its summary — what its next start sets.
- 2026-09-30 (A9b) §4.2.5: `park` is optional (the question pending now), as `approve`'s. §4.2.6: the route answers in this order — not a person 403 (the Use words), not `login` 409 `‹name› is signed in`, `method`/`apiKey` checked against the stored `login.methods` (so a bad request isn't asked to confirm first), the fresh Use check 403, the shared-sandbox 409 `{confirm: true}`, then the engine; an engine error that isn't one of its own is 504 `‹name› didn't answer its sign-in`.
- 2026-09-30 (A9b) §4.3.9: every `/needs` item whose waiting run is a coding agent's carries `harness` (the compact §4.3.6 view — not only for `login`), so a sub-run's approval names it too. §4.3.8: `kids.harness` counts coding agents below the root in an active status (running, queued, blocked, awaiting, sleeping, waiting_input). §4.3.1: `POST /ask` and `POST /runs` answer a coding agent's run as the `Run` JSON plus its `harness` (as the STUB did — its sidebar row). The push (needs_push.go) says a coding agent's park in its words; a sign-in is push kind `login`.
- 2026-09-30 (A9b) §4.2.11 beyond the table: schedules and triggers also refuse a coding agent's conversation as `targetRun` (400 `targetRun: a coding agent's conversation takes messages from people — schedules run the built-in agent`), and `PATCH /runs/{id}` refuses `model`/`sandbox`/`detach` when the path's run or its root is a coding agent's. `harness_sessions` gains `name` (the harness's name as the manager advertised it at the spawn — the summary's `name` for one the sdk catalog lacks) and `started_ms` (`session.startedAt`).
- 2026-09-30 (A9b) The STUB-drift guard (not in §8): `_backend/testdata/harness_shapes.json` (TestHarnessShapes: the real backend's key paths and types per shape; it fails when the backend stops producing one) and `hack/agent-template-harness-shapes.test.mjs` (the STUB over the fixtures may serve only those). It moved the STUB and fixtures to the backend: the ask's answer has no `pendingState`/`class` (the class is the view's), `config.sandbox` has no `shared`, the catalog's `modes` no `description`, `/needs` items carry `harness` and `subRun`, tree nodes keep `login.device` (only `methods` go, per §4.3.6).
- 2026-09-30 (A10) §4.4 the parent never answers a child's park, so a `subagent_message` (an `hprompt` from `parent`) to a harness child parked on a permission or question **waits for the person's answer** instead of rejecting it first as §3.5's reply rule does (a person's reply still rejects; `harnessPass` picks the reply among the non-parent prompts); its reply says so: `queued: #N is waiting for a person to approve: ‹title› — it gets your message once they have`. A message to a child whose link already settled opens a new link (the built-in rule), which settles at the end of the child's next turn — a turn a person's direct message started, if one runs.
- 2026-09-30 (A10) §4.4 `harness` with `after` is refused (`after: a coding agent starts at once — wait for those first (subagent_wait), then start it`): a dependency's results reach a built-in child as a user row written at its start, which a harness child has no place for. `harness_mode: "plan"` for a provider without a plan mode is its approve mode (opencode: neither — its own). A spawned child's `Harness.By` is `""` (the agent started it); its system row is its class's prompt (no subagent contract).
- 2026-09-30 (A10) §4.4 the enum for a binding whose manager says nothing (`harnesses` nil, a hello predating the field) is the sdk catalog's four; the spawn refuses one `harness_seen` knows missing or a manager that advertises says the sandbox's image lacks (`‹sandbox›'s image doesn't have ‹name›`, POST /ask's check — a binding stores nil for both "says nothing" and "none here"), else the first start probes. `maxHarness` counts harness runs below the root in an active status (a turn running or parked on a person) — an idle child with a live adapter doesn't count (its exec does, in the exec-room check). The exec check lists the sandbox's execs through the binder's `sandboxUse` (the same rights check as any sandbox tool) and is skipped when the hello has no `limits.execsRunning`.
- 2026-09-30 (A10) §4.4 the digest: `#N ‹label› — ‹phase› for ‹time›, harness ‹provider› · N tool calls · $0.40`, then `waiting for a person to approve: ‹title›` / `… to answer: ‹question›` / `… to sign in` (also in the short digest), `doing: ‹activity›`; the phase is `waiting for a person` while parked and `failed`/`lost` with the error when resting so. The digest's lines are newline-separated for every child now (a digest without recent calls glued `latest text` to its head line).
- 2026-09-30 (A10) §4.3.13 the `hnote` row is `{text: "[direct message to #N (‹name›) from ‹user›]\n‹message›", source: "harness", from: N, sender: ‹user›}` on the parent, written after the child's `hprompt` (not for a retried `clientId`); delivered by `deliverBoundary` as a plain user-role message (no meta, no task-ledger entry). A message of files only reads `(files: ‹names›)`.
- 2026-09-30 (A11) §4.3.2 `mode`: the session's modes, else the adapter's config option of category `mode`, else the catalog's — opencode 1.18.32 answers `session/new` with no `modes`, only a `mode` option (`build`/`plan`), so its picker was empty (found live; `harnessModes`). It also takes `session/set_mode` (answered `{}` live), so a stored mode reaches it on a respawn as the sdk sends it.
- 2026-09-30 (A11) The live check is `test/isolated` `TestHarnessLive` / `TestHarnessLiveVM` (and `hack/harness-smoke.sh`, `HARNESS_SMOKE_LIVE=1`): the fake adapter reaches the sandbox through the files API (copied to `/work/.xbin-a11/fakeacp`) and the image's advertisement (`fake`, an absolute `argv`), not a bind; the handoff is a live-reload redeploy of the agent's backend (a new file under `_backend`) mid-turn. The exec baseline is exercised live only by the test's own handshake (stdin POSTs, the base64 long-poll) — the pipe takes `stdio` wherever the runtime offers it, and there is no switch to make it use the baseline.
- 2026-09-30 (A11) Settles A6's open point on claude's login: live, the adapter itself offers a terminal method under `CLAUDE_CODE_REMOTE=1` (`claude-login`, `_meta["terminal-auth"]` = `node …/claude-agent-acp --cli`: Claude Code's own onboarding, a theme and then its sign-in), which `login.command` uses once a session exists; before one, the catalog's `CLAUDE_CODE_REMOTE=1 claude /login` is right — coding-sandbox's advertised `claude /login` lacks the variable, and without it the adapter offers the browser sign-ins (`claude-ai-login`, `console-login`: `auth login --claudeai|--console`, a localhost redirect a sandbox can't take).
- 2026-09-30 (U9) §7.3: the harness pass's `fake` is `FSB_HARNESS_FAKE="$REPO/bin/fakeacp --steer --auto-mode --require-login --persist"`, set in run.sh's xbind environment: without `--isolate` apps/fakesbx runs on the host, every sandbox command is a host process, and a variable without the `XBIN_` prefix reaches its backend (`internal/runner` hostenv) — no copy of the binary into the tile; under `HARNESS_ISOLATE` the pass SKIPs. The sandboxes not used for the sign-in shots are signed in through the manager's files API as the person (`PUT …/files/content?path=<home>/.fakeacp/credentials&mkdirs=1`).
- 2026-09-30 (U9) §3.4 beyond the spec: a `tool_call` under the id of a call of this generation that already ended is a new call, stored as `h<gen>:<id>#<n>` (its updates, park and parent links follow the newest); ACP says an id is unique in a session, but acptest's scripts reuse theirs from turn to turn (`perm` is `t1` every time), which rewrote the earlier call's card. A successor reads the counts back from the stored rows (`harnessReuse`).
- 2026-09-30 (U9) U4's steered note (§10 U4) is said only for a message queued while a turn ran (`harness.state` `working`, a user row before it) that left the queue while it still ran: the real view lists every waiting `hprompt` — a message sent between turns, and a new conversation's first one (the summary says `working` once the session is up, before its prompt goes), were said to be steered. fold.js's "a creation note precedes the first message" rule skips a coding agent's run (none is created by an automation), so a sign-in note written in its first prompt's second follows it.
- 2026-09-30 (U9) Native: a multiple-choice field's per-choice yes/no (U4's DIFFERENCE) is titled `‹question›: ‹choice›`, the first carrying the question's description — the flat form had lost the question; the Sign in screen's Retry (and its login terminal's) leaves for the chat.
- 2026-09-30 (Afix) §4.3.12 bypass modes are default-deny: `explicit` came only from the sdk catalog's flag, so a harness a manager advertises that the catalog lacks — or a mode a newer adapter adds — could be switched (PATCH), asked for (POST /ask by a non-person) or allowed (a plan approval's option) into bypass by any participant. Now only a mode `acp.Provider.Safe` knows (non-explicit `Modes`, new `SafeModes`: opencode's `build`, `plan`), or the one the adapter opened its first session in with no mode asked (`harness_sessions.start_mode`, new column), is open; the rest is owner-only (the rule in §4.3.12). A permission option is `explicit` only when it is an allow that switches to such a mode (a reject never is; see the verifier's line below for the options that name no mode). A stored option the live adapter reports as category `mode` is no longer set by the handshake (`acp.Config.SkipModeOptions`, new) — it leaves `config.harness.options` with a note (§4.2.3 said so; nothing did it). Both views' ⚠ reads the summary's and the park's `explicit` through `model/harness-ask.js` `controls`/`permission` — no UI change.
- 2026-09-30 (Afix) §3.5/§4.2.11: `POST /runs/{id}/cancel` on a coding agent's run that rests with its adapter up (`cancelRuns` skipped inactive runs, so it did nothing) now queues the cancel row too (and `subagent_cancel`, a halt and a delete's fan-out with it — as does every `cancelBelow`: an idle coding-agent child's adapter stops when its parent's turn ends as "nothing below outlives the turn" has it (a subagent parent's every turn end, a top-level one's `finish`), when the owner interrupts the parent, and on a channel's stop); the pass stops the adapter (state `stopped`, a note "‹name› stopped (‹reason›) — the next message starts it again") and leaves the status as it was — there is no turn to cancel, as the built-in engine leaves an idle run. `cancelled` lists the run.
- 2026-09-30 (Afix) §3.4: a child's link settles with this turn's text only — the newest assistant text after `harness_sessions.turn_seq` (new column: the turn's first row, set as the prompt's user row is written, and at a codex detached turn's steered row), `(no answer)` when the turn wrote none — not the run's newest text, which was an earlier turn's. §4.4: a harness child's system row drops a subagent parent's subagent contract (`strings.TrimSuffix`).
- 2026-09-30 (Afix) §4.2.4: a PATCH with `mode` and `option` whose mode the adapter took and whose option it refused stores the mode (the next start keeps it), publishes the change and answers the option's refusal (502/504); a refused mode still stores nothing. Engine errors of `authenticate` (and the `hnote`, approve and compact texts) name a coding agent as its manager advertised it (`harness_sessions.name`), not its id.
- 2026-09-30 (Afix) §4.3.2 `mode` for an adapter that speaks its modes as a config option of category `mode` (opencode): the sdk handshake's `session/set_mode` for a stored mode now also sets that option's current value (the snapshot, so the summary shows it), and isn't sent when the option already has it.
- 2026-09-30 (Afix) §3.2 the rollback caveat held only for idle runs — an older binary's recover() and pass reach `turn()` for a running, parked-and-answered or messaged harness run. Mitigated without changing old binaries: `runs.turn_steps` of a harness run is kept at maxTurnSteps' ceiling (500) by two triggers (`harness_turn_cap_ins`/`_upd`, AFTER INSERT and UPDATE OF status, engine, turn_steps), so v0.3.59–v0.3.64's `turn()` ends any turn there at its step cap before a model call (its note: "stopped after 500 steps in one turn (maxTurnSteps)"); this binary never reads turn_steps for a harness run. What still happens first there is written in §3.2.
- 2026-09-30 (Afix) §3.3 `hasWork()` also counts a harness session whose adapter runs (`exec_id` set, state starting, live or login), idle too: a process that exits with no successor leaves the resume job, and the next one (≤ 1 min) attaches the adapter and re-arms its idle reclaim. `BeginShutdown` still never stops an adapter — a process can't tell a blue/green handoff from a last exit (the successor waits on the lock unseen). With `harnessIdleMin` 0 that keeps the backend running while an adapter does, as its keep-alive hold already did.
- 2026-09-30 (Afix) coding-sandbox's default image advertises claude's sign-in as `CLAUDE_CODE_REMOTE=1 claude /login` (A11's finding) for a new or never-saved config; a saved config keeps the logins it was saved with (the harnesses already did). Builtin templates carry no tile version (`hack/tile-versions.txt` is the builtin tiles'), so there is none to bump.
- 2026-09-30 (Afix) verifier: claude-agent-acp 0.81 (the rootfs pin) offers its plan approval's modes under ids that name none — `exit-plan-bypass` / `exit-plan-clear-bypass` ("Yes, [clear context and] bypass permissions", `allow_always`), `exit-plan-auto`, `exit-plan-clear-auto`, `exit-plan-accept-edits`, `exit-plan-clear-accept-edits`, `exit-plan-default` (`allow_once`) — so the rule "an allow that names such a mode" let any participant approve a plan into bypass. The sdk catalog gains `acp.Provider.OptionModes` (claude: those ids → their modes; `json:"-"`), and an option is `explicit` when the mode it names (its id, else `OptionModes`) isn't open, or — default-deny — when it is an `allow_always` of a `switch_mode` call whose mode the catalog can't place (a harness the catalog lacks, a newer adapter's id). An `allow_once` that names no mode stays anyone's (codex's `implement_plan`, gemini's `proceed_once`, the fake's `exit-plan-default`), so `approve: true` still works for a participant.
- 2026-09-30 (engine-fix) §3.3 attach: a successor's attach the conversation's rights refuse (`harnessUse`: egress none, the binder's Use or part gone, the sandbox detached, the harness gone from the image) stops the adapter as a refusal mid-turn does (`stopHarnessNow`: killed, `failed` with why, a park settled `(stopped)`, the turn ended) — it was only stored `failed`, the adapter left working unread and the run `running`. Ending an adapter this process doesn't drive (`dropExec`: a predecessor's, half-started or refused) needs no rights — its manager is called for the binder (the harness's starter once the binding is gone) over the exec routes — and `/cancel`, a delete, an `hstop` and a sign-in's Retry also end one stored `failed` or `lost` (a stop whose kill may not have got through; a pipe cut off by a manager that stopped answering). An attach the manager doesn't answer (`unavailable`/`unreachable`: it restarts too) is tried again by the run's one-shot timer, 2 s doubling to 1 min (a count per run, in memory), until it takes, the exec turns out gone (the turn ends, `lost`) or the rights refuse it.
- 2026-09-30 (engine-fix) §3.3 attach: an adapter parked on its sign-in with no session (codex refuses `session/new` signed out: `AwaitLogin`) is attached like one with its session open (the snapshot has `authNeeded`; `authenticate` opens the session through it), not ended as "still opening" — which left the run parked on a sign-in `authenticate` refused ("is signed in"); a sign-in park with neither is left as a Retry leaves it. codex's detached turn (§10 A2) is followed again by the successor: the record already says it — the run `running` with no prompt in flight once a turn began (`turn_seq` > 0; a new run is `running` before its first prompt goes) — so it isn't persisted separately; its quiet timer ends it, an interrupt ends it (one queued before the attach too), a message waits for its end. A stdio socket closed `4001 replaced` while this process still owns the session (`hpTarget.Guard` nil: a successor bumps the epoch before it attaches) is another client's, not a handoff: the pipe attaches again (logged) instead of letting the session go unread.
- 2026-09-30 (engine-fix) §3.3 consistency, §3.5 steering: what a handoff may catch between an event and its commit. **Questions:** the snapshot is the client's state, which may be ahead of `read_off` (the read loop files a question before the consumer applies the event before it), so the successor restores only the questions the record knows — the question park's, the queued ones', one whose answer is on its way, a sign-in's url one — and keeps `elicitNext`: a question dropped is read again and parks (a url one is declined), where restored it was taken for one already filed and nobody was asked. **Answers** (a permission's option, a question's action) are recorded in `harness_sessions.answers` (additive) before they go and forgotten once the adapter has them (a stdin POST answered; over stdio, a pong): the client's resolution clears the park before its reply is on the wire, so a handoff while the reply still retries lost it; the successor answers each recorded one again (a permission by its rpc id, never its recorded pid — a successor's pids start over, so another request may be pending under it; one whose park cleared by the reply alone — the adapter ignores a second answer). **Steers** are at most once, as prompts: the inbox row is marked on its way (`harness_sessions.steer_row`, additive) before `_session/steering` goes (its answer bounded at 30 s); one that never left or that the adapter answered with an error waits for the turn's end; one whose answer is lost (a handoff, a stdio drop, the timeout) — or a mark a successor finds — is written as the user row with a note that the coding agent may not have it, never delivered again. **A device-code sign-in** a predecessor started: its `authenticate` answer is lost with it, so the successor takes the adapter's `elicitation/complete` (the url question restored while the login shows its code, accepted again) as the sign-in done and wakes the run as Retry does (a fresh adapter reads the stored sign-in, the held prompt goes); a shown code with no url question restored is taken away. `sdk/acp`: accepting a url question moves it to the accepted ones in one step, so `State()` never misses it between the two.
- 2026-09-30 (routes-fix) Classes (§2.2, A5): settings `k='classes'` stores the `harness` toolset apart from `toolsets` (`harness: true` on the stored class; toolsets that already hold it are read as they are) — an agent from before coding agents (v0.3.64) rolled back to refused every class save (`class coding: unknown toolset "harness"`: its editor resends every stored class, its normalize knows no such toolset). `GET /classes` and the API are unchanged; a class save on such a build rewrites every stored class (its editor resends them all), so every class loses its coding agents (verifier: said so in API.md and the changelog).
- 2026-09-30 (routes-fix) §3.2 `hnote`: kept in a table of its own, `harness_notes (id, run_id, after, body, created, delivered_at, msg_id)`, not `inbox` — `after` is the inbox's highest id when it was written, so `deliverBoundary` reads it among the parent's rows in the order they came; on start, undelivered `hnote` inbox rows an earlier build of this program wrote move there (and, verifier, a run's own as its input is read: that build's process still serves through a blue/green swap after the start's move, and a late row read as a note would name no `harness_notes` id); a deleted run's notes go with it. An `hnote` row an idle parent kept for good (by design) was work to a rolled-back v0.3.64 forever (its `hasWork` counts every undelivered inbox row, its pass never consumes the kind): its resume job woke the tile every minute. `hprompt`/`hanswer` rows queued at a rollback still do — documented (§3.2, API.md, changelog) with the clean-up.
- 2026-09-30 (routes-fix) §4.2.6/§4.3.2 a device code is the requester's alone: `harness_sessions.login` and the park's `login` store — and every summary, view, `/tree`, `/needs` and event publishes — `login.device: {by}` only; the page and code stay in memory with the sign-in (`hAuth.dev`), answered in the 202, in the requester's own `GET /runs/{id}/harness` (`{url, message, by}`, while this process drives the session), and again (202) to the same person asking again for the same method (else 409 `already under way`). Everyone who saw the conversation — a viewer, a participant `authenticate` refuses — could read the code and enter it first, signing the shared sandbox's harness in as themselves (A9b had put it in the summary for the UI; its cards already fall back to the 202). The app keeps the 202's device per park for its transcript link too. `Engine.harnessAuthenticate` gains `by`. (verifier) The client's snapshot (`harness_sessions.snapshot`, which keeps an accepted url question for a successor) is stored without the question's `url` and `message` too (`storedState`): a successor restores it by its ids.
