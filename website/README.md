# website — xbin.dev

The **xbin.dev** site, in the Base Two brand (D183; the system is `plans/brand.md`).
Static files and nothing else: plain HTML and CSS, one small ES module, no build
step, no framework, no CDN. The site sets no cookies, stores nothing (no
localStorage, sessionStorage or IndexedDB), runs no analytics and loads nothing from
other sites; every page works without JavaScript. Light or dark follows the visitor's
system setting (`prefers-color-scheme`): there is no toggle.

Preview it as it will be served (the pages use root-relative URLs):

```
python3 -m http.server 9421 --bind 127.0.0.1 --directory website
```

## Pages

| Page | Volume | What it is |
|---|---|---|
| `index.html` | Announce, then Inform, then Work | The story: the hero (kicker, the stair, the lead, two buttons; the glass hall beside it, a strip of it on phones), `#curve` (the measured series: new repositories created on GitHub per year, 2013 to 2025, and August 2026's annualised rate; its heading and lines were written for a seventy-year chart and wait for the owner's call, D183), `#breaks`, `#idea` (the page's one field band, cobalt), `#what`, `#measured`, `#install` (both commands, the trial first, `#try` on its block), `#start` (the closing call). `id="top"` is the hero. |
| `product.html` | Inform | How xbin works: the hero with the "At a glance" plate beside the h1 and jump links under it, eight parts (`#underneath`, `#workspace`, `#apps`, `#agents`, `#per-person`, `#grants`, `#bx`, `#ios`): the copy and its visual on alternating sides where the visual exists (S-12, S-5, S-4, and the measured figures under `#underneath`), the heading beside the copy where it waits for the product theme (S-2, S-6, S-7, S-10), then `#it`, the measurement plate `#measured`, and the band `#get` (buttons only, the trial first). |
| `install.html` | Inform | The install guide: the hero, the install command on the band (`#command`), the installer's four steps and S-11 (`#first`), `#requirements` (a plate), the trial command on the band (`#trial`: every other page's "Try it free" links there), `#upgrade`. Strictly paper or ink: in dark its two bands are concrete, not cobalt. |
| `security.html` | Inform, strictly | For IT (`site/security.md`): the hero beside the facts plate (`#facts`), then `#default-deny`, `#sandboxes`, `#grants` (S-5), `#identity`, `#oversight` (S-9, an interim still), `#per-person`, `#vault`, `#evidence`, `#next`. No field, stair or photograph. |
| `ios.html` | Inform | The iOS app in beta testing (`site/ios.md`): the hero (the copy and the status plate; the three phone screens, S-10, stand beside it once the beta build is captured), `#features` under magenta part rules, `#next`. No store badge, link or date. |
| `privacy.html` | Inform | The privacy policy, restyled; its title, description and words are unchanged and pinned (see Checks). |
| `404.html` | Announce, small | The three-line stair and plain links; root-relative URLs so it works at any depth; `noindex`. |
| `og.html` | — | Not a page: the share card's artwork, rendered to `og.png` (below). |

Each page loads `css/tokens.css` and `css/site.css`; `index.html` and `install.html`
also load `js/copy.js`. The words come from the brand's copy decks, set verbatim; builders write
no copy of their own. A gap in a deck becomes an HTML comment `TODO-COPY: …`; while a
page is built, a history figure the research pass has not supplied yet stays a visible
`<mark data-todo="data">{{DATA: …}}</mark>`, and no chart is drawn without its
numbers. No page ships with a slot left in it: `make website` refuses one (the guard
with `--dist`), so a figure the register does not give is taken out with its sentence.

### The shared header and footer

Every page carries the same header and footer, written into each page by hand between
`<!-- shared header … -->` / `<!-- /shared header -->` and `<!-- shared footer … -->` /
`<!-- /shared footer -->`. Change them in every page at once. Only two things differ
per page: `aria-current="page"` on the page's own link (in both the desktop list and
the phone menu), and the "Try it free" link (`data-try`), which goes to `#try` on the
home page and `/install.html#trial` everywhere else. The era stripe above the header
is 8 px on the home page (`body.page-home`) and 6 px elsewhere. Under 900 px the links
fold into a `<details>` menu, with no script.

### Styles

`css/tokens.css` is every colour, font and size the site uses, light and dark; nothing
else holds a hex value. `css/site.css` is the components, by the brand's names:

- layout: `.wrap` (margins that hold content to 1440 px), `.sec`, `.sec-rule` (a 3 px
  rule on top), `.split` (5 + 7 columns), `.grid`;
