# Workspace agent — one approval, one place (D250)

> Status: live

> **D251 changes rules here** (`plans/wsagent/auto-default.md`, the
> owner's answers of 2026-11-21): tile agents act without asking by
> default, a mode is an ordinary choice stated as a fact, and continuing
> is one plain approval. Each rule it replaces is marked **[D251]** below
> with what replaces it; the safety table marks the rows that changed and
> adds D251's own.

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

**No "·" in what this batch adds or rebuilds** (the owner, during this
batch, narrowed later the same day: "keep that for new elements for now,
and do a bigger refactor once we build broader ux/branding guidelines"):
the brief's one card and its marker, the one batch and its count badge,
xbin's start sheet (its picks, their captions, its bar and footer), the
continue and message items, the preferences — each fact an element of its
own, a chip, a labelled field, a row, a muted item laid out with a gap.
Lines this batch only passes through (the call sheet's body note, the plan
review, the app's sheets and inbox, the shared words tables and the app's
vectors) keep their "·" until those guidelines.

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
|  Runs on   ( OpenCode ) ( acts without asking ) ( no sign-in needed ) |
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
| ( OpenCode ) ( acts without asking ) |
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
|          ( OpenCode ) ( acts without asking )             |
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
| ( OpenCode ) ( acts without asking ) |
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
|  (!) OpenCode in this mode acts without asking: it edits files   |
|      and runs commands in eui without asking you. ...            |
|  Tile          Eui   apps/eui                                    |
|  Coding agent  [OpenCode              v]                         |
|                remembered for this tile                          |
|  Model         [its default           v]                         |
|                OpenCode's own list shows once a session runs     |
|  Mode          [its own default, acts without asking v]          |
|                OpenCode has no mode that asks first: in any mode |
|                it edits files and runs commands without asking   |
|                you.                                              |
|  [ ] Remember for this tile                                      |
|      Keeps OpenCode and its model for your next start here,      |
|      never its mode ...                                          |
|  Network       Its model only. No internet.                      |
|  Other tiles   It can call the tile APIs this tile is granted.   |
|  What it will be told   Written by your agent                    |
|  | This is a piece of work in this tile ...                  |   |
|  sent exactly like this                                          |
|  > Details: POST /api/xbin/term/sessions                         |
|  THE AGENT'S OWN WORDS, NOT CHECKED BY XBIN                      |
|  asked every time       change in Preferences                    |
|  [Deny]                               [Start without asking] (!)  |
+------------------------------------------------------------------+
```

The owner's case (the re-review, item 3): OpenCode is his own choice for
the tile, remembered, so it is preselected — and, having no mode that
asks, warned every time. A warned approval (a mode that doesn't ask, or a
coding agent with none): the warning is a banner in the body, Deny stays
where it is and quiet, Start stays where it is, coloured as the danger it
is, armed after 600 ms; nothing on the sheet says it asks. **[D251]** No
longer: the mode is a chip ("acts without asking" / "asks before
acting") and one plain line, the button the ordinary Start; nothing says
it asks where it doesn't.

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
- **[D251]** The default mode is the coding agent's work mode (it acts
  without asking); the agent's suggestion of a mode or a coding agent is
  a preselection, except a bypass (explicit) mode; OpenCode is an
  ordinary choice; it starts unasked only in a mode that asks the person
  named. The D250 rule this bullet replaces, as it was: the agent can't
  choose a mode that doesn't ask, or a sign-in, by
  suggesting it: a suggested mode that doesn't ask is never preselected —
  the select shows the asking default and, under it, "your agent suggested
  build, which acts without asking: pick it yourself if you want it". The
  default mode is the harness's own asking mode (its ask mode, else its
  plan mode — `acp.Provider.Asks`): a session the agent directs asks the
  person before it edits or runs, so nothing the agent tells it acts
  unseen. **OpenCode has none xbin can vouch for** (the review, M2: in
  its own agent table every agent starts from allow-all, `plan` only
  denies edits, and a tile's own config and plugins can loosen any
  agent): the agent's suggestion of it isn't preselected
  (`harnessNotTaken`, warned); the person's own choice of it — remembered
  for the tile, or their preference — is, warned every time (the banner,
  the armed delay, the danger-coloured Start; re-review item 3); it never
  starts unasked ("your person's coding agent for this tile acts without
  asking, so every start is approved on the sheet"); every agent message
  to a session of it is a message approval; a plan line that resolves to
  it carries the warning and starts unticked, as warned lines do (D238).
  The workspace's default is a coding agent that asks wherever one is
  allowed — OpenCode only where it is all the tile allows, warned.
- **A plan's `agent.start` runs the line as reviewed** (the review, H1):
  the open is the line's coding agent, model and mode, whatever the call's
  body named; the line says so, and how to run another.
- **The approval holds to what the sheet showed** (the review, L2): the
  approve carries `seen: {signInId, net}` (and, for a continue, the
  session's `harness` and `mode`, D251); where xbind would now resolve
  the sign-in or the network otherwise, it is 409 `{changed, reload}`,
  nothing decided, and the sheet reloads. An approve that picks, or
  continues, without `seen` is 400 (re-review item 5); one that picks
  nothing (an older client, the app) is as before.
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
  ticked for them, and they can untick it). It keeps only the fields the
  person CHANGED (not a value preselected from the agent's suggestion) —
  the coding agent and model of any coding agent, OpenCode's too
  (re-review item 3) — and never a mode that doesn't ask: one predicate
  in the store (`RememberTile`), which refuses such a mode on write and
  ignores one on read (the review, M1). **[D251]** Any mode but a bypass
  one is kept now, asking or not. The sheet says beforehand what is
  kept. Such a mode is picked each time, as a bypass mode is in the Agent
  tab. A value an earlier xbind stored under looser rules (v0.3.83 took
  OpenCode's `plan` as an asking mode) is ignored at read, never an error,
  so it never blocks a later write (re-review item 1).
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

**By cause** (the review, L3): a start the agent got wrong — not a tile
agent's, a saved sign-in named — is refused before its ticket is spent
(`bound`), never `failed`. `failed` is only for a backend that filed the
start with `knows: ["retry"]` (the workspace agent's does); an older one
gets the open's answer as before. **Try again** is the sheet again: the
earlier picks preselected, the sign-in tick always unticked, Allow armed
late; `…/retry` takes the approve's body and validates it as one, keeping
no earlier tick. **Review all** says "N request(s) were withdrawn or
decided meanwhile" and goes on.

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
  as it showed. A continue is judged on the live session — its model and
  what it holds, as a message to it is — never on what a new start there
  would run on (the review, M3). The sheet shows the session's coding
  agent, model, mode and sign-in as elements, warns when it doesn't ask
  or carries a sign-in, and re-arms on every switch between continue and
  new (L1). **[D251]** No warning, tick or re-arm: continuing is one plain
  approval, Send, held to the session shown (`seen`), and a continue or a
  message can be sent from its batch row. A start filed as a follow-up is judged at filing as the
  decide judges it — on the session's live source — not as a new start
  on the model the body suggests; a new session the person picks instead
  is judged in full (re-review item 6). **A session that carries the
  person's saved sign-in**: the
  follow-up uses it, so it needs the explicit, unticked, warned tick "Use
  my sign-in for this follow-up" each time — the rule chosen, as a start
  with that sign-in needs it; without the tick the sheet offers "Start a
  new session instead", and the redeem checks the tick still holds.
- (c) **A message to a session that doesn't ask is an approval, not a dead
  end.** `tile_agent_message` to such a session files a request of kind
  `message` (`POST /api/xbin/term/sessions/<id>/prompt {text}`): "Send
  this message to the OpenCode session in eui (it acts without asking)",
  the whole text shown. The approval sends exactly that text, once, to
  that session; it is refused (§4) if the session's coding agent or mode
  changed since, it ended, or it isn't this agent's. A session that asks
  takes messages as before, unapproved. A message the agent sent that is
  held behind a running turn is judged again when it goes in (the review,
  L4): if the person switched the session to a mode that acts without
  asking meanwhile, it is not delivered — the queue event lists it in
  `returned` with the reason, and a notice says so; the person's own held
  messages go in as ever. The agent's own read of its sessions carries
  what was returned, and the conversation is told once, in xbind's words,
  with what to do (re-review item 4a). A switch the person asked for that
  the coding agent hasn't answered yet counts as its mode too — at the
  delivery and at the direct send gate (4b).

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
coloured as danger where the act is (the wider standing grant; **[D251]**
no longer a mode that acts without asking), never by promoting Deny — and says the warning in
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

Each was run RED without its rule (the rule taken out in the code, the
test run, failing), then GREEN with it restored. The batch's 22 runs are
in the message of the commit that adds this sentence; the review's 28 in
the message of the commit after it; the re-review's in the message of the
commit that adds rows 35 to 44. 44 rules, each with its test:

| # | Rule | Test |
|---|---|---|
| 1 | the ticket still binds tile, brief text and kind: a brief the AGENT swaps after approval is refused, with "the brief's text changed after approval", and spends nothing | `internal/server: TestApprovedStartBindsTheBrief` |
| 2 | the person's choices are validated as a by-hand start's: a harness the tile's orgs don't allow, a mode the harness doesn't have, a model xbind doesn't offer — 400, nothing decided | `internal/server: TestApproveChoicesAreByHandChoices` |
| 3 | **[D251]** a suggested bypass mode is never preselected (any other suggestion is) | `internal/server: TestSuggestionNeverPicksABypassModeOrSignin` |
| 4 | a start naming a sign-in is refused before its ticket is spent, whatever was approved | `internal/server: TestSuggestionNeverPicksABypassModeOrSignin` |
| 5 | the person's pick is what opens: harness, mode, model | `internal/server: TestPersonsPickIsTheStart` |
| 6 | a coding agent with no default mode starts on its ticket | `internal/server: TestNoDefaultModeStartsOnItsTicket` |
| 7 | start-defaults says the mode the start takes, and whether it asks (**[D251]** the work mode) | `internal/server: TestNoDefaultModeStartsOnItsTicket` |
| 8 | an approved message passes the mode rule, sends exactly the approved text, once, to the session named | `internal/server: TestMessageApproval*` |
| 9 | an approved message is refused after a mode or harness change | `internal/server: TestMessageApproval*` |
| 10 | continue is offered only for a live, idle session this agent started on the same tile — never the person's own, another agent's, one on another tile, or a busy one | `internal/server: TestContinueOnlyOwnSessionSameTile` |
| 11 | a continue whose session ended or is busy is refused, nothing spent | `internal/server: TestContinueOnlyOwnSessionSameTile` |
| 12 | an older client (no `knows: ["continue"]`) approves a new session, through the start route | `internal/server: TestContinueOnlyOwnSessionSameTile` |
| 13 | an older client approving a start sends no choices: today's defaults | `internal/server: TestOlderClientApprovals` |
| 14 | preferences are per person: another person can't read or write them, the agent reads only its person's chosen line and writes nothing | `internal/server: TestStartPrefsArePersonal` |
| 15 | starting without asking: no suggestion, only an asking mode the person named, no sign-in | `internal/server: TestUnaskedStartIgnoresSuggestion` |
| 16 | refusals say what differed, and spend nothing | `internal/delegation: TestRedeemSaysWhatDiffered` |
| 17 | Try again only for a start that failed for a reason not the agent's, by the person, its sign-in tick its own | `internal/delegation: TestFailedStartRetry`, `internal/server: TestFailedStartRetryRoute` |
| 18 | a plan's start opens the line reviewed, not the agent's suggestion (H1) | `internal/server: TestPlanStartRunsTheReviewedLine`, `TestPlanStartUsesOnlyTheSignInItsLineNamed` |
| 19 | OpenCode has no asking mode (M2) | `internal/server: TestOpenCodeHasNoAskingMode` |
| 20 | OpenCode never starts unasked (M2); **[D251]** a suggestion of it is preselected | `internal/agentprefs: TestNoStartWithoutTheSheetInAModeThatDoesntAsk`, `internal/server: TestOpenCodeHasNoAskingMode` |
| 21 | a session of OpenCode takes no unapproved agent message (M2) | `internal/server: TestOpenCodeHasNoAskingMode` |
| 22 | Remember keeps only the person's own changes (M1) | `internal/server: TestRememberKeepsOnlyThePersonsOwnPick` |
| 23 | **[D251]** the store keeps any mode but a bypass one, and a read ignores a bypass one | `internal/agentprefs: TestRememberKeepsAnyModeButBypass`, `TestAgentSessionModesAreAnyButBypass` |
| 24 | a continue is judged on the live session; a connector model by its own id (M3) | `internal/server: TestContinueIsJudgedOnTheLiveSession`, `internal/term: TestConnectorModelIDDropsTheHarnessPrefix` |
| 25 | **[D251]** a follow-up into a session with the person's sign-in needs no tick, and is held to that session's sign-in at approval and at redeem | `internal/server: TestContinueIntoASessionWithASignInIsThatSession` |
| 26 | **[D251]** continuing is one plain approval: chips, Send, no tick | `hack/agent-start-words.test.mjs` |
| 27 | the approval holds to what the sheet showed (L2) | `internal/server: TestApprovalHoldsToWhatTheSheetShowed`, `hack/agent-start-words.test.mjs` |
| 28 | an agent's own bad start is refused before its ticket is spent (L3) | `internal/server: TestStartFailuresByCause` |
| 29 | `failed` only by cause, and only for a backend that knows it (L3) | `internal/server: TestStartFailuresByCause` |
| 30 | Try again preselects the earlier picks, never the sign-in (L3) | `hack/agent-start-words.test.mjs` |
| 31 | an agent's held message is judged when it goes in (L4) | `internal/term: TestAgentMessageHeldIsJudgedAtDelivery` |
| 32 | the queue route marks the agent's message as the agent's (L4) | `internal/server: TestAgentQueuedMessageIsMarkedAsTheAgents` |
| 33 | a plan's stubbed open is checked as picked, not only authorised (the review's test fix) | `internal/server: TestPlanStartUsesOnlyTheSignInItsLineNamed` |
| 34 | the sheet says a model or mode a coding-agent switch reset is the default, not the person's pick — what Remember keeps (M1) | `hack/agent-start-words.test.mjs` |
| 35 | a file a released xbind wrote (OpenCode's `plan` saved as asking) loads, and never blocks a later write (re-review 1) | `internal/agentprefs: TestAReleasedFileNeverBlocksAWrite` |
| 36 | the person's OpenCode for a tile: remembered (**[D251]** its mode too), preselected (re-review 3) | `internal/server: TestTheOwnersOpenCodeForATile` |
| 37 | a coding agent with no mode that asks never starts unasked, and says why (re-review 3) | `internal/server: TestTheOwnersOpenCodeForATile` |
| 38 | **[D251]** the workspace's default is the first coding agent the tile allows, in its work mode (no longer one that asks) | `internal/server: TestTheOwnersOpenCodeForATile` |
| 39 | **[D251]** the OpenCode sheet: the fact, no warning, nothing says it asks; Remember says what it keeps | `hack/agent-start-words.test.mjs`; approvalsUx checks the sheet |
| 40 | **[D251]** a plan line on the person's OpenCode is an ordinary line, its mode said | `internal/server: TestPlanStartUsesOnlyTheSignInItsLineNamed` |
| 41 | picks and continuing need what the sheet showed (re-review 5) | `internal/server: TestPicksNeedWhatTheSheetShowed` |
| 42 | a follow-up is filed as it would be decided; a new session picked instead is judged in full (re-review 6) | `internal/server: TestAFollowUpIsFiledAsItWouldBeDecided` |
| 43 | the agent is told once of a held message xbind returned (re-review 4a) | `builtin-templates/agent/_backend: TestWsEvalApprovalReturnedMessageIsSaidOnce`, `internal/server: TestAgentQueuedMessageIsMarkedAsTheAgents` |
| 44 | a mode switch in flight counts at the delivery and at the send gate (re-review 4b) | `internal/term: TestAgentMessageHeldIsJudgedWhileASwitchIsInFlight`; the send gate by a widened window (the commit message) |

D251's rules (`plans/wsagent/auto-default.md` §A5, B1), each run red
without its rule — the runs are in the message of the commit that adds
them (S1–S12):

| # | Rule | Test |
|---|---|---|
| 45 | every text to a session that doesn't ask is an approval of that text, once — whatever mode it was approved in (D250's exemption gone) | `internal/server: TestAStartActsWithoutAskingAndEveryTextToItIsApproved`, `TestDelegateSendRules` |
| 46 | no path without the sheet starts a session in a mode that doesn't ask (the preference, start-defaults), and the agent is told so | `internal/server: TestNoPathWithoutTheSheetStartsAModeThatDoesntAsk` |
| 47 | a notification's Allow once never approves a start or a message | `internal/server: TestAStartActsWithoutAskingAndEveryTextToItIsApproved` |
| 48 | a continue is held to the session shown — its coding agent, mode and sign-in (`seen`) | `internal/server: TestAContinueIsHeldToTheSessionShown`, `TestContinueIntoASessionWithASignInIsThatSession` |
| 49 | the default is the work mode; a suggestion is a preselection, never a bypass mode | `internal/server: TestAStartActsWithoutAskingAndEveryTextToItIsApproved`, `TestSuggestionNeverPicksABypassModeOrSignin` |
| 50 | the ticket still binds brief, text and path; `seen` still required | `internal/server: TestApprovedStartBindsTheBrief`, `TestMessageApproval`, `TestPicksNeedWhatTheSheetShowed`, `internal/delegation: TestRedeemSaysWhatDiffered` |
| 51 | a text is allowed only with all of it on screen: a clipped `seen.textLen` is refused (H1) | `internal/server: TestAllOfTheTextOrNothing`; `hack/agent-start-words.test.mjs`; approvalsUx (no Send before Show all) |
| 52 | a waiting line provided early is stale where its slot or a warning changed (M1) | `internal/broker: TestWaitingLineProvidedEarlyIsHeldToTheProposal` |
| 53 | a resume never reopens in a bypass mode unless its person picks it there (M2) | `internal/server: TestDelegateSessionStart` |
| 54 | a start line is held to its facts and warnings, not its words (L3) | `internal/server: TestStartLineStalenessIsItsFacts` |
| 55 | a continue's card names the session's saved sign-in; held to it at the redeem, its mode too (L4) | `internal/server: TestAContinueIntoASessionWithItsSignIn`, `TestAContinueIsHeldToTheSessionShown` |

The agent's side: the retro's session replayed as scripted evals
(`builtin-templates/agent/_backend/ws_eval_approvals_test.go`): the tic
tac toe start with the person switching to OpenCode on the sheet — one
approval round, started with OpenCode, the agent told so; "add a computer
player" — continues the session, one approval, no second session; a fix
handed to a session that doesn't ask — one message approval; a brief
edited by the person before approving — one round.

The browser: `hack/ui-harness/passes/approvalsux.js` (approvalsUx).
