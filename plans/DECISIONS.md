# XBin — Decision Log

Status meanings:
- **NEEDS CALL** — blocks or shapes early work; want an explicit decision.
- **DEFAULT SET** — a default is picked and the plans assume it; veto if wrong.
- **ACCEPTED TRADE-OFF** — known wart, recorded so it's deliberate.
- **RESOLVED** — decided (date, decider).

---

## Resolved (2026-07-02, magik6k)

- **D10 — Repo & license**: `github.com/magik6k/xbin`, images at
  `ghcr.io/magik6k/xbin`, **dual-licensed MIT + Apache-2.0** (Rust-style,
  `LICENSE-MIT` + `LICENSE-APACHE`).
- **D2 — Workspace git policy**: option (a) — auto `git init`, ignore `.xbin/` +
  `data/`, never auto-commit.
- **D1 — Fat image**: confirmed; `-slim` stays backlog.
- **D13 — Container user**: option (b) — start as root, drop to workspace-owner uid.
  (Now also load-bearing for auth tier 2: root xbind is what enables per-scope uids.)
- **D3 — Auth**: superseded — the single-token sketch was too weak for the intended
  element↔element model. Full design in **`auth.md`**: element identities, RBAC
  roles/grants with a xbind gateway, per-element vaults, standardized `API.md` +
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
`auth.md` §9. XBind already runs as root (D13b), so spawning each scope's backends
under a dedicated uid is cheap mechanically, and it's what turns identity, vault, and
resource grants from "attribution" into "enforcement" — including *elements can't
modify source, even their own* (editing becomes terminal-only). Cost: uid allocation
bookkeeping, per-resource group or brokered fallback for cross-scope shared-rw
sqlite, more integration tests. **Recommendation: do it in phase 4** alongside the
broker — retrofitting enforcement later is exactly how honor systems calcify.

---

## Defaults set — veto if wrong

### Runtime & deployment model (new, from `runtime.md`)
- **RT-1 — Production boundary is a VM/host xbind controls**, not a Docker
  container; xbind becomes the sandbox runtime for its components. **The
  single-container Docker runtime is now dropped** (2026-07, at the user's
  direction — it was container-as-boundary with no per-component isolation, less
  secure than the sandbox runtime): `docker/Dockerfile`, `docker/compose.yml`,
  the `make image` target, and the CI image job are removed; Docker survives only
  as a build tool for the base rootfs / fuse-overlayfs. `plans/deployment.md` is
  retained as history. (Supersedes the "container is the boundary" framing in D1.)
- **RT-2 — Component userland is a fat, Ubuntu-based OCI rootfs** (Go/Node/Python/
  git + `opencode`/`claude-code` + `bx`), mounted ro as every sandbox's base,
  overlayfs per component; kept separate from a minimal host OS.
- **RT-3 — Ship a virtual appliance** (qcow2/OVA/ISO/cloud), immutable + A/B
  updates, workspace on a data disk, as the eventual production on-ramp.
- **RT-4 — Terminals share the base rootfs** (agents + toolchains present by
  default) but stay unsandboxed-from-workspace (owner plane).
- **RT-5 — `wasm`/wazero is a first-class lightweight runtime** alongside the
  rootfs-based ones.
- **D1 revisited**: the fat image stays, but as the base **OCI rootfs** for
  sandboxes/terminals, not as the deployment boundary; `-slim` remains backlog.

### Per-component isolation — Tier 3 (new, from `isolation.md`)
- **ISO-1 — Egress mechanism**: target is netns + a transparent userspace egress
  relay (per-component, DNS-aware, rootless-capable); nftables-per-uid owner
  rules are the simpler interim (per-scope, kernel-enforced). Phased, not
  either/or.
- **ISO-2 — Egress grants**: `net:internet[:port]` (the internet **scope**, all-
  or-nothing, never covers RFC1918) and `net:<cidr|host>[:port]` (LAN/specific,
  per address/subnet/port) as `uses` targets at role `egress`. Internet and LAN
  are separately grantable; owner-approved, never self-approved. Egress **to the
  xbind gateway** (unix socket) is always allowed — that's the RBAC path.
