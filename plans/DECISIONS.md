# Buxon — Decision Log

Status meanings:
- **NEEDS CALL** — blocks or shapes early work; want an explicit decision.
- **DEFAULT SET** — a default is picked and the plans assume it; veto if wrong.
- **ACCEPTED TRADE-OFF** — known wart, recorded so it's deliberate.
- **RESOLVED** — decided (date, decider).

---

## Resolved (2026-07-02, magik6k)

- **D10 — Repo & license**: `github.com/magik6k/buxon`, images at
  `ghcr.io/magik6k/buxon`, **dual-licensed MIT + Apache-2.0** (Rust-style,
  `LICENSE-MIT` + `LICENSE-APACHE`).
- **D2 — Workspace git policy**: option (a) — auto `git init`, ignore `.buxon/` +
  `data/`, never auto-commit.
- **D1 — Fat image**: confirmed; `-slim` stays backlog.
- **D13 — Container user**: option (b) — start as root, drop to workspace-owner uid.
  (Now also load-bearing for auth tier 2: root buxond is what enables per-scope uids.)
- **D3 — Auth**: superseded — the single-token sketch was too weak for the intended
  element↔element model. Full design in **`auth.md`**: element identities, RBAC
  roles/grants with a buxond gateway, per-element vaults, standardized `API.md` +
  `expose.roles` docs, enforcement tiers. Owner/browser login mechanics from the old
  D3 (one-time URL → cookie, bearer for CLI) survive unchanged as the *owner*
  principal. New sub-decisions ND1–ND5 below.
- **D9 — was "ro grants are honor-system"**: superseded by the tier model in
  `auth.md` §9. Cross-scope db access is brokered (no file-path disclosure) at tier 1
  and fs-enforced at tier 2; the old accepted-trade-off text no longer applies as
  stated. Residual honesty: tier 1 identity is spoofable via `/proc` by a determined
  hostile element — closed at tier 2.

---

## Needs a call

### ND1 — Per-scope uids (auth tier 2): phase 4 or later?
`auth.md` §9. Buxond already runs as root (D13b), so spawning each scope's backends
under a dedicated uid is cheap mechanically, and it's what turns identity, vault, and
resource grants from "attribution" into "enforcement" — including *elements can't
modify source, even their own* (editing becomes terminal-only). Cost: uid allocation
bookkeeping, per-resource group or brokered fallback for cross-scope shared-rw
sqlite, more integration tests. **Recommendation: do it in phase 4** alongside the
broker — retrofitting enforcement later is exactly how honor systems calcify.

---

## Defaults set — veto if wrong

### Auth (new, from `auth.md`)
- **ND2 — Browser-side caller attribution via injected frame tokens.** Owner cookie
  authenticates the human; per-frame token (injected at the D4 point, attached by
  `buxon.fetch()`) attributes requests to an element; cookie-without-token = owner,
  only off element pages. Alternative (Referer-sniffing only) rejected as too
  heuristic; alternative (accept the same-origin hole) rejected as it guts RBAC in
  the plane users actually build in.
- **ND3 — Vault at-rest encryption deferred.** v1: plaintext under buxond-uid 0600,
  excluded from backups by default and from git always. Optional env-provided master
  key later. Encrypting with a key stored on the same disk is theater; a real KMS
  story can wait.
- **ND4 — Role names free-form, `reader`/`writer`/`admin` as blessed convention**
  with SDK-known ordering (`admin ⊃ writer ⊃ reader`); custom roles exact-match
  unless manifest declares `implies`. Mandatory human descriptions per role.
- **ND5 — Same-scope grants auto-approved** at the requested role; cross-scope and
  workspace-global grants require one-time owner approval. A scope is one app, one
  trust unit.
- **Vault has no cross-element sharing** — deliberate. Shared secret = each element
  stores its own copy, or the owning element wraps it behind a role-guarded API.
- **`uses` unifies API + resource grants** (replaces the separate `resources`
  manifest key); `deps` (code visibility) stays separate from `uses` (call rights).

