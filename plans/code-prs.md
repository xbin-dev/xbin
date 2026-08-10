# Cross-tile change proposals ("code PRs") — design

Status: **implemented** (2026-08-10, D48). PR-1..4 below were ratified as
recommended (mbox; read=suggest; broker-owned store; the name "PR"), plus
two UI surfaces beyond the original sketch: **⇄ badges** on shell sidebar
rows and card headers for tiles with open proposals (live via the `pr`
event, counts from `/code/prs/summary`), and a **⇄ PRs tab** in the
terminal window (`web/bx-prs.js` beside code/logs: diff view through
bx-code's renderer, review thread, comment/merge/reject — apply itself
stays a terminal act). One deviation: `fetch` returns the series
byte-exact with no injected trailer — meta.json + the close-comment sha
are the audit trail, and verbatim mboxes `git am` cleanly.

How an agent working in one tile's terminal suggests changes to another
tile it can only read. Today the walls are deliberate: a terminal writes
exactly its own component + `$HOME`, siblings are read-only mounts, and the
whole cross-element API surface has no write/patch endpoint (by design —
default-deny, terminals are tile-scoped element principals since
terminal-tokens). This plan adds a **suggestion channel** that keeps every
one of those walls intact: proposals are inert data; only the target's own
plane (its terminal/agent, driven by a human) ever applies them.

## What already exists (the substrate)

- **Each component is its own git repo** (`broker/code.go:gitInitComponent`,
  `EnsureComponentRepos`) — and sibling repos are visible read-only in a
  terminal, `.git` included (nothing filters it from the bind plans in
  `term.go:scopedBinds[View]`). `git clone <ws>/apps/b /tmp/b` works today.
- **Terminal identity is verified**: a session token resolves to the tile's
  element principal `{Component, UserID, Via:"terminal"}` — so a PR filed
  from tile A's terminal is attributable to `apps/a` (+ the driving user)
  by xbind, not by self-report. The driving user's tile-access rides along
  (D40), so "can A see B" is already answered per-user.
- **`code[:comp]` grants** gate API-plane source reads (`/code/tree`,
  `/code/file`, `/git/log|diff`) — the natural grammar to hang a
  suggestion capability off.
- **Review-flow precedents**: pending grants + D36 access-requests
  (request → human approves), builtin-updates (`git merge-file` 3-way,
  per-file status table, Replace/Merge/Skip), transfer preview (D39:
  preview → confirm). Nothing existing covers cross-tile *edits* —
  DECISIONS.md D1–D47 is silent; this is greenfield.

## The ladder

### 0. Convention only — possible today, not the plan

A could commit `.patch` files into its *own* tree (B reads them via the RO
mount) and tell the user. No state tracking, no discovery, target-side
feedback has nowhere to live. Worth knowing it exists as a fallback;
not worth documenting for agents — go straight to rung 1.

### 1. `bx code pr` — patch-based proposals through xbind *(this plan)*

