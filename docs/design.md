# Designing a tile: the Base Two guide

How a tile should look and read in this workspace. This is the product
side of xbin's brand, Base Two (D183), as the workspace draws it in two
themes, Concrete Day and Concrete Night (D184). It is written for people
and for coding agents building tiles. The mechanics (opting in, every
token, `<bx-icon>`, `/vendor/bx-theme.js`) are in
[frontend-kit.md](/docs/frontend-kit.md) §Theme; this page is how to use
them well.

## The idea

The brand is loud on its posters and quiet at work. The workspace is where
people spend the day, so a tile is a working surface: concrete, ink and one
accent, with colour only where it means something. Three tests for every
screen:

- **Calm.** Could someone look at it all day, in light and in dark, on a
  laptop and on a large monitor? Nothing decorates; nothing moves unless
  something changed.
- **Labelled.** Every state is named in words. Colour is the second cue,
  never the only one.
- **Honest.** Show the real thing: paths, commands, hostnames, measured
  times, the actual rule or grant. No invented numbers, no estimate shown
  as a fact, no progress percentage without a source.

## Start from this

```html
<!doctype html>
<html lang="en" data-bx-theme="auto">
<head>
  <meta charset="utf-8">
  <link rel="stylesheet" href="/vendor/theme.css">
  <script type="module" src="/vendor/bx-icons.js"></script>
</head>
<body class="bx">
```