- type: `.label`, `.label-mono`, `.h1`…`.h4`, `.lead`, `.body`, `.small`, `.caption`,
  `.crumb`, `.more`, `.ext` (an external link's ↗ drawn by CSS);
- the stair: `h1.stair` with `span.st1`…`.st4` (yellow, green, magenta, cobalt);
- fields and bands: `.field.field-cobalt`, `.band` (ink in light, cobalt in dark);
- proof: `.figs` / `.fig` (`.fig-label`, `.fig-num` with `.fig-q`, `.fig-claim`),
  `.measure-note`, `.chart`;
- product shots: `figure.shot` around `.mat.mat-shell|-terminal|-agents|-admin` with
  `span.mat-tab`, the image, and a `figcaption`; give every image below the first
  screen `loading="lazy"`, and a light and a dark source in a `<picture>`; a shot that
  waits for its capture is `.ph` (`.ph-phone` for a phone screen) in its mat (below,
  "Assets");
- plates: `.plate` with `.plate-h` and a `dl`;
- commands: `.cmd` blocks in a `.cmds` grid on the band (`code.cmd-code` sized to stay
  on one line, the `$ ` prompt drawn by CSS and never copied, `button.copy
  data-copy="…"`), or `.code` with a `pre` elsewhere;
- Inform pages: `.ihero` (crumb, h1, lead); the privacy text: `.prose`; the 404:
  `.nf`, `.link-list`;
- security and iOS (their own section, before the footer's, scoped to
  `body.page-security` and `body.page-ios`): `.ih-grid` (the security hero's copy
  beside the facts plate), `.four`, `.leadins`, `.card`, `.evidence`;
- product and install (their own section, at the end, scoped to `.page-product` and
  `.page-install`): the hero grid, `.flow` copy columns, `.flip` for a visual on the
  left, `.it`, `.steps`.

Every rule counts against every page's 60 KB, so keep a page's own rules few. The
phone screens' row (S-10 on the iOS hero and under the product page's `#ios`) left
with its placeholders; when the captures land, bring its rules back from git history
(`git show cd41d496:website/css/site.css`, `.phones`). A row that scrolls sideways
edge to edge, as it does on phones, draws its focus ring inside
(`outline-offset: -3px`), where the viewport cannot cut it off.

Corners are 2 px wherever one shows; focus is the cyan ring (3 px, 2 px gap) from
`:focus-visible`, and the root's `scroll-padding-block` (16 px) keeps a focused
element's ring clear of the viewport's top and bottom edges; motion plays once and is
off under `prefers-reduced-motion`.

### The one script

`js/copy.js` makes the Copy buttons copy, read "Copied" for 1.6 s and say so in the
page's polite live region (`<p class="sr" id="copied" aria-live="polite">`). Without
JavaScript the buttons are hidden (a `<noscript>` style in the head) and the command
is plain text that one click selects whole. Any page with a Copy button loads the
module and carries the live region.

## Copy rules

The decks are the copy; `plans/brand.md` §3 is the voice. In short: announce, then
explain in a plain sentence; no comparisons with other products, no "not X, but Y",
no em dashes, no hype words; measured figures only from `hack/demo/measurements.md`,
each sentence naming its machine and each page that quotes figures carrying the
hardware note once; agents are software that builds and changes apps, never
colleagues; **no page says where xbin runs or who runs it** (no deployment mode);
**no sandbox technology names** on story pages (home, product, iOS), and the security
page never names it either; the two calls to action are exactly `ssh xbin@vcpu.sh`
(first) and `curl -fsSL https://xbin.dev/install.sh | sh`; external links go only to
`https://github.com/xbin-dev/xbin` (source, docs, issues) and `https://vcpu.sh/xbin`,
open in the same tab and carry ↗.

## Assets

- **Fonts** (`fonts/`, each family's OFL licence beside it): Bricolage Grotesque 600
  and 800, Instrument Sans 400 and 600, JetBrains Mono 400, 500 and 700, as Google
  Fonts' latin subsets; plus `instrument-sans-{400,600}-sym.woff2` (the arrows,
  U+2190–2199, for ↗) and `bricolage-grotesque-800-sym.woff2` (→ ≤ ≥), instanced from
  the upstream variable fonts at the same coordinates with fontTools and loaded by
  `unicode-range`.
