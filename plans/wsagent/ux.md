# Workspace agent — UX design (package D0)

> Status: live — the design pass for the web and iOS UI of the workspace
> agent (2026-11-15, branch `wsagent/d0-ux`, from `wsagent/main`). A
> specification and static mockups for the owner's review; no product code.
> Packages D (web) and E (iOS) of `plans/workspace-agent.md` build from it.
> Nothing here is decided until the owner has answered section 15.

Reads with: `plans/workspace-agent.md` (§4, §5.1–5.3, §5.6, §5.8–5.10),
`plans/wsagent/chat-kit-spec.md`, D229 (constraints, the connection view,
agent preferences, `open({session})`), and the delegation contract —
`docs/protocol/delegation.md` on branch `wsagent/s2-delegation` (requests
and tickets, standing approvals, change sets, the audit log, pause). That
contract is not merged into `wsagent/main` yet; this document uses its
routes and words and names no decision number for it.

**Mockups** are static, self-contained HTML files outside the repo, in
`/tmp/claude-1000/wsagent/ux/` (`index.html` links them all; `shots/` has
each at 1440 and 390 px wide in Concrete Night and Concrete Day). Section
12 lists them. They are sketches of layout, hierarchy and words — not
pixel specifications. Token values are `web/theme.css`'s.

## 0. The design in short

1. **One button, always in the same place.** Web: a chip in the shell's
   top bar. iPhone and iPad: the trailing-most item of every panel bar.
   iPhone Duo: the last item of the vertical bar. Each carries a count of
   what waits for the person.
2. **Beside the work, never instead of it.** Web: a dock that pushes the
   canvas. iPhone: a sheet with a medium detent that leaves the tile usable.
   iPad and Duo: a second pane. The tile in view is offered as a context
   chip.
3. **Two layers, visibly different.** The agent's page shows what the agent
   wrote. Everything that *grants* — approving a call, a tile agent start,
   an admin batch, resuming after a pause — is drawn by xbin's own chrome
   (web) or by the app (iOS), from what xbin says about the request.
4. **The connection state is a sentence that is always on screen**: "14
   tiles · 3 orgs connected · 6 not connected", one tap from the panel that
   says why for each.
5. **The UI teaches the right kind of request**: an empty state that says
   what the agent does and doesn't do, starters built from the person's own
   tiles, questions as choices, an editable brief before any tile agent
   starts, a plan before anything changes, and redirects instead of errors.
6. **Mobile is not a port.** Thumb-reach layout, dictation, quick replies,
   one Live Activity per workspace, pushes only when the person is needed,
   Handoff for what is better on a big screen.

Where web and iOS deliberately differ is collected in section 11.5.

### Principles (from the repo's own rules)

- Agents are software. Chrome says "your agent" and "the workspace agent",
  never a name or a face; no sparkles, no robot (docs/design.md, brand §9).
- Status is a glyph and a word; the accent is for the primary action,
  selection and links only; 2px corners, no pills, squares for dots; no
  looping animation — progress is a running time (D184, theme rules R1–R7).
- Sentence case, short declaratives, buttons are verbs. Inside the dock,
  capitalisation follows the Agent tab ("Allow once", "Send"); the top-bar
  chip follows its lower-case siblings (`settings`, `docs`).
- Show the plan, then ask. A missing thing is said in a sentence, with who
  can fix it.

### Patterns taken from other products (late 2026)

One line each; sources were read on 2026-11-15 by a research sub-agent and
are listed in section 16. Nothing visual is copied.

| Pattern | From | Used in |
|---|---|---|
| One never-resetting thread with child sessions as status rows | Claude Dispatch (Cowork) | §4, §5 |
| Two waiting states: the agent asked *you* vs a child is waiting | Dispatch ("Awaiting answer" / "Awaiting input") | §5 |
| The list names the pending thing ("Approve a change to Invoices"), not "approval required" | Devin's sidebar | §5.4 |
| Approval with an exit that is neither yes nor no ("tell it what to do") | Codex in ChatGPT mobile | §6.2 |
| Question cards: a few choices, free text, Skip that is not consent | Lovable | §2.4 |
| A plan that lists done-when and out-of-scope, editable before the build | Replit plan mode, Lovable plan view | §2.5–2.6 |
| Needs-you groups sorted above working and finished | Devin, Claude Code agent view | §5.4 |
| Push only for questions and approvals, not every finished turn | Cursor cloud agents | §9.3 |
| Live Activity for running agents; open goes to what waits | Replit mobile, Cursor iOS | §9.4 |
| The person stays the owner, the agent is the delegate | Linear | copy throughout |

Not taken: Dispatch's auto-deny after ten minutes (ours wait 24 hours and
say so); Linear's "no prompt for undoing its own work" (every side effect
is asked or under a standing approval here); approving from a Live
Activity (an open request at Codex, unshipped; see §9.3).

---

## 1. Entry

Mockups: 01, 11, 14.

### 1.1 The button

| Form factor | Where | Looks like | Badge |
|---|---|---|---|
| Web shell | top bar, left of `settings` | a chip like its siblings: the agent glyph + `workspace agent`; selected state (selection fill + 2px accent rule) while the dock is open | a square count plate |
| Old scaffolds | none (no shell hook) | one dismissible alert pointing at `/xbin/agent` (plan §5.8) | the page's title count |
| iPhone | `PanelBar`, the trailing-most item on every panel (Home, a screen, a tile, a terminal, an agent) | a 2pt-corner plate with the agent glyph | square accent plate |
| iPad | the same item | the same | the same |
| iPhone Duo, open | the same item, marked vertical-preferred: the **last** item of the system's vertical bar, apart from the panel's own items | the same | the same |
| Home (all iOS) | additionally a row under "Needs you": "Workspace agent — working · 2 tile agents · 2 need you" | list row | count |

- **The glyph**: one node above three (a parent and its delegates), drawn
  as a `<bx-icon>` / SF-style symbol in the agent part colour. It is the
  only new glyph the feature needs.
- **The count** is what waits *for the person because of the agent*:
  pending approval requests, proposed change sets, the agent's own open
  questions, and tile agent sessions it started that wait for the person.
  On iOS the workspace switcher's existing count stays the total across
  workspaces and includes these.
- **Not shown at all** to a person who has no agent (no read access on the
  sealed component, or the workspace switch is off and they are not an
  admin). See §10.1.
- **The owner's brief says "iPad sidebar".** The app has no sidebar on any
  device (one `PanelStack` everywhere). The design puts the button in the
  panel bar on iPad and does not add a sidebar for it (question Q2).

### 1.2 What opening it does

- **Web: a resizable dock on the right**, `--bx-dock-w` (400px; 432 in the
  mockups) by default, 360–720, width remembered per person. It sits in the
  shell's body beside the canvas and *pushes* it, as `<bx-side>` does on the
  left; it never covers tiles. Under 640px the dock is the whole page — the
  same layout `/xbin/agent` has. Opening focuses the composer; `Esc` in an
  empty composer closes the dock; the button toggles. A shortcut is
  proposed in Q11.
- **iPhone: a sheet with detents `[.medium, .large]`**, opened large.
  - Why a sheet and not a pushed screen: a push joins the panel stack, so
    the person loses the tile they were looking at and edge swipes change
    meaning; a sheet sits over whichever panel is showing, is dismissed with
    one swipe, and comes back where it was.
  - **Medium is the "beside what I'm looking at" mode**: background
    interaction is enabled up through medium, so the tile behind scrolls and
    taps. The sheet then shows the newest lines, quick replies and the
    composer. Focusing the composer raises the sheet to large.
  - The sheet keeps its scroll position and draft when dismissed.
- **iPad: a trailing column** beside the panel stack (resizable, about 40%
  by default). In Split View, Slide Over or any compact width it falls back
  to the iPhone sheet.
- **iPhone Duo, open: the second pane.** The first pane keeps the panel
  stack; the agent holds the second. This is new app chrome at the root:
  today two panes exist only inside a native tile's `split` node. Folded,
  the Duo is an iPhone. (D200's caveat stands: no Duo simulator exists, so
  this layout is designed but cannot be tested yet.)

### 1.3 "Talk about what I'm looking at"

- When the agent is opened, the tile in focus (web: the active window; iOS:
  the tile or screen of the panel) is offered as a **context chip** in the
  composer: "About **Orders** ✕". On a screen with no single tile in focus
  the chip is the screen: "About the **Sales desk** screen".
- It is an offer, not a setting: one tap removes it, and it is attached to
  the next message only. The sent message shows the chip above its text.
- On the web the tile the chip names is outlined on the canvas (2px accent
  outline) while the composer has focus.
