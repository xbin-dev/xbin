# The agent template, native — keeping the UX at today's level

> Status: **implemented** (2026-09-26, D96, then D106) — the model/view split
> (`builtin-templates/agent/model/`, `createApp()`, the FEATURES registry with
> both views' `IMPLEMENTS` and `DIFFERENCES`), the native view (`native.js` +
> `native/`, the drawer as `sheet edge="leading"`), and the backend's
> `/stream?deltas=1` (text, thinking and, since D106, `tool.delta`), paged
> `/view` and `/thumb` (§4, milestone 2's server half). Since D106: §4's
> fold cache (`FoldCache`, per block, identical output), "Needs you" as a
> push (`xbin.NotifyUserWith` for questions, approvals and failed
> automation runs, to the people `/needs` lists them for), a held ask with
> attachments from home (draft keys: `PUT /ask/upload?draft=`, `POST /ask
> {draft, files}`), and the settings, memory, files and skills calls in
> `model/actions.js`. The SwiftUI renderer now draws the drawer, folded
> row/message actions and message thumbnails (D104), and the app draws the
> render preview's `canvas html=` island (D103) — type-checked on Linux,
> not yet on a device. Not yet: the on-device performance targets.
> Departures: uploads and images use origin-absolute `/api/<self>/…` paths;
> rename and "new chat with options" live in menus (the vocabulary has no
> title tap or long press); Needs-you pushes reach the owner and explicit
> participant members, not a team-wide role's members (the tile can't list
> a team).

`builtin-templates/agent` is the one big tile in this repo (≈3.7k lines of
frontend: 15 modules plus `index.html`, and a Go backend with ~75 routes) and the owner's
bar is explicit: its native mode must be **as good as the web UI is today, and
stay that good** as the tile evolves. This plan is how.

## 1. What "as good" covers — the feature inventory

Everything the web UI does today (`index.html`, `agent.js`, `sidebar.js`,
`chat-*.js`, `conv-*.js`, `automations.js`, `auto-*.js`, `share.js`,
`home.js`), each with its native form. Nothing on the left may be missing on
the right at parity sign-off (§7).