- **Marks:** `img/mark.svg`, `img/wordmark.svg` (wordmark A) and `favicon.svg` are
  copies of `plans/brand/marks/`; `apple-touch-icon.png` is `icon-1024.svg` at 180 px
  (`rsvg-convert`, then `oxipng`). Swapping in wordmark B is more than the file: B is
  wider (852 × 244, 3.49:1, against A's 2.54:1), so its `<img>` sizes change with it, on
  every page and in `og.html`. From `lockup-b.svg`'s unit (the word's ascender 1u under
  the tile's top, 2u from it): the header's 73 × 21 with `.lockup img+img{margin-top}`
  3.5 px (A: 51 × 20, 5 px), the footer's 105 × 30 with `.foot-lockup img+img` 5 px
  (A: 61 × 24, 11 px), `og.html`'s 154 × 44, then `make website-og`. The guard fails on
  a wordmark `<img>` whose sizes do not fit the file in use.
- **The glass hall:** the generated masters, with their prompts and reports, are in
  `art/` (`hall-day.jpg` and `hall-night.jpg`, both 2736 × 1536, with the `.json` beside
  each). `art/` itself is not served. The files the hero loads are in `img/`, written
  from the masters by `make website-images` (`hack/website-images.py`, Pillow with
  AVIF) as AVIF with a JPEG fallback, at 1× and 2×:
  - the square the hero's portrait column shows (`hall-{day,night}-880` and `-1536`), the
    master's full height cut at 70 % across so `object-position: 70% 62%` keeps the
    art direction's framing. The column is at most 880 px tall, and 1536 is the
    masters' height;
  - the full frame for the phone strip (`hall-{day,night}-strip-900` and `-1800`).

  The script refuses any file over 240 KB, so the hero's largest candidate stays inside
  the first-screen budget. The night image is the 2k replacement that `site/visuals.md`
  P-2 commissioned. Its report lists the four takes, the edit that took the ceiling out
  of the frame and the one retouch.
- **The film** (F-1, the 12 s loop) waits for the product theme. Its poster frames
  `img/film/F-1-{day,night}-poster.webp` are flat placeholders in the product theme's
  shell background, there only for building the markup. They never ship: the sections
  ship without the film until it is shot, and `make website` leaves `img/film/` out of
  `dist/` until `media.lock` pins the film. Its MP4 and WebM go in `media/` (below).
- **Product shots.** `shots.todo.md` lists each one with its persona, screen, size,
  theme, mat and file names. Each sits in `figure.shot` with `data-shot` naming the
  capture `site/visuals.md` asks for (`grep data-shot` lists them), as a `<picture>`
  with the Concrete Day source for light and the Concrete Night one for dark, below the
  first screen with `loading="lazy"`. The masters are in `art/shots/` (WebP, 3200 ×
  2000); `make website-images` writes their web sizes to `img/shots/`
  (`<ID>-<light|dark>-{800,1600}.{avif,webp}`).
  - **The brand-terminal shots** (S-4 `bx --help`, S-5 the grants file's diff, S-11 the
    installer on a fresh machine, S-12 the apps as folders) are captured: real sessions,
    recorded byte for byte by `hack/website-terminal-record.py` (each `art/shots/<ID>.session`,
    with the steps typed, the machine and the shell in its `.json`) and replayed into
    the product's own terminal emulator, xterm.js, set up as the brand terminal by
    `hack/website-terminal.mjs` (its header has the window size and why it is 800 × 500).
  - **The product UI shots** (S-2, S-6, S-7, S-10) wait for the product's re-theme
    (D184). Until one is captured, its section ships without it, as the film's do, and
    a comment marks where it goes. A still from the film set stands in only where it
    shows what the spec asks for: today that is S-9 on the security page,
    `img/shots/S-9-interim.webp` (`.film-media/stills/07-admin-desk-view-as.png`, the
    current product theme, one theme only, 1600 × 1000), until the Concrete Day and
    Night captures replace it in a `<picture>`.
  - While a page is built, a shot that waits may hold its place as a concrete frame at
    its aspect in its mat (`.ph`, `.ph-phone` for a phone screen), `aria-hidden` with
    a visible `<mark data-todo="shot">S-…</mark>` and no alt (the alt goes on the
    capture). The guard counts them, and `make website` refuses to deploy one.
- **The curve** (`#curve`, visual D-1) is drawn from `data/software-per-year.json`,
  the series the research pass's history register gives for it (new repositories
  created on GitHub per year, every point with its source; the 2026 point an
  annualised rate) and the dated inflection marker. `make website-chart`
  (`hack/website-chart.mjs`) draws the whole figure into `index.html` between the
  `<!-- chart D-1 … -->` markers: an SVG stretched to the frame with HTML labels over
  it, the numbers in a "Show the numbers" table, the figures under it and the caption,
  so nothing about the chart is written by hand. Edit the data, never the block. The
  annualised point is reached by a dotted segment with no fill and ends in a hollow
  square, and the caption says why. A projection (the dashed cobalt "If the curve
  holds" line, its sentence in the caption and its figure) is drawn only once the data
  file carries one; until then none of the three exists.
- **Media over 1 MiB** (the 12 s film, later) stays out of git: it lives in
  `website/media/` (gitignored) and `media.lock` pins each file's sha256 and source.

### og.png

`og.html` is the share card's artwork (1200 × 630, light only). After editing it,
re-render it: `make website-og` (Playwright from `PLAYWRIGHT_DIR`, which
`hack/dev-setup.sh` writes into `.dev.mk`, over `file://`; then pngquant and oxipng
when installed).

## Build

```
make website        # website/dist: every page, css/, fonts/, img/ (but img/film/
                    # until the film is locked), js/, data/, app/, install.sh,
                    # og.png, favicon.svg, apple-touch-icon.png, media/ as
                    # media.lock pins it, static/helpers/
```

It runs the site's guard with `--dist` first, which refuses a site that is not ready to
deploy (below, "Checks"). `dist/` is the deployable artifact (any static host, GitHub
Pages, an object store). Among the refusals: every prebuilt helper set
`hack/helpers.sha256` lists must be staged, each `<group>/<key>/<arch>.tar.zst` in
`website/static-helpers/` with the sha256 the manifest pins. The site serves them at
`https://xbin.dev/static/helpers/…` (`make helpers`; docs/maintenance.md → "Prebuilt
helpers"; `hack/helpers-static.sh` stages them into the gitignored
`website/static-helpers/`, binaries never tracked, or download the sets the live site
serves and check their sums), and a deploy without one would send everyone's
`make helpers` for that set back to building from source.

Deploying is by hand: `make website`, then copy `website/dist/` to the host as it is
(nothing to rewrite; serve `app/ios.json` as `application/json`). Then a smoke check
against the live site: `/`, `/install.sh` (byte-identical, `sha256sum` against
`INSTALL_SH_SHA256` in `hack/check-website.mjs`), `/app/ios.json`, `/privacy.html`,
`/og.png` and one `/static/helpers/<group>/<key>/amd64.tar.zst` answer 200, and the
browser pass run against `dist/` before the copy (`make website-check
WEBSITE_CHECK_FLAGS=--dist`) found nothing.

## Checks

Two layers. `make website-check` runs both; `make guards` (and CI) runs the first.

The guard, `make website-guard` (`hack/check-website.sh`, no browser; its tests,
`hack/check-website.test.mjs`, break each rule on a copy of the site):

- **Preserved files:** `install.sh` byte-identical to the bootstrap master had; the
  privacy page's title, description and words unchanged (normalized text, pinned by
  digest: a deliberate change updates `PRIVACY_TEXT_SHA256` in
  `hack/check-website.mjs`, `--privacy-digest` prints it); `app/ios.json` parses as the
  kill switch.
