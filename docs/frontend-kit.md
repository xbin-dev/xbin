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
| `/vendor/theme.css` | the design tokens (§Theme below: Concrete Night and Concrete Day, D184), the self-hosted fonts, opt-in `.bx` base and control styles, and the workspace's thin scrollbars (D123): on a mouse/trackpad a 6px bar whose thumb is drawn 3px and fattens under the pointer, the scroller the next wheel or key would move tinted in the focus colour (`[data-bx-scroll]`); touch keeps its native bars. Link it to take the theme; it is **never injected** into your document, and your document turns light only when it opts in (`<html data-bx-theme="auto">`). A document that links it also gets the focused-scroll tracker (below) from `xbin-client.js`; `<meta name="xbin-scroll-focus" content="off">` opts out |
| `/vendor/bx-theme.js` | a document's appearance, for code that paints or follows the person (§Theme): `appearance()`, `setAppearance()`, `follows()`, `scheme()`, `token(name, el)`, `onAppearance(cb)`. Dependency-free |
| `/vendor/bx-icons.js` | `<bx-icon name="lock">` and `iconSvg(name, {size, label})`: the workspace's drawn glyphs, 16px in `currentColor` (§Theme › Icons). Dependency-free |
| `/vendor/fonts/` | Instrument Sans, JetBrains Mono and Bricolage Grotesque (woff2, SIL OFL 1.1, the licences beside them); `theme.css` loads them, each URL with its version, which xbind serves as immutable — a reload draws them from the cache. Use the `--bx-sans`, `--bx-mono` and `--bx-display` tokens rather than naming them |
| `/vendor/bx-scroll.js` | the scrollbar CSS for **shadow roots** (a document stylesheet does not reach them): `scrollCssText`, the same rules `theme.css` carries — a lit element puts `unsafeCSS(scrollCssText)` (or `/vendor/scroll-css.js`'s shared `scrollCss`) in its `static styles`. Importing it installs `installScrollFocus()` once per document: it keeps `data-bx-scroll` on the scroller the next scroll would move — the innermost scrollable under the mouse, the one a wheel actually latches onto (at its end, the next one out), or the focused element's on a scroll key — and hands off to/from framed documents (`xbin:scroll-focus`, [protocol.md](/docs/protocol.md)). Dependency-free |
| `/vendor/scroll-window.js` | `ScrollWindow` — a long list that renders a window of its rows and never moves what the reader is looking at (D124, D130; the Agent tab's, framework-free): `attach(scroller)` (it sets `overflow-anchor: none`), then `before()` right before every render and `after()` right after it, in the same frame — the bottom stays pinned while the reader is there, else the first visible row keeps its place. Rows are the scroller's direct children with a key (`rows`: a selector, default `:scope > [data-k]`; `keyOf(el)`). `wantsAbove()` / `wantsBelow()` say when to render (or fetch) more rows, `trimAbove()` / `trimBelow()` the key of the last row to keep once more than 6 views of rows lie beyond the view (keep 2.5: never ping-pongs with the 1.5-view fill); `onScroll`, `onResize`, `atBottom`, `keepView`, `toBottom()`, `firstVisible()`, `rowsPerView()`. A touch scroll grows the window only once it settles. Dependency-free |
| `/vendor/xb-native.js` | a tile's **native UI** for the xbin mobile app: `html`, `render`, `repeat`, `nothing` (lit-shaped), and `widget` (the tile's card on the app's screens) over the native vocabulary (`/vendor/xb/vocab.js`: `screen`, `section`, `row`, `field`, `button`, …). A tile's `native.js` imports it; the app runs that file in a hidden document with the tile's own identity and draws what it renders with platform UI, re-rendering by patches. Also exports `native` — in the app the same object as `xbin.native` (`caps`, `supports()`, `meta()`, `copy()`, `share()`, `open()`, `state`, `saveState()`, `widgetSize`, `on('widgetsize')`). Outside the app nothing loads `native.js`; browsers keep showing `index.html`. The reference — templates, every primitive, the rules — is [native.md](/docs/native.md). Worked examples: `examples/counter-go/native.js` (one round trip, and a widget) and `examples/calendar/native.js` (a list, a form, a bus refresh); the builtin chat, egress-approver, prometheus-viewer, s3-archiver and webhooks tiles ship one too — logic a page and its native view share lives in a plain module both import (chat's `chat-core.js`, the viewer's `prom.js`) |
| `/vendor/xbin-client.js` | injected into every tile document by xbind — do not import it yourself. Besides `xbin.self` (the tile path in every deployment) it sets `xbin.deployment` only in a document of a tile deployment other than the primary (`/c/<tile>+<name>/`): its name; absent means the primary. Likewise `xbin.partition`, only in a partitioned tile's document: the partition the viewer reaches, `user:<id>` or `global` ([partitions.md](/docs/partitions.md)) |

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
`/vendor/agent-css.js` (its styles), `/vendor/agent-testapi.js` (its test
surface) and
`/vendor/frame-titlebar.js` (the pop-up's title bar), `/vendor/term-predict.js`
(the terminal's prediction engine, D70), `/vendor/term-src.js` (where
`<bx-terminal src>` connects and when it reconnects),
`/vendor/term-chrome.js` (the terminal's own markup and styles, and
xterm's loader), `/vendor/term-palettes.js` (the terminal settings menu's
named palettes, D184), `/vendor/term-sessions.js` (the
frame's view of the terminal session directory, D73),
`/vendor/frame-deploy.js` (the terminal window's live reload controls: the
`deploy` entry, the chip and its menu, Reload now, the launcher's banner, the
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
D129), `/vendor/frame-css.js` (`<bx-frame>`'s own styles),
`/vendor/frame-testapi.js` (`<bx-frame>`'s
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

## Theme

The workspace draws in two themes from the Base Two system (D184): **Concrete
Night** (dark) and **Concrete Day** (light). Every colour, font, corner and
shadow is a custom property in `/vendor/theme.css`; everything else uses
`var(--bx-…)`. A person picks one in the shell's settings — *Theme*: System,
Light or Dark, and *Density*: Compact or Comfortable — and System follows
the device's light or dark setting. This section is the mechanism; how a
tile should look with it (the rules, the components, the words) is
[design.md](/docs/design.md).

### Opting in

```html
<!doctype html>
<html lang="en" data-bx-theme="auto">
<head>
  <meta charset="utf-8">
  <link rel="stylesheet" href="/vendor/theme.css">
</head>
<body class="bx">
```

- `data-bx-theme="auto"` makes the document **follow the person**: their
  choice, or the system's when they chose System. It is right in the first
  painted frame, with no script of yours: xbind's document injection adds
  `<meta name="xbin-theme">` / `<meta name="xbin-density">` when the person
  chose something ([protocol.md](/docs/protocol.md)), and the sheet reads
  them and `prefers-color-scheme`. Later changes reach a framed document as
  `xbin:appearance` from its embedder, which `xbin-client.js` applies; a
  page open in a browser tab of its own picks a change of the setting up on
  reload, and follows the system live. No other value of the attribute is
  defined.
- A document that links the sheet **without** the attribute gets Concrete
  Night — new values under the same names — so a tile written for the old
  dark palette, light text hard-coded, stays legible. It turns light only
  when it opts in. Two things there stay as they were: the old status
  names keep their old values (below), and `button.primary`, `.quiet`,
  `.danger` and the 28 px control size are left to the document (Controls,
  below).
- A document that doesn't link the sheet: the core elements in it render
  from their fallbacks, which are Night.
- `<body class="bx">` takes the base: the panel background, the text colour
  and the UI type (13/18; 14/20 comfortable).

Then look at your tile in both themes (the system setting, or the shell's
settings) and keep every colour on a token.

### Theme precedence

What a document that follows the person shows, first match wins (D188):

1. **The device** — the shell's settings, *This device*: Follow my setting,
   Light or Dark. Light or Dark holds in that browser only (its
   `localStorage`, `xbin-theme-device`), whatever the person's Theme says:
   a dark laptop beside a light office screen. Nothing is synced; Follow my
   setting clears it.
2. **The person** — *Theme*: Light or Dark, synced to all their devices
   (the shell's prefs bucket, `theme`).
3. **The system** — *Theme*: System (or no choice): the device's
   `prefers-color-scheme`.

Density is always the person's. A tile needs nothing for this: the device's
theme arrives the way the person's does — in the injected
`<meta name="xbin-theme">` of its first frame (`<bx-frame>` asks for it with
`?xbin-appearance=light|dark` on the tile's URL,
[protocol.md](/docs/protocol.md)) and as `xbin:appearance` when it changes.
The workspace root page applies it before its first paint with
`/vendor/theme-boot.js` (a root from before D188 gets it when the shell
loads); while it is set, the `xbin_theme` hint cookie holds the device's
theme, so xbind's sign-in pages follow it on that browser too.

### Tokens

The values are in `theme.css` itself (Night is its `:root`, Day the block an
opted-in document gets); the names and what they are for:

| Group | Tokens | For |
|---|---|---|
| Surfaces | `--bx-bg` (the canvas), `--bx-sidebar`, `--bx-panel` (windows, cards, bars, menus, `body.bx`), `--bx-panel-2` (inset panels, toolbars), `--bx-hover`, `--bx-selection` / `--bx-selection-text`, `--bx-border` (hairlines), `--bx-border-strong` (inputs, secondary buttons), `--bx-code-bg`, `--bx-canvas-dot` | backgrounds and edges |
| Text | `--bx-text`, `--bx-muted` (secondary, icons at rest), `--bx-subtle` (placeholders, tertiary) | |
| Accent | `--bx-accent`, `--bx-accent-hover`, `--bx-accent-ink` (text and icons on the accent), `--bx-link` | primary actions, selection, prose links: nothing else |
| Focus | `--bx-focus`, `--bx-focus-gap`, `--bx-focus-width`, `--bx-focus-offset`, `--bx-focus-outline`, `--bx-focus-halo` | the focus ring |
| Status | `--bx-ok`, `--bx-warn`, `--bx-danger`, `--bx-info`, each with a `-bg` tint | always with an icon and a word |
| Old names | `--bx-green`, `--bx-amber`, `--bx-red` | text colours that keep working. In a document that opted in they are ok, warn and danger; in one that didn't they keep their values from before Base Two (`#4CAF50`, `#F2A71B`, `#EF5350`), and `--bx-ok` / `-warn` / `-danger` follow them there (`--bx-info` follows `--bx-muted`). A fill is a `-bg` tint with the status colour or `--bx-text` on it — not an old name, whose Day value is a dark text colour |
| Partition marker | `--bx-part` | yours / shared / global ([partitions.md](/docs/partitions.md)) |
| Window chrome | `--bx-titlebar`, `--bx-titlebar-active`, `--bx-title-text`, `--bx-title-text-inactive`, `--bx-window-border`, `--bx-window-border-active`, `--bx-control-hover`, `--bx-close-hover` / `-ink` | |
| Parts and fields | `--bx-part-tab-shell` / `-terminal` / `-agent` / `-admin` (3px tabs, `--bx-part-tab-h`), `--bx-field-yellow` / `-green` / `-magenta` / `-cobalt` with `-ink`, `--bx-elevated-bg` / `-ink` | brand colour, sparingly (below) |
| Elevation, shape | `--bx-shadow` (a card), `--bx-shadow-rest` / `-active` (windows), `--bx-shadow-pop` (menus, popovers, dialogs, toasts), `--bx-scrim`, `--bx-radius` (2px) | |
| Code | `--bx-syn-keyword`, `-string`, `-number`, `-comment`, `-function`, `-type`, `-attr`, `-builtin`, `-deletion`, `-addition`; `--bx-diff-add`, `-del`, `-hunk`, `-context`, each with `-bg` | highlighted code and diffs |
| Terminal | `--bx-term-bg`, `-fg`, `-cursor`, `-cursor-ink`, `-selection`, and the 16 ANSI colours `--bx-term-black` … `--bx-term-bright-white` | xterm's theme |
| Type | `--bx-sans` (Instrument Sans), `--bx-mono` (JetBrains Mono), `--bx-display` (Bricolage Grotesque); `--bx-font` (the UI shorthand), `--bx-font-micro`, `-meta`, `-ui`, `-body`, `-title`, `-heading`, `-hero`, `-code`, `-number`; `--bx-tracking-micro` / `-heading` / `-hero` | `font: var(--bx-font-…)` |
| Density | `--bx-row` (28px; 32px comfortable), `--bx-control-h`, `--bx-pad`, `--bx-grid` (4px), `--bx-text-size` / `-line`, `--bx-term-size` / `-line`, `--bx-mono-size` (mono beside UI text: ids, paths, times; 12px, 13px comfortable), `--bx-density` | compact or comfortable |
| Layout, motion | `--bx-titlebar-h`, `--bx-topbar-h`, `--bx-statusbar-h`, `--bx-sidebar-w`, `--bx-rail-w`, `--bx-dock-w`, `--bx-elevated-h`; `--bx-ease-out`, `--bx-ease-in-out`, `--bx-dur-instant` / `-ui` / `-panel` / `-window` (0 under reduced motion) | |
| The theme in force | `--bx-scheme` (`dark` or `light`) | for code that paints |

Rules of use:

- **The accent** is for primary buttons, the selection (its background plus
  a 2px accent rule), the active tab's underline and links in prose — never
  for identifiers, paths, status, row actions or decoration.
- **Status** is an icon, a word and its colour, never the colour alone; the
  `-bg` tints are for alert and badge backgrounds. A brand field colour
  never signals status, and it belongs to the workspace's own chrome only:
  the part tabs on system windows, the elevated banner and the first-run
  screen. A tile uses none ([design.md](/docs/design.md) rule 9).
- **Corners** are `var(--bx-radius)` or 0: no pills, no circles (a status
  dot is an 8px square, an avatar a square).
- **Type** is 13px or larger (`var(--bx-font)`, `--bx-font-ui`, `-body`,
  `-title`); 11px only for the micro caps of section labels
  (`--bx-font-micro` with `--bx-tracking-micro`, uppercase), 12px only for
  timestamps and meta (`--bx-font-meta`). Mono for paths, commands,
  hostnames, ids and times; display type for headings and empty states;
  tabular figures (`font-variant-numeric: tabular-nums`) in tables and
  numbers.
- **Elevation**: windows `--bx-shadow-rest` / `-active`, anything that pops
  over `--bx-shadow-pop`, scrims `--bx-scrim`, cards a border.
- **In a shadow root** the document's rules don't reach: give it
  `button, input, select, textarea { font: inherit }`, the code font on
  `pre, code`, `::placeholder { color: var(--bx-subtle); opacity: 1 }` and
  `:focus-visible { outline: var(--bx-focus-outline); outline-offset:
  var(--bx-focus-offset); box-shadow: var(--bx-focus-halo) }`.
- **Fallbacks** are `var(--bx-x, <its Night value>)`, or none.
- **Code that paints** (a canvas, SVG built in JS, xterm) reads tokens with
  `token()` and paints again in `onAppearance()` (below).
- Nothing tuned for one theme only. `make theme-check` keeps the
  workspace's own code to these rules ([maintenance.md](/docs/maintenance.md));
  your tiles are yours, and the rules are how they match.

### Controls

In a `.bx` document (or on a `.bx` element): `button` is a secondary button
(the panel, a `--bx-border-strong` edge, 600 weight), and `input`, `select`
and `textarea` are fields with a strong edge; every interactive element
gets the focus ring on `:focus-visible`; tables get tabular figures;
`.bx-label` is the micro caps label. In a document that **opted in**,
`button.primary` is the accent fill with the accent ink, `button.quiet` text
only, `button.danger` the destructive outline (a danger fill only inside a
confirmation), a disabled button fades, and controls are 28 px (32 px
comfortable). Those wait for the opt-in because a document from before Base
Two may give the same class names a meaning — and sizes — of its own; and
they are written at the base rule's specificity (`.bx button`), so your own
later `button.primary { … }` still wins. An opted-in document also gets
`accent-color` for checkboxes, radios and ranges, and themed placeholders
and text selection.

### A palette of your own

A document may set the tokens itself — its own `:root { … }` after the
`theme.css` link wins in both themes. A tile from before Base Two did it
through the old names (`--bx-bg`, `--bx-panel`, `--bx-panel-2`,
`--bx-border`, `--bx-text`, `--bx-muted`, `--bx-accent`, `--bx-green`,
`--bx-amber`, `--bx-red`, `--bx-term-bg`, …), and in Night the roles Base
Two added on top of them are written from them, so they follow:

| Role | Written from (Night) |
|---|---|
| `--bx-hover` | the panel, 8.3% toward `--bx-muted` |
| `--bx-code-bg` | `--bx-bg`, 5% toward `--bx-text` |
| `--bx-accent-hover`, `--bx-accent-ink`, `--bx-link` | the accent 25% toward white; `--bx-bg`; the accent |
| `--bx-ok-bg`, `-warn-bg`, `-danger-bg`, `-info-bg` | 12% of the status colour over the panel |
| `--bx-ok`, `-warn`, `-danger`, `-info` (a document that didn't opt in) | `--bx-green`, `-amber`, `-red`, `--bx-muted` |
| `--bx-titlebar`, `-titlebar-active`, `--bx-title-text`, `-inactive` | the panel, `--bx-panel-2`, the text, `--bx-subtle` |
| `--bx-window-border`, `--bx-control-hover`, `--bx-close-hover`, `-ink` | `--bx-border-strong`, `--bx-border`, the danger colour, `--bx-bg` |

Each computes to Concrete Night's value exactly when nothing is set. The
rest are values of their own: set them too if your palette needs them —
`--bx-selection` with `--bx-selection-text`, `--bx-focus` with
`--bx-focus-gap`, `--bx-subtle`, `--bx-border-strong`, `--bx-part`, the
syntax (`--bx-syn-*`) and diff (`--bx-diff-*`) palettes a `<bx-code>`
draws code in, and the terminal's (`--bx-term-*`). A document that opts in
gets Concrete Day's own values for those roles in light; set them in your
`:root` rule for both themes if you keep a palette of your own there.

### `/vendor/bx-theme.js`

```js
import { token, onAppearance } from '/vendor/bx-theme.js';

const paint = () => {
  ctx.fillStyle = token('--bx-panel', canvas);
  ctx.strokeStyle = token('--bx-accent', canvas);
  // …
};
paint();
onAppearance(paint); // the person's change, the system's light/dark or contrast
```

| Export | What |
|---|---|
| `appearance(doc?)` | `{theme, density}`: `'system'`, `'light'` or `'dark'`, and `'compact'` or `'comfortable'`, as the document has them |
| `follows(doc?)` | the document opted in (`data-bx-theme="auto"`) |
| `scheme(el?)` | `'light'` or `'dark'`: what `el` renders in now |
| `token(name, el?)` | a token's current value at `el` |
| `onAppearance(cb, win?)` | `cb(appearance)` after a change of the person's choice, the system's scheme or its contrast setting; returns an unsubscribe |
| `setAppearance({theme, density}, doc?)` | change the document's appearance (the shell's settings do; a tile has no reason to) |
| `appearanceMessage()`, `applyAppearanceMessage(data)` | the `xbin:appearance` relay (`<bx-frame>` and `xbin-client.js` use them) |
| `rememberTheme(theme)`, `THEMES`, `DENSITIES`, `MESSAGE`, `EVENT`, `COOKIE` | the hint cookie (the device's theme while it overrides), and the names |
| `deviceTheme(win?)`, `setDeviceTheme(v, doc?)`, `syncDeviceTheme(doc?)` | this browser's override (D188): `'light'`, `'dark'` or `''`; set or clear it and apply; apply what is stored (the shell does, on load and on another tab's change) |
| `personTheme(doc?)`, `personAppearance(doc?)`, `setPersonAppearance(next, doc?)` | the person's own choice under an override (set aside on `<html data-bx-theme-person>` while the page shows the device's), and changing it |
| `effectiveTheme(person, device)`, `deviceUrl(url, doc?)`, `DEVICE_KEY`, `DEVICE_PARAM` | the precedence (§Theme precedence), and a tile URL that asks for the override |

### Icons

`/vendor/bx-icons.js` draws the workspace's glyphs, in place of emoji and of
symbols set in whatever font a button fell back to. An icon is 16px (or
`size="20"`), takes `currentColor` and sits on the text's baseline:

```js
import '/vendor/bx-icons.js';
html`<button title="Close" aria-label="Close"><bx-icon name="xmark"></bx-icon></button>`;
html`<span class="st"><bx-icon name="warning"></bx-icon> 2 warnings</span>`;
```

`<bx-icon name="…">` is decorative (hidden from assistive technology);
`label="…"` makes it an image with that name; an icon-only button needs its
own `aria-label` or `title`. `iconSvg(name, {size, label})` returns the same
SVG as a string, `hasIcon(name)` says whether a name exists, and `ICON_NAMES`
lists them all. A name that doesn't exist draws nothing (and says so once in
the console). `<option>` text can't hold an icon: there the word stands
alone. The names follow the native vocabulary's where the meaning is the
same:

`agent` `archive` `arrow-left` `arrow-right` `bell` `bell-slash` `bolt`
`box` `branch` `calendar` `call` `caret-down` `caret-right` `chart` `chat`
`check` `chevron-down` `chevron-left` `chevron-right` `chevron-up`
`clipboard` `clock` `code` `compact` `copy` `cpu` `database` `deploy`
`device` `diff` `doc` `download` `ellipsis` `error` `eye` `eye-slash`
`file` `filter` `folder` `globe` `grid` `grip` `home` `host` `info` `key`
`link` `list` `live` `lock` `mail` `maximize` `menu` `minimize` `minus`
`network` `ok` `org` `paperclip` `pause` `pencil` `people` `person`
`photo` `pin` `play` `plug` `plus` `popout` `power` `question` `refresh`
`restore` `save` `search` `send` `server` `settings` `shield` `signal`
`split` `tag` `terminal` `thought` `trash` `unlock` `upload` `vm` `wait`
`warning` `window` `xmark`, and the aliases `attach` `back` `building`
`channel` `close` `collapse` `danger` `edit` `expand` `external` `forward`
`gear` `gpu` `hourglass` `image` `laptop` `logs` `more` `package` `phone`
`rack` `reload` `stop` `storage` `team` `user` `view-as` `warn` `wrench`.

A person's own emoji (a folder's icon, an agent class's) is their content,
and is shown as they wrote it.

### `/vendor/theme-boot.js`

For a page xbind serves with no document injection (its own partitions
page): a classic script in `<head>`, before the `theme.css` link, that
copies the `xbin_theme` hint cookie the shell keeps into
`<meta name="xbin-theme">`, so the page opens in the person's theme. The
workspace root page runs it too (D188): when the browser has a device
override (§Theme precedence) it replaces the injected meta with the
device's theme, keeping the person's on `<html data-bx-theme-person>`. A
tile never needs it: its documents get the injection.

## Rules

- **URLs are frozen.** A module served today stays at its path; a helper
  that moves leaves a re-export behind (`clampBox` in `bx-frame.js`).
- **One home per helper.** `make js-check` refuses a second definition of
  a kit helper (`api`, `jbody`, `esc`, `deepActive`, `pathHas`, `clampBox`),
  of the code viewer's highlight helpers, or of `bx-theme.js`'s and
  `bx-icons.js`'s, anywhere in the shipped trees.
- **Theme fallbacks stay.** Core elements carry `var(--bx-x, <literal>)`
  fallbacks so a document that never linked `theme.css` still renders;
  `make theme-check` keeps every literal equal to Concrete Night's value in
  `theme.css`. Your own document decides its theme: link the sheet (and opt
  in to follow the person), or set the tokens yourself.

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
