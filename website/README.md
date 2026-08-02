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

`install.sh` is a deliberately tiny, auditable bootstrap: it pins
`CURRENT=<release tag>` (overridable with `XBIN_VERSION=…`), fetches that tag's
`deploy/install.sh` from GitHub, and runs it with `XBIN_REF` pinned to the same
tag — so a piped install always builds a *released* tree, never master.
Arguments pass through (`… | sudo bash -s -- --check-only`).

**Release flow** (each release):
1. Tag the release (`vX.Y.Z`) and push the tag.
2. Bump `CURRENT` in `website/install.sh`, commit.
3. Sync the `releases` branch to the release point (`git push origin
   <commit>:releases`) — the stable raw URL
   `https://raw.githubusercontent.com/xbin-dev/xbin/releases/website/install.sh`
   always serves the current bootstrap (point the xbin.dev `/install.sh` route
   at it, or redeploy `dist/`).
4. `make website` and redeploy `dist/` so the page and script match.