- **ISO-3 — Capabilities**: the default container stays **unprivileged (Tier 1)**;
  Tier 3 is opt-in (`--isolate`) and needs user namespaces (preferred) or
  CAP_NET_ADMIN/CAP_SYS_ADMIN, shipped as a separate hardened deployment.
- **ISO-4 — Namespace lifecycle**: default to reusing a per-scope namespace across
  backend generations (hot-reload perf) over a fresh ns per spawn; measure.
- **ISO-5 — Filesystem view**: the sandbox root is exactly {own dir ro, granted
  deps ro, granted resource files rw, toolchain ro, gateway socket, private
  tmp/dev/proc}; everything else is unmounted, not just unreadable. Ingress stays
  unix-socket-only (only xbind can connect in).

### Code management — one workspace repo, per-component views
- **CM-1 — No per-component git repos.** Considered giving each component its
  own `.git`; rejected. The workspace is already one git repo (D2), so nested
  repos mean embedded-repo/gitlink confusion, `cp -r` copying `.git` (shared
  history), and friction with go.work/deps and the "just files in one repo"
  ethos. Instead the Admin tile's **code & history** tab scopes to a component's
  path (`git log/diff -- <path>`, read files under the dir), giving per-component
  history/diffs/code with none of the downsides. Independent per-component
  *push/share* can be layered later via `git subtree`/`filter-repo` (see
  `tile-sharing.md` rungs 2–3) without changing this. *(Decided while the user
  was away; veto if per-component repos are actually wanted.)*
- **CM-2 — Commit policy.** Agents commit **often and unprompted** on any
  meaningful change; the user is never asked to approve a commit. Small,
  component-scoped commits. xbind still **never auto-commits** (D2) — commits
  are the agent's/human's action, git is the reversibility net. Documented in
  `AGENTS.md`.
- **CM-3 — Code/history endpoints are admin-gated.** `/api/xbin/{code,git}/*`
  need `xbin:admin` (like the rest of the console) — they expose source and
  history across the whole workspace, which is owner-level.

### Builtin updates (new, from `builtin-updates.md`)
- **BU-1 — Version signal**: content-hash decides *whether* an update exists
  (auto, no manual bumping); a small human `version` + one-line changelog in the
  catalog communicates *what*. Alt (hand-maintained semver only) rejected —
  someone always forgets to bump.
- **BU-2 — Marker + base snapshot in `.xbin/`** (gitignored, per-deployment):
  `.xbin/builtins.json` + `.xbin/builtins/<id>/`. Lost on a bare clone → the
  adoption path re-seeds. Alt (tracked root lockfile) kept as an option for
  teams that want builtin versions in git history.
- **BU-3 — 3-way merge via `git merge-file`** (workspace is already a git repo);
  no bespoke merge engine.
- **BU-4 — Scope**: scaffold + imported tiles are managed; **template instances
  are forks and are never auto-updated** (plans/templates.md). Updating a
  template changes only the *next* instantiation.
- **BU-5 — Recoverability**: "Replace" is safe because everything is in git;
  optional pre-update checkpoint commit makes the diff reviewable.

### Auth (new, from `auth.md`)
- **ND2 — Browser-side caller attribution via injected frame tokens.** Owner cookie
  authenticates the human; per-frame token (injected at the D4 point, attached by
  `xbin.fetch()`) attributes requests to an element; cookie-without-token = owner,
  only off element pages. Alternative (Referer-sniffing only) rejected as too
  heuristic; alternative (accept the same-origin hole) rejected as it guts RBAC in
  the plane users actually build in.
