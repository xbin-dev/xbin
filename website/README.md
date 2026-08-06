# website — xbin.dev

The **xbin.dev** landing page. `index.html` carries the whole pitch (no build
step, no external fonts/CDNs — same buildless ethos as the workspace), in the
product's own steel + hazard-amber palette, with IBM Plex Sans self-hosted in
`fonts/` (OFL, headings only) and one inline SVG icon set — no emoji, no
glyph-soup. Plus `install.sh`, the bootstrap installer the site serves, and
`og.png` (`og:image`, 1200×630). `shots/` holds the real-product screenshots
(pngquant-compressed) used by the workspace band and the workflows section.

## Pitch structure
Hero (thesis + install one-liner + animated shell) → **workspace band** (the
real overview screenshot, no words — proof, not claims) → "everything is a
directory" model → three pillars (yours / sandboxed / self-modifying) →
composition (typed wires) → app terminals (BYO agent) → **workflows** (the
change-a-tile and create-a-tile screenshot flows) → who it's for →
**users & orgs** (the multi-user model) → security posture → "in the box"
list → install → footer.

## Build

```
make website        # assembles website/dist/ (index.html, install.sh, og.png, fonts/)
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
