# Base Two in the workspace: the token contract and the plan (D184)

> Status: **design approved by the owner at gate B2 (2026-10-03); W9 builds it.**
> Branch `theme/base-two` (from `promo/film-set`). This file is the contract
> six packages build against in parallel; the decision record is D184 in
> `plans/DECISIONS.md`.

Inputs: the brand system (`/tmp/claude-1000/xbin-brand/w3/system/`: `brand.md`
§4, §5.3, §9, §11; `product-ui.md`; `tokens.json`, the `light.product` and
`dark.product` blocks), the owner's review page (`w3/review/artifact.html`,
its §5 "Product UI theme preview" and Q1–Q13), and the light-theme audit
(`/home/magik6k/buxon/.film-media/theme-preview/`: `_tools/audit-summary.txt`,
`_tools/audit/*.json`, `_tools/static.json`, and the `current`, `v2-light`,
`v2-dark` and `canary` screenshots). File and line references to the audit
are against `promo/film-set`, which is where this branch starts.

What is already on the branch (the contract commit):

| File | What it is | Owner from here |
|---|---|---|
| `web/theme.css` | every token, Concrete Night and Concrete Day, the opt-in selectors, the old names aliased, the fonts' `@font-face`, base and control styles, the scroll CSS | P1 |
| `web/bx-theme.js` | the client side of the mechanism: read and change a document's appearance, the relay message, the hint cookie, `onAppearance` | P1 |
| `web/bx-icons.js` | the icon set (`<bx-icon name>`, `iconSvg()`), 89 glyphs and 29 aliases | P1 |
| `web/theme-boot.js` | the hint cookie → meta, for xbind's static pages | P1 |
| `web/bx-scroll.js` | the scroll CSS mirror (tint now in the focus colour), kept identical to `theme.css` | P2 |

---

## 0. Decided: do not reopen

The owner approved the system at gate B2 with its defaults. In the owner's
words: *"I really like the less rounded corners, we should apply that
consistently everywhere; Theme should follow system settings, and we need to
audit light theme isn't broken by hardcoded styles."*

| # | Decision | What it means here |
|---|---|---|
| B2 | Product tokens: Concrete Day (light) and Concrete Night (dark) | `tokens.json` `light.product` / `dark.product`, as amended below |
| Q4 | Lift Night's panel and panel_2 one step on the concrete ramp; windows get a border_strong edge | Night panel `#16171D` → `#1F2028`, panel-2 → `#262730`; the dependent steps follow (§1.3); windows `1px var(--bx-window-border)` = border_strong |
| Q5 | Day title bars use the window_chrome tokens: `#F1F2F5`, `#FFFFFF`, border_active `#33353F`, and its light shadow | `--bx-titlebar`, `--bx-titlebar-active`, `--bx-window-border-active`, `--bx-shadow-rest/-active`; never panel_2 and never the black ring |
| Q6 | Cobalt (periwinkle in dark) only for primary actions, selection and links in prose | identifiers and paths in text colour (mono); table row actions as quiet buttons |
| Q7 | Syntax-highlight and diff tokens | `--bx-syn-*`, `--bx-diff-*` (§1.1); tokens.json had none, these are W9's |
| Q8 | UI text 13 px or larger, with tabular figures | 11 px only for caps micro labels, 12 px only for timestamps and meta; tabular figures in every table and number |
| Q9 | The theme follows the system with a per-person override; terminals follow the shell unless the person sets one | §2 |
| Q10 | Compact density is the default | `--bx-row: 28px`, UI text 13/18, terminal 12/18 |
| Q11 | The server-rendered sign-in pages use the product tokens | Work volume, the wordmark, no fields, the same light/dark switch |
| Q12 | V2's part colours are `--bx-part-tab-*`; `--bx-part` (the partition marker) stays | `--bx-part-tab-shell/-terminal/-agent/-admin` |
| Q13 | Replace emoji icons with drawn glyphs, part glyphs and status shapes first | `web/bx-icons.js`; tile authors keep whatever they use in their own apps |
| — | 2 px radius everywhere, no pills; the radius is a token | `--bx-radius: 2px`; only 10 of 396 radius rules use a token today, 40 are pills |
| — | The light theme is audited for hard-coded styles until none is left | the guard (§3) and the canary pass make it stay that way |

Hard constraints, restated so every package has them:

- **Never break users** (`docs/compat.md`). `theme.css` is never injected.
  A third-party tile that links `/vendor/theme.css` gets a dark palette today
  and may hard-code light text, so it stays dark: **a document turns light
  only when it opts in.** Bare documents keep rendering from the `var()`
  fallbacks, which equal Night (`hack/theme-fallbacks.mjs --fix`). Old token
  names keep working.
- **No new HTML rewriting.** The import-map/client injection (D4) is the one
  sanctioned transform; the appearance metas are two more lines in that same
  block, like the deployment and partition metas before them. No JS build
  step; no TS syntax in `web/`.
- **No flash of the wrong theme** when the shell or a shipped tile loads:
  the first painted frame is already right (§2.3, verified in Chromium).
- A changed builtin tile bumps its `tile.json` version and
  `hack/tile-versions.txt` is regenerated (`UPDATE_TILE_VERSIONS=1`,
  `internal/builtins` `TestTileVersions`).
- Builder-visible behaviour: `docs/*.md` and a `docs/changelog.md` entry; new
  xbind surface: `docs/protocol.md`. Inside the embedded trees (`web/`,
  `docs/`, `workspace-template/`, `builtin-*`) cite **D184**, never
  `plans/…` (`assets_test.go`).
- Processes: kill only PIDs you started; delete only your own paths, by
  absolute path; never use ports 8697, 8891, 8893 or 9874.

---

## 1. The token contract

`web/theme.css` is the only place a colour, font, radius or shadow value is
written. Everything else uses `var(--bx-…)`. Core elements keep a literal
fallback on every token, equal to the Night value (compat rule 5); `make
theme-check` keeps them equal.

### 1.1 Every token

Night is the `:root` block (the default for every document); Day is the
block opted-in documents get (§2). "—" means the same value in both.

**Surfaces**

| Token | Night | Day | Use |
|---|---|---|---|
| `--bx-bg` | `#0B0C12` | `#E8E9EE` | the canvas (tokens.json `shell_bg`); a page background |
| `--bx-canvas-dot` | `#1F2028` | `#CDD0D8` | the canvas grid marks |
| `--bx-sidebar` | `#111218` | `#F1F2F5` | the shell's sidebar |
| `--bx-panel` | `#1F2028` | `#FFFFFF` | windows, cards, bars, menus, `body.bx` |
| `--bx-panel-2` | `#262730` | `#F7F8FA` | inset panels, toolbars, a person's chat turn |
| `--bx-hover` | `#2A2B34` | `#EEF0F4` | a hovered row on a panel |
| `--bx-selection` | `#262C5C` | `#DDE2FF` | a selected row (plus a 2 px accent rule), text selection |
| `--bx-selection-text` | `#E9EAF0` | `#0B0C12` | text on the selection |
| `--bx-border` | `#33353F` | `#CDD0D8` | dividers, hairlines, panel edges |
| `--bx-border-strong` | `#666A7E` | `#7E8194` | component borders (inputs, secondary buttons): 3:1 |
| `--bx-code-bg` | `#16171D` | `#FFFFFF` | code panes, diffs |

**Text**

| Token | Night | Day | Use |
|---|---|---|---|
| `--bx-text` | `#E9EAF0` | `#0B0C12` | body and control text |
| `--bx-muted` | `#A3A6B6` | `#4B4D5C` | secondary text, icons at rest |
| `--bx-subtle` | `#8E91A2` | `#626576` | placeholders, tertiary text, disabled labels |

**Accent** (actions, selection and prose links only: R1)

| Token | Night | Day | Use |
|---|---|---|---|
| `--bx-accent` | `#8C9BFF` | `#1F3DFF` | primary buttons, selection rule, active tab underline, the live square, terminal cursor |
| `--bx-accent-hover` | `#A9B4FF` | `#1530D6` | a primary button under the pointer |
| `--bx-accent-ink` | `#0B0C12` | `#FFFFFF` | text and icons on an accent fill |
| `--bx-link` | `#8C9BFF` | `#1F3DFF` | links in prose |

**Focus** (cyan: a hue nothing else uses)

| Token | Night | Day | Use |
|---|---|---|---|
| `--bx-focus` | `#3DD6F5` | `#0086A6` | the ring |
| `--bx-focus-gap` | `#0B0C12` | `#FFFFFF` | the 2 px gap under the ring (the page colour) |
| `--bx-focus-width` | `3px` | — | ring width |
| `--bx-focus-offset` | `2px` | — | ring offset = gap width |
| `--bx-focus-outline` | `var(--bx-focus-width) solid var(--bx-focus)` | — | composite: the `outline` |
| `--bx-focus-halo` | `0 0 0 var(--bx-focus-offset) var(--bx-focus-gap)` | — | composite: the gap, as a `box-shadow` |

The ring (brand §4.5): a 3 px ring in the focus colour, 2 px outside the
element, over a 2 px gap in the page colour, on `:focus-visible`:

```css
:focus-visible { outline: var(--bx-focus-outline); outline-offset: var(--bx-focus-offset); box-shadow: var(--bx-focus-halo); }
```

Inside a container that clips (a scrolling list, a table cell): the ring
goes inside, `outline-offset: calc(-1 * var(--bx-focus-width))`, no halo.

**Status** (always with its icon and a word: R2)

| Token | Night | Day | Icon |
|---|---|---|---|
| `--bx-ok` / `--bx-ok-bg` | `#A3CF5E` / `#2F352E` | `#436C0C` / `#EEF5E1` | `ok` (check in a square) |
| `--bx-warn` / `--bx-warn-bg` | `#F2994A` / `#382F2C` | `#9A4A06` / `#FBEEDF` | `warning` (triangle) |
| `--bx-danger` / `--bx-danger-bg` | `#FF7A7A` / `#3A2B32` | `#C81E1E` / `#FBE7E7` | `error` (octagon) |
| `--bx-info` / `--bx-info-bg` | `#A9B4C6` / `#30323B` | `#3D4A5C` / `#ECEEF2` | `info` (i in a square) |

**Old names** (aliases; `var()` references, so they follow the theme)

| Token | Value | Note |
|---|---|---|
| `--bx-green` | `var(--bx-ok)` | was `#4caf50` |
| `--bx-amber` | `var(--bx-warn)` | was `#f2a71b` |
| `--bx-red` | `var(--bx-danger)` | was `#ef5350` |

`--bx-bg`, `--bx-panel`, `--bx-panel-2`, `--bx-border`, `--bx-text`,
`--bx-muted`, `--bx-accent`, `--bx-radius`, `--bx-shadow`, `--bx-font`,
`--bx-mono`, `--bx-term-bg` keep their names and meaning with new values.
`--bx-sans` and `--bx-part` were used but never defined; now they are.
`--bx-yellow` (37 uses, agent template only) and `--bx-surface-2` (1 use,
starter) stay undefined: their users move to `--bx-warn` and `--bx-panel-2`
(P5), and the lint treats a fallback of an undefined token as a hard-coded
colour.

**Partition marker, fields, part tabs, elevated**

