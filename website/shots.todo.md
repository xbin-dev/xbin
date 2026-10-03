# Shots to take: the product stills and the film

Every product image xbin.dev needs from the product UI is shot on the film set after the
product's re-theme (W9: Concrete Day and Concrete Night, D184, `plans/brand.md` §14 and
§15). Until a shot lands, its section ships with the interim given below where one
exists (today only S-9's), or without the figure, as the film's sections do; nothing in
this list ships as a mock-up or as an empty frame (`make website` refuses one). The four
brand-terminal shots do not wait for the theme and are captured (below). The spec is
`site/visuals.md` in the brand pass (§1 rules, §6 the film, §7 the screenshots). These
rows sum it up, and the spec wins where they differ.

The photographs (the glass hall) are done: `website/art/` holds the masters and
`website/img/` the web sizes (`website/README.md` → "Assets").

## Rules for every shot

- **Real product, real state.** Capture the running product. No mock-ups, no edited
  pixels, no compositing inside one capture. S-7 sets two captures side by side, by
  design. No data that implies a feature xbin does not have.
- **The demo workspace** is `harbor`. Its people are fictional: **Mira Okafor** (admin),
  **Jonas Berg** and **Lena Ruiz**. Its apps are `handbook` (a team handbook), `orders`
  and `notes` (a small notes app), plus the shell, the admin console and the app manager
  under their real folder names. Any grant example goes to `api.example.com:443`.
- **Nothing real, nothing about hosting.** No real people, companies, brands or logos.
  No hostnames, server names, paths or hosting details anywhere in a frame. No sandbox
  technology names in anything a story page shows.
- **Sizes.** Desktop: a viewport of 1600 × 1000 CSS px at device pixel ratio 2, so
  3200 × 2000 px. Phone: 1179 × 2556 px, with a clean status bar (9:41, full signal and
  battery).
- **Themes.** Shoot every shot twice with identical content: Concrete Day for the light
  page, then Concrete Night for the dark page. Shoot Day first, since the promo film
  works in the light workspace. The pages swap the two with `<picture>` and
  `media="(prefers-color-scheme: dark)"`.
- **The brand terminal** (S-4, S-5, S-11, S-12) is a standalone terminal emulator set
  up as follows. Type: JetBrains Mono 400 and 700 at 13 px on a 20 px line, ligatures
  off. Padding: 8 px by 12 px. Colours: the product terminal's (`plans/brand.md` §15).
  Window: 1600 × 1000 CSS px at 2× in the spec, cropped to the content area with no
  window chrome; captured at 800 × 500 CSS px (97 × 24 cells) at 4× instead, the same
  3200 × 2000 master, because at 1600 the 13 px type shows at about 6 px beside the
  copy and at 800 at about 12 px (an open question for the owner). The prompt is `$ `
  and the output stays unedited. `hack/website-terminal-record.py` records a real
  session byte for byte and `hack/website-terminal.mjs` replays it into xterm.js set
  up this way (`website/README.md` → "Assets").
- **Mats** (the page draws them). A solid mat in the part's colour, 10 px wide on screens
  over 900 px and 8 px below. The outer corner radius is 2 px; the screenshot inside has
  square corners. A tab sits on the mat's top-left edge: Instrument Sans 600 at 13 px,
  white on cobalt and magenta, ink on green and yellow. The mat colours are cobalt
  `#1F3DFF` (light) or `#3350FF` (dark), green `#00A86B`, magenta `#DB0072` and yellow
  `#FFD000`. No device frames, browser chrome, tilt, perspective, reflections or glow.

## Files

- **Masters:** `website/art/shots/<ID>-light.webp` and `<ID>-dark.webp`, WebP at
  quality 90 and capture size. A phone set adds the screen's letter, as in
  `S-10a-light.webp`. Masters are not served. Each must stay under 1 MiB for git
  (`make large-files`); a UI capture at quality 90 usually is. A terminal shot keeps
  its recording beside them: `<ID>.session` and `<ID>.session.json` (the steps typed,
  the machine, the shell).
- **Web sizes:** `website/img/shots/<ID>-<light|dark>-<width>.{avif,webp}`, at 1× and 2×
  of the widths the page gives the shot, written by `make website-images` from the
  masters (only the beside-copy sizes so far; add the others to
  `hack/website-images.py` with the first shot that needs them). Use exact fractions
  of the capture so the type stays crisp:
  - a desktop shot beside copy (the 7-column side of a 5 + 7 split shows at most about
    740 CSS px inside its mat): 800 and 1600 wide;
  - a desktop shot set across the full content width (up to about 1320 CSS px): 1600
    and 3200;
  - S-7: 803 and 1605 (an eighth and a quarter of 6420);
  - a phone screen (about 200 to 300 CSS px wide): 295 and 590.
- **Lazy loading:** every shot below the first screen gets `loading="lazy"`. The
  exception is S-10 on the iOS page's hero, which is in the first screen. There the
  three screens' largest candidates must total 244 KB or less (the 250 KB budget less the
  header and footer marks), so each 590-wide file has to stay at about 80 KB or less.
  The phone row's rules left `site.css` with the placeholders; bring them back from
  `git show cd41d496:website/css/site.css` (`.phones`) with the captures.