- If the tile is not connected in this session, the chip says so instead of
  failing later: "About **Ledger** — not connected (Finance allows private
  models only)", and tapping it opens the scope panel.
- What the agent receives is the tile's path and name, nothing the page
  shows. It looks with its own tools (and `preview`, when that lands).

---

## 2. Empty state and nudges

Mockups: 02, 03, 11.

### 2.1 The empty state

One heading, one sentence, two short lists, starters.

- Heading: **Ask for an outcome**
- Line: "The workspace agent plans the work, hands building to each tile's
  own agent, and asks you before anything changes."
- **It can**: look across your *N* connected tiles · start tile agents and
  keep track of them · do things in a tile for you, once you approve ·
  remember what you tell it.
- **It can't**: change code itself — a tile's agent does · answer a tile
  agent's questions for you · see the *M* tiles that aren't connected ·
  use the web.
- *N* and *M* are the scope chip's numbers, so the list is true for this
  session. On iPhone each list has three lines.

The lists exist because the research is consistent that people don't know
what an agent can do and give up after one failure (plan §5.10). They are
shown in a new session until the first message, and again from ⋯ → "What
can it do?".

### 2.2 Starters, built from the person's own tiles

- Four at most (three on iPhone), each with a kind in micro caps: **Ask**,
  **Fix**, **Change**, **New**.
