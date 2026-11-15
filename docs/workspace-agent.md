# The workspace agent

The **workspace agent** is one long-lived agent per person that works at a high level across the whole workspace: it looks at what its person can see, plans, hands building to each tile's own agent, does things in a tile for its person once they approve, keeps a notebook, and reports back. It acts with its person's access and never more, and everything with a side effect is approved by that person in xbin's own chrome, where xbind — not the agent — keeps and checks the approval.

This page is for the people who run a workspace and for tile authors: what the agent is, what it reaches, how its approvals work, what a tile should declare, and what the language model's provider sees. The rules behind it are in [auth.md §Delegation](/docs/auth.md#delegation-a-persons-workspace-agent); the routes in [protocol/delegation.md](/docs/protocol/delegation.md); the org rules in [protocol/orgs.md](/docs/protocol/orgs.md); the connector in [llm.md](/docs/llm.md).

## What it is

- **A sealed component at `xbin/wsagent`.** Its code ships inside xbind and is always the running release's: nothing of it is copied into the workspace, so there is no instance to merge, no terminal, no code tab. It is the agent template's backend (`builtin-templates/agent`) started in *workspace mode* (`XBIN_AGENT_MODE=workspace`, set by xbind from its own table) — the same engine, with one profile, one fixed class and a closed list of routes.
- **One partition per person.** Each person's agent is their own partition: its conversations, its notebook and its settings are in that person's encrypted data, and it calls xbind as that person and nobody else.
- **Sessions.** A person has one default session, **Main**, made on first use: long-lived, renamable, never archived. They can open more, each with its own name, scope (which orgs and tiles it looks at) and model. Old turns are compacted, and the agent can search and re-read them. A session never carries a conversation to a model that an org whose data it has read doesn't allow: the change is refused, and a new session is offered.
- **Off until set up.** Nobody has an agent until an admin connects a language model ([llm.md](/docs/llm.md)), turns workspace agents on (`PUT /api/xbin/delegate/workspace`) and gives people read access to the component. A person pauses their own agent at any time; pausing stops its every call at once.

## What it can reach

Everything goes through xbind's delegation, as the person, under the delegation cap — a closed list:

| It can | How |
|---|---|
| see what its person sees | the map of their tiles, screens and orgs; a tile's status and what it reports; a tile's log where the person may open a terminal; xbin's docs; a tile's `API.md` |
| call a tile's own API | `GET` runs; any other method asks the person first, with the exact call shown |
| start a tile agent | a coding-agent session in the tile, opened as the person and marked "started by your agent"; asked every time (or the person's preference). The model never picks the coding agent: it is the person's choice among what the tile's org allows, and its mode and network are the person's preferences |
| look at a tile's page | a read-only preview of one tile as the person sees it: the agent gets the page as text |
| steer the tile agents it started | message, read status and result, stop a turn — never one the person started themselves |
| ask a sub-agent about a tile's code | a read-only research sub-agent lists and reads the tile's files through `/code/tree` and `/code/file` and answers in words |
| create a tile from a template | where the person may create; asked every time |
| propose admin changes | only for an admin who turned it on: a batch the person reviews; xbind applies what they tick, as them |
| remember | a notebook of pages the person can read, edit and delete |

What a session is connected to follows the tile's org on every call: an org can keep workspace agents out, allow private models only, or name the coding agents allowed on its tiles. The session's connection line ("14 tiles · 3 orgs connected · 6 not connected") is xbind's; a blocked tile is never named to the agent.

### What it never does

The workspace agent never reads or changes a tile's code itself: a tile's own agent builds, and a research sub-agent reads and summarises. It never answers a tile agent's permission request or question, since those are its person's to answer, and a message it sends a waiting tile agent is held until they did. It can't delete or disable a tile, open a terminal, use the web, change passwords, devices or sign-ins, call the admin API, or approve anything: every approval is its person's own click in xbin's chrome or the app. It can't see a tile that isn't connected in its session, and it can't act for anyone but its own person.

## How approvals work

The agent **asks**; the person **decides**, in chrome the agent's page can't draw in; xbind **keeps** the decision and checks it on the call.

1. The agent files the side effect exactly as it will be sent (`POST /delegate/requests`): the method, path and body of a tile call, a tile agent's start, a tile's creation. Before that it asks xbind what the call would be (a dry run) — every changing tool of the agent has that form.
2. The person sees xbind's description beside the agent's words (which are quoted as the agent's), and allows it once, allows it as a standing approval, or denies it. A request waits 24 hours.
3. Approved, the request is a ticket: once, within 15 minutes, for that exact call. The agent's backend makes the call and gives the answer to the model; denied, expired or withdrawn, the model is told that instead.

Starting a tile agent is approved with its brief: the brief is part of the ticket, xbind shows it as what the session is told first, and xbind sends it. What the agent messages that tile agent afterwards is not approved message by message — the tile agent's own mode, asking the person before it acts, is the guard there.

A request holds nothing up: the agent is told it is pending and goes on, or ends its turn, and the outcome reaches the conversation as an update when the person decides — hours later if need be. Meanwhile it still hears from the tile agents it started. A message from the person is never taken for the decision. xbind tells the agent's backend that something changed with a notice that carries ids and kinds only; the backend reads the state again through the API, so a forged or repeated notice changes nothing — and it reads everything it waits on again when it starts and while anything is open, so a notice that never arrives costs a delay, not a stuck conversation.

Admin changes are never calls. An admin's agent proposes a change set (grants, bindings, org shares, limits); xbind writes each line, its before and after and its warnings; the person ticks what to apply.

## Using it

A person meets their agent in **the dock**: a panel beside the canvas, opened with the **workspace agent** chip in the top bar (or Ctrl+J / ⌘J when no field or terminal has the keys), or about one tile with **Ask my agent about this tile** in that tile's ⋯ menu. The chip carries the number of things that wait for them; on a narrow screen the dock is the whole page. A workspace whose shell has no chip — a scaffold from before it — uses **`/xbin/agent`**, the same dock as a page of xbind's own; such a shell shows one alert that names the page, per person, until they dismiss it.

The dock's header is xbin's, and so is everything a person decides in it; the agent's own conversation is a page in a frame below, and it can draw nothing in the header or over it.

- **Needs you** lists what waits: requests for approval, proposed admin changes, and tile agents the person's sessions wait on.
- **An approval** opens as a sheet from the header. It shows what xbind read of the request — the tile and the person's level on it, the exact call, what is sent (text, or "binary", its size and hash), whether it can be undone — with the agent's own sentence quoted below as the agent's. The choices: **Allow once**, **always allow this exact action**, or — marked as wider, with a second step whose first button is *Go back* — **every change to this tile** for a time. A tile agent's start shows the coding agent, the model, the mode ("asks before acting", or the wording of a mode that doesn't — there *Deny* is the first button), the network and the brief exactly as sent. Allow answers only to the person's own click, a moment after the sheet shows; Enter never allows, Esc leaves it for later.
- **Admin changes** open as a dialog over the workspace: a line an item with its before and after; an item with a warning (it makes something public, opens a port, lets a tile reach the internet) starts unticked; an item xbind would refuse is shown with the reason and can't be ticked. Applying asks for a recent sign-in when there is none. The results name what was applied, what failed and what was out of date; **Undo** proposes a new set to review — it undoes nothing by itself.
- **Approvals** lists the standing approvals, each with **Take back**. **What my agent did** is the person's log in sentences (changes, refusals, everything). **Preferences** holds whether the agent may start tile agents without asking and, for an admin, whether it may propose admin changes.
- **Pause** stops the agent's every call at once; only the person resumes it.

In place of the conversation the dock says why there is none: not set up (an admin sees the three steps), no language model connected, turned off for the workspace, paused, or held after a restart or a downgrade.
## The agent's page

The conversation with the agent is its own page — the sealed component's frontend, the agent template's `ws/` page — framed by xbin's dock (or `/xbin/agent`). It shows:

- **The session in view** and a switcher: Main always first and never archived; other sessions with their name, scope, model and status; New session… (it copies the scope and model of the one in view and starts empty), Rename, Archive (archived ones fold under Older).
- **The scope chip**, always in view: xbin's line ("14 tiles · 3 orgs connected · 6 not connected") and the model. It opens a panel: the four totals; orgs unfolding to tiles, each *connected*, *hidden by you*, *blocked* (with the org's own sentence) or *no access*; ticks staged and applied with one button; the model with a what-if before anything changes, and a change the conversation's data doesn't allow drawn as "This session can't move to …" with a new session as the way forward. Scope is focus, not permission, and the panel says so: a blocked tile can't be ticked.
- **The conversation**, with the agent's own cards where it made them: the plan (which steps only read and which will ask), the brief for a tile agent (Goal, Done when, Leave alone, Check by — editable, what the tile agent is told first, what it will run on, a pick among the coding agents the tile allows), a request waiting for approval (what, for which tile, when it expires) and afterwards what came of it, a tile agent it started (its status as a glyph and a word, its last line, Open — or Answer when it waits for you: only you can answer it), xbin's refusals with the way forward, and the agent's questions as choices with "Something else".
- **An empty state** for a new session: what the agent can and can't do in this session, and starters built from your own tiles. A starter fills the message box; it never sends.
- **The notebook**: every page, read, edited, added or deleted by you; a page a session may not read is listed but closed, with the reason.
- **Banners** for what stops it: paused, a used-up quota, turned off by an admin, xbin updating, no connection (your draft stays and is not sent).

**The page grants nothing.** It renders what a model wrote, so every approval — a tile call, a tile agent's start, an admin change set, resuming — is drawn by xbin's chrome from xbind's own reading of the request. The page asks the chrome to open one by its id (`xbin:agent-open-request {id}`, `xbin:agent-open-changeset {id}`; a tile agent's session with `xbin:open-session {session, tile}`, or a new one of a tile's own agent with your own words as its draft — `{session: "new", tile, draft}` — when the agent redirects a request about code: the tile's window opens on a new session's launcher, and the words wait, unsent, in the composer of the coding agent you pick; a shell from before this gets a plain link to the tile instead), and the chrome tells the page what it was opened about (`xbin:agent-context {tile}` or `{screen}`: the message box's "About …" chip, sent with the next message as its `context`). The contract is in the template's `API.md` §Workspace mode → The page.

## For tile authors

A workspace agent's call to your tile's API looks like its person's — `X-XBin-From: user:<id>`, `X-XBin-User`, their level, role `reader` or `writer` and never `admin` — plus **`X-XBin-Agent`**, the sealed component's path.

- **Declare what agents may use.** `"agent"` in the tile's `xbin.json`: `"full"` (the default), `"read"` (agents see what the person sees and change nothing) or `"off"` (the API hands out secrets or is a control surface). A manifest can lower the level at once; raising it needs a person who manages the tile ([protocol/delegation.md §A tile's agent level](/docs/protocol/delegation.md)).
- **Check `Caller.Agent`.** `xbin.Caller(r).Agent` (or `ByAgent()`) is the acting component, empty for the person's own hand. A route that *answers* something a person was asked — an approval, a permission, a question, a consent, a sign-in — refuses a caller with it, whatever the manifest says ([elements.md → A person's agent](/docs/elements.md)).
- **Write an `API.md`.** The agent reads it before it calls your tile: say what each call does and what it changes. It is read as a description, never as instructions.
- **What your tile returns is data.** The agent is told that tile data, logs and pages are things to report, not to obey — but a tile that returns "ignore your instructions" is still a tile behaving badly. Don't put text meant for agents in data.
- **A person approves a call, not a meaning.** xbind shows the method, the path and the body. Make write routes say what they do in their path and body, so the approval a person gives is an informed one.