| Token | Night | Day | Use |
|---|---|---|---|
| `--bx-part` | `#3FB5A3` | `#1F8778` | the partition marker (yours/shared/global); the iOS app has the same values (D181) |
| `--bx-field-yellow` / `-ink` | `#FFD000` / `#0B0C12` | — | brand field (first run, empty states only) |
| `--bx-field-green` / `-ink` | `#00A86B` / `#0B0C12` | — | brand field |
| `--bx-field-magenta` / `-ink` | `#DB0072` / `#FFFFFF` | — | brand field |
| `--bx-field-cobalt` / `-ink` | `#3350FF` / `#FFFFFF` | `#1F3DFF` / — | brand field (lifts in Night) |
| `--bx-part-tab-shell` | `#3350FF` | `#1F3DFF` | the shell part (screenshots, mats) |
| `--bx-part-tab-terminal` | `#00A86B` | — | 3 px tab on terminal windows |
| `--bx-part-tab-agent` | `#DB0072` | — | 3 px tab on agent session windows |
| `--bx-part-tab-admin` | `#FFD000` | — | 3 px tab on admin console windows |
| `--bx-part-tab-h` | `3px` | — | part tab height |
| `--bx-elevated-bg` / `-ink` | `#FFD000` / `#0B0C12` | — | the view-as banner and every elevated mode |

**Window chrome** (product-ui §3)

| Token | Night | Day | Use |
|---|---|---|---|
| `--bx-titlebar` | `#1F2028` | `#F1F2F5` | an inactive window's title bar |
| `--bx-titlebar-active` | `#262730` | `#FFFFFF` | the active window's title bar |
| `--bx-title-text` | `#E9EAF0` | `#0B0C12` | title text |
| `--bx-title-text-inactive` | `#8E91A2` | `#626576` | an inactive window's title |
| `--bx-window-border` | `#666A7E` | `#CDD0D8` | a window's 1 px edge (Night: border_strong, Q4) |
| `--bx-window-border-active` | `#9396A4` | `#33353F` | the active window's edge |
| `--bx-control-hover` | `#33353F` | `#E8E9EE` | a window control (and a sidebar row) under the pointer |
| `--bx-close-hover` / `-ink` | `#FF7A7A` / `#0B0C12` | `#C81E1E` / `#FFFFFF` | the close control under the pointer |

**Elevation and shape**

| Token | Night | Day | Use |
|---|---|---|---|
| `--bx-shadow` | `0 1px 2px rgba(0,0,0,.4)` | `0 1px 2px rgba(11,12,18,.08)` | a card (or nothing: prefer the border) |
| `--bx-shadow-rest` | `0 10px 28px rgba(0,0,0,.4)` | `0 1px 0 rgba(11,12,18,.04), 0 8px 24px rgba(11,12,18,.1)` | a window at rest |
| `--bx-shadow-active` | `0 16px 40px rgba(0,0,0,.55)` | `0 1px 0 rgba(11,12,18,.06), 0 14px 36px rgba(11,12,18,.16)` | the active window |
| `--bx-shadow-pop` | `0 12px 32px rgba(0,0,0,.6)` | `0 1px 0 rgba(11,12,18,.06), 0 12px 32px rgba(11,12,18,.18)` | menus, popovers, dialogs, toasts |
| `--bx-scrim` | `rgba(0,0,0,.55)` | `rgba(11,12,18,.32)` | behind a modal or a drawer |
| `--bx-radius` | `2px` | — | every corner; 0 is the only other value |

**Syntax highlighting** (a tool palette, like the ANSI colours: 4.5:1 or
more on the code well, panel-2, the selection and the diff tints)

| Token | Night | Day | highlight.js classes (bx-code, bx-md, the admin console) |
|---|---|---|---|
| `--bx-syn-keyword` | `#FF8CC8` | `#A3005A` | `keyword`, `selector-tag`, `section`, `name` (tags), `variable.language`, `doctag` |
| `--bx-syn-string` | `#7BE0B0` | `#00754A` | `string`, `regexp`, `char.escape_`, `meta .string` |
| `--bx-syn-number` | `#FFD54A` | `#875C00` | `number`, `literal`, `symbol`, `bullet` |
| `--bx-syn-comment` | `#9598A9` | `#626576` | `comment`, `quote` |
| `--bx-syn-function` | `#96A6FF` | `#2A3FD0` | `title.function_`, `title` |
| `--bx-syn-type` | `#5CCBE3` | `#0B6A85` | `type`, `title.class_`, `class .title` |
| `--bx-syn-attr` | `#F5B07A` | `#9A4A06` | `attr`, `attribute`, `property`, `selector-attr/-class/-id/-pseudo` |
| `--bx-syn-builtin` | `#C6A8FF` | `#6A3DB8` | `built_in`, `meta`, `template-variable`, `variable` |
| `--bx-syn-deletion` | `#FF8F8F` | `#B81C1C` | `deletion` (with `--bx-diff-del-bg`) |
| `--bx-syn-addition` | `#7BE0B0` | `#00754A` | `addition` (with `--bx-diff-add-bg`) |

`params`, `subst`, `punctuation` and `operator` stay in the text colour;
`emphasis` is italic, `strong` 600, `link` the link colour.

**Diffs**

| Token | Night | Day | Use |
|---|---|---|---|
| `--bx-diff-add` / `-add-bg` | `#5EDBA5` / `#1D302D` | `#00754A` / `#E6F2ED` | `+` marker and line tint; "+13" stats |
| `--bx-diff-del` / `-del-bg` | `#FF8F8F` / `#342429` | `#B81C1C` / `#FBEDED` | `-` marker and line tint; "−5" stats |
| `--bx-diff-hunk` / `-hunk-bg` | `#5CCBE3` / `#1C2830` | `#0B6A85` / `#ECF4F6` | `@@` hunk headers |
| `--bx-diff-context` / `-context-bg` | `#A3A6B6` / `transparent` | `#4B4D5C` / — | unchanged lines |

Syntax colours keep their colour on the add and delete tints; file headers
(`+++`, `---`, `commit …`, `Author:`) are text colour, weight 600.