- Generated when the session is empty from what the agent can already see
  without side effects: tile names, alerts and failed states ("Team
  calendar's last sync failed. Find out why."), recent sessions. Generic
  ones are a fallback only when the person has fewer than two tiles.
- **A starter fills the composer; it does not send.** One with a blank
  ("Add ___ to Invoices") puts the cursor in the blank. Filling shows the
  shape of a good request and lets the person make it theirs (Q8).
- No starter proposes a destructive or admin action.

### 2.3 The composer's standing nudges

- Placeholder: "Ask for an outcome…" (later turns: "Reply, or ask for
  something else…").
- Hint under the composer until the first message of a session: "Say what
  you want and how you'll know it's done. The agent works out the steps."
- The `+` menu: attach a file, add a tile as context, "What can it do?".

### 2.4 Three kinds of request, three answers

| The person writes | The UI answers with | Why |
|---|---|---|
| Vague — "fix the invoices" | A **question card**: one question, two to four choices *built from what the agent found* ("Three invoices are overdue but still marked sent", "The PDF export failed twice this morning"), "Something else" as a free-text field, **Answer**, and "Skip — let the agent look around first". | People often can't answer open clarifying questions; they can pick. Skip is never read as consent to extra work. |
| Low-level — "change line 40 in export.go" | A **redirect card** with two choices: "Hand this to Orders' own agent" (opens the tile's Agent tab / native agent screen with the message as written, as a draft) and "Tell me the outcome instead" (with an example in the person's domain). | Not an error. The agent doesn't edit code; the tile's agent does, and the person may not know there are two. |
| Impossible — "delete the old tile", "check prices online" | A **"Can't do this" card** (info tone, not danger): each thing it can't do, why in one line, and the nearest thing it can ("It can show you where it is and what still uses it"). | A refusal without a next step teaches people to stop asking. |

Questions the agent asks (`ask_user`) always use the question card: at most
four questions at a time; choices as full-width rows; "Something else" on
every question; drafts kept if the dock or sheet closes. On iPhone a
multi-question card pages ("Answer 1 of 2") with the buttons at the bottom.

### 2.5 "Scope, then hand off": the brief

Before a tile agent starts, the agent restates the request as a **brief
card** the person can edit:

- Four fields: **Goal**, **Done when**, **Leave alone**, **Check by**.
  Edit turns them into text fields in place; or the person just says what
  to change.
- One line under the card says what it will run on: "Claude Code · asks
  before acting" (from the person's agent preferences and the tile's org).
- **One button: "Review and start…"** It opens the session-start approval
  (§6.4), which shows the brief exactly as it will be sent. There is no
  second "Start": approving the start *is* approving the words.
- If the person's preference is "start without asking" (§6.5), the button
  reads "Start" and the brief is the only confirmation.

### 2.6 The plan before side effects

For anything with more than one step that changes something, a **plan
card** first:

- Numbered steps, each with a tag: "reads only", "asks you" (shield glyph),
  and later its state ("done", "working 4:10", "waiting for you", "needs
  your OK", "not started").
- Buttons: **Go ahead** and **Change the plan**.
- A line that removes the commonest misunderstanding: "Saying go ahead
  starts step 1. It does not approve steps 2–4: each asks when it is
  reached."
- After "go ahead" the card stays as the conversation's progress (the kit's
  `plan` block; pinned by a host that pins plans).

### 2.7 Progress and reports

- **Progress lines**: a quiet list with a left rule — "Started Claude Code
  in Orders · 10:03" — and one live line with a running time, "working
  11:42 · 2 tile agents". Older lines fold into "Worked for 12 min · 6
  steps". No shimmer.
- **Reports** are written for someone who saw none of the work (plan
  §5.10): a bold one-line result, "Checked: …", "Not checked: …", and what
  still waits, with quick replies for the next step.

---

## 3. Scope and connection state

Mockups: 04, 13.

### 3.1 The chip

- Always visible at the top of the conversation, under the header: three
  segments —
  - plug glyph, "**14 tiles · 3 orgs**" (connected),
  - lock glyph, "**6 not connected**" (hidden + blocked + no access; the
    segment is absent when zero),
  - the model, "big ▾", with "· private" when the connector says so.
- The words are xbin's (`counts.summary` of `GET /agents/connection`).
  Narrow widths shorten to "14 connected · 6 not".
- Tapping it opens the panel (web: a pop-over inside the dock; iPhone: a
  sheet pushed inside the agent sheet; iPad/Duo: a pop-over).
- The chip changes at once when a rule, a share or the scope changes
  (refetch on the `orgs`/`components` events the shell already gets).

### 3.2 The panel

- **Totals first**: four counters — connected, hidden by you, blocked, no
  access.
- **The model** on one row with "Change…".
- **A filter**: All · Connected · Not connected.
- **Org rows**, each: a tri-state box, a disclosure arrow, the name, "5 of
  6", and the state word when the whole org shares one. The tiles in no org
  are the row "Workspace — tiles in no org".
- **Tile rows** under an unfolded org: a box, the name, the state.

| State | Glyph + word | Box | Extra |
|---|---|---|---|
| connected | check, "connected" | ticked | |
| hidden by you | crossed eye, "hidden by you" | empty, can be ticked | |
| blocked | lock, "blocked" | disabled | the reason, once per org, in a tinted row: "Finance allows private models only (pinned by a workspace admin). **big** is not private." with "See what a private model would connect…". A tile under an org by a share adds "This tile is under Finance by its share `apps/*`." |
| no access | "no access" | disabled | only for something the scope names that the person can't see |

- Blocked wins over hidden, as in the API: ticking would not connect it.
- **Multi-select**: ticking an org takes all its allowed tiles; a mixed box
  means some. "Select all allowed" resets to the default. Changes are
  staged; **Apply** commits them to this session, **Cancel** drops them.
- Footer sentence: "Scope is where this session looks. It is not a
  permission — blocked stays blocked whatever you tick." Scope is the
  session's own parameter; the boundary is the org rules, enforced by
  xbind on every call. The panel must not suggest otherwise.

### 3.3 Changing the model: what-if first

- "Change…" lists the models the person may use, each with where it runs
  and "private" as a tag.
- Selecting one shows xbin's `whatIf` before anything changes: "**Connects
  4 tiles**" (named), "**Disconnects none**", and the resulting sentence
  "18 tiles · 4 orgs connected · 2 not connected". Then **Switch model** /
  **Cancel**.
- A change that disconnects tiles lists them with the reason and makes the
  button read "Switch and disconnect 3".

### 3.4 A model change that is refused

When the conversation already holds data from an org whose rule the new
model doesn't satisfy:

- Title: "This session can't move to big"
- Body: "This conversation already holds data from **Finance**, and Finance
  allows private models only. **big** is not private, so the conversation
  can't be sent to it." / "A new session on big starts empty. It won't
  carry anything from this one, and Finance's 4 tiles won't be connected to
  it."
- Buttons: **Start a new session on big** (primary) · **Stay on
  local-llama**.

The same card appears if the person asks for the change in words.

---

## 4. Sessions

Mockups: 08, 13.

- **Main** is the default, long-lived session. It can be renamed but not
  archived, and is always first.
- **More sessions** are for work that needs another scope or model, or a
  clean slate. Each has a name, its scope, its model and a status.
- **Switching**: the session name under the header ("Main ▾") opens the
  list. Each row: name; "14 tiles · 3 orgs · big" (+ "· private"); status
  with glyph ("working · 2 tile agents", "waiting for you", "idle · 6
  Nov"). The current one has the selection fill.
- **New session…** asks for nothing up front: it copies the current scope
  and model, opens the empty state, and is named from the first request
  (editable). Scope and model are changed from the chip as usual.
- **Rename** inline; **Archive** for all but Main (archived sessions fold
  under "Older"; nothing is deleted from here).
- A session that waits for the person shows in Needs you with its name.
- Text under the list: "A new session starts empty, with its own scope and
  model. The notebook is shared by all of them, within each one's scope."
- The header always shows *which* session is in view; the chip always
  shows *its* scope and model. Two sessions never share a transcript.

---

## 5. Delegated work

Mockups: 01, 08, 11, 14.

### 5.1 The tile agent card

One card per session the agent started, in the conversation where it was
started, updated in place:

- the tile (glyph + name), the harness ("Claude Code"),
- status as glyph + word: `starting`, `working 4:10`, `waiting for you`,
  `finished`, `failed`, `stopped`, `stopped for the update`, `couldn't
  continue`,
- the last line, one line, clipped ("Writing export_test.go — 3 of 5 cases
  pass"),
- **Open**.

Several cards stack into one group. A finished group folds to "3 tile
agents finished" after the report.

### 5.2 Opening one

- **Web**: `open({session})` on the tile's window — the session's tab in
  the tile's Agent tab, which already says "started by your agent". Old
  shells get the D220 link.
- **iOS**: the native agent screen for that session (`.agent(cwd:
  session:)`), pushed on the panel stack; on iPhone the agent sheet drops
  to medium so both are visible, on the Duo it opens in the first pane.
- Coming back is the button again; the conversation is where it was.

### 5.3 A session waiting for the person

- The card gets a warn left rule, "waiting for you", the question in one
  line ("Asks: may I run the export tests?") and the words "Only you can
  answer." The button becomes **Answer** and goes to the request in the
  tile's session.
- The workspace agent never answers; the UI never offers that it could.
- v1 answers in the tile's session, where the context is. Answering from
  the dock is Q6.

### 5.4 "Needs you" on the web

- A button in the dock header with a count; it opens a tray under the
  header (xbin chrome). One list, oldest first, each row naming the thing:
  - "Approve a change to Invoices" — asked 10:14 · expires in 23 h 58 min —
    **Review…**
  - "5 admin changes proposed" — expires in 7 days — **Review…**
  - "Invoices · Claude Code asks permission" — run the export tests ·
    waiting 6 min — **Open**
  - "The agent asked you a question" — the first line — **Answer**
- It lists every tile agent session of the person that waits (the iOS
  inbox's rule: `pending>0 || questions>0 || waiting_permission`), with
  "started by your agent" where true, so it is also the web's first
  Needs-you list.
- Empty: "Nothing needs you".
- iOS keeps its inbox ("Needs you") and adds the two approval kinds to it,
  above the sessions.

---

## 6. Approvals (the security-critical surface)

Mockups: 05, 06, 12, 14.

### 6.1 The rule and how it is made visible

- **The request** is a card in the conversation, drawn by the agent's page
  from the agent's description. It says that something waits, in the
  agent's words, with one button, **Review…**. It cannot approve.
- **The grant** is drawn by trusted chrome from xbin's own reading of the
  request (`GET /delegate/requests/<id>`), never from anything the page
  passes except the id.
  - **Web**: `<bx-agent-approve>`, an xbind-served element owned by the
    dock (and by `/xbin/agent`). It is a sheet that **hangs from the dock's
    header and overlaps it** — a region the agent's frame cannot draw in —
    with an inverted bar "XBIN · APPROVAL — asking Ana Ruiz", a 2px
    full-contrast border, and a scrim over the agent's page. When the dock
    is closed, Review… from Needs you or a notification opens the dock
    first.
  - **iOS**: a native sheet presented by the app, with the same inverted
    bar. The app fetches the request with the person's own session.
- **Anti-spoof and anti-misclick rules** (both platforms):
  - Allow is armed 600 ms after the sheet is fully shown and only while
    nothing covers it (the rule `<bx-part-consent>` already follows).
  - Enter / the default keyboard action never allows. Focus lands on the
    heading.
  - The agent's text is always inside a quote block labelled "The agent's
    own words — not checked by xbin".
  - The sheet never renders markdown, links or images from the request.
  - One request at a time; the next shows after a decision, with "1 of 3".

### 6.2 A one-shot ticket (a tile API call)

Heading: "Your agent wants to change something in **Invoices**"

| Row | Content | Source |
|---|---|---|
| Tile | name, path, org, "you can write here" | `preview.tile`, `level` |
| Action | the method and path, mono, in full | `method`, `target` |
| Sends | the body, mono, "48 bytes · shown in full" or "showing the first 2 KiB of 14 KiB — Show all"; "not text — 3.2 MiB, sha256 …" for a binary body | `body`, `bodyShown`, `bodySize` |
| Undo | "xbin can't undo this. Whether Invoices can is up to Invoices." | fixed for tile calls |
| The agent's own words | quoted, labelled unchecked | `summary` |

**Allow** — three choices, radio rows, *Once* preselected:

1. **Once** — "This request only. The agent has 15 minutes to use it."
2. **Always, this exact action** — xbin's sentence (`preview.standing.
   call`): "POST to `…/links/rebuild` on Invoices, whatever it sends,
   without asking again."
3. **Always, every change to Invoices** — tagged **WIDER**: "Any create,
   change or delete in Invoices that you could do yourself, without asking
   again."

- Choosing 2 or 3 shows a duration toggle: "For one hour" / "Until I take
  it back". Default: "until I take it back" for the exact action, "for one
  hour" for the whole tile (Q5).
- Choosing 3 opens a **second step**: "Allow every change to Invoices?" —
  what that means in the tile's own terms ("That includes sending invoices
  to customers" when xbin can say so; otherwise the general sentence), the
  duration, and two buttons: **Go back** (primary) and **Allow every
  change** (outlined danger). On iOS this step asks for Face ID.
- Buttons: **Deny** · **Allow once** (the label follows the choice:
  "Allow always").
- Footer: "asked 10:14 · expires in 23 h 58 min".
- **Deny** returns to the conversation with the composer focused and the
  placeholder "Tell the agent what to do instead (optional)" — the exit
  that is neither yes nor no.
- Afterwards the request card in the conversation settles: "Allowed once by
  you · 10:16" + what ran and its status ("used 10:16 · 200"), or "Denied
  by you", "Expired", "Withdrawn by the agent".

Creating a tile from a template uses the same sheet with the heading
"Your agent wants to create a tile" and rows Template, Name and place,
Owner; no standing choices (asked every time).

### 6.3 Where standing approvals live

- Web: dock ⋯ → **Approvals** (xbin chrome; also on `/xbin/agent`). iOS:
  agent ⋯ → Approvals, and Settings → Workspace agent.
- One row each: the tile, the scope sentence ("Always · this action: `POST
  …/links/rebuild`" or "every change", the latter with a warn glyph), since
  when, until when, **Take back**.
- Under the list: "Starting a tile agent and creating a tile are asked
  every time. Taking an approval back stops a change that is running under
  it."
- A change made under a standing approval is marked in the conversation
  ("under 'always, this action'") and in the activity log.

### 6.4 Approving a tile agent start

Heading: "Start a tile agent in **Invoices**?"

| Row | Content |
|---|---|
| Tile | name, path, org |
| Coding agent | "Claude Code · model sonnet, through the workspace's connector" (or "on your own sign-in") |
| Mode | **asks before acting** (ok tag) — "It asks you before it edits a file or runs a command." / **acts without asking** (danger tag) — "It edits files and runs commands in Invoices without asking you." (`preview.session.asks`) |
| Network | "Its model only. No internet." / the allowed set, in words |
| Other tiles | which tile APIs it may call, with level |
| What it will be told | the prompt, in full, scrollable — "sent exactly like this" |

- Line: "Its questions and permission requests come to you, in Invoices and
  in Needs you. Your workspace agent can't answer them."
- Buttons: **Deny** · **Start**. With `asks: false`: a danger banner ("Your
  workspace agent asked for a mode that never asks. You will not get a
  chance to stop a single edit or command."), **Deny** becomes the primary
  button and the other reads **Start without asking** (outlined danger).
- No "always" here. Footer: "asked every time · change in Preferences".
- Resuming a past session uses the same sheet with "Continue the tile
  agent in Invoices?" and the session's own harness and mode.

### 6.5 Preferences (trusted chrome)

Dock ⋯ → **Preferences**; iOS ⋯ → Preferences. `GET/PUT /agent/prefs` and
`PUT /delegate/state` — the person's own session only.

- **When my agent wants to start a tile agent**: "Ask me each time"
  (default) / "Start without asking — only in a mode that asks before
  acting, with the network below. You are told in the chat."
- **Started tile agents get**: coding agent, mode (modes that never ask
  can't be chosen: "modes that never ask can't be saved here"), network,
  other tiles.
- **Admin changes** (admins only): §7.1.

---

## 7. Admin change sets

Mockups: 06 (opt-in), 07, 12.

### 7.1 The opt-in

In Preferences, shown only to workspace and org admins, off by default:

- Toggle: **Let my agent propose admin changes**
- Text: "It can read grants, bindings, org shares and limits, and propose
  changes in a batch. Nothing is applied until you approve the batch here."
- Turning it on is the person's own session only; it is said in the
  conversation as a notice ("You let your agent propose admin changes").

### 7.2 The proposal in the conversation

A request card: "**5 admin changes proposed**" — the agent's title — "1 has
a warning · 1 can't be applied" — **Review…** — "expires in 7 days".

### 7.3 The review

- **Web**: a dialog the shell owns (xbin's `<bx-agent-changeset>`), up to
  860px wide, centred over the workspace, so before and after sit side by
  side. **iOS**: a native full-height sheet, one card per change, before
  above after.
- Header: "Your agent proposes 5 admin changes", the agent's title in the
  unchecked quote block, and "Each line, its before and after and its
  warnings are written by xbin from the change itself. Nothing has been
  applied."
- **Per item**:
  - a tick box;
  - xbin's plain-language line ("Give Invoices read access to Orders") and
    the kind as a tag (grant, binding, org share, limit);
  - before → after, mono, in the diff tints;
  - effects, in a sentence ("Invoices restarts (about 3 s).", "7 people in
    Finance can read Orders. Orders comes under Finance's agent rules.");
  - warnings — a caps tag with a glyph in the danger tint and a full
    sentence, as prominent as the line: **PUBLIC** (globe), **OPEN PORT**,
    **EGRESS**;
  - "Agent's reason: '…'" quoted, last.
- **Items with a warning start unticked** (Q7). **A blocked item** is shown
  with xbin's reason ("Can't be applied. It is above Finance's ceiling for
  one tile (1024 MiB).") and can't be ticked.
- Footer: "3 of 5 ticked · applied in order as you. If one fails, later
  ones are skipped and earlier ones stay." — **Reject all** · **Apply 3
  changes…** (the count follows the ticks; with none ticked the button
  reads "Apply none" and ends the set rejected).

### 7.4 The recent-sign-in step

Only when the server answers `stepUp`:

- `password`: "**Confirm it's you.** Applying admin changes needs your
  password, or a sign-in from the last ten minutes." — a password field —
  **Continue** · **Not now**. For SSO accounts: "Use sign in again instead;
  your ticks are kept."
- `signin`: "**Sign in again first.** Applying admin changes needs a
  sign-in from the last ten minutes." — **Sign in again** · **Not now**.

The words match adding a device. On iOS the app asks for Face ID before
sending, and still shows this step if the server asks for it (Q4).

### 7.5 Results

- Title: "2 of 3 changes applied" (or "All 3 changes applied", "Nothing was
  applied").
- A warn banner when partial: "One change failed, so the one after it was
  skipped. The two that were applied stay."
- Per item, a word with a glyph: **applied** · **failed** ("xbin said:
  '…'", the handler's words) · **skipped** · **out of date** ("Someone
  changed this after the agent proposed it. What you ticked no longer
  matches, so xbin left it alone.") · **left out by you** · **couldn't be
  applied**.
- **Undo the 2 applied…** makes a new change set from xbin's inverses and
  opens it for review like any other; it does nothing by itself. Absent
  when nothing can be undone.
- **Done**. The conversation's card settles to the same title, and the
  agent is told the results.

---

## 8. Notebook, audit, pause

Mockups: 08, 16.

### 8.1 Notebook — "what the agent remembers"

- Agent's page; web ⋯ → Notebook, iOS ⋯ → Notebook.
- A list of pages: title, one line ("Reports in bullet points · never email
  customers without asking"), who last wrote it and when, and an org tag
  ("yours", "Sales", "Finance").
- The person can open, edit, add and delete any page. "Deleting a page
  makes the agent forget it."
- A page from an org outside this session's scope is listed with a lock
  tag and closed: "Not available in this session: Finance isn't connected."
- Code summaries carry the tile's revision; cautions carry who said them
  and why, and the note that the agent removes them when the reason is gone.
- In the conversation, when the agent writes a page: a notice "Noted in the
  notebook: 'How Ana likes things done'" with **Open** and **Undo**.

### 8.2 "What my agent did" — the audit log

- **Trusted chrome** (`<bx-agent-activity>`, `GET /delegate/audit`): the
  agent and its page can't read this list. Web: dock ⋯ → What my agent did.
  iOS: ⋯ → What my agent did.
- Grouped by day, newest first. Each row is a sentence with a glyph, a
  detail line and a time:
  - "Changed **Invoices**" — `POST …/links/rebuild` · you allowed it once
  - "You allowed a request" — Invoices · once
  - "Started a tile agent in **Orders**" — Claude Code · asks before acting
  - "Was refused: **Ledger**" — Finance allows private models only
  - "Applied 2 admin changes as you" — grant, binding · batch "…"
  - "Changed **Orders**" — under "always, this action"
- Filter: **Changes** (default) · **Refused** · **Everything** (adds
  reads).
- Footer: "Kept by xbin for 13 months. Your agent and its page can't read
  or change this list. It never holds what was sent, only what was called."
- An admin reading someone else's log (admin console) sees the same list
  with the person's name in the title.

### 8.3 Pause

- **Always visible**: a button in the dock header ("Pause" / "Resume"); on
  iOS the last item of the ⋯ menu ("Pause my agent"), and Resume as the
  one button that replaces the composer.
- Pausing is immediate and needs no confirmation (it is the safe
  direction). Resuming is the person's own click in trusted chrome.
- While paused: a warn banner, "**Paused by you at 10:31.** Your agent
  can't read or change anything in the workspace until you resume. Tile
  agents that are already running keep going; their questions still come
  to you." The composer is disabled ("Resume to send a message") (Q10).
- The top-bar chip and the iOS button show a pause glyph instead of the
  count.

---

## 9. Mobile specifics

Mockups: 11–16.

### 9.1 One-handed reach

- The bottom third holds everything tapped often: composer, mic/send,
  quick replies, answer and approval buttons, the context chip.
- The header is for reading (session, scope chip) and the ⋯ menu. The
  scope sheet's Apply and every approval's Deny/Allow are at the bottom.
- Deny left, Allow right, equal width. Destructive-direction buttons are
  never the one nearest the thumb's resting place alone.
- The entry button is at the top (the bar is the app's one consistent
  place, and on the Duo it becomes the side bar). To make up for the reach:
  the Home row, the Live Activity and notifications all open the agent, and
  an App Intent ("Ask the workspace agent") makes it available to the
  Action button, Siri, Spotlight and a Control Center control (Q1).

### 9.2 Dictation, quick replies, the keyboard

- **Dictation**: the mic sits where Send will be while the composer is
  empty. Tap to start; a level meter and a timer replace the field; the
  words appear as a dashed **draft** in the transcript area; tap the check
  to stop. The text lands in the composer to edit or send. Nothing is sent
  by voice alone. On-device recognition only; if it isn't available the mic
  is hidden and the keyboard's own dictation remains.
- Long, rambling dictation is fine: the agent answers with a brief or a
  question card, which is where the request gets its shape.
- **Quick replies**: a row above the composer for the choices of the
  newest question, "Go ahead / Change the plan", and next steps after a
  report. They stay visible with the keyboard down and scroll sideways.
- **Keyboard**: the composer rides it; the existing "Hide keyboard" button
  is kept; dragging the transcript dismisses it. Focusing the composer at
  the medium detent raises the sheet to large. An approval sheet never
  appears with the keyboard up (it is dismissed first).

### 9.3 Notifications

| When | Title — body (xbin's words, never the agent's) | Actions |
|---|---|---|
| An approval waits (`agent.approval`) | "Approval needed — Invoices" — "Your workspace agent wants to change something in Invoices. Open to see exactly what." | **Review…** (opens the app's sheet after unlock) · **Deny** |
| A change set waits (`agent.changeset`) | "Admin changes proposed" — "Your workspace agent proposes 5 admin changes. Nothing is applied until you approve." | **Review…** |
| A tile agent it started waits | the existing "Permission needed — Invoices" / "Question — Invoices" | Open |
| The agent asks the person something | "Question from your workspace agent" — no body text | Open |
| A request is finished | "Workspace agent finished" — "The report is ready." | Open |

- **No Allow on the lock screen** (Q3): the preview there can't show the
  call. Deny is safe and stays.
- One push per waiting thing and one per finished request; none per turn.
  Pushes for the same request collapse into one.
- Tapping any of them opens the agent sheet on the right session, with the
  approval already presented when that is what waits.

### 9.4 Live Activity

- One per workspace, started when the agent has tile agents running, built
  on the existing agent activity and its words.
- **Lock screen**: "Workspace agent · Northwind" — "**3 tile agents
  working · 1 waiting for you**" — one bar per tile agent (orange for
  waiting, green for finished) — "Started 12 min ago" — **Open**. Counts
  only: no tile names, no request text.
- **Dynamic Island, compact**: the agent glyph with the working count;
  on the other side a hand glyph with the waiting count (absent when zero).
- **Expanded** (unlocked): a row per tile agent with its state; the first
  button goes to the oldest thing that waits ("Answer Invoices"), the
  second opens the agent.
- **End**: "All 3 tile agents finished — The report is ready" for a few
  minutes, then it is replaced by the one "finished" notification. A
  failure ends on "2 finished · 1 failed".
- Needs xbind to send Live Activity updates over APNs (§13.6).

### 9.5 Handoff

- The agent session is an `NSUserActivity` ("Workspace agent — Main"):
  picking it up on a Mac opens the shell with the dock on that session (the
  D220 link with the dock open), or `/xbin/agent` on an old scaffold.
- A change set on iPhone shows "Easier on a bigger screen — it's ready on
  your Mac" when Handoff is available; the phone review still works.
- ⋯ → "Continue on the web" does the same with a web ticket.

### 9.6 Offline and the tunnel

- A warn banner under the header: "**Can't reach Northwind.** Showing what
  was loaded at 10:31. Trying again…" with the tunnel's existing status
  line under it ("Tunnel · not connected", "Tunnel · relay (host) · 85
  ms").
- The transcript stays readable. The draft stays in the composer with
  "Kept here. Not sent until you're connected." — no silent queue: a
  message sent later than the person thinks is worse than one not sent.
- Approval cards are greyed with "Approvals need a connection."
- On a slow relay nothing is said unless sending takes over 3 s; then the
  message shows "sending…" with a time.
- Coming back refetches the conversation, the requests and the connection
  view; the banner goes without a toast.

---

## 10. States and errors

Mockups: 09, 10, 16.

Every state says: what is wrong, that nothing is lost (when true), what
still works, and who or what fixes it.

### 10.1 Not configured (no language model)

- **A non-admin, before setup**: no button, nothing. (Plan §7: an upgrade
  changes nothing a user sees.)
- **An admin, before setup**: the top-bar chip reads `set up workspace
  agent`. The dock shows "Set up the workspace agent" — "One agent per
  person that plans work across tiles and asks before anything changes. It
  is off until you finish these three steps. Nobody else sees it yet." —
  1. **Connect a language model** — "None is connected. Use a provider
     key, or a model your organisation runs itself." — Connect… (the LLM
     section)
  2. **Turn workspace agents on** — "Off. You can turn it off again at any
     time; every agent stops at once."
  3. **Choose who gets one** — "Everyone, or the people and orgs you pick.
     Orgs can add their own rules."
  Each step shows a check when done; later steps are disabled until the
  one before is.
- **A hosted workspace**: step 1 is done and tagged **Set by your host**;
  "Your host provides the models and sets the limits; those can't be
  changed here."
- **Anyone, when the model goes away later** (the agent was on): "**Not
  available right now.** The workspace agent needs a language model, and
  this workspace has none connected at the moment. Your earlier
  conversations and the notebook are kept." — "An admin can fix this: Mira
  Okafor or Jonas Lind." — **Copy a message for an admin** · **Check
  again**.
- **iOS, admin before setup**: the same three steps, read-only, with
  "**Open the setup on the web**" and the Handoff line. Setup is not built
  natively in v1.

### 10.2 The rest

| State | What the person sees | Controls |
|---|---|---|
| **Quota reached (stops)** | banner: "**Today's limit is used up.** You've used your 2M tokens for today. It resets at 01:00. You can still read everything here; tile agents on your own sign-in keep working." Composer disabled: "Sending resumes at 01:00". Hint: "An admin can raise your limit: …" | an admin sees **Raise my limit…** |
| **Quota (warns only)** | a one-line notice in the conversation: "You're past today's budget for AI use. An admin has been told; nothing is stopped." | — |
| **Policy refusal** | a card with the lock glyph: "**Blocked by Finance's rules** — Finance allows private models only. This session is on **big**, which is not private, so Ledger isn't connected." + "This is a rule set by a workspace admin, not something the agent can work around." | **See what a private model connects…** · **Open scope** |
| **A tile says no to agents** (`"agent": "off"`/`"read"`) | the same card: "**Ledger doesn't let agents change it.** Its makers set that. You can still do it yourself in Ledger." | **Open Ledger** |
| **Paused** | §8.3 | **Resume** |
| **Turned off by an admin** | "**Turned off for this workspace.** An admin turned workspace agents off at 09:12. Nothing an agent started is running on your behalf. Your conversations and notebook are kept for when it is turned on again." | — |
| **Sealed component held: locked after a restart** | "**Locked after a restart.** Your agent's conversations and notebook are encrypted, and the workspace was restarted. They stay locked until an admin unlocks the workspace. Nothing is lost." + admins' names | **Check again**; an admin sees **Unlock…** |
| **Sealed component held: older xbin than the data** | "**Waiting for a newer xbin.** This workspace is running an older xbin than the one that last saved your agent's data. To keep that data safe the agent stays off until xbin is updated again. Nothing has been changed or removed." + a meta line with the two versions for an admin | — |
| **xbin is updating** | info banner: "**xbin is updating.** Back in about a minute. Running tile agents are stopped and continue afterwards." Tile agent cards: "stopped for the update". Composer: "Reconnecting…" | — |
| **After the update** | a notice: "**xbin was updated at 14:02.** 2 of 3 tile agents continued. Team calendar's could not." The failed card: "couldn't continue — Its transcript is kept. Open it to see where it stopped." Then the agent offers the next step as a choice. | **Yes, start it again** · **No, leave it** |
| **No connection** | §9.6 (the web has the same banner with **Try now**) | — |
| **A coding agent needs a sign-in** | the kit's sign-in card (`<bx-agent-signin>`), in the conversation, naming the harness and the tile | **Sign in** |
| **Too many requests** (the 30-acts-a-minute limit) | a quiet line: "Slowing down — the agent is doing a lot at once." No action needed. | — |
| **A request expired** | the card settles to "Expired — not approved within 24 hours." | **Ask again** (a message to the agent) |

---

## 11. Accessibility

### 11.1 Contrast and colour

- Only theme tokens, in both themes: body text ≥ 4.5:1, UI parts ≥ 3:1
  (the theme's own guarantee). The mockups use no colour outside the token
  table except the lock-screen wallpaper and the system's Dynamic Island.
- No state is colour alone: every status is glyph + word; warnings are a
  tag with a glyph *and* a sentence; the wider grant is the word "WIDER";
  the trusted sheet is an inverted bar with the words "XBIN · APPROVAL".
- Before/after never relies on red/green: the two boxes are in fixed
  positions (left/right, or above/below on iPhone) with an arrow and are
  read as "before … after …".
- Focus is the theme's 3px cyan ring with a 2px gap on every control,
  including tree rows and choice rows.

### 11.2 Zoom and Dynamic Type

- Web: the dock works at 200% browser zoom (it becomes the full-width
  layout under 640 CSS px); nothing in an approval is clipped — the sheet
  scrolls, and its buttons stay pinned.
- iOS: every text style scales with Dynamic Type through the accessibility
  sizes. At large sizes: the scope chip stacks its segments; key/value rows
  stack label above value; Deny and Allow stack vertically (Allow below);
  tile agent cards put the button on its own row; the four totals become
  two rows.
- Minimum touch target 44pt; choice rows are full width.

### 11.3 Screen readers

- **The entry button**: "Workspace agent, 2 need you" / "Workspace agent,
  paused".
- **The scope chip**: one element — "Scope: 14 tiles in 3 orgs connected, 6
  not connected. Model big. Button."
- **The tree**: org rows are "Sales, 5 of 6 connected, partly selected,
  collapsed"; tile rows "Ledger, blocked: Finance allows private models
  only, dimmed".
- **The approval sheet** is a modal dialog labelled by its heading, and is
  announced as "xbin approval". Reading order: heading → Tile → Action
  (method and path read in full) → Sends → "xbin can't undo this" → "The
  agent's own words, not checked by xbin: …" → the three choices as a radio
  group (the third announces "wider") → Deny → Allow. The Allow button's
  label includes the choice: "Allow once", "Allow always, this exact
  action", "Allow every change to Invoices". While it is not yet armed it
  is announced as dimmed, then "Allow once, button" when armed.
- **A change set**: each item is one group — "Change 3 of 5, not selected.
  Publish Orders' export at export.northwind.example. Warning, public:
  anyone on the internet can reach … Before: not connected. After: host
  …". The Apply button reads its count.
- **Live regions**: a new request, a status change of a tile agent card
  and the end of a turn are announced politely, once; progress lines are
  not announced one by one.
- **Keyboard (web)**: the dock is a landmark; `F6` cycles shell regions
  into it; the tree follows the ARIA tree pattern (arrows, space to tick);
  the trusted sheet traps focus and returns it to the request card.

### 11.4 Motion

- Nothing loops or pulses. Working is "working 4:10".
- The dock and the sheets slide in the theme's durations; with reduced
  motion they appear without sliding, and the 600 ms arming delay stays
  (it is a safety rule, not an animation).
- The dictation meter is the one moving element; with reduced motion it is
  a static "Listening… 0:07".

### 11.5 Where web and iOS deliberately differ

| | Web | iOS |
|---|---|---|
| Container | a dock that pushes the canvas | iPhone: a sheet with detents; iPad/Duo: a second pane |
| Entry | a text chip in the top bar | a glyph plate in the panel bar / the Duo's vertical bar; plus Home row, App Intent |
| Needs you | a tray in the dock (new) | the existing inbox sheet, with two new kinds |
| The grant | xbin's sheet hanging from the dock header; change sets in a shell dialog | native sheets; Face ID for the wider grant and for admin changes |
| Change set layout | before → after side by side | before above after, one card per item |
| Scope | a pop-over in the dock | a sheet inside the agent sheet |
| Pause | a header button, always visible | ⋯ menu to pause; Resume replaces the composer |
| Voice | the browser's own | a mic in the composer |
| Away from the screen | the top-bar count | pushes, a Live Activity, Handoff |
| Setup | the three steps act | the three steps are read-only and point to the web |
| Capitalisation of status words | lower case, as the Agent tab | as the app has them ("Working", "Waiting for you") |

---

## 12. Mockups

All under `/tmp/claude-1000/wsagent/ux/`; screenshots in `shots/` as
`<name>.{desktop,phone}.{night,day}.png`. `_src/` holds the generator
(`build.mjs`, `shots.cjs`).

| File | Shows |
|---|---|
| `index.html` | links to everything |
| `01-web-dock-conversation.html` | the dock with a conversation in progress |
| `02-web-empty-state.html` | the empty state and starters |
| `03-web-nudges.html` | vague / low-level / impossible requests, the brief, the plan, a report |
| `04-web-scope-panel.html` | the chip, the panel, the what-if, a refused model change |
| `05-web-ticket-approval.html` | a ticket approval, the wider step, standing approvals, the settled card |
| `06-web-session-start-approval.html` | a session-start approval, the never-asks variant, Preferences |
| `07-web-change-set.html` | a change-set review, the sign-in step, partial results, an out-of-date item |
| `08-web-sessions-notebook-audit.html` | sessions, notebook, activity log, Needs you, paused |
| `09-web-not-configured.html` | not configured: admin, non-admin, hosted |
| `10-web-states.html` | quota, policy, updating, locked, older xbin, offline, turned off |
| `11-ios-entry-chat.html` | iPhone: Home, the sheet large and medium, empty state, dictation, questions |
| `12-ios-approval.html` | iPhone: request, ticket, wider grant, session start, change set, results |
| `13-ios-scope.html` | iPhone: scope, model what-if, refused change, sessions |
| `14-ios-duo-ipad.html` | the open Duo (two panes + vertical bar), an approval on it, iPad |
| `15-ios-live-activity.html` | lock screen, notification actions, Dynamic Island, finished |
| `16-ios-states.html` | iPhone: the ⋯ menu, paused, offline, updating, quota, admin before setup |

The phone-width screenshots of the web pages show the narrow layout of the
dock (and of `/xbin/agent`); the phone-width screenshots of the iOS pages
show each phone screen at 1:1.

---

## 13. Components to build

### 13.1 Web — xbind-served (`/vendor/`), trusted chrome

These hold the person's own session and are the only things that grant.
They reach old scaffolds through `/xbin/agent`.

| Element | Does |
|---|---|
| `<bx-agent-button>` (`bx-agent-button.js`) | the top-bar chip and its count; the one shell hook (one tag in the scaffold's top bar) |
| `<bx-agent-dock>` (`bx-agent-dock.js`) | the dock: resize, header (title, Needs you, Pause, ⋯, close), the frame of the agent's page, the host of every trusted overlay |
| `<bx-agent-needs>` | the Needs you tray: requests, change sets, sessions that wait, the agent's questions |
| `<bx-agent-approve>` | the ticket sheet, the wider step, the session-start and tile-create sheets |
| `<bx-agent-changeset>` | the change-set dialog: review, the sign-in step, results, undo |
| `<bx-agent-approvals>` | standing approvals, Take back |
| `<bx-agent-activity>` | "What my agent did" |
| `<bx-agent-prefs>` | start preferences, the admin opt-in |
| `<bx-agent-setup>` | the three setup steps and the "not available" state (it must render with no agent component running) |
| `/xbin/agent` | a page composing the above around the agent's frame |

Also xbind-served but **used inside the agent's page** (not trusted, shared
so `/xbin/agent`, the dock and AgTT agree): `<bx-agent-scope>` (the chip,
the panel, the model what-if — it calls `GET /agents/connection` itself)
and the chat kit (`/vendor/chat/*`), `<bx-agent-signin>`.

### 13.2 Web — the agent's own page (the sealed component's frontend)

On the chat kit, in workspace mode. It only reads delegation state and may
pause.

- transcript, composer with the context chip and the standing hint;
- the empty state and starters;
- the session name and list, the scope chip's host strip;
- blocks of its own, drawn through `ChatCtx.other` (no kit change needed):
  `brief` (editable), `wsession` (a tile agent card), `request` (state of a
  delegate request + Review…), `changeset` (summary + Review…), `redirect`,
  `cannot`, `refusal` (policy), `report`;
- the kit's own `plan`, `ask` (choices + "Other" already exist, M11),
  `notice`, `signin`;
- the notebook view;
- banners for quota, paused, updating, offline.

### 13.3 Messages between the page and the dock (to specify in package D)

Proposed names; they are new protocol surface and go into
`docs/protocol.md` when built.

| Message | Direction | Means |
|---|---|---|
| `xbin:agent-review {kind, id}` | page → dock | "show the approval for this request / change set". The dock fetches it itself; an unknown id shows nothing |
| `xbin:agent-context {tile?, screen?}` | dock → page | the tile or screen in focus when the dock opened or the focus changed |
| `xbin:agent-open {view}` | page → dock | open Approvals, Activity or Preferences (links from the conversation) |
| `xbin:open-session {tile, session}` | page → shell | exists (D229) |
| `delegate` events | xbind → both | exist: refetch what they name |

The page learns that a request was decided from the `delegate` event, not
from the dock.

### 13.4 iOS — native app chrome

- the entry item in `PanelBar` (vertical-preferred), its count, the Home
  row, the App Intent;
- the agent container: the sheet with detents and background interaction
  (iPhone), the trailing column (iPad), the root second pane (Duo);
- **every grant**: the ticket, wider-step, session-start, tile-create and
  change-set sheets, the sign-in step, results; Face ID gates;
- the inbox's two new kinds; Approvals; "What my agent did"; Preferences;
  Pause/Resume;
- the scope sheet (totals, tree, model what-if, refusal) — native, fed by
  `GET /agents/connection`, writing the choice back through the tile's
  action (below);
- dictation in the composer; the offline/updating banners;
- the Live Activity (extending `AgentActivityWidget`), notification
  categories `xbin.agent.approval` and `xbin.agent.changeset`, Handoff.

### 13.5 iOS — through the native vocabulary, and the additions it needs

The conversation itself comes from the sealed component's `native.js` and
is drawn by `XbinRenderer/Chat`. Today's vocabulary can't express seven
things the design needs. Proposed additions (each behind a rev or feature
flag, with a fallback an older app can draw):

| # | Addition | For | Fallback on an older app |
|---|---|---|---|
| V1 | `request` transcript node `{id, kind: "call"\|"session"\|"template"\|"changeset", text}` — **the app** draws its state and the Review… button and presents its own sheet | approvals (the trusted boundary: the tile supplies an id and its own sentence, never buttons) | a `notice`: "Something waits for your approval. Update the app to review it." |
| V2 | `session` transcript node `{tile, session, provider, status, line}` whose action is the app's own deep link to the native agent screen (`xbin://<ws>/agent/<id>` exists) | tile agent cards with Open | a `notice` with the tile's name |
| V3 | `question` rev 2: choices as rows (`style: "rows"`), `other: true` (free text riding on the question), several questions paged, `skip` | question cards | rev 1's menus |
| V4 | `card` transcript node holding `text`/`markdown`, `field`s and `actions` | the editable brief; redirect and "can't" cards; the report's buttons | `markdown` + composer chips |
| V5 | `connection` node `{summary, counts, model, scope}` pinned above a transcript, with `@scope`/`@model` actions — the app opens its native scope sheet and sends the choice back | the scope chip and panel | a `toolbar` badge with the summary |
| V6 | `composer` rev 2: `context: [{id, label, icon}]` with `@remove`, `quickReplies: [{label, send}]`, `dictation: true` | the context chip, quick replies, the mic | chips as child buttons (today's way) |
| V7 | `transcript` rev 3: an `empty` slot (heading, text, two lists, `suggestions: [{kind, text, fill}]`) | the empty state and starters | `empty` + buttons |

A generic tri-state tree is **not** needed in the vocabulary: the scope
sheet is app chrome (V5). The plan card, notices, tool cards, thinking,
steps and the composer exist. `approval` (rev 1) stays for AgTT's own
approvals and is never used for delegation.

### 13.6 What this design asks of other packages

Not built here; each is a gap between the mockups and what exists.

- **Needs-you sources.** Requests, change sets and waiting sessions are
  xbind's and live over `/ws/events`. The agent's *own* open questions live
  in the component; the count must reach the button while the dock is
  closed (a tile report or a small route). The owner of package C decides
  which.
- **`/whoami`** (or `GET /delegate/state`) should carry enough for the
  button: has an agent, set up or not, paused, the count.
- **Admins' names** for "An admin can fix this" — a route a non-admin may
  call, or the sentence falls back to "an admin".
- **Starters** need a cheap, side-effect-free call in the component that
  returns three or four suggestions from tiles and alerts.
- **Live Activity pushes** from xbind (ActivityKit push tokens per device
  and workspace; start, update and end events on session status changes).
- **`agent.approval` / `agent.changeset` pushes** exist in the contract;
  the app needs the two categories and the Deny action's route
  (`POST /delegate/requests/<id>/deny` from a notification action).
- **Request preview**: the sheet would like `reversible` and, for a tile
  that documents its API, a one-line description of the route from the
  tile's `API.md` — shown as "Invoices says: rebuilds customer links". Not
  required for v1; without it the Action row is the path alone.
- **The Agent tab** should take a draft when opened from a redirect card
  (`open({session: "new", draft})`), so "hand this to Orders' agent" keeps
  the words.
- **A draft-preserving sign-in** for the step-up when the account is SSO.

---

## 14. Copy deck

Every string above, gathered for review and for the catalogs. Sentence
case; no exclamation marks; "xbin" for the product, "your agent" / "the
workspace agent" for the agent, "tile agent" for a coding agent session in
a tile; "connected" for scope; "approve" for a request, "allow" on the
buttons.

**Entry.** `workspace agent` · `set up workspace agent` · "Workspace
agent" · "Workspace agent, 2 need you" · Home row: "working · 2 tile
agents · 2 need you" / "idle" / "paused".

**Composer.** "Ask for an outcome…" · "Reply, or ask for something else…"
· "Say what you want and how you'll know it's done. The agent works out
the steps." · "About **Orders**" · "About the **Sales desk** screen" ·
"About **Ledger** — not connected" · "Tell the agent what to do instead
(optional)" · "Resume to send a message" · "Sending resumes at 01:00" ·
"Reconnecting…" · "Kept here. Not sent until you're connected." · Send.

**Empty state.** "Ask for an outcome" · "The workspace agent plans the
work, hands building to each tile's own agent, and asks you before
anything changes." · "It can" · "Look across your 14 connected tiles" ·
"Start tile agents and keep track of them" · "Do things in a tile for you,
once you approve" · "Remember what you tell it" · "It can't" · "Change
code itself — a tile's agent does" · "Answer a tile agent's questions for
you" · "See the 6 tiles that aren't connected" · "Use the web" · "Try one
— from your tiles" · Ask · Fix · Change · New.

**Nudges.** "Which one do you mean?" · "Something else" · Answer · "Skip
— let the agent look around first" · "The workspace agent doesn't open or
edit code. Two ways forward:" · "Hand this to Orders' own agent — It opens
in Orders with your message as written." · "Tell me the outcome instead"
· "Can't do this" · "Only you can, from the tile's ⋯ menu." · "It only
works inside this workspace." · "What can it do?"

**Brief and plan.** "Brief for Orders' agent" · Goal · Done when · Leave
alone · Check by · Edit · "Review and start…" · "Claude Code · asks before
acting" · "Plan" · "reads only" · "asks you" · "needs your OK" · "Go
ahead" · "Change the plan" · "Saying go ahead starts step 1. It does not
approve steps 2–4: each asks when it is reached." · "you said go ahead ·
10:02".

**Progress and tile agents.** "Started Claude Code in Orders · 10:03" ·
"working 11:42 · 2 tile agents" · "Worked for 12 min · 6 steps" ·
starting · working · waiting for you · finished · failed · stopped ·
"stopped for the update" · "couldn't continue" · "Asks: may I run the
export tests? Only you can answer." · Open · Answer · "started by your
agent" · "Checked:" · "Not checked:".

**Scope.** "14 tiles · 3 orgs connected · 6 not connected" · "14
connected · 6 not" · "What this session is connected to" (iOS: "Connected
to") · connected · hidden by you · blocked · no access · "Model" ·
"hosted · not private" · "private" · "Change…" · All · Connected · "Not
connected" · "Select all allowed" · "5 of 6" · "Workspace — tiles in no
org" · "See what a private model would connect…" · "Scope is where this
session looks. It is not a permission — blocked stays blocked whatever you
tick." · Apply · Cancel · "Change this session's model" · "If you switch
to local-llama" · "Connects 4 tiles" · "Disconnects none" · "Switch
model" · "Switch and disconnect 3" · "This session can't move to big" ·
"Start a new session on big" · "Stay on local-llama".

**Sessions.** Main · "always here" · "New session…" · Rename · Archive ·
Older · "A new session starts empty, with its own scope and model. The
notebook is shared by all of them, within each one's scope."

**Needs you.** "Needs you" · "oldest first · approvals expire" · "Approve
a change to Invoices" · "5 admin changes proposed" · "Invoices · Claude
Code asks permission" · "The agent asked you a question" · "Review…" ·
"Nothing needs you".

**Approvals.** "XBIN · APPROVAL" · "asking Ana Ruiz" · "Your agent wants
to change something in Invoices" · "Your agent wants to create a tile" ·
Tile · Action · Sends · Undo · "you can write here" · "shown in full" ·
"xbin can't undo this. Whether Invoices can is up to Invoices." · "The
agent's own words — not checked by xbin" · Allow · "Once — This request
only. The agent has 15 minutes to use it." · "Always, this exact action"
· "Always, every change to Invoices" · WIDER · "For one hour" · "Until I
take it back" · Deny · "Allow once" · "Allow always" · "Allow every
change to Invoices?" · "From now on your agent can create, change and
delete anything in Invoices that you can, without asking." · "It ends by
itself after the time you pick. You can take it back sooner in
Approvals." · "Go back" · "Allow every change" · "asked 10:14 · expires
in 23 h 58 min" · "Waiting for your approval" · "Allowed once by you" ·
"Denied by you" · "Expired — not approved within 24 hours." · "Withdrawn
by the agent" · "under 'always, this action'" · Approvals · "3 standing"
· "Take back" · "Starting a tile agent and creating a tile are asked
every time. Taking an approval back stops a change that is running under
it."

**Starting a tile agent.** "Start a tile agent in Invoices?" · "Continue
the tile agent in Invoices?" · "Coding agent" · Mode · "asks before
acting" · "It asks you before it edits a file or runs a command." · "acts
without asking" · "It edits files and runs commands in Invoices without
asking you." · Network · "Its model only. No internet." · "Other tiles" ·
"What it will be told — sent exactly like this" · "Its questions and
permission requests come to you, in Invoices and in Needs you. Your
workspace agent can't answer them." · "Your workspace agent asked for a
mode that never asks. You will not get a chance to stop a single edit or
command." · Start · "Start without asking" · "asked every time · change
in Preferences".

**Preferences.** "When my agent wants to start a tile agent" · "Ask me
each time" · "Start without asking" · "Only in a mode that asks before
acting, with the network below. You are told in the chat." · "Started
tile agents get" · "modes that never ask can't be saved here" · "Let my
agent propose admin changes" · "It can read grants, bindings, org shares
and limits, and propose changes in a batch. Nothing is applied until you
approve the batch here."

**Change sets.** "XBIN · ADMIN CHANGES" · "Your agent proposes 5 admin
changes" · "The agent's title — not checked by xbin" · "Each line, its
before and after and its warnings are written by xbin from the change
itself. Nothing has been applied." · grant · binding · org share · limit
· PUBLIC · "OPEN PORT" · EGRESS · "Can't be applied." · "Agent's reason:"
· "3 of 5 ticked · applied in order as you. If one fails, later ones are
skipped and earlier ones stay." · "Reject all" · "Apply 3 changes…" ·
"Confirm it's you" · "Applying admin changes needs your password, or a
sign-in from the last ten minutes." · "Sign in again first." · Continue ·
"Not now" · "2 of 3 changes applied" · "All 3 changes applied" · "Nothing
was applied" · "One change failed, so the one after it was skipped. The
two that were applied stay." · applied · failed · skipped · "out of date"
· "left out by you" · "couldn't be applied" · "xbin said:" · "Undo the 2
applied…" · Done.

**Notebook, activity, pause.** Notebook · "what the agent remembers" ·
"Add a note" · yours · "Not available in this session: Finance isn't
connected" · "You can open, edit or delete any page. Deleting a page
makes the agent forget it." · "Noted in the notebook:" · "What my agent
did" · Changes · Refused · Everything · "Changed Invoices" · "You allowed
a request" · "Started a tile agent in Orders" · "Was refused: Ledger" ·
"Applied 2 admin changes as you" · "Kept by xbin for 13 months. Your
agent and its page can't read or change this list. It never holds what
was sent, only what was called." · Pause · Resume · "Pause my agent" ·
"Paused by you at 10:31. Your agent can't read or change anything in the
workspace until you resume. Tile agents that are already running keep
going; their questions still come to you."

**Mobile.** "Listening… 0:07" · "tap ✓ to stop, then edit or send" ·
"Hide keyboard" · "Approval needed — Invoices" · "Your workspace agent
wants to change something in Invoices. Open to see exactly what." ·
"Admin changes proposed" · "Question from your workspace agent" ·
"Workspace agent finished" · "The report is ready." · "3 tile agents
working · 1 waiting for you" · "All 3 tile agents finished" · "2 finished
· 1 failed" · "Answer Invoices" · "Open agent" · "Read the report" ·
"Continue on the web" · "Easier on a bigger screen — it's ready on your
Mac" · "Can't reach Northwind. Showing what was loaded at 10:31. Trying
again…" · "Approvals need a connection."

**States.** Section 10's sentences are the copy.

---

## 15. Open questions for the owner

Each with the recommendation the mockups already follow.

| # | Question | Recommendation |
|---|---|---|
| Q1 | **iPhone: a sheet or a pushed screen; and the button at the top or a floating one at the bottom?** | A sheet with medium and large detents, opened large (§1.2). The button in the panel bar, trailing-most: it is the one place every panel has, it becomes the Duo's bar item for free, and a floating button would sit on top of tiles' own controls. Reach is covered by the Home row, the Live Activity, pushes and an App Intent for the Action button. |
| Q2 | **iPad "sidebar"**: the app has none. Build one for this? | No. The same bar item, opening a trailing column. A sidebar is a navigation redesign of its own. |
| Q3 | **Allow from the lock screen or a Live Activity?** | No in v1: Review… and Deny only. The call can't be shown there, and an approval nobody could read is not an approval. Revisit for "once" approvals whose whole body fits a notification. |
| Q4 | **Face ID** for "every change to a tile" and for applying admin changes on iOS; should it ever count as the server's recent sign-in? | Yes to the local gate now. Counting as the server's step-up needs a device-bound key the server can verify — later, as its own change. |
| Q5 | **"For one hour"**: the contract has `hour` and `always`. Offer both, and which is the default? | Both, as a toggle that appears once a standing choice is picked. Default "until I take it back" for the exact action, "for one hour" for the whole tile. |
| Q6 | **Answering a tile agent's permission from the dock / the Live Activity** (it is the person's own session, so trusted chrome could). | Not in v1: Open goes to the session, where the context is, and there is one permission card to keep right. Add inline answers once the kit's card is shared by the tray. |
| Q7 | **Change-set items with a warning start unticked.** | Yes. "Approve all" must never publish something by default. |
| Q8 | **Starters fill the composer instead of sending.** | Fill. It costs one tap and teaches the shape of a request; "Ask" starters could send directly later if people find the extra tap annoying. |
| Q9 | **The scope chip and panel are the agent page's**, not trusted chrome (an xbind-served element used inside the page). | Yes: scope is focus, not permission, and the page must know it anyway. The panel says so in words. The model's "private" tag and every "blocked" come from xbind. |
| Q10 | **While paused, may the person still type to the agent** (it could answer from memory, with no tools)? | No: the composer is disabled and says "Resume to send a message". One meaning for pause. |
| Q11 | **A web shortcut** to open the dock. | `Ctrl+J` / `⌘J` if free in the shell (Linear uses it for the same thing); otherwise none in v1. |
| Q12 | **The top-bar label in lower case** (`workspace agent`), like `settings` and `docs`. | Yes in the bar; "Workspace agent" everywhere else. |
| Q13 | **Non-admins see nothing before setup.** | Yes (plan §7). An admin who wants to announce it does so themselves. |
| Q14 | **One Live Activity per workspace**, and it needs xbind to send ActivityKit pushes. | Yes to one per workspace; schedule the push work with package E. Until it lands the activity updates only while the app is in the foreground or woken by an ordinary push. |
| Q15 | **Admins' names** in "an admin can fix this". | Show names (first two, "and 3 more") where the person can already see who the admins are; otherwise "an admin". |
| Q16 | **Dictation on-device only**, a new microphone and speech permission for the app. | Yes; the permission is asked on the first tap of the mic, with the reason in plain words. |
| Q17 | **Session limits**: is there a maximum number of sessions? | No hard cap in the UI; archived ones fold away. Main can't be archived. |
| Q18 | **The wider grant's "what this means" sentence** ("That includes sending invoices to customers") needs the tile to describe itself. | v1 uses the general sentence only; a tile-supplied line is shown as the tile's words ("Invoices says: …") when the manifest gains one. |

---

## 16. Sources of the product research

Read on 2026-11-15 by a research sub-agent; re-read before quoting.

- Claude Dispatch / Cowork: `claude.com/docs/cowork/guide/dispatch`,
  `support.claude.com/en/articles/13947068`, `…/13345190`; Claude Code
  agent view: `code.claude.com/docs/en/agent-view`.
- Codex in ChatGPT mobile: `openai.com/index/work-with-codex-from-anywhere/`,
  `learn.chatgpt.com/docs/mobile`, `…/docs/remote-connections`,
  `developers.openai.com/blog/mastering-codex-remote-for-engineering`. Live
  Activities for Codex were only found in secondary sources (unverified);
  approving from one is an open request (`github.com/openai/codex/issues/60079`).
- Devin: `docs.devin.ai/release-notes/overview`,
  `docs.devin.ai/work-with-devin/advanced-capabilities`,
  `cognition.com/blog/devin-2-1`.
- Replit: `docs.replit.com/features/agent/plan-mode`,
  `…/features/agent/task-board`, `…/features/platforms/mobile-app`.
- Lovable: `docs.lovable.dev/features/plan-mode`,
  `…/features/projects/chat`, `…/integrations/lovable-mobile-app`.
- Linear: `linear.app/developers/agent-interaction`,
  `linear.app/developers/agent-signals`, `linear.app/docs/linear-agent`,
  `linear.app/now/how-we-built-linear-agent`.
- Cursor: `cursor.com/docs/cloud-agent/mobile`,
  `cursor.com/docs/release-notes/cloud-agents`.
