# website — xbin.dev

Prototype landing page for **xbin.dev**. A single self-contained `index.html`
(no build step, no external fonts/CDNs — same buildless ethos as the workspace),
in the product's own steel + hazard-amber palette. Responsive desktop→mobile;
the hero is a faithful, self-booting mini-shell so the page *is* the UX.

## Pitch structure
Hero (thesis + install one-liner + animated shell) → three pillars (yours /
sandboxed / self-modifying) → "everything is a folder" model → **who it's for**
(homelab, Linux enthusiasts, IT admins, technical business) → security posture →
feature grid → install → footer.

## Deploy
Static — any host, GitHub Pages, or an object store. Or dogfood it: serve it as
an xbin tile. It's one file with inlined CSS/JS and an inline SVG logo.

## Placeholders to wire before launch
- **`https://xbin.dev/install.sh`** — set up as a redirect to the real installer
  (`https://raw.githubusercontent.com/magik6k/xbin/master/deploy/install.sh`), or
  serve the script at that path.
- Copy is a first pass — tighten claims against the shipping feature set before
  it goes public.

## Status
v1 prototype for review. Not yet a launch artifact — see the repo's
open-sourcing prep (`plans/user-org-ux.md` and the mobile-mode work) for what
should land in the product before the site points people at it.