| Area | Web today | Native |
|---|---|---|
| **Home** | greeting + tagline (`HOME`), example prompts, "Needs you" (`/needs`: questions, approvals, failed) | same content as a screen; Needs-you items also feed the app inbox and push |
| **Conversations** | sidebar: New chat, "⋯" new chat with options (first message, tool mode, title, instructions), search (`?q=`, content snippets; pasting a `#join=` link joins), groups Pinned/Today/Yesterday/Previous 7/30 days/Older, unread bold, ⇆ shared, status glyphs (? ! spinner), "more" paging, row menu Rename/Pin/Share/Archive/Delete-or-Leave, footer Mine/Shared with team/Archived | a **drawer** over the chat (edge swipe or the title): the same groups (`conv-groups.js`), search with snippets, swipe actions (pin, archive, delete/leave), context menu (rename, share), a scope picker; New chat in the toolbar, long-press for options (a sheet) |
| **Transcript** | user messages (sender label in shared chats), assistant markdown, thinking (live open, folded after), tool cards (headline, collapsed args/result, 1200-char cut + show all), subagent cards (open while running, child folded inside, "open ↗", inline approval), step lines, notices (engine and automation deliveries), compaction banner, breadcrumb chain in subagents, the one activity line, reconnecting banner, error steps | the chat family (plans/native.md §8.4) over the same `fold()` blocks: `message`, `thinking`, `toolcard` (nested `transcript` for subagents; "open" pushes the child full screen), `step`, `notice`, `activity`; compaction and reconnect as notices; breadcrumbs in the title |
| **Asking** | approval card (approve/deny), question from `run.result` answered by the next message | `approval` and `question` primitives inline; answering focuses the composer |
| **Composer** | text, Enter/Shift+Enter, 🔒/🌐 tool mode per user (`/api/xbin/prefs/toolset`), 📎 attach + paste + drop (16 MiB/file), upload chips, Stop (queued text returns to the composer), Send, queue strip (take back a queued message), placeholder by state (view only / steer / answer / follow up), held ask while attachments upload | `composer`: tool-mode chip, attachments from Photos/camera/Files uploaded by the app to `PUT /runs/{id}/upload` (plans/native.md §8.4), dictation, Stop returns queued text, queued messages as chips with "take back", the same placeholder states |
| **Top bar** | Automations crumb, title, tool-mode badge, status badge, view-only badge, Retry, Compact, Learn skill, Memory (n), Files (n), ⑂ tree, Share/Shared, Delete | title (tap to rename), status + mode badges; the rest in a toolbar menu; Retry surfaces inline when a run failed |
| **Run tools** | Memory, Files (with editor, versions, render pane), Skills, the workflow tree pane (cost bars, stop subtree), the render preview (sandboxed, CSP, maximize) | pushed screens: Memory, Files (viewer/editor, share/export), Skills, Workflow tree (with Stop); the render preview is a `canvas html=` island (no-script CSP), full screen |
| **Sharing** | dialog: visibility (only invited / team can view / team can reply), members with roles, invite links shown once with revoke, Leave | a sheet with the same controls; links go to the share sheet; `#join=` links open the app |
| **Automations** | page with Schedules, Watchers, Channels, Triggers; cards with unread/attention; detail with paged runs; schedule form with cadence presets; channel claim/pairing/people/sessions/undelivered/rules; trigger form, test fire, unmatched pushes | a pushed Automations screen: sections per kind, the same cards (badges), detail screens, native forms; destructive actions confirmed natively |
| **Managers** | ⚙ Settings (Config model per tier, Features, Memory, Files, Skills, MCP), ⏻ halt all (while runs are active) | Settings screen for managers; halt as a destructive toolbar action with confirmation |
| **States** | halted (423 for non-managers), view-only, reconnecting, retry on error/canceled | the same states, same wording |
| **Deep links** | `#c=<id>`, `#auto[=kind:id]`, `#join=<token>` | `xbin://<ws>/c/<agent>#c=…` etc. route into the same model router |

## 2. Architecture — one model, two thin views

The reason the web UI is good is the model under it (`fold`, `Session`,
`ConvList`, the automations registry), not the lit templates. So the native
mode shares the model and adds a second view:

```
builtin-templates/agent/
  model/          pure JS, no lit, no DOM — shared
    fold.js         (today chat-fold.js)        transcript blocks
    tool-heads.js   (as today)                  tool headlines/families
    conv-groups.js  (as today)                  sidebar grouping
    stream.js       (as today, Live)            SSE with resume
    conv-list.js    (as today, ConvList)        paging, search, apply(event)
    session.js      (chat-view.js minus template()) the open conversation
    actions.js      (out of agent.js)           send/upload/answer/approve/stop/control/share
    rules.js        (out of agent.js/sidebar.js) who may do what: top-bar, composer state, halt, menus
    router.js       (out of agent.js)           #c / #auto / #join
    auto.js         (AutoPage state + registerKind, out of automations.js)
    features.js     the FEATURES registry (§5)
  agent.js, chat-cards.js, sidebar.js, …   today's lit views, where they are — behaviour unchanged
  chat-fold.js, conv-list.js, …            one-line re-exports of their model/ successors
  native.js       the native views (vocabulary templates) over model/
  index.html      unchanged
```

- **The extraction is behaviour-preserving** and lands first (milestone 1),
  with every existing test (`test/*.mjs`, the node tests) still green — the web
  UI must not change by a pixel.
- **Why sharing is safe in the runtime:** the native runtime is a real (hidden)
  WebKit document with the same injection as the web frame (plans/native.md
  §7.2) — `window.xbin`, `bx-kit`'s `api()` (its `sandboxed()` check finds the
  injected meta) and even lit imports behave as in the browser. The split is
  about sharing logic cleanly, not about making modules loadable.
