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
| `/vendor/bx-frame.js` | `<bx-frame src="apps/x">` — embed another tile (with its terminal pop-up); exports `clampBox` for compatibility. `src` may name a tile deployment, `apps/x+dev`: the frame loads `/c/apps/x+dev/` with a frame token for that deployment, never as chrome, and reloads and paints its build overlay from the tile's `deployments` events ([tile-deployments.md](/docs/tile-deployments.md)). `open(layout)` also takes `'deployments'`, the window's Deployments panel (a layout never restored after a reload: the window comes back on the terminal) |
| `/vendor/bx-dialog.js` | `<bx-dialog>` — a modal; `xbin.dialog()` falls back to it outside the shell |
| `/vendor/bx-grants.js`, `/vendor/bx-bindings.js` | the owner's grant-approval and interface-binding panels |
| `/vendor/bx-multiselect.js` | `<bx-multiselect>` — a compact multi-pick control |
| `/vendor/bx-netrules.js` | the network-set rule grammar (`RULE_KINDS`, `parseRule`, `fmtRule`, `ruleProblem`, `netOptions`, …) — interpreted client-side exactly as the server defines it (D54) |
| `/vendor/bx-allow.js` | the allowance grammar (`ALLOW_KINDS`, `parseAllow`, `fmtAllow`, `allowProblem`, `describeAllow`, `capInfo`) (D57) |
| `/vendor/bx-grant-row.js` | `grantArrow(g)` — a grant or request's `from → target` with the policy-block / capability tooltip, as rendered by the shell, the admin console and the organisations tile |
| `/vendor/bx-code.js` | `<bx-code>` (file tree + highlighted viewer + diffs); exports `diffHTML`, `diffStats`, `hl`, `langFor` |
| `/vendor/events-socket.js` | `onEvent(cb)` — every `/ws/events` frame goes to `cb(e)` over the one shared socket; returns an unsubscribe. Filter on `e.type` yourself |
| `/vendor/theme.css` | the design tokens (`--bx-bg`, `--bx-panel`, `--bx-text`, …) plus opt-in `.bx` control styles. Link it to take the theme; it is **never injected** into your document |
| `/vendor/xb-native.js` | a tile's **native UI** for the xbin mobile app: `html`, `render`, `repeat`, `nothing` (lit-shaped) over the native vocabulary (`/vendor/xb/vocab.js`: `screen`, `section`, `row`, `field`, `button`, …). A tile's `native.js` imports it; the app runs that file in a hidden document with the tile's own identity and draws what it renders with platform UI, re-rendering by patches. Also exports `native` — in the app the same object as `xbin.native` (`caps`, `supports()`, `meta()`, `copy()`, `share()`, `open()`, `state`, `saveState()`). Outside the app nothing loads `native.js`; browsers keep showing `index.html`. The reference — templates, every primitive, the rules — is [native.md](/docs/native.md). Worked examples: `examples/counter-go/native.js` (one round trip) and `examples/calendar/native.js` (a list, a form, a bus refresh); the builtin chat, egress-approver, prometheus-viewer, s3-archiver and webhooks tiles ship one too — logic a page and its native view share lives in a plain module both import (chat's `chat-core.js`, the viewer's `prom.js`) |
| `/vendor/xbin-client.js` | injected into every tile document by xbind — do not import it yourself. Besides `xbin.self` (the tile path in every deployment) it sets `xbin.deployment` only in a document of a tile deployment other than the primary (`/c/<tile>+<name>/`): its name; absent means the primary |

## Shell-only modules

`/vendor/bx-menu.js` (the shell's context menus — action closures, not a
tile API; tiles use `xbin.dialog` / `xbin.window`), `/vendor/bx-terminal.js`,
`/vendor/bx-logs.js`, `/vendor/bx-prs.js` (the terminal pop-up's panels —
reachable through `<bx-frame>`; `<bx-logs deployment="<name>">` shows a
non-primary tile deployment's log, and nothing unless the server echoes it,
and `<bx-terminal deployment="<name>">` opens sessions targeting one —
the server's echo is what it shows), `/vendor/bx-agent.js` (the pop-up's Agent
tab, D74) with `/vendor/bx-md.js` (its hardened markdown renderer) and
`/vendor/frame-titlebar.js` (the pop-up's title bar), `/vendor/term-predict.js`
(the terminal's prediction engine, D70), `/vendor/term-sessions.js` (the
frame's view of the terminal session directory, D73),
`/vendor/frame-deploy.js` (the terminal window's live reload controls: the
`⇈` entry, the chip and its menu, Reload now, the launcher's banner, the
`📌 pinned` chip over a tile, the grey lines in open terminals —
[tile-deployments.md](/docs/tile-deployments.md)) with
`/vendor/deploy-state.js` (its pure view model: which chip, menu items and
tile API select entries a tile's state and a viewer's permissions yield, and
every string they show, from `GET /api/xbin/deployments`; imports nothing),
`/vendor/bx-deploy.js` (defines `<bx-deployments component="<tile>">`, the
terminal window's Deployments panel: live reload, a tile's deployments,
promotion with the diff first, the deploy log, data, vault, deliveries,
edges and dormant registrations; its host frame, the `frame` property, asks
every question) with `/vendor/deploy-panel.js` (the panel's pure view
model, beside `deploy-state.js`), `/vendor/frame-testapi.js` (`<bx-frame>`'s
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
