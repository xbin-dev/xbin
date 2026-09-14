# website — xbin.dev

The **xbin.dev** landing page. `index.html` carries the whole pitch (no build
step, no external fonts/CDNs — same buildless ethos as the workspace), in the
product's own steel + hazard-amber palette, cut in the shell's chamfered
corner language, with IBM Plex Sans self-hosted in `fonts/` (OFL, headings
only) and one inline SVG icon set — no emoji, no glyph-soup. All interactivity
is ES-module Lit elements in `js/` (`lit` vendored in `vendor/` from
`web/vendor`, loaded via an import map — no npm, no bundler): a WebGL2 shader
backdrop (`xb-gl`, with a CSS-gradient fallback and a static frame under
`prefers-reduced-motion`), the self-booting shell mock (`xb-shell-scene`),
the copy buttons (`xb-copy`) and the screenshot lightbox (`xb-lightbox`).
Plus `install.sh`, the bootstrap
installer the site serves, and `og.png` (`og:image`, 1200×630). `shots/`
holds the real-product screenshots (pngquant-compressed) used by the
workspace band and the two flows under "Who works here".

## Pitch structure

The page leads with what a reader gets and who runs it, in a fraction of the
words it once carried (~300 visible words, six sections; the earlier nine
sections and ~2,000 words explained the architecture first, and readers said
they could not tell what xbin was for). Order: hero (**An office for your
agents.** + the install one-liner + the animated shell over the WebGL
tile-field) → **workspace band** (the real overview screenshot, no words) →
**Floor plan** (one app, one room: four facts + a terminal block) → **Who
works here** (role-prefixed use cases, then the two real screenshot flows) →
**Access** (identity and roles · grants and network policy · the kernel
sandbox · secrets, backups, audit) → **Install** → the closing call → footer.

Copy rules (D67): no comparisons and no "not X, but Y"; one claim per
sentence; verb-first or concrete-noun headlines; IT's own words verbatim
(identity, roles, SSO, grants, policy, audit); the office metaphor says where
agents work and who holds the keys, never what agents are; "app" on the page
("tile", "slot", "harness" stay in the docs). Hero ≤ 24 words, two CTAs, no
em dashes.

## Build

```
make website        # assembles website/dist/ (index.html, install.sh, og.png,
                    # fonts/, shots/, js/, vendor/)
```

`dist/` is the deployable artifact — any static host, GitHub Pages, or an
object store. `https://xbin.dev/` serves `index.html`;
`https://xbin.dev/install.sh` serves the bootstrap.

## og.png

`og.html` is the master artwork for the share card. After editing it, re-render:

```
chromium --headless --disable-gpu --screenshot=website/og.png \
  --window-size=1200,630 --hide-scrollbars \
  --default-background-color=16181dff "file://$PWD/website/og.html"
```

## install.sh & releases

`install.sh` is a deliberately tiny, auditable bootstrap — and **100%
static across releases**: at run time it resolves the latest semver tag
from the GitHub tags API (no auth, no jq; `XBIN_VERSION=vX.Y.Z` pins one,
resolution failure exits with the pin instructions), fetches that tag's
`deploy/install.sh`, and runs it with `XBIN_REF` pinned to the same tag —
so a piped install always builds a *released* tree, never master.
Arguments pass through (`… | sh -s -- --check-only`).

**Release flow** (each release): tag `vX.Y.Z` on master and push the tag.
That's it — the bootstrap picks it up on the next run. Only when the
bootstrap or the page *themselves* change: sync the `releases` branch
(`git push origin master:releases`) so the stable raw URL
`https://raw.githubusercontent.com/xbin-dev/xbin/releases/website/install.sh`
serves the current copy, and `make website` + redeploy `dist/`.