- **ND3 — Vault at-rest encryption — RESOLVED (implemented 2026-07-02).**
  Superseded: the vault now has an AES-256-GCM encryption barrier
  (`internal/vault`). A random DEK encrypts each vault file; the DEK is
  wrapped by an Argon2id-derived KEK and only the wrapped DEK + salt sit on
  disk. Seal/unseal: the passphrase is supplied at unseal (never persisted)
  and the DEK held in memory (mlock best-effort). Boot modes:
  `XBIN_VAULT_PASSPHRASE` auto-unseal, manual `bx vault unseal`, or
  plaintext-with-warning when no passphrase is set (non-breaking zero-config
  default). Existing plaintext is migrated to ciphertext on first init.
  Scope is at-rest encryption + seal/unseal, **not** full Vault parity (no
  Shamir splitting, transit engine, dynamic secrets, leases). Details:
  docs/auth.md §vault. The old "same-disk key is theater" concern is
  resolved: the passphrase is the one secret that never touches at-rest data.
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
- **D6 — `$HOME` = `/workspace/home`.** *Amended:* `$HOME` is per user —
  `/workspace/homes/<user>` (root token → `homes/owner`), lazily seeded per
  user; a legacy shared `home/` migrates at startup to the workspace's sole
  user/admin (else `homes/owner`), bailing only when both forms hold real
  data. Hygiene, not a security boundary (shells carry the owner token).
- **D7 — `bx` CLI ships in phase 3, minimal** (`new/logs/ls/doctor/grant`, now +
  `api`, `vault`).
- **D8 — Blue/green drain: 30 s then kill.** (Instance credentials also die at swap —
  auth.md §2.)
- **D14 — No TLS in xbind**; Tailscale or fronting proxy.
- **D15 — User ids are immutable, validated keys.** A user id is the permanent
  key for `homes/<user>`, the prefs bucket, and `user:<id>` attribution, so it
  is validated at creation (`[a-z0-9][a-z0-9._-]{0,31}`, `owner` reserved) and
  never renamed — locked before GA because the charset can't be tightened once
  real ids exist on disk. Charset ⊆ what homes' sanitizeHomeKey preserves, so
  id == home key (no two ids fold onto one home). Load bypasses validation, so
  legacy/hand-edited ids keep working; only new users are gated.
- **Terminal tokens (min(user, tile))** — a terminal's `XBIN_TOKEN` is a
  per-session token resolving to the TILE's element principal (self-admin +
  its approved grants; the frame-token model for shells), never the owner —
  so agents can't self-approve grants or read other tiles' admin surfaces.
  Session-open gates: `CanUseTile(cwd)`; the **root terminal is disabled**
  (whole-ws editing + owner automation live on the host). Deleting a user
  kills their live shells' API access. `plans/terminal-tokens.md`.
- **D16 — Per-tile access tiers + a create permission** (planned RBAC refinement,
  `plans/multi-user.md`). Replace the flat `Tiles []string` allow-list + global
  `Terminal bool` with per-tile levels **read < write < terminal** (monotone:
  terminal⊇write⊇read) — `read` = see the tile + its source (the visibility gate,
  D17), `write` = edit/drive it, `terminal` = a shell on it — plus a **prefix-
  scoped `CanCreate`** (`sales/*`, reusing the existing `prefix/*` syntax; "create"
  ≈ "own a namespace"). Creating a tile auto-grants the creator `terminal` on it.
  Rationale: a mixed dev/sales/exec team needs finer grants than "use or not."
  Migration is trivial (prod is one admin = all); loader upgrades `Tiles`→`write`
  and `Terminal:true`→`terminal`, accepting both shapes and rewriting on save
  (D15-style). IMPLEMENTED 2026-07-11 (`internal/users`, gates in
  `internal/auth`; levels union — highest matching entry wins, patterns widen
  and never narrow; legacy API bodies still accepted, responses are new-shape;
  view/frame/alerts gates = read, terminal open + dev-layer reset = terminal;
  create auto-grant fires for the attributed user even when driving a
  manager-style tile). `docs/changes/2026-07-11-tile-access-tiers.md`.
