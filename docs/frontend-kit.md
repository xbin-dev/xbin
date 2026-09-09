# The frontend kit — what a tile may import from `/vendor/`

Every shipped frontend module lives under `/vendor/` beside the vendored
libraries (lit, xterm, marked, highlight.js), served unauthenticated so a
sandboxed tile frame can load it. This page says which of those modules
are **for tiles**, which are **shell chrome**, and the rules that keep the
URLs stable across xbind upgrades ([compat.md](/docs/compat.md) rule 3).

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
| `/vendor/bx-kit.js` | the helper kit: `api(url, opts)` (JSON out, throws the server's `error`; in a sandboxed tile the request carries the frame token — your tile's identity — in chrome the session cookie, i.e. the signed-in human), `xbinApi('/grants')`, `selfApi('/runs')` (your own backend), `jbody(value, method?)`, `esc(text)`, `deepActive()`, `pathHas(event, selector)`, `clampBox(box, {minW, minH, margin})`, `dragPointer({cursor, onMove, onUp})` (a window-level pointer drag with the iframe shield), `dragShield(cursor)`, `sandboxed()` |
| `/vendor/bx-frame.js` | `<bx-frame src="apps/x">` — embed another tile (with its terminal pop-up); exports `clampBox` for compatibility |
| `/vendor/bx-dialog.js` | `<bx-dialog>` — a modal; `xbin.dialog()` falls back to it outside the shell |
| `/vendor/bx-grants.js`, `/vendor/bx-bindings.js` | the owner's grant-approval and interface-binding panels |
| `/vendor/bx-multiselect.js` | `<bx-multiselect>` — a compact multi-pick control |
| `/vendor/bx-netrules.js` | the network-set rule grammar (`RULE_KINDS`, `parseRule`, `fmtRule`, `ruleProblem`, `netOptions`, …) — interpreted client-side exactly as the server defines it (D54) |
| `/vendor/bx-allow.js` | the allowance grammar (`ALLOW_KINDS`, `parseAllow`, `fmtAllow`, `allowProblem`, `describeAllow`, `capInfo`) (D57) |
| `/vendor/bx-grant-row.js` | `grantArrow(g)` — a grant or request's `from → target` with the policy-block / capability tooltip, as rendered by the shell, the admin console and the organisations tile |
| `/vendor/bx-code.js` | `<bx-code>` (file tree + highlighted viewer + diffs); exports `diffHTML`, `diffStats`, `hl`, `langFor` |
| `/vendor/events-socket.js` | `onEvent(type, fn)` over the shared `/ws/events` socket |
| `/vendor/theme.css` | the design tokens (`--bx-bg`, `--bx-panel`, `--bx-text`, …) plus opt-in `.bx` control styles. Link it to take the theme; it is **never injected** into your document |
| `/vendor/xbin-client.js` | injected into every tile document by xbind — do not import it yourself |

## Shell-only modules

`/vendor/bx-menu.js` (the shell's context menus — action closures, not a
tile API; tiles use `xbin.dialog` / `xbin.window`), `/vendor/bx-terminal.js`,
`/vendor/bx-logs.js`, `/vendor/bx-prs.js` (the terminal pop-up's panels —
reachable through `<bx-frame>`), and the shell's own siblings under
`shell/`. They are served, and they will keep being served, but their
shapes follow the shell.

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