- **The film** is the only media over 1 MiB: `website/media/film/` (gitignored), each
  file pinned in `website/media.lock` with its sha256 and source.

## Summary

| ID | Shows | Pages and sections | Mat and tab | Signed in as | Capture | Status today |
|---|---|---|---|---|---|---|
| F-1 | Save to live, 12 s silent loop | Home `#what`, Product `#underneath` | cobalt, "browser shell" | not named (any member) | 1600 × 1000 px, 60 fps | Placeholder posters only; the sections ship without the film |
| S-2 | Workspace, working canvas | Product `#workspace` | cobalt, "browser shell" | not named; Mira Okafor suggested | 3200 × 2000 | Waits for the theme; the section ships without it (no film-set still shows this content) |
| S-4 | bx help | Product `#bx` | green, "bx" | none (brand terminal) | 3200 × 2000 | Captured 2026-10-03 (the first screenful, scrolled back to the command) |
| S-5 | Grants file diff | Product `#grants`, Security `#grants` | green, "terminal" | none (brand terminal) | 3200 × 2000 | Captured 2026-10-03 (`git diff xbin.json`: approved grants and bindings live in the workspace manifest) |
| S-6 | Agent session | Product `#agents` | magenta, "agents" | Mira Okafor | 3200 × 2000 | Waits for the theme; the section ships without it |
| S-7 | One small app, two people | Product `#per-person` | cobalt, "browser shell" | Jonas Berg (left), Lena Ruiz (right) | 2 × 3200 × 2000, composed 6420 × 2000 | Waits for the theme; the section ships without it |
| S-9 | Admin console, view-as-user | Security `#oversight` | yellow, "admin console" | Mira Okafor, viewing as Lena Ruiz | 3200 × 2000 | Interim shipping: the film set's still in the current product theme |
| S-10a, b, c | iOS app, three screens | Product `#ios`, iOS hero `#top` | magenta, "iOS app" on each | not named; Mira Okafor suggested | 1179 × 2556 each | Waits for the beta build's capture; both sections ship without it |
| S-11 | Installer plan | Install `#first` | green, "installer" | none (brand terminal, fresh Linux machine) | 3200 × 2000 | Captured 2026-10-03 on a fresh Ubuntu 24.04 VM (its end: the user-mode plan and the question) |
| S-12 | Apps as folders | Product `#apps` | green, "terminal" | none (brand terminal) | 3200 × 2000 | Captured 2026-10-03 (`ls`, `ls apps tiles`, `tree apps/orders`: see below) |

The brand-terminal shots were the spec's "NOW" shots, since the brand terminal does not
wait for the product theme; their recordings are in `website/art/shots/`. A terminal shot
is taken again only when its output changes (a new `bx --help`, a new installer plan).

## Each shot

### F-1 · Save to live, 12 s silent loop