- **D17 — Non-admin terminals are locked down by default** (mixed-tenant hygiene;
  each gates on `!IsAdmin()`, so admin/owner terminals are unchanged and — since
  prod is single-admin — these are dormant until non-admin users exist):
  (a) **source visibility scoped to the allow-list** — bind only tiles the user
  may access + shared SDK, mask the rest (today every terminal sees all source);
  (b) **`api=0` by default** — no live-tile token unless explicitly granted;
  (c) **`net=none` by default** — no internet egress (the exfil path that makes
  (a) matter); (d) **cgroup + disk limits** on the terminal (survive-incompetence).
  D18 is the kernel-level half of this. IMPLEMENTED 2026-07-11 except the disk
  half of (d): (a) = sealed masks over every tile below `read` (term.Manager.
  HiddenTiles, wired from the registry); (b)/(c) = **explicit per-user grants**
  `TermAPI`/`TermNet` (the "self-serve vs grant" question resolved: grants —
  they ride the same users.json/UI we were already touching, and clamping beats
  403 so an ungranted user still gets a working airgapped code-only shell;
  `net=host` stays admin-only unconditionally); (d) = restricted sessions join
  a per-session cgroup leaf with the backend caps when delegation is on
  (admin terminals stay unlimited). Disk quotas on terminals DEFERRED — needs
  per-directory quota machinery the workspace fs doesn't have; the existing
  low-free-space alerts + resource-write blocks still apply workspace-wide.
- **D18 — Restricted terminals block namespace re-privilege via ucounts, not
  clone-filtering.** For an untrusted terminal we drop `CAP_SYS_ADMIN` (mount /
  namespaces) — but `apt` never needed it (only file caps: CHOWN/DAC_OVERRIDE/
  FOWNER/FSETID/MKNOD/SETFCAP/SETUID/SETGID/SYS_CHROOT), so we keep those and it
  still installs packages. The hard part: unprivileged userns creation needs *no*
  capability, so a capless shell could `unshare -Ur` into a nested userns and
  regain a full cap set. **Primary fix** — init writes `/proc/sys/user/max_user_
  namespaces=0` + `max_mnt_namespaces=0` inside the terminal userns (blocks
  creation *inside* `create_user_ns`/`copy_mnt_ns`, so it's immune to `clone3`'s
  in-memory flags that seccomp can't read), **then drops `CAP_SYS_RESOURCE`** so
  the shell can't raise the limit back — a closed loop (can't raise it, can't
  escape to a userns to try). **Belt-and-suspenders** — a seccomp filter EPERMs
  `clone`/`unshare`(NEWUSER|NEWNS)/`setns` and ENOSYS's `clone3` (the systemd/
  Docker `RestrictNamespaces` recipe; **ENOSYS not EPERM**, or glibc aborts
  `pthread_create`→apt, per moby#42680). **Rejected:** seccomp user-notify
  (unsound — its own man page forbids security use; TOCTOU on the re-read of
  clone3's memory) and ptrace `RET_TRACE` (sound only single-threaded; a hostile
  sibling races the same window). **Considered/deferred:** BPF-LSM `userns_create`
  (6.1+, per-cgroup) — clean but no mount-ns hook, needs `lsm=bpf`/reboot/host-root;
  the ucount knob is upstream, LSM-free, and covers both. Cost: restricted
  terminals can't run rootless podman / nested `bwrap` / Chrome's userns sandbox
  (`--no-sandbox`) — acceptable for the untrusted tier; dev/admin keep full caps.
  Researched in depth (three agents; man pages + kernel `ucount.c`/`user_
  namespace.c` + moby/systemd sources). `internal/sandbox` + `docs/isolation.md`.
