# website — xbin.dev

The **xbin.dev** landing page. A single self-contained `index.html` (no build
step, no external fonts/CDNs — same buildless ethos as the workspace), in the
product's own steel + hazard-amber palette, plus `install.sh`, the bootstrap
installer the site serves.

## Pitch structure
Hero (thesis + install one-liner + animated shell) → three pillars (yours /
sandboxed / self-modifying) → "everything is a folder" model → composition
(typed wires) → app terminals (BYO agent) → who it's for → **users & orgs**
(the multi-user model) → security posture → feature grid → install → footer.

## Build

```
make website        # assembles website/dist/ (index.html + install.sh)
```

`dist/` is the deployable artifact — any static host, GitHub Pages, or an
object store. `https://xbin.dev/` serves `index.html`;
`https://xbin.dev/install.sh` serves the bootstrap.

## install.sh & releases

`install.sh` is a deliberately tiny, auditable bootstrap — and **100%
static across releases**: at run time it resolves the latest semver tag
from the GitHub tags API (no auth, no jq; `XBIN_VERSION=vX.Y.Z` pins one,
resolution failure exits with the pin instructions), fetches that tag's
`deploy/install.sh`, and runs it with `XBIN_REF` pinned to the same tag —
so a piped install always builds a *released* tree, never master.
Arguments pass through (`… | sudo bash -s -- --check-only`).

**Release flow** (each release): tag `vX.Y.Z` on master and push the tag.
That's it — the bootstrap picks it up on the next run. Only when the
bootstrap or the page *themselves* change: sync the `releases` branch
(`git push origin master:releases`) so the stable raw URL
`https://raw.githubusercontent.com/xbin-dev/xbin/releases/website/install.sh`
serves the current copy, and `make website` + redeploy `dist/`.