**Terminals** (product-ui §7; programs choose these, so the "fields never
signal" rule doesn't apply)

| Token | Night | Day | xterm theme key |
|---|---|---|---|
| `--bx-term-bg` | `#0B0C12` | `#FFFFFF` | `background` |
| `--bx-term-fg` | `#E6E7EE` | `#1C1D24` | `foreground` |
| `--bx-term-cursor` | `#8C9BFF` | `#1F3DFF` | `cursor` (a block in the accent) |
| `--bx-term-cursor-ink` | `#0B0C12` | `#FFFFFF` | `cursorAccent` |
| `--bx-term-selection` | `#262C5C` | `#DDE2FF` | `selectionBackground` |
| `--bx-term-black` / `-bright-black` | `#1C1D26` / `#5C5F70` | `#1C1D24` / `#5A5D6C` | `black` / `brightBlack` |
| `--bx-term-red` / `-bright-red` | `#FF6B6B` / `#FF8F8F` | `#C81E1E` / `#E0352B` | `red` / `brightRed` |
| `--bx-term-green` / `-bright-green` | `#4CD69B` / `#7BE6B6` | `#00794A` / `#00965C` | `green` / `brightGreen` |
| `--bx-term-yellow` / `-bright-yellow` | `#FFD54A` / `#FFE27A` | `#8A6100` / `#A87800` | `yellow` / `brightYellow` |
| `--bx-term-blue` / `-bright-blue` | `#6F86FF` / `#96A6FF` | `#1F3DFF` / `#4A63FF` | `blue` / `brightBlue` |
| `--bx-term-magenta` / `-bright-magenta` | `#FF5FB0` / `#FF8CC8` | `#B0005C` / `#D4007A` | `magenta` / `brightMagenta` |
| `--bx-term-cyan` / `-bright-cyan` | `#4FC3DC` / `#85DCEC` | `#0E7490` / `#0891B2` | `cyan` / `brightCyan` |
| `--bx-term-white` / `-bright-white` | `#C9CBD6` / `#FFFFFF` | `#7A7D8C` / `#A6A9B8` | `white` / `brightWhite` |

Bold is weight (700), never a brighter colour (`drawBoldTextInBrightColors:
false`); ligatures off. Charts and other categorical colour (the admin
console's series, the film set's calendar categories) use the six normal
ANSI tokens in the order blue, magenta, cyan, green, yellow, red: a tool
palette with contrast in both themes, so no chart token set is needed.

**Type** (brand §5.3; R5)

| Token | Value | Use |
|---|---|---|
| `--bx-sans` | `"Instrument Sans", system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif` | UI text |
| `--bx-mono` | `"JetBrains Mono", ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace` | code, terminals, paths, commands, ids, times |
| `--bx-display` | `"Bricolage Grotesque", "Arial Black", "Helvetica Neue", Arial, system-ui, sans-serif` | headings, empty states |
| `--bx-font` | `var(--bx-text-size)/var(--bx-text-line) var(--bx-sans)` | the `font` of `body.bx` and controls (13/18 compact, 14/20 comfortable) |
| `--bx-font-micro` | `600 11px/14px var(--bx-sans)` | caps section labels only, with `--bx-tracking-micro` (`0.06em`) and uppercase |
| `--bx-font-meta` | `400 12px/16px var(--bx-sans)` | timestamps, the status bar, captions under a control only |
| `--bx-font-ui` | `400 var(--bx-text-size)/var(--bx-text-line) var(--bx-sans)` | controls, lists, tables, menus |
| `--bx-font-body` | `400 14px/20px var(--bx-sans)` | agent chat, in-app docs |
| `--bx-font-title` | `600 16px/22px var(--bx-sans)` | dialog and panel titles |
| `--bx-font-heading` | `600 20px/26px var(--bx-display)` | headings, with `--bx-tracking-heading` (`-0.01em`) |
| `--bx-font-hero` | `800 32px/36px var(--bx-display)` | empty states, first run, with `--bx-tracking-hero` (`-0.02em`) |
| `--bx-font-code` | `400 var(--bx-term-size)/var(--bx-term-line) var(--bx-mono)` | code panes, terminal-like text |
| `--bx-font-number` | `600 var(--bx-text-size)/var(--bx-text-line) var(--bx-sans)` | numbers that matter; add `font-variant-numeric: tabular-nums` (the `font` shorthand resets it) |

**Density** (compact default; comfortable is a person's choice)

| Token | Compact | Comfortable |
|---|---|---|
| `--bx-density` | `compact` | `comfortable` |
| `--bx-row` | `28px` | `32px` |
| `--bx-control-h` | `28px` | `32px` |
| `--bx-pad` | `12px` | `16px` |
| `--bx-grid` | `4px` | — |
| `--bx-text-size` / `--bx-text-line` | `13px` / `18px` | `14px` / `20px` |
| `--bx-term-size` / `--bx-term-line` | `12px` / `18px` | `13px` / `20px` |

**Layout and motion**

| Token | Value |
|---|---|
| `--bx-titlebar-h`, `--bx-topbar-h`, `--bx-statusbar-h` | `28px`, `40px`, `24px` |
| `--bx-sidebar-w`, `--bx-rail-w`, `--bx-dock-w`, `--bx-elevated-h` | `248px`, `48px`, `400px`, `28px` |
| `--bx-ease-out`, `--bx-ease-in-out` | `cubic-bezier(0.16, 1, 0.3, 1)`, `cubic-bezier(0.65, 0, 0.35, 1)` |
| `--bx-dur-instant`, `-ui`, `-panel`, `-window` | `80ms`, `120ms`, `200ms`, `240ms` (all `0ms` under `prefers-reduced-motion`) |
| `--bx-scheme` | `dark` / `light`: the theme in force, for code that paints |

### 1.2 From tokens.json to `--bx-*`

`shell_bg` → `--bx-bg`; `canvas_dot`, `sidebar`, `panel`, `panel_2`, `hover`,
`border`, `border_strong`, `text`, `muted`, `subtle`, `accent`, `accent_ink`,
`selection`, `selection_text`, `focus`, `ok`/`warn`/`danger`/`info` →
the same names with `--bx-` and dashes; the theme-level `accent_hover`,
`link`, `focus_gap` and `*_bg` → `--bx-accent-hover`, `--bx-link`,
`--bx-focus-gap`, `--bx-*-bg`; `part.*` → `--bx-part-tab-*`;
`elevated_banner` → `--bx-elevated-bg/-ink`; `window_chrome.titlebar`,
`titlebar_active`, `title_text`, `title_text_inactive`, `border`,
`border_active`, `shadow`, `shadow_active`, `control_hover`, `close_hover` →
`--bx-titlebar`, `--bx-titlebar-active`, `--bx-title-text`,
`--bx-title-text-inactive`, `--bx-window-border`, `--bx-window-border-active`,
`--bx-shadow-rest`, `--bx-shadow-active`, `--bx-control-hover`,
`--bx-close-hover`; `terminal.*` → `--bx-term-*`; `type.families` →
`--bx-sans/-mono/-display`; `type.product.*` → `--bx-font-*`; `radius` →
`--bx-radius`; `size.*` → the layout tokens; `motion` → `--bx-ease-*`,
`--bx-dur-*`.

### 1.3 Where the tokens differ from tokens.json, and why

- **Night lift (Q4).** The review measured panel against canvas at 1.09:1,
  "one dark mass". Panel and panel-2 move one step up the ramp (925 → 900,
  ~912 → 850) and everything that sat a step above them follows, so the
  relationships hold: hover `#1F2129` → `#2A2B34`, border `#2C2E38` →
  `#33353F` (800), title bar `#16171D` → `#1F2028`, active title bar
  `#1F2028` → `#262730`, control hover `#262730` → `#33353F`. Panel against
  canvas is now 1.20:1, and the window's border_strong edge (Q4's second
  half) carries the separation: 3.65:1 against the canvas. The active
  window's edge goes one step brighter, `#9396A4`, so active reads stronger
  than inactive as it does in Day.
- **Night subtle** `#8A8D9E` → `#8E91A2`: on the lifted hover the old value
  fell to 4.27:1; this holds 4.50:1 (placeholders and tertiary text are
  text). Night's inactive title text uses it too.
- **Night status tints** are recomputed as 12 % of the status colour over
  the lifted panel. tokens.json's (`#1C2612` …) were tuned for `#0B0C12` and
  are darker than the new panel (1.0:1, invisible). Status text on its tint
  stays ≥ 5.29:1.
- **New**: `--bx-code-bg`, the syntax and diff palettes (Q7), `--bx-shadow`
  (card), `--bx-shadow-pop`, `--bx-scrim`, `--bx-part-tab-shell`, the focus
  composites, density, layout and motion tokens, `--bx-scheme`.
- **Kept**: `--bx-part` at its current teal values (Q12; the iOS app draws
  the same marker, D181).

### 1.4 Contrast (WCAG 2, computed from the values above)

| Pair | Night | Day |
|---|---|---|
| text on panel / panel-2 | 13.50 / 12.36 | 19.52 / 18.37 |
| muted on panel / panel-2 | 6.71 / 6.14 | 8.35 / 7.86 |
| subtle on panel / panel-2 / hover | 5.19 / 4.75 / 4.50 | 5.77 / 5.43 / 5.05 |
| accent on panel; accent-ink on accent | 6.37; 7.67 | 6.63; 6.63 |
| ok · warn · danger · info on panel | 8.99 · 7.28 · 6.42 · 7.74 | 6.20 · 6.26 · 5.74 · 9.00 |
| status on its own tint (worst) | 5.29 (danger) | 4.83 (danger) |
| focus on panel (non-text, ≥ 3) | 9.37 | 4.23 |
| partition marker on panel | 6.45 | 4.38 |
| window edge against the canvas: rest / active | 3.65 / 6.64 | 1.27 / 10.06 |
| syntax, worst on code bg / tints / selection | 6.25 / 4.85 / 4.60 | 5.76 / 5.02 / 4.50 |
| terminal: as tokens.json notes (normal colours ≥ 4.5:1 except Day white 4.1:1, dim text) | | |

P1's lint recomputes the pairs in this table from `theme.css` (§3.3), so a
later token edit can't silently drop one below its threshold.

### 1.5 Rules of use (every package)

- **R1 Accent** only for primary buttons, selection (the selected row's
  background plus a 2 px accent rule on the left), the active tab's 2 px
  underline, prose links, the live square and the terminal cursor. Never
  for identifiers, paths, row actions in tables (quiet buttons), status,
  decoration or focus.
- **R2 Status** is icon + word + colour (`ok`, `warning`, `error`, `info`
  glyphs; `*-bg` tints for alert and badge backgrounds). Colour is never the
  only cue; a field colour never signals status.
- **R3 Fields** (yellow, green, magenta, cobalt) appear only as 3 px part
  tabs, the yellow elevated banner, and one field block in a first-run or
  empty state. Never a panel or sidebar background, never text colour.
- **R4 Focus** as in §1.1; every interactive element in a shadow root
  carries the rule (documents opted in with `body.bx` get it from
  `theme.css`).
- **R5 Type**: running UI text is 13 px or larger (`var(--bx-font)`,
  `--bx-font-ui/-body/-title`); 11 px micro caps for section labels only;
  12 px meta for timestamps and meta only. Mono for paths, commands,
  hostnames, ids and times. Display type only for headings and empty
  states. Every shadow root sets `font: inherit` on `button, input, select,
  textarea` and gives `pre, code` the code font (the Arial and UA-monospace
  findings). Tabular figures in tables and numbers.
- **R6 Shape**: `border-radius: var(--bx-radius)` or `0`. No pills, no
  circles: status dots become 8 px squares, avatars are squares.
- **R7 Elevation**: windows `--bx-shadow-rest/-active`; menus, popovers,
  dialogs and toasts `--bx-shadow-pop`; cards a border (or `--bx-shadow`);
  scrims `--bx-scrim`. No `rgba(0,0,0,…)` anywhere.
- **R8 Density**: rows `var(--bx-row)`, controls `var(--bx-control-h)`,
  panel padding `var(--bx-pad)`, spacing on the 4 px grid.
- **R9 Icons**: `<bx-icon name>` from `/vendor/bx-icons.js` (16 px,
  `currentColor`); an icon-only button keeps its `title` and gets an
  `aria-label`. Names per §1.6. A glyph that isn't there: use the nearest,
  and list the need in your hand-off (P1 draws it).
- **R10 Placeholders**: `::placeholder { color: var(--bx-subtle); opacity: 1 }`
  in every shadow root with an input.
- **R11 Painting code** (xterm, canvases, SVG drawn from JS) reads tokens with
  `token(name, el)` and re-reads in `onAppearance()` (`/vendor/bx-theme.js`).
- **R12 Opt in** every shipped document: `<html lang="en" data-bx-theme="auto">`,
  the `theme.css` link, `body class="bx"` where it had it. Nothing tuned for
  one theme only.
- **R13** No colour, font stack, radius or shadow literal outside
  `theme.css`, except what the allowlist names with a reason (§3).
- **R14 Motion** uses `--bx-dur-*` and `--bx-ease-*`; nothing loops.
- **R15 Fallbacks**: where you write `var(--bx-x, literal)`, the literal is
  the Night value from `theme.css`. Don't hand-edit old fallbacks you aren't
  touching; the integration step regenerates them all (§4.8).

### 1.6 Icons: names and what they replace

The emoji, or the text glyph set in whatever font the button fell back to,
gives way to a drawn glyph. **A string loses the emoji and the space after
it; the words stay.** Where the string is rich (a button, a chip, a row), a
`<bx-icon>` is drawn before the words; where it can't be (an `<option>`, a
`title`, an `aria-label`, plain text), the word stands alone. A test that
matched `"🔌 tile API"` matches `"tile API"`.

| Replaces | Glyph | Replaces | Glyph |
|---|---|---|---|
| ✕ (close, dismiss, remove) | `xmark` | 🔒 🔐 | `lock` |
| – ▣ □ (window size) | `minimize`, `maximize`, `restore` | 🔓 | `unlock` |
| ⧉ (pop out, open in a tab) | `popout` | 🔑 | `key` |
| ⋯ | `ellipsis` | 🛡 | `shield` |
| ☰ | `menu` | 📌 | `pin` |
| ⚙ 🔧 | `settings` | 🔌 | `plug` |
| » « ‹ › | `chevron-right`, `chevron-left` | 🌐 | `globe` |
| ▸ ▾ (disclosure) | `caret-right`, `caret-down` | 🖧 | `network` |
| + − | `plus`, `minus` | 🏢 | `org` |
| >_ | `terminal` | 👤 🧑 | `person` |
| { } | `code` | 👥 | `people` |
| ▤ (logs) | `list` | 👁 (view as) | `eye` |
| ⇄ (change proposals) | `diff` | 📱 | `device` |
| ⇋ (beside) | `split` | 🔔 🔕 | `bell`, `bell-slash` |
| ⇈ (live reload, deployments) | `deploy` | 💾 | `save` |
| ⟲ ↻ (reset, reload) | `refresh` | 🔗 | `link` |
| ⎇ | `branch` | 📦 | `box` |
| ✓ | `check` | 🖼 | `photo` |
| ● (live, status dot) | `live`, or an 8 px square in CSS | 📡 (channel, bus) | `signal` |
| ⚠ | `warning` | 📋 | `clipboard` |
| ⛔ | `error` | ⏳ | `wait` |
| ℹ | `info` | 🗜 | `compact` |
| ✅ | `ok` | 📅 | `calendar` |
| 📁 | `folder` | 🗑 | `trash` |
| 📄 📖 | `doc`, `file` | 💭 🧠 (thinking) | `thought` |
| 🗄 | `database` | 📎 | `paperclip` |
| 💻 | `host` | ✏️ | `pencil` |
| 🖥 (VM) | `vm` | 🔎 🔬 | `search` |
| 🎮 (GPU) | `cpu` | ⚡ | `bolt` |
| ✉ | `mail` | ↪ | `arrow-right` |

Part glyphs (brand §9): shell `window`, terminal `terminal`, agents `agent`
(two linked squares), admin `key`, xbind `server`. Also available: `copy`,
`download`, `upload`, `send`, `play`, `pause`, `filter`, `tag`, `home`,
`chart`, `clock`, `question`, `archive`, `chat`, `eye-slash`, `grip`,
`arrow-left`, and aliases (`close`, `more`, `gear`, `external`, `logs`,
`rack`, `danger`, `warn`, `reload`, `edit`, `attach`, …: `ICON_NAMES`).
Never drawn, by brand rule: stars, hearts, sparkles, robots, brains, faces.

**User content is never rewritten**: a folder icon a person typed, an
agent class icon an admin chose, message text. The agent template already
maps known emoji to icon names for its native view
(`builtin-templates/agent/model/classes.js` `ICONS`); its web view draws
the same name as a `<bx-icon>` and shows an unknown emoji as text.

**View models that carried an emoji** gain an `icon` field (a glyph name)
and lose the emoji from their text; renderers draw `<bx-icon name=${icon}>`
when it is set. This is the contract between the modules that build the
strings (P3: `deploy-state.js`, `deploy-panel.js`, `deploy-branch.js`,
`bx-netrules.js`, `bx-allow.js`, `agent-cards.js` `KIND_ICON`) and the ones
that draw them (P2: `frame-deploy.js`, `frame-titlebar.js`; P5: the admin
tabs). Each side codes to it independently.

---

## 2. The mechanism

### 2.1 How a document opts in

```html
<html lang="en" data-bx-theme="auto">
<head>
  <link rel="stylesheet" href="/vendor/theme.css">
```

- A document that links `theme.css` **without** the attribute gets Concrete
  Night, exactly as it got the dark-steel palette: new values under the same
  names, old names aliased. That is every third-party tile today, so a tile
  that hard-codes light text stays legible.
- `data-bx-theme="auto"` says: this document follows the person (their
  override, else the system). No other value is defined; others are
  reserved. A Day-only document is not a thing: either it follows or it is
  Night.
- An attribute on `<html>`, not `<meta name="color-scheme">`: the standard
  meta is boilerplate a third-party tile may already carry for unrelated
  reasons, and it must not be what turns a tile light. An xbin-specific
  attribute can't be set by accident.