`bx new` writes this page (the icons' script aside).
`data-bx-theme="auto"` makes the page follow the person's light or dark
choice; `body.bx` gives it the panel, the text colour, the UI type and the
workspace's controls. Then style with the tokens, and look at the tile in
both themes (Settings → Theme: Light, then Dark) before you call it done.

## The rules

1. **Tokens only.** Every colour, font, corner and shadow is a
   `var(--bx-…)`. A literal colour is right in one theme and wrong in the
   other.
2. **One accent, for actions.** `--bx-accent` (cobalt in Day, periwinkle in
   Night) marks the primary button, the selection, the active tab's
   underline and links in prose. Nothing else: not identifiers, paths,
   status, table row actions or decoration. One primary button per view.
3. **Status is a glyph, a word and a colour**: the `ok`, `warning`,
   `error` or `info` glyph from `<bx-icon>`, the word beside it, the glyph
   in `--bx-ok` / `-warn` / `-danger` / `-info`, and that status's `-bg`
   tint behind an alert or a badge. Never a coloured dot alone; a field
   colour (below) never means a status.
4. **Square corners.** `border-radius: var(--bx-radius)` (2px) or 0. No
   pills, no circles: a status dot is an 8px square, an avatar a square.
5. **Small, steady type.** `font: var(--bx-font)` for UI text (13/18).
   Running text never goes below 13px: 11px is only the caps section label
   (`.bx-label`), 12px only timestamps and meta (`--bx-font-meta`). Paths,
   commands, hostnames, ids and times are mono (`--bx-mono`). The display
   face (`--bx-font-heading`, `--bx-font-hero`) is for headings and empty
   states only. Numbers that line up use tabular figures.
6. **Dense, on a 4px grid.** Rows `var(--bx-row)` (28px, 32px for people
   who chose Comfortable), controls `var(--bx-control-h)`, panel padding
   `var(--bx-pad)`, gaps in multiples of 4. Tiles are often narrow: reflow,
   never scroll sideways.
7. **Glyphs, not emoji.** `<bx-icon name="…">` draws the workspace's 16px
   glyphs in `currentColor`. No emoji as icons; never sparkles, robots,
   brains, stars or faces. A person's own emoji (a name they typed) is
   their content: show it as they wrote it.
8. **Flat.** Borders separate things; shadows only lift what floats over
   the page (`--bx-shadow-pop` for menus and popovers). No gradients, glows,
   blur, glass, illustrations or photographs.
9. **No brand fields in a tile.** The four field colours (yellow, green,
   magenta, cobalt) belong to the workspace's own chrome: the part tabs on
   system windows, the yellow banner of an elevated mode, the first-run
   screen. A tile shares the screen with other tiles, so it uses none, and
   it never draws a part tab of its own.
10. **Motion confirms a change and stops.** Use `--bx-dur-ui` /
    `--bx-dur-panel` with `--bx-ease-out` (all 0 under reduced motion).
    Nothing loops, pulses or shimmers; a "working" label with the elapsed
    time shows progress.

## Components

The `.bx` controls are already right; build the rest from these.

**Buttons** are verbs: "Approve grant", "Restore backup", "Open
terminal". A `.bx` button is secondary; add `primary` for the one action
that matters, `quiet` for a text-only action (a table row's), `danger`
for a destructive one (a red fill only inside the confirmation that asks).

```html
<button class="primary">Approve grant</button>
<button>Deny</button>
<button class="quiet">Show rule</button>
```

**Fields** are 28px with the strong edge and the focus ring (`.bx input`,
`select`, `textarea`); a placeholder is an example, not the label. Put the
label above the field, and set paths and hostnames in mono.

**Lists and trees**: rows of `var(--bx-row)`; `--bx-hover` under the
pointer; the selected row on `--bx-selection` with a 2px accent rule on its
left.

```css
.row { display: flex; align-items: center; gap: 8px; min-height: var(--bx-row); padding: 0 8px; }
.row:hover { background: var(--bx-hover); }
.row[aria-selected="true"] {
  background: var(--bx-selection); color: var(--bx-selection-text);
  box-shadow: inset 2px 0 0 var(--bx-accent);
}
```

**Tabs** are words with a 2px accent underline on the active one. No
pills, no rounded tab tops.

```css
/* button.tab: as specific as the sheet's .bx button, and later, so it wins */
button.tab { padding: 0 12px; min-height: var(--bx-row); border: 0; border-bottom: 2px solid transparent;
  border-radius: 0; background: none; color: var(--bx-muted); }
button.tab[aria-selected="true"] { color: var(--bx-text); border-bottom-color: var(--bx-accent); }
```

**Tables**: a sticky header with a 2px rule in the text colour, hairlines
between rows, tabular figures, mono for times and ids, and row actions as
quiet buttons.

```css
table { border-collapse: collapse; width: 100%; font: var(--bx-font-ui); font-variant-numeric: tabular-nums; }
th { position: sticky; top: 0; background: var(--bx-panel); text-align: left; font-weight: 600;
  border-bottom: 2px solid var(--bx-text); }
td { height: var(--bx-row); padding: 0 8px 0 0; border-top: 1px solid var(--bx-border); }
td.mono { font-family: var(--bx-mono); font-size: var(--bx-mono-size); }
```

**Badges** are square, 20px tall, caps, with a 1px border; a status badge
carries its glyph.

```html
<span class="badge warn"><bx-icon name="warning"></bx-icon> Degraded</span>
```

```css
.badge { display: inline-flex; align-items: center; gap: 4px; box-sizing: border-box; height: 20px; padding: 0 6px;
  border: 1px solid currentColor; border-radius: var(--bx-radius);
  font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase; }
.badge.warn { color: var(--bx-warn); background: var(--bx-warn-bg); }
```

**Alerts** inside a tile: the status tint behind the glyph, the word and
the sentence, in the text colour.

```html
<div class="alert error" role="alert"><bx-icon name="error"></bx-icon>
  <span><b>Sync failed.</b> The last good copy from 14:02 is still served. Check the token in the vault.</span></div>
```

```css
.alert { display: flex; gap: 8px; align-items: flex-start; padding: 8px 12px; border-radius: var(--bx-radius); }
.alert.error { background: var(--bx-danger-bg); }
.alert.error bx-icon { color: var(--bx-danger); }
```

**Dialogs: show the plan, then ask.** Before any consequential action
(deleting, restoring, granting, restarting, sending), say what will
change, then offer the action as its verb. Ask the shell for the dialog,
so it floats over the workspace and names your tile:

```js
const r = await xbin.dialog({
  title: 'Restore backup',
  message: 'This replaces 3 files in apps/invoices and restarts its backend:\n'
    + '  data/invoices.db\n  data/customers.db\n  config.json\n'
    + 'Its page reloads for everyone who has it open.',
  buttons: [{ label: 'Cancel', value: null }, { label: 'Restore backup', value: 'restore', primary: true }],
});
if (r.button === 'restore') await restore();
```

**Status and toasts** belong to the shell: report a lasting condition with
`xbin.status(level, msg)` and a one-off event with `xbin.notify(level,
msg)` ([elements.md](/docs/elements.md)). The shell draws the glyph, the
word and the colour; don't build a toast stack of your own.

**Plates** summarise one thing (a grant request, an app's identity, a
backup's details), like an enamel sign: a header bar in the text colour
with the plate's name in caps (it inverts with the theme), then label and
value rows divided by hairlines, then the actions. The shell's grant
requests are drawn this way.

```html
<div class="plate">
  <div class="ph">Backup</div>
  <div class="pr"><span class="k">Sealed</span><span class="mono">2026-10-03 14:02</span></div>
  <div class="pr"><span class="k">Size</span><span>3.2 GB</span></div>
</div>
```

```css
.plate { border: 1px solid var(--bx-border-strong); border-radius: var(--bx-radius); background: var(--bx-panel); overflow: hidden; }
.plate .ph { padding: 4px 12px; background: var(--bx-text); color: var(--bx-panel);
  font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase; }
.plate .pr { display: grid; grid-template-columns: 88px minmax(0, 1fr); gap: 12px; padding: 4px 12px; border-top: 1px solid var(--bx-border); }
.plate .ph + .pr { border-top: 0; }
.plate .k { color: var(--bx-muted); font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase; }
.mono { font-family: var(--bx-mono); font-size: var(--bx-mono-size); }
```

**Empty states** say what is there and what to do next, in one line, with
the one action as a button: "No invoices yet. Import a CSV or create one."
A heading in `--bx-font-heading` is enough; no illustration, no field.

## Words

The workspace's voice is plain and exact. Write a tile's labels, errors
and notices the same way.

- **Buttons are verbs, titles are nouns.** "Approve", "Open terminal";
  "Grants", "Audit log".
- **Errors say what happened, what is still safe and what to do next.**
  "Blocked: invoices tried to reach api.example.com. Outbound network is
  closed until a destination is granted."
- **Agents are software.** They run, build, change and ask for grants:
  "The agent changed 2 files", "Claude Code is waiting for a grant". Never
  a teammate or a colleague; no faces, no "I'm on it!".
- **Short declaratives, sentence case, no exclamation marks.** No hype:
  not "seamless", "magic", "instant" or "blazing fast".
- **Numbers carry units and a space** ("214 ms", "3.2 GB"), measured by
  something you can point to. Say "about" or "under" when it is a bound.
- **Name the real thing** in mono: `apps/invoices`, `api.example.com:443`,
  `bx logs invoices`.

## Both themes

**Concrete Day** is white panels on a light concrete canvas with ink text
and a cobalt accent. **Concrete Night** is dark concrete panels on an ink
canvas with near-white text and a periwinkle accent. Night is not Day
inverted: surfaces get lighter as they come forward (canvas, then panel,
then inset), and shadows get deeper. Both follow the person's choice in
Settings → Theme, or their system's when they chose System.

What keeps a tile right in both:

- every colour is a token, including inside a shadow root (the
  document's rules don't reach it: [frontend-kit.md](/docs/frontend-kit.md)
  §Theme lists what a shadow root needs);
- text sits on the surface tokens it was made for (`--bx-text` and
  `--bx-muted` on `--bx-panel` and `--bx-panel-2`; `--bx-accent-ink` on the
  accent), so its contrast holds;
- code that paints (a canvas, a chart, an SVG built in JS) reads
  `token(name, el)` and paints again in `onAppearance()`
  (`/vendor/bx-theme.js`); chart series take the terminal's colours in the
  order blue, magenta, cyan, green, yellow, red (`--bx-term-blue` …);
- an image with a background of its own sits on a panel, framed by a
  border, never straight on the canvas.

## Do and don't

```css
/* do: a card is a panel with an edge */
.card { background: var(--bx-panel); border: 1px solid var(--bx-border); border-radius: var(--bx-radius); padding: var(--bx-pad); }

/* don't: a literal white, a big radius, a glow, a gradient */
.card { background: #fff; border-radius: 12px; box-shadow: 0 8px 30px rgba(0, 0, 0, .3);
  background-image: linear-gradient(135deg, #6a5cff, #ff5ca8); }
```

```html
<!-- do: a glyph, a word, a colour -->
<span class="st warn"><bx-icon name="warning"></bx-icon> 2 jobs waiting</span>

<!-- don't: colour alone, an emoji, a pill -->
<span style="color: orange; border-radius: 99px">● ⚠️</span>
```

```css
.st { display: inline-flex; align-items: center; gap: 4px; }
.st.warn { color: var(--bx-warn); }
```

```html
<!-- do: one primary action, named by its verb; a destructive one in the danger outline -->
<button class="primary">Import 3 invoices</button>
<button class="danger">Delete invoice</button>

<!-- don't: a vague label, an exclamation, an emoji, several primaries -->
<button class="primary">Go! 🚀</button> <button class="primary">OK</button>
```

Don't:

- set a colour, a font family or a radius by hand, or tune a style for
  one theme only;
- use yellow, green or magenta as a background, a button or a link, or the
  accent for anything but actions, selection and links;
- show status by colour alone, or draw a part tab;
- use emoji, sparkles or robot faces as icons, or give an agent a face;
- animate for decoration, loop anything, or show a spinner where a
  "working 0:42" label would do;
- show a number nobody measured.

Look at the tile in both themes, at a narrow width and at a wide one.
When in doubt, use concrete and ink.
