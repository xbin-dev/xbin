# Workspace agent — one approval, one place (D250)

> Status: live

The owner's test session of 2026-11-21 (the agent's retro and his
screenshot are kept outside the repo, `~/buxon-keep/2026-11-21/`): "turn
eui into a tic tac toe game" took five approval rounds and started
nothing. Two cards with a Review… each; changing the coding agent on the
card and approving came back to the agent as "approved, but the approval
no longer covered the call"; the agent believed "your person edited it"
and blamed the brief; Deny was the filled button and Approve the red
one; the follow-ups opened a new session each time; and there was no way
to pick a model. This document finds each cause in the code, says the
rule that replaces it, and lists the safety rules with the test that
holds each.

Reads with: `docs/workspace-agent.md`, `docs/protocol/delegation.md`
(requests and tickets, start-defaults, plans), D230 (sign-ins on a start),
D238 (plans, the `agent.start` line), D243 (the dock's permission cards),
D245 (homes on untrusted tiles).

**No "·" in anything a person reads** (the owner, during this batch):
where a surface this batch touches joined facts with "·", each fact is
now an element of its own — a chip, a labelled field, a row, a muted item
laid out with a gap. The words tables return the facts as a list
(`parts`), and keep their joined accessors for an older shell or app.

## 1. One approval, one place

**Cause.** `builtin-templates/agent/ws/place.js:166-177` places a card for
every request of the session, a brief's start among them, after the
brief's own card; `ws/cards.js:115` draws the brief's primary "Review…"
(`ws/words.js:251`: the state `asked` → `W.review`) and `ws/cards.js:75`
the request card's own. The app's view (`native/ws-chat.js:95-107,
121-152`) places the same two through the same `compose`. The dock's list
(`web/bx-agent-panels.js:45-62`, `web/agent-chrome-words.js:577 needs`)
had one row per request already; it was the conversation that showed the
start twice. Technical lines (`POST /api/xbin/term/sessions`) were on the
card (`ws/cards.js:68`).

**Rule.**

- A pending act is ONE item wherever it appears. A start that has a brief
  is the brief card alone: `compose` draws no request card for a request
  whose `brief` it already drew, in the page and in the app.
- In the conversation every pending item carries one compact marker,
  "Waiting for your approval", with an arrow, that opens the one item in
  xbin's chrome (the sheet) — a link-styled control, never a filled
  button, never one that approves. A request card that waits shows what,
  for which tile, and when it was asked and expires as two muted items;
  the call itself is the sheet's (its Details).
- Everything that waits for the person is ONE batch in the dock and on
  `/xbin/agent`: the header button **Waiting for you** with a count badge
  opens the list; each item once, oldest first (the order asked), one
  primary action each (Review… a request or a plan, Try again a start
  that failed, Open a session that asks a question; a permission card
  keeps the tile window's own buttons, D243). **Review all** (N > 1) walks
  the requests and plans in that order: each sheet says "1 of 3", and a
  decision opens the next. A plan stays one review.
- The app's list was one row per request already; its conversation
  follows the page's `compose` and draws the brief alone.

### The one card — desktop (the conversation, 1280)

```
+------------------------------------------------------------------------+
| [doc] Brief for Eui's agent              ( Waiting for your approval ) |
|       Written by your agent                                    [Edit]  |
|  GOAL        Turn the eui page into a playable tic tac toe game ...    |
|  DONE WHEN   Opening apps/eui shows a 3x3 board ...                    |
|  LEAVE ALONE Don't add a backend ...                                   |
|  CHECK BY    Run the tile as a page and play it through ...            |
|  Runs on   ( OpenCode ) ( asks before acting ) ( no sign-in needed )  |
|            you choose on the approval                                  |
|  [shield] Waiting for your approval  ↑ open it                         |
|  This is what the tile agent is told first. ...                        |
+------------------------------------------------------------------------+
```

### The one card — phone (430)

```
+--------------------------------------+
| [doc] Brief for Eui's agent   [Edit] |
| ( Waiting for your approval )        |
| Written by your agent                |
| GOAL                                 |
| Turn the eui page into ...           |
| DONE WHEN                            |
| Opening apps/eui shows ...           |
| Runs on                              |
| ( OpenCode ) ( asks before acting )  |
| ( no sign-in needed )                |
| [shield] Waiting for your approval   |
|          ↑ open it                   |
+--------------------------------------+
```

### The one batch — desktop (the dock, 400 to 720 px)

```
+-----------------------------------------------------------+
| [agent] Workspace agent  [Waiting for you (2)] [Pause] [⋯] |
+-----------------------------------------------------------+
| Waiting for you (2)                         [Review all]  |
| oldest first                                              |
|-----------------------------------------------------------|
| [shield] Start a tile agent in Eui            [Review…]   |
|          ( OpenCode ) ( asks before acting )              |
|          asked 10:47      expires in 24 h                 |
|-----------------------------------------------------------|
| [shield] Change something in Eui              [Review…]   |
|          asked 10:48      expires in 24 h                 |
+-----------------------------------------------------------+
```

### The one batch — phone (the dock is the page)

```
+--------------------------------------+
| Workspace agent  [Waiting (2)] [||]  |
|--------------------------------------|
| Waiting for you (2)   [Review all]   |
|--------------------------------------|
| [shield] Start a tile agent in Eui   |
| ( OpenCode ) ( asks before acting )  |
| asked 10:47                          |
| expires in 24 h                      |
|                         [Review…]    |
|--------------------------------------|
| [shield] Change something in Eui     |
| asked 10:48                          |
| expires in 24 h                      |
|                         [Review…]    |
+--------------------------------------+
```

### The sheet

```
+------------------------------------------------------------------+
| [shield] XBIN  APPROVAL   1 of 2                 asking Ada Admin |
| Start a tile agent in Eui?                                       |
|  Tile          Eui   apps/eui                                    |
|  Coding agent  [OpenCode              v]                         |
|                remembered for this tile                          |
|  Model         [its default           v]                         |
|                OpenCode's own list shows once a session runs     |
|  Mode          [plan, asks before acting v]                      |
|                the default: asks before it edits or runs         |
|  [x] Remember for this tile                                      |
|  Network       Its model only. No internet.                      |
|  Other tiles   It can call the tile APIs this tile is granted.   |
|  What it will be told   Written by your agent                    |
|  | This is a piece of work in this tile ...                  |   |
|  sent exactly like this                                          |
|  > Details: POST /api/xbin/term/sessions                         |
|  THE AGENT'S OWN WORDS, NOT CHECKED BY XBIN                      |
|  asked every time       change in Preferences                    |
|  [Deny]                                              [Start]     |
+------------------------------------------------------------------+
```

A warned approval (a mode that doesn't ask, picked by the person): the
warning is a banner in the body, Deny stays where it is and quiet, Start
stays where it is, coloured as the danger it is, armed after 600 ms.

## 2. What the person picks on the approval is the approval

**Cause — three, together.**

1. `internal/server/delegate.go:425-431` (`ticketStart`) refuses a ticket
   whose shown mode is empty: "the ticket says no harness and mode: ask
   again". An empty mode is exactly a coding agent whose catalog names no
   default mode, started in "its own" — OpenCode (`sdk/acp/providers.go:
   185-200`; `internal/agentprefs/agentprefs.go:407-408` gives it
   `ApproveMode || DefaultMode`, both empty). The refusal comes AFTER
   `internal/delegation/requests.go:519` (`Redeem`) spent the ticket
   (`delegate.go:831-841`), with the refusal code `ticket`, which the
   agent's backend words as `ws_notify.go:409-410`: "the approval no longer
   covered the call when it was made (it expired after 15 minutes, or the
   tile changed)". A plan's start had the exception
   (`planstart.go noDefaultMode`), which is why the agent's workaround — a
   plan naming OpenCode — started first time.
2. The coding agent was picked on the agent's own page, not on xbin's
   sheet: the card's select (`ws/cards.js:117-119`) PUTs the brief, and
   the backend files the start again and withdraws the old request
   (`ws_routes.go:491-510`). The sheet had nothing to choose.
3. Any PUT of the brief marks it edited (`ws_routes.go:486`: `edited=1`),
   a coding agent's change included; `tile_agents` then says "your person
   edited it" (`ws_tools_agents.go:538`) — the evidence the agent trusted
   over its person.

**Rule.**

- A ticket for a tile-agent start binds what the AGENT chose and may not
  change after approval: the tile (the request's path and tile), the
  brief's text (the body's hash, which holds the prompt), the kind of act.
  It does not bind the session's coding agent, model, mode or sign-in
  line: those are the PERSON's choices at approval. `POST …/approve`
  carries them (`harness`, `model`, `mode`, `remember`); xbind validates
  each exactly as a by-hand start in the Agent tab would — a coding agent
  the tile's orgs allow and the person may start there, a mode of that
  harness, a model of the list xbind offers for it there — and the D230
  sign-in and trust rules unchanged (the sign-in only by its own unticked,
  warned tick, offered for the harness picked). The request keeps them
  (`chosen`), its `preview.session` is redrawn with them, and the ticket's
  start is THAT: the open gets the person's harness, mode and model,
  whatever the agent's body named. The agent is told what ran: "tile agent
  #1 started in apps/eui: OpenCode, model …, asks before acting".
- The agent may SUGGEST: `tile_agent_start {harness, model, mode}` and a
  plan's `agent.start {tile, brief, harness?, model?, mode?}` (model is
  new there). A single start carries them as `suggest` in its body (bound
  like the rest of the body, so it can't change after approval); the sheet
  shows the suggestion preselected unless the person's preference says
  otherwise (§3), marked "suggested by your agent"; the person's pick
  always wins.
- The agent can't choose a mode that doesn't ask, or a sign-in, by
  suggesting it: a suggested mode that doesn't ask is never preselected —
  the select shows the asking default and, under it, "your agent suggested
  build, which acts without asking: pick it yourself if you want it". The
  default mode is the harness's own asking mode (its ask mode, else its
  plan mode — `acp.Provider.Asks`): a session the agent directs asks the
  person before it edits or runs, so nothing the agent tells it acts
  unseen. For OpenCode that is `plan` (it proposes and edits nothing);
  `build` (its own default) is a pick of the person's, warned.
- A sign-in is never suggested (the body's `signin` stays 403 for the
  agent).
- Changing the coding agent, model or mode is never a brief edit: the
  "edited" mark is set only when the brief's text changed
  (`ws_routes.go`), and the brief's card has no coding-agent select any
  more (it says what the start runs on, and that the person chooses on the
  approval).
- A person editing the brief's text on the card before approving: one
  round. The card's Save files the start again with the person's text
  (the superseding request is filed by the agent's backend on the person's
  edit — the page can't act as the person at xbind, so xbind can't re-bind
  the ticket itself; the person then approves exactly that text on xbin's
  sheet, which says it is the text sent); the agent is told "your person
  edited the brief before approving; what was sent:" with the new text.
  No model round trip.

## 3. Preferences for tile coding agents

**Cause.** The sheet named the model as text and never as a control
(`web/agent-chrome-words.js:278`); the start never named one
(`ws_xbin_http.go:479-498 startCall`); the person's agent preferences
held the mode per coding agent and the network
(`internal/agentprefs/agentprefs.go:61-72`), no coding agent and no
choice per tile.

**Rule.**

- (a) **On the sheet:** Coding agent, Model and Mode selects. Their
  options are xbind's, in the request's `preview.session.choices`: the
  coding agents the tile's orgs allow and the person may start there
  (start-defaults' list); per coding agent its modes (the catalog's, with
  whether each asks; a harness with no default mode also has "its own
  default", which doesn't ask) and its models. Models are what xbind knows
  before a session exists — the connector's models where the connector
  serves that coding agent on that tile, the person's saved model for it
  (`harnesses.<id>.model`, the Agent tab's), the one remembered for the
  tile, and a model the agent suggested where nothing narrower is known —
  plus "its default", and the sheet says that the coding agent's own list
  shows once a session runs.
- (b) **The person's preference**, in the dock's Preferences under
  "Sessions my workspace agent starts": the coding agent
  (`agentSessions.harness`, new), per coding agent the mode
  (`agentSessions.modes`, as before: modes that ask only) and the model —
  `harnesses.<id>.model`, the same store the Agent tab's launcher keeps,
  not a parallel one (said beside the select: "also what your own sessions
  of it start on"). **Remember for this tile** on the sheet stores the
  approved coding agent, model and — when it asks — mode for that person
  and tile (`agentSessions.tiles.<tile>`, new; at most 200 tiles, the
  oldest dropped). It is unticked until the person changes a select (then
  ticked for them, and they can untick it). A mode that doesn't ask is
  never remembered: it is picked each time, as a bypass mode is in the
  Agent tab.
- **Precedence on the sheet:** this tile's remembered choice > the
  person's preference > the agent's suggestion > the workspace default,
  per field; under each select a few words say where it came from
  ("remembered for this tile", "your preference", "suggested by your
  agent", "the default"). Only an allowed coding agent and an existing
  mode count at each step.
- (c) **No admin default or lock.** The workspace agent's layering (the
  host's file, then an admin's setting, then the person's) covers the web,
  MCP and the context budget only; the orgs' coding-agent lists constrain
  but don't default. An admin default for start choices would be a new
  layer: left out, said here.
- **The agent reads its person's preference for a tile** —
  `GET /delegate/start-defaults?tile=` gains `chosen: {harness, model,
  mode, from: {harness, model, mode}}` — and never sets it (`/agent/prefs`
  is the person's own session's alone, as ever).
- **Starting without asking** (the person's standing preference) takes
  nothing from a suggestion: the chain without the agent's step, and only
  a mode that asks, never a sign-in. A start whose suggestion differs from
  what the chain resolves needs the person's approval, as a start that
  names a mode always did.

## 4. Refusals that say what differed

**Cause.** `ws_notify.go:409-411` worded every `ErrBadTicket` the same;
xbind's own were `internal/delegation/requests.go:219-220` — "the request
doesn't exist, isn't approved, expired or was already used" and "the
ticket was approved for another call" — and `delegate.go:430`.

**Rule.** Every refusal after an approval names, in xbind's words, what
differed and what to do; the backend passes xbind's sentence to the
model and the page's card unchanged (`refusal: "ticket"` with a `reason`
code beside it):

| Site | Reason | xbind's words (the agent and the card) |
|---|---|---|
| `Redeem`, no such request | `missing` | no request {id} for this person: file it again |
| `Redeem`, pending | `pending` | request {id} waits for the person's decision: nothing to use yet |
| `Redeem`, denied | `denied` | the person denied request {id} at 10:51: don't ask again unless they say so |
| `Redeem`, used | `used` | request {id} was used at 10:52: an approval runs once |
| `Redeem`, withdrawn | `withdrawn` | request {id} was withdrawn at 10:52 (by the agent, or by xbin when the agent was stopped) |
| `Redeem`, ticket expired | `expired` | the approval of 10:47 expired at 11:02, 15 minutes after it: file it again |
| `Redeem`, pending expired | `expired` | request {id} expired at 10:47 tomorrow after 24 h without a decision |
| `VoidTickets` (pause, switch off, disabled) | `voided` | the approval of 10:47 was voided when your agent was paused (or stopped) at 10:49: file it again |
| `Redeem`, method | `bound` | the call's method changed after approval (approved POST, made PUT): make exactly the approved call |
| `Redeem`, path or query | `bound` | the call's path changed after approval |
| `Redeem`, content type | `bound` | the call's Content-Type changed after approval |
| `Redeem`, a start's brief | `brief` | the brief's text changed after approval: file the start again with the current brief |
| `Redeem`, a start's other fields | `bound` | the start changed after approval ({fields}): file it again |
| `Redeem`, a body | `bound` | the body changed after approval: make exactly the approved call |
| `Redeem`, the tile | `tile` | the tile moved: {path} now names another tile than the one approved |
| a start's choice | `choice` | the coding agent the person picked is no longer allowed on {tile}: file it again |
| a message's session | `session` | the session the person approved a message to ended / is no longer one this agent started / is now on {harness} in mode {mode}, not {…} as approved: file it again |
| `ticketStart`, nothing named | `choice` | the approval names no coding agent: file it again |

Nothing is spent by a refusal: the gate checks, then spends.

**A start that fails for a reason that isn't the agent's** — the
coding agent didn't start, the sandbox didn't, the first prompt couldn't
be handed over (a 5xx of the open) — is `failed`, with xbind's words
(`failure: {at, why, retry}`), never "lapsed" or "denied". The agent is
told: "approved by your person, but the tile agent didn't start: … — not
your doing; your person can try again from the card". The batch shows it
with **Try again** (`POST /delegate/requests/<id>/retry`, the person's own
session): the request is approved again, a fresh 15 minutes, and the
agent's backend uses it as it used the first — no new request from the
agent. Retry is offered until the request's 24 hours run out. A refusal
of the open itself (a 4xx: a sign-in removed, a trust the tile now asks
for) is `failed` without retry, its words saying what to do.

## 5. Follow-ups go to the session that is there

**Cause.** `ws_tools_agents.go:251 wsStartTool` never looked at the
sessions on the tile; `tile_agents` (`:522`) listed only this
conversation's, and nothing named the person's own; the backend's start
answer said nothing of what runs. A message to a session in a mode that
doesn't ask is refused (`internal/server/delegatesend.go:103-113`
`modeRefusal`): such a session acts without asking, so a message from an
agent that can be prompt-injected would be an act nobody approved.

**Rule.**

- (a) `tile_agent_start`'s answer, `tile_status` and `tile_agents` name
  the live sessions on the tile: the ones this conversation started
  (working, idle since 10:52, waiting for the person; coding agent, model,
  mode; its brief in one line) and the person's own ("your person's own
  session: not yours to drive").
- (b) **Continue by default.** When a start is asked for a tile where a
  session this conversation started is live and idle, the backend files
  it with `continue: <session>`; xbind checks the session is one this
  agent started, on that tile, live, and offers it on the sheet:
  "Continue the OpenCode session from 10:52 with this follow-up" (the
  brief as the message), secondary "Start a new session instead". Approved
  as continue, the ticket's start sends the brief to that session (queued
  if it works by then) instead of opening one, and answers its row with
  `continued: true`. `tile_agent_start {new: true, reason}` files a new
  session, the reason quoted on the sheet. Not offered — a new session
  opens — when the session ended, is working or waits for its person, or
  when the person picks another coding agent on the sheet. Context fill is
  not part of the rule: xbind doesn't know it for every harness. An older
  client (approve without `knows: ["continue"]`) approves a new session,
  as it showed.
- (c) **A message to a session that doesn't ask is an approval, not a dead
  end.** `tile_agent_message` to such a session files a request of kind
  `message` (`POST /api/xbin/term/sessions/<id>/prompt {text}`): "Send
  this message to the OpenCode session in eui (it acts without asking)",
  the whole text shown. The approval sends exactly that text, once, to
  that session; it is refused (§4) if the session's coding agent or mode
  changed since, it ended, or it isn't this agent's. A session that asks
  takes messages as before, unapproved.

## 6. The buttons

**Cause.** `web/bx-agent-approve.js:376-382`: a start whose mode doesn't
ask set `never`, which drew Deny `primary` and Allow `danger`; the wider
step (`:297-298`) drew its Allow `danger` FIRST and Go back `primary`
after it. The app did the same (`AgentApproval.swift:296, 338, 363`
`primaryDeny: r.neverAsks`; `DelegationWords.swift:378`). The permission
cards (`chat/asks.js` `permView`, drawn by the dock, the Agent tab and the
agent template's own approvals) drew the coding agent's options in its
order — its rejection usually LAST, on the right, where every other
surface has its approve. The plan review (`bx-agent-changes.js:302-303`)
was already right.

**Rule** for every approval surface (the sheet, the plan review, the
permission cards, the app's sheets): Approve/Apply is the primary action,
on the right; Deny / Not now / Go back the quiet secondary, on the left;
always the same positions. A permission card keeps the coding agent's
options and their words, placed: its rejections first, its first allow
(the primary) last, the others between in its order — superseding "in its
order" (the checks `harness-ask`, `harness-child`, `grant` say so now).
A warned approval keeps Approve where it is —
coloured as danger where the act is (a mode that acts without asking, the
wider standing grant), never by promoting Deny — and says the warning in
the body, armed after 600 ms (`/vendor/agent-armed.js`, unchanged).

## 7. Smaller, from the retro

- (a) `tile_agent_start`'s description: read the person's preference for
  the tile (start-defaults' `chosen`) and pass a coding agent, model or
  mode only when the person named one.
- (b) The pending answer says when it expires and that waiting is fine:
  "It waits up to 24 hours, until 10:47 tomorrow: an approval doesn't
  lapse while it waits."
- (c) **Page-driving verification — a proposal, not built.** A
  `preview_play {tile, steps: [{click | type | key | wait | read}]}` would
  drive one tile's page in the confined browser xbind already runs for
  previews (`POST /preview`, D234): the steps act on the page as the person
  sees it, no network beyond the workspace, no other tile's frames, at
  most 20 steps and 20 s, every click by a selector or a text, and the
  answer is the page's text and accessibility tree after each step (no
  screenshots to the model). Safety: the page's own API calls run as the
  person — so a play is an ACT: a page whose clicks call its API is
  approved like a tile call (a plan line or a sheet listing the steps),
  and a page that declares itself side-effect free (`"agent": "read"` and
  no non-GET call during a play: xbind cuts one) plays without asking.
  Size: about 600 lines (the preview runner gains an input queue, xbind a
  route and its gate row, the agent a tool), one decision.

## Safety rules, with the tests that hold them

Each was run RED without its rule, then GREEN with it — the rule taken
out for each run, and the verdict, are in the message of the commit that
adds this sentence, and of the commit after it for the last row (21 runs:
the 16 below and the backend's 5):

| Rule | Test |
|---|---|
| the ticket still binds tile, brief text and kind: a brief the AGENT swaps after approval is refused, with "the brief's text changed after approval", and spends nothing | `internal/server: TestApprovedStartBindsTheBrief` |
| the person's choices are validated as a by-hand start's: a harness the tile's orgs don't allow, a mode the harness doesn't have, a model xbind doesn't offer — 400, nothing decided | `internal/server: TestApproveChoicesAreByHandChoices` |
| a suggested mode that doesn't ask is never preselected, and a sign-in is never suggested | `internal/server: TestSuggestionNeverPicksANonAskingModeOrSignin` |
| the person's pick is what opens: harness, mode, model | `internal/server: TestPersonsPickIsTheStart` |
| a coding agent with no default mode (OpenCode) starts on its ticket | `internal/server: TestNoDefaultModeStartsOnItsTicket` |
| an approval-gated message sends exactly the approved text, once, to the session named; refused after a mode or harness change | `internal/server: TestMessageApproval*` |
| continue reaches only a live session this agent started, on the same tile | `internal/server: TestContinueOnlyOwnSessionSameTile` |
| preferences are per person: another person can't read or write them, the agent reads only its person's chosen line and writes nothing | `internal/server: TestStartPrefsArePersonal` |
| starting without asking: unchanged — no suggestion, only an asking mode, no sign-in | `internal/server: TestUnaskedStartIgnoresSuggestion` |
| an older client approving a start sends no choices: today's defaults; one without `knows: ["continue"]` approves a new session | `internal/server: TestOlderClientApprovals` |
| refusals say what differed, and spend nothing | `internal/delegation: TestRedeemSaysWhatDiffered` |
| retry only for a start that failed for a reason not the agent's, by the person | `internal/server: TestFailedStartRetry` |
| a coding agent with no default mode is said to start in its asking mode (start-defaults), so a session in it is known to ask | `internal/server: TestNoDefaultModeStartsOnItsTicket` |

The agent's side: the retro's session replayed as scripted evals
(`builtin-templates/agent/_backend/ws_eval_approvals_test.go`): the tic
tac toe start with the person switching to OpenCode on the sheet — one
approval round, started with OpenCode, the agent told so; "add a computer
player" — continues the session, one approval, no second session; a fix
handed to a session that doesn't ask — one message approval; a brief
edited by the person before approving — one round.

The browser: `hack/ui-harness/passes/approvalsux.js` (approvalsUx).