- **D18a — Net-provider tiles keep net-admin caps via an admin-granted
  `cap:net-admin`.** D18 made every tile backend fully unprivileged
  (`dropAllCaps` under `Spec.Unprivileged`) — which broke net-PROVIDER tiles
  (egress-approver, netrouter): building their dataplane needs CAP_NET_ADMIN
  (routing tables, `ip_forward` sysctl) + CAP_NET_RAW (`AF_PACKET`), so they
  died at gate setup with "operation not permitted" (regression 2026-07-10,
  663ec76; reported 2026-07-12). Fix: a reserved capability grant
  `cap:net-admin` (admin-only to approve, like `gpu:*`; declared in the
  provider's `uses`, pending on import) makes the sandbox
  `dropCapsExcept(netProviderCaps())` — keeping only NET_ADMIN / NET_RAW /
  NET_BIND_SERVICE **inside the tile's own netns**, still dropping every other
  cap and applying the backend seccomp block-list (which never blocked the net
  syscalls — the break was purely the caps). Chosen over auto-granting any
  `provides: net` tile (declaring a provide isn't an admin action, so an
  imported tile could self-claim raw sockets) and over a blanket "sysadmin"
  cap (a provider needs only the three net caps; scope to what's needed). The
  policy ceiling's `net` deny class covers it (a tile denied network can't be
  a provider); `grantedRole`→`grantRestart` so approval takes effect without a
  manual restart. `internal/sandbox` + `internal/broker/gpu.go` + runner hook.
- **D19 — Orgs & teams: positional `/o/` path binding, union-only grants**
  (plans/orgs.md; user decisions 2026-07-11). Multiple orgs per workspace; an
  org OWNS a namespace **positionally** — the reserved `o` segment
  (`o/<org>/…` or `<dir>/o/<org>/…`, e.g. `apps/o/sales/crm`), reddit-style,
  with `u/` reserved now for future per-user tiles — so a tile's org is
  readable off its path, collisions are impossible, and there is no ownership
  table to drift. Non-marker paths stay workspace-plane (the existing
  deployment keeps working untouched). `o`/`u` are rejected in NEW tile paths
  everywhere else (create/clone/imports; existing dirs grandfathered, doctor
  warns) — squatting `apps/o/x` before org x exists is impossible. Teams are
  GitHub-semantics **union-only** (rejected: caps/maxLevel — GitHub doesn't,
  and min-over-caps × max-over-grants is unreasonable-about): effective level
  = max(own entries, teams' entries clamped to OrgOf(path)==team's org, org
  basePermission (""|read|write — never terminal), org-admin implicit
  terminal). The org clamp is EVALUATION-time, so hand-edited escaping
  patterns are inert (write-time pattern validation deliberately skipped —
  the clamp is the guarantee; doctor flags inert patterns). Create-in-team:
  per-team `newTiles` level (default write) auto-granted to the team, chosen
  over per-creation choice (a low-trust member could hand the team terminal)
  and over namespace-only (no per-tile row to show/revoke); creator keeps the
  D16 terminal auto-grant. All identity data in `data/users.json` — outside
  the workspace, terminals/tiles can't edit ACLs. `internal/users/orgs.go` is
  the whole semantic core; auth/broker/API/UI only ask it questions.
  AMENDED (pre-ship review): creating INSIDE an org requires org membership
  (a broad personal canCreate like `apps/*` must not inject tiles into
  `apps/o/<org>/…`; read/write personal patterns deliberately stay global —
  the auditor case); the org container path itself (`…/o/<org>`) is not a
  valid tile path; and tile-creation authority is one shared gate across
  create/clone/git-import/tile-import/template-instantiate (canCreate works
  on all of them, copy-shaped routes need read on the source, and an
  element's xbin:writer never extends the attributed DRIVING user's own
  create rights — the confused-deputy clamp; unattributed automation keeps
  capability semantics).
- **D20 — Org policy = pattern-keyed ceiling rows, enforced at evaluation.**
  `{tiles, deny[net|gpu|xbin-caps], mayCall[]}` at workspace + org level;
  matching rows compose restrictively (any deny wins; every mayCall-bearing
  row must cover the target — intersection). Enforced at approval (friendly
  400 naming the row) AND at every evaluation: `grantedRole` applies the
  ceiling before all three grant sources (explicit rows, interface bindings,
  same-scope auto-grants) so a hand-edited xbin.json can't bypass it — grants
  live in the git-tracked workspace manifest, but the CEILING lives in
  xbind-owned users.json; `netBinding` (the one net resolution point) goes
  inert under a deny; gpu rides grantedRole. `xbin-caps` also kills a covered
  element's xbin/xbin:users roles → its broker-adminship. Humans are never
  subject to policy (it constrains the runtime plane). Chosen over global
  toggles (no per-namespace differentiation) and over per-team constraint
  sets (would need tile→team ownership; rows reuse the pattern idiom).
  AMENDED (pre-ship review): mayCall governs EXTERNAL reach only — same-
  scope targets (an app's own res:<scope>/* and intra-app calls) are exempt,
  because the obvious org row {tiles:"*", mayCall:["apps/o/x/*"]} silently
  severed every covered tile from its own database (a scope is one trust
  unit, ND5). Deny kinds apply regardless. Pending requests a ceiling makes
  unapprovable are annotated `blocked` so UIs don't offer a dead approve.
  AMENDED 2026-07-12 (code:reader regression): reserved CAPABILITY targets
  must be classified explicitly, never left to the mayCall path-matcher —
  bare `code` (whole-workspace source read, owner-level) joins xbin/xbin:*
  under the xbin-caps class; `code:<comp>` is governed like calling that
  component (same-scope exempt + mayCall on the component path). The suite
  missed it because testBroker ran without a user store while prod always
  has one, so every ceiling path was dormant in tests — testBroker now
  attaches an empty store, so all broker tests run the ceiling like prod.
- **D21 — Org admins are security-capped, and live in chrome, not the admin
  tile.** Org admins manage their org (name, members, co-admins, base
  permission, teams, per-tile access entries — org-clamped) but NOT the
  workspace-security knobs: org create/delete, policy rows, team
  termApi/termNet. They act as signed-in humans (cookie principal); a frame
  principal never inherits the driving user's org-adminship. Their UI surface
  is the SHELL's per-tile ⚙ access panel (workspace chrome, raw fetch = the
  human) — deliberately NOT the admin tile, because granting a non-ws-admin
  read on tiles/admin would mint them its frame token and thereby the tile's
  own xbin capabilities (the same reason bx-tile-admin uses raw fetch). Org
  admins get implicit terminal+create on org tiles (equivalent power to their
  ACL-editing rights, with less friction; explicit rows still show in
  /access provenance). IMPLEMENTED for real delegation: the shell's
  "orgs & teams" popover (bx-org-admin, chrome/raw-fetch like bx-tile-admin)
  gives org admins members/teams/base editing without bx; whoami's driving-
  user view on element principals is trust-scoped (identity only → own-org
  slice for org tiles → full list for xbin-capable tiles) so a low-trust
  tile can't harvest memberships — an xbin-caps policy deny downgrades the
  view with the capability.
- **D12 — Playwright e2e only JS tooling, dev-side only.**
- **Nested-frame reload targeting** — longest-prefix match, most-specific frame only.
- **Reserved namespace** — component id `xbin`; top-level `vendor`, `data`,
  `.xbin`, `home`; URL prefixes `/c/ /api/ /ws/ /vendor/ /healthz`; since
  ingress: `ingress` (the public-caller From identity) and `runtime` (the
  builtin ingress source).

### Ingress (2026-07-12, from `plans/ingress.md` — implemented)

- **ING-1 — `exposes` manifest section + config-carrying bindings.** A third
  direction on the binding graph: `exposes` declares endpoints offered to
  the OUTSIDE (`http` with a public-paths allowlist; `stream` with
  proto/port); the owner binds each slot to an ingress source (`runtime` or
  a terminator tile), and the binding CARRIES the route config
  (`BindRef{ref, host|zone|listen}` — bare refs still marshal as plain
  strings, full back-compat). Unexposed/unbound = unreachable (today's
  default-deny preserved). Declaring is agent-writable and inert; binding
  is admin-only.
- **ING-2 — hostname authority at bind: exact host or delegated zone.** An
  http binding grants either one exact hostname or a wildcard zone
  (`*.sites.example.com`) within which the tile self-registers concrete
  hosts at runtime (`PUT /ingress-hosts`, self-scoped like iface-instances,
  refused outside the zone, conflict-checked). The owner draws the boundary
  once; no per-host approval queue; a tile can never claim
  `bank.example.com`.
- **ING-3 — two HTTP terminators, one route table.** A minimal builtin
  second listener in xbind (`--ingress-listen`, BYO/no TLS — for
  Tailscale/LB/dev; public traffic never shares the console listener) and
  the Traefik builtin tile (ACME/Let's Encrypt TLS in a sandboxed tile;
  certs in its own resource — ACME never in the daemon). Both consult the
  same broker-computed routes; a terminator tile hands requests back on a
  per-tile forward unix socket (reached via a relay gateway forward —
  possession of the socket IS the attribution) and can only route hosts
  bound through it.
- **ING-4 — L4 = userspace relay first; the egress relay is also the
  inbound door.** A tile's gVisor egress stack doubles as its ingress path:
  `relay.DialIn` dials from the host side into the netns over the existing
  TUN (source = the gateway IP) — no setns, no extra fds, no privilege. A
  bound stream expose forces the TUN+relay plumbing with a DENY-ALL egress
  policy (and no DNS) for ingress-only tiles. Host listeners (tcp splice /
  udp idle-expiring sessions) reconcile against bindings; unbinding severs
  live flows. Kernel veth/DNAT fast-path deferred. Non-isolated tiers dial
  127.0.0.1 (backends live on the host there).
- **ING-5 — the `ingress` principal is structural.** Anonymous public
  traffic enters only on the ingress listeners, reaches exactly the one
  bound tile, only its declared public paths (path-cleaned before matching),
  with all inbound `X-XBin-*` stripped, the workspace session cookie
  removed, `X-XBin-From: ingress` + `X-XBin-Ingress-Host` injected, and no
  route to `/api/xbin/*` or siblings. `ingress`/`runtime` are reserved
  component names so the identity can't be spoofed. A new policy-ceiling
  deny kind `ingress` makes tiles unpublishable (refused at bind AND inert
  at evaluation).
- **ING-6 — net-tile inbound = `lan-ingress` links; hairpin =
  split-horizon.** A service tile binds a `lan-ingress` interface to a
  router/VPN provider tile and gets a second addressed TUN leg
  (10.43/16 /30s, the inbound twin of the 10.42/16 egress splice links);
  the provider routes to it (an L3 link — the provider is the filter, it
  holds cap:net-admin). Published hostnames resolve inside egress relays to
  a hairpin VIP (10.0.2.4) whose flows short-circuit into the ingress path
  (same anonymous principal) — never a real out-and-back; no-egress tiles
  get no hairpin. Direct tile→tile TCP is a `stream` interface bound to a
  sibling's exposed slot (`provider#slot` → `XBIN_IFACE_<slot>_ADDR` via a
  per-slot gateway forward), so intra-workspace consumers skip ingress
  entirely.

