# The workspace agent on iOS, stage 2: reachable when the app is not open

> Status: live — the spec for stage 2 of the workspace agent in the xbin app
> (2026-11-16, branch `wsagent/ios-stage2`, from `wsagent/main` 2027c8fe;
> D241). Stage 1 is D233. The design it builds is `plans/wsagent/ux.md` §9
> (mobile), §10 (states) and §13.4–13.6; where this spec departs from it, it
> says so and why.

Reads with: `native/spec/push.md` (the wire every push and Live Activity
already uses), `relay/README.md`, `docs/protocol/delegation.md` (requests,
change sets, §Events and pushes, §The chrome), `docs/workspace-agent.md`
§In the xbin app, `native/AGENTS.md` §The workspace agent's chrome and
§App Store (privacy), D102 (Live Activities), D181 (xbind's own pages in
pushes), D233.

## 0. In short

1. **One push per thing that waits**, sealed to the device as every xbin
   push is; the words on it are xbind's and the app's, drawn from the
   request's own fields, never the model's in a title. Pushes for one thing
   collapse; a background push clears them when the thing is decided
   elsewhere or withdrawn.
2. **Lock-screen actions** come from a matrix the app's own code applies to
   xbind's reading of the request: **Allow once** only for a plain tile call
   whose whole call fits the notification (xbind's clip rule *and* the app's
   own check), and only after the person turned it on; **Deny** wherever it
   is on (default on); **Review…** always. Every action needs device
   authentication; the app re-reads the request with the person's session
   and decides nothing that differs from what was shown. A decision from a
   notification is audited as one.
3. **One Live Activity per workspace** while the agent works or waits —
   counts and a phase only, pushed by xbind through the relay like the tile
   agents' cards, and updated by the app while it is in front.
4. **One router** opens exactly the thing from a notification, the Live
   Activity and Home's row: cold start, another workspace, a locked app, and
   "already decided" said in words, never an empty sheet.
5. **Dictation** (on-device system speech) in the agent's composer, **quick
   replies that fill and never send**, **Reply** from a notification — a
   message, never an approval.
6. **Handoff** of the conversation to the web; what universal links can't do
   for a self-hosted domain, said plainly.
7. **Offline**: nothing is queued blindly — "Couldn't reach Northwind —
   nothing was decided", with Retry. **Badges** across workspaces, kept
   honest by pushes and background refresh. **Per-workspace settings** that
   xbind is told (it sends nothing the person turned off).

## 1. What exists today, and what is reused