## What the language model's provider sees

The agent's model calls go through xbind's LLM connector, on the model its session is on. The provider of that model receives the conversation: what the person wrote, the agent's replies, and the results of its tools — tile names and status lines, what a tile's API answered, log lines (redacted for known secret shapes), a tile agent's report, the research sub-agent's summaries, and the notebook pages the session may read. It does not receive tile code unless a research sub-agent quoted a line of it, a tile agent's transcript beyond its final report, or anything from a tile that isn't connected.

Orgs decide which models may see their tiles ([protocol/orgs.md](/docs/protocol/orgs.md)): a session on a model an org doesn't allow isn't connected to that org's tiles, xbind remembers which orgs' data a conversation has read, and notebook pages carry the orgs whose data they may hold — a session reads a page only when its model is allowed by every one of them. A tile agent the workspace agent starts runs on the harness and model the person's preferences and the tile's org allow, with its own sign-in or the connector.

## What the agent knows about xbin

The agent's prompt carries a short map of xbin — components and tiles, screens, people's access, orgs, partitions, grants, interfaces and bindings, sandboxes, terminals, tile agents, and what the agent itself is — generated from these docs by `hack/wsagent-model.sh` (the passages it quotes are listed in `hack/wsagent-model/main.go`), under a size budget, and checked by a test: change a quoted passage and the test fails until the file is regenerated. Everything else it reads on demand, with `docs_search` and `docs_read` over the docs xbind serves.

## The notebook

"What the agent remembers": Markdown pages in the person's own partition, with every version kept. The agent writes what should outlast a session — how the person likes things done (the page `preferences` is shown to it in every session), what their tiles are for, what a research sub-agent verified about a tile (stamped with the tile's revision). The person reads, edits and deletes any page; deleting a page makes the agent forget it. Every page the agent writes carries the orgs whose data its conversation held at that moment, and a session reads a page only when its model is allowed by each of them; reading one marks the session as holding that data. A page closed to a session is still listed for the person, who can edit it — and is the only one who can clear its marks.

## Running it

- **Turn it on**: connect a model ([llm.md](/docs/llm.md); the agent uses the model of the `agent` role unless a person picks another the rules allow), `PUT /api/xbin/delegate/workspace {enabled: true}`, and give people read access to `xbin/wsagent`.
- **Constrain it**: per org — workspace agent on or off, which models, which coding agents ([protocol/orgs.md](/docs/protocol/orgs.md)). Per tile — its `"agent"` level.
- **Watch it**: every side effect, approval and refusal is in the person's log (`GET /api/xbin/delegate/audit`; an admin reads another person's with `?user=`). The agent and its page can't read it.
- **Stop it**: a person pauses their own (`PUT /delegate/state`); an admin turns it off for the workspace. Tile agents already running keep going, as their person's sessions.
- **Its own API** (for the page that shows it, and for debugging): the template's `API.md` §Workspace mode.
