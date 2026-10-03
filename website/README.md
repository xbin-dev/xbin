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
| `index.html` | Announce, then Inform, then Work | The story: the hero (kicker, the stair, the lead, two buttons; the glass hall beside it, a strip of it on phones), `#curve` (software made per year, 1950s to today), `#breaks`, `#idea` (the page's one field band, cobalt), `#what`, `#measured`, `#install` (both commands, the trial first, `#try` on its block), `#start` (the closing call). `id="top"` is the hero. |
| `product.html`, `security.html`, `install.html`, `ios.html` | Inform | Stubs: the deck's crumb, H1 and lead in the Inform hero, inside `<main data-todo="page">`. The page builders set the rest of each deck. `install.html` must carry `#trial`: every other page's "Try it free" links there. |
| `privacy.html` | Inform | The privacy policy, restyled; its title, description and words are unchanged and pinned (see Checks). |
| `404.html` | Announce, small | The three-line stair and plain links; root-relative URLs so it works at any depth; `noindex`. |
| `og.html` | — | Not a page: the share card's artwork, rendered to `og.png` (below). |

Each page loads `css/tokens.css` and `css/site.css`; `index.html` also loads
`js/copy.js`. The words come from the brand's copy decks, set verbatim; builders write
no copy of their own. A gap in a deck becomes an HTML comment `TODO-COPY: …`; a
history figure the research pass has not supplied yet stays a visible
`<mark data-todo="data">{{DATA: …}}</mark>`, and no chart is drawn without its
numbers. The home page does not ship with a slot left in it.

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
  screen `loading="lazy"`, and a light and a dark source in a `<picture>`;
- plates: `.plate` with `.plate-h` and a `dl`;
- commands: `.cmd` blocks in a `.cmds` grid on the band (`code.cmd-code` sized to stay
  on one line, the `$ ` prompt drawn by CSS and never copied, `button.copy
  data-copy="…"`), or `.code` with a `pre` elsewhere;
- Inform pages: `.ihero` (crumb, h1, lead); the privacy text: `.prose`; the 404:
  `.nf`, `.link-list`.

Corners are 2 px wherever one shows; focus is the cyan ring (3 px, 2 px gap) from
`:focus-visible`; motion plays once and is off under `prefers-reduced-motion`.

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
- **Marks:** `img/mark.svg`, `img/wordmark.svg` (wordmark A; swapping in B is this one
  file) and `favicon.svg` are copies of `plans/brand/marks/`; `apple-touch-icon.png` is
  `icon-1024.svg` at 180 px (`rsvg-convert`, then `oxipng`).
- **The glass hall** (`art/`): the generated masters with their prompts and reports
  (`hall-day.jpg` 2736 × 1536, `hall-night.jpg` 1360 × 768, the `.json` beside each) and
  the crops the hero loads: the square the hero's portrait column can show
  (`hall-day-1536.webp`, `hall-day-1024.webp`, `hall-night-768.webp`, cut at 70 % across
  so `object-position: 70% 62%` keeps the art direction's framing) and the full frame for
  the phone strip (`*-strip.webp`), WebP from the masters with PIL. The night image's
  sheets do not double; it ships until its replacement is generated (same names).
- **Media over 1 MiB** (the 12 s film, later) stays out of git: it lives in
  `website/media/` (gitignored) and `media.lock` pins each file's sha256 and source.

### og.png

`og.html` is the share card's artwork (1200 × 630, light only). After editing it,
re-render it: `make website-og` (Playwright from `PLAYWRIGHT_DIR`, which
`hack/dev-setup.sh` writes into `.dev.mk`, over `file://`; then pngquant and oxipng
when installed).

## Build

```
make website        # website/dist: every page, css/, fonts/, img/, art/*.webp, js/,
                    # app/, install.sh, og.png, favicon.svg, apple-touch-icon.png,
                    # media/ as media.lock pins it, static/helpers/
```

It runs the site's check with `--dist` first. `dist/` is the deployable artifact (any
static host, GitHub Pages, an object store). It refuses to build when
`hack/helpers.sha256` lists prebuilt helpers but `website/static-helpers/` is missing:
the site serves them at `https://xbin.dev/static/helpers/…` (`make helpers`;
docs/maintenance.md → "Prebuilt helpers"; `hack/helpers-static.sh` stages them into the
gitignored `website/static-helpers/`, binaries never tracked), and a deploy without
them would send everyone's `make helpers` back to building from source.

## Checks

`make website-check` (`hack/check-website.sh`, part of `make guards`; its tests break
each rule on a copy of the site):

- **Preserved files:** `install.sh` byte-identical to the bootstrap master had; the
  privacy page's title, description and words unchanged (normalized text, pinned by
  digest: a deliberate change updates `PRIVACY_TEXT_SHA256` in
  `hack/check-website.mjs`, `--privacy-digest` prints it); `app/ios.json` parses as the
  kill switch.
- **No third-party loads:** no `src`, `srcset`, stylesheet, icon or preload link,
  `url()`, `@import` or JS import points at another site; external links only to the
  allowed URLs.
- **Nothing stored:** no `document.cookie`, `localStorage`, `sessionStorage` or
  `indexedDB` in any script.
- **Budgets, per page** (KB of 1,000 bytes, uncompressed): HTML and its CSS ≤ 60, JS ≤ 80,
  fonts ≤ 200 (every face the CSS declares), first-screen images ≤ 250 (every image
  without `loading="lazy"` counts; a `<picture>` by its largest candidate).
- **One header, one footer:** the shared blocks identical on every page but for
  `aria-current` and the Try link's target, and those two right.
- **Images and colours:** every `<img>` has `alt`, `width` and `height`; no hex colour
  outside `tokens.css`, in a `style` attribute or an inline SVG `fill`.
- **The mark:** `img/mark.svg` and `favicon.svg` are the masters in
  `plans/brand/marks/`, byte for byte, and `img/wordmark.svg` is wordmark A or B.
- **Media:** `media.lock` well formed; with `--dist`, every locked file present with
  its sha256.

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
`/art/`, `/js/copy.js`. No longer served: the old site's `/js/` islands, `/vendor/`,
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
