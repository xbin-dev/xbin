# Workspace agent — the first run against a real model (2026-11-17)

> Status: live — a measurement, what it found and what was changed for it
> (branch `wsagent/real-model`, from master `c4e2e770`, the day after
> v0.3.80). Everything before this had run against scripted models
> (`hack/fakeopenai`) and a scripted coding agent (`hack/fakeacp`).

The endpoint: an OpenAI-compatible gateway the owner provided for a day,
which serves **chat completions only** (`POST /responses` is a `404`), with
the model `deepseek/deepseek-v4.1-flash` — a provider id with a slash in it.
Only synthetic data was used, on throwaway local workspaces; the only remote
host contacted was the endpoint. The key was read by xbind from a file (the
host file's `keyFile`); it is in no command line, log, transcript or commit.

## 0. In short

- **It works on a chat-completions-only endpoint — after one fix.** The
  workspace agent's own engine was fine as released (it picks the chat
  wire). The LLM connector was not: it relayed `/v1/responses` to the
  upstream's `404` while offering Codex — which speaks Responses only — as
  the first coding agent that needs no sign-in, so every tile agent a plan
  started would have failed. The connector now translates (D244); Codex
  built the journey's tool through that translation in every benchmark run.
- **The scenarios**: 29 graded (the 28 of the list, and a web page that
  tries to direct the agent). Read against the transcripts: **PASS_N pass,
  PARTIAL_N partial, FAIL_N fail** — §3.
- **The benchmark, for real**: from a fresh workspace, one prompt → the
  five-line plan → one review → Codex builds the MCP tile → xbind binds the
  waiting line → the agent checks and reports → the assistant rolls with the
  tool. It reached the end in BENCH_OK of BENCH_N runs; **14–17
  interactions to the agent's "done"** (13 scripted), 5½–6½ minutes of wall
  time from the login URL to the roll, 0.6–1.0 M input tokens (≈ 85 %
  cached) — §4.
- **What the run changed in the product** (§6): the connector's translation;
  the engine's task reminder, which a real model reported as an injected
  instruction; four tool results a real model misread; four rules of the
  prompt; an ordering race in `sdk/acp`'s steering. Each behaviour fix has a
  scripted twin.

## 1. The setup, and how to repeat it

```sh
hack/wsagent-real-run.sh <base-url> <model> <key-file> [--out DIR] [--bench N]
# e.g.
hack/wsagent-real-run.sh https://llm.example/v1 vendor/model ~/.cache/llm.key --bench 3
```

It needs what `make integration` needs (`hack/dev-setup.sh --check`), and
for `--bench` a Playwright checkout (`PLAYWRIGHT_DIR`). Run it with the tool
sandbox off. What it does:

- **The scenarios** — `test/isolated/wsagent_real*_test.go` (skipped unless
  `XBIN_REAL_LLM_BASE` is set, so CI never calls a provider): an xbind as
  an install runs one (no `--dev`, `--isolate`, owner auth, the vault, the
  release's sealed `xbin/wsagent`), its connector set by a host file
  (`XBIN_LLM_CONFIG`) — the endpoint as the upstream `real`, marked private,
  and again as `hosted`, unmarked, for the model-rule scenarios; a
  **per-person quota** (3 M tokens a day) so a loop cannot run away. The
  workspace (`wsagent_real_seed_test.go`): Invoices (seven invoices, three
  overdue; marking one paid and sending a reminder are writes), Orders (a
  note from "web-form" tries to instruct whoever reads it), Calendar (its
  sync fails and says why in its log), pages Sales and Report, and Ledger
  in an org that allows one model only. People: an admin and three users.
  Each scenario runs in a new session and is driven as its person would:
  approvals and denials with their own sign-in, a tile agent's permission
  requests answered where the scenario says so.
- **Tile agents in the scenarios** are the scripted coding agent, in
  Invoices, which *really makes the change it reports* (it puts an overdue
  filter into the backend, waits for the rebuilt tile to answer with it,
  and says what it did and did not check). A first attempt used a script
  that only printed a report: the model called the new route, found
  nothing had changed, said so — correctly — and every scenario after that
  point was about the lie. A real coding agent is `XBIN_REAL_TILE_AGENT=real`:
  one turn of Codex on "add an overdue filter" was 33 model calls and
  1.6 M input tokens (it wrote the filter, a 322-line page, a test file,
  and was checking the page in a headless browser when the run stopped it
  at its cap of 60 calls a scenario) — the benchmark's job, not the list's.
- **Scenario 29** (a web page that tries to direct the agent) can't run on
  a real xbind under the run's rules: `web_read` dials public addresses
  only, and nothing but the endpoint may be contacted. It runs in the agent
  backend's own tests with the real model and a faked xbind and page
  (`builtin-templates/agent/_backend/ws_real_test.go`).
- **The benchmark** — the harness pass `journeyReal`
  (`hack/ui-harness/passes/journeyreal.js`): `journeyAgent`'s journey with
  nothing scripted, counted through the same counter.
- **The grades are mechanical** (which tools were called, what reached
  xbind, a few words of the answer). §3's are those, corrected by reading
  each transcript; where the two differ the row says so.

## 2. Chat completions only

| What | As released | Now |
|---|---|---|
| The workspace agent's engine | works: `auto` picks the chat wire for every model that is not an OpenAI reasoning family, and falls back by itself | unchanged |
| The connector's `/v1/chat/completions` | works: streaming, parallel tool calls (this gateway sends both calls in one chunk), usage, the `event:`/comment lines it sends before the first data line | unchanged |
| The connector's `/v1/responses` | the upstream's `404` | translated to a chat completion and back, event by event (D244) |
| Codex as a tile agent (the default pick: the first the connector serves) | offered with "no sign-in needed"; could not make one call | builds the journey's tool (§4) |
| OpenCode as a tile agent | OPENCODE_ROW | OPENCODE_ROW2 |
| A model id with a slash | fine everywhere it was tried: `GET /v1/models` (`real/deepseek/deepseek-v4.1-flash`), a call by full name and by the provider's own id, the default, a role, an alias, the usage row, the provider's Test | `TestModelIDsWithASlash` holds it |

The admin's side of setting such an endpoint up was checked through the
API the page uses (the `llm` harness pass covers the page with scripted
names): an upstream with no model list passes the provider's own list
through, ids with slashes included; Test answers; usage is metered under
the full name. Two things an admin should know, neither a bug: the owner
wrote the model as `deepseek-v4.1-flash` — the gateway only knows
`deepseek/deepseek-v4.1-flash`, and its own "model not found" is what the
connector passes on; and an upstream *named* `deepseek` would read
`deepseek/deepseek-v4.1-flash` as "the model `deepseek-v4.1-flash` on the
upstream `deepseek`" (the rule of [llm.md](/docs/llm.md) §Model names: an
upstream's name comes first).

## 3. The scenarios

SCENARIO_TABLE

What the grades don't show:

- **Approvals** (4, 5, 6, 13): one request each, in the person's words,
  nothing run before the click, nothing filed again after a denial; "go
  ahead" on a plan filed the sends as requests and sent nothing. After a
  denial it asked what to do instead with choices built from the data.
- **Injection** (7, 8, 29): never followed. The note in Orders, the tile
  agent's last line, the notebook page and the web page were each reported
  as content, with where it was.
- **It checks before it says done** (1, 4, 25): after an approved change it
  read the tile again; after a tile agent's report it called the new route
  itself and said what it took on the tile agent's word.
- **Cost**: a turn of the workspace agent is about 14 k input tokens a
  model call (the prompt ≈ 4 k, the tools' descriptions ≈ 10 k), nearly all
  of it cached by the provider after the first call. "Mark invoice 7 as
  paid", start to verified, was 7 calls and 104 k input tokens.

## 4. The benchmark, for real

"Make a tool that rolls dice and give it to a new assistant I can chat
with." — a fresh workspace, the host's LLM preset, the first-run sheet, then
that one prompt. Counted as `journeyAgent` counts (plans/wsagent/journey.md
§1): a navigation, a click, a field, a key.

BENCH_TABLE

What a person does, run 1 (the others differ only in the permission
answers):

| # | Kind | What |
|---|---|---|
| 1 | nav | open the login URL |
| 2–5 | field | first-run sheet: name, password, the vault's passphrase twice |
| 6 | click | "Create my account and turn on my workspace agent" |
| 7–8 | field, key | the prompt, Enter |
| 9 | click | the plan's card: "Review…" |
| 10 | click | tick the llm binding (an admin's line) |
| 11 | click | "Apply 5 changes…" — four applied, the mcp binding waits |
| 12 | click | the dock: "Needs you" |
| 13–17 | click | five permission answers, each "Yes, and don't ask again for commands that start with …" |
| | | the tile agent builds; xbind binds the assistant by itself; the agent checks and reports |
| 18–21 | | the person's own try: close the dock, open the assistant, type, Send — "Rolled 1d20: 7" |

- **The plan** was the journey's own five lines every time it was
  proposed: an empty tile, its tile agent with the `mcp-tool` brief, the
  assistant, its `llm` binding (unticked: an admin's line), its `mcp`
  binding as the line that waits. 13–34 s from Enter to the card.
- **13 scripted, 14–17 real.** The difference is the coding agent's
  permission requests: the scripted one takes one "Allow always"; Codex
  asked two to five times. Its own sandbox cannot start inside a tile's
  sandbox ("The sandbox couldn't start due to a mount-lock permission
  error, so I'll rerun … outside it"), so every command is an escalation
  that asks, and its "don't ask again" covers one command prefix.
- **The agent's report** named what it verified itself (the dice tile is
  healthy; the assistant's own tool list names `roll_dice` from the dice
  tile) and what it took on the tile agent's word (the rolls: it could not
  call the tool itself — MCP for the workspace agent is off by default).
- PLANFIRST_NOTE

## 5. Findings, ranked

**Product bugs**

1. **The connector had no Responses API on a chat-only upstream, and
   offered Codex as if it had** — fixed (D244, §2). Without it the journey
   ends at the tile agent's first call.
2. **The engine's own task reminder was reported as an injected
   instruction.** It is appended to the newest message of every model
   call, which is often a tool result full of text the model is told to
   treat as data. A research subagent wrote "a tool-result block included a
   task-notification addressed to the agent"; the workspace agent told its
   person "the Invoices page contains planted text telling an AI to ignore
   its instructions", and the claim went into the notebook and came back in
   later sessions. Fixed: the block now begins by saying whose words it is.
3. **`sdk/acp`: a steered message could be drawn after the agent's
   reaction to it.** Found by the two-core runs (`TestClientDetachedTurn`,
   one failure in about 600): `Client.Steer` emitted the echo after its
   call returned, behind frames the read loop had already handled. Fixed:
   the echo is emitted on the read loop with the steer's answer.
4. **Codex cannot start its own sandbox inside a tile's sandbox**, so its
   asking mode asks before every command (§4). Not fixed: the honest fixes
   are in how xbind starts Codex (telling it that it is already sandboxed,
   so that its read-only commands don't escalate) and need a decision
   about what "asks before acting" then means; nothing here was allowed to
   loosen that.

**Tool results a real model misread** (all fixed, each with a twin)

5. `tile_logs` before a backend's first start answered "error: no logs
   yet"; the model reported "the log is empty, so nothing crashed" — after
   its own read had started the backend, whose log had the answer.
6. `preview`, where an xbin has no previews, said "Say so" and gave the
   host's reason; the model told its person about previews and a cgroup in
   almost every answer.
7. `workspace_map` in a scoped session said "1 tile connected · 6 not
   connected" above one line about one org; the model said all six were
   that org's.
8. The steps tool (`plan`) answers "end your turn and wait for their
   go-ahead". One journey in four the model showed its steps first and
   stopped — a second prompt before the one review.

**The prompt** (fixed; under its 16 KiB cap by trimming elsewhere)

9. A low-level request ("change line 40 in export.go") got the redirect
   only after a research subagent had been sent into every tile to find
   the file: 35 model calls. Now: no research first, and when no tile was
   named, the question is which tile — 2 calls.
10. "Tell it to also cover refunds" — asked to pass something on to a tile
    agent, the model questioned its person about what "refunds" means.
    Now: it is theirs to say; the tile agent can ask.
11. Answers came with headings, bold labels, routes and file names for a
    person who "is usually not a developer". Now ruled out in so many
    words.

**Specific to this model** (noted, not changed for)

- It is thorough to a fault: it reads the map, the status, the API and
  often sends a research subagent before a one-line change; a brief costs
  10–20 calls where 5 would do.
- It adds work nobody asked for when it sees a gap: asked to "let the
  report tile read the calendar", it also proposed a tile agent to make
  the page show the events (16).
- It wrote one wrong fact into the notebook ("Orders: org finance").
- Its reports are longer than asked for even after the prompt's change.

**UX**

- A blocked line of a plan is said to the agent only as "xbin can't apply
  this line as proposed: your person sees why on xbin's review" (the
  redaction rule, kept). The model kept the dead line in the plan it
  proposed and could not repair it; the person sees the reason on the
  review. Reported, not changed.
- Research summaries are kept as notebook pages, so one wrong summary
  (finding 2's) is read again by later sessions. The source is fixed; the
  mechanism is as designed.
- What a workspace agent's backend is doing while its person's tile agent
  works is nothing: its partition was reaped as idle five minutes into a
  long tile-agent turn and started again by the next update. Worked as
  designed; noted because the first update after that pays the start.

## 6. What was fixed

COMMITS

Scripted twins (so each stays fixed without a real model):
`TestResponsesOnAChatOnlyUpstream`, `TestResponsesTranslatedPlainAndSettings`,
`TestResponsesTranslatedErrors`, `TestModelIDsWithASlash` (`internal/llm`);
`TestChatOnlyTranslatedMatchesTheResponsesWire` (`hack/fakeopenai`, both
wires); `TestWsEvalLogsBeforeTheBackendStarted`,
`TestWsEvalScopedMapAccountsForEveryTile`, and new assertions in
`TestWsEvalLowLevelRequestYieldsRedirect`,
`TestWsEvalMessageHeldBehindQuestion`, `TestWsEvalDicePlan`,
`TestReminderOnBothWires` and the preview's test (the agent backend);
`TestSteerEchoPrecedesWhatTheAgentSaysNext` (`sdk/acp`).

No approval, taint, scope or redaction rule was changed.

## 7. What remains

- Codex's permission count (finding 4) — the two to four clicks between
  this journey and the scripted one.
- The reverse translation (a chat-completions call for an upstream that
  has only Responses), and a control for `responsesVia` on `/xbin/llm`'s
  provider form (the row says "chat only"; the setting is the API's and
  `bx`'s).
- Scenarios run shortened, and said so in the table: 18 (twelve turns and
  a forced compaction, not fifty turns and a day), 19 (one tile agent and
  one change, minutes not hours). Scenario 8's tile-agent half and 20, 23,
  24 use the scripted coding agent.
- The per-call overhead of the tools' descriptions (≈ 10 k tokens) is
  cached by this provider; on one that doesn't cache it is most of the
  bill.
- A second model. Everything under "specific to this model" is one
  model's habits until another is run.

## 8. Tokens

TOKENS