---

## Accepted trade-offs (recorded, not blocking)

- **Tier 1 identity is soft** (same-uid `/proc` token theft possible) until ND1
  lands. The model is right from day one; the floor hardens at tier 2. Do not market
  tier 1 as element isolation.
- **Browser plane: attribution, not isolation.** Same-origin elements share the JS
  realm's ambient powers (DOM of embedding page, storage). Frame tokens make RBAC
  meaningful; subdomain-per-scope (phase 5) makes it enforced.
- **D11 — xbind restart kills terminal sessions**; `tmux` inside is the workaround.
- **In-memory bus, at-most-once**; durability is the subscribing app's job.
- **Terminal Landlock read guard MUST handle `LANDLOCK_ACCESS_FS_REFER`** — on an
  ABI-2+ kernel (5.19+), enforcing *any* Landlock ruleset denies reparenting
  (cross-directory `rename`/`link`) with `EXDEV` unless REFER is handled AND
  granted on both source and destination, regardless of what the ruleset
  otherwise restricts. The read guard handling only `READ_FILE` silently broke
  `apt` (its `partial/ → parent` rename) and every tool that moves a file
  across directories — misdiagnosed for three commits as a fuse-overlayfs
  cross-layer rename (it fails identically on tmpfs; the overlay was never the
  cause). Fix: handle REFER and grant it on the same hierarchies as READ_FILE
  (access-neutral, so no escalation; secrets stay ungranted). Do NOT drop REFER
  from the read guard's access mask. (`internal/sandbox/landlock_linux.go`,
  TestReadGuardKernelInstall; docs/isolation.md.)
