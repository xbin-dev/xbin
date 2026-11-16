# Workspace agent — the first-run journey, measured (the owner's UX benchmark)

> Status: live — a measurement and a plan (2026-11-16, branch
> `wsagent/journey`, from `wsagent/main`). Built since: G3, G5, G7 — the
> first-run sheet (branch `wsagent/first-run`, D236; §8). Read with
> [ux.md](ux.md) and [../workspace-agent.md](../workspace-agent.md).

The owner's benchmark: *"starting with an installed workspace that has a
pre-provided llm, count the steps (every click) from opening the UI for the
first time, setting up vault etc to creating an MCP tile (sth simple like
'roll dice') and having it hooked up into an AgTT instance. Currently the
number of steps is ~30-50 … make sure nearly all that is collapsed to a
~single Workspace Agent prompt."*

**The numbers today** (two UI-harness passes, `journeyManual` and
`journeyAgent`, on an xbind booted as an install boots one):

| Path | Interactions | + by hand | Prompts | Approvals | Dead ends | Things to know |
|---|---|---|---|---|---|---|
| by hand, the shortest path | **27** (1 navigation, 18 clicks, 5 fields, 3 keys) | 3 | — | — | 0 | 3 |
| through the workspace agent | **49** (1 navigation, 30 clicks, 15 fields, 3 keys) | 4 | 3 | 2 sheets and a review (8 clicks), plus the tile agent's permissions | 3 | 3 |
| the target (§4) | **12** (10 where the host unseals the vault) | 0 | 1 | 1 review (4 clicks) | 0 | 0 |

**With the first-run sheet** (G3, G5, G7 — built on `wsagent/first-run`,
D236; the same passes, measured 2026-11-16; §8):

| Path | Interactions | + by hand | Prompts | Dead ends | Things to know |
|---|---|---|---|---|---|
| by hand | **28** (1 navigation, 17 clicks, 7 fields, 3 keys) | 3 | — | 0 | 3 |
| through the workspace agent | **37** (1 navigation, 22 clicks, 11 fields, 3 keys) | 4 | 3 | 3 | 2 |

The agent path costs almost twice the manual one today. It needs an account
the manual path never makes (10 interactions), it cannot create a tile for
an admin at all, and each of its approvals is a card, a sheet and a button
of its own.

## 1. The journey, and how it is counted

**Start.** A workspace `xbind init` just made; xbind run as the installer
runs it (`--isolate`, no `--dev`, owner-token login, no
`XBIN_VAULT_PASSPHRASE` — the installer's default is manual unseal, so the
vault boots unconfigured); the LLM connector configured by the host's file
(`XBIN_LLM_CONFIG`: one upstream, a default model). The owner opens the
login URL for the first time.

**End.** An instance of the agent template exists whose chat agent *called*
`roll_dice`, served by a new MCP tile bound to its `mcp` slot, and the
answer shows the roll. Both passes assert it (`roll_dice · apps/dice` in the
transcript, `rolled 1d20: N` in the answer).

**What counts.** One interaction is a navigation, a click, a field (one
filled, or one choice picked), or a key (Enter, Escape). All go through one
counter (`hack/ui-harness/passes/journey.js`), which also keeps a screenshot
of each. Beside the count:

- **wait** — time the person sits through, measured. The model and the
  coding agent are scripted, so these are floors: a real coding agent takes
  one to three minutes for this tile, and a real model a few seconds a turn.
- **know** — something the person must know that no screen says. Worse than
  a click: it stops people who don't know it.
- **by hand** — interactions a real coding agent adds and the scripted one
  doesn't (its permission requests), and the password step a sign-in older
  than ten minutes adds. Counted from the code, not driven.
- **dead end** — the agent stops and the person has to do something else
  somewhere else.