- **Frame:** the workspace in the browser, with the sidebar collapsed to its rail and two
  windows tiled. On the left, at 60 % of the width, the `handbook` app shows its front
  page, with the headline "Team handbook" in large type. On the right, at 40 %, the
  `handbook` app's terminal (green part tab) has the page open in a terminal editor, the
  cursor on the headline line.
- **Action** (in real time, on the measured workstation):
  - 0 to 1.5 s: still.
  - 1.5 to 3 s: the headline is changed to "Team handbook, autumn edition".
  - 3 s: saved with the editor's write command. By 3.35 s the window shows the new
    headline and the live state; if it takes more than 21 frames, discard the take.
  - 6.5 to 8 s: changed back to "Team handbook", saved at 8 s, shown by 8.35 s (the
    same 21-frame check).
  - Until 12 s: still, with the cursor back where it began, so frame 720 matches frame 1.
  - No speed changes, cuts, zooms, cursor effects, burned-in text, music or clocks.
- **Format:** exactly 12.00 s at 60 fps (720 frames), 1600 × 1000 px, no audio track.
  MP4 (H.264 High, under 3 MB) and WebM (VP9, under 2.5 MB), in Day and in Night.
- **Files:** `website/media/film/F-1-day.mp4`, `F-1-day.webm`, `F-1-night.mp4` and
  `F-1-night.webm`. The poster frames, taken at 5.00 s, go to
  `website/img/film/F-1-day-poster.webp` and `F-1-night-poster.webp`. They replace the
  placeholders there now: flat frames in the shell background, made by
  `make website-images`, which must never ship.
- **Accessible name:** Film, 12 seconds, silent: in the xbin workspace, the headline of
  the handbook app's page is changed and saved, and the app's window shows the new
  headline. Then it is changed back and saved again.

### S-2 · Workspace, working canvas

- **Screen:** the sidebar expanded, with its Apps, Agents and Terminals sections showing.
  Three windows are tiled: `orders` on the left, `handbook` at the top right, and the
  `orders` app's terminal (green part tab) at the bottom right, focused. In the terminal,
  `git status` is being typed at the prompt over a 300 ms round-trip link, so its last two
  characters show underlined: predicted, not yet confirmed. The status bar shows.
- **Alt:** The xbin workspace with two apps and an app terminal side by side; the last
  characters typed in the terminal are underlined until they are confirmed.

### S-4 · bx help

- **Screen:** the brand terminal with `bx --help` (or bx's real help command, if it
  differs) and the first screenful of its output, unedited.
- **Captured** (2026-10-03, `website/art/shots/S-4.*`): `bx --help` from this tree
  (v0.3.67) on the measured workstation; the help runs to about 125 lines, so the
  terminal is scrolled back to the command and shows its first 24.
- **Alt:** The bx command-line tool's help output in a terminal.

### S-5 · Grants file diff

- **Screen:** the brand terminal with a diff of the workspace's real grants file, before
  and after approving one network destination, `api.example.com:443`, for the `orders`
  app. Use the file in its real format: `git diff` if it is under git, `diff -u` of the
  two versions if not. Added lines show in the terminal's green, removed lines (if any)
  in red, and context in the default foreground. Nothing is hand-edited, and no hostname
  or path shows where the workspace runs.
- **Captured** (2026-10-03, `website/art/shots/S-5.*`): approved grants and network
  approvals (bindings, since egress became one) live in the workspace's `xbin.json`,
  which is under git, so the shot is `git diff xbin.json` in `harbor` after the owner
  ran `bx bind apps/orders net=internet:api.example.com:443`: one closing bracket
  changed and the five lines of the binding added.
- **Alt:** A terminal showing a diff of the xbin grants file after one network
  destination, api.example.com on port 443, was approved for the orders app.

### S-6 · Agent session

- **Screen:** the workspace with the agent chat open on the right (magenta part rule),
  signed in as Mira Okafor, in a real, unedited session of the built-in agent on
  `orders`. Mira's prompt is "Add a CSV export to the orders list." The reply shows its
  steps collapsed, each with its result, and a summary of the files it changed, linked to
  the app. Beside the chat, the `orders` window shows the new export button.