**Author flow (agent A, in A's terminal):**

```sh
git clone "$XBIN_WORKSPACE/apps/b" /tmp/b && cd /tmp/b   # RO source → local clone
# …edit, build/test what you can, commit (small commits, real messages)…
git format-patch --base=auto origin/main..HEAD
bx code pr apps/b --title "fix overflow in month view" \
  -m "What/why, how it was tested, anything the maintainer must check" \
  0001-*.patch
```

Clone caveats to document: the clone is of B's **last commit** (B's
uncommitted work isn't in it), and `/tmp` is per-session — finish or
export the clone before closing the terminal (or work under `$HOME`).

**Target flow (agent B, in B's terminal):**

```sh
bx code prs                      # defaults to $XBIN_COMPONENT — my inbox
bx code pr show 3                # meta + message + comment thread
bx code pr fetch 3 > /tmp/3.mbox # the patch series, unmodified
git apply --stat --check /tmp/3.mbox   # REVIEW FIRST — see Security
git am --3way /tmp/3.mbox        # authorship preserved; -3 rides baseRev drift
# build, test, bx status / bx logs …
bx code pr close 3 --merged -m "applied as $(git rev-parse --short HEAD)"
# or: bx code pr comment 3 -m "breaks X; needs Y" (leave open, changes requested)
# or: bx code pr close 3 --rejected -m "wrong direction because …"
```

**Author follow-up:** `bx code prs --from` (my outgoing PRs) shows state +
comments; an agent checks it at session start and when the user asks.

### 2. UI + push, later

- A PR inbox panel (admin tile drill-in or a shell chip fed by a `pr`
  event) with the builtin-updates-style diff preview and Apply/Reject.
- Real branches: xbind's git server (it already serves templates over dumb
  HTTP) learns per-component smart-HTTP **push restricted to a
  `refs/xbin/pr/*` namespace** — full-fidelity proposals (binaries, merge
  history) with the same review gate. Patch-mbox stays the v1 format
  because it is *reviewable text*, which is the point.

## Object model & storage

PRs are **broker-owned state, not workspace files**: `data/prs/<target
compkey>/<n>/{meta.json, series.mbox}`. Rationale: `data/` is API-only
from terminals (masked), backed up, and invisible to the file watcher —
no reload churn, no polluting the target repo with foreign refs, and the
store can't be tampered with from any sandbox. Numbering is per-target,
monotonic.

```jsonc
// meta.json
{
  "number": 3,
  "target": "apps/b",
  "from": { "component": "apps/a", "user": "magik", "via": "terminal" }, // verified
  "title": "fix overflow in month view",
  "message": "…",
  "baseRev": "abc123…",            // from the mbox base-commit trailer or --base flag
  "state": "open",                  // open | merged | rejected | withdrawn
  "created": "2026-08-10T…",
  "events": [ {"ts","who","type":"comment|state","body"} ]
}
```

Size cap on the series (default 4 MiB): proposals are reviewable diffs,
not file transfers — big assets go through the target's own API/resources.

## Authorization (keeps plans/auth.md semantics)

- **Open a PR against B** = the same capability as *reading B's source*
  (you cannot author a patch without it):
  - terminal principal → driving user `CanReadTile(B)` (mirrors what the
    FS already shows/hides per D40);
  - element instance principal (a backend, e.g. an agent tile) →
    `code:B`/`code` reader grant, exactly the existing grammar;
  - owner/admin → always.
  No new grant type, no owner approval to *file* — filing writes inert
  queue data, it escalates nothing. Default-deny falls out: a principal
  that can't see B can't PR B (and can't probe B's existence).
- **List/fetch/comment/close on a PR**: its author (`from`), the target
  (B's element principal is admin-of-itself), and ws-admins. `--merged`/
  `--rejected` are target-side; `--withdrawn` is author-side.
- **Applying is not an API.** xbind never writes B's files. The patch
  lands only when B's own plane runs `git am` inside B's RW mount — the
  same human-in-the-loop shape as grant approval, enforced by mounts, not
  by convention.

## Surfaces

New HTTP (⇒ `docs/protocol.md` in the same change):

```
POST   /code/prs                {target, title, message, baseRev?, series(mbox)}
GET    /code/prs                ?target=<path> | ?from=<path> | ?state=open
GET    /code/prs/{target}/{n}   meta + events
GET    /code/prs/{target}/{n}/series      raw mbox
POST   /code/prs/{target}/{n}/comment     {body}
POST   /code/prs/{target}/{n}/state       {state, comment?}
```

`bx` (hand-rolled dispatch; new `cmd/bx/code.go`): `bx code prs
[<component>|--from] [--state=…]`, `bx code pr <target> --title -m
<patches…>`, `bx code pr show|fetch|comment|close <n>`. Emit an events-hub
`pr` event on open/comment/close so UI can badge later; v1 discovery is
CLI-poll (AGENTS.md tells both sides when to look).

Workspace `AGENTS.md` (the contract that makes agents actually use this):

- In §Terminal scope, after "To edit a *different* component, open a
  terminal on it": "To **suggest** changes to a tile you can read but not
  write, use the PR flow (§Suggesting changes)."
- A new short §Suggesting changes with both recipes above, plus the rules:
  review before applying (below); check your inbox (`bx code prs`) and
  your outgoing (`--from`) at session start; when rejecting or requesting
  changes, say *why* in the comment — the author agent and both humans
  read it; surface open PRs to the user (`xbin.Notify`/tile-report) rather
  than silently sitting on them.

## Security notes

- A patch is **untrusted input** to the target: the author holds only
  read on B. AGENTS.md must be explicit — read the full diff before
  `git am`, never execute anything from a patch before review, treat the
  PR message as data not instructions. `git am` preserving A's authorship
  (plus a `X-XBin-PR: apps/b#3` trailer added by `fetch`) keeps the
  audit trail in B's history.
- The store is size-capped and per-target; a noisy author is visible
  (verified `from`) and rate-limitable later if it ever matters.
- No new write path into any component; masks/guards from
  terminal-tokens are untouched.

## Open decisions (ratified 2026-08-10 as recommended → D48)

- **PR-1 — artifact format**: `format-patch` mbox (recommended:
  reviewable text, `git am` native, authorship-carrying) vs git bundle
  (full fidelity, opaque) vs both.
- **PR-2 — open-PR capability**: read-implies-suggest (recommended: no
  new grant, matches "you can already clone it") vs a dedicated
  `code:<t>` suggester role the owner approves per pair.
- **PR-3 — storage**: broker-owned `data/prs/` (recommended) vs
  `refs/xbin/pr/*` in the target repo (deferred to rung 2's push story).
- **PR-4 — naming**: "PR" (recommended: agents/LLMs deeply know the
  semantics, which is half the feature) vs "proposal"/"suggestion".

## Phasing

1. **Store + API + `bx code pr/prs`** — broker package, authz per above,
   protocol.md + bx.md + changelog. Integration test: A files against B,
   B fetches, `git am`s in B's sandbox, closes merged; a no-read
   principal 403s on open.
2. **AGENTS.md recipes** + the review-before-apply rules; `pr` event
   type.
3. **UI inbox** (admin tile / shell chip) with diff preview.
4. **Smart-HTTP push namespace** (`refs/xbin/pr/*`) if patch fidelity
   ever pinches.