- **Instances (D72):** customised copies keep working — the web view files
  stay at their paths (so instance patch series still apply), modules that
  move into `model/` leave a re-export at the old path, and `HOME` and the
  other hooks live in `model/` where both views import them.

## 3. The native view

`native.js` renders from the model, one surface at a time (plans/native.md
§15 — no split views, even on the Duo or iPad):

```js
// builtin-templates/agent/native.js — shape, not the final code
import { html, render, repeat, nothing } from '/vendor/xb-native.js';
import { app } from './model/app.js';          // Session, ConvList, AutoPage, rules, router
import { blocks } from './model/fold.js';

app.on('change', () => paint());               // batched per frame by xb-native

const transcriptTpl = (s) => html`
  <transcript follow ?older=${s.hasOlder} @more=${() => s.loadOlder()}>
    ${repeat(blocks(s), (b) => b.id, blockTpl)}
    ${s.activity ? html`<activity text=${s.activity.text} ?live=${s.activity.live}/>` : nothing}
  </transcript>`;

const chatScreen = (s) => html`
  <screen title=${s.title} subtitle=${s.statusText}>
    <toolbar>
      <button icon="list" @tap=${app.openDrawer}>Conversations</button>
      <menu icon="ellipsis">${runMenu(s)}</menu>
    </toolbar>
    ${transcriptTpl(s)}
    <composer value=${s.draft} placeholder=${app.rules.placeholder(s)} ?busy=${s.busy}
              ?disabled=${!app.rules.canTalk(s)} attachments=${s.uploads}
              upload=${{ method: 'PUT', path: `/runs/${s.id}/upload?name={name}` }}
              @input=${(e) => s.setDraft(e.value)} @send=${(e) => app.actions.send(s, e.value)}
              @stop=${() => app.actions.stop(s)} @uploaded=${(e) => s.attached(e)}>
      <button icon=${s.toolset === 'web' ? 'globe' : 'lock'} @tap=${() => app.actions.toggleToolset()}>
        ${s.toolset === 'web' ? 'web' : 'internal'}</button>
    </composer>
  </screen>`;
```

Block mapping (`fold()` kinds → primitives): `user` → `message role=user`
(sender label when shared), `assistant`/`draft` → `message role=assistant
markdown` (`streaming` while drafting), `think` → `thinking`, `tool` →
`toolcard` (headline/family from `tool-heads.js`, state from `resultState`),
`agent` → `toolcard family=agent` with a nested `transcript` of the folded
child (tap "open" pushes the child full screen), `step` → `step`, `notice` →
`notice`; the approval/question states → `approval`/`question`.

The conversations drawer is a `sheet` from the leading edge (the renderer
presents it as a drawer), listing `ConvList` groups with swipe actions; picking
a row routes and closes it.

## 4. Performance — streaming at 120 Hz on 1k-message conversations

Today every `text`/`thinking` draft event carries the **whole** answer so far
(O(n²) bytes on a fast stream), `/runs/{id}/view` is **unpaged** (every
message, compacted ones included; tool results up to 64 KiB), every paint
re-runs `fold()` over the whole transcript and re-renders markdown for every
assistant block, and images come through blob URLs. The web copes; a phone
over the bridge needs better. All server changes are additive:

| Change | Where |
|---|---|
| `GET /stream?deltas=1` sends `text.delta`/`thinking.delta` (appended text) instead of full text; old clients unchanged | backend `draft.go`, `stream.go`; `model/stream.js` handles both |
| `GET /runs/{id}/view?before=<seq>&limit=<n>` pages messages/steps from the end; the transcript's `more` loads older | backend `stream.go`; `model/session.js` |
| `GET /runs/{id}/thumb?path=&w=` — sized image thumbnails (the app loads them with the frame token) | backend `files.go` |
| `fold()` cached per block id; markdown tokens cached per (block id, text) in the runtime | `model/fold.js`, xb-native |
| the bridge carries patches only; lists are lazy in SwiftUI | xb-native, renderer |