- **Rules:** agents appear as software that builds: no avatars, faces or personable
  names, and never an agent doing a person's job.
- **Alt:** The xbin workspace with the agent chat open: the built-in agent has added a
  CSV export to the orders app and lists the steps it took and the files it changed.

### S-7 · One small app, two people

- **Screen:** two captures side by side in one cobalt mat, split by a 10 px cobalt
  gutter (20 px at 2×), composed into one 6420 × 2000 px image. On the left, signed in as
  Jonas Berg, the `notes` app shows his three notes and his name. On the right, signed in
  as Lena Ruiz, the same app shows her two different notes and her name. Use two
  separate browser profiles, each a real session against the same workspace.
- **Never in this frame:** the agent, the agent template, the partitions page or the
  admin console. The frame sits next to the per-person figure, which was measured on this
  kind of small app.
- **Alt:** The same small notes app open for two people side by side; each sees only
  their own notes.

### S-9 · Admin console, view-as-user

- **Screen:** Mira Okafor in view-as-user mode, viewing as Lena Ruiz: the workspace
  exactly as Lena sees it, with the product's view-as-user banner and its read-only state
  showing.
- **Alt:** An admin viewing the workspace as one person, read-only, with the
  view-as-user banner across the top.

### S-10a, S-10b, S-10c · iOS app, three screens

- **Screens:** three phone captures, each in its own magenta mat.
  - S-10a: the `handbook` app in a native app view.
  - S-10b: the `orders` app's terminal.
  - S-10c: an agent session following the built-in agent's work on `orders`.
- **Rules:** use the current beta build until the redesigned theme ships. No device
  frame, and no App Store badge or reference anywhere on screen.
- **Placement:** on the Product page, a row (a horizontal scroll row on phones). On the
  iOS page's hero, beside the copy, staggered in height by 24 px steps. That one is in
  the first screen (see Files).
- **Alt:** S-10a: The xbin iOS app showing the handbook app in a native view. S-10b: The
  xbin iOS app showing the orders app's terminal. S-10c: The xbin iOS app following an
  agent session.

### S-11 · Installer plan

- **Screen:** the brand terminal on a fresh Linux machine, with exactly
  `curl -fsSL https://xbin.dev/install.sh | sh` at the prompt. Then the installer's real
  output, captured once it has printed its plan and is waiting for an answer. Do not
  answer. If the output is taller than the frame, show its end: the last lines of the plan
  and the question. Unedited; the prompt shows no hostname.
- **Captured** (2026-10-03, `website/art/shots/S-11.*`): a fresh Ubuntu 24.04.5 LTS
  cloud VM, its user `mira`, the bootstrap resolving v0.3.67. As a user without a mode
  flag, the installer prints both plans and asks "Install [s]ystem-wide via sudo,
  [u]ser-only, or [q]uit?"; the frame shows the end: the system plan's last line, the
  user plan and the question.
- **Alt:** A terminal: the xbin installer has printed its plan and is asking before it
  changes anything.

### S-12 · Apps as folders

- **Screen:** the brand terminal at the folder that holds the workspace's apps, running
  `tree -L 2 -I .git` (or `ls -R` if `tree` is missing). The listing shows `handbook`,
  `orders` and `notes`, plus the shell, the admin console and the app manager under their
  real folder names. The `orders` folder is expanded to show its page and its `backend`
  folder. Unedited; no prompt or path reveals a hostname or where the workspace runs.
- **Captured** (2026-10-03, `website/art/shots/S-12.*`) with three commands in place of
  the one: in a workspace the apps are in `apps/`, the shell in `shell/` and the admin
  console and the app manager in `tiles/admin` and `tiles/manager`, and `tree -L 2`
  at the root lists the shell's 18 files. So: `ls` at the workspace's root, `ls apps
  tiles`, and `tree apps/orders` (its page, `index.html`, and `backend/`): 18 lines.
- **Alt:** A terminal listing the workspace's apps as folders, including the shell, the
  admin console and the app manager.