**What is scripted.** The model is `hack/fakeopenai`; its plan is written
out in the pass (`ws:` lines — with a new `wait` step, so one prompt plays a
plan with approvals in between). The coding agent is `hack/fakeacp`, planted
in the tile; where a real one would write the MCP server it copies a
prepared source into place (`hack/ui-harness/fixtures/journey-dice`: a node
backend, 60 lines, `provides.mcp`). Codex and OpenCode run on the connector
with no sign-in (docs/llm.md), so the stand-in adds no sign-in clicks that a
real run would have; Claude Code and Gemini CLI would add their own sign-in
(not counted: the path with a host LLM doesn't need them).

**Run it.** `make sealed`, then `HARNESS_ISOLATE=1 hack/ui-harness/run.sh
--shots journeyManual journeyAgent` (each boots and stops its own xbind on
`$PORT+3`; 35 s and 52 s here). Tables: `out/journey-manual.md`,
`out/journey-agent.md`.

## 2. By hand today: 27 interactions

The shortest path, as the owner token's session — no account is made.

| # | Stretch | Kind | What |
|---|---|---|---|
|  | first run | **know** | the login URL: it is in the installer's output and the server log (journalctl -u xbin \| grep login), nowhere in a browser |
| 1 | first run | nav | open the one-time login URL |
| 2 | first run | click | admin tile: the "VAULT UNCONFIGURED" banner |
| 3 | first run | field | vault: a new passphrase |
| 4 | first run | field | vault: the passphrase again |
| 5 | first run | click | vault: "set passphrase & unseal" |
| 6 | the MCP tile | field | Tile Manager: the new tile's name (Dice) |
| 7 | the MCP tile | click | Tile Manager: "Create" |
| 8 | the MCP tile | click | sidebar: apps/dice (opens the tile) |
| 9 | the MCP tile | click | the tile's title bar: the terminal button (opens its window) |
| 10 | the MCP tile | click | the window's launcher: a coding agent — _the scripted agent stands in for Codex or OpenCode, which the launcher offers on the workspace connector with no sign-in; Claude Code and Gemini CLI need the person's own sign-in (docs/llm.md)_ |
|  | the MCP tile | **know** | what an MCP tile is here, to say it in the prompt: a backend (xbin.json "runtime"), streamable HTTP at /mcp, and "provides": {"mcp": {"kind": "http", "service": "mcp"}} — no doc page, starter or template says it (the agent template's xbin.json comments do) |
| 11 | the MCP tile | field | the coding agent: the prompt (make this tile an MCP server with a roll_dice tool) — _the scripted agent copies a prepared source into place (fixtures/journey-dice); a real one writes the four files_ |
| 12 | the MCP tile | key | the coding agent: Enter |
|  | the MCP tile | wait 8.0 s | the coding agent's turn ends — _scripted: 8 s. A real coding agent on this task: one to three minutes_ |
|  | the MCP tile | by hand +3 | a real coding agent in its asking mode asks before it writes and before it runs: about three permission clicks for four files and a check (one "Allow always" for edits, one or two for commands) |
| 13 | the MCP tile | click | the tile's window: close it (it covers the next tile) — _driven through testApi()_ |
| 14 | the agent instance | click | Tile Manager: the "new from template" tab |
| 15 | the agent instance | click | the AI Agent template: "create" |
|  | the agent instance | wait 1.3 s | the instance is created |
| 16 | the agent instance | click | sidebar: apps/agent (opens the tile) |
|  | the agent instance | **know** | which of the five slots matter: `llm` must be bound to "LLM by xbin" and `mcp` to the new tile before the chat can do anything; `net`, `sandboxes` and `scm` can wait. The popover lists five rows and a grant request without saying so |
| 17 | the agent instance | click | the llm slot: "pick providers…" |
| 18 | the agent instance | click | the llm slot: tick LLM by xbin |
| 19 | the agent instance | key | the llm slot: Escape (closes the list) |
| 20 | the agent instance | click | the llm slot: "bind" |
| 21 | the agent instance | click | the mcp slot: "pick providers…" |
| 22 | the agent instance | click | the mcp slot: tick Dice |
| 23 | the agent instance | key | the mcp slot: Escape (closes the list) |
| 24 | the agent instance | click | the mcp slot: "bind" |
| 25 | the agent instance | click | the popover: fold it (it covers the chat) |
| 26 | the chat | field | the assistant: the message ("Roll a d20 for me.") |
| 27 | the chat | click | the assistant: "Send" |
|  | the chat | wait 10.8 s | the answer shows the roll |

The three worst stretches:

1. **Binding the instance (9: steps 17–25).** Two slots, four interactions
   each — open the list, tick, Escape, bind — then fold the popover, which
   sits over the chat with four more rows the person doesn't need. Nothing
   says that `llm` and `mcp` are the two that matter.
2. **The coding agent (6 + 3 by hand, and the longest wait).** Open the
   tile, its window, pick a coding agent, write the prompt, send, close the
   window; a real agent then asks three or so permissions. The prompt has to
   say what an MCP tile is here — no doc page, starter or template does.
3. **The vault (4), before anything works.** A banner inside the admin tile,
   in the bottom-right corner of the first screen; nothing else leads there.

The owner's "~30–50" is this path plus what a host LLM takes away: connecting
a provider on `/xbin/llm` (about 8), a coding agent's own sign-in (about
6–10), and the account the banner asks for (10, measured in §3).

## 3. Through the workspace agent today: 49 interactions, 3 prompts, 3 dead ends

The same start; the first admin. The prompt: *"Make a tool that rolls dice
and give it to a new assistant I can chat with."*

| # | Stretch | Kind | What |
|---|---|---|---|
|  | first run | **know** | the login URL: it is in the installer's output and the server log (journalctl -u xbin \| grep login), nowhere in a browser |
| 1 | first run | nav | open the one-time login URL |
| 2 | first run | click | admin tile: the "VAULT UNCONFIGURED" banner |
| 3 | first run | field | vault: a new passphrase |
| 4 | first run | field | vault: the passphrase again |
| 5 | first run | click | vault: "set passphrase & unseal" |
|  | an account (the agent is a person's) | **know** | the workspace agent needs an account: the first login is the owner token, which has none — no chip, no notice. The banner asks for an account for another reason (security) |
| 6 | an account (the agent is a person's) | click | admin tile: the "user management" tab |
| 7 | an account (the agent is a person's) | field | new user: the username |
| 8 | an account (the agent is a person's) | field | new user: the display name |
| 9 | an account (the agent is a person's) | field | new user: the role (admin) |
| 10 | an account (the agent is a person's) | field | new user: a password |
| 11 | an account (the agent is a person's) | click | new user: "create" |
| 12 | an account (the agent is a person's) | click | top bar: "sign out" |
| 13 | an account (the agent is a person's) | field | sign in: the username |
| 14 | an account (the agent is a person's) | field | sign in: the password |
| 15 | an account (the agent is a person's) | click | sign in: "Sign in" |
| 16 | turning the agent on | click | top bar: "set up workspace agent" |
| 17 | turning the agent on | click | setup: "Turn on" |
| 18 | turning the agent on | click | setup: "Just me for now" |
|  | turning the agent on | wait 1.3 s | the agent's page is up (its backend starts in its sandbox) |
| 19 | the one prompt | field | the agent: the prompt ("Make a tool that rolls dice and give it to a new assistant I can chat with.") |
| 20 | the one prompt | key | the agent: Enter |
| 21 | the one prompt | click | create the dice tile: "Review…" on the request's card |
| 22 | the one prompt | click | create the dice tile: the sheet's Allow |
|  | the one prompt | **dead end** | tile_create fails for an admin's agent: the approved POST /templates/new is refused ("creating workspace-owned tiles is a workspace-admin action") — a delegate call is never an admin's, and an admin's new tile defaults to the workspace's. The sheet had said "Owner: you". The person makes both tiles by hand |
| 23 | by hand: the two tiles | field | Tile Manager: the new tile's name (Dice) |
| 24 | by hand: the two tiles | click | Tile Manager: "Create" |
| 25 | by hand: the two tiles | click | Tile Manager: the "new from template" tab |
| 26 | by hand: the two tiles | field | the AI Agent template: where to create it (apps/assistant) |
| 27 | by hand: the two tiles | click | the AI Agent template: "create" |
|  | by hand: the two tiles | wait 1.3 s | the instance is created |
| 28 | the tile agent | field | the agent: "I made apps/dice and apps/assistant by hand. Go on." |
| 29 | the tile agent | key | the agent: Enter |
|  | the tile agent | **know** | which coding agent to pick on the brief: the default (Claude Code) needs the person's own sign-in; Codex and OpenCode run on the workspace connector. The card lists five names and says neither |
| 30 | the tile agent | field | the brief's card: the coding agent — _the scripted agent stands in for Codex or OpenCode_ |
| 31 | the tile agent | click | start the tile agent: "Review…" on the request's card |
| 32 | the tile agent | click | start the tile agent: the sheet's Allow |
|  | the tile agent | wait 0.8 s | the tile agent asks its first permission |
| 33 | the tile agent | click | the tile agent's card: "Answer" (opens the tile's window on its session) |
| 34 | the tile agent | click | the tile agent's permission: "Allow once" |
|  | the tile agent | by hand +2 | a real coding agent asks more than once (edits, then commands): about two more permission clicks, each in the tile's window |
|  | the tile agent | wait 8.5 s | the tile agent builds; the agent reads its result and proposes the bindings — _scripted: under a minute. A real coding agent: one to three minutes_ |
|  | the tile agent | **dead end** | admin_propose is refused until the person turns on "Let my agent propose admin changes": the agent can only say so, and the switch is two menus away in the dock |
| 35 | the admin opt-in | click | the dock's menu |
| 36 | the admin opt-in | click | the menu: "Preferences" |
| 37 | the admin opt-in | click | Preferences: "Let my agent propose admin changes" |
| 38 | the admin opt-in | click | Preferences: back to the conversation |
| 39 | the bindings | field | the agent: "I turned that on. Try again." |
| 40 | the bindings | key | the agent: Enter |
| 41 | the bindings | click | the change set's card: "Review…" |
| 42 | the bindings | click | the review: tick the llm binding (it starts unticked: it spends the workspace's quota) |
| 43 | the bindings | click | the review: "Apply 2 changes…" |
|  | the bindings | by hand +2 | applying asks for the password when the sign-in is older than ten minutes — it is, once a real coding agent has built the tile: one field and one click |
| 44 | the bindings | click | the results: "Done" |
|  | the bindings | **dead end** | the agent can't verify the hook-up: tools/list and tools/call are POSTs (an approval each), and asking the assistant is a POST too. It reports the bindings as applied; whether the assistant can roll is the person's to try |
| 45 | the chat | click | top bar: the workspace agent chip (closes the dock: it covers the tile) |
| 46 | the chat | click | sidebar: apps/assistant (opens the tile) |
| 47 | the chat | click | the popover: fold it (it covers the chat) |
| 48 | the chat | field | the assistant: the message ("Roll a d20 for me.") |
| 49 | the chat | click | the assistant: "Send" |
|  | the chat | wait 16.0 s | the answer shows the roll |

How far today's agent gets, step by step:

| Step | Today | Cost |
|---|---|---|
| first-run setup (vault, account) | not the agent's; nothing leads to it | 5 + 10 |
| turning the agent on | the chrome's three steps; the host's LLM does the first | 3 clicks |
| creating a tile | **fails for an admin.** `tile_create` asks, the person approves, xbind refuses the approved call: an admin's new tile defaults to the workspace's (`resolveCreateOwner`), and a delegate call is never an admin's (`canCreateAt`). The dry run said yes and the sheet said "Owner: you". A person who is no admin gets a personal tile — the tool works for everyone but the person who set it up | 2 wasted clicks, then 5 by hand, and a second prompt |
| starting a tile agent that writes the MCP server | works: a brief, a request, a sheet. The default coding agent is Claude Code, which the connector can't serve and nobody is signed in to — the person has to know to pick another. The tile agent's permissions are the person's, in the tile's window | 1 field + 2 clicks, then 2 (+2 by hand) for permissions |
| instantiating the agent template | the same `tile_create`: fails the same way | by hand (3) |
| binding the `mcp` (and `llm`) slot | `admin_propose` with `binding.set`: works, once the person finds "Let my agent propose admin changes" in the dock's menu → Preferences. The `llm` line starts unticked (SPEND warning) | 4 for the opt-in, a third prompt, 4 in the review (+2 by hand: the password, when the sign-in is over ten minutes old) |
| checking the hook-up | **can't without another approval.** It reads the dice tile's `GET /roll`; MCP's `tools/list` and `tools/call` are POSTs, which the gate takes for changes, and asking the assistant is a POST too | the person tries it: 5 |

The three worst stretches:

1. **An account, to have an agent at all (10: steps 6–15).** The owner
   token's session has no agent — `GET /delegate/state` is 403, no chip, no
   notice. Make a user (5 fields and clicks), sign out, sign in.
2. **The approvals and the detours around them (25: steps 19–44 less the
   tile agent's own).** Three prompts where the benchmark wants one; four
   sheets; a failed creation; two tiles made by hand; the opt-in.
3. **The tile agent (5 + 2 by hand, and the wait).** Pick the coding agent,
   Review, Allow, then leave the dock for the tile's window to answer its
   permissions.

Also seen, not counted:

- xbin's review of a change set draws a binding's *before* and *after* as
  `[object Object]` (`web/bx-agent-changes.js`): the one place a person
  reads what a binding replaces.
- After the agent's bindings, the assistant's popover still holds four
  undecided rows (a grant, `net`, `sandboxes`, `scm`) over the top of the
  chat.
- While exploring, a tile agent's turn that started and ended within
  seconds of the workspace agent's message was never reported to the
  conversation, which then waited for good. Read in `ws_notify.go`
  (`wsSeen` asks for a wake only when the state differs from the one last
  told; a turn that begins and ends between two reads leaves it as it was),
  not reproduced on purpose; the pass's scripted turn takes 8 s for that
  reason. Worth a test of its own.

## 4. The target

Open the login URL → one first-run sheet → the dock opens → one prompt →
one batched approval → the agent reports what it built and what it checked.

| # | Kind | What |
|---|---|---|
| 1 | nav | open the login URL |
| 2–3 | field | first-run sheet: your name, a password (an account, signed in at once) |
| 4–5 | field | first-run sheet: the vault's passphrase, twice — absent when the host unseals (`XBIN_VAULT_PASSPHRASE`) |
| 6 | click | first-run sheet: "Create my account and turn on my workspace agent" |
| 7–8 | field, key | the prompt, Enter |
| 9 | click | the plan's card: "Review…" |
| 10 | click | the review: tick the line with a warning (the model spends the workspace's quota) |
| 11 | click | the review: "Apply 5 changes…" |
| 12 | click | the results: "Done" |

Twelve interactions self-hosted, ten where the host unseals the vault; no
dead end; nothing to know but the login URL. The person's own first roll in
the assistant is three more (open it, type, send) and no longer part of
"done": the agent checked.

## 5. The gaps, ranked by interactions saved

Saved against §3's 49 (+4). "After" is the count once this gap and the ones
above it are closed (in brackets: where the host unseals the vault).

| # | Gap | Change | Where | Keeps | Effort | Saves | After |
|---|---|---|---|---|---|---|---|
| G1 | an admin's agent can't create a tile (a bug, and the precondition of the rest) | under delegation an empty owner is the person's (`user:<id>`), for admins too — what the sheet already says; the dry run runs the real check; a test for an admin's `tile_create` | xbind (`internal/broker/create.go`, the delegate dry run) | the tile is the person's, never the workspace's or an org's unless they say so; the clamp on delegate calls stays | S (½ day) | 5 (5 by hand and a prompt go; the 2 wasted clicks become two real approvals, 4) | 44 |
| G2 | one approval per step, and the opt-in | **the plan is one change set** (§6c): `tile.create` and `agent.start` become change-set kinds beside `binding.set`; a set whose every line the person could do themselves needs no admin opt-in, and the opt-in is offered on the review | xbind (change sets), agent backend (a `plan_propose`, the prompt), chrome (the review) | only xbin's chrome grants; xbind writes every line; warned lines start unticked; a recent sign-in for admin lines; lines that need an admin say so, with "Ask an admin" | L (5–7 days) | 12 (two creations 4, the start 2, the opt-in 4, a prompt 2, the review 4 → one review 4) | 32 |
| G3 | an account, by hand | first run offers "Create your account": name and password on one sheet; xbind makes the admin account and signs this browser in as it (the token session ends there) | first-run flow (shell, `POST /users`, a route that swaps the session) | the token stays the bootstrap credential until the person disables it; the account is an ordinary admin; a workspace that has accounts never sees the sheet | M (2 days) | 7 (10 → 3) | 25 |
| G4 | the hook-up is checked by the person | the agent reads what the instance would offer (a GET on the agent template: tools by class, with their MCP server) and may call MCP's read methods (`initialize`, `ping`, `tools/list`) on a tile's declared `mcp` endpoint without a request; one `tools/call` rides the plan as a line of its own | agent template (`GET /tools`), xbind (delegate gate), prompt | a POST stays a change unless it is exactly one of those three JSON-RPC methods on an endpoint the tile declared; `tools/call` is always asked | M (2 days) | 5 (the person's own try is no longer part of "done") | 20 |
| G5 | turning the agent on: three steps | the first-run sheet's button turns it on for the account it makes, and the dock opens (§6a) | first-run flow, `PUT /delegate/workspace` | an admin's own click; "Just me" by default; a host can set it or lock it off | S (1 day, with G3) | 3 | 17 |
| G6 | the tile agent's permissions, in another window | a start may carry "edits inside this tile without asking" — a mode on the plan's line, in the danger wording where the harness's mode doesn't ask; and permission requests of sessions the agent started are answerable from the dock's Needs you | xbind (start modes), chrome, the per-harness mode map | never the agent answering; the mode is the person's choice on xbin's sheet; the default stays "asks" | M (2–3 days) | 2 (+2 by hand) | 15 |
| G7 | the vault's banner | the passphrase on the first-run sheet; no step at all where the host unseals (§6b) | first-run flow | the passphrase is never stored unless the host chose that; "can't be recovered" stays on the sheet | S (½ day, with G3) | 2 (4 where the host unseals) | 13 (11) |
| G8 | the default coding agent can't run | an agent-started session defaults to a harness the connector serves when the person has no sign-in for the preferred one; the brief's card says which run with no sign-in | xbind (`start-defaults`), agent page | the person's saved choice wins; org rules as today | S (½ day) | 1, and a "know" | 12 (10) |
| G9 | nobody knows how to build an MCP tile | an `mcp-server` template, a doc page, two lines in the agent's map (§6d) | builtin template, docs, `ws_model.md` | the template is a starting point the person owns, like any | M (2 days) | no click: the three "know"s, most of the coding agent's wait — and it is what lets G2 be one batch (the binding is valid before the tile agent wrote a line) | — |
| G10 | the popover over the chat | a tile the plan made opens with its remaining slots folded ("3 optional interfaces"), not over the composer | shell | nothing is bound or dismissed for the person | S (½ day) | 1, when the person opens the assistant (inside G4's 5) | — |
| G11 | `[object Object]` in the review | draw a binding's before and after as words | chrome (`bx-agent-changes.js`) | — | S (an hour) | 0 — but it is what the person approves | — |
| G12 | a quick turn is never reported | note a turn's number, not only its state, in what was told | agent backend (`ws_notify.go`) | — | S (½ day) | 0 — a stuck conversation | — |

G1 comes first whatever it saves: without it the agent path dead-ends at
its first step for the person who set the workspace up. G1, G11 and G12 are
bugs and stand on their own. G2 + G9 are the collapse; G3 + G5 + G7 are one
first-run sheet; G4 and G6 are what make "done" true without the person
leaving the dock.

## 6. The five questions

### (a) With a host LLM, which agent-setup steps can go?

All three, for the first admin, behind one click.

- *Connect a model* is done already: xbind reads `host.active` and the step
  shows "Set by your host" (measured: `model+ on who`).
- *Turn on* and *choose who* fold into the first-run sheet's button. The
  rule: **when the connector is configured (by the host or by an admin) and
  workspace agents were never switched (`workspaceAt` absent), the first-run
  sheet's primary button reads "Create my account and turn on my workspace
  agent" and sends `PUT /delegate/workspace {enabled: true, who: "me"}` as
  the new account, in the same click that made it.** A second, quiet button
  makes the account without it. "Everyone" stays a later, deliberate choice
  (it spends the workspace's quota for people who were never asked).
- A host that wants it without a click sets it in the deployment's file, as
  it sets the LLM: a new key beside the connector's, read at boot and locked
  like the rest — `"workspaceAgent": {"enabled": true, "who": "admins" |
  "everyone"}` (and `false` to lock it off). `"admins"` is a new value of
  `who`: each admin account gets its own entry when it is made. Hosted
  instances pre-populate and lock it; an admin sees "Set by your host".
- Without a model, nothing changes: the sheet says the agent needs one and
  links `/xbin/llm`.

It must stay an admin's act or a host's: no default turns agents on in a
workspace that exists today (`workspaceAt` absent *and* no accounts is the
only state the sheet shows in).

### (b) Can vault setup be folded or deferred?

Folded, yes; deferred, no. Without a vault xbind refuses resource storage,
so the agent template's instance (sqlite) and the workspace agent's own
partition (held: `locked`) can't run — the journey needs it before the first
tile. Two cases:

- **The host unseals** (`XBIN_VAULT_PASSPHRASE`, what a hosted instance and
  the installer's option 1 do): there is no step. Zero interactions.
- **Manual unseal** (the installer's default): the passphrase and its
  repeat are two fields of the first-run sheet, under the line that it
  can't be recovered. Same two fields as today, one click fewer, and no
  hunt for the banner. The installer could also *ask* which the person
  wants with the trade-off in a sentence; today the default is the one that
  costs four interactions on every first run and one after every restart.

### (c) What should ONE batched approval look like?

**What exists.** Two mechanisms, and the plan straddles them:

| | covers | approved | batch |
|---|---|---|---|
| requests → tickets | a tile API call, a tile agent's start, a tile's creation | one sheet each; "a session start and a tile create are approved each time" | no |
| change sets | admin settings only: `grant.add/revoke`, `binding.set/remove`, `org.share`, `limits.set` | one review, a tick a line, one Apply | yes — but admins only, behind an opt-in |

So change sets do **not** cover creating tiles or starting tile agents.

**The smallest extension: two new change-set kinds, and a rule for who may
propose what.**

- `tile.create {template, path, owner?}` — applies as `POST /templates/new`
  (owner: the person, unless they named an org they may create in).
- `agent.start {tile, brief, harness?, mode?}` — applies as `POST
  /term/sessions` with the brief as its first prompt, exactly as the ticket
  does today; the line shows the coding agent, the mode and the brief "sent
  exactly like this".
- **A later line may name a tile an earlier line of the same set creates.**
  xbind checks each line as today, with the earlier `tile.create` lines
  taken as done (the template's manifest tells it the slots and provides);
  at Apply the lines run in order and a failure skips the rest — the rule
  change sets already have.
- **Who may propose.** A set is *personal* when every line is something the
  person could do with their own hands and no admin: creating where they may
  create, starting an agent in a tile they have a terminal on, binding a
  slot of a tile they manage to a provider they may bind. A personal set
  needs no opt-in (it is the three sheets they get today, on one page). A
  line that needs an admin makes the set an admin set: for an admin, the
  opt-in and the recent sign-in as today — and the opt-in is offered *on the
  review* the first time ("This plan has 2 admin changes. Let my agent
  propose those?"), not two menus away. For a person who is no admin, the
  review shows those lines as "needs an admin", with **Ask an admin**: the
  set goes to the admins' Needs you as a request from that person, and the
  rest can be applied now.

**What the person sees** — xbin's dialog, xbind's lines, the agent's words
quoted:

```
XBIN · PLAN                                             asking Ada Admin
Your agent proposes 5 changes
  "Make a dice tool and an assistant that can use it"   — the agent's title

  [x] Create the tile apps/dice from the template MCP server — yours
  [x] Create the tile apps/assistant from the template AI Agent — yours;
      each person's data kept apart
  [ ] Bind apps/assistant's llm to the workspace's LLM connector
      SPEND: its calls spend the workspace's LLM quota           (admin)
  [x] Bind apps/assistant's mcp to apps/dice                     (admin)
  [x] Start a tile agent in apps/dice — Codex, on the workspace connector,
      ASKS BEFORE ACTING · the brief, sent exactly like this: …  [show]

  4 of 5 ticked · applied in order as you            Reject all   Apply 4…
```

Review, one tick (the SPEND line — kept: it is the rule that a line with a
warning starts unticked), Apply, Done. The tile agent then works while the agent
waits; its end and the agent's check arrive as updates. Undo proposes the
inverse set, as today (a created tile's inverse is "delete it — yours to
do", not something the agent can).

Not in the batch: answering the tile agent's permissions (G6 — a mode the
person picks on the start's line, or answers from the dock), and
`tools/call` unless the plan carries it as a line ("Call roll_dice once on
apps/dice, to check").

### (d) What does the agent need to know?

To build an MCP tile right the first time:

- **An `mcp-server` builtin template**: a backend with one example tool,
  `"provides": {"mcp": {"kind": "http", "service": "mcp", "role":
  "reader"}}` declared, an `API.md`, and an `AGENTS.md` for the coding agent
  that says where tools go and how to check (`tools/list`, `tools/call`
  from its own sandbox). Because the manifest declares the provide from the
  first second, the plan's `binding.set` line is valid before the tile agent
  has written anything — which is what lets creation, binding and start be
  one batch. Today the only description of the contract is three comment
  lines in the agent template's `xbin.json`.
- **A doc page** (`docs/mcp.md`: "Give an agent a tool") that `docs_search`
  finds for "MCP", "tool", "agent tool": the template, the slot, the three
  JSON-RPC methods, the binding as the grant. Today the search answers five
  sections for "build an MCP server tile for an agent", and no page under
  `docs/` describes building one (read, not judged from the hits).
- **Two lines in the agent's map** (`ws_model.md`, generated from the docs):
  "an agent instance gets tools from MCP tiles bound to its `mcp` slot; make
  one from the `mcp-server` template" and "an agent instance needs its `llm`
  slot bound — `xbin:llm` is the workspace's connector".
- **A brief template** for this kind of work, filled by the agent: goal (the
  tool's name, arguments, result), done when (`tools/list` names it and a
  call returns the expected shape), check by (the two calls from the tile's
  own sandbox), leave alone (the manifest's `provides`).

To verify the hook-up before it says done, without a new approval:

1. the tile agent's own report, read with `tile_agent_result` (today);
2. `tools/list` on the tile's `mcp` endpoint as a read (G4): the tool is
   there, with its schema;
3. `GET /tools` on the instance (G4): "class Internal offers
   `roll_dice` from apps/dice" — the binding reached the backend;
4. one real `tools/call`, as a line of the plan the person ticked.

Then it reports "checked: 1–4" and "not checked: a conversation in the
assistant", in the words the prompt already asks for.

### (e) The same journey on the iOS app

From the docs and the app's design, not driven here (the passes are web):

| Stretch | iOS today |
|---|---|
| the login URL, the vault, the account | web only: the app joins a workspace that already has an account (pairing from `/xbin/devices`) |
| turning the agent on | web only — the app shows the three steps read-only and "Open the setup on the web" |
| the prompt, the request sheets, the change-set review, Needs you, Preferences (the opt-in) | native |
| creating a tile by hand, from a template | web only (the Tile Manager is a web tile) |
| the tile agent's permissions | native (the app's agent screen) |
| binding slots by hand | web only (the popover and the admin console) |
| chatting with the assistant | native (the agent template's native view) |

So today the journey can't be finished on a phone: the two tiles and the
bindings have no native path once the agent's own fail or are refused.
After G1 and G2 everything past first run is the agent's page and xbin's
sheets, which the app already draws — the plan's review needs the two new
kinds in the app's change-set sheet (a card a line, as it has), and the
first-run sheet (G3, G5, G7) should exist natively for a hosted workspace,
where the app may be the first thing a person opens. That is the one piece
of new native UI.

**What a native first run would need** (the web sheet is built, D236; the
app is not touched — it joins a workspace that already has an account):

- **A way in without an account.** The app signs in with a password, SSO
  or a device key, all of an account; the bootstrap token is a browser
  cookie or bx's bearer. A native first run needs the token handed to the
  app once — a pairing link or QR code the installer prints beside the
  login URL (`xbin://<host>/first-run?token=…`, never kept by the app) —
  and an xbind route that takes it as a bearer for exactly `GET
  /first-run`, `POST /users` and the swap, answering an **app** session
  (`NewBearerSession`) rather than a cookie: today's swap sets a browser
  cookie and refuses bx's bearer on purpose.
- **The same sheet, natively**: name, password (iOS's strong-password
  suggestion — `textContentType .newPassword` with `.username` beside it),
  the vault's two fields under the "cannot be recovered" line, the agent's
  sentence and the two buttons; its words the web sheet's
  (`web/first-run-words.js`), kept in step the way the app's delegation
  words are kept with `agent-chrome-words.js`; Face ID offered for the
  device key right after, since the account is new.
- **Then the device**: enrolment of this phone as the new account's first
  device in the same flow (today a signed-in browser makes a code), so the
  app keeps working after the token's session is gone.
- **The hand-over**: the app's agent sheet opened, message box focused —
  the counterpart of the dock's `focusComposer()`.
- The host's preset needs nothing native beyond the words: the app reads
  the same `locks` on `GET /delegate/state`.

## 7. What was not measured

- A real model: whether it makes this plan from the prompt, and in how many
  turns. The script is the plan the tools invite; scenario 13 of
  `testdata/ws-real-model-scenarios.md` is the nearest manual check.
- A real coding agent: its permission count (taken as three) and its time.
- The path without a host LLM (a provider on `/xbin/llm`, a coding agent's
  sign-in): estimated in §2, not driven.
- iOS: read, not driven.
- The password step of a change set (a sign-in older than ten minutes): the
  scripted run is too fast to meet it; counted by hand.
- A person who is no admin: `tile_create` should work for them (the refusal
  is the admin's default owner) — not driven.

## 8. After the first-run sheet (G3, G5, G7 — D236)

Built on `wsagent/first-run`; the passes above, run again on it
(`journeyManual`, `journeyAgent`), and `firstRun`, which asserts the sheet's
own states.

**The agent path: 49 → 37.** Steps 2–18 of §3 (the vault's banner, an
account in the admin console, sign out and in, the three setup steps — 17
interactions and one "know") are now five: the login URL lands on one sheet
— your name (the sign-in name follows from it), a password, the vault's
passphrase twice, **Create my account and turn on my workspace agent** — and
the shell comes back as that account with the agent's dock open and its
message box focused. The rest of §3 is unchanged (G1, G2, G4, G6 are other
packages'): 3 prompts, 3 dead ends, 2 things to know.

**By hand: 27 → 28.** The by-hand path's first stretch is the sheet's quiet
button now: name, password, the passphrase twice, "Create my account without
it" — five where the banner's vault was four — and the person ends on an
account of their own, not the reusable token. Skipping the sheet ("Not now")
and finding the banner costs the same five.

**Where the host unseals the vault** the sheet has no vault fields: three
interactions to an agent with its message box focused (name, password, the
button), so §4's target becomes reachable once G1 + G2 land: 12, or 10.

**What the sheet keeps** (`firstRun` asserts each): only the owner token's
browser session in a workspace with no accounts sees it (`GET /first-run`);
an existing workspace, `--dev` and `--no-auth` never do; "Not now" is
per-browser; the account is an ordinary admin from `POST /users`; the swap
(`POST /first-run/session`) opens no session for an account that existed
before or was used, only from the token's browser session, same origin,
throttled; the token stays valid until token sign-in is turned off (the
sheet says where); "Everyone" is never chosen; a vault or agent step that
fails is said as what is left, with the person signed in; the host's
`workspaceAgent` preset turns the agent on (or off) with no click and locks
the controls.

**Not done here:** a shell from before the hook keeps today's path (xbind
rewrites no HTML); the iOS app's first run (§6e says what it would need); a
password manager's save prompt is offered the right fields
(`autocomplete` name / username / new-password, the vault's fields in a
form of their own) but a headless browser can't show that it saves.