- A bare document (no `theme.css`) can't opt in: core elements render from
  their fallbacks, which are Night.

### 2.2 Where the person's choice lives

- In the person's **shell prefs bucket** (`data/prefs/<user>/root.json`,
  the bucket the shell already keeps its layout and font size in): key
  `theme` = `"light"` or `"dark"`, absent (or `"system"`) = follow the
  system; key `density` = `"comfortable"`, absent = compact. Separate keys,
  so a font-size write from an older tab can't clobber the theme.
- Written by the shell's settings with the **existing** routes:
  `PUT /api/xbin/prefs/theme` with `"light"`/`"dark"`, `DELETE` for system
  (likewise `density`), with the `X-Prefs-Writer` header the shell already
  sends. No new route.
- Only chrome writes the root bucket: a tile's `PUT /prefs/…` lands in the
  tile's own bucket. A tile can't change the person's theme.
- A write publishes the existing `prefs` event to the person's own human
  sessions (`VisibleTo`), which is how their other tabs and devices follow.

### 2.3 First paint: the injected metas and the selectors

xbind's D4 injection (`internal/server/static.go` `headInjection`) adds, in
the same block as `xbin-component` and the frame token, **only when the
person chose something other than the default**:

```html
<meta name="xbin-theme" content="light">          <!-- or "dark"; absent = system -->
<meta name="xbin-density" content="comfortable">  <!-- absent = compact -->
```

A person who never chose gets byte-for-byte today's injection. The block
lands right after `<head>`, before the document's own stylesheet link, so
the metas are in the DOM when styles first resolve.

`theme.css` reads them with `:has()`:

```css
:root { /* Concrete Night */ color-scheme: dark; }
:root:where([data-bx-theme="auto"]:has(> head > meta[name="xbin-theme"][content="light"])) { /* Day */ }
@media (prefers-color-scheme: light) {
  :root:where([data-bx-theme="auto"]:not(:has(> head > meta[name="xbin-theme"][content="dark"]))) { /* Day */ }
}
:root:where([data-bx-theme="auto"]:has(> head > meta[name="xbin-density"][content="comfortable"])) { /* comfortable */ }
```

- **System** is the media query: no script, so it is right at first paint
  in the shell and in every frame (an iframe evaluates
  `prefers-color-scheme` against the system, verified in Chromium 149).
- **The override** is the meta: light forces Day, dark blocks the system
  rule.
- The Day block appears twice (explicit light; system light without a dark
  override); `hack/theme-fallbacks.mjs` checks the two copies between the
  `bx-day:start/end` markers are identical.
- `:root:where(…)` keeps the specificity of `:root`, so a document's own
  `:root { --bx-accent: … }` after the link still wins in both themes
  (verified), and the `:where()` list is forgiving: a browser without
  `:has()` drops the inner selector and the document stays Night.
- `color-scheme` follows the theme, so UA controls, scrollbars and the
  canvas behind a frame match it.

Verified in Chromium 149 with the real `theme.css` and `bx-theme.js`
(scratch probes, not in the repo): the person's dark on a light system and
light on a dark system are right in the first animation frame of a
sandboxed frame; a non-opted frame stays Night on a light system; a
document override wins in Day; the live relay below switches theme and
density; a system flip while open follows.

Who "the person" is, at injection: the principal's `UserID` (a session, a
device, or a frame or terminal token driven by them); `root` for the owner
token and `--no-auth` (the bucket `prefsKeys` uses); while an admin views as
someone (D64), the viewed person (product-ui §9: everything looks as that
person sees it); a backend principal gets no meta. Every injected document
gets it: tile pages, deployment URLs, partitioned tiles, chrome, the native
runtime document. `inject: false` documents get no injection, so an opted-in
one follows the system only.

Reading the bucket costs a stat per document load: xbind caches the two
keys per person by the file's size and mtime. (`internal/server` can't
import `internal/obs`, which imports it: P1 reads the file directly or
moves the bucket path and reader into a small shared package both use, so
the path logic keeps one home.)

### 2.4 Live changes, frames included

```
person picks Dark in the shell's settings
  └ shell: setAppearance({theme:'dark'})          (/vendor/bx-theme.js)
      ├ rewrites the shell document's <meta name="xbin-theme">  → the shell restyles at once
      ├ refreshes the hint cookie (§2.7)
      └ dispatches 'xbin-appearance' on window
          └ every <bx-frame> in the document posts {type:'xbin:appearance', theme, density}
              to its iframe (targetOrigin: the frame's origin, else '*')
              └ xbin-client.js in the frame: source must be window.parent
                  (and the workspace origin on a tile origin) → setAppearance(…)
                  └ the frame restyles; its own <bx-frame>s relay further down
  └ shell: PUT /api/xbin/prefs/theme "dark"
      └ 'prefs' event → the person's other tabs and devices: GET the key → setAppearance
```

- `<bx-frame>` posts `appearanceMessage()` on **every** iframe `load` (the
  first one too: a pref that changed while the frame was loading is
  corrected) and from `onAppearance()`, but **only when its own document
  follows** (`follows()`). An old shell that never opted in sends nothing,
  so a new opted-in tile inside it keeps the injected choice.
- The message carries no credential and asks for nothing; it only makes the
  frame match its embedder. It is documented with the other tile↔shell
  messages in `docs/protocol.md`.
- A tile opened in a tab of its own (`/c/apps/x/`) has no embedder: it gets
  the person's choice at load, and follows the system live; a change of the
  override reaches it on reload.
- `onAppearance(cb)` fires on the event, on `prefers-color-scheme` and on
  `prefers-contrast` changes: everything that paints from tokens re-reads
  there.

### 2.5 UA controls

- `color-scheme` is set per theme on `:root` and inherits into shadow roots:
  form controls, scrollbars and the canvas follow.
- `accent-color: var(--bx-accent)` on opted-in documents: checkboxes,
  radios, ranges and progress bars.
- `::placeholder` and `::selection` from the tokens in opted-in documents;
  shadow roots add their own (R10), since document rules don't reach them.
- Buttons and inputs in shadow roots set `font: inherit` (R5); the `.bx`
  control styles in `theme.css` already do.
- Scrollbars: the thumb from `--bx-muted`, the focused-scroll tint (D123)
  in the **focus** colour (cyan: it shows where scrolling input goes, which
  is focus's job, not the accent's), corners `--bx-radius`. Done in this
  commit, in `theme.css` and `bx-scroll.js` together (`hack/scroll-css.test.mjs`).

### 2.6 Terminals follow the shell and keep their own choice

- The 🔧 menu's stored value `default` (every terminal that never picked a
  palette) now means **"Workspace (follows the theme)"**: the xterm theme is
  built from the `--bx-term-*` tokens at the terminal element
  (`token()`), and rebuilt in `onAppearance()`; the open terminal's colours
  change with the shell, live.
- A palette the person picked stays theirs (per browser, `localStorage`, as
  today). The list gains **Concrete Night** and **Concrete Day** (so a dark
  terminal in a light shell is one click), and keeps the named palettes,
  which move to `web/term-palettes.js` (data, allowlisted).
- The terminal font is the `--bx-mono` token (JetBrains Mono, ligatures
  off); the terminal waits for `document.fonts.load()` before `open()` and
  refits when fonts finish loading, so xterm never measures a fallback face.
  The default size follows density (`--bx-term-size`) until the person
  picks one in 🔧.
- The log viewer (`bx-logs`) uses the same theme object.

### 2.7 Pages without an injection

- **The hint cookie** `xbin_theme=light|dark` (absent = system): `Path=/`,
  `SameSite=Lax`, `Max-Age` 400 days, `Secure` on https, not HttpOnly,
  host-only (never sent to tile origins). A UI hint, never a credential:
  any other value is ignored. Written by `rememberTheme()` in
  `/vendor/bx-theme.js`: `setAppearance()` calls it, and `xbin-client.js`
  calls it on load in a top-level unsandboxed document (the shell), so it
  mirrors the server's truth whatever the shell's age. In a sandboxed
  document cookies throw and nothing is written.
- **Sign-in and xbind's other server-rendered pages** (P4): link
  `theme.css`, opt in, and render `<meta name="xbin-theme">` from the
  cookie before sign-in (and on the signed-in ones: the cookie mirrors the
  person's choice in that browser). No cookie: the system.
- **The partitions page** (`/xbin/partitions`, static by design: no
  principal-specific bytes, no inline script): `/vendor/theme-boot.js`, a
  classic synchronous script in `<head>` before the stylesheet, copies the
  cookie into the meta (verified: first frame right for cookie dark on a
  light system and light on a dark one; none or garbage → system).
- **The docs viewer** (`/docs/`, authed, rendered by `static.go`): the
  person's prefs, like the injection.
- Pre-sign-in pages are Work volume (Q11): the workspace's branding (D76)
  or the one-colour wordmark "xbin" in Bricolage Grotesque 800; no fields;
  the mark itself lands when Q1's mark is drawn (§6).

### 2.8 Fonts

- Self-hosted woff2 under `web/vendor/fonts/` (served at `/vendor/fonts/`,
  unauthenticated like the rest of `/vendor/`), with each family's OFL 1.1
  text as `fonts/OFL-<family>.txt`. The file names are what `theme.css`'s
  `@font-face` rules say (or P1 edits the rules with the files).
- Instrument Sans 400, 600 and Bricolage Grotesque 600, 800 in latin and
  latin-ext subsets with `unicode-range` (names like Łukasz and Tomás);
  JetBrains Mono 400, 500, 700 with what terminals draw (box drawing,
  blocks, arrows: the full character set is preferred over subsets).
  Optional: Instrument Sans 400 italic for chat emphasis (else synthesized).
- `font-display: swap` (text is never invisible); the terminal waits for
  its face (§2.6).
- Pinned like the other vendored files: sources and exact versions in
  `hack/vendor.sh`, hashes in `hack/vendor.sha256`, and `check-pins.sh`
  learns the `fonts/` subdirectory (today it lists `web/vendor/*` flat and
  would call `fonts` unlisted).
- Sandboxed frames fetch fonts from an opaque origin: fonts are CORS
  requests, and xbind already answers `Origin: null` with
  `Access-Control-Allow-Origin: null` (`nullOriginCORS`). P1 verifies a
  sandboxed tile renders in Instrument Sans in every asset mode
  (`legacy`, `tokens`, `origins`).

### 2.9 What every kind of workspace sees

| Situation | Result |
|---|---|
| Third-party tile linking `theme.css`, not opted in | Concrete Night; old names work; fonts are Instrument Sans/JetBrains Mono through `--bx-font`/`--bx-mono` |
| Bare document, no `theme.css` | core elements render from fallbacks = Night |
| New xbind, **old shell** (scaffold not updated) | the shell is Night; new opted-in tiles follow the person (on a light system with no override: Day tiles in a Night shell) until `bx builtin update` brings the new root and shell. The changelog says so |
| New shell on an **older xbind** | not a combination xbind ships (the scaffold comes with the binary); after a downgrade the newer shell's `/vendor/` imports are missing, as for any shell newer than its xbind (the shell already imports `scroll-css.js`, D123) |
| Admin viewing as someone | that person's theme |
| The xbin app's web views | the injected meta like a browser; the native chrome follows the phone (open item, §6) |
| `inject: false` document that opted in | follows the system only |
| Strict asset gating, `tokens` / `origins` | unchanged injection; fonts load cross-origin from opaque frames; the hint cookie stays on the workspace origin |

### 2.10 Security and privacy

- The metas tell tile code the person's light/dark choice and density, as
  `prefers-color-scheme` already tells any page. Nothing else leaves the
  bucket; tiles still can't read the shell's bucket.
- `xbin:appearance` is accepted only from the frame's parent (source check;
  origin check on a tile origin); its values are validated against
  `THEMES`/`DENSITIES`. A tile can post it only to frames it embeds, which
  changes nothing but their colours.