- **No third-party loads:** no `src`, `srcset`, stylesheet, icon, preload or prerender
  link, `url()`, `@import` or JS import points at another site; no script names an
  absolute or protocol-relative URL in a string (what `fetch`, `sendBeacon`, a
  WebSocket or `new Image().src` would reach); no `ping` attribute; external links
  only to the allowed URLs.
- **Nothing stored:** no `document.cookie`, `cookieStore`, `localStorage`,
  `sessionStorage`, `indexedDB`, `openDatabase`, `caches` or `serviceWorker` in any
  script.
- **Budgets, per page** (KB of 1,000 bytes, uncompressed): HTML and its CSS ≤ 60, JS ≤ 80,
  fonts ≤ 200 (every face the CSS declares), first-screen images ≤ 250 (every image
  without `loading="lazy"` counts; a `<picture>` by its largest candidate).
- **One header, one footer:** the shared blocks identical on every page but for
  `aria-current` and the Try link's target, and those two right.
- **Images and colours:** every `<img>` has `alt`, `width` and `height`; no hex colour
  outside `tokens.css`, in a `style` attribute or an inline SVG `fill`.
- **The mark:** `img/mark.svg` and `favicon.svg` are the masters in
  `plans/brand/marks/`, byte for byte; `img/wordmark.svg` is wordmark A or B, and every
  `<img>` of it is sized to that file's proportions.
- **Media:** `media.lock` well formed.

It also counts what is still open: data slots, `TODO-COPY` gaps, shots waiting for
their capture, stub pages.