- **Terminal resource-mount (resenc) names can appear in `mount`** — the tile
  terminal binds the workspace with a RECURSIVE bind, which is the only option:
  a rootless userns locks every inherited mount, so the resenc gocryptfs
  submounts can be neither unmounted from inside (`umount2`→EINVAL) NOR excluded
  by a non-recursive bind (a non-recursive bind of a subtree with locked
  children →EINVAL — same lock, other direction; verified on the live kernel).
  Two prior attempts to hide the names — a post-clone detach (b7520a9, silent
  no-op) and a non-recursive bind (76d399b) — the latter **broke terminal
  startup entirely** on any workspace with encrypted resources (EINVAL on the
  workspace bind → sandbox-init exit 127). Reverted to recursive. Contents stay
  masked; the names are a benign disclosure (a terminal can already `ls` every
  tile). **Do not re-try NoRec/detach.** Actually removing the names needs
  resenc storage relocated outside the workspace tree — a separate change, not
  yet done. (docs/isolation.md; related lock: D18.)

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
  (`xbin.bus.on`), backends publish + use cron for reactions. Documented in
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
  (symlinked from `.xbin/run`) when the workspace path is deep.
- **`xbind init` is also auto-init**: an empty bind mount initializes on
  first boot, per deployment.md.

## Residual risks (watching, no action now)