- The cookie is a hint, ignored unless exactly `light` or `dark`; pages
  that read it server-side escape nothing user-controlled into the page
  (two constant strings).
- Pages that start linking `/vendor/theme.css` relax their CSP for styles
  and fonts from `'self'` only (P4); script rules stay as they are.

### 2.11 The decision

Recorded as **D184** in `plans/DECISIONS.md` (design now; P1 completes the
entry with what shipped and how it was verified).

---

## 3. The regression guard

`make theme-check` runs two scripts; both must pass for `make check`.

### 3.1 `hack/theme-fallbacks.mjs` (extended by P1)

- Strip comments before reading `theme.css`'s `:root` block (today a token
  name inside a comment would be read as a token).
- Resolve `var()` references in theme values to Night literals, so the old
  names' fallbacks become `var(--bx-red, #FF7A7A)`, never
  `var(--bx-red, var(--bx-danger))`.
- Exempt every font token (`--bx-font`, `--bx-font-*`, `--bx-sans`,
  `--bx-mono`, `--bx-display`): a fallback may abbreviate the stack.
- Check that the two Day blocks between the `bx-day:start/end` markers are
  identical.
- Take paths: `--fix web/bx-code.js workspace-template/shell` limits the
  rewrite; no paths = every tree, as today.

### 3.2 `hack/theme-lint.mjs` (new, P1)

Scope: `web/` (not `web/vendor/`), `workspace-template/`, `builtin-tiles/`,
`builtin-templates/`, `examples/`, `hack/demo/tiles/`, and the HTML/CSS
string literals of `internal/server/*.go`. Skips `test/`, `testdata/`,
`_backend/`, `node_modules/`, `*_test.go`, `*.test.mjs`. Comments are
stripped first (CSS, JS, HTML).

It fails, with `file:line: check: literal`, on:

1. **colour**: a hex colour, `rgb()/rgba()/hsl()/hsla()/hwb()/lab()/lch()/
   oklab()/oklch()/color()`, or `black`/`white` in a styling context
   (declarations, `style=`, `.style.`, `setProperty`, `fillStyle`/
   `strokeStyle`, xterm theme objects, SVG `fill`/`stroke`). Not a finding:
   the fallback inside `var(--bx-known, …)` when `theme.css` defines the
   token (theme-fallbacks checks its value); `transparent`,
   `currentColor`, `inherit`. A fallback of a token `theme.css` doesn't
   define **is** a finding: it is a hard-coded colour wearing a `var()`.
2. **radius**: any `border-*radius` other than `0` or `var(--bx-radius…)`.
3. **font**: a `font`/`font-family` naming a family outside `var(--bx-…)`
   (`system-ui`, `monospace`, `Arial`, …).
4. **small type**: a literal `font-size` (or `font` shorthand size) under
   13 px outside the micro/meta tokens.
5. **emoji**: a pictographic emoji in UI source (the ranges of the audit's
   `emoji.py`).
6. **contrast**: the pairs of §1.4 recomputed from `theme.css` against
   their thresholds (4.5 text, 3 UI parts), in both themes.

Exceptions, each with a reason:

- `hack/theme-allow.txt`: `<path-glob>  <checks>  <reason>`, one per line.
  Expected entries, and no more without a reason that holds:
  - `web/theme.css  colour,font,radius  the token file: values are written here`
  - `web/term-palettes.js  colour  the 🔧 menu's named terminal palettes (data; programs choose ANSI colours)`
  - `web/xb/render-theme.js  colour,font,radius  the native renderer's token table; it previews the app, whose shapes follow iOS`
  - `web/xb/render-*.js  radius  the native renderer draws the app's shapes (frontend-kit: it follows the app's look)`
- Inline, on the line or the one before: `theme-ok: <reason>` (the
  `// exec-ok:` precedent; `TestNoDirectExec`). For a QR code's dark-on-light
  modules, a data table mapping a person's emoji to glyph names, a demo
  avatar hue derived from an id.

Tests: `hack/theme-lint.test.mjs` (each check fires on a fixture, a known
fallback passes, an unknown one fails, `theme-ok` and the allowlist work,
contrast fails on a lowered pair). Docs: `docs/maintenance.md`
(the guard, how to satisfy it, how to add an exception).

### 3.3 The runtime guard

`hack/ui-harness/passes/themecanary.js` (P1): the preview tool's canary
audit (`.film-media/theme-preview/_tools/preview.js`: every colour token set
to a fingerprint `rgb(1, 254, n)`, every font token to a fake family) as a
harness pass. It walks the rendered shell, a terminal, a code diff, the
agent chat, the admin console, the partitions page and the sign-in page in
both themes and fails on any colour, font or radius that didn't come from a
token (minus `theme-ok` sources). This catches what a static scan can't: UA
defaults (placeholder grey, `accent-color: auto`, Arial in buttons) and
colours built at runtime.

And `hack/ui-harness/passes/appearance.js` (P1): no flash (an init script
records `--bx-scheme` at the first animation frame in the shell and in
sandboxed and chrome frames; it must equal the person's choice for
light/dark/system × light/dark system), the settings change reaching every
frame live, a second tab following through the `prefs` event, the hint
cookie, terminals following (after P3 lands), density.

---

## 4. The packages

Six packages with **disjoint file ownership**. A package edits only the
files it owns; when its change breaks something it doesn't own (a test
asserting an old string, a shared helper), it says exactly what in its
hand-off and the owner (or the integration step) fixes it.

### 4.0 For every package

- **Start** from `theme/base-two` at the contract commit: `web/theme.css`,
  `web/bx-theme.js`, `web/bx-icons.js`, `web/theme-boot.js` are there.
  Read §1 and §2 first; build against the tokens as they are.
- **`make theme-check` is red until the integration step** (the 1159
  fallbacks below are regenerated once, after every package has merged, so
  nobody fights over fallback literals). Run the other guards:
  `make fmt-check vet js-check js-test native-check shellcheck pins-offline large-files`
  and the tests for your area.
- **Until P1's lint lands**, check your files with the audit's scanners:
  `python3 /home/magik6k/buxon/.film-media/theme-preview/_tools/static_scan.py <worktree> <out.json>`
  (colours and font stacks), `…/emoji.py <worktree>` and `…/counts.py
  <worktree>` (radius, pills, black shadows), filtered to your files.
  Target: zero in your files, or an allowlist entry with a reason.
- **Look** at your work in both themes: the UI harness with
  `colorScheme: 'light'` and `'dark'` (P1 adds `HARNESS_THEME`; until then
  pass `ctxOpts`), or `make dev` with the system theme flipped. Compare with
  `/home/magik6k/buxon/.film-media/theme-preview/{current,v2-light,v2-dark}/`.
- **Rules** R1–R15 (§1.5); icons and strings per §1.6.
- **Don't restructure the UX**: restyle what is there; layouts, flows and
  features stay. The one new control is the theme/density setting (P2).
- **Commit** small, `area: …` subjects, the body says why and what was
  verified (AGENTS.md). Hand-off: what changed, what you couldn't verify,
  strings or exports others depend on, glyphs you needed and didn't have.

### 4.1 P1 — core: fonts, mechanism, guard, docs, integration

**Owns**

- `web/theme.css`, `web/bx-theme.js`, `web/bx-icons.js`, `web/theme-boot.js`,
  `web/xbin-client.js`, `web/bx-kit.js` (no change expected),
  `web/vendor/fonts/**` (new)
- `internal/server/static.go` (the D4 injection and the docs viewer),
  `internal/server/appearance.go` (new) and its test, `internal/server/static_test.go`
  (injection cases), `internal/obs/prefs.go` (only if the bucket reader
  moves to a shared package; then that package too)
- `hack/theme-fallbacks.mjs`, `hack/theme-lint.mjs` (new),
  `hack/theme-allow.txt` (new), `hack/theme-*.test.mjs` (new),
  `hack/xbin-client-partition.test.mjs`, `hack/xbin-client-ws.test.mjs`,
  `hack/vendor.sh`, `hack/vendor.sha256`, `hack/check-pins.sh`,
  `hack/check-js.mjs` (to add the new modules' helpers to the one-home
  list), `Makefile` (`theme-check`)
- `hack/ui-harness/{lib.js, shots.js, run.sh, seed.sh, app-help-shots.sh, fakebin/**}`,
  `hack/ui-harness/passes/{appearance.js, themecanary.js}` (new),
  `hack/ui-harness/passes/{oldscaffold.js, tileassets.js, apphelp.js}`
- `docs/frontend-kit.md`, `docs/protocol.md`, `docs/elements.md`,
  `docs/compat.md`, `docs/maintenance.md`, `docs/getting-started.md`,
  `docs/changelog.md`, `workspace-template/AGENTS.md`, and the final D184
  entry in `plans/DECISIONS.md`

**Changes**

1. **Fonts** (§2.8): vendor, license, pin; `check-pins.sh` and `vendor.sh`
   handle `web/vendor/fonts/`; verify loading from a sandboxed frame in
   every asset mode.
2. **Injection** (§2.3): `appearance.go` reads the person's `theme` and
   `density` (cached by size and mtime), `headInjection` adds the metas when
   set; the person per §2.3 (view-as, owner/no-auth `root`, frame and
   terminal tokens, none for backends). Tests: no pref → injection
   byte-identical to today; light, dark, comfortable; a garbage value →
   none; view-as → the viewed person's; a backend principal → none; a
   deployment URL and a partitioned tile carry it; the native runtime
   document carries it; the cache sees a rewrite.
3. **Docs viewer** (`docViewerHTML`): opt in, the person's metas, tokens
   only, body type (14/20), code and tables on tokens, prose links in the
   link colour.
4. **`xbin-client.js`**: import `/vendor/bx-theme.js`; accept
   `xbin:appearance` from `window.parent` (and `WORKSPACE` on a tile
   origin) → `applyAppearanceMessage`; in a top-level unsandboxed document,
   `rememberTheme(appearance().theme)` on load. Header comment updated.
5. **Own the contract modules**: tests for `bx-theme.js` (node with a small
   DOM shim, or in `appearance.js`) and `bx-icons.js` (`ICON_NAMES`,
   `iconSvg` escaping, unknown names); draw glyphs other packages ask for;
   the `.bx` control styles stay in step with product-ui §6.
6. **The guard** (§3): `theme-fallbacks.mjs` extensions, `theme-lint.mjs`,
   the allowlist, tests, `Makefile`, `docs/maintenance.md`.
7. **Harness**: `lib.js` creates contexts with `colorScheme: 'dark'` by
   default (Playwright's default is light, which would flip every existing
   screenshot once tiles opt in) and honours `HARNESS_THEME=light|dark`;
   new passes `appearance.js` and `themecanary.js` (§3.3); keep
   `oldscaffold.js` green (an old workspace's chrome on the new xbind:
   Night, nothing broken) and `tileassets.js` (fonts and the meta under
   strict gating).