### Pre-existing
- **D4 — Serve-time import-map injection** is the one sanctioned HTML transform
  (now also carries the frame token). Opt-out `"inject": false` (which also forfeits
  frame-token attribution — such an element's frontend can only be owner-called).
- **D5 — Manifests are JSONC.**
- **D6 — `$HOME` = `/workspace/home`.**
- **D7 — `bx` CLI ships in phase 3, minimal** (`new/logs/ls/doctor/grant`, now +
  `api`, `vault`).
- **D8 — Blue/green drain: 30 s then kill.** (Instance credentials also die at swap —
  auth.md §2.)
- **D14 — No TLS in buxond**; Tailscale or fronting proxy.
- **D12 — Playwright e2e only JS tooling, dev-side only.**
- **Nested-frame reload targeting** — longest-prefix match, most-specific frame only.
- **Reserved namespace** — component id `buxon`; top-level `vendor`, `data`,
  `.buxon`, `home`; URL prefixes `/c/ /api/ /ws/ /vendor/ /healthz`.

---

## Accepted trade-offs (recorded, not blocking)

- **Tier 1 identity is soft** (same-uid `/proc` token theft possible) until ND1
  lands. The model is right from day one; the floor hardens at tier 2. Do not market
  tier 1 as element isolation.
- **Browser plane: attribution, not isolation.** Same-origin elements share the JS
  realm's ambient powers (DOM of embedding page, storage). Frame tokens make RBAC
  meaningful; subdomain-per-scope (phase 5) makes it enforced.
- **D11 — buxond restart kills terminal sessions**; `tmux` inside is the workaround.
- **In-memory bus, at-most-once**; durability is the subscribing app's job.

---

## Review findings (from the plans review pass; fixes applied)

1. **Import-map injection vs "no HTML rewriting"** — contradiction resolved as D4
   (sanctioned single transform + opt-out); ARCHITECTURE.md §2 updated.
2. **`Authorization: Bearer` was missing** — cookie-only auth would break `bx`/curl
   from terminals; bearer path added.
3. **Proxy behavior during rebuild was undefined** — `Ensure` blocks until
   healthy/failed; 502 + JSON error the overlay understands.
4. **inotify limits are a host sysctl** — documented in deployment + `bx doctor`
   check; likely #1 support issue otherwise.
5. **Nested component reload ambiguity** — longest-prefix targeting.
6. **Bind-mount uid friction** — surfaced as D13, resolved (b).
7. **Unix socket 108-byte path limit** — hashed short run dirs.
8. **Editor atomic saves double-fire rebuilds** — watcher coalescing + unit tests.

## Implementation notes (2026-07-02, initial build of phases 1–4)

Deviations and refinements made while implementing; all deliberate:

- **Backend bus subscriptions became "frontends subscribe, backends don't".**
  Long-lived backend WS subscriptions fight idle-reaping; instead of the
  planned manifest bus-hooks, v1 ships: frontends subscribe live
  (`buxon.bus.on`), backends publish + use cron for reactions. Documented in
  docs/resources.md. Manifest-declared bus→endpoint webhooks remain a clean
  future addition if needed.
- **Cross-scope sqlite = not supported (not even brokered).** The plans
  already leaned this way; docs now state it plainly: same-scope gets the
  file path, everyone else uses the owning app's API. The brokered query API
  stays deferred until a concrete need.
- **Calendar example uses kv, not sqlite.** Keeps the flagship example free
  of a heavyweight sqlite driver dependency; sqlite provisioning/env
  delivery is implemented and documented, just not exercised by the example.
- **Tier-2 per-scope uids are implemented** (`--scope-uids`: uid allocator,
  spawn credentials, data chown) **but not exercised in an automated test**
  — needs a root environment; the attack-style integration tests from the
  plan are still to be written in a container context.
- **Owner login for dev (`--no-auth`) keeps element identity active** —
  instance/frame tokens still resolve to element principals so dev and prod
  run identical RBAC. Worth knowing when debugging "why is my curl owner but
  my backend isn't".
- **Unix-socket 108-byte limit handled** by falling back to a tmp run dir
  (symlinked from `.buxon/run`) when the workspace path is deep.
- **`buxond init` is also auto-init**: an empty bind mount initializes on
  first boot, per deployment.md.

## Residual risks (watching, no action now)

- Cold-cache `go build` latency for dep-heavy components — shared GOMODCACHE + image
  pre-warm; shared build daemon only if it hurts.
- Watch-count growth on monorepo-scale trees — per-dir watches fine to ~10⁴ dirs.
- fsnotify drift on macOS dev hosts — weekly macOS CI job.
- Manual vendored-ESM upgrades — fine at 2 deps; growth would demand an import-map
  generator, not a bundler.
- Frame-token UX friction: element authors must use `buxon.fetch()` (or copy the
  header) for cross-element calls; raw `fetch` to a sibling 403s. Mitigate with a
  crisp error body pointing at the docs.