With `--dist` (what `make website` runs), the guard holds the site to what may be
deployed, and fails on any of: a `{{DATA}}` slot, a shot waiting for its capture or a
stub page on any page; a file `media.lock` pins missing from `website/media/` or not
matching its sha256; a prebuilt helper set `hack/helpers.sha256` lists missing from
`website/static-helpers/` or not matching its sha256.

The browser pass, `hack/website-check.mjs` (Playwright from `PLAYWRIGHT_DIR`, as for
`make website-og`): every page at 360, 390, 768, 1024, 1440 and 1920 px wide, in
light, dark and reduced motion, and at 320 px (WCAG's reflow width, a desktop at
400 %) for overflow only, served by `python3 -m http.server 9424 --bind 127.0.0.1`,
which it starts and stops (it refuses a port someone else holds). About 30 s. A page
fails on:

- a console error or an uncaught exception;
- a request that leaves `127.0.0.1:9424` (blocked, and named);
- horizontal overflow: the page scrolls sideways, a box runs past the viewport's
  edge with nothing of the page's own to scroll or clip it, or a command does not fit
  its line (it scrolls with no scrollbar shown, so its end would be hidden); again
  with every `<details>` open;
- layout shift over 0.05 (the largest session window, as Chrome counts CLS) while
  the page loads and is scrolled to its end. The fonts are held until the first paint
  and then let in one at a time, as on a first visit, so a font swap that moves the
  page counts on every run;
- a focusable element without a visible focus ring: every element Tab reaches (and
  what each `<details>` reveals) must match `:focus-visible`, be shown and not covered,
  and draw its ring at 3:1 or more against the ground around it with at least 60 % of
  the ring inside the viewport and the boxes that clip it;
- a missing image: an `<img>` that did not load, a request that failed or answered
  4xx/5xx, or a `src`/`srcset` candidate the server does not have (the 2× files and the
  dark sources included);
- an animation that runs under `prefers-reduced-motion`.

`WEBSITE_CHECK_FLAGS` passes its flags: `--dist` checks `website/dist` as
`make website` left it, `--page NAME` one page, `--shots DIR` also writes full-page
screenshots of every page at 1440 × 900 and 390 × 844 (DPR 2), light and dark.

## Preserved URLs

| URL | Serves |
|---|---|
| `https://xbin.dev/` | `index.html` |
| `https://xbin.dev/install.sh` | the bootstrap installer, byte-identical across the redesign |
| `https://xbin.dev/app/ios.json` | the iOS app's kill switch (below) |
| `https://xbin.dev/privacy.html` | the privacy policy |
| `https://xbin.dev/og.png` | the share card (`og:image`, 1200 × 630) |
| `https://xbin.dev/static/helpers/…` | the prebuilt helpers (`website/static-helpers/`) |

New with Base Two: `/product.html`, `/security.html`, `/install.html`, `/ios.html`,
`/404.html`, `/favicon.svg`, `/apple-touch-icon.png`, `/css/`, `/fonts/`, `/img/`,
`/data/software-per-year.json`, `/js/copy.js`. No longer served: the old site's `/js/` islands, `/vendor/`,
`/shots/` and its IBM Plex fonts (`website/shots/` stays in git for the repository
README's image).

### app/ios.json: the iOS app's kill switch

`https://xbin.dev/app/ios.json` is the iOS app's remote kill switch (plans/native.md
§23): `{"nativeRuntime": {"disabled": false, "disabledBuilds": []}}` — `disabled:
true`, or a build number (`CFBundleVersion`) in `disabledBuilds`, turns native tile
views off in the app (tiles open as web pages) without an app update. The app re-reads
it every 6 hours, fails open (an unreachable or unreadable file never turns anything
off) and ignores an answer older than a week. Serve it as `application/json`, uncached
or with a short max-age.

## install.sh and releases

`install.sh` is a deliberately tiny, auditable bootstrap, and **100% static across
releases**: at run time it resolves the latest semver tag from the GitHub tags API (no
auth, no jq; `XBIN_VERSION=vX.Y.Z` pins one, resolution failure exits with the pin
instructions), fetches that tag's `deploy/install.sh`, and runs it with `XBIN_REF`
pinned to the same tag, so a piped install always builds a *released* tree, never
master. Arguments pass through (`… | sh -s -- --check-only`).

**Release flow** (each release): tag `vX.Y.Z` on master and push the tag. That's it:
the bootstrap picks it up on the next run. Only when the bootstrap or the site
*themselves* change: sync the `releases` branch (`git push origin master:releases`) so
the stable raw URL
`https://raw.githubusercontent.com/xbin-dev/xbin/releases/website/install.sh` serves the
current copy, and `make website` + redeploy `dist/`.