8. **Docs**: `frontend-kit.md` (a Theme section: opting in, the token table
   or its summary with the rules of use, `bx-theme.js`, `bx-icons.js` and
   its names, `theme-boot.js`; the `theme.css` row's "tinted amber" →
   the focus colour); `protocol.md` (`xbin:appearance` in the tile↔shell
   table; the two metas in the injection list; the hint cookie; the shell
   bucket's `theme`/`density` keys); `elements.md` (the injection list;
   the terminal's 🔧 menu: "Workspace (follows the theme)", Concrete
   Night/Day); `compat.md` rule 5 (Night is the default; a document turns
   light only when it opts in; old names alias; fallbacks equal Night);
   `getting-started.md` and `workspace-template/AGENTS.md` ("dark-steel …
   amber" and the runtime-dot sentence: dots no longer encode runtime, P2);
   `changelog.md` (one entry for the whole change, drafted in §4.9; not
   BREAKING: nothing a tile does stops working).
9. **D184**: rewrite the design entry with what shipped, the tests, what
   was verified live.
10. **Integration** (§4.8), after P2–P6 have merged.

**Closes**: A1, A9 (docs viewer), A11 (document-level UA rules), A15 (the
glyph set), A23, A24; the guard and the canary keep A16 closed.

**Verify**: `go test ./internal/server/... ./internal/obs/...`;
`node --test hack/theme-*.test.mjs hack/xbin-client-*.test.mjs`; `make
js-check js-test pins-offline`; harness `appearance`, `themecanary`,
`oldscaffold`, `tileassets`, `scrollbars`; `make dev`: switch the setting
(after P2) and watch every open frame and a second tab follow.

### 4.2 P2 — the shell

**Owns**

- `workspace-template/shell/**` (`bx-shell.js`, `shell-css.js`, `bx-canvas.js`,
  `bx-side.js`, `bx-devices.js`, `bx-part-consent.js`, `bx-tile-admin.js`,
  `menus.js`, `shell-kit.js`, `shell-brand.js`, `shell-account.js`,
  `partition-mode.js`, `grid-layout.js`, `layout-sync.js`, `rev-draft.js`,
  `zorder.js`, `index.html`, `xbin.json`), `workspace-template/root/index.html`
- `web/bx-frame.js`, `web/frame-titlebar.js`, `web/frame-launcher.js`,
  `web/frame-deploy.js`, `web/frame-panels.js`, `web/frame-info.js`,
  `web/frame-testapi.js`, `web/bx-menu.js`, `web/bx-scroll.js`,
  `web/scroll-css.js`, `web/scroll-window.js` (no change expected)
- `hack/{menus,scroll-css,grid-layout,layout-sync,partition-mode,rev-draft,deploy-aware}.test.mjs`
- harness passes `devices, gridscale, layoutsync, menuopen, newtile,
  partitionconsent, partitionmark, partitionsentry, scrollbars, tabstrip,
  termsessions, termsets, viewas, vmtoggle, windows`

**Changes** (restyle, don't restructure)

1. **Opt in** `root/index.html` and `shell/index.html` (R12).
2. **The setting** in the settings menu: *Theme* (System · Light · Dark) and
   *Density* (Compact · Comfortable) as square segmented controls with
   `aria-pressed`. On change: `setAppearance()`, then `PUT`/`DELETE
   /api/xbin/prefs/theme|density` with `X-Prefs-Writer`. On a `prefs` event
   for `theme`/`density` from another writer: `GET` the key and
   `setAppearance()`. The settings selects (`share-org`,
   `share-target`, A11) get styles; the password inputs a placeholder rule.
3. **The relay** in `bx-frame.js` (§2.4): post `appearanceMessage()` on
   every iframe load (before the `_inFlight` early return in `_onFrameLoad`)
   and from `onAppearance()`, only when `follows()`; unsubscribe on
   disconnect. Spawned windows host `<bx-frame>`s, so they follow too.
4. **Window chrome** (product-ui §3) on canvas cards, floating and spawned
   windows and the terminal pop-up: 28 px title bar (`--bx-titlebar`, active
   `--bx-titlebar-active`), the live square (8 px: hollow
   `--bx-border-strong` while building, accent when live, danger outline
   and the word "failed" on a failed build, from the state the shell
   already has), name (UI 600) and path (mono 12, muted); controls 28 × 28
   with 16 px icons; close hover `--bx-close-hover`; 1 px
   `--bx-window-border` / `-active`; `--bx-shadow-rest` / `-active` (not the
   black ring at `shell-css.js:140, 405-407`); corners 2 px; resize handles
   6 px. Active = the window on top of the shared z-order (`zorder.js`).
5. **Part tabs**: a 3 px strip on the top edge of system windows only:
   `--bx-part-tab-terminal` on the terminal pop-up while a terminal tab is
   active, `--bx-part-tab-agent` while an agent session tab is, 
   `--bx-part-tab-admin` on the admin console's windows (`tiles/admin`).
   User apps get none.
6. **Top bar, tabs, sidebar**: the top bar's chips lose their status-coloured
   dots (`bx-shell.js:1730, 1761, 1762`, A17); screen tabs become text tabs
   with a 2 px accent underline (no rounded tab tops); the sidebar on
   `--bx-sidebar`, 28 px rows, hover `--bx-control-hover`, selected =
   `--bx-selection` + 2 px accent rule; micro section labels; the runtime
   dots (`shell-kit.js` `RUNTIME_COLOR`) stop encoding runtime in colour:
   the row's square shows the tile's state, the runtime goes in the tooltip.
7. **Canvas**: `--bx-bg` with the grid marks in `--bx-canvas-dot` (keep the
   grid geometry).
8. **View-as banner**: `--bx-elevated-bg` with `--bx-elevated-ink`, 28 px,
   the `eye` glyph instead of 👁 (A18).
9. **Alerts and toasts**: the alert bar (`shell-css.js:161-167`) and `.st-info`
   (`:440`) on status tokens with icon + word; toasts bottom right, panel +
   border + status icon + word + `--bx-shadow-pop`; the setup card's dismiss
   (`bx-shell.js:1709`).
10. **Menus and popovers** (`bx-menu.js`, `menus.js`, the admin popover, the
   settings menu, the devices panel, scrims): `--bx-shadow-pop`,
   `--bx-scrim`, 2 px, 28 px rows, icons for 📌 💾.
11. **Accent ink** where text sits on the accent (`shell-css.js:106, 309, 626`;
   `frame-titlebar.js:277, 282`; `frame-deploy.js:457, 464`;
   `frame-launcher.js:238, 243`; `bx-devices.js:116`) and the default shell
   mark (`shell-brand.js:42-46`: plate `--bx-accent`, X `--bx-accent-ink`;
   a workspace's own branding icon is untouched).
12. **Glyphs**: every Arial glyph button and emoji in your files → icons
   (`.tab .x`, `.card .head` buttons, `aside.collapsed .expand`, the pop-up's
   bar: `>_ { } ▤ ⇄ ⇋ ⇈ ⟲ ⋯ ✕ +`); `font: inherit` on buttons; selects'
   option text loses its emoji (🏢 🌐 🔌 ⛔ 🎮); draw `icon` fields from
   P3's view models (§1.6) in `frame-deploy.js`/`frame-titlebar.js`. The
   folder dialog keeps a person's own emoji icon; its placeholder becomes
   words, and a folder without one shows the `folder` glyph.
13. **The partition marker** (`partCss`, `bx-part-consent.js`) on
   `var(--bx-part, #3FB5A3)`.
14. **The QR code** (devices panel) stays dark on light for scanners
   (`theme-ok`).
15. **Radius, fonts, placeholders, focus** throughout your files (R4–R10);
   the 44 radius rules in `shell-css.js`.

**Closes**: A4 (shell part), A7 (shell part), A8 (shell part), A11
(settings selects), A12 (shell glyph buttons), A13 (shell inputs), A14
(shell mark), A15 (24 lines), A16 (your files), A17 (shell), A18, A19
(windows' edge), A24 (verify).

**Verify**: `node --test` your tests; harness passes you own in both
themes; screenshots of canvas, settings menu, terminal pop-up, view-as,
toasts and the phone layout in Day and Night; the canary audit on the
shell finds nothing (after P1's pass, or the preview tool); switching the
setting changes the shell and every frame at once.

### 4.3 P3 — core elements

**Owns**

- `web/bx-terminal.js`, `web/term-palettes.js` (new), `web/term-predict.js`,
  `web/term-src.js`, `web/term-links.js`, `web/term-sessions.js`,
  `web/bx-logs.js`, `web/logs-partition.js`, `web/bx-code.js`, `web/bx-prs.js`,
  `web/bx-deploy.js`, `web/deploy-state.js`, `web/deploy-panel.js`,
  `web/deploy-branch.js`, `web/bx-grants.js`, `web/bx-grant-row.js`,
  `web/bx-bindings.js`, `web/bx-multiselect.js`, `web/bx-netrules.js`,
  `web/bx-allow.js`, `web/bx-dialog.js`, `web/bx-agent.js`, `web/bx-md.js`,
  `web/agent-*.js`, `web/partitions.html`, `web/partitions-*.js`,
  `web/signin-scan.js`, `web/events-socket.js`, `web/xb-native.js`,
  `web/xb/**`
- `hack/{agent-fold,agent-pages,agent-slash,agent-tools,deploy-branch,deploy-panel,deploy-state,deploy-target,events-socket,logs-partition,netrules,partitions-page,partitions-realtime,signin-scan,term-links,term-predict,term-predict-trace,term-sessions,term-src,native-docs,bx-native-probe,xbn-runner,xb-native,xb-native-examples,xb-native-widget,xb-render,xb-tile-resource}.test.mjs`
- harness passes `agentsignin, agenttab, agentlong, agentlongperf,
  deploybranches, deployments, livereload, livepreview, partitionlogs,
  personpage, personpageasked, personalbinds, predict, termrun`; a new
  `termtheme.js` if you want the terminal's follow covered by its own pass

**Changes**

1. **Terminal** (A2, §2.6): the named palettes move to `term-palettes.js`
   with Concrete Night and Concrete Day (values equal to `theme.css`; a
   test compares them); `default` = "Workspace (follows the theme)", built
   from `--bx-term-*` with `token()` and re-applied in `onAppearance()`
   (the background was read once, `bx-terminal.js:248, 593`); the 🔧 gear
   → `settings` glyph, its menu on tokens (panel, border, `--bx-shadow-pop`,
   2 px, `var(--bx-font)` instead of `system-ui`, `:178, 194`); the lag
   chip and ⚡ (`bolt`) on tokens (`:170-174`); xterm's `fontFamily` from
   `--bx-mono` (`:567`), `document.fonts.load()` before `open()` and a refit
   after; the default size from `--bx-term-size` until the person picks
   one; `drawBoldTextInBrightColors: false`.
2. **Logs** (A3): `bx-logs.js` the same way (`:83, 107, 112-113, 155-156`).
3. **Code and diffs** (A6): `bx-code.js` on `--bx-syn-*` with the class map
   in §1.1, diffs on `--bx-diff-*`, panes on `--bx-code-bg`, `pre, code`
   with `font: var(--bx-font-code)` (`:148`, the UA-monospace finding), the
   file tree's 📁 📄 → `folder`/`file`, line numbers `--bx-subtle`;
   `bx-prs.js` (`:59-61, 75-79, 92`) and `bx-deploy.js` (`:193-196`) diffs
   likewise; `bx-md.js` code blocks and tables.
4. **View models with emoji** (§1.6): `deploy-state.js` (13 lines),
   `deploy-panel.js`, `deploy-branch.js`, `bx-netrules.js` (8),
   `bx-allow.js` (3), `agent-cards.js` `KIND_ICON` (read → `doc`, edit →
   `pencil`, delete → `trash`, move → `arrow-right`, search → `search`,
   execute → `terminal`, think → `thought`, fetch → `globe`, switch_mode →
   `refresh`, other → none): text without the emoji, an `icon` field with
   the glyph name; update your tests to the new strings.
5. **Actions and plates**: green action buttons with white text become
   primary (`bx-grants.js:51`, `bx-bindings.js:57`, A5); a grant request is
   a plate (product-ui §8): header, rows, the note, Approve (primary) and
   Deny (secondary); cards lose `--bx-shadow` for a border.
6. **Dialogs and pickers**: `bx-dialog.js` (`:37, 43, 72`: square, 1 px
   border-strong, title type, `--bx-shadow-pop`, `--bx-scrim`, accent ink
   on the primary; plan-then-ask stays), `bx-multiselect.js` (`:48`).
7. **Agent tab**: `bx-agent.js` (`:151, 156` accent ink; ⚠ → `warning`),
   `agent-signin.js` (`:53`), tool calls as mono blocks with exit status,
   fonts from tokens (the `--bx-sans` fallbacks).
8. **Partitions page**: `partitions.html` opts in and loads
   `/vendor/theme-boot.js` before the stylesheet (its CSP allows `'self'`
   scripts); the inline `background: var(--bx-bg, #1b1e24)` and
   `partitions-css.js` on tokens; ⚠ → `warning` (`partitions-sections.js:246`).
9. **Native reference renderer** (A10): `xb/render-theme.js`'s colour roles
   from Concrete Day/Night (accent cobalt/periwinkle, on-accent the accent
   ink, bubble panel-2, separators border, ok/warn/danger, chart-1…6 from
   the ANSI order of §1.1); fonts: body stays the system font (iOS idiom,
   product-ui §10), large titles Bricolage Grotesque 800, mono JetBrains
   Mono; the app's shapes stay (allowlisted, §6); `render-content.js`'s
   xb-terminal (`:257-262`), `preview-host.js` (`:55-63`) and
   `fixture.html` (`:20-25`) on tokens. Optional: `preview-host.js` sets
   `<xb-view theme>` from `appearance()`.
10. **The rest of your files**: radius, fonts, placeholders, focus, emoji,
   `signin-scan.js`.

**Closes**: A2, A3, A4 (elements part), A5 (elements part), A6 (elements
part), A7 (elements part), A8 (terminal and log chips), A10, A12 (elements
part), A13 (elements part), A15 (about 40 lines), A16 (your files).

**Verify**: your `node --test` files; harness passes you own in both
themes; the audit's ANSI sample (`printf '\e[1mansi\e[0m'; for i in 0 1 2 3
4 5 6 7; do printf '\e[3%sm %s \e[0m' $i $i; done; …`, see
`v2-*/terminal-ansi.png`) in a terminal in both themes, then switch the
setting with it open; the code-diff screen in both themes against
`v2-light/code-diff.png`; the partitions page with the cookie set and not.

### 4.4 P4 — xbind's own pages

**Owns**

- `internal/server/{login.go, webticket.go, requestpage.go, partitionpage.go,
  tilenav.go, tileorigin.go, branding.go}`, the sign-in and invite handlers
  in `internal/server/server.go` (no other change there), `internal/server/pagetheme.go`
  (new), and the `_test.go` files for these
- harness passes `branding.js` (sign-in branding) and `partitionswitch.js`

**Changes**

1. `pagetheme.go`: the `theme.css` link and `<meta name="xbin-theme">` from
   the `xbin_theme` cookie (exactly `light` or `dark`; anything else →
   none), for every page below. Pages put `data-bx-theme="auto"` on
   `<html>`.
2. **Sign-in, invite, invite-invalid** (`login.go:123-216`), **"Continue
   as"** (`webticket.go:350-373`), **request access** (`requestpage.go:36-51`,
   its GitHub-dark palette), **partition switch** (`partitionpage.go:125-128`,
   its own light/dark pair), **tile navigation** (`tilenav.go:182`) and **the
   tile-origin refusal** (`tileorigin.go:515`): Work volume (Q11, brand
   §12): concrete and ink, a plate, the workspace's branding (D76) or the
   one-colour wordmark "xbin" (lowercase) in `--bx-display` 800; no fields;
   tokens only; primary button in the accent, inputs per product-ui §6
   (28 px, border-strong, the focus ring), errors and warnings as icon +
   word (the `error`/`warning` path strings from `bx-icons.js` inline as
   `currentColor` SVG).
3. **The default mark** (`branding.go` `defaultMarkSVG`, amber `#f5a623` and
   `#23272e`): replaced by the wordmark until the chosen mark is drawn
   (Q1, §6). The favicon data URI stays until then.
4. **CSP**: pages that now link `/vendor/theme.css` allow `style-src 'self'`
   (plus `'unsafe-inline'` where it was) and `font-src 'self'`
   (`webConfirmCSP`, the two `default-src 'none'` navigation pages); script
   rules unchanged.
5. **Tests**: each page carries the opt-in and the link, honours the cookie
   (light, dark, absent, garbage), keeps its CSP's script and frame rules,
   shows the branding; update assertions that matched "X/BIN".

**Closes**: A9 (pages), A12 (pages' stacks), A14 (sign-in mark).

**Verify**: `go test ./internal/server/ -run '<your tests>'` and `go vet`;
screenshots of each page with light and dark system and with each cookie;
the harness `branding` and `partitionswitch` passes.

### 4.5 P5 — shipped tiles, templates and examples

**Owns**

- `workspace-template/tiles/**` (admin with `admin-css.js`, `admin.js`,
  `shared.js`, `plain-tabs.js`, `tabs/**`; `apidocs`; `manager`;
  `organisations`), `workspace-template/apps/welcome/**`
- `builtin-tiles/**` and `hack/tile-versions.txt`
- `builtin-templates/**` (agent with `native/`, `model/`, `test/`;
  `agent-messaging-bridge`; `coding-sandbox`; `starter`)
- `examples/**`, `internal/scaffold/templates.go` and its test
- `hack/{admin-partitions,admin-settings,coding-sandbox-ui,tile-js}.test.mjs`,
  `hack/agent-template-*.test.mjs`
- harness passes `admindeploy, adminmap, adminpartitions, agentconvs,
  agentharness, agenthomes, agenthosted, agentmoves, agentsandbox,
  agentsignins, agenttask, agenttemplate, agenttemplatelong, channels,
  channelspartitioned, codingsandbox, ingressmulti, personalplane,
  sandboxes, sandboxnet, sandboxterminal, settings, templatecard,
  tilepages, users`

**Changes**

1. **Opt in** every `index.html` (R12); `bx new`'s scaffold
   (`internal/scaffold/templates.go`) too: new tiles start following the
   person, on tokens only.
2. **Admin console** (product-ui §9): the pill group tabs become text tabs
   with the 2 px accent underline (`admin.js:79`, `admin-css.js:20`, A17);
   tables 32 px rows, tabular figures, a sticky header with a 2 px
   text-coloured rule, hairlines, mono for times and identifiers;
   component paths in the text colour (mono), not accent links; "disable ·
   hide" as quiet buttons (A17: the cobalt column, Q6); the green
   "create" (`admin-css.js:85`) and white-on-red (`:53`) become primary and
   the danger outline; status chips (healthy, idle, host) square badges,
   20 px, micro caps, 1 px border, status icon + word; the alert copy
   (`:30-35`), `.lv-read` (`:125-126`), the VM badge (`:304, 346`) and the
   chart series (`:335-336`, ANSI order) on tokens; its code view on the
   syntax tokens (`:267-275`); placeholders (`.q`); 🔒 🏢 ⚠ ⛔ 🔑 👥 👤 →
   glyphs, drawing P3's `icon` fields; the `docs/partitions.md` link colour.
3. **Organisations, manager, apidocs, welcome**: accent ink
   (`manager/index.html:44`), green actions (`organisations.js:67`), radius,
   fonts, emoji; the welcome notes teach the theme per P1's docs.
4. **Agent template** (the film's `apps/lark` is one of its instances):
   accent ink (`index.html:277`), the black shadows (`:56, 112, 138, 176,
   407, 532`, `harness-board.js:127`), placeholders (`:583, 651`), 📎 →
   `paperclip`, 🔒 private/Internal → `lock` + word, 📌 Task → `pin` + word,
   every ⚠ → `warning`; `--bx-yellow` → `--bx-warn` (37 uses);
   conversation-class icons drawn from the emoji → name table (§1.6), the
   class form's placeholder in words; chat per product-ui §8: a person's
   turns on panel-2, the agent's on panel, tool calls as mono blocks with
   exit status, no faces; `native/` strings lose emoji (vocab icons where
   the primitive takes `icon`, words elsewhere); 40 radius rules.
5. **Coding sandbox, messaging bridge, starter**: fonts (own stacks),
   accent ink (`coding-sandbox/index.html:53`,
   `agent-messaging-bridge/index.html:40`), `--bx-surface-2` → `--bx-panel-2`
   (starter).
6. **Builtin tiles** (chat, egress-approver, llm-gw, prometheus-viewer,
   s3-archiver, sandbox-terminal, traefik, webhooks): opt in, tokens, own
   font stacks → tokens, accent ink (`chat/index.html:35`,
   `sandbox-terminal/index.html:36`, `llm-gw.js:62`,
   `s3-archiver/index.html:22`, `webhooks/index.html:27`), green actions
   (`egress-approver/index.html:33`), emoji (chat 🔧 ⚠, prometheus ⚠); each
   tile's `tile.json` version + 1 with a changelog line, then
   `UPDATE_TILE_VERSIONS=1 go test ./internal/builtins -run TestTileVersions`.
7. **Examples** (calendar, counter-go, email): opt in, tokens; they are
   integration fixtures and docs: keep them working.

**Closes**: A4, A5, A6, A7, A8, A12, A13 (their tile parts), A15 (about 150
lines), A16 (your files), A17 (admin), A21 (template side).

**Verify**: `node --test` your tests (`agent-template-*` assert strings:
update them to §1.6); `make tile-check` for the builtin backends you
touch; `go test ./internal/builtins ./internal/scaffold`; the harness passes
you own in both themes; admin, agent chat and each builtin tile against
`v2-light`/`v2-dark`; `make integration` if an example's backend changed
(it shouldn't).

### 4.6 P6 — the film set

**Owns**: `hack/demo/**` (`tiles/**`, `tiles/_lib/**`, `seed.sh`,
`stills.js`, `up.sh`, `reset.sh`, `README.md`; `data/**` only where it
carries styling, never the story), `hack/demo-*.test.mjs`.

**Changes**

1. Opt in every demo tile page; `_lib/ui.js` on tokens: drop `--blue`,
   `--teal`, `--violet` (`:69-75`); status via the status tokens.
2. People's avatars: square (2 px), concrete with ink initials, or a hue
   derived from the person's id with contrast checked in both themes
   (`theme-ok: identity colour derived from an id`). No gradients.
3. CRM: card shadows (`--bx-shadow`), column hues → neutral columns with a
   text-coloured rule, the logo gradient flat (brand: no gradients);
   onboarding: the progress gradient flat (`--bx-ok` for done, the track
   `--bx-panel-2`); calendar: event categories from the ANSI order (§1.1),
   times in mono; email, expenses, ops report, telematics, host exporter:
   tokens, radius, fonts.
4. `stills.js`: shoot `DEMO_THEME=dark|light|both` (default dark, so the
   existing stills stay as they are); the seed may give some people a theme
   preference if the film wants both on screen (`PUT /api/xbin/prefs/theme`
   as each person).
5. After P5 merges, re-seed and check the agent instance (`apps/lark`) and
   the coding sandbox on screen.

**Closes**: A20, A21 (film side), the audit's `/c/apps/{calendar, crm,
onboarding}` findings, A16 (film files).

**Verify**: `node --test hack/demo-*.test.mjs` (the fiction rules hold);
`HARNESS_SEED=demo … hack/ui-harness/run.sh --keep` stills in both themes;
compare with `.film-media/theme-preview/v2-*`; the scanners show zero for
`hack/demo/tiles`.

### 4.7 Dependencies and merge order

- Everyone builds against the contract commit; no package waits for
  another to start.
- Contracts between packages, fixed above: the relay message (P2 sends, P1
  receives; both in `bx-theme.js`), the hint cookie (P1 writes, P4 and the
  partitions page read), `theme-boot.js` (P1, used by P3), view-model
  `icon` fields (P3 → P2, P5), strings without emoji (§1.6).
- Merge order: **P1, P3, P2, P4, P5, P6**, then the integration step. Any
  order compiles; this one lets each merge's tests run against what they
  render (P3's `icon` fields before P2's and P5's renderers; P5 before P6's
  re-seed).

### 4.8 Integration (P1, after all six have merged)

1. `node hack/theme-fallbacks.mjs --fix` over every tree; commit.
2. `node hack/theme-lint.mjs`: zero findings beyond the allowlist; fix
   strays or send them back.
3. `make check` green (theme-check included); `make tile-check`;
   `make integration` for the examples.
4. Harness: every pass (default dark), then `appearance`, `themecanary`,
   `scrollbars`, `windows`, `oldscaffold`, `tileassets` with
   `HARNESS_THEME=light` too.
5. The audit's own numbers, rerun on the branch: colour literals outside
   tokens 0 (allowlisted files aside), font stacks 0, emoji 0 (user content
   aside), radius rules on the token 100 % (or 0), pills 0, black
   `rgba(0,0,0,…)` 0, the 10 audit screens clean under the canary.
6. Film stills in both themes (with P6); the docs and changelog read true;
   D184 final.

### 4.9 The changelog entry (draft for P1)

> - **The workspace follows your system's light or dark setting** (Base Two:
>   Concrete Day and Concrete Night, D184). Settings → Theme overrides it
>   per person (System, Light, Dark), and Density offers Comfortable;
>   terminals follow unless you pick a palette in 🔧, which now lists
>   Concrete Night and Concrete Day. A tile document follows only when it
>   opts in with `<html data-bx-theme="auto">` and links `/vendor/theme.css`;
>   every other document that links the sheet stays dark, with new values
>   under the same tokens (`--bx-green`, `--bx-amber`, `--bx-red` keep
>   working as aliases of `--bx-ok`, `--bx-warn`, `--bx-danger`). New
>   tokens: surfaces, focus, status tints, shadows, window chrome, part
>   tabs, syntax and diff colours, the terminal's 16 colours, type and
>   density ([frontend-kit.md](/docs/frontend-kit.md) §Theme). New modules:
>   `/vendor/bx-theme.js` (follow the person's appearance from code) and
>   `/vendor/bx-icons.js` (`<bx-icon name>`). xbind adds
>   `<meta name="xbin-theme">` / `<meta name="xbin-density">` to a tile
>   document when the person chose a theme or density, and frames hear
>   changes as `xbin:appearance` ([protocol.md](/docs/protocol.md)). Corners
>   are 2 px; the fonts are Instrument Sans, JetBrains Mono and Bricolage
>   Grotesque, served from `/vendor/fonts/`. Builtin tiles <list with new
>   versions> follow the theme; `bx builtin update` brings the shell, root
>   and admin console (until then an older shell stays dark). Nothing to
>   change.

---

## 5. The contract commit and the fallbacks P1 regenerates

This commit writes `web/theme.css`, `web/bx-theme.js`, `web/bx-icons.js`,
`web/theme-boot.js`, the scroll CSS in `web/bx-scroll.js`, this plan and the
D184 design entry. Checked: `make js-check` parses (560 scripts),
`hack/scroll-css.test.mjs` passes, the mechanism and the icon set render in
Chromium (scratch probes: first paint, relay, density, overrides,
`theme-boot.js`; a contact sheet of every glyph at 1x and 2x in both themes).

`node hack/theme-fallbacks.mjs` (check mode) now fails, as expected:
**1159 fallbacks disagree with `theme.css`.**

| Kind | Count | What P1 does |
|---|---|---|
| Old names (`--bx-red` 114, `--bx-amber` 73, `--bx-green` 65) | 252 | the script resolves the `var()` aliases to Night literals, then `--fix` |
| `--bx-sans` | 10 | exempt (a font token), no rewrite |
| `--bx-part` (now defined) | 2 | `--fix` (drops the `light-dark()` fallbacks for `#3FB5A3`) |
| New Night values (`--bx-muted` 219, `--bx-border` 200, `--bx-accent` 135, `--bx-text` 117, `--bx-panel-2` 93, `--bx-panel` 86, `--bx-bg` 26, `--bx-radius` 9, `--bx-term-bg` 5, `--bx-shadow` 5) | 895 | `--fix` |

By tree: `web/` 487, `workspace-template/shell` 290 (`shell-css.js` 209),
`workspace-template/tiles` 280 (`admin-css.js` 154), `builtin-tiles` 74,
`builtin-templates` 17, `workspace-template/apps` 11. They are regenerated
once, in the integration step (§4.8), not package by package: the packages
rewrite many of these lines anyway, and a mechanical rewrite of the same
lines in parallel would only make conflicts.

Baselines for the burn-down (the audit's scanners on this commit):

| Measure | Now | Target |
|---|---|---|
| colour literals outside token fallbacks | 521 in 49 files (+279 in `theme.css`, allowlisted); `examples/` + `hack/demo/tiles/` 24 more | 0 outside the allowlist |
| hard-coded font stacks | 21 | 0 |
| colour emoji in UI source | 213 lines in 69 files | 0 (user content aside) |
| `border-radius` declarations / on the token / pills | 396 / 12 / 40 (+54 in examples and the film set) | all on the token or 0 / 0 pills |
| black `rgba(0,0,0,…)` | 64 in 20 files (5 are Night's shadow tokens in `theme.css`) | 0 outside `theme.css` |
| `::placeholder` / `:focus-visible` rules | 6 / 10 (2 of each in `theme.css`) | every input-bearing shadow root / every interactive one |

### Audit items

From the review's "Unthemed: what W9 must fix", the canary audit and the
static scan. Line references are `promo/film-set`'s.

| # | Finding | Package |
|---|---|---|
| A1 | the theme is dark-only: one token set, `color-scheme: dark`, no switch (`theme.css:8-28`) | contract, P1 |
| A2 | the terminal keeps xterm's white foreground and Tango palette: Day is white on white; the background is read once (`bx-terminal.js:90, 246-249, 248, 593`) | P3 |
| A3 | the log viewer likewise (`bx-logs.js:83, 155-156`) | P3 |
| A4 | no accent ink: dark ink on the accent (`shell-css.js:106, 309, 626`, `frame-titlebar.js:277, 282`, `frame-deploy.js:457, 464`, `frame-launcher.js:238, 243`, `bx-devices.js:116`, `shell-brand.js:44` → P2; `bx-dialog.js:72`, `bx-agent.js:151, 156`, `agent-signin.js:53` → P3; `chat/index.html:35`, `sandbox-terminal/index.html:36` → P5); white ink (`admin.js:79`, `admin-css.js:20`, `manager/index.html:44`, `agent/index.html:277`, `llm-gw.js:62`, `s3-archiver/index.html:22`, `webhooks/index.html:27`, `agent-messaging-bridge/index.html:40`, `coding-sandbox/index.html:53` → P5) | P2, P3, P5 |
| A5 | green action buttons with white text, 1.8:1 in Night (`bx-grants.js:51`, `bx-bindings.js:57` → P3; `admin-css.js:85, 53`, `organisations.js:67`, `egress-approver/index.html:33` → P5) | P3, P5 |
| A6 | a fixed One Dark syntax and diff palette, 1.7–2.4:1 in Day (`bx-code.js:160-166, 183, 188, 190-197`, `bx-prs.js:59-61, 75-79, 92`, `bx-deploy.js:193-196` → P3; `admin-css.js:267-275` → P5) | P3, P5 |
| A7 | 60 black shadows and rings tuned for dark (`shell-css.js:122, 140, 183, 190, 278, 281, 286, 405-407`, `bx-frame.js:167`, `bx-menu.js:60, 68, 130`, `bx-devices.js:93, 99` → P2; `bx-dialog.js:37, 43`, `bx-multiselect.js:48` → P3; `agent/index.html:56, 112, 138, 176, 407, 532`, `harness-board.js:127` → P5; the CRM's cards → P6) | P2, P3, P5, P6 |
| A8 | status surfaces with fixed hues (alert bar `shell-css.js:161-167`, `.st-info` `:440`, setup card `bx-shell.js:1709` → P2; `admin-css.js:30-35, 125-126, 304, 335-336, 346` → P5; terminal gear and badges `bx-terminal.js:170-174`, `bx-logs.js:108, 112-113` → P3) | P2, P3, P5 |
| A9 | server-rendered pages never load `theme.css` (`login.go:128-216`, `webticket.go:357-373`, `requestpage.go:40-51`, `partitionpage.go:126-128`, `tilenav.go:182`, `tileorigin.go:515` → P4; the docs viewer `static.go:716-725` → P1) | P4, P1 |
| A10 | the native renderer's own amber palette (`xb/render-theme.js:31-44`, `render-content.js:257-262`, `preview-host.js:55-63`, `fixture.html:20-25`) | P3 |
| A11 | UA controls: settings-menu selects with no CSS (`bx-shell.js:1440-1442`) → P2; no `accent-color` → `theme.css` (contract) | P2, contract |
| A12 | fonts outside tokens: xterm and logs (`bx-terminal.js:178, 194, 567`, `bx-logs.js:107, 112, 155`), `pre` without `font: inherit` (`bx-code.js:148`) → P3; shell buttons in Arial (`shell-css.js:76, 209, 383`) → P2; builtin and template stacks → P5; server pages → P4 | P2, P3, P4, P5 |
| A13 | placeholders in the UA grey: 4 rules in the product (`agent/index.html:583, 651`, admin `.q` → P5; the shell's password fields → P2) | P2, P5 (+ P3 in its elements) |
| A14 | logos and the default marks in amber (`shell-brand.js:42-46` → P2; `branding.go` default mark → P4; the image files wait for the mark, §6) | P2, P4 |
| A15 | colour emoji as icons: 213 lines in 69 files (P2 ≈ 24, P3 ≈ 40, P5 ≈ 150) | P2, P3, P5 |
| A16 | shape and focus not tokenised: 396 radius rules, 10 on the token, 40 pills; 8 `:focus-visible` rules | every package; the guard |
| A17 | status colours as decoration (top-bar chips `bx-shell.js:1730, 1761, 1762`, `RUNTIME_COLOR` `shell-kit.js:17-23` → P2; pill group tabs, accent links down the admin tables → P5) | P2, P5 |
| A18 | Day title bars vanish (1.06:1), floats held by a black ring; no window, part-tab or elevated tokens | contract, P2 |
| A19 | Night is one dark mass (panel vs canvas 1.09:1) | contract (lift), P2 (window edge) |
| A20 | the film set's own hues: `_lib/ui.js:69-75`, CRM gradient and columns, onboarding gradient, calendar events, avatars | P6 |
| A21 | the agent instance on screen (`/c/apps/lark/`): 📎 🔒 📌, placeholders | P5, P6 |
| A22 | xterm's helper textarea colours come from `vendor/xterm.css` (vendored, invisible): not ours | — |
| A23 | the workspace `AGENTS.md`, `frontend-kit.md`, `elements.md` and `getting-started.md` teach dark-steel, amber and runtime-coloured dots | P1 |
| A24 | the scroll tint in the accent (amber): the focus colour now | contract (done), P2 verifies |

---

## 6. Open items (not in W9)

- **The mark** (Q1: M4 by default) and the wordmark asset: favicons,
  `logo.*`, `apple-touch-icon.png`, the sign-in mark and the shell's default
  mark change when it is drawn. Until then the wordmark is text.
- **The iOS app**: its web views get the person's override through the
  injected meta while its native chrome follows the phone. The app can
  relay `xbin:appearance` itself (its document-start script already relays
  messages), if the owner wants app pages to follow the phone instead.
  The native renderer keeps the app's shapes (iOS idioms, product-ui §10)
  until the app adopts Base Two corners.
- **Comfortable density** ships as tokens and the setting; how far each
  surface uses `--bx-row`/`--bx-pad` beyond the shell and admin tables is
  the packages' judgement.
- **A per-tile override** (a tile pinning one theme) is deliberately not
  offered.