- Cold-cache `go build` latency for dep-heavy components — shared GOMODCACHE + image
  pre-warm; shared build daemon only if it hurts.
- Watch-count growth on monorepo-scale trees — per-dir watches fine to ~10⁴ dirs.
- fsnotify drift on macOS dev hosts — weekly macOS CI job.
- Manual vendored-ESM upgrades — fine at 2 deps; growth would demand an import-map
  generator, not a bundler.
- Frame-token UX friction: element authors must use `xbin.fetch()` (or copy the
  header) for cross-element calls; raw `fetch` to a sibling 403s. Mitigate with a
  crisp error body pointing at the docs.

## Lifecycle & backup (LC-*, see plans/lifecycle.md)

- **LC-1** — component lifecycle (`disabled`/`offloaded`/`offloaded-full`) in
  `WorkspaceManifest.Lifecycle`; absent = enabled; owner-only; gated at the proxy
  + `runner.Ensure`; still listed (stub for `-full`).
- **LC-2** — per-component backup scope = **data + source + terminal-env layer**;
  component env layer excluded (rebuilt from `setup`); vault excluded by default.
  sqlite checkpointed.
- **LC-3** — `archive` is an interface kind; the archiver tile *provides* an HTTP
  tar-in / versions-and-file-out contract; **xbind is the client**; owner binds
  an archiver (workspace default + per-component override), no component decl.
- **LC-4** — offload/restore/scheduled+manual backup share the one archive path;
  two offload depths (data, or data+source+term-env).
- **LC-5** — backups schedule on the existing cron engine as owner jobs; retention
  prunes versions.