Targets, checked on a device from milestone 2: a streamed answer patches at
the display rate with no dropped frames; opening a 1k-message conversation
shows the latest screenful in < 300 ms; memory stays flat while streaming.

## 5. Keeping it that good — parity enforcement

- **One registry.** `model/features.js` lists every feature key (the §1 rows,
  finer grained: `conv.pin`, `chat.queue.takeBack`, `auto.trigger.testFire`,
  …). Both views declare what they implement (`web/…` and `native.js` each
  export `IMPLEMENTS`); a node test fails when a key is missing from either
  side unless it is listed as an intended difference with a reason.
- **One fixture set, both views.** The `test/backend.mjs` STUB seeds that drive
  today's web tests also drive native tree tests: xb-native's JSON target
  renders `native.js` in node against the stubbed `xbin`, and the tests assert
  semantics (the approval card has approve/deny, a failed run offers Retry, a
  view-only chat has a disabled composer, a shared chat labels senders).
- **Screenshots both ways.** A UI-harness pass renders `native.js` through the
  Lit reference renderer at 390×844 (light/dark); iOS CI snapshots the key
  agent screens from fixtures; a contact sheet puts web and iOS side by side
  per screen.
- **The rule, written down** in the template's own docs (and API.md for
  instances): a UX change lands in the model and **both** views in the same
  commit; a view-only change must say why it is view-specific.

## 6. Platform additions it needs

- **`POST /api/xbin/notify {user, title, body, link}`** — the backend's
  Needs-you moments (a question, an approval, a failed automation) reach the
  person's phone through the push relay (plans/native.md §14). Additive,
  rate-limited, limited to users who can read the tile.
- The runtime, vocabulary and bridge of plans/native.md; nothing
  template-specific beyond §4's backend routes.

## 7. Milestones

1. **Model extraction** — `model/` + `web/`, the web unchanged, all tests green.
2. **Read-only native chat** — transcript from `fold()`, streaming via deltas,
   paging, markdown tokens, thinking/tool/subagent cards.
3. **Talking** — composer, attachments (app uploads), approvals, questions,
   stop/queue, tool mode.
4. **Conversations** — drawer, groups, search + snippets, pin/archive/delete,
   rename, sharing + join links.
5. **Automations** — all four kinds with their forms and details.
6. **Run tools** — memory, files (+ editor), skills, workflow tree, render
   preview, settings for managers, halt.
7. **Needs you** — `/api/xbin/notify` + push + the app inbox.
8. **Parity sign-off** — the FEATURES test green with no unexplained gaps, the
   contact sheet reviewed, performance targets met on a device.

## 8. Risks

- **The extraction touches a live, customised tile** — mitigated by keeping
  web file names and behaviour, and by D72's upstreaming recipe for instances.
- **Bridge throughput while streaming** — deltas + per-block caching + patches;
  measured from milestone 2.
- **Markdown fidelity** — the web renders with marked + sanitizer
  (`chat-md.js`); the native side renders marked's tokens, so both agree on
  structure; the security rules carry over (no remote images, safe link
  schemes, links only with `cap:open-links`).
- **Render preview** — a no-script `canvas` island needs the same CSP as the
  web's sandboxed iframe; verified by a fixture with hostile HTML.

## 9. Decisions for review

1. **Model/view split** in the template with a FEATURES registry and the
   "both views in one commit" rule (recommended) vs a separately maintained
   native frontend.
2. **Server additions** — `/stream?deltas=1`, paged `/view`, thumbnails
   (recommended; all additive).
3. **`POST /api/xbin/notify`** as a platform API rather than an agent-only
   route (recommended).
4. **Drawer, not split**, for conversations on every device (per the owner's
   one-surface rule; recommended).
