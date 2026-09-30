# The frontend kit — what a tile may import from `/vendor/`

Every shipped frontend module lives under `/vendor/` beside the vendored
libraries (lit, xterm, marked, highlight.js, qrcode-generator — the last at
`/vendor/qrcode.mjs`), served unauthenticated so a sandboxed tile frame can
load it. This page says which of those modules are **for tiles**, which are
**shell chrome**, and the rules that keep the URLs stable across xbind
upgrades ([compat.md](/docs/compat.md) rule 3).

Import by **absolute URL** — never a bare specifier. The import map lives
in each workspace's `xbin.json` and no upgrade rewrites it, so a bare name
that works in a fresh workspace 404s in an older one:

```js
import { xbinApi, jbody, esc } from '/vendor/bx-kit.js';
import '/vendor/bx-frame.js';
```

## Modules tiles may import

| Module | What it is |
|---|---|
| `/vendor/bx-kit.js` | the helper kit: `api(url, opts)` (JSON out, throws the server's `error`; in a sandboxed tile the request carries the frame token — your tile's identity — in chrome the session cookie, i.e. the signed-in human), `xbinApi('/grants')`, `selfApi('/runs')` (your own backend), `jbody(value, method?)`, `esc(text)` (HTML-escapes `&<>"'`: safe in text and in attribute position), `deepActive()`, `pathHas(event, selector)`, `clampBox(box, {minW, minH, margin})`, `anchorBox(rect, {dx, dy, w, h}, bounds?)` / `anchorOffsets(rect, box)` (a window anchored to an element, as a viewport box and back; `bounds` fences its top-left), `followBox(target, box, alive)` (the animation-frame loop that keeps such a window on its anchor), `dragPointer({cursor, onMove, onUp})` (a window-level pointer drag with the iframe shield), `dragWindow(ev, el, {bounds, onMove, onUp})` (a title-bar drag of a fixed window, fenced by `bounds`), `dragShield(cursor)`, `sandboxed()` |
| `/vendor/bx-frame.js` | `<bx-frame src="apps/x">` — embed another tile (with its terminal pop-up); exports `clampBox` for compatibility. `src` may name a tile deployment, `apps/x+dev`: the frame loads `/c/apps/x+dev/` with a frame token for that deployment, never as chrome, and reloads and paints its build overlay from the tile's `deployments` events ([tile-deployments.md](/docs/tile-deployments.md)). Or keep `src` the tile and set `deployment="dev"`: only the page follows it — its terminal pop-up, code, logs and proposals stay the tile's (the shell's windows do this). `open(layout)` also takes `'deployments'`, the window's Deployments panel (a layout never restored after a reload: the window comes back on the terminal) |
| `/vendor/bx-terminal.js` | `<bx-terminal src="…">` — a terminal on any endpoint speaking the terminal wire, dialled with your frame token: a sandbox manager's `tty` through your bound interface, your own pty route ([elements.md](/docs/elements.md) §`<bx-terminal>`). Its `/ws/term` attributes (`cwd`, `net`, …) are the shell's |
| `/vendor/bx-dialog.js` | `<bx-dialog>` — a modal; `xbin.dialog()` falls back to it outside the shell |
| `/vendor/bx-grants.js`, `/vendor/bx-bindings.js` | the owner's grant-approval and interface-binding panels |
| `/vendor/bx-multiselect.js` | `<bx-multiselect>` — a compact multi-pick control |
| `/vendor/bx-netrules.js` | the network-set rule grammar (`RULE_KINDS`, `parseRule`, `fmtRule`, `ruleProblem`, `netOptions`, …) — interpreted client-side exactly as the server defines it (D54) |
| `/vendor/bx-allow.js` | the allowance grammar (`ALLOW_KINDS`, `parseAllow`, `fmtAllow`, `allowProblem`, `describeAllow`, `capInfo`) (D57) |
| `/vendor/bx-grant-row.js` | `grantArrow(g)` — a grant or request's `from → target` with the policy-block / capability tooltip, as rendered by the shell, the admin console and the organisations tile |
| `/vendor/bx-code.js` | `<bx-code>` (file tree + highlighted viewer + diffs); exports `diffHTML`, `diffStats`, `hl`, `langFor` |
| `/vendor/events-socket.js` | `onEvent(cb)` — every `/ws/events` frame goes to `cb(e)` over the one shared socket; returns an unsubscribe. Filter on `e.type` yourself |
| `/vendor/theme.css` | the design tokens (`--bx-bg`, `--bx-panel`, `--bx-text`, …) plus opt-in `.bx` control styles, and the workspace's thin scrollbars (D123): on a mouse/trackpad a 6px bar whose thumb is drawn 3px and fattens under the pointer, the scroller the next wheel or key would move tinted amber (`[data-bx-scroll]`); touch keeps its native bars. Link it to take the theme; it is **never injected** into your document. A document that links it also gets the focused-scroll tracker (below) from `xbin-client.js`; `<meta name="xbin-scroll-focus" content="off">` opts out |
| `/vendor/bx-scroll.js` | the scrollbar CSS for **shadow roots** (a document stylesheet does not reach them): `scrollCssText`, the same rules `theme.css` carries — a lit element puts `unsafeCSS(scrollCssText)` (or `/vendor/scroll-css.js`'s shared `scrollCss`) in its `static styles`. Importing it installs `installScrollFocus()` once per document: it keeps `data-bx-scroll` on the scroller the next scroll would move — the innermost scrollable under the mouse, the one a wheel actually latches onto (at its end, the next one out), or the focused element's on a scroll key — and hands off to/from framed documents (`xbin:scroll-focus`, [protocol.md](/docs/protocol.md)). Dependency-free |
| `/vendor/scroll-window.js` | `ScrollWindow` — a long list that renders a window of its rows and never moves what the reader is looking at (D124, D130; the Agent tab's, framework-free): `attach(scroller)` (it sets `overflow-anchor: none`), then `before()` right before every render and `after()` right after it, in the same frame — the bottom stays pinned while the reader is there, else the first visible row keeps its place. Rows are the scroller's direct children with a key (`rows`: a selector, default `:scope > [data-k]`; `keyOf(el)`). `wantsAbove()` / `wantsBelow()` say when to render (or fetch) more rows, `trimAbove()` / `trimBelow()` the key of the last row to keep once more than 6 views of rows lie beyond the view (keep 2.5: never ping-pongs with the 1.5-view fill); `onScroll`, `onResize`, `atBottom`, `keepView`, `toBottom()`, `firstVisible()`, `rowsPerView()`. A touch scroll grows the window only once it settles. Dependency-free |
| `/vendor/xb-native.js` | a tile's **native UI** for the xbin mobile app: `html`, `render`, `repeat`, `nothing` (lit-shaped), and `widget` (the tile's card on the app's screens) over the native vocabulary (`/vendor/xb/vocab.js`: `screen`, `section`, `row`, `field`, `button`, …). A tile's `native.js` imports it; the app runs that file in a hidden document with the tile's own identity and draws what it renders with platform UI, re-rendering by patches. Also exports `native` — in the app the same object as `xbin.native` (`caps`, `supports()`, `meta()`, `copy()`, `share()`, `open()`, `state`, `saveState()`, `widgetSize`, `on('widgetsize')`). Outside the app nothing loads `native.js`; browsers keep showing `index.html`. The reference — templates, every primitive, the rules — is [native.md](/docs/native.md). Worked examples: `examples/counter-go/native.js` (one round trip, and a widget) and `examples/calendar/native.js` (a list, a form, a bus refresh); the builtin chat, egress-approver, prometheus-viewer, s3-archiver and webhooks tiles ship one too — logic a page and its native view share lives in a plain module both import (chat's `chat-core.js`, the viewer's `prom.js`) |
| `/vendor/xbin-client.js` | injected into every tile document by xbind — do not import it yourself. Besides `xbin.self` (the tile path in every deployment) it sets `xbin.deployment` only in a document of a tile deployment other than the primary (`/c/<tile>+<name>/`): its name; absent means the primary. Likewise `xbin.partition`, only in a partitioned tile's document: the partition the viewer reaches, `user:<id>` or `global` ([partitions.md](/docs/partitions.md), in development) |

## Shell-only modules

`/vendor/bx-menu.js` (the shell's context menus — action closures, not a
tile API; tiles use `xbin.dialog` / `xbin.window`), `/vendor/bx-logs.js`,
`/vendor/bx-prs.js` (the terminal pop-up's panels — reachable through
`<bx-frame>`; `<bx-logs deployment="<name>">` shows a non-primary tile
deployment's log, and nothing unless the server echoes it — on a
partitioned tile `<bx-logs partition="global|user:<id>">` asks for the
global instance's log or a person's shared one under the same echo rule
(`X-XBin-Partition`), and its corner offers what the viewer may read
([partitions.md](/docs/partitions.md) §Operating people's partitions) — and
`<bx-terminal deployment="<name>">` opens sessions targeting one — the
server's echo is what it shows), `/vendor/bx-agent.js` (the pop-up's Agent
tab, D74) with `/vendor/bx-md.js` (its hardened markdown renderer),
`/vendor/agent-fold.js`, `/vendor/agent-pages.js` (the session log folded,
and the pages of it the tab holds, D124/D130), `/vendor/agent-tools.js`,
`/vendor/agent-cards.js`, `/vendor/agent-slash.js`,
`/vendor/agent-testapi.js` (its test surface) and
`/vendor/frame-titlebar.js` (the pop-up's title bar), `/vendor/term-predict.js`
(the terminal's prediction engine, D70), `/vendor/term-src.js` (where
`<bx-terminal src>` connects and when it reconnects), `/vendor/term-sessions.js` (the
frame's view of the terminal session directory, D73),
`/vendor/frame-deploy.js` (the terminal window's live reload controls: the
`⇈` entry, the chip and its menu, Reload now, the launcher's banner, the
grey lines in open terminals —
[tile-deployments.md](/docs/tile-deployments.md)) with
`/vendor/deploy-state.js` (its pure view model: which chip, menu items and
tile API select entries a tile's state and a viewer's permissions yield, and
every string they show, from `GET /api/xbin/deployments`; imports only
`/vendor/deploy-branch.js`, the pure words and offers of branch-assigned
deployments),
`/vendor/bx-deploy.js` (defines `<bx-deployments component="<tile>">`, the
terminal window's Deployments panel: live reload, a tile's deployments,
promotion with the diff first, the deploy log, data, vault, deliveries,
edges and registrations; its host frame, the `frame` property, asks
every question; `target` — `primary`, a deployment's name or `off` — is the
active tab's, whose row it tags `Dev API`) with `/vendor/deploy-panel.js` (the panel's pure view
model, beside `deploy-state.js`), `/vendor/frame-panels.js` (the terminal
window's body: which panel shows, the terminal beside it, the divider —
D129), `/vendor/frame-testapi.js` (`<bx-frame>`'s
test surface for the UI harness), and the shell's own siblings under
`shell/`. They are served, and they will keep being served, but their
shapes follow the shell.

## Native preview modules

`/vendor/xb/render.js` — `<xb-view>`, the reference renderer of the native
vocabulary: it draws a native tree (or the runtime's `mount`/`patch`
messages) the way the xbin app does, light or dark, default or large text,
and reports the user's taps and typing as the app would.
`/vendor/xb/preview-host.js` puts it to work inside a tile's native runtime
document (`/c/<tile>/?native=1&preview=1`), so a browser shows the tile's
native UI; `/vendor/xb/fixture.html?tree=<url>` draws one tree. They exist
for previews and tests — a tile never imports them, and they follow the
app's look rather than a frozen API.

## Rules

- **URLs are frozen.** A module served today stays at its path; a helper
  that moves leaves a re-export behind (`clampBox` in `bx-frame.js`).
- **One home per helper.** `make js-check` refuses a second definition of
  a kit helper (`api`, `jbody`, `esc`, `deepActive`, `pathHas`, `clampBox`)
  or of the code viewer's highlight helpers anywhere in the shipped trees.
- **Theme fallbacks stay.** Core elements carry `var(--bx-x, <literal>)`
  fallbacks so a document that never linked `theme.css` still renders;
  `make theme-check` keeps every literal equal to `theme.css`. Your own
  document decides its theme: link the sheet, or set the tokens yourself.

## lit pitfalls met in this codebase

- A plain field (not in `static properties`) never re-renders on its own —
  call `this.requestUpdate()` after changing one that the template reads.
- `?selected` on `<option>` never *resets* a `<select>` back to a value the
  DOM already moved away from; set `.value` on the select after render.
- Re-creating an `<iframe>` (a changed `sandbox` attribute applies only to
  the next navigation) needs lit's `keyed()` — see `bx-frame`.
- `e.target` is retargeted at shadow boundaries: an `<input>` inside a
  nested element never matches `e.target.closest('input')`; use
  `pathHas(e, 'input')` from the kit.
- `<select>` dropdowns never render in headless screenshots; the harness
  dumps their options to text instead.