| Exists | Where | Reused as |
|---|---|---|
| Sealed pushes: X25519 + HKDF + AES-GCM to a per-workspace device key, the relay sees ciphertext and a hash collapse id, the NSE opens them | `internal/push` (seal.go, sender.go), `relay/`, `native/ios/NotificationService`, `Shared/PushCrypto.swift`, XbinCore `Client/Push.swift`, `native/spec/push.md` | the one push path: every new kind rides it |
| `agent.approval`, `agent.changeset` kinds (one notification a person, a count, ≤ 1 a minute; link = the holder's path) | `internal/server/delegateapi.go`, `changeset.go`, `plan.go` → `push.Notice` | the kinds stay; they become one per thing (§3) |
| Tile agent pushes (`agent.permission`, `agent.question`, `agent.turn`) with a 3 s grace | `internal/push/push.go` | unchanged — a tile agent the workspace agent started waits for its person through them |
| Live Activities for tile agent turns: update tokens and push-to-start registered per device, the relay builds the APNs body from enumerated fields only, limits and flush | `internal/push/activity*.go`, `relay/liveactivity.go`, `App/Push/LiveActivities.swift`, XbinAgent `LiveActivity.swift`, `Widgets/` | the same registration routes, store, sender, limiter and relay endpoint for the workspace agent's card (§5) |
| The workspace agent's own needs pusher (`ask_user` → `POST /api/xbin/notify` kind `question`) | `builtin-templates/agent/_backend/needs_push.go` | kept; xbind now writes the words for a holder (§3.4) |
| The app's chrome: the sheets drawn from `GET /delegate/requests/<id>`, `AgentReveal`, arming, `WorkspaceAgentModel` sending every grant | `App/Shell/Agent*.swift`, `App/Model/WorkspaceAgentModel.swift` | the router opens these sheets; notification actions send through the same model |
| `HandoffActivity` (window → `NSUserActivity`, `webpageURL` + `xbin://` link) | `App/Model/Handoff.swift` | extended with the agent's conversation |
| `DeepLink` (`xbin://<ws>/xbin/agent` opens the agent, D233) | XbinCore `DeepLink.swift` | the router's links are that page's link plus a fragment an older app ignores |

Found on the way and fixed here: the approval and change-set pushes link
`xbin/wsagent` (the holder's path), which no app parses (`xbin/<page>` names
xbind's pages, and `wsagent` is none) — tapping one opened the workspace's
Home. They now link `xbin/agent#…` (§3.2).

## 2. The privacy rule, and why the new payloads meet it

The rule is push.md §6 and native/AGENTS.md §Privacy: **a notification's
content is sealed to the device**; the relay and APNs see a handle, a hash
collapse id, ciphertext sizes and timing; **a Live Activity push is the one
unsealed channel and carries enumerated state and counts only** (the relay
builds the APNs body and refuses anything else); no title, name, path or text
ever goes through it. The app's privacy manifest is unchanged by this stage:
nothing new is collected for the developer.

Every new notification payload (§3.1) is a v1 payload with one more member,
`agent`, inside the sealed plaintext — so the relay learns nothing new. The
background `agent.clear` push is sealed the same way; its APNs body is
`{"aps":{"content-available":1},"xbin":<envelope>}`. The Live Activity's
pushed state adds one count (`agents`, tile agents working) and one phase
(`paused`) to the enumerated fields — no string; the relay checks both
(§5.3).

What the notification shows comes from xbind's reading of the request: the
title is xbind's words with the tile's name; the body is either xbind's words
or the call itself (method, path, body) — the request, never the agent's
`summary`, and never the model's words in a title. Characters that reorder or
hide text never reach the screen raw: a call with any is not offered Allow,
and its text is drawn through `DelegationWords.reveal` (⟨U+202E⟩).

## 3. Notifications

### 3.1 The payload

A v1 payload (push.md §2) with `agent`:

```json
{"v": 1, "ws": "<push id>", "kind": "agent.approval",
 "title": "Approval needed",
 "body": "Your workspace agent wants to change something in the tile “Invoices”. Open to see exactly what.",
 "link": "xbin/agent#r=<request id>", "collapseId": "agent-approval:<request id>",
 "agent": {"v": 1, "badge": 3,
   "request": {"id": "<24 hex>", "kind": "call", "tile": "apps/invoices", "tileName": "Invoices",
               "method": "POST", "target": "apps/invoices/links/rebuild",
               "contentType": "application/json", "body": "{\"ids\":[1,2]}",
               "bodySize": 13, "bodySha256": "<hex>", "offer": "once", "expires": 1763400000}}}
```

| member | in | meaning |
|---|---|---|
| `agent.v` | all | 1; a newer shape changes it, and an app reads only the version it knows |
| `agent.badge` | all | what waits for the person in this workspace (xbind's count: pending requests, open change sets, tile agents of theirs that wait) |
| `agent.request` | `agent.approval` | the request as xbind reads it: `id`, `kind` (`call` \| `session` \| `template` \| `web` \| `mcp`), `tile`, `tileName` (xbind's name for it), `expires` (unix s); **only when `offer` is `once`**: `method`, `target` (the path under `/api/`, with its query), `contentType`, `body` (the whole body), `bodySize`, `bodySha256` |
| `agent.request.offer` | | `once`: xbind's clip rule (§3.3) lets the lock screen offer Allow once; `""`: Review only |
| `agent.changeset` | `agent.changeset` | `{id, items, warned, plan, asks}`: the number of lines, of lines with a warning, whether it is a plan, and — for an admin — whether it is another person's ask (`asks: true`) |
| `agent.conversation` | `agent.ask`, `agent.done` | `{id}`: the agent's conversation (its run id) — where Reply goes |
| `agent.ask` | `agent.ask` | the question as the agent wrote it, at most 200 characters, cut by xbind — drawn by the app quoted, as the agent's words, revealed (§3.6) |
| `agent.clear` | `agent.clear` | `[collapse id, …]`: the notifications that no longer stand |

An older app ignores `agent` and shows `title`/`body`, which are xbind's
words with no request data; tapping opens the agent (`xbin/agent`, D233) or,
older still, the workspace.

### 3.2 Kinds, words, collapse, links

All kinds sit under `agent` (an app registering `agent` gets them all).

| kind | raised when | title — body (xbind's) | collapse | link |
|---|---|---|---|---|
| `agent.approval` | a request waits 3 s (grace: one decided at once raises nothing) | "Approval needed" — by kind: "Your workspace agent wants to change something in the tile “Invoices”." / "…wants to start a tile agent in the tile “Invoices”." / "…wants to create a tile." / "…wants to read a web page." / "…wants to use a tool of the tile “Invoices”." + " Open to see exactly what." The tile's name is its author's words (review finding 5): never in the title, quoted, one line, at most 32 characters; one that is empty, hides text or breaks a line gives way to the path's last part (`quotedTileName`, XbinCore `AgentNotice.quotedName`); a person's name in "“Ana Ruiz” asks for an admin's changes" the same | `agent-approval:<id>` | `xbin/agent#r=<id>` |
| `agent.changeset` | a set is proposed (a plan, admin changes) or a person asks the admins (to each admin) | "Admin changes proposed" — "Your workspace agent proposes N admin changes. Nothing is applied until you approve." / "A plan to review" — "Your workspace agent proposes a plan of N steps. Nothing is applied until you approve." / "Ana Ruiz asks for an admin's changes" — "N changes wait for an admin." | `agent-changeset:<id>` | `xbin/agent#cs=<id>` |
| `agent.ask` | the agent asks its person (`ask_user`; the holder's notify, §3.4) | "Question from your workspace agent" — "Open the agent to answer, or reply here." | `agent-conv:<conversation>` | `xbin/agent#c=<conversation>` |
| `agent.done` | a request is finished: the conversation went idle with nothing of it open (§3.4) | "Workspace agent finished" — "The report is ready." | `agent-conv:<conversation>` (replaces a question of the same conversation) | `xbin/agent#c=<conversation>` |
| `agent.clear` | a request was decided, withdrawn or expired; a set decided or withdrawn | (background: nothing shown) | — | — |

None per turn: a turn that leaves work open (a tile agent running, a request
pending) raises no `done`. Over the person's delegate budget (12 at once,
then 60 an hour) single pushes stop and one summary goes out when the budget
allows ("N things wait for you — Open your agent to see each.", collapse
`agent-approval`), never more than one.

**Clearing.** When the thing a notification is about no longer waits — a
request decided on the web, on another device or from another notification,
withdrawn (by the agent, or by xbin: workspace agents off, the person's agent
taken away, the person disabled), expired, a set decided — xbind sends
`agent.clear` naming its collapse id to each of the person's devices: a
**background** push (`apns-push-type: background`, priority 5; relay type
`background`, §6). The app opens it as the NSE does, removes the delivered
notifications whose collapse id it names, updates the badge, and ends the
Live Activity's stale "needs you". iOS rations background pushes and
delivers none to an app the person force-quit, so the app also clears on
every foreground and background refresh (§9): it re-reads what waits and
removes every delivered agent notification whose thing no longer waits. A
notification tapped for a thing that is gone opens the "already decided"
state (§7.3), never an empty sheet. Pause, held and off clear nothing from
xbind's side (a paused agent's requests are kept, push.md's switches table);
the notification stays and its Allow answers "your agent is paused" (§4.4).

### 3.3 The clip rule (xbind's): when the lock screen may offer Allow once

`offer: "once"` only when **all** hold — else `""`:

- `kind` is `call` (a tile API call): never a session start, a tile create, a
  web page (the address is the data, asked every time) or an MCP tool (its
  own sheet shows the tool's marks and arguments);
- the method is not `GET`/`HEAD`; the request names its tile;
- the body is text (`bodyShown`, not `bodyBinary`), its content type is none,
  `application/json` or `text/plain`;
- `target` is at most **120 bytes**, the body at most **200 bytes**;
- neither holds a character that reorders or hides text (Unicode category
  Cf, controls but `\n`), and the target holds no line break;
- **rendered** (review finding 1): the text the app draws — `<method>
  <target>`, then the body or "(no body)" — certainly shows whole in the
  collapsed notification: at most **4 lines** of **12 em** (281 pt of text at
  23 pt: the largest default Dynamic Type on the narrowest iPhone), wrapped
  greedily at spaces, a word wider than a line broken between characters,
  each character's advance counted at or above SF Pro's (30/45/65/80/100
  hundredths of an em by class). Only printable Latin (U+0021–007E, U+00A1–
  024F but the soft hyphen) and – — ‘ ’ “ ” … € are counted at all: a line
  or paragraph separator (U+2028/2029, NEL, VT, FF, CR), a tab, a space but
  U+0020, two spaces together, a right-to-left letter, a wide, combining or
  emoji character refuse the offer. `delegation.LockScreenFits` and XbinCore
  `LockScreenFit` are the same function, held together by
  `lockscreen-fit.json` (written by the Go test, read by the Swift one);
- the request does not say the conversation read the web (`preview.readWeb`:
  what it asks may come from a page) and its preview carries no warning.

Why so short: iOS shows a notification's action menu (swipe → Options, or a
long press) over the notification as it is drawn — the collapsed form shows
the title, the subtitle and about four lines of body. What the rule offers
is what a person sees **in full** before the menu, at any default text size
on any iPhone; anything that does not certainly fit is Review only.
The rule is one function (`delegation.LockScreenOffer`), and the decision
route enforces it too (§4.4).

### 3.4 Where `agent.ask` and `agent.done` come from

The workspace agent's backend already pushes its own question through
`POST /api/xbin/notify` (kind `question`, `needs_push.go`) — with the
conversation's title (the model's words) as the title. For a caller that is
a **delegation holder** (`auth.HoldsDelegation`), xbind's notify no longer
takes the tile's words: it maps the kind and writes its own —

| the holder's kind | becomes | xbind keeps of what the holder sent |
|---|---|---|
| `question` | `agent.ask` | the conversation from the link (`#c=<run>`), the body cut to 200 characters as `agent.ask` |
| `done` | `agent.done` | the conversation |
| anything else | nothing (202, `suppressed`) | — |

The holder's title and body never reach a screen. The backend, in workspace
mode, sends `done` once per finished request: when a workspace session's
top-level turn ends answered or finished, 3 s later, if the conversation is
idle (not waiting on its person), none of its tile agents is starting or
running, none of its requests or sets waits, and the person wrote to it since
the last `done` it sent for that conversation.

### 3.5 The action matrix

The app's NSE picks the category from **the payload's request fields and
the workspace's settings** — never from text, and never from anything the
agent's view or the model can set. The app re-checks the same rule when an
action arrives (§4).

`eligible` = `offer` is `once` **and** the app's own check passes on the
same fields: kind `call`, method and target present, the body's UTF-8 length
equals `bodySize`, the clip rule's sizes and lines, and
`DelegationWords.revealed(body + target)` finds nothing hidden.

| the notification | settings (this workspace) | category | actions |
|---|---|---|---|
| `agent.approval`, eligible | Allow once on, Deny on | `xbin.agent.request.allow-deny` | **Allow once** · **Deny** · **Review…** |
| `agent.approval`, eligible | Allow once on, Deny off | `xbin.agent.request.allow` | **Allow once** · **Review…** |
| `agent.approval`, eligible | Allow once off, Deny on | `xbin.agent.request.deny` | **Deny** · **Review…** |
| `agent.approval`, anything else (a start, a create, a web page, an MCP tool, too long, hidden characters, a warning, binary) | Deny on | `xbin.agent.request.deny` | **Deny** · **Review…** |
| any `agent.approval` | Deny off, Allow once off (or not eligible) | `xbin.agent.request.review` | **Review…** |
| `agent.changeset` (admin changes, a plan, an admin's ask) | Deny on | `xbin.agent.changeset.reject` | **Reject** · **Review…** |
| `agent.changeset` | Deny off | `xbin.agent.changeset.review` | **Review…** |
| `agent.ask`, `agent.done` | — | `xbin.agent.conversation` | **Reply** (text) · **Open** |
| a follow-up that can be retried | — | `xbin.agent.followup.retry` | **Retry** · **Review…** |
| any other follow-up | — | `xbin.agent.followup` | **Review…** |

| action | options | does |
|---|---|---|
| Allow once (`allow-once`) | `.authenticationRequired` | §4 — never `.foreground`: the decision is the action itself |
| Deny (`deny`), Reject (`reject`) | `.authenticationRequired`, `.destructive` | §4 |
| Review… (`review`) | `.foreground`, `.authenticationRequired` | the router (§7) |
| Reply (`reply`) | `UNTextInputNotificationAction`, `.authenticationRequired` | a message to the conversation (§8.3) |
| Retry (`retry`) | `.authenticationRequired` | §4, again from the re-read |

Nothing wider ever has a lock-screen approve: an "always" scope, a whole
tile, a session start (whatever its mode), a create, a web page, an MCP tool,
an admin line, a plan. Their only way to Allow is the app's sheet.

### 3.6 What the app draws

The NSE composes the visible text itself from the payload's fields (ported
words in XbinCore `AgentNotice.swift`; xbind's `title`/`body` are only an
older app's):

- title: "Approval needed" · subtitle: the workspace's name (as today);
- body, eligible: the call as the sheet shows it, in full — line 1
  `POST /api/apps/invoices/links/rebuild`, then the body ("(no body)" when
  there is none);
- body, otherwise: xbind's sentence for the kind (§3.2);
- `agent.ask`: "Your agent asks: “…”" — the agent's words, quoted, revealed;
  never in the title;
- `interruptionLevel` `.timeSensitive` for `agent.approval` and
  `agent.changeset` (it needs the entitlement — §12; without it iOS treats
  it as `.active`), `.active` for the rest;
- `threadIdentifier` `<ws>/xbin/agent`: the agent's notifications are a group
  of their own per workspace;
- `userInfo`: `{app, ws, kind, link, collapse}` and, for a request, `shown` —
  the fields the text was drawn from (`id, kind, tile, method, target,
  contentType, body, bodySha256, offer`) — what an action is checked against.

## 4. Deciding from a notification

### 4.1 The rule

Only the app's own code decides, with the person's session, after device
authentication (the action's option), after re-reading the request:

1. the action names a workspace the app has, and the setting for it is on
   now (an Allow from a notification delivered before the person turned it
   off decides nothing);
2. `GET /delegate/requests/<id>` with the device's session (through the
   workspace's tunnel when it has one: `TunnelManager.inBackground`, 25 s);
3. `AgentNoticeDecision.verify(shown:, current:)` (XbinCore, pure):
   - not reachable → **nothing is sent**, a follow-up "Couldn't reach
     Northwind — nothing was decided." with Retry (§10);
   - 404/403 or not pending → a follow-up that says what came of it (§7.3's
     words: "Already allowed once on the web · 10:16", "Already denied on
     Ana's iPhone", "Withdrawn by the agent", "Expired");
   - any shown field differs from xbind's reading (kind, tile, method,
     target, content type, body, its hash) → nothing is sent, "This request
     isn't the one shown — open it to review." (Review);
   - Allow: not eligible by the app's rule on the **re-read** request → "This
     needs a look in the app" (Review);
4. `POST /delegate/requests/<id>/approve {grant: "once", via: "notification"}`
   or `…/deny {via: "notification"}` — `WorkspaceAgentModel.approve/deny`,
   the sheet's own path;
5. the outcome as a follow-up that replaces the notification (same
   identifier): "Allowed once — POST …/links/rebuild" (passive) · "Denied"
   (passive) · `stopped` → "Nothing was allowed: your agent is paused."
   (xbind's refusal words) · `gone` → the already-decided words · `failed` →
   "Couldn't allow: <xbind's words>" (Review).

A change set's Reject runs the same steps against
`GET /delegate/changesets/<id>` (state `proposed`) and
`POST …/reject {via: "notification"}`.

### 4.2 Reply is a message, never a decision

`reply` posts the typed text to the conversation as the person's message
(§8.3) — `POST /api/<holder>/runs/<conversation>/message {text}` with the
device's session — and nothing else: `AgentNoticeAction` maps every action
id to one effect, and `reply` maps to `.message` whatever the text says
("allow", "yes", "approve" included). xbind already never reads a message as
a decision (docs/workspace-agent.md §How approvals work); the app-side test
(§13) holds that no text a reply carries reaches a decision route.

### 4.3 What the agent and its view can't do

- The model chooses the request's method, path and body — not the category:
  that is xbind's `offer` AND the app's own check on fields that are the
  request itself (a summary, a title, a question never take part).
- The agent's native view has no notification API (tile code has no device
  APIs; `AgentBridge.View` is closed): no message of the bridge posts,
  schedules or changes a notification, a category or an action.
- A notification's action carries the id and the shown fields from the NSE;
  the app decides only when xbind's current reading equals them — a forged
  or stale notification decides nothing.
- The NSE holds what it held before — the per-workspace X25519 private keys
  (Keychain, after first unlock) — plus the settings it reads (§11). No
  session, no device key, no credential of xbind's: it can draw, never
  decide.

### 4.4 xbind's side of a decision from a notification

`via: "notification"` (the only value; anything else 400) on approve, deny
and reject:

- **approve** is refused unless `grant` is `once` (or absent) and the
  request passes `LockScreenOffer` now: `409 {error, review: true}` — "This
  request needs a look in the app";
- the request records `decidedFrom: "notification"` and, for a device
  session, `decidedDevice: <device id>` (also set for every decision of a
  device session) — what "Already decided on Ana's iPhone" reads;
- the audit row of the decision carries `via: "notification"` (and the
  workspace agent's activity log says "from a notification").

## 5. The Live Activity

### 5.1 The card

`WorkspaceAgentActivityAttributes` (XbinAgent, a type of its own so its
push-to-start token is its own): `{ws, ref, workspace, appWorkspace}` —
display names set by the app; a push-started one carries `ws` and `ref` only.

Content state `{phase, since, pending, agents}`:

| phase | when | lock screen ("Workspace agent · Northwind") | compact / minimal |
|---|---|---|---|
| `running` | a tile agent it started is starting or running, or its own turn is running (a model call in the last 90 s) | "**Working** · 3 tile agents" · "for 12 min" | the agent glyph + `3` |
| `waiting` | something waits for the person (`pending` > 0), whatever else runs | "**Needs you (2)** · 1 tile agent working" | a hand + `2` |
| `paused` | the person paused it (the last state; the card ends) | "**Paused** — your agent can't act until you resume." | a pause glyph |
| `idle` | nothing works or waits (the last state; the card ends) | "**Done** · the report is ready" | a check |

Counts only: no tile name, no request text, no conversation title — the
same on the lock screen, in the Dynamic Island and in the pushed state.
Expanded Dynamic Island: the line above, the elapsed time, and two buttons:
**Review** (what waits first; shown while waiting) and **Open agent**. A tap
anywhere else goes where Review goes, else to the agent. Links:
`xbin://<appWorkspace>/xbin/agent#first` and `…/xbin/agent`.

### 5.2 Its life

- **One per workspace.** The app starts it while in front (ActivityKit
  starts activities only there) when the agent works or something waits for
  the person, after 10 s of either (no flash for a quick turn at the desk) —
  at once when something waits, or when the app leaves the front with the
  agent working. xbind starts it by push (§5.3) when the app never did.
- **It ends** after 2 minutes with nothing working and nothing waiting
  (final `idle`, dismissed 5 minutes later), at once when the person pauses
  it (final `paused`, dismissed after 1 minute), when workspace agents go
  off, the person's agent is taken away or held (dismissed at once), when
  the person turns the card off in Settings, and when the person swipes it
  away — then no card comes back by push until the next working stretch.
- **Updates** arrive from xbind over APNs while the app is suspended, and
  from the app's own model while it is in front; both follow the same
  rules, and a timestamp that only grows keeps the newer.
- **Budget.** Waiting changes go at priority 10, the rest at 5; xbind's per
  activity limit is the tile agents' (240 an hour, burst 30) with the latest
  state flushed when it allows again; changes inside 2 s coalesce into one.

### 5.3 xbind and the relay

- **Registration** (additive):
  - `POST /devices/push {…, agentStartHandle}` — the relay's handle for the
    push-to-start token of the workspace agent's card ("" removes it,
    absent keeps it);
  - `POST /devices/push/activities {deviceId, agent: true, handle, since?}`
    — the card the app started → `{activity: {agent: true, created,
    ended?}}`; one xbind started registers by `{deviceId, ref, handle}` as
    today and is answered `{activity: {agent: true, …}}`;
  - `DELETE /devices/push/<deviceId>/activities/@agent` — the person swiped
    it away;
  - `GET /devices/push` → per device `agentActivity: true`,
    `agentStart: true`; at the top `features: ["agent-push/1",
    "agent-actions/1", "agent-activity/1"]` — how the app knows this xbind
    has stage 2 (an xbind without the field has none of it, and the app
    keeps stage 1's behaviour).
- **The picture.** xbind recomputes a person's picture (has an agent and
  workspace agents on; paused; pending; tile agents working; the agent's
  own turn) on every `delegate` event, every status change of a session
  their agent started, and every model call of their holder — coalesced
  per person (500 ms).
- **The pushes**: `update` (+ `staleDate` now + 4 h), `end` (+
  `dismissalDate`), and `start` to the device's `agentStartHandle` after 30
  s of a working or waiting stretch on a device that shows none, takes
  `agent.wsactivity` (registration kinds) and did not dismiss this stretch's.
- **The relay** (§6) builds the APNs body: `attributes-type`
  `WorkspaceAgentActivityAttributes` for a start of kind `agent`,
  `content-state` `{phase, since, pending, agents}` with `phase` ∈
  running | waiting | paused | idle and `agents` 0–99. **An older relay**
  (no `activity-agent` in its features) gets no start of this kind, no
  `paused` (the card ends on `idle` instead), and drops `agents` — the card
  then says "Working" without the count.

## 6. The relay (additive)

- `POST /v1/push {handle, type: "background", envelope, collapseId?}` — a
  sealed envelope to a device handle as a background push: `apns-push-type:
  background`, priority 5 (APNs requires it), body
  `{"aps":{"content-available":1},"xbin":<envelope>}`. Counted like an
  alert.
- `activity.kind: "agent"` (start, update, end): the workspace agent's card.
  A start names `attributes-type: WorkspaceAgentActivityAttributes` and
  attributes `{ws, ref, workspace: "", appWorkspace: ""}`; its alert is
  generic ("Your workspace agent is working." / "…needs you."). Its state
  may say `phase: "paused"` and `agents` (0–99); a tile agent's card (no
  kind) is checked as before.
- `GET /v1/workspace` → `{workspaceId, features: ["background",
  "activity-agent"]}`. xbind reads it at its daily key check and when push is
  configured, and sends only what the relay lists.

## 7. The router

### 7.1 Routes and links

`AgentRoute` (XbinCore): `request(id)`, `changeset(id)`, `tileSession(id)`
(a tile agent's session, the app's agent screen), `conversation(id)`,
`needs`, `first` (what waits first), `agent`. The link is xbind's page link
with a fragment — `xbin://<ws>/xbin/agent#r=<id>` (`cs=`, `ts=`, `c=`,
`needs`, `first`) — so an app from stage 1 opens the agent, and an older
one the workspace. Ids are `DelegateID`'s shapes (24 hex for requests and
sets, a session's word); any other fragment is `agent`. A push's link, the
Live Activity's, Home's row and Handoff's all go through it.

### 7.2 One way in

`AppModel.open(route:)` → the scene in front (the focused one) or, when no
window exists yet (a cold start from a notification), held until the first
window registers; then `SceneModel.open(agentRoute:in:)`: select the
workspace (another than the one in front switches to it), open the agent's
container, wait for the agent's state (read when unknown), then — after the
app lock is passed — present: the request's sheet (`agentReview`), the
change set's review, the tile agent's screen (pushed; on an iPhone the sheet
drops to medium), the conversation (the view's `#s=<id>` address), Needs
you, or the first thing that waits (requests and sets oldest first, then
waiting tile agents, else the agent). A person without an agent gets the
workspace's Home, as a link to the agent does today.

### 7.3 Already gone

A sheet opened for a request or set that no longer waits shows, in place of
the buttons, what came of it and where: "Already allowed once — on the web ·
10:16", "Already denied — from a notification on this iPhone · 10:16",
"…on Ana's iPad", "Withdrawn by the agent", "Withdrawn by xbin when your
agent was stopped", "Expired — not approved within 24 hours.", and **Done**.
"Where" is `decidedVia`/`decidedDevice`/`decidedFrom` (§4.4): a browser
session is "on the web"; this device's id is "on this iPhone"; another of
the person's devices is named from `GET /api/xbin/devices`; an xbind that
doesn't say is "elsewhere". An id xbind doesn't have (404): "This request is
gone — it was decided or withdrawn, and xbin keeps decided requests a day."

## 8. Dictation, quick replies, Reply

### 8.1 Dictation

The agent's composer gets a mic where Send is while the draft is empty
(app chrome: `XbinServices.dictation`, supplied only to the person's agent's
view — no tile gets a microphone, and no vocabulary change: the text lands
in the draft as typed text does). Tap: the permission (microphone, speech
recognition) the first time, with the reason; then `SFSpeechRecognizer` with
`requiresOnDeviceRecognition = true` (no audio leaves the phone; no
third-party speech), a level meter and "Listening… 0:07" in place of the
field, the words as a dashed draft, ✓ to stop — the text lands in the
composer to edit or send. **Nothing is sent by voice.** Where on-device
recognition isn't available (or the person refused the permission) the mic
is hidden and the keyboard's own dictation remains. With reduced motion the
meter is the static line.

### 8.2 Quick replies

The composer's replies (the view's `replies`: "Go ahead" / "Change the
plan", a question's choices, next steps) are the agent's suggestions: the
row is labelled "Suggested by your agent — tap to edit, then send", and a
tap **fills** the composer; it never sends (the template's store:
`reply(id)` fills on the web page and in the native view alike — a change
of ux.md §2.6's one-tap "Go ahead", by the owner's rule for stage 2).

### 8.3 Reply from a notification

`agent.ask` and `agent.done` have **Reply** (text input, "Message your
agent", Send): the app sends the text to the conversation the payload names
— `POST /api/<holder>/runs/<conversation>/message {text}` with the device's
session, which reaches the person's own partition of their agent — and
nothing else. Sent: the notification goes (the conversation shows the
message). Unreachable or refused: a follow-up "Your reply wasn't sent —
couldn't reach Northwind." with **Open** (the draft is not kept by the
system; the follow-up shows the text so it can be copied). Paused / off:
the backend answers `409 agent-off`; the follow-up says the switch and that
nothing was sent.

## 9. Badges and background refresh

- **The app icon's badge** = what waits for the person across workspaces:
  per workspace, the agent's count (requests, change sets, waiting tile
  agents it started) plus the person's other sessions that wait — the
  switcher's count. The app sets it (`setBadgeCount`) whenever it reads
  counts; the NSE sets `content.badge` from the payload's `agent.badge` and
  the other workspaces' last counts (kept in the shared Keychain, one item a
  workspace); a background `agent.clear` sets it too.
- **Background refresh** (`BGAppRefreshTask` `dev.xbin.app.refresh`, as
  often as iOS allows): per workspace, re-read the delegate state and the
  pending requests and sets, clear delivered agent notifications whose thing
  no longer waits, set the badge, end a Live Activity whose stretch is over.
  Nothing is decided in the background except an action the person took.

## 10. Offline and the tunnel

- An action from a notification while the workspace can't be reached sends
  **nothing**: the request must be re-read first. The follow-up says
  "Couldn't reach Northwind — nothing was decided." and offers **Retry**
  (the whole flow again, re-read first; it repeats the call in its text so
  the person decides with it in view) and **Review…**. No queue: a decision
  sent later than the person thinks is worse than none.
- A tunnelled workspace (D204) is reached through `TunnelManager.inBackground`
  within the action's time; a tunnel that can't come up is "couldn't reach".
- Reply follows the same rule (§8.3).
- In the app, stage 1's banner and "Approvals need a connection" stand.

## 11. Settings

Per workspace, in **Settings → <workspace> → Workspace agent** and the
agent's **Preferences** (one view, `AgentNotifySettingsView`):

| setting | default | what it changes | xbind is told |
|---|---|---|---|
| Needs you | on | `agent.approval`, `agent.changeset`, `agent.ask` pushes | registration `kinds` leave them out |
| Finished | on | `agent.done` | `kinds` |
| Live Activity | on | the card, app-started and pushed | no `agentStartHandle`, no activity registration; `kinds` leave `agent.wsactivity` out |
| Deny from the lock screen | on | Deny / Reject actions | — (the app's category) |
| Allow once from the lock screen | **off** | the Allow once action | — |

With every agent kind on the app registers `agent` (all of them, later ones
included); with any off it lists the kinds it wants. **Allow once** is
turned on only by the person, after an explanation sheet — "For a single
call whose whole request fits the notification, you can allow it once from
the lock screen, after Face ID. Anything wider — an always, a whole tile, a
tile agent's start, a plan, admin changes — opens the app." — whose **Turn
on** is armed like Allow (600 ms, `AgentWidening`); turning it off is never
held. The settings live in the shared Keychain (`dev.xbin.agentnotify`, one
item a workspace) where the NSE reads them.

## 12. What needs the owner (Apple account, keys, relay) — in order

Everything else is built to work the moment these are done.

1. **Deploy the relay from this branch** (relay/README.md): it adds the
   background type and the agent card; `GET /v1/workspace` then lists
   `background` and `activity-agent`. Until then xbind sends no clear pushes
   and no agent card starts (the app clears on foreground and starts the
   card itself).
2. **Set the app's relay** (`XbinPushRelay` in `Support/App-Info.plist`) —
   push is off in every build until it names the project's relay; then the
   privacy table of native/AGENTS.md §Privacy applies (Device ID, App
   Functionality, not linked) — unchanged by this stage.
3. **Apple Developer → Certificates, Identifiers & Profiles → Identifiers →
   `dev.xbin.app`**: enable **Time Sensitive Notifications**; then add
   `<key>com.apple.developer.usernotifications.time-sensitive</key><true/>`
   to `Support/Xbin.entitlements` (left out so builds sign without it). Push
   Notifications is already on; Live Activity pushes need nothing more (the
   relay's token-based APNs key covers `liveactivity`).
4. **Background Modes** (Remote notifications, Background fetch) are
   Info.plist keys this branch adds — nothing to enable in the account.
5. **App Store Connect → the app → App Review Information**: paste the
   updated notes (`Support/review-notes.md` §Workspace agent: lock-screen
   actions, the Live Activity, dictation — on-device only, its two usage
   strings).
6. **App Privacy**: unchanged ("Data Not Collected" while push is off;
   Device ID once the relay is set). Speech recognition on-device collects
   nothing.
7. **Universal links** (optional): only for a domain the project controls —
   `applinks:<domain>` in the entitlements and an
   `apple-app-site-association` there naming `/xbin/agent*`. A self-hosted
   workspace's own domain can't be claimed by the App Store app (the file
   would have to be on every workspace's server and its domain in the app's
   entitlements); those links open in the browser, and the `xbin://` link is
   their way into the app.
8. **On a device** (§14's manual checks).

## 13. Security rules and their tests

Each fails without its rule:

| rule | test |
|---|---|
| the category is chosen from the request's fields and settings only — a summary, a title, a question can't make an action appear; xbind's `offer` alone isn't enough | XbinCore `AgentNoticeTests.categoryIgnoresWords`, `offerAndOwnCheckBothNeeded`, `hiddenCharactersNeverAllow`, `widerShapesReviewOnly` |
| an action decides only what was shown: a changed field, a gone request, an unreachable xbind → nothing sent | XbinCore `AgentNoticeDecisionTests` (`anyChangedFieldSendsNothing`, `goneAndUnreachable`, `ruleOnTheReRead`) |
| a Reply is a message whatever it says; no action id but allow-once/deny/reject reaches a decision | `AgentNoticeActionTests.replyIsNeverADecision`; UI `XbinWorkspaceAgentNotifyTests.test04ReplyIsAMessage` |
| the setting is read when the action runs | `AgentNoticeDecisionTests.settingOffAtActionTime` |
| the agent's view has no way to a notification | `AgentNoticeBridgeTests.theViewCannotMakeANotification` |
| xbind refuses a notification Allow for an ineligible request, or wider than once | Go `TestDelegateNotificationAllowRule`, `delegation.TestLockScreenOffer` |
| a decision from a notification is audited as such | Go `TestDelegateNotificationAudited`, `TestDelegatePushChangeSet` (Reject) |
| a request push carries the call only with `offer: once`, never the summary; a holder's words never reach a notification | Go `TestDelegatePushPayload`, push `TestHolderNotifyWords` |
| withdrawn / decided / expired and the off switches clear | Go `TestDelegatePushClears` (decided on the web, denied, withdrawn by the agent, workspace agents off) |
| pause and off end the card; the pushed state holds no string | push `TestAgentActivityFollowsAndEnds`, `TestAgentActivityOlderRelay`; relay `TestAgentActivityRefusesText`; XbinAgent `WorkspaceAgentActivityTests.stateHoldsNoText` |
| the NSE holds no credential beyond the push keys | `native/tools/store-check.py` §8 (the extension's sources and names) |

## 14. Not verifiable here (the owner's manual checks)

| | check on a device |
|---|---|
| real APNs delivery of alert, background and Live Activity pushes | with the relay deployed and `XbinPushRelay` set: a request → the banner; decide on the web → it goes within a minute (app not force-quit); the card on the lock screen follows a tile agent |
| the collapsed lock-screen preview shows a 120-byte target and a 3-line body in full | the longest eligible request on an iPhone with the largest default text size: swipe → Options; the whole call above the menu |
| Face ID on an action | lock the phone; Allow once → Face ID; the follow-up |
| time-sensitive delivery through Focus | after step 12.3, with a Focus on |
| the Dynamic Island | an iPhone with one: compact, expanded, and minimal — drawn only beside another app's Live Activity (the island shows one per app), which the simulator has none of, so it is unseen here |
| the lock screen in the system's dark appearance | the UI tests' `-XbinAppearance dark` darkens the app only; Springboard and the card follow the simulator's own appearance (light in the gallery) |
| dictation's audio | the mic, a sentence, the draft |
| Handoff to a Mac | the conversation in front on the phone → the Mac's Dock icon → `/xbin/agent` on the same session |
| background refresh cadence | a day of use: the badge matches Needs you |

## 15. Decisions taken here (D241)

See plans/DECISIONS.md D241. In short: one push per thing, sealed as
before, words xbind's and the app's; lock-screen Allow once only for a
plain tile call that fits, on two independent checks and the person's own
opt-in; clearing by a sealed background push plus foreground and refresh
sweeps; one Live Activity per workspace on the existing activity plane, a
type of its own, counts only; the router's links are xbind's page link plus
a fragment; quick replies fill; dictation on-device only, app chrome.
