# XBin — Decision Log

> Status: **live** — the decision log; every non-obvious choice gets an entry here.

Status meanings:
- **NEEDS CALL** — blocks or shapes early work; want an explicit decision.
- **DEFAULT SET** — a default is picked and the plans assume it; veto if wrong.
- **ACCEPTED TRADE-OFF** — known wart, recorded so it's deliberate.
- **RESOLVED** — decided (date, decider).

---

## Resolved (2026-07-02, magik6k)

- **D10 — Repo & license**: `github.com/xbin-dev/xbin`, images at
  `ghcr.io/xbin-dev/xbin`, **dual-licensed MIT + Apache-2.0** (Rust-style,
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
  **AMENDED (reversed): each component now IS its own git repo**
  (`broker.EnsureComponentRepos`; the workspace remains a repo too). Per-component
  repos turned out to be what makes clone/import/templates/backups diffable and
  independently versioned; the embedded-repo concerns were handled by keeping the
  workspace repo ignoring component subtrees. History/diffs come from each
  component's own repo, not `git -- <path>` on the workspace.
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
  the plane users actually build in. *Amended by ND8: the token is now the tile's
  ONLY credential and cookie-without-token from a tile context is dropped, not
  honored.*
- **ND11 — Tile popups are a per-tile grant: `cap:open-links` →
  `allow-popups allow-popups-to-escape-sandbox` (2026-09-08).** ND8's sandbox
  (ND10 included) dropped every `<a target="_blank">` and `window.open` in
  every tile — silently, including the shipped tiles' own `/docs/…` links
  and the agent template's markdown links. `allow-popups` alone would not
  do: a popup inherits the sandbox and any external site breaks; the useful
  pair is `allow-popups allow-popups-to-escape-sandbox`, and with it a tile
  opens a fully privileged, cookie-bearing top-level page at a URL it chose
  — the look-alike-page primitive. That is the higher-severity case ND10's
  rationale reserved a grant for (a download is browser-mediated; this is
  not), so it is a **reserved, admin-approved capability**:
  `uses {target:"cap:open-links", role:"writer"}`, never same-scope
  auto-granted, delegable to org admins only through a `cap:open-links`
  allowance, stripped by the `xbin-caps` deny class. Enforced in BOTH
  browser layers from ONE source: the broker maps the grant to the tokens
  (`SandboxTokensFor`), the server composes the CSP `sandbox` header per
  document (injected and `inject:false`, so direct-tab opens of `/c/<tile>/`
  match) and reports the extras on `/components`, and `bx-frame` appends
  exactly what it was sent — it keeps no token list of its own. Frontend-
  only: no backend restart; the `grants` event makes `bx-frame` re-key its
  iframe, because a changed `sandbox` attribute applies only to the next
  navigation. Never COOP on the tile document — a sandboxed-origin top-level
  response with COOP other than unsafe-none is a network error per the HTML
  spec — so opener severing lives on the targets: chrome pages (already)
  and `/docs/` send COOP same-origin, authors add `rel="noopener"`, and
  Chromium credentialless frames force noopener on popups anyway. Shipped
  tiles (admin, apidocs, welcome) declare it and the template workspace
  pre-approves it; the agent template declares it (pending on
  instantiation); existing workspaces update the tiles and approve three
  rows by hand — a silent backfilled grant is what this decision avoids.
  The injected client warns once on a blocked `target=_blank` click,
  naming the grant. Rejected: global enablement (ND10's move — the
  severity differs), a manifest self-flag (not a boundary), a
  shell-relayed `xbin:open` with a confirm dialog (works without loosening
  the sandbox, but `window.open` stays dead and activation transfer across
  the frame boundary is browser-dependent — kept as the fallback design),
  `allow-popups` alone, COOP on the tile document. Follow-up: a per-link
  confirm relay for ungranted tiles if it turns out wanted.
- **ND10 — Tile downloads: `allow-downloads` for every sandboxed frame
  (2026-08-24).** ND8's token list omitted `allow-downloads`, so any tile-
  initiated download — blob + `<a download>.click()` or a
  `Content-Disposition: attachment` navigation — was silently blocked by
  the browser (undocumented; it broke the admin tile's restore-one-file
  flow). Now every sandboxed tile carries `allow-downloads`, in BOTH layers
  (the iframe `sandbox` attribute and the CSP `sandbox` header — browsers
  intersect them), unconditionally. Rationale: a download crosses no
  workspace/session/tile boundary — the residual risk is an unsolicited
  save prompt, and the browser's own download UI is the consent surface;
  popups and top-navigation stay blocked. *Amended by ND11: popups are now
  a per-tile grant (`cap:open-links`); top-navigation stays blocked.*
  Rejected: a self-declared
  manifest flag (not a security boundary — the tile author writes the
  manifest — so it buys only friction), an owner-approved capability grant
  (invents frontend-grant machinery for a low-severity, browser-mediated
  permission), and a shell-relayed `xbin:download` postMessage (unneeded —
  `allow-downloads` is supported by every current engine; the bx-spawn
  relay remains the fallback design if that ever changes). Client sugar:
  `xbin.download(name, data)` (blob hand-off) and `xbin.url(path)` (a
  same-host URL carrying the current frame token as `?frame=` — the only
  way a tag-driven request authenticates; xbind already accepted `?frame=`
  on every route and strips it before proxying, and tile JS could already
  read its own token, so no new exposure).
- **ND9 — Warm-IP gate on the /c/ subresource exception + trusted-proxy IP
  attribution (2026-08-14).** ND8's Fetch-Metadata fingerprint is client-
  spoofable by construction (any non-browser client can set the headers), so
  the credential-less `/c/` subresource read now also requires a
  **recently-authenticated source IP**: any successful auth warms the client
  IP for 1 h (sliding), and the exception serves only warm IPs. Rationale:
  the threat that matters is drive-by internet scanners extracting tile
  source en masse — they have no login, so they 401 — while browsers are
  unaffected because a tile's subresource loads always follow its
  authenticated document load (cookie or `?frame=` bootstrap) from the same
  IP, and open tiles renew frame tokens every few minutes. Residual,
  accepted: a client sharing an egress IP with a signed-in session (NAT/VPN
  exit) or holding any account passes — tile source stays non-secret (D30).
  Alternatives rejected: service workers (impossible — SW registration needs
  a non-opaque origin; allowing it voids the sandbox), token-in-path asset
  URLs via injected `<base href>` (works, but forces a relative-URLs-only
  authoring rule, kills deliberate cross-tile tag-loads, and turns source
  into bearer-URL-shareable — a breaking redesign kept in reserve), a
  per-tile/workspace kill switch (a knob nobody needs yet — the gate is
  compatible with every real browser flow). Two review-hardened details:
  the warm window renews only on REAL auths — a credential-less gate check
  never slides it (else one login would keep an egress IP warm forever for
  anyone polling /c/, even after every session from it was revoked; frame-
  token renewals give browsers fresh warmth anyway) — and, for attribution
  behind a reverse proxy, `X-Forwarded-For` is honored **only** from
  `--trusted-proxies` peers *and read at the rightmost untrusted hop* (the
  address the appending proxy vouched for; the leftmost hop is client-
  supplied, so reading it would hand the throttle-bypass spoof right back
  to anyone behind the proxy). The same resolver feeds per-session IP
  records, surfaced in the new `GET /api/xbin/sessions` + admin-console
  sessions tab so an operator can *see* what the gate believes.
- **ND8 — Browser-plane isolation via sandboxed tile frames (2026-08-04).**
  Non-chrome tile documents run in an opaque origin (`sandbox` iframe attr +
  CSP `sandbox` header, so direct-tab opens are confined too): no DOM access
  either way, no storage/cookies/SW; the frame token alone authenticates the
  tile (cookie-less renewal included). *Amended by ND10: the sandbox now
  also carries `allow-downloads`. Amended by ND11: a tile granted
  `cap:open-links` also carries `allow-popups allow-popups-to-escape-
  sandbox`, in both layers.* Server-side, a Fetch-Metadata gate
  (`Sec-Fetch-Site: cross-site` on non-navigations; non-GET navigations to
  `/api/*`/`/ws/*`) drops the session cookie out of tile contexts —
  unforgeable in both directions — so a hostile tile omitting its token can't
  ride the ambient human session.   `/c/<tile>/` subresources authorize by
  the opaque-origin Fetch-Metadata fingerprint (cross-site +
  script/style/image/font destinations; sandboxed frames strip cookies AND
  the Referer, so that's the only signal — unforgeable from a sandbox,
  spoofable by non-browser clients, so tile source is treated as
  non-secret). `Access-Control-Allow-Origin: null` re-enables tile fetch()
  (opaque-origin requests are CORS-blocked otherwise). Humans act from **chrome**:
  root/shell plus manifest `chrome: true` (host-set only; the create APIs
  never write it) — tiles/organisations migrated there. `<iframe
  credentialless>` layered on where supported (Chromium), with a bootstrap
  `?frame=` token in the iframe URL minted by the embedding chrome.
  Alternatives rejected: subdomains/extra ports (deployment constraint:
  single origin, `127.0.0.1` dev), CHIPS/partitioned cookies (per-top-level-
  site only, needs Secure), Origin-Agent-Cluster (process hint, not a
  boundary), fenced frames (ads-only, no postMessage), guardian service
  workers (evadable by the tile itself), credentialless-only (Chromium-only,
  per-top-level-document jar shares across tiles). Residual: same-origin
  tiles share a renderer process; a browser exploit still crosses —
  per-origin process isolation remains the subdomain roadmap. BREAKING: tiles
  lose localStorage/IDB/cookies and raw-cookie human identity inside frames
  (docs/changes/2026-08-04-tile-frontend-isolation.md).
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
  `{tiles, deny[net|gpu|xbin-caps|ingress], mayCall[]}` at workspace + org level
  (the `ingress` deny kind was added with ING-5 — makes matching tiles
  unpublishable);
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
- **Browser plane: enforced isolation as of ND8 (2026-08-04).** ~~Attribution,
  not isolation~~ — same-origin elements no longer share the JS realm's
  ambient powers: tile frames are sandboxed opaque origins (no parent DOM, no
  storage) and the ambient cookie is dropped out of tile contexts by the
  Fetch-Metadata gate. The remaining soft spot is narrower: tiles share a
  renderer *process*, so a browser exploit (not tile JS) still crosses —
  subdomain-per-scope (phase 5) remains the fix for that.
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

- **D23 — Creator sharing (reserved 2026-07, never shipped as such).**
  `plans/user-org-ux.md` reserved this id for letting a tile's creator share
  it without an admin. The ownership rewrite (D24–D28) made that an
  *ownership right* instead — a tile's user-owner manages its ACL — so no
  separate mechanism landed. Kept so the citations resolve.

- **D24 — Component ownership (NPM-style), replacing positional org paths.**
  (2026-08-02) Every component may have an owner — `user:<id>` or `org:<id>` —
  stored in the xbind-owned users store (`data/users.json` `owners`), never in
  the workspace (a tile terminal must not edit its own ownership). Absent =
  workspace-owned. Assigned at every creating entry point (create/clone/
  git-import/builtin-import/template-new; humans default to user-owned,
  admins/automation to workspace-owned) and transferable (`POST /owner`;
  owner→their orgs, owning-org admins within/between their orgs, ws-admin
  anywhere). A user-owner holds implicit terminal and manages the tile's ACL —
  subsumes the D16 creator auto-grant and the "creator sharing" question.
  Supersedes D19's `o/<org>/` positional binding; the `o`/`u` path
  reservations are dropped. Rationale: plans/ownership.md.
- **D25 — Flat org roles; teams removed.** (2026-08-02) Org membership is
  `{id, level: read|write|terminal, create, admin}` applied org-wide to
  org-OWNED tiles (admins implicitly terminal); `create` gates create-as-org;
  `admin` is org management. Sharing beyond membership is per-tile exact ACL
  entries (`user:` or `org:` → level, `org.Tiles` mirroring `user.Tiles`).
  Org policy-ceiling rows key off OWNERSHIP, not paths. Supersedes D19's
  team semantics; D20 ceilings and D21's "element principals never manage
  orgs / humans approve" rules carry forward unchanged.
- **D26 — Org allowances: ws-admin-delegated approval, one floor.**
  (2026-08-02) A per-org allowance (target patterns: res:/gpu:/cap:/net:…/
  iface:/ingress:host|zone|listen/tile:) lets the org's ADMINS approve
  grants and bindings on org-OWNED tiles themselves — anything is delegable
  (net:host, cap:containers, publication) at the ws-admin's discretion,
  EXCEPT the `xbin`/`xbin:*` capability family: an element granted xbin@admin
  IS a workspace admin (broker.IsAdmin), so delegating it would make org
  admins ws-admins transitively — rejected at write AND ignored at
  evaluation. Intra-org wiring (both grant endpoints owned by the same org)
  is org-admin approvable with no allowance. Ceilings still evaluate on
  every approval — deny beats allow. Revokes/unbinds are always allowed for
  the owning org's admins (narrowing is safe).
- **D27 — defaultTiles: workspace-level visibility for every user.**
  (2026-08-02) A ws-admin-managed pattern→level map applied to all users
  (scaffold: welcome/apidocs → read) — how non-admins see anything on first
  login without per-user grants. Humans only; elements stay grant-governed.
- **D28 — Permission sets: reusable org-permission bundles by reference.**
  (2026-08-02) Named `{allow, policy, termApi, termNet}` bundles attached to
  orgs via `sets: […]` (multiple per org). Effective allowance = ∪(sets) ∪
  org extras; ceiling rows compose restrictively (a set can impose fleet-wide
  denies); term flags confer to members of attached orgs (replacing the
  group mechanism teams provided). ws-admin-only; deleting an attached set
  is refused (detach first). Multi-org management = edit one set.

- **D22 — Invite tokens: admin-minted credential delivery, no self-signup.**
  (2026-08-02) `POST /users` without a password creates a credential-less
  account and mints a single-use, 72h invite link (`/login?invite=…`); the
  invitee sets their own password on a themed page and is signed in. Tokens
  are 24 random bytes, sha256-hashed at rest; re-minting or redemption
  invalidates them; redemption is login-throttled and enforces the password
  floor. There is NO self-registration surface — accounts only come from
  admins (manual add or invite). The account row stays credential-agnostic on
  purpose: future enterprise SSO/OIDC (company-wide workspaces) binds an IdP
  identity to the same User row under the same admin-decides rule, rather
  than introducing a second account model.

- **D29 — Backends always get the driving human attributed.** (2026-08-02)
  On every proxied component call with a signed-in user behind it — direct,
  or riding the tile's own frontend (frame token) or terminal — xbind
  injects `X-XBin-User: <id>` and `X-XBin-User-Level: <level on the
  callee>`. Absent for automation/cron/bootstrap-token. Rationale: a frame
  call runs at the tile's full self-role, so `read` level was effectively
  "fully drive the tile" with the backend unable to tell Pat from Juno;
  attribution lets tiles gate in-app (SDK: `Caller(r).UserCanWrite()`).
  Inbound X-XBin-* stripping is unchanged, so the headers stay trustworthy.
- **D30 — Vault values are readable by the tile's BACKEND only.** (2026-08-02)
  `GET /vault/<c>/<key>` requires the instance token: admins and the tile's
  own TERMINALS are write-only managers (list/set/rotate/delete), and the
  tile's FRONTEND (frame token) cannot reach the vault API at all. Closes
  the hole where anyone who could open or shell a tile could read its
  secrets — org-conferred terminal on a credential-bearing tile no longer
  leaks keys. Backends fetch secrets at runtime as before.
- **D31 — Org tiles are governed by the org; exact entries are
  authoritative.** (2026-08-02) Access resolution: admin/user-owner/org-admin
  shortcut (terminal) → an EXACT per-user entry sets the level outright
  (down as well as up; `none` = explicit exclusion) → otherwise union, but
  personal pattern entries and workspace defaultTiles apply to
  workspace/user-owned tiles ONLY — "your perms on an org tile are your
  perms in the org". Consequences: an org can carve one sensitive tile out
  (exact override/none), broad workspace patterns can't leak into org
  property, and "set this user's level to X" actually results in X.
  BREAKING vs the old union-only semantics (migration note).
- **D32 — Allowance grammar: per-class validation + role/provider/instance
  granularity.** (2026-08-02) Entries validate per class at write time (a
  dead entry is refused, not stored to lie in resolvedAllow; cap:xbin*
  spellings refused). New qualifiers: `res:<glob>@<role>` /
  `tile:<pat>@<role>` cap the delegable role (bare = any role);
  `iface:<svc>@<tile-glob>[#<inst-glob>]` pins an interface allowance to a
  provider tile and instance — "the dev instance only, for this org" is
  expressible; binding targets normalize with provider+instance to match.
  matchTile additionally treats a mid-string `*` as a glob everywhere
  (previously a silent never-match).
- **D33 — Provider-side consent, and nobody is blind.** (2026-08-02)
  Approval runs on BOTH edges: the consumer org (D26, within allowance) and
  the org that OWNS the target property — its admins may approve (sharing
  your own property is an ownership right, no allowance needed) and revoke
  at any time; same for bindings when every ref is their provider.
  Visibility: org-scoped /grants and /bindings include provider-direction
  rows; every signed-in user sees their own writable tiles' rows and
  pendings ("mine") with who-can-approve hints; a `grants` event fires when
  NEW pending requests appear (watch-loop diff) so approver UIs and the
  shell badge update live. Grant rows record approvedBy/approvedAt and the
  audit log carries the full triple. Lifecycle joins the ownership rights
  (owner/org-admin may disable/enable their tiles); org policy rows are
  org-admin-readable.

- **D34 — Account disable (ws-admin) and org member suspension (org
  admins).** (2026-08-02) `User.Disabled`: login, sessions, frame/terminal
  tokens and invite redemption all refuse while set, but every ACL row,
  membership and owned tile stays — re-enabling restores the account exactly
  (contractor pause, incident response). Lockout-guarded: not yourself, not
  the last enabled admin. `Member.Suspended` is the org-scoped little
  sibling, set by org admins through normal member editing: a suspended
  membership confers NOTHING (org-tile level, shares, create, adminship,
  set-conferred term flags) but stays listed for one-click reinstatement —
  "org admins get org-level moderation, ws-admins get the account switch."
- **D35 — Hostname-granular egress + carve-out allowances.** (2026-08-02)
  The `net` binding vocabulary gains FILTERED internet:
  `internet:<host|ip|cidr>[:port][,…]` — enforced by the existing userspace
  relay via DNS pinning (the relay already terminates the sandbox's :53; it
  now records name→address pins from the responses it forwards and admits
  flows to pinned PUBLIC addresses the policy's host rules allow — private
  answers never pin, so DNS rebinding can't reach the LAN). Bindings name
  concrete destinations; allowance entries do the granting:
  `net:internet:<host-glob|cidr>[:port]` with hostname globs and CIDR
  CONTAINMENT — an org allowed `net:lan:10.0.0.0/8` may approve any
  narrower `lan:10.x.y.z/nn` binding ("carve a subnet out of the grant"),
  and unfiltered `net:internet` subsumes every filter. Complex filtering
  (L7, rotating CDNs, allowlist management) stays in provider tiles — a
  filtering net-provider is the escape hatch, not more grammar. Org-wide
  resource limits and per-tile memory caps were deliberately deferred.
- **D36 — Human access requests.** (2026-08-02) The people-plane mirror of
  the elements' pending-grant queue: any signed-in user files (tile, wanted
  level, note ≤200 chars, ≤20 pending, dedupe per user+tile); the tile's
  manager set (user-owner / owning-org admins / ws-admin — the D24 sharing
  gate) approves into an exact ACL entry (authoritative, D31) or dismisses;
  requesters withdraw. A signed-in human navigating to an unreadable tile
  now gets a request-access page naming the owner instead of a bare 403 —
  the tile's existence isn't secret to someone holding its link. Requests
  ride `users` events (badges/queues update live) and die with the user.

- **D37 — Shared screens: a workspace default + org screens.** (2026-08-02)
  Personal screens stay per-user prefs. New workspace layer
  (data/screens.json): a ws-admin-curated DEFAULT screen new users seed
  from (replacing hand-editing root/index.html; the <bx-frame> pins remain
  the fallback), and ORG SCREENS — layouts owned by an org, tabs for every
  member, with an `edit` knob (admins | write | members) choosing who may
  rearrange. Creation/rename/knob/delete are org-admin acts; tile edits
  follow the knob; suspended members see nothing. Layout JSON is opaque to
  the server (64K cap); changes publish `users` events so shells refresh.
- **D38 — Self-service password change + org-admin reset-by-link.**
  (2026-08-02) `POST /account/password {current,new}`: a signed-in user
  rotates their own credential after proving the current one (admins keep
  the reset flows; the bootstrap token has no password). And the family
  story's last admin-password chore: an ORG ADMIN may re-mint an invite
  link (`POST /users/<id>/invite`) for a NON-ADMIN member of their org —
  delegated reset-by-link; resetting an admin user stays ws-admin-only.

- **D39 — Transfers are first-class: create-bound receive, preview, active
  re-evaluation.** (2026-08-02, spec: plans/transfer.md) Transfer authz
  splits into GIVE (unchanged D24: ws-admin / user-owner / owning-org
  admin) × RECEIVE, where receiving INTO an org requires that org's
  **Create** knob — "may I transfer into X" ≡ "may I create in X"
  (BREAKING vs membership-only; migration note). `GET /owner/preview`
  reports impacts before every confirm: the caller's own level
  before/after (owner-terminal can drop to org-member level, D31),
  bindings/grants that die under the new owner's ceilings, and
  approval-plane shifts. The transfer itself then keeps state honest:
  slots whose every ref is ceiling-dead are UNBOUND (not left to
  silently resurrect), spawn-materialized access re-materializes via
  backend restarts (the tile + displaced net providers), and grant rows
  stay stored (inert-but-visible is the auditable choice). UIs confirm
  with the report; bx prompts with it.

- **D40 — Restricted terminals mount an allow-list view.** (2026-08-02)
  Non-admin terminals no longer get the whole workspace bound read-only with
  per-tile tmpfs masks (deny-list) — production showed two leaks: masked
  tiles' NAMES enumerate in `ls`/mountinfo, and the real root files are
  readable, `cat ../../xbin.json` handing any one-tile user the entire
  grants/bindings topology incl. public hostnames. Instead xbind stages a
  per-session VIEW dir (.xbin/term/view-*, removed on close) holding
  redacted root files — xbin.json filtered to rows whose every referenced
  component is readable, go.work filtered to readable modules (builds must
  not chase absent dirs), AGENTS.md/.gitignore copies, a CLAUDE.md symlink,
  an empty .xbin marker (bx root detection), and pre-created mountpoints
  (the view mounts read-only) — binds it at the workspace root, then binds
  ONLY the readable components (RO), the session's own component (RW), and
  the user's $HOME (RW). No masks; .xbin/data/homes and the resenc
  mount-table names simply don't exist inside. Admin terminals keep the
  full-view+masks plan (their view is the workspace, and the recursive-bind
  lock constraint documented in sandbox.Bind still applies there). The old
  HiddenTiles deny-list path remains as the fallback when no TermView is
  wired (tests, exotic embeddings).

- **D41 — Essential-builtin backfill + org-terminator ingress consent.**
  (2026-08-02) Two upgrade/delegation gaps: (a) workspaces created before a
  builtin tile existed never got it, though newer chrome targets it (the
  shell's ⚑ opens tiles/organisations) — boot now backfills ESSENTIAL
  scaffold units, ledgered in data/backfills.json so a deliberate delete
  sticks (present units are ledgered untouched); `bx builtin updates`
  lists missing essentials and `update` installs them (bare names
  resolve). Only workspaces already running a defaults regime get the new
  tile added to defaultTiles — empty defaults stay empty. (b) Ingress
  consent follows terminator OWNERSHIP: an ingress host/zone routed
  through a terminator tile owned by an org the approver administers is
  consented without an allowance — org property flowing through org
  property, on both the caller side (org owns publisher + terminator) and
  the provider side (the terminator's org consents to outside publishers,
  mirroring D33). Host ports (ingress:listen:) and the builtin runtime
  listener remain workspace infrastructure — allowance or ws-admin. The
  binding normalizer now pairs each target with its ref explicitly.

- **D42 — Hidden tiles.** (2026-08-03) A lifecycle state `hidden` =
  disabled (identical enforcement: backend stopped, spawn refused) + kept
  out of sidebars and listings until unhidden. Same owner-plane gate as
  the rest of lifecycle (D24). UIs filter behind show-hidden toggles and
  badge revealed rows; screens are left alone (a placed hidden tile
  renders its disabled state — hiding is about listings, not layouts).
  Refused while offloaded so the archived-data marker is never clobbered.

- **D43 — Encrypted container stores: gocryptfs single-tenant mode, no
  plaintext opt-out.** (2026-08-03) Podman's layer store structurally
  cannot live on a stock unprivileged gocryptfs mount — the daemon is the
  physical I/O actor, so 0555 layer dirs EACCES its own mkdir, sub-uid
  chowns EPERM, and without allow_other sub-uid processes are
  kernel-refused. A `"plain": true` unencrypted escape hatch was built and
  reverted the same day: an opt-out that silently removes a resource from
  the encrypted/seal plane is a shortcut in security-impactful code, and
  "the store survives a stolen disk" is exactly resenc's contract. The
  real fix ships as a patchset on the pinned gocryptfs
  (hack/gocryptfs-patches/, applied by the build, probed at mount time):
  `-xbin-single-tenant` virtualizes uid/gid/mode/rdev into encrypted
  xattrs (chown/chmod/mknod always succeed and round-trip; whiteouts,
  FIFOs and security.capability included), keeps the cipher tree
  uniformly daemon-owned 0700/0600, skips in-mount permission checks, and
  implies allow_other — sound because a resenc mount is single-tenant by
  construction (it serves exactly one scope's sandboxes, which already
  share the resource rw; the 0700 runtime dir is the visibility
  boundary). The broker requests the mode only for `filesystem` resources
  of scopes holding cap:containers, follows the grant (a policy-ceiling
  strip flips the mount back), and remounts on grant change — same
  on-disk format either way. PreserveOwner is forced off in this mode
  (the daemon must never impersonate callers). Known gap, accepted:
  symlink ownership is not virtualized (user xattrs are forbidden on
  symlinks). Companion (kept from the reverted commit): cap:containers
  sandboxes get a cgroup2 view at /sys/fs/cgroup via cgroupns unshare.

- **D44 — Per-inode cache invalidation: forget on last unlink, distrust
  symlinks.** (2026-08-03) The single-tenant identity/capability caches
  (same-day container-store speed work) were keyed by cipher inode with no
  invalidation — "the daemon is the sole writer" covered mutation but not
  *deletion*: the backing filesystem recycles inode numbers (ext4/xfs
  reuse a freed inode immediately; btrfs never does, which is why no dev
  box reproduced it), so the next occupant of a reused inode wore the dead
  file's cached identity. Two visible casualties, both fatal to execve
  mid-`apt-get`: a fresh `update-alternatives` symlink served as the
  deleted regular file (`which: Permission denied`, dpkg exit 126 — the
  identity clobbers type bits), and a phantom `security.capability` on a
  just-unpacked binary (the cap cache is only seeded by xattr queries, so
  creates never heal it). Mechanism proven with a deterministic
  inode-recycling FUSE passthrough (reusefs) A/B'ing shipped vs fixed
  builds; the full real workload (podman build, ubuntu + 163MB apt
  install on a single-tenant store) runs green on the fix. Chosen fix:
  on losing the last directory entry (unlink, rmdir, replacing rename)
  write a "no identity"/"no capability" tombstone for the inode (matches
  what a cold load would find — plain deletes would leave the same race
  windows but with less obvious semantics); never apply a cached identity
  to a raw symlink (symlinks cannot carry the identity xattr, so any hit
  is definitionally stale); fd-getattr/fd-setattr of unlinked-but-open
  files (nlink==0) read/write the xattr directly and skip the cache, so a
  dying inode number is never re-seeded. Rejected: dropping the caches
  (reintroduces the small-file collapse the caches fixed); trusting
  filesystem generation numbers (not visible through the syscalls the
  daemon can afford per-op).

- **D45 — Kernel writeback cache for single-tenant mounts (go-fuse
  patched).** (2026-08-03) Per-op FUSE round trips dominate container-store
  performance; the daemon-side caches (D43/D44) fixed the read path, but
  every small write still cost a round trip. The kernel's writeback cache
  is the mechanism built for exactly this — batch dirty pages, flush as
  few large WRITEs — and it is sound here for the same reason as the other
  caches: a single-tenant mount's backing tree has exactly one writer, so
  the kernel-cached view cannot go stale. go-fuse has carried
  CAP_WRITEBACK_CACHE for years without an opt-in, so we ship a second
  patchset (hack/gofuse-patches/, applied by build-gocryptfs.sh with a
  local `replace`) adding MountOptions.EnableWritebackCache; gocryptfs
  sets it only under -xbin-single-tenant. Verified: protocol-level 4096:1
  write batching, a cold-remount data-integrity suite (mixed sizes,
  unaligned RMW, appends, truncate-over-dirty-pages, fsync, shared
  writable mmap — which FUSE refuses without writeback and now works,
  fixing e.g. SQLite WAL on resource mounts), the full containerfs
  integration battery, and a real podman build. Measured honestly:
  chunked small writes ~25% faster; open-append-close cycles pay a small
  flush-on-close cost; image COMMIT unchanged (bounded by per-file
  lookup/create/setattr round trips, which no data cache batches — the
  commit lever remains a tmpfs scratch build store, unimplemented).
  Companions on the same sole-writer argument: 60s attr/entry/negative
  kernel cache timeouts, and no-op setattr elision (skip the encrypted
  identity rewrite when chmod/chown changes nothing, as tar extraction
  does constantly).

- **D46 — FUSE round-trip diet default, writeback demoted to opt-in.**
  (2026-08-03) A production build failed with `close()`→EIO right after
  D45's writeback shipped; a full-fidelity repro (real subuid topology,
  the exact Dockerfile, outside every sandbox layer) passed cleanly both
  with and without writeback, so causality is unproven — but unexplained
  EIO on the default path is unacceptable, and writeback (plus the 60s
  timeouts) moved behind `XBIN_GOCRYPTFS_WRITEBACK=1` until the failing
  host's daemon logs settle it. The default path keeps the performance
  goal by removing protocol round trips instead of batching data: an
  opcode census of a real dpkg-through-overlay flow (~230 files) showed
  6.5k LOOKUP / 4.6k GETXATTR / 4.1k SETATTR / 3.6k FLUSH — so:
  FOPEN_NOFLUSH on every open (gocryptfs FLUSH is dup+close, a no-op;
  kernel 5.16+ skips the op, older kernels ignore the bit), and
  FUSE_HANDLE_KILLPRIV (second go-fuse opt-in patch) so the kernel stops
  the per-write security.capability getxattr — with the implied duty
  implemented in stKillPriv: write/truncate clears suid (always) and
  sgid (only with group-exec; bare sgid is mandatory locking), drops
  fscaps, all off the in-memory caches with a one-time backing probe on
  miss. Also: FSYNC now uses the already-open handle (upstream's
  Node.Fsync shadows File.Fsync and re-walks the path per fsync), and
  MaxBackground 12→64. Census after: FLUSH −100%, GETATTR −40%. The
  remaining GETXATTR storm is fuse-overlayfs's own cap queries
  (userspace, cache-served, not removable kernel-side). Not pursued:
  entry-timeout raises on the default path (kept at stock 1s until the
  EIO is understood).

- **D47 — Prebuilt install bundles + arch-parameterized rootfs.**
  (2026-08-08) Every install built from source: podman/docker to compile
  gocryptfs + fuse-overlayfs, plus a multi-minute/multi-GB rootfs build
  (apt, go/node/bun/opencode toolchains, Playwright+Chromium). Added a fast
  path: a **full bundle** per arch — `bin/` (4 native binaries) + `rootfs/`
  (unpacked base) + `sdk/`, one `xbin-<ver>-linux-<arch>.tar.zst` — described
  by `release-manifest.json` {version, baseVersion, variants[]}.
  `install.sh --prebuilt-rootfs[=SPEC]` (or `XBIN_PREBUILT`) downloads the
  manifest, picks the arch variant, sha256-verifies, unpacks, and reuses the
  existing `XBIN_PREBUILT_BIN`/`XBIN_ROOTFS_DIR`/`XBIN_SDK_SRC` path — no
  podman, no Go, no build. Chose a **full** bundle (not rootfs-only: building
  even the binaries needs podman for the two container-built statics) and
  **manifest JSON parsed with awk** in the POSIX installer (no jq/python on
  the target). The rootfs Dockerfile is now **arch-parameterized** (all
  toolchain downloads keyed off `dpkg --print-architecture`, so the same
  Dockerfile builds under `podman --platform linux/amd64|arm64`) — this is
  what makes an **arm64** variant possible at all (it never worked before:
  Go/Node/gh were x64-hardcoded). Publishing is a **manually-run**
  `deploy/publish-release.sh` (build per-arch, `gh release upload`) — not CI,
  to avoid spending CI minutes building multi-GB rootfs on every tag; the
  script cross-builds a foreign arch via `--platform`+qemu (slow) or the
  maintainer runs it natively per arch. `flavor` is kept in the schema so a
  future `slim` (no Chromium) slots in without breaking older installers.
  Bundles are hosted as **GitHub Release assets** on the tag (fits the
  existing tag-based bootstrap; the manifest and tarballs sit together).

- **D48 — Cross-tile change proposals ("code PRs"): read = suggest,
  broker-stored, never auto-applied.** (2026-08-10) A terminal writes only
  its own tile (D17/D40), so agents had no sanctioned way to improve a
  sibling tile they can read. Added a PR system (plans/code-prs.md):
  clone the target's repo out of the RO mount, commit, `git format-patch`,
  `bx code pr <target>` files the series into a broker-owned store
  (`data/prs/<target-key>/<n>/{meta.json,series.mbox}`); the target's own
  plane lists/reviews (`bx code prs`, the terminal window's ⇄ tab), applies
  with `git am --3way` in its OWN mount, and closes merged/rejected with a
  note the author's agent reads. Four sub-decisions, all ratified:
  **(1) format-patch mbox** as the artifact (reviewable text, `git am`
  native, authorship-carrying; bundles/push deferred to a possible
  `refs/xbin/pr/*` smart-HTTP rung), **(2) no new grant — the capability
  to suggest IS the capability to read** (terminal/frame: driving user's
  D40 read; elements: `code[:target]`; filing writes inert queue data and
  escalates nothing, so no owner approval to open), **(3) broker-owned
  `data/prs/`** rather than refs in the target repo (API-only from
  sandboxes, backed up, watcher-invisible, no foreign refs polluting
  component repos), **(4) the name "PR"** (agents already know the
  workflow's semantics — that familiarity is half the feature). The
  target-plane-applies property is enforced by mounts, not convention:
  xbind has no apply endpoint. merged/rejected = target side (own
  principals, write-level users, admins); withdrawn = author; no reopen.
  A `pr` event fans out on open/comment/close, read-filtered per D40 like
  the mounts (server-side filter in /ws/events); shell sidebar/cards badge
  ⇄N and the terminal window carries the review UI. Series capped at
  4 MiB — proposals are diffs, not file transfers.

- **D49 — Builtin-update conflicts travel as PRs (update mode "pr",
  manual propose).** (2026-08-10) The shipped ApplyMerge wrote `<<<<<<<`
  markers into a live tile's working files (watcher reloads a broken tile)
  and recorded base=theirs BEFORE resolution — provenance claimed currency
  over unresolved conflicts. Rather than patch that flow, route it through
  D48: `POST /builtins/update {mode:"pr"}` renders base→theirs (adopted:
  ours→theirs — a path merge-file refuses outright) as a format-patch via
  a temp repo and files it as a change proposal against the tile, carrying
  kind:"builtin-update" + unit id + embed rollup hash + changelog +
  per-file status in the message. The tile's own plane merges with
  `git am --3way` (the install baseline commit supplies base blobs; the
  PR body's clone-first recipe keeps markers out of live files), and
  **provenance records only on merged-close**, hash-guarded so a proposal
  rendered from an older embed can't claim currency after an xbind
  upgrade (it errors; the update stays offered). Filing is **manual** — a
  "Propose as PR" click / `--pr` flag, no auto-filing at boot (no surprise
  PRs). Idempotent per (unit, embed hash); a newer embed auto-withdraws
  the stale open proposal ("superseded"). UI: Propose-as-PR is the primary
  Updates-tab action for conflicted/adopted units; Replace stays for
  clean/discard; merge-file demoted to legacy. Rejecting the PR keeps the
  update offered — refusal is a first-class outcome, not a stuck state.

- **D50 — Template instances share git ancestry with their template repo.**
  (2026-08-11) The fork-upstream model (template remote + fetch/merge,
  templaterepo.go) was wired but broken: instances got a fresh `git init`
  root from EnsureComponentRepos, so the documented merge always refused
  with unrelated histories. Instantiate now SEEDS the instance repo before
  EnsureComponentRepos can touch it: init, fetch the materialized template
  repo's main from its local path, point main at the snapshot without
  touching the working tree (which already holds the CopyTree rewrites),
  then commit the rewrites on top ("instantiate <name> as <path>"). The
  snapshot is the merge base forever after; the accruing one-commit-per-
  version template history (materializeTemplateRepo) does the rest.
  Detection is all local plumbing, no fetches: an instance is behind when
  the template repo's HEAD is not an ancestor of its HEAD, and legacy
  (pre-seeding) when even the template's ROOT commit isn't — surfaced via
  GET /templates/updates, `bx template updates`, and the Tile Manager
  (recipe only; nothing is ever auto-merged — instances are forks, the
  builder picks what to adopt, unlike D48/D49 there is no provenance to
  refresh). Chosen over retrofitting builtin-update tracking onto
  instances (they diverge by design; content-hash provenance would
  misread every divergence as a conflict) and over PR-based delivery
  (D49) — a real git merge with shared ancestry is strictly stronger
  here, and the remote already existed.

- **D51 — SSO sign-in: generic OIDC + GitHub, email-bound to the
  credential-agnostic User row (2026-08-25).** Implements what D22 reserved:
  an IdP identity is just another credential arriving at the same User row.
  Shape: ONE provider per workspace — generic OIDC (code + PKCE; ID token
  verified via JWKS; presets google/keycloak/okta/entra/authentik/custom)
  plus a distinct GitHub OAuth2 path (GitHub has no OIDC — identity is the
  API's verified primary email). Resolution order: User.Email binding
  (new field; unique, lowercased — ids stay the permanent dir-safe keys,
  emails can change) → the admin's domain allow-rule JIT-provisions a
  default-access, credential-less account (ProvisionSSO, sibling of
  UpsertInvited; id folded from the email local-part). Explicitly-unverified
  emails always refuse; the google preset with domains set also requires a
  matching `hd` claim. Storage: SSOConfig (client secret included) lives in
  data/users.json next to tokenLoginDisabled — the VAULT CANNOT hold it (it
  boots sealed; login must work before an admin can unseal — circular), and
  the file already carries the password hashes at the same 0600/masked
  protection. The redirect URI comes from the new --external-url (xbind had
  no public-URL concept; request-derived URIs can't be pre-registered at an
  IdP), which also fixes the printed boot/invite links. Round-trip state
  (state/nonce/PKCE verifier) rides an HMAC-signed 10-min cookie keyed by a
  boot-random secret — no server-side store; a mid-login restart just means
  clicking again. Deps: golang.org/x/oauth2 + coreos/go-oidc/v3
  (user-chosen over hand-rolled stdlib; first outbound HTTP the daemon
  makes). Rejected/deferred: Apple (client secret is an ES256 JWT minted
  from a .p8; private-relay emails defeat the domain rule), reverse-proxy
  header auth (no operator need), passwordLoginDisabled SSO-only mode,
  IdP-group→org mapping, and offboard-on-IdP-removal reconciliation —
  recorded as follow-ups.

- **D52 — Provisioning policy: SSO pre-provisioning, a new-account seed
  (with default org membership), and an org-only tile-creation policy
  (2026-09-04).** Running a workspace on SSO (D51) exposed three gaps: an
  SSO user could only exist by JIT (or by minting an invite nobody would
  use), JIT accounts landed with defaultTiles and nothing else (every row
  hand-edited afterwards), and nothing stopped a domain's worth of new
  users from scattering personal tiles outside the org structure. Shapes
  chosen: (1) `POST /users {sso:true, email}` is the invite flow's
  credential-less row WITHOUT the link (UpsertInvited, no CreateInvite) —
  no new account kind, the email binding is the credential. (2) A
  workspace-level `newUsers` seed (tiles, canCreate, termApi/termNet, orgs
  [{org, level, create}]) lives in users.json next to defaultTiles and is
  COPIED onto every new row — password, invite, SSO-pre-provisioned, and
  JIT alike — as a union with the request. Deliberately a one-shot copy,
  not a live layer: defaultTiles already is the live baseline, and a seed
  that keeps applying would make "I removed that from Jane" impossible.
  Deliberately workspace-wide rather than per-SSO-domain: one concept,
  every creation path, and per-domain rules are a later refinement if a
  workspace ever runs two domains with different roles. (3) `tileCreation:
  any|org-only` binds NON-ADMINS only (user-ratified): under org-only a
  `user:` owner is refused everywhere and an empty owner resolves to the
  single org where the user holds Create (several → must name one; none →
  refused) — so single-org users need no UI change while ambiguity is
  never guessed. The four non-/create entry points (clone, builtin import,
  template new, git import) gained `owner` and route through the same
  resolver, which they previously bypassed with a hardcoded creator-owned
  default. Rejected: role/org-admin in the seed (user-ratified — an
  allow-listed domain must never mint admins; promotion stays manual);
  applying the policy to admins (workspace-owned creation is the admin's
  own housekeeping act); a live "defaults layer" for new users (see above).
  Follow-ups: per-domain seeds; `bx org member add` via the new
  AddOrgMember single-row path; the manager tile's clone/template/import
  tabs get an owner picker (today they rely on the single-org auto-pick).

- **D53 — SSO-driven org membership: group sync with provenance, admins by
  group, single-membership API, sign-in tab (2026-09-05).** Running a
  workspace on SSO (D51/D52) still left org housekeeping manual: the only
  SSO→org link was the global new-account seed, nothing removed access when
  someone left a team, and membership was editable only as a whole list.
  Shapes: (1) IdP-group rules live ON THE ORG (Org.SSOGroups; ws-admin
  write, because rules grant power like sets/allow) — the org card is where
  the members they explain are read; workspace-admin groups live in
  SSOConfig. (2) Provenance is one flag per membership row (Member.Via
  "sso" + ViaGroups): sync re-sets synced rows to the rule's knobs at every
  sign-in and REMOVES them when the group or rule disappears; manual rows
  are never touched and MANUAL WINS when both exist (delete the manual row
  to hand it to sync); detach (via:"") is the only API-writable provenance
  change; whole-list PATCH keeps provenance and ignores the body's. User-
  ratified over join-only: sync is the offboarding. (3) Never on failure: a
  provider that returns no groups (API error, scope not consented, claim
  absent) changes nothing — unknown ≠ empty — and the failure is recorded on
  the user (the console derives the newest one; nothing extra persisted).
  (4) Admin by rule (user-ratified) with three guards: only RoleVia "sso"
  admins are demoted, never the last enabled admin (retried next sign-in),
  every change audited. (5) Group-reading scopes are requested only while
  rules exist (consent churn; Google rejects unknown scopes); Google never
  looks for a claim — Cloud Identity searchDirectGroups is the only source
  (group emails), GitHub = teams as org/slug + orgs, OIDC = configurable
  claim from the ID token then UserInfo. (6) SSO-only mode refuses a CORRECT
  non-admin password in the handler (the page can't know the role; admins
  keep password as break-glass; needs a ready provider; cleared with SSO).
  (7) Sign-in facts (LastLogin/Via, SSOGroups) on the user row — the
  console's never/stale chips and "groups seen" datalist run on them.
  (8) A sign-in sub-tab (daily surface — the users table — separated from
  set-once config), single-membership routes replacing whole-list edits
  everywhere in the UI and bx, native <details> row menus (no dialog
  component), keyed rows. Rejected: a global mapping table (loses the
  members-next-to-rules reading), a live "defaults layer" instead of copy
  semantics, re-stamping hand-promoted admins, GitHub auth-endpoint probing
  in the test (nothing meaningful without a user). Follow-ups: per-domain
  seeds; IdP-driven offboarding beyond groups (SCIM-style deprovisioning);
  persisted auth-event log tab.

- **D54 — Organisation network sets: one rule list = ceiling + allowance +
  default + terminal egress (2026-09-05).** Egress was per-tile only: an org
  admin with `net:internet` bound the internet, `host` was admin-only always,
  the only org-level knob was the binary `deny net` row, terminals were
  hardcoded to `net:internet` behind a boolean `termNet`, and the CIDR/host
  forms had no UI — a startup could not say "devs → 10.42/16, infra → 10/8 +
  host, sales → internet". Shapes: (1) a **NetSet** is a named rule list
  attached to orgs BY REFERENCE (the permission-set precedent); rules are the
  existing `net:` allowance entries without the prefix (`internet`,
  `internet:<host|glob|ip|cidr>[:port]`, `lan:<ip|cidr>[:port]`, `host`,
  `provider:<glob>`) — no new grammar, `parseAllowEntry` validates, the
  relay's existing union of internet/CIDR/pinned-host rules enforces; the only
  new enforcement is one-`*` host globs (+ apex) in the relay's host matcher.
  (2) One list, four meanings, all derived from `users.Ceiling` (the per-tile
  composed policy) and `ResolvedAllow`: the ceiling on org-owned tiles' net
  bindings, the org admins' allowance, the default binding — the builtin
  `org` = the LIVE union, so a fresh org tile that declares `net` has its
  org's reach with no binding row — and the egress of terminals opened on
  org-owned tiles (new scope `org`; the set IS the grant, members need no
  `termNet`, which now governs personal/workspace tiles only). User-ratified:
  terminals ride the org that OWNS THE TILE (network is a property of the
  tile, not the person); auto-bind by default; `host` allowed via a set with
  a loud warning (relaxes D17's "admin-only, always"); no `termNet` needed on
  org tiles. (3) Union, not intersection, across attached sets — sets are
  reach you ADD (a provider-only set therefore airgaps `org`; the label says
  so); `deny net` still beats everything. (4) Refuse at write, inert only
  when stale: an uncovered ref is a 400 naming the set (ws-admins included —
  they widen the set), while a set narrowed or a transfer into a non-
  covering org turns the binding inert with the reason surfaced in
  `/bindings.inert`, `/tile-status`, `bx iface/status/doctor` and every UI;
  `deadSlotReason` previews it on transfer. (5) Same-org provider tiles are
  covered without a `provider:` rule (D26's intra-org spirit); cross-org and
  workspace providers need one. (6) New builtin `none` (anywhere) pins a tile
  offline; `org` on a non-org tile is refused. (7) Set/attachment edits
  restart affected org tiles + their stored providers (the transfer
  mechanism); terminals apply at next spawn. (8) The client never guesses
  network state: the session frame lists the scopes THIS user may pick on
  THIS tile (+ `netNote` for a clamp) and bind options carry server labels;
  one shared browser module (`web/bx-netrules.js`) owns rule parsing/labels
  so the admin console, organisations tile, tile popover and terminal menu
  cannot drift. (9) A separate "network sets" tab from permission sets: sets
  answer "what can this org reach", permission sets "who may approve what".
  Rejected: a separate net-policy-row grammar (D20 rows are restrictive;
  reach is additive), folding reach into permission sets (buries the
  founder's question behind the allowance grammar), per-tile net ACLs (the
  org is the unit), hostname globs in `lan:` and in per-tile bindings (D35
  unchanged), intersection semantics, broker-side caches (`Ceiling` already
  composes under one lock), clamping refused `host` to `internet` (D17's
  clamp-to-none kept; `org` when available). Follow-ups: per-org relay
  metering roll-ups; a "why can't I reach X" explainer in the terminal;
  set-level DNS overrides.

- **D55 — Org screens with explicit save + revisions; one sidebar tree per
  owner with shared folders (2026-09-05).** D37's org screens auto-saved
  every drag through a 500 ms debounce: an accidental nudge on a shared tab
  changed it for the whole org, every drag tick was a file rewrite plus a
  `users` broadcast, two members rearranging at once clobbered each other
  silently, and members could neither park, reorder, fork nor rename an org
  tab. The sidebar grouped each owner section by top-level directory
  (APPS/TILES), which duplicated the owner structure, cost vertical space
  and left an org no way to curate how its tiles are presented. Shapes,
  user-ratified: (1) Grafana-style editing — view mode is read-only for
  everyone; "edit layout" opens a LOCAL DRAFT, and only "Save and update for
  everyone" publishes (or Discard). (2) A per-screen REVISION counting tile
  saves only: a save names the rev it was based on, a stale one is refused
  with 409 carrying the current screen, and a human picks reload / overwrite
  — a plain compare-and-set, no locks. Rename/knob changes are admin-plane
  and never bump the rev, so an admin renaming never conflicts with a
  member's draft. A write WITHOUT rev stays accepted as the legacy overwrite
  so pre-D55 shell copies keep working (non-breaking; the scaffold update
  brings the UX). (3) Drafts are personal state in the layout pref (dirty
  only), survive a reload, and a draft whose screen vanishes (deleted,
  membership lost) is forked into a personal screen — nothing is lost, no
  orphan UI. Org drafts are never pruned client-side: `/components` is
  read-filtered, so a member cannot tell "offloaded" from "unreadable" and
  pruning would delete other members' tiles. (4) The sidebar is ONE TREE PER
  OWNER SECTION, everywhere: curated folders + the remaining readable tiles
  flat at the root, no directory headers; the tree is complete. Shared
  folder sets live in data/screens.json keyed `ws` (ws-admins curate) and
  `org:<id>` (its admins), under the same draft/rev/409 flow; per-user open
  state stays personal; personal top-level folders keep holding anything.
  Curators save the FULL list, never the render-filtered one. (5) Org-tab
  ergonomics are personal state: tab order across both kinds, hidden org
  tabs (restorable from the org section, which always lists the org's
  screens), copy-to-my-screens for any member; org admins rename from the
  tab and replace an existing org screen in place (sharing no longer always
  creates a new one). Rejected: keeping debounced auto-save (the failure
  mode itself), per-tile locks (no session concept, stale locks, and the
  problem is accidental overwrite, not co-editing), CRDT/merge (overlapping
  rects have no meaningful merge; conflicts are rare, a 409 + choice is
  cheaper and legible), hard-refusing legacy writes (breaks every old
  shell copy for no safety gain), keeping directory grouping, a second
  personal folder set inside "mine" (top-level personal folders already are
  the user's tree), storing shared folders on the org record (`users.json`
  is the 0600 identity store; sidebar structure is layout; `ws` has no org
  row). Follow-ups: server-side filtering of folder items to readable tiles
  (members currently see path names of unreadable tiles inside shared
  folders); a per-org screen soft cap; onboarding starters (a later
  decision).

- **D56 — One menu element for every surface; the tile admin is a window
  (2026-09-06).** The shell had one right-click menu (empty canvas: create /
  new screen / new folder) and no per-tile menu: a tile's actions were split
  across the card head (`>_`, `⚙`, pin, full page, close), the pop-up frame's
  layout buttons (code, logs, proposals) and the `⚙` popover's eight
  sections; sidebar rows only toggled; a recently used tile meant scrolling
  the sidebar; new tiles always went through the owner picker; touch devices
  had none of it; and the `⚙` popover was a fixed 340px panel whose binding
  pickers (long provider refs, the multiselect's absolutely positioned list)
  overflowed into an inner scroll. Shapes, user-ratified: (1) ONE generic
  menu element, `web/bx-menu.js`, serves every surface — canvas, card head,
  float, sidebar row, `⋯` — with plain item objects carrying action
  closures (the menu is shell-internal; closures beat an event/id protocol),
  flyout submenus, a grid row, a filter input, keyboard navigation, and a
  bottom-sheet mode; the shell only builds item lists. (2) The canvas menu
  gets "open tile" (the five most recent not already on the screen + a find
  box; recents are personal state in the layout pref) and "create a new
  tile" as an explicit owner (honouring `tileCreation: org-only`); "new
  sidebar folder" leaves the menu. (3) The tile menu leads with four squares
  (terminal · logs · source · proposals) driven by a new public
  `bx-frame.open(layout)`, then screen actions, then the admin lines that
  open the admin at a section. (4) The `⚙` popover becomes a WIDE,
  RESIZABLE POPOVER with context-menu manners (click outside or Escape
  closes, nothing to drag, one at a time, section targeting, a sheet on
  phones) — first shipped as a draggable window on the pop-out chrome, then
  trimmed back on review: window-isms (drag, ✕, stacking) added nothing over
  a popover that simply dismisses; `⚙` opens it directly (one click to
  everything), the menu's lines open it at a section.
  (5) Overflow is solved structurally: the multiselect list is
  viewport-fixed (position from the control rect, re-placed on scroll/
  resize) so no container clips it; control-heavy tables use fixed layout
  and ref cells ellipsize with the full text on title; the window is
  resizable so wrapping happens only when the user makes it narrow.
  (6) Mobile: `⋯` on card heads and rows plus long-press on heads, rows and
  the empty canvas; menus render as bottom sheets with drill-in submenus;
  the head keeps only `>_` and `⋯` under 820px. Rejected: native
  `<menu>`/popover (no submenus or sheet mode, no positioning control),
  per-surface bespoke menus (three DOM/CSS copies drifting, mobile ×3),
  keeping the 340px popover (clipped lists, no resize, single instance,
  non-reactive position), rich content in `bx-dialog` (its data-only spec is
  an anti-phishing property; a menu needs closures, hover and submenus),
  exporting `bx-menu` to tiles now (API still settling). Follow-ups: lazy
  per-section loads in the admin element; focusable sidebar rows + the
  ContextMenu key; `bx-menu` as a tile API once its schema settles.
  *Amended 2026-09-09: the native menu is never fully lost — inputs,
  editable text and links keep it (paste), selected shell text keeps it
  (copy), and a selection inside a tile rides the relay so the tile menu
  leads with Copy, the shell writing the clipboard on the sandboxed frame's
  behalf (`navigator.clipboard`, `execCommand` fallback on plain http); on
  touch a live selection defers to the platform toolbar.*

- **D57 — Permission sets are built from typed rows; the allowance grammar
  gets one browser module (2026-09-07).** The permission-sets tab took the
  D26/D32 allowance grammar as a comma-separated string next to a one-line
  cheat sheet — to delegate "let devs' tiles call the LLM gateway as
  writer" a ws-admin had to know that this is `tile:apps/llm-gw@writer` and
  that the binding-plane twin is `iface:openai@apps/llm-gw`; a typo was a
  400 from the server, and attaching the set to orgs was a second trip to
  every org card. Shapes: (1) **one shared browser module,
  `web/bx-allow.js`** (`ALLOW_KINDS`, `parseAllow`, `fmtAllow`,
  `allowProblem`, `describeAllow`) that mirrors `parseAllowEntry` case by
  case — the D54 `bx-netrules` precedent: the grammar is defined once
  server-side and *interpreted* once client-side, so the creator, the org
  card and the organisations tile agree; the server still validates for
  real, the module only catches the typo before the round trip and says in
  words what an entry does. (2) **Rows, not text**: each row is a kind
  (use a tile · bind an interface · use a resource · hold a capability ·
  use a GPU · publish hostname/zone/port · network reach · raw) with that
  kind's fields (pattern + role cap; service + provider + instance; …),
  datalists from what the workspace actually has (tiles, provided
  services, capability classes), an in-words preview plus the exact entry,
  and an inline problem that disables the save. Anything the parser can't
  place becomes a `raw` row, so editing never loses an entry. (3) **One
  save = the set + its attachments**: the form carries the orgs to attach
  and PATCHes each org whose membership changed. (4) The same rows edit an
  org's *extra* entries; the cards list stored entries in words. (5) `net:`
  rows stay offered but point at network sets (D54: reach belongs in sets;
  an extra wider than the sets is refused by the ceiling anyway). Rejected:
  a server-side form schema endpoint (the grammar is small and stable; a
  second source of truth), keeping free text with better hints (the
  failure mode was not knowing the class, not the spelling), a wizard with
  steps (a set is a flat list — rows read at a glance), editing ceiling
  rows in the creator (the D20 policy editor stays where it is; the card
  shows the count). Follow-ups: ceiling rows in the set form; a "what
  would this let org X approve today" preview against the live grant
  requests; the organisations tile describing an org's allowance in words.

- **D58 — Shipped trees are guarded by tests, and the upgrade contract is
  a served page (2026-09-09).** The five embedded trees (`web/`, `docs/`,
  `workspace-template/`, `builtin-tiles/`, `builtin-templates/`) ride in
  every xbind and are copied into workspaces, yet nothing checked what
  `go:embed all:` picked up: a stray `go build` binary in
  `builtin-tiles/devbox/backend` shipped in every release for a month
  (+10 MB per xbind, and `bx tile import` would have copied it), and 160
  `plans/…` citations pointed workspace readers — including the coding
  agents the scaffolded `AGENTS.md` briefs — at files that only exist in
  this repo. Shapes: (1) **`assets_test.go` walks the real embed** and
  refuses ELF files, files over 512 KB outside `web/vendor/`, nested
  repos/dependency trees, and any `plans/` pointer; inside the shipped
  trees design records are cited by served page or decision ID, and the
  overview index says where the records live. (2) The copier skips an
  ELF named after its own directory (`backend/backend`,
  `_backend/_backend`) — the `go build` shape — and nothing else, so a
  `cgi` handler that is a compiled executable still instantiates. (3)
  `.gitignore` covers build output; `hack/vendor.sha256` pins the
  vendored frontend deps and the release preflight verifies it; gofmt
  runs over an explicit `GOFMT_DIRS`. (4) **`docs/compat.md`** states the
  never-break-users contract (API additive-only, frozen `/vendor/` URLs,
  additive scaffold layouts, theme fallbacks kept, CLI superset,
  idempotent migrations with fixture tests, warn-before-error) and
  **`docs/maintenance.md`** documents every guard — both served, because
  the contract is what builders rely on and a maintainer returning after
  months needs the guards explained next to the docs they protect.
  Rejected: embedding `plans/` (18k lines of internal design prose in
  every workspace), an allowlist of "known" pointers (rots), keeping
  contributor docs out of `docs/` (the guards would again live only in
  heads), and skipping every executable in the copier (`backend/handler`
  is a legitimate executable).

- **D59 — The frontend kit is named and homed; the theme is never injected
  (2026-09-09).** A de-facto kit had grown under `/vendor/` beside the
  vendored libraries — `bx-netrules` (5 importers), `bx-allow` (4),
  `events-socket` (4), `bx-multiselect`, `bx-dialog`, `bx-frame`'s
  `clampBox` — undocumented (docs/elements.md called `bx-menu` "not a tile
  API" while shipped tiles imported its siblings), while `api()` was
  hand-rolled nine times with four signatures, `deepActive` four times,
  the viewport clamp twice, and the admin tile carried a copy of the code
  viewer's highlighter. Shapes: (1) **`web/bx-kit.js`** holds `api` /
  `xbinApi` / `selfApi`, `jbody`, `esc`, `deepActive`, `pathHas`,
  `clampBox`; `bx-code.js` exports its `hl` / `langFor`; `make js-check`
  refuses a second definition of any of them, and `clampBox` stays
  re-exported from `bx-frame.js` (URLs are frozen, docs/compat.md rule 3).
  (2) **`docs/frontend-kit.md`** is the contract: the tile-importable
  list, the shell-only list, absolute-URL imports only (the import map
  lives in each workspace's `xbin.json` and is never rewritten, so a bare
  specifier would 404 in older workspaces), and the lit pitfalls.
  (3) **Theme fallbacks are kept and regenerated** — `make theme-check`
  keeps every `var(--bx-x, <literal>)` equal to `theme.css` — and
  **`theme.css` is not injected** into tile documents: it sets
  `color-scheme: dark`, which would flip the default colours of a
  third-party tile that never opted in, and a tile using one token with
  its own background could lose contrast. The fallbacks therefore ARE the
  theme for bare documents. Rejected: a `/kit/` URL prefix (reserving
  `kit` as a top-level name could break a workspace that has one; nothing
  is gained over `/vendor/`), injecting a tokens-only sheet (the
  contrast case above; revisit only with an opt-in signal from the tile),
  and moving modules between URLs.

- **D60 — One error writer, one durable file write (2026-09-09).** The
  broker wrote its API errors as 305 inline `map[string]string{"error": …}`
  literals (with `"docs"` on 34 of them), decoded bodies through a helper
  that lived in vault.go, and persisted nine stores with their own
  tmp-then-rename — none of which fsync'd — while three more (cron,
  backup schedules, the uid map whose corruption would orphan chowned
  sqlite files) wrote in place. Shapes: (1) `server.WriteError(w, code,
  msg[, docs])`, `server.WriteOK(w)` and `server.DecodeJSON(r, v)` are
  the API's error / success / body helpers; the migration was mechanical
  and wire-identical (`{"error"}` plus `"docs"` only where a page was
  named; `{"ok":"true"}` stays a string). (2) `internal/fsutil.
  WriteFileAtomic` — same-directory temp named `.<name>.<random>.tmp`,
  chmod to the target mode before writing, write, fsync, close, rename,
  fsync the directory, temp removed on any error — and every store uses
  it; the watcher ignores the temp name. Rejected: making the prs.go body
  decoders strict (they accept unknown fields today; tightening is a
  behaviour change for old clients, so they decode by hand with a
  comment), and a `RequireCap` helper (the two guards that exist read
  better than a predicate-taking generic).
- **D61 — The broker's decisions reach the server through one interface
  (2026-09-09).** `server.Server` carried six nullable func fields —
  `IsAdmin`, `OwnerOf`, `BusFilter`, `Interfaces`, `SandboxExtras`,
  `CodeReadGrant` — each installed by the broker at boot and each read
  through its own nil-default at the call site (owner-only, unowned, no
  bus events, no meta, base sandbox, no code plane). `server.Policy` names
  the six questions as one interface; `NoopPolicy` is those defaults in
  one place and what a server without a broker (tests, apicheck) runs;
  the broker installs `brokerPolicy` through `InstallPolicy`. Wire-identical:
  every answer is the same value the hooks returned, and `Interfaces` keeps
  its `map[string]any` shape because the single-slot and multi-slot forms
  are marshalled verbatim into the `xbin-interfaces` meta that shipped
  tiles read. Rejected: typing `Interfaces` as a struct (an empty
  `endpoints: []` would be dropped by `omitempty`, changing the meta) and
  "improving" `OwnerOf`'s empty-string overloading (the request-access page
  switches on it). Next: the broker split (I6d) targets this interface —
  the kernel that answers it is what becomes `internal/authz`.
- **D62 — The daemon is a package; its configuration is one struct
  (2026-09-09).** `cmd/xbind/main.go` was 1,235 lines with a 680-line
  `serve()` taking eleven parameters, fifteen boot-time migrations whose
  ordering edges were unwritten, `os.Exit` in five places plus the signal
  handler, and 26 `XBIN_*` variables read lazily by eleven packages with
  three parsers — none of it testable without the binary. Shapes: (1)
  `internal/boot.Config` is the flag block plus every env-only setting,
  with struct tags (`flag`, `env`, `def`, `doc`, `readBy`, `secret`) that
  both declare the flags (`RegisterFlags`; a flag's default is its env
  value, so precedence stays flag > env > default) and render
  `docs/config.md` (a test fails when the page is stale; `UPDATE_DOCS=1`
  rewrites it). Settings other packages read where they are used stay
  there but are listed with `readBy`, so the reference is complete
  without threading five values through the tree. (2) `boot.Run(ctx,
  cfg)` runs `boot.Steps` — sixteen named stages in an order a test
  asserts (workspace before privileges, users before homes, registry
  before broker, …) — then serves until the context ends; every failure
  is an error, `main` owns the log level, `signal.NotifyContext` and the
  exit code. `Privileges` (the setuid path), the console `Listener`, a
  `Ready` callback and `Stdout` are injectable. (3) The five inline
  handlers that read across runner, broker and ingress (`/backends`,
  `/runtime`, `/ingress`, `/tile-status`, `/term-net`) moved to
  `boot/api.go` rather than into the broker: they are cross-cutting by
  nature and the broker does not know the runner. (4) `ResolveVaultMode`
  is the boot's one pure decision, table-tested. (5) `Broker.Close`
  releases the KV database's file lock, the cron scheduler and the disk
  monitor — the daemon relied on process exit, and a second in-process
  boot of the same workspace blocked on bbolt's flock. Proof of value:
  the legacy-workspace fixture now boots twice in-process in 0.2 s (the
  binary twin takes ~30 s) and is part of `make check`. Rejected: moving
  every lazily read variable into Config now (five packages would gain
  parameters for a documentation gain the `readBy` column delivers).
- **D63 — The broker sheds planes, one at a time, starting where the seam
  is clean (2026-09-09).** `internal/broker` is 17k lines and a 321-method
  struct holding 18 concerns; the plan (I6d) is a kernel (`authz`) plus
  identity / net / storage / content / obs planes with the `Policy`
  interface (D61) as the seam. The per-file field map showed which files
  touch only their own state, and the observability trio — tile status
  reports (`status.go`), per-user prefs, backend logs — touches nothing
  of the broker but three answers: the workspace root, the hub, "is this
  principal admin", "is this path a component". `internal/obs.Plane`
  takes those as fields; the broker builds it in `Register` and mounts it.
  Wire-identical (same routes, same handlers, same `IsAdmin`), and the
  route inventory still sees every `RegisterAPI` literal because it scans
  `internal/`. The shape for the next planes: a struct whose fields are
  the exact answers it needs from the rest, built and mounted by the
  broker, tests moved with it on a fixture of those answers rather than a
  whole broker. Rejected: an interface for the answers (three funcs are
  the interface) and moving the storage plane first (it shares the KV
  store, the barrier and the resenc manager with backup and usage —
  three seams, not one).

- **D64 — "View as user" is a read-only session swap, minted by ticket
  and bound to the admin's own browser (2026-09-10).** Admins asked to
  see what a user is permitted to see and do. Options: a per-request
  header (`X-XBin-As`, honored for admins) — every call site would have
  to carry it and the shell would need an "as" mode; a server-side
  "effective permissions" report — tells, doesn't show, and drifts from
  the shell's actual reading of grants/screens/menus; or a session that
  IS the user. Chosen: a session. `POST /api/xbin/impersonate {user}`
  mints a one-shot ticket (2 min) for the calling admin; opening
  `/login?impersonate=<ticket>` top-level in the same browser swaps the
  cookie for a session whose principal is the user with `Impersonator`
  set. The admin then loads the ordinary shell as that user — the same
  code path, no "as" branches anywhere. Constraints that make it safe:
  the ticket is bound to the minting admin (redeeming from a browser
  signed in as anyone else fails, so the URL delegates nothing); the
  session is read-only — the authed middleware refuses every non-GET
  except the way out, terminals included (`Principal.ReadOnly`), and
  frame tokens minted under it inherit the mark, so tiles can't write
  either; no nesting; a disabled account or yourself can't be viewed;
  `/whoami` and `/sessions` name the impersonator, audit lines carry
  it, and the shell shows a banner with the exit. The session remembers
  the admin's own session id (or that they came in on the owner cookie)
  and hands it back on stop — and `/logout` from a view does the same
  rather than dropping the admin to the login page. The admin console is
  a sandboxed tile with no cookie access, which is why a ticket exists
  at all: the tile mints a URL and opens it (`cap:open-links`), or the
  admin copies it. Known consequence: a cookie is per browser, so every
  tab is the user until the view ends — the banner is in every shell.

- **D65 — Named network sets are terminal scopes and a workspace-admin
  binding ref (2026-09-11).** D54 gave a set two homes: the `org` binding
  (the live union of an org's sets) and the `org` terminal scope on
  org-owned tiles. Nobody could pick ONE set, and no set reached a
  personal/workspace tile's terminal at all. Shapes: (1) a terminal scope
  `set:<name>` — the relay under exactly that set's rules, host networking
  when it says host — listed for each set attached to the tile's org, for
  whoever may open a terminal there (the same gate that gives them `org`;
  `TermNetFor` never checked membership, so "member" was never the unit),
  each a NARROWING of the union; a workspace admin sees every workspace
  set on every tile — they may already pick `host` anywhere, so no new
  privilege class appears. (2) A binding ref `set:<name>` — a WORKSPACE-
  ADMIN act (org admins bind `org`, which is what their sets are for; the
  user's framing), still inside the owning org's union on org tiles (D54
  rule 1 is an invariant every surface builds on: `deadSlotReason`, inert
  notes, the org card's reach, `bx doctor`; the remedy is attaching the set
  to the org), judged by the set's MATERIAL rules — relay targets and
  host — never its `provider:` rules, which a single slot cannot honour.
  (3) Defaults never move: `defaultScope` is one function for the picker
  list and the clamp, and a set is never it (`org` on org tiles, then
  `internet`/`none`); an unpickable or vanished set clamps to the default
  with a note, and the set name is charset-gated at `?net=` because the
  note is written into the PTY. (4) Provider-only sets are neither a scope
  nor bindable (they would mean "offline"; `none` says that honestly).
  (5) Deleting a bound set is 409 like an attached one (explicit beats a
  silent reach change); a vanished set still resolves inert, fail-closed.
  (6) Set edits restart every tile bound to the set, org-owned or not.
  (7) `GET /bindings` carries `netOptions` — every net slot's option list,
  bound or not — since pending rows exist only while unbound; the set rows
  are server-labelled and `blocked` for anyone but a workspace admin, so
  `Blocked` now means "refused for everyone, or for this caller". Revisits
  D54's ratified "network is a property of the tile, not the person": it
  stands — a non-admin only narrows within the tile's org; the admin
  exception is the one D54 already carried on internet/host. D54's rejected
  "per-tile net ACLs (the org is the unit)" stands too: `set:` selects
  among org-level objects under the org's ceiling. Documented asymmetry: an
  admin may OPEN a terminal under any set on any tile (a human act, outside
  the element ceiling) but may not BIND a tile to an uncovered one.
  Rejected: uncovered `set:` bindings for ws-admins; inert-on-delete;
  listing provider-only sets as "airgapped"; a `net:set:<name>` allowance
  target (new grammar, D54 point 1); a per-user `termNetSets` grant (not
  asked; D17's `termNet` stays the only per-person knob).

- **D66 — Shell windows belong to their tile and live inside the canvas;
  grid drags push, never overlap (2026-09-14).** Three shell complaints,
  one model. (1) A tile's terminal pop-up is positioned RELATIVE to its
  frame (`{dx, dy, w, h}` offsets; `position: fixed` stays, so `overflow:
  hidden` cards can't clip it, and a per-frame animation loop keeps it on
  its anchor through scrolls and drags), and its top-left is fenced to the
  canvas's tile extent (`popBounds` from bx-canvas), never left of / above
  the scroll origin; the canvas grows to contain open pop-ups. So every
  terminal is reachable by scrolling — the user's rule, after pop-ups
  anchored once in viewport coordinates were left behind or off the
  scrollable area. Frames outside the shell (no bounds) keep the viewport
  clamp. (2) The shared-org-screen bar is a strip in the shell's column
  under the screen tabs — pinned, subtle, never in the scroll area (it was
  a sticky card inside `<main>` that lost z-order to dragged cards and slid
  away on horizontal scroll). (3) A grid drag or resize pushes the tiles it
  lands on: contact direction = the axis of the smaller overlap, away from
  the mover (so a tile hit from the left goes right); a pushed tile keeps
  that direction across pointer moves (a pure recomputation flipped the
  preview when the penetrations crossed) and passes it down a cascade; the
  push is recomputed from the layout at pointerdown on every move, so
  backing off restores everyone and a release only commits what still
  overlaps; a left/up push that would leave the canvas flips; positions
  snap in the push direction; a resize (top-left anchored) pushes right/down
  only; ghosts preview the landing spots and the pushed tiles stay put
  until release. The math is a lit-free module (`grid-layout.js`) with node
  tests; the gesture renders from state (the dragged card from its live
  rect, so lit re-renders no longer reset it), never reorders the tiles
  array (`repeat` would remount frames), and skips a no-op commit (a click
  must not dirty an org draft). Rejected: drag velocity as the push
  direction (zero when the pointer pauses, noisy at low speed); hoisting
  the pop-up out of the frame into the canvas (bx-frame is a standalone
  `/vendor/` element); refusing an overlapping drop instead of pushing.

- **D67 — The landing page and the README lead with the office-for-agents
  frame, in a fraction of the words (2026-09-14).** Readers said they could
  not tell what xbin was for: xbin.dev opened with "Code and state, in
  separate sandboxes. A Harvard architecture for self-hosted software" and
  reached "who it's for" in section five of nine (~2,000 words); the README
  opened with the component model and 160 lines of installation. Research
  over the pages that got popular (Linear, Vercel, Cursor, Stripe, Raycast,
  Umbrel, n8n, Retool, the agent-identity vendors) settled the shape:
  outcome-first heroes of 9–23 words, at most six sections, ~22 words of
  body per section, exactly two CTAs, zero comparisons, no numbered
  how-it-works steps (a screenshot or terminal block does that job),
  role-prefixed use-case tiles ("Ops can: …"). Chosen frame: **an office for
  your agents** — the workspace is where agents and people work, each app a
  room with its own identity, grants and network policy, and IT holds the
  keys. The metaphor describes where agents work and who controls the room,
  never what agents are: the "hire an AI employee / coworker" register was
  publicly punished in 2026 (Forrester: "the agent-as-coworker narrative is
  nonsense"; Artisan retired "Stop hiring humans"), while the governance
  half of the analogy is exactly what Entra, Okta, Auth0, CyberArk and
  SailPoint converged on — identity, roles, least privilege, grants,
  onboard, revoke, audit trail — so the page uses those words verbatim.
  Rejected: "middle office" (finance jargon outside banking; it names the
  work, xbin provides the room), "data/process management layer" (a layer
  is the abstraction the landing-page roasts punish), comparison-led copy
  and "not X, but Y" (the top-tier pages have none), keeping "tile / slot /
  harness" on the page (docs vocabulary; "app" and "directory" on the page).
  The technical model survives as four facts under "One app, one room";
  the security list as four noun-list tiles under "IT holds the keys"; the
  README gets the same pitch, the use cases, try-it and how-it-works before
  its reference material. Copy rules recorded in website/README.md; the site
  now runs under `make js-check` and `shellcheck` (it never had a guard).

- **D68 — The grid scale is per browser and geometry-only (2026-09-14).**
  A shared layout laid out on a 32-inch 4K display is far too large on a
  laptop; the only knob was the per-user font size, which zooms the whole
  shell (CSS `zoom`) and follows the user to every device. The grid scale
  multiplies the RENDER of the logical layout (tiles are multiples of the
  48px grid) by k in [0.5, 1.5]; the stored geometry never changes, so one
  org screen stays one layout, and drags, resizes and the push ghosts divide
  pointer deltas by k and snap in logical units. Per browser
  (`localStorage`), not per user: the same person wants different scales on
  different devices, and a layout must not carry a device's preference.
  Geometry-only rather than CSS zoom of the canvas: text and controls inside
  tiles stay sharp and pointer math stays exact; the font-size zoom remains
  the "everything bigger" knob. Floats, pop-ups, spawned windows and the
  admin popover are viewport windows and keep their size; mobile stacks
  cards and renders at 1. A browser zoom (ctrl/cmd +/−/0, ctrl-wheel, a
  pixel-ratio change on resize) earns a one-time toast pointing at the
  slider, since browser zoom shrinks text along with the layout. Rejected:
  storing the scale in the layout or the per-user prefs; scaling floats
  (they are placed by hand in viewport space).

- **D69 — A covered neighbour yields into the space the drag vacated
  (2026-09-14).** D66 always pushes a neighbour in the direction it is hit,
  so dragging one of two equal side-by-side tiles onto the other pushed
  the second one further out and left a hole: the layout grew when the
  user meant a swap. Now, in `pushLayout`, a tile hit by the DRAG itself
  (not by a cascade, not by a resize) yields — steps to the far side of the
  drag, just clear of it — when the drag covers more than half of it along
  the push axis and that spot is inside the canvas and free of every other
  tile; otherwise it is pushed as before. The threshold keeps a glancing
  overlap a gentle push (a tile with a free gap behind it must not leap to
  the other side on first contact); the free-spot test keeps the no-overlap
  invariant, which is also why two equal neighbours only swap once the drag
  covers the neighbour fully (before that, the yielded spot would leave the
  canvas or overlap the drag). A yield sticks for the rest of the drag while
  its spot stays free — recorded in the sticky-direction map as the
  upper-case push direction — so crossing back over the threshold cannot
  flip the preview; and since every move is recomputed from the original
  layout, dragging past the neighbour returns it to its place. Rejected:
  gridstack-style swapping where the drop lands the drag in the neighbour's
  slot rather than at the pointer (the card is the preview here, there is
  no separate placeholder); yielding on a resize (nothing is vacated).

- **D70 — Predictive local echo in the terminal is mosh's engine, validated
  by a server echo ack, per browser, auto above 100 ms (2026-09-15).** On a
  slow link every keystroke waited a round trip before it showed. Options:
  (a) write the typed byte into xterm's buffer locally — rejected, the real
  echo then lands on a screen that already moved and every mistake (a
  password prompt, a program that does not echo, a shell that rewrites the
  line) corrupts what the user sees; (b) mosh's approach — a prediction
  OVERLAY judged against the real screen and withdrawn when wrong. (b),
  ported from `src/frontend/terminaloverlay.cc` in its `experimental`
  flavour (immediate display, a wrong cell is dropped alone, no tentative
  epochs), because the user asked for that flavour and because the
  algorithm has a decade of use behind its edge cases (insert shifts the
  row, the last column is unknown, Enter on the bottom row blanks it rather
  than predicting a scroll, arrows move the cursor, everything else predicts
  nothing). The engine is a pure module (`web/term-predict.js`) over a
  duck-typed framebuffer so `node --test` covers it; xterm's buffer API is
  the framebuffer and xterm decorations are the overlay — xterm owns the
  cell metrics, a hand-positioned layer would drift on every font change.
  Validation needs to know when the server has acted on the input: the
  server acks each input frame 50 ms after the PTY took it (mosh's
  ECHO_TIMEOUT; the application has answered by then if it will), through
  the SAME ordered queue as PTY output so the ack always follows the echo it
  vouches for; the browser applies it through xterm's write queue so the
  screen it judges includes every earlier frame. RTT comes from an
  app-level ping/pong every 5 s (a WebSocket ping is answered below JS) and
  is smoothed as RFC 6298 does. Thresholds: auto shows predictions above
  100 ms SRTT (the user's number; mosh's own is 60) with hysteresis to 60,
  underlines above 160 (mosh's), and shows them on any link when a
  prediction has waited 250 ms (mosh's glitch trigger). The mode is per
  browser in localStorage like the terminal theme and font size — it is a
  property of the link, not the user. Left out on purpose: renditions
  (predictions draw in the terminal's default colours), overwrite mode,
  scroll prediction, wide characters (their cells are never predicted), the
  alternate buffer (full-screen apps). Compat: both frames are additive and
  ignored by an older peer; the session frame's `echoAck` gates the feature.

- **D71 — With the terminal cursor hidden, predictions anchor where the
  echo lands, learned from the echo itself (2026-09-15).** mosh predicts at
  the terminal cursor, which is right for a shell and wrong for the programs
  xbin's users actually type into: Ink apps such as Claude Code, and most
  TUIs, hide the cursor (DECTCEM off), park it at the end of their frame,
  draw their own block cursor and echo typed text into an input field
  elsewhere on screen. A prediction at the parked cursor is a stray glyph
  at the bottom of the screen for one round trip. So when the application
  has hidden the cursor (tracked through xterm's parser hooks for `CSI ? 25
  h/l` and RIS) the engine switches to ANCHOR mode: nothing is predicted for
  the first keystroke of an input session; when its ack arrives, the screen
  is diffed against a snapshot taken at the keystroke and the cell where the
  typed character newly appeared (nearest the expected place, else the
  bottom-most — input fields live at the bottom) becomes the anchor, plus
  one. Later keystrokes are predicted at the anchor in OVERWRITE mode (no
  shift: the field's frame must stay put), backspace steps it back, Enter
  forgets it (the field is about to change), arrows move it, and every acked
  keystroke keeps re-learning it, so a field that re-renders a row up is
  followed after one miss. Rejected: predicting at the parked cursor anyway
  (mosh's behaviour; harmful here); looking for the application's drawn
  cursor (an inverse-video cell — app-specific and often absent). The
  overlay itself moved off xterm decorations onto a layer positioned by the
  renderer's cell metrics, because xterm hides decorations in the alternate
  buffer and tmux, vim and less all live there.

- **D72 — The agent template takes its instance's generic work back as a
  patch series, replayed onto the moved template (2026-09-19).** Twelve
  patches made in an `apps/agent` instance against the template snapshot of
  2026-08-11 (D50 gives instances the template's history, so the series had
  a real base). The template had moved since — `esc()` and `api()` come
  from `/vendor/bx-kit.js` (I5) with a different call shape, handlers write
  through the SDK (I10d) — drift git cannot see, so the series was replayed
  commit by commit with a three-way merge against the pristine base and
  each commit fixed in place: kit-style calls, SDK writes, the XSS fix in the
  kit's `esc` rather than a local copy (the kit-duplicate guard forbids one,
  and every consumer needed it). What stayed out, as the series' own README
  says: the instance's assistant persona and version-guarded prompt, the
  owner-context injection and its cross-scope grant, the model fallback, the
  instance's home-view copy, and its manifest/module rewrites. Budgets:
  `agent.js` (≈1460 lines) is listed rather than split — the template is one
  module by design, instances fork it and merge updates by git (D50), so its
  file layout is part of the contract. Browser tests ride along
  (`test/*.mjs`, Playwright, skipping without it) with a shared `kit.mjs`
  that serves the kit and marks the page sandboxed so the kit's `api()`
  takes the stubbed `xbin.fetch`. Left open, by the series' own flag: the
  skills store crosses lanes — a private-lane run can write a skill a
  web-lane run reads and sends out; fixing it needs per-lane skills or no
  skill writes from the private lane, a design call not taken here.

- **D73 — Terminal sessions are directed by xbind per user; the browser
  keeps no session ids (2026-09-21).** Sessions were owned per user
  server-side all along (`homeKey`), but the only pointer to them — which
  ids belong on which tile, their tab names and pickers — sat in the
  browser's `localStorage['bx-term:<tile>']`, keyed by tile alone. So a
  session was reachable only from the browser profile that opened it; a
  user switch on one browser inherited the previous user's tab list (names,
  pickers, geometry), tried their ids, and read the 403 as "session gone"
  — or, for an admin, attached to the other user's live shells; and no
  endpoint let a user find their own sessions. Now the server answers
  "which are mine here" (`GET /api/xbin/term/sessions`, `ListFor` over the
  in-memory map, filtered to tiles the caller may still open a terminal
  on), a tab's name lives on the session (`PATCH`), and a `term` event
  tells the owner's browsers when the directory changes; the frame's tab
  bar is a view of that answer (`web/term-sessions.js`, unit-tested), the
  window's own state is a per-user pref, and the legacy record is adopted
  once and removed. Chosen over syncing the browser record through prefs
  (prefs would carry stale ids across daemon restarts and cannot express
  "mine" — an admin's prefs on a shared browser would still name another
  user's sessions) and over a persisted directory (nothing it would point
  at survives a restart). Window state is per user rather than per browser
  because there is one place for it and the geometry is tile-anchored and
  re-fitted on open. Reattach re-checks `CanTerminalTile` so a withdrawn
  level closes the door to sessions already open there; the sessions are
  not killed (a policy change left for its own decision). Admins keep the
  explicit by-id attach for debugging; only the auto-restore stops crossing
  users. `DELETE /ws/term/env` still kills every user's sessions on a tile
  — flagged, not changed.
- **D74 — Agent sessions: a terminal session with an agent driver, the
  host inside the sandbox, keys from the tile vault (2026-09-21).** A
  coding agent should run where a shell runs — the tile's sandbox, the
  user's home, the tile's token and network scope — and be driven from
  anywhere: a browser tab, a second browser, `bx` in a shell, all seeing
  one stream. So an agent session IS a terminal session (`kind:"agent"`,
  same `Manager`, directory, per-user ownership, reaper, limits) whose
  entry is not a shell but the daemon's own `bx __agent-host`, bound
  read-only into the sandbox (host and daemon are one build): it spawns
  the provider's ACP adapter from the first frame xbind sends and proxies
  the Agent Client Protocol between them, serving `fs/*` and `terminal/*`
  itself *inside* the sandbox, where the kernel's mount view (the
  allow-list, the masks, the read-only tiles) is the authority instead of
  a second copy of the visibility rules in the daemon. Chosen over a
  supervisor in xbind (nothing can run a second process in the sandbox's
  namespaces after the init has exec'd — `init_linux.go` applies the
  guards and execs, PID 1 is the entry) and over a sub-sandbox for the ACP
  handling (agreed overkill: the host is xbind code, a descendant of the
  init, under every guard already). Events are an append-only per-session
  log (ring: 5000 events / 8 MiB) replayed by cursor and mirrored live as
  `session` hub events filtered like `term` ones — chosen over a stateful
  channel on `/ws/events` (the hub has no history and evicts slow
  subscribers, so a client re-fetches on a skipped seq; the log is the
  truth, the socket the hint). Permissions: any client answers, first
  wins; "allow for session" answers the agent's `allow_always` option and
  records a rule on the *session* — not a grant, because grants have no
  session scope and a terminal-level user cannot approve one. Credentials:
  the agent authenticates from the session's per-user `$HOME` (D6) — the
  same home a shell terminal gets — and nowhere else. The daemon never
  overrides `HOME` or the CLIs' config env, so a `claude /login` / `codex
  login` / `opencode auth login` done once in a shell terminal signs the
  agent in on every tile; no login → the first turn's `status error` names
  the command to run in a terminal. (v0.3.51 shipped an extra path that
  injected keys from the tile vault into the agent process; it was reverted
  the same day as needless jank — the home is the whole point of per-user
  terminals, and agent sessions are terminals. There are no provider keys
  in the vault.) The requested mode is applied after the CLI has loaded its
  settings, so a `permissions.defaultMode` in `~/.claude/settings.json` is
  the default when no mode is asked. Conservative modes are the
  defaults; bypass modes are `explicit` in the provider table and must be
  named. A shell's own terminal token may open and drive a session for its
  OWN tile (`CanTerminalTileVia`, revocation-safe) so `bx agent run` works
  inside a terminal, but never another tile's (tile A's agent must not
  drive tile B's session); such a session is restricted
  even for an admin — the token is the tile, not the human. The daemon's
  `bx` is bound in rather than the rootfs's (`/usr/local/bin/bx` drifts
  from the daemon; the host must be the daemon's version), so the dev `bx`
  is built static. Out of scope, flagged: persisting logs across restarts,
  ACP v2 (fs/terminal move out of the protocol — the host becomes
  optional), MCP servers handed to the agent (`mcpServers: []`), per-user
  "always" rules across sessions.

- **D75 — Agent session history and resume: the transcript outlives the
  session; resume is the agent's own `session/load`, capability-gated
  (2026-09-22).** A finished conversation is worth reading back and, often,
  continuing — and on an auto-updating box every restart would otherwise
  erase it. So when an agent session ends (or the daemon stops, `FlushAgents`
  on the SIGTERM path) its event log and a little metadata are written to
  `data/agent-history/<user>/<tile>/<id>.json` — per user × tile like the
  prefs store, the newest 20 per tile, never a session that took no prompt
  (`internal/term/history.go`). Read-back reuses the live `/events` shape so
  the Agent tab renders it unchanged, read-only. *Resume* means the agent
  reopens its OWN session: the client reads `agentCapabilities.loadSession`
  at initialize, persists the agent's session id, and `POST /term/sessions
  {resume}` runs `session/load` instead of `session/new` — the agent replays
  the earlier turns as updates, then continues; the continuation supersedes
  the entry it reopened. Not chosen: re-seeding a fresh session with the old
  transcript (lossy — the agent's own state, tool results and files context
  are gone) or a provider `--resume` argv (adapter-specific, bypasses ACP).
  An agent without `loadSession` gets read-only history and a "start a new
  session here" fallback, so nothing breaks where support is thin.

- **D76 — Workspace branding: a title and an icon, stored as a size-capped
  data URI in a sealed-safe `data/branding.json`; the brand replaces the
  logo (2026-09-23).** Every workspace said "workspace" and wore xbin's mark
  — the word was only the `<bx-shell name>` attribute in the workspace-owned
  `root/index.html`, and the mark was hand-copied into five places. An admin
  should set both from the admin tile, and have them show everywhere the
  word/mark shows — including the sign-in page, which renders before auth
  and before the vault is unsealed. That last constraint decided the storage:
  the blob and KV planes are authenticated *and* 503 while sealed, `/c/`
  chrome needs a principal, `/vendor/` is the embedded FS; so the icon is a
  data URI (allowed image types, bytes sniffed to match, ≤ 256 KiB) in a
  plain xbind-owned JSON doc under `data/` (`internal/branding`), templated
  straight into the login/invite HTML and handed to the shell over
  `GET /api/xbin/branding`. A data URI only ever lands in `<img>` and
  `<link rel=icon>` — never a navigable same-origin URL — so an SVG's scripts
  are inert; no new unauthenticated route, no cache story, no file store.
  UX: a set brand *replaces* the logo (icon + title, no X/BIN wordmark); the
  tab title keeps its shape (`<Title> · xbin`); `root/index.html` is never
  rewritten (fork-and-merge contract) — the shell overrides title and favicon
  at runtime, as it already did for the title. Admin `PUT` is audited like
  every admin write and publishes a `branding` hub event so open shells
  update live. Not chosen: a dedicated open `/branding/icon` route (a second
  static plane with its own cache/CSP/sniff rules for a few KB of image).

- **D77 — The Agent tab renders agent quirks from one normalized tool
  record; richer adapter output is opted into by capability; plan approvals
  are never session rules (2026-09-24).** The adapters say much more than
  ACP's own fields: a shell command's human description (Claude keeps it out
  of `title`, which is the command), the tool's name, the subagent a call
  runs under, terminal output riding `_meta`, a plan approval dressed as a
  `switch_mode` permission. Rendering ACP generically gave a heredoc as a card
  title, raw JSON under "Permission — Ready to code?", and Codex's output as a
  bare `[terminal id]`. What mature clients (Zed, Toad, agent-shell,
  CodeCompanion, avante, Codeg) converged on, and what we do: the daemon lifts
  the known `_meta` namespaces (`claudeCode`, `codex`, the shared
  `terminal_output*`/`terminal_exit`) into plain event fields
  (`internal/agent/acp/toolmeta.go`: `name`, `label`, `parent`, `subagent`,
  `planReview`, `output`/`outputDelta`/`exitCode`), each key decoded alone so
  an unknown shape drops only itself; the browser folds a call's events into
  one record (`web/agent-tools.js`, pure, node-tested) and renders by kind
  with special cards for plan approval, execute, diff (`web/agent-cards.js`).
  The headline is **the harness's own description**; where there is none
  (opencode, Codex) a deterministic reading of the command (heredoc → "Python
  script (N lines) → main.go", `sed -i`/`cat >`/`tee`/`>` name their file).
  Raw input is behind a toggle, text content is markdown. Richer output is
  **opt-in by client capability** (`clientCapabilities._meta.terminal_output`
  / `terminal_output_delta`), each flag flipped in the change that renders
  it. **Plan approval**: ACP's `allow_always` on `switch_mode` means "approve
  AND raise the mode" (the spec's own example), so `Permissions` never
  records a rule from, nor auto-answers, a `switch_mode` — before this, the
  second plan of a session was approved unseen with the first allow_always
  option, Claude's "clear context and use auto mode". The plan card shows the
  plan as markdown and every option in the agent's words; "keep planning"
  takes feedback that is sent as the next prompt once the rejected turn
  settles (what Claude's TUI does). **What changed on disk** comes from git
  snapshots of the tile (`internal/term/agentdiff.go`): `add -A` +
  `write-tree` at each turn's start and each tool call's end, diffed
  consecutively (`files.changed` per call and per turn) — the approach of
  Cline, opencode, OpenHands and Codex's ghost commits, and the only one that
  sees a shell write. The tile's own repository is **never used**: the
  sandboxed agent can write its `.git/config`, and a git run by xbind that
  reads it runs whatever `core.fsmonitor` or filter it names (verified). So
  the snapshots live in a private per-session git dir (index, objects,
  config), with no system/global config and fsmonitor/hooks forced off; the
  tile is only the work tree. Not chosen: an LLM to rephrase commands or
  produce diffs — nobody does the latter, and a model-written label is a
  guess where the harness usually already wrote one; Claude's own per-turn
  file-change reports (checkpointing) — they miss shell writes and exist for
  one agent only. **Questions**: the client advertises ACP form elicitation
  (`elicitation.form`), which is what turns Claude's AskUserQuestion back on
  (the adapter disallows the tool for a client that cannot render a form); a
  question is held like a permission (`elicitation.request`, first answer
  wins, a turn cancel answers `cancel`) and rendered generically from its
  JSON schema, honouring the shared `_askUserQuestionCustomAnswer` marker for
  the per-question "Other" box. URL-mode elicitation is not advertised.
  **Slash commands** ride `status` like `options` (on change and every
  idle) and are sent as prompt text, ACP's own model.

- **D78 — Nothing runs with xbind's privileges on data a sandbox can write:
  tools on workspace data run in a throwaway sandbox (`internal/confine`),
  enforced by a guard test; isolation failures fail closed (2026-09-24).**
  Found while building D77's snapshot diffs: a host `git -C <tile> diff`
  runs the tile's `.git/config` `core.fsmonitor` (verified with a marker
  file), and a tile's `.git` is writable from its terminals and coding
  agents — so every Code panel view, repo init, template check and builtin
  update was "write my tile → run code as xbind". `go build` of backends was
  the same through VCS stamping (and `go.mod` replaces reach any host
  directory). The terminal manager, when its sandbox failed under
  `--isolate`, fell back to a host shell — for a non-admin too. Fix, as a
  rule rather than a patch list: `internal/confine` runs a tool in a fresh
  sandbox over the base rootfs (`env` resolves the tool; only the paths the
  job needs are bound; `Unprivileged`: no caps + the syscall block-list; no
  netns unless the job fetches — `NetHost` for an import the operator asked
  for, `NetInternet` through the relay for module downloads); `confine.Git`
  adds hardened flags/env (no system/global config, fsmonitor/hooks off,
  `safe.directory=*` since xbind is root inside). Measured ~45 ms per run
  with fuse-overlayfs (~17 ms kernel overlay) — fine for the Code panel's
  handful of calls, async for the snapshotter. `confine.Configure` runs in
  an early boot step (`confine`, before registry/broker, which init repos).
  Go builds keep compatibility where it matters: the host toolchain bound
  read-only (the rootfs ships an older Go), the workspace read-only with
  `.xbin`/`data`/`homes` masked so `go.work` resolves unchanged, per-tile
  GOCACHE/GOMODCACHE (a shared writable cache is a cross-tile poisoning
  channel) seeded offline from the host module cache as a read-only
  `file://` GOPROXY, public-only egress (`XBIN_BUILD_NET=host` for LAN
  proxies), `-buildvcs=false`. Without isolation there is no sandbox and no
  boundary (backends and terminals already run as xbind), so confine runs
  the tool directly with the same hardening. The rule is mechanical:
  `TestNoDirectExec` fails on any exec in daemon code outside confine,
  sandbox and the in-sandbox agent host unless the call says
  `// exec-ok: <why>`; AGENTS.md/CLAUDE.md carry it as a hard rule. Not
  chosen: hardening host git with `-c` overrides alone (a denylist — git
  has many config-named commands, and `go build`/`git clone` run their own
  git); running tools inside the tile's own long-lived sandbox (no such
  process exists for most tiles, and a terminal's sandbox is the user's,
  not xbind's); refusing the features without isolation (they'd lose the
  Code panel in dev/no-isolate installs for no boundary gained).

- **D79 — An exposed endpoint takes many routes: exclusivity is per
  hostname, zone and host port, never per slot (2026-09-25).** A webhost tile
  behind the traefik tile could serve one hostname (or one wildcard zone)
  per `exposes` slot; several unrelated domains meant declaring `web`,
  `web2`, … in the manifest. Nothing in ING-1..6 asked for that: the storage
  was already a list (`Binding []BindRef`), traefik already renders a router
  per host, the listeners are keyed per listen address — the limit was one
  `len(binding) != 1` check plus consumers reading `FirstRef()`. The real
  exclusivity rules sit on the other side and stay: a hostname maps to
  exactly one (tile, slot) (ING-5 — an anonymous request reaches exactly one
  attributable tile), a zone is delegated once, a host port/proto relays to
  one slot. So each binding entry is a route validated on its own (source,
  exactly one of host/zone, listen), none repeated within the slot, each
  conflict-checked against every other slot; sources may mix (one host
  direct via `runtime`, others via a terminator). A registered host inside
  overlapping zones belongs to the most specific one, and lookup and the
  route list agree on it. API, additive: `POST /bindings {add:true}`
  appends one route (plain POST still replaces); `DELETE /bindings` naming a
  provider/host/zone/listen removes just that route; `GET /ingress` rows gain
  `routes` (scalars keep the first — old admin tiles read them) and `GET
  /bindings` gains `exposes` (every endpoint with all routes + its source
  options — `pending` still lists only unbound slots, so old prompts don't
  resurface bound ones). The org-admin gate (D26/D41) judges the DELTA — the
  route added or removed — so an org admin manages its own terminator's
  hostnames on a slot that also carries a workspace-admin `runtime` route.
  A route change no longer restarts the terminator (it re-reads
  `/ingress-routes` on its own; a restart would drop every site it serves).
  Not chosen: a `multi` flag on `exposes` (the manifest would have to change
  for what is an owner's routing choice); a separate route table (the
  binding carrying the route is the ING-1 property that keeps publishing a
  single owner-approved act). lan-ingress and stream interfaces stay 1:1 —
  they are consumer slots, not endpoints.

- **D80 — A tile opened from a right-click menu goes to the click, into the
  nearest free cells, and never pushes (2026-09-25).** Every open used
  `_freeSpot` (the first gap scanning the top row), so a tile picked from the
  canvas menu could land a screen away from where the person right-clicked.
  Now the menu remembers its click in the layout's logical px
  (`bx-canvas.gridPoint`, clamped into the visible pane). `spotNear`
  (grid-layout.js, node-tested) puts the tile's top-left cell under that
  point, pulled in just enough to fit the pane when it can (the way a menu
  flips at the screen edge). If that overlaps a grid card, the free spot
  whose top-left is nearest wins; ties go up, then left, so the tile tends
  to still cover the point. This applies to the canvas menu's Open tile /
  Create and to the tile menu's open lines on a closed tile. A sidebar
  row's menu point clamps to the canvas's left edge. Plain sidebar clicks
  keep `_freeSpot`, since they carry no point on the canvas. Not chosen:
  pushing neighbours aside, as a drop does (D66). Opening a tile should not
  rearrange the layout; pushing stays a drag gesture the person is steering.

- **D81 — The agent template drives runs with per-run actors on a durable
  inbox, owned by one process through a file lock; nothing polls
  (2026-09-25).** The old loop was one scheduling predicate (`readyRuns`)
  reached from boot, an in-process kick and a 1-minute heartbeat cron, with
  30 s per-run leases and 4 drive slots held for whole drives. That caused
  the reported stalls. A run with `settled_at`/`cancel_req` set was
  invisible to the predicate forever, so a chat that ever finished parked
  `blocked` on its next spawn. `depth DESC` slot order put new chats behind
  subagents. Every save orphaned leases that only the cron recovered (30–90
  s). A message sent mid-drive was overwritten. A join waited on every
  pending edge with no timeout.

  Now:
  - **Actors and inbox.** Every input is an `inbox` row, consumed exactly
    once in a transaction that also checks the engine epoch. A commit pokes
    the run's actor (at most one per run; a two-phase exit with a dirty
    flag, so a poke during exit is never lost). Timers exist only for a
    known instant (a yield, a subagent deadline) and are one-shot; a test
    forbids tickers in the package.
  - **Ownership.** One process owns the engine by holding `flock` on
    `<db>.engine`. Blue/green starts the successor first: it serves HTTP
    at once (handlers only write rows) and blocks on the lock. The
    predecessor cancels its calls on SIGTERM and exits, and the kernel
    hands the lock over, so a save re-issues the cut-off call in
    milliseconds. `settings.engine_epoch`, bumped at takeover and checked
    by every write, fences a stale process.
  - **Keep-alive.** The engine holds a request to itself open while work
    exists (the reaper counts only inbound requests). A process that exits
    with work pending leaves one `resume` cron job; there is no beat.
  - **LLM gate.** It is taken per model call, not per drive. Top-level
    calls go first, and children are capped at limit−1, so an interactive
    chat waits for at most one release.
  - **Steer at the step boundary.** Queued user rows are appended after
    the step's tool results and before the next call, which keeps every
    request valid. A plain answer with a message pending loops again.
  - **Links.** `links` rows replace `run_deps` edges for parent→child. The
    child writes only its outcome and the parent only delivery and
    demotion, so settle-vs-deadline races resolve either way. A parent
    waits only on its own step's calls. A deadline or a human message
    demotes the wait to background, and background answers arrive as one
    batched notice. A child's turn end cancels its descendants.
  - **Top-level runs are never terminal**: done, error and canceled resume
    on a message.
  - **The UI** is one SSE stream (view snapshot + cursor, per-root ring,
    coalesced drafts, `reset`/`bye`). The chat is template-owned lit
    modules: subagents render inline and the sidebar asks for roots only.
  - **Thinking** comes from a second wire, the Responses API
    (auto-selected for gpt-5/o-series, falling back to chat), plus the
    chat wire's reasoning deltas.
  - **Tool summaries**: every tool gets an injected required `summary`,
    stripped before dispatch.

  Not chosen:
  - Keeping leases with a faster recovery ticker: that still stalls by the
    tick and polls forever.
  - WAL: the db sits on gocryptfs, unprobed.
  - Making `xbin.Serve` wait for shutdown: that is an SDK change for every
    tile, left for later. The engine bounds its own unwind at 2 s.
  - A shared chat UI module in `/vendor`: the template is forked and
    merged by git (D50/D72), so its UI must be its own.

  Migration is additive and idempotent. The one-time cost: runs mid-drive
  in the old binary finish their legacy lease (up to 30 s) before
  adoption; an `engine:<gen>` lease mark keeps an old binary off new runs
  during the overlap.

- **D82 — Tile creation follows ownership, not path patterns; the path rule
  guards reserved names, scopes and leftovers (2026-09-25).** A non-admin
  with no `canCreate` pattern got 403 creating a personal tile. The D16
  patterns came from path-keyed permissions, which D24 ownership replaced;
  creating as an org already ignored them. Creation authority is now the
  owner: `resolveCreateOwner` decides it (`user:<self>` or an org with
  Create; `tileCreation: org-only` still forbids personal tiles), and the
  one gate `canCreateAt` applies `newTilePathOK` to every non-admin human,
  directly or attributed on an element call (the deputy clamp). Checked in
  this order:
  - **Reserved**: `tiles/` (the built-ins, at fixed paths the shell, the
    defaults and boot backfill trust), `root`/`shell` (chrome), and any `:`
    in a segment (grant-target/identity syntax).
  - **Scope**: the nearest `scope.json` root at or above the path must be
    absent or owned by the new owner. It is owned through its own owner
    entry, or, with none, when the new owner owns every tile in it. A new
    tile in a scope gets auto-approved same-scope grants, so this is a
    trust boundary. Tiles still never nest.
  - **Leftovers**: a path is a durable key and nothing prunes it when a
    directory disappears. Refused over:
    - grant rows naming it on either side;
    - bindings, instances and ingress hosts;
    - its vault;
    - another owner's entry;
    - other users' exact entries, org shares, an exact `defaultTiles` entry.

    The error lists them. The path's current owner is exempt.

  Org creation gains the rule too. It used to skip path checks, so a
  vanished `tiles/admin`'s `xbin:admin` row was claimable by any org
  member with Create. `canCreate` stays in `users.json` and the API, but is
  ignored (never break users: scripts keep working, a downgrade keeps
  them). The UI half: `bx-dialog` got an `error` alert, which the shell's
  *New tile* dialog uses; before, the refusal replaced the intro text and
  read as a hint.

  Not chosen:
  - Keeping `canCreate` as an override on top of the rule: another knob
    with nothing left to decide.
  - Scrubbing leftovers on create: that would let a non-admin request
    delete admin-set state.
  - Letting tiles nest under an owned tile: that means nested repos, and a
    parent's sandbox and dev layer holding the child.

- **D83 — Agent conversations belong to the person who starts them;
  chatting needs `read` on the tile, managing it needs `write`; privacy
  holds between users, not against the tile's operators (2026-09-26).**
  Every user who could open the template saw and could delete every run,
  and anyone could halt the agent or rewrite a schedule.

  **How ownership works:**
  - A run records `owner`, `visibility` (`private` | `team`), `team_role`
    and `origin`.
  - Access is resolved on the root: owner, then members (`run_members`,
    viewer | participant), then team visibility.
  - Unowned runs (legacy, the owner token, scripts) stay team-visible and
    belong to the tile's managers. The column defaults are exactly that
    shape, so rows an old binary writes during a blue/green overlap never
    disappear, and the backfill classifies them on the next boot.
  - The caller comes from xbind's `X-XBin-User` (D29).
  - View-as (D64) now reaches backends as `X-XBin-Viewed-By`, and the agent
    shows it only team-visible runs.

  **Enforcement:**
  - A declared route table (`routes.go`) gives every route a need.
  - `guard` answers 404 for runs the caller can't see (existence doesn't
    leak) and 403 for runs they can see but not change.
  - The list, the stream (per-subscriber filtering, with the ACL loaded
    before the hub lock; `revoked` on loss) and schedules are filtered by
    the same rule.
  - Managers (write/terminal on the tile) own the tile-wide settings and
    the halt, and oversee every automation without opening private runs.

  **Not chosen:**
  - Admins seeing everything: the user wanted admins excluded.
  - Per-user tile grants: a platform change, and anyone with write already
    controls the backend.
  - Enforcing privacy against write/terminal holders: impossible, since
    they can change or read the backend.

  The migration note says plainly who can still read everything.

- **D84 — `"alwaysOn": true`: the runner starts a backend at boot and
  restarts it after exits; there is no watchdog (2026-09-26).** A chat
  adapter holds an outbound connection (Slack Socket Mode), so no inbound
  request ever starts it or keeps it alive.

  **Why not self-hold plus a resume cron (D81's pattern)?**
  - The resume job is left only on a graceful SIGTERM; a crash, `kill -9`,
    OOM or host reboot leaves nothing to restart the tile.
  - A permanent watchdog cron is a ticker.

  **The flag, in `runner/alwayson.go`** (kept out of runner.go, which is at
  its budget):
  - Start triggers, all event-driven: boot (a step after watch), vault
    unseal, lifecycle enable, and each rescan (the flag appearing).
  - The reaper skips these tiles.
  - The crash watch schedules a one-shot restart after a backoff (1 s
    doubling to 5 min, reset after 10 healthy minutes).
  - The existing crash-loop breaker (3 exits in 30 s) still stops it until
    a save.
  - At most 2 builds at once, so a boot with many such tiles doesn't
    storm.

  **Not chosen:** a `cap:always-on` grant. A tile can already keep itself
  alive by holding a request to itself (D81), so the flag adds only
  boot-start and restart, and the owner stops it by disabling the tile.

- **D85 — Bus push subscriptions: xbind POSTs bus events to a backend's own
  endpoint as `xbin/bus`; at-most-once, like the bus (2026-09-26).** A
  backend could only consume the bus by holding `/ws/events`, which idle
  reaping severs; the docs said "use a cron sweep", which is a ticker. The
  agent's event triggers need a backend-side consumer.

  **The shape follows cron** (`internal/broker/bussubs.go`):
  - A component subscribes only itself (admins may name one) and picks the
    role deliveries carry. There is no escalation surface: the target is
    always the subscriber.
  - Delivery goes through the proxy as principal `xbin/bus`, so an idle
    backend starts lazily and Policy treats it like `xbin/cron`.
  - Stored in `data/bus-subscriptions.json`; a component backup carries
    its own.

  **What differs from cron:**
  - The subscriber must hold `reader` on the bus. It is checked at
    registration against the SUBSCRIBER's grants, even when an admin
    registers it, and again at every delivery, so a revoke stops delivery
    at once.
  - Fed directly from publish, never through the hub's 64-slot
    per-subscriber buffer.
  - A queue per subscription (256; the newest dropped when full), one POST
    in flight, a 2 min timeout, no retries.
  - A 100/s loop guard, for a handler that publishes to the bus it
    consumes.
  - A disabled subscriber's events are dropped; a subscriber that no
    longer exists loses the subscription.

  **Leftovers (the D82 follow-up):** creating a tile drops the path's cron
  jobs and bus subscriptions (in `assignOwner`, the hook all five creation
  paths share). D82 declined to scrub leftovers because grants and bindings
  are admin decisions. These are the removed tile's own delivery
  registrations: the new tile re-registers what it needs, and inheriting
  them would hand it calls it never asked for.

  **Not chosen:**
  - Retries or durable delivery: the bus is "go look", and truth lives in
    kv/sqlite. A durable queue is a different resource.
  - Holding the WebSocket from a backend: it needs a keep-alive, and
    alwaysOn (D84) for every consumer.
  - Delivering through the hub's subscriber channel: a slow backend would
    be dropped from the hub.

- **D86 — Chat channels: adapter tiles report facts, the agent owns
  sessions, access, lanes and replies; replies are a durable outbox the
  adapter pulls (2026-09-26).** Users want the agent reachable from Slack
  and similar platforms, with OpenClaw/Hermes-like session semantics.

  **The split:**
  - An adapter tile (Slack first) knows the platform. It binds to the
    agent's `inbox` provide (service `agent-inbox`), and the binding grants
    the custom role `channel`, which reaches only `/adapter/*`.
    `RoleSatisfies("admin","channel")` is false, so an adapter can't touch
    the rest of the agent.
  - The adapter never names a session or a lane. `sessionKey()` builds the
    key from the reported facts, and ids are %-escaped, so a `:` in a
    platform id can't forge a key.
  - A hello creates an **unclaimed** channel. The binding authorizes the
    calls; a manager's claim decides whose automation it is and its rules.

  **Session semantics** follow OpenClaw:
  - a session per DM peer, an optional shared `main`, and assistant
    threads per thread;
  - in groups, a mention starts a thread-scoped session and its thread is
    followed without further mentions;
  - no automatic reset by default; `idle:`/`daily:` policies are checked
    lazily on the next message;
  - `/new` rotates by hand, and old runs stay listed.

  **Security:**
  - DM pairing codes (8 characters, 1 h, at most 3 waiting, re-sent at
    most every 10 min) and group allowlists by default, plus a per-peer
    rate limit.
  - Channel conversations run in the **web lane**, since a reply is an
    egress. `privateLane` opens the private lane only to trusted peers and
    `trustedGroups`, and never with open DMs. Revoking trust rotates the
    session to a new web-lane run.
  - A per-run `Config.Deny` (default: `schedule`, `unschedule`,
    `skill_manage`) is enforced where specs are built, at dispatch and in
    `runTool`, and inherited by subagents.
  - The halt keeps messages and says "paused"; a channel message never
    lifts it.

  **Replies:**
  - Rows are written in the transaction that ends or parks the turn
    (`endTurnTx`, `ask_user`, the approval park). `NO_REPLY` and empty
    answers write nothing; errors are posted generically.
  - The adapter pulls over `GET /adapter/outbox` (SSE, no pings, `bye` at
    handover, a kick after takeover) and acks with the platform's
    message id: at-least-once, made effectively once by the adapter's
    own record.
  - A message typed into a channel run from the web UI is answered there,
    not posted: the reply target is the last message the run took in.

  **Not chosen:**
  - The agent pushing to the adapter: that needs a second binding and
    retry timers.
  - Adapters owning sessions: every adapter would re-implement the
    semantics and the access rules.
  - A channel running in the private lane by default: any reply could
    carry internal data out.

  **Adapters are built from a template, not shipped per platform.**
  `builtin-templates/agent-messaging-bridge` implements the contract
  once:
  - an event is stored before the platform is acknowledged, then
    delivered in order;
  - a reply's platform id is recorded before its ack, so a crash never
    posts it twice;
  - attachments both ways, splitting, retries, the typing hint, the
    linking page, alwaysOn (D84).

  The platform is one Go interface (connect and receive, send, format,
  fetch), which a coding agent writes in the copy's terminal from the
  template's `AGENTS.md`. A built-in console plays the platform until
  then.

  Not chosen: builtin tiles per platform. Every platform's API drifts, and
  each deployment wants its own variant. A Slack tile was built first and
  replaced before release: it proved the contract, and its generic parts
  became the template.

  **Files** go through the contract both ways:
  - In: `POST /adapter/files` stages an upload; the message names it; on
    delivery (the new `inbound.Adopt` hook, inside the delivering
    transaction) it becomes a session file attached to the message.
  - Out: the model's `attach_to_reply` tool (offered when `Config.Channel`
    is set) puts files on the turn's outbox row; the adapter downloads
    them from `GET /adapter/files/{row}/{i}` (its own rows only).

  **Linking a chat account to an xbin account.** The person pastes the
  code the bot gave them (unasked when they are new, or on `/link`) on the
  adapter's page while signed in. The page calls `POST /adapter/link`
  itself, and the agent takes the person from xbind's attribution
  (`X-XBin-User` with a level on the agent, and no view-as) — never from
  the adapter's word. A bridge therefore can't make anyone anyone: the code
  proves the chat account, the session proves the xbin account.
  - Linked, a person's DM is their own conversation (owned by them, listed
    with their chats) and their group messages carry their id.
  - A session changing hands rotates to a new run.
  - The owner can hear only linked people (`dm.policy: linked`,
    `groups.linkedOnly`) and trust them (`trustLinked`).
  - Not chosen: storing the mapping in the adapter. The agent is what must
    trust it.

- **D87 — Event triggers: bus events and tile pushes start agent work;
  data classes keep the lane firewall across triggers (2026-09-26).** Users
  want agents that react to things, like a deploy webhook or a calendar
  change, without anyone typing.

  **Sources:**
  - A bus the agent may read, through a bus push subscription (D85). It is
    registered per enabled trigger (`trig-<id>`); a missing `reader` grant
    becomes status `needs-grant` with the platform's message.
  - A push from a tile bound to the agent's `inbox` (`POST /adapter/event`
    on the agent-inbox contract), from its own `source_ref` only.

  **Firing** is one transaction on `trigger_events`: the dedupe (event id),
  the hourly cap (counted lazily), the halt (dropped, 503 to a push), then
  delivery via `deliverInboundTx`:
  - isolated: a new run keyed by the event;
  - persistent: the `trig:<id>` session;
  - conversation: into `targetRun`, if its owner can still post there.

  **Data classes:**
  - Event data is private unless its source declares it public. Bus data
    is always private; a webhook from outside is public.
  - A trigger that reaches outside, through the web lane or `deliver` (an
    announcement into a channel session its owner owns), takes public data
    only. This is checked on save and per event.
  - Runs on public data also get the channel deny list.
  - The goal quotes the data as data, flagged as outside data when public.

  **Not chosen:**
  - Polling (a ticker).
  - Holding a bus socket in the agent: that needs alwaysOn for every
    agent.
  - Letting the adapter name the target run.
  - Class inference from content.

  **Webhooks** are a builtin tile, `webhooks`, not a route on the agent.
  The agent never faces the internet; the tile publishes only `/hook/*`
  (ingress `paths`) and checks each delivery against its own vault
  secret. One tile then serves several agents (a multi `agents` slot),
  and the agent's inbox stays the one contract for everything that comes
  from outside.

- **D88 — The personal plane: per-user switches, workspace personal
  defaults, and self-approval on tiles you own (2026-09-26).** D82 let every
  non-admin own tiles, and admins had no per-user say in what that meant.
  Everything orgs had for their tiles (permission sets, network sets,
  allowance, ceiling rows; D26/D28/D54) stopped at the `org:` prefix. A
  personal tile had no default egress, and only a workspace admin could
  approve or wire anything on it — its owner couldn't even revoke. Nothing
  in the new-account seed could hold a freshly joined SSO user back.
  - **Two switches per account**, both off by default:
    - `noPersonalTiles` is `org-only` for one user. It is checked where the
      workspace policy is: every creation path, and transfers into
      `user:<self>` (which bypassed `org-only` until now).
    - `noTerminal` caps the account at `write` on every tile, whatever the
      source (owner, org admin, org level, exact entry, share, default). So
      no shells, no agent sessions, and — since logs follow terminal level —
      no backend logs. Switching it on kills their live sessions.
  - **Sets on users.** `sets` and `netSets` attach permission and network
    sets to a user, for the tiles they own. The workspace
    `personalDefaults {sets, netSets}` is a live layer unioned with every
    user's own (D54's union rule), empty by default.
  - **What the plane does:**
    - permission sets: `Policy` rows cap the owner's tiles; `Allow` entries
      are the owner's allowance; `termApi`/`termNet` reach the user, as an
      org's sets reach its members;
    - network sets: their union is the **personal network**. It is the
      default egress of the owner's tiles (the `personal` net ref), a
      terminal scope there, and `net:<rule>` allowance entries.
  - **Personal network sets are not a ceiling**, unlike an org's. A live
    default must never strand wiring a workspace admin already made on a
    personal tile; a `deny net` policy row still caps one.
  - **Self-approval.** A tile's user-owner approves grants and bindings on
    it within their allowance (the D26 edge, for the owner). Always:
    revoke, unbind, `net:none`, `net:personal`, and wiring to another tile
    they own. Never: `xbin:*` and `set:` refs. This resolves
    ownership-ux-review #8 and retires "owners don't self-wire".
  - **Terminals on a personal tile** get a union, not a replacement (the
    user's call, unlike D54 on org tiles): the owner's `personal` network
    (the default), plain `internet` when the user has `termNet` or the sets
    hold full internet, each of the owner's sets as a `set:<name>` scope to
    narrow further, and `none`. Revisits D54's "termNet governs personal
    tiles" and D65's rejected per-user sets: the network is still the
    tile's (a terminal on someone else's personal tile uses its owner's).
  - **The seed** (`newUsers`) carries both switches, OR'd in, and sets,
    unioned — it can only restrict a fresh account; an admin lifts a
    switch per user.

  **Not chosen:**
  - Seed-only defaults: existing users would never see them.
  - Personal network sets as a ceiling: see above.
  - A per-user "no terminal on personal tiles only": the user wanted every
    shell.
  - Group-sync rules setting the switches: seeding covers new SSO users;
    left for later.

- **D89 — VM sandboxes: a Firecracker microVM inside the namespace sandbox,
  the binds as FUSE over vsock, per terminal and per backend (2026-09-26).** The
  namespace sandbox shares the host kernel. Its residual risk is a userns
  kernel escape (plans/containers.md), and some work needs a real root
  anyway (docker, kernel knobs). Design: plans/vm-sandbox.md.

  **Chosen:**
  - **Firecracker, not Cloud Hypervisor.** Minimal devices, static binary,
    and `MAP_PRIVATE` snapshot restore for copy-on-write templates later.
    Cold boot is already ~150 ms, so templates can wait.
  - **The namespace sandbox is the jailer.** Firecracker's own jailer
    needs root; ours is rootless. It keeps its binds, masks, netns and
    relay. Its root becomes a bare tmpfs; its lockdown becomes the five
    file caps, the block-list and the mount guard.
  - **FUSE over vsock for files, caching hard.** Firecracker has no
    virtio-fs.
    - 9P over vsock, the first choice, measured 170× native on `git
      status`: the kernel's `trans=fd` client makes each lookup a ~250 µs
      round trip, and none of its cache modes is both coherent and fast.
    - The guest now mounts FUSE and pumps `/dev/fuse` over vsock to a
      go-fuse server in the jail. The host kernel enforces read-only binds
      and masks, and the guest can only walk to the exports, one
      beneath-only `openat2` per step.
    - The guest caches entries, attributes, listings, pages and negative
      lookups for an hour, with zero-message opens. Host inotify turns
      outside changes into invalidations.
    - Result: warm work runs at local speed (`git status` 2.7×, `find` 1×)
      and a first touch is one ~0.13 ms round trip.
  - **A routed netns.** The TUN stays, the TAP is the guest's, and there is
    no NAT or proxy ARP. The relay and its policy are untouched; the guest
    owns 10.0.2.15.
  - **A separate VM disk per tile** for root filesystem changes, in the
    tile's layer: its lock, base pin and Reset.
  - **A workspace admin switch, then open use** (terminals and backends
    separately). A VM is stronger isolation, so the gate is its memory,
    capped by count and budget. (The installer now turns it on where it
    was never set: D110.)
  - **`"vm"` in a manifest fails closed** when VMs can't run: no silent
    namespace fallback (D78).

  **Refused in a VM:** host networking, provider splices, lan-ingress legs,
  GPUs, `setup`.

  **Not chosen:**
  - virtio-blk images of the tile dir. xbind reads, serves and watches
    those files live.
  - Sharing the namespace upper with the VM through fuse-overlayfs over the
    file transport. Slow, with fragile ownership.
  - Serving the file server from xbind itself. It would resolve
    guest-supplied paths as xbind (D78).

- **D90 — Emulated VMs where KVM isn't usable: QEMU (TCG) runs the same
  guest, vhost-device-vsock carries its vsock (2026-09-26).** Firecracker
  needs KVM, and most small cloud VMs have no nested virtualization, so D89
  left VM sandboxes "unavailable" exactly where people start. Design:
  plans/vm-sandbox.md §Emulated VMs.

  **Chosen:**
  - **The same guest, a different VMM.** A static, minimal QEMU (microvm,
    virtio-mmio blk/net/balloon/vhost-user-vsock, TCG only) boots the same
    kernel, initramfs, erofs image and VM disk on the same TAP, in the same
    namespace jail, under its own `-sandbox` filter too.
  - **vsock via vhost-device-vsock**, which speaks Firecracker's hybrid
    Unix-socket protocol — so the shim past the VMM's start, the agent, the
    file server, the gateway and the listen bridge are unchanged.
  - **Automatic, visible fallback:** KVM ⇒ Firecracker, else emulation when
    shipped, else unavailable. `emulated` + `note` in every status; the
    toggle says it's slower. `XBIN_VM_ACCEL` forces either for tests and
    ops. No new policy switch: the admin's VM switch already gates cost.
  - **Three boot hazards of the emulated board, each found by stress and
    fixed at the root:**
    - *TSC calibration:* under TCG the guest's TSC is the host's; PIT-based
      calibration fails now and then under emulation jitter, and the
      fallback waits forever for a PIT tick. The shim measures the host's
      TSC and passes `tsc_early_khz` (~2% of boots hung; 0 of 336 after).
    - *Early vsock connects:* a host connection made before the guest's
      vsock driver is up can wedge vhost-device-vsock for good (guest up,
      agent listening, nothing gets through). The agent now calls the host
      on a ReadyPort once it listens, and the shim connects only then — for
      Firecracker too, which just stops polling.
    - *Initrd over the ACPI tables:* microvm loads the initrd flush against
      the top of RAM, in the page holding the ACPI tables, so an agent build
      whose initrd tail landed past 0xd00 there corrupted the DSDT and the
      guest summed "gigabytes" of table forever — deterministic per build,
      ~19% of sizes. The initrd is padded to whole pages.
    Result: 60/60 VM-backend cold starts on a KVM-less 4-vCPU VM (each ~2 s),
    60/60 local starts through the shim.

  **Not chosen:**
  - gVisor (runsc) for KVM-less hosts. A good sandbox, but not a VM: no
    guest kernel for docker or kernel knobs, and a second, different
    integration.
  - Host vhost-vsock with QEMU: needs the module, root-owned device access,
    and CIDs are global across the host.
  - virtio-serial with our own multiplexer: rewrites the guest protocol for
    one VMM.

- **D91 — Native tile UIs: a tile's `native.js` runs in an xbind-generated
  runtime document in a hidden WebView, renders a small vocabulary through
  `/vendor/xb-native.js`, and the app draws the tree natively
  (2026-09-26).** Design: plans/native.md §7–§11 (its decisions 1, 2, 9 and
  10), the wire contract native/spec/tree.md, the builder reference
  docs/native.md. Web frontends are untouched; a native UI is opt-in per
  tile, and every tile still opens in the app as its web page.
  - **The engine is a hidden WKWebView per open native tile** loading
    `/c/<tile>/?native=1`: the whole web platform with JIT, the same
    `xbin-client`, sandbox, import map and module loading as the tile's
    page, so the same modules serve both views.
  - **xbind generates the runtime document**, not the tile: it carries the
    page's D4 `<head>` injection from the one `headInjection` (identity,
    sandbox, interfaces, the frame-token rule of D95), even under
    `inject: false`, is authorized exactly like `index.html` (no new access
    surface), and loads the entry with the runtime's `boot()` so a module
    that fails to load reports `{op:"error", kind:"module"}` at once.
    Trusted chrome has none (it acts as the human).
  - **Opting in** is a `native.js` next to `xbin.json`, or `"native":
    "./path.js"`; `false` opts out. The key is never a manifest error — any
    other value is ignored, and old xbinds ignore it. Saving the entry
    reloads the tile without restarting its backend, except where a backend
    could read the file (an undeclared `native.js` under `node`, a `go`
    backend built from the tile root).
  - **One schema:** `web/xb/vocab.js` defines the vocabulary; the runtime
    validates against it, `native/spec/vocab.json` is its checked-in export
    for Swift (a test keeps them equal), docs/native.md's tables are
    generated from it.
  - **The wire format:** nodes `{k, t, p, e, c}` (absent `p/e/c` = empty),
    keys slot-based and opaque; `mount`/`patch` carry a sequence `n`; ops
    `set / unset / events / insert / remove / move`, applied strictly in
    order, moves within the parent, fewest moves by a longest increasing
    subsequence; `xbn.remount()` recovers a lost tree. Runtime → app
    messages are **JSON strings** (a WKScriptMessage body goes through
    NSNumber, which merges booleans and numbers), and the app parses them
    with its own strict JSON (never JSONSerialization/JSONDecoder).
    `error` kinds `unsupported` / `exception` / `module` fail over to the
    web page; `uncaught` and `diag` are only reported.
  - **Controlled props** use a shadow tree and the sequence number: a
    keystroke made before a reset arrived can't mask the reset, a bound
    prop without a handler is read-only, and the renderer shows its own
    copy until the tile restates a value (XbinCore `TreeDelta.restated`).
  - **Validation degrades per prop**; only what the app cannot draw (an
    unknown primitive, a prop newer than the app's revision, a missing
    feature) sends `unsupported` before the tree, so the app falls back
    before drawing. Markdown is lexed in the runtime into a sanitized token
    subset (the web kit's policy) and drawn natively.
  - **`xbin.native`** (`caps`, `supports`, `meta`, `copy`, `share`, `open`,
    `state`, `saveState`) is the app's whole API for tile code: the app
    injects `window.xbin = {native: {caps, state}}` before `xbin-client.js`
    freezes `window.xbin`. No device APIs, ever (App Review 4.7.2).
  - **The contract is additive-only** (docs/compat.md rule 10 and "The
    native app contract"): the vocabulary, the tree format, `xb-native.js`,
    `xbin.native` and the bridge only grow; a new prop raises its
    primitive's revision, a new value an app must support names a feature.
  - `X-XBin-Client: app/<version>` documents carry `xbin-ws-origin` (a
    custom-scheme page has no WebSocket origin to derive); browsers get
    byte-identical documents.
  - **Not chosen:** a JavaScriptCore `JSContext` (no JIT for embedded JSC,
    polyfills and a module loader to maintain); an app-bundled runtime page
    (the tile's identity and sandbox would have to be recreated in the
    app); a native markdown parser as the tiles' renderer (two parsers
    drift); failing the whole view on any unknown prop.

- **D92 — The native client is built and verified without a Mac: fixtures
  are the renderers' contract, a Lit reference renderer, Foundation-only
  Swift packages tested on Linux, and script-driven Apple CI
  (2026-09-26).** Dev loop: native/AGENTS.md; plans/native.md §3, §6,
  §12–§17, §21 (its decisions 3, 5, 6, 8, 11 and 12).
  - **Fixtures are the contract** (`native/fixtures/<name>/`: `native.js`,
    a scripted `xbin` in `data.json`, the tree in `expected.json`). `make
    native-check`, part of `make check`, renders each in node on a virtual
    clock and fails unless the fixtures together exercise **every** item of
    the vocabulary — a vocabulary change comes with its fixture.
    Interactions name nodes by selector and must match exactly one; a
    pinned time zone or locale runs in a child process (ICU fixes them per
    process); `expected.json` has one canonical, reviewable layout. The
    `tile-*` fixtures import the shipped tiles' `native.js`, so they can't
    test a stale copy.
  - **The Lit reference renderer** (`/vendor/xb/render.js`,
    `preview-host.js`, `fixture.html`) is served for previews and `bx
    preview` only, never imported by tiles (plan decision 11): one
    LitElement with a single shadow root, an `xb-<prim>` element with
    `data-k` per node, a vendored Lucide icon subset. It plays the app in a
    browser — and lends `xbin.fetch` (the tile's token) only to the
    workspace's own paths.
  - **Everything that doesn't draw is Swift that tests on Linux:**
    `XbinCore` (JSON, the tree and strict patching — a divergence means the
    web page, never a guessed tree — the bridge, deep links, device login,
    and `Client/`, the app's workspace logic), `XbinTerm` (the `/ws/term`
    codec and session; the predictive echo engine ported and held to
    `web/term-predict.js` by a differential trace that `make js-test`
    guards), `XbinAgent` (the ACP session model, held to the web Agent
    tab's own output by `native/tools/agent-parity.mjs` over captured
    sessions), and `XbinRenderer`'s model target. The SwiftUI views sit
    behind `#if canImport(UIKit)` and are type-checked on Linux against SDK
    stubs (`native/tools/swiftui-stubcheck`). `make swift-test` runs the
    four packages.
  - **The app** (iOS 26, XcodeGen, never a committed `.xcodeproj`): one
    `WKWebsiteDataStore` per workspace; web tiles through the `xbin-ws`
    scheme handler, which always sends the tile's own frame token, never
    cookies, and follows redirects only on the workspace origin; a
    top-level page with WebKit's `xbin` handler counts as embedded for
    `xbin-client.js`, so `xbin.dialog` / `xbin.window` reach the app;
    SwiftTerm behind XbinTerm; one Face ID prompt per new session,
    single-flight re-sign on a 401 (plan decisions 5, 6); iOS semantic
    colours with the amber tint darkened for text contrast (decision 12).
  - **The only Apple toolchain is GitHub Actions** (`ios.yml`, `runs-on:
    xcode-27`, feature branches only): three independent jobs (packages,
    app, snapshots). The logic lives in `native/ios/scripts/` (bash 3.2),
    shellchecked and dry-run on Linux against fake Apple tools
    (`ci-local-check.sh`), so a CI round trip is never spent on a typo.
    Snapshots are written to `SNAPSHOT_DIR` from a hosted offscreen window
    (ImageRenderer draws UIKit-backed views as placeholders).
  - **Not chosen:** a Mac in the loop (none exists; one may come later
    over ssh); pixel comparison in CI (contact sheets are reviewed by
    eye); per-primitive shadow roots in the reference renderer; logic in
    workflow YAML.

- **D93 — Device login, and credentials bound to the login that minted
  them (2026-09-26).** plans/native.md §5 and §20 rows 1–2 (its decisions 4
  and 7); docs/auth.md §Device login; native/spec/device-login.md.
  - **The app's credential is a P-256 key in the Secure Enclave** (Face ID;
    the passcode when no biometrics are enrolled), one per workspace; the
    server stores its SPKI. It signs `"xbin-device-login-v1\n" + origin +
    "\n" + deviceId + "\n" + nonce` for a single-use 60 s nonce. The origin
    is **fixed at enrollment** (`--external-url`, else the enrolling
    browser's origin): the app signs what it was told, a request's Host is
    client-controlled, and a signature for one server can't be replayed at
    another. Passkeys were rejected: they need an associated domain per
    relying party, which a generic client of self-hosted servers can't
    ship.
  - **A device login opens the same human session** a browser login does
    (same TTLs, same reach; `via` device|app), carried as a
    transport-bound `Authorization: Bearer`.
  - **Enrolling is a step-up**: a code needs a sign-in under 10 minutes old
    or the password again — a device outlives the session that adds it, so
    a stolen cookie must not become a permanent credential.
  - **The IdP stays in charge of accounts it alone lets in** (D53): in
    SSO-only mode every non-admin, and whenever SSO is configured every
    account without a password (SSO-provisioned, or an admin by SSO group
    — "admins keep password sign-in" does not hold for them; mixed mode is
    the default once SSO is set up). Such a device signs in only while the
    user's last SSO sign-in is within the session max TTL, and the session
    ends when that window does (`403 {reauth:"sso"}`), so removing someone
    at the IdP still ends their access within the session TTL. An account
    with a usable password is not bound (the IdP is not its only way in).
  - **Sign-out-everywhere keeps enrolled devices unless asked**
    (`?devices=1`, `bx user signout --devices`; the console asks) — the
    route has always meant "they can sign in again"; a password change can
    remove the caller's other devices.
  - **Frame tokens are bound to their login**: a fifth field names a
    credential generation — `s.<handle>` (a login session; a random,
    non-secret name), `u.<epoch>.<n>` (no session behind the mint;
    sign-out-everywhere bumps `n`), `o.<keyed hash of the owner token>` —
    renewals copy it, and the token verifies only while it lives. A tile
    in use slides its login's idle window (at most once a minute; the
    30-day cap stands). Generation handles (never credentials) persist in
    `.xbin/frame-gens.json`, so a restart still signs everyone out while
    open tiles keep working until their login would have expired;
    anything that ends a login saves synchronously. Pre-binding tokens
    verify for one hour after boot and renew into bound ones. Rejected:
    persisting sessions (credentials on disk, a changed restart
    semantics); letting every open tile die on each upgrade.
  - **Asset tokens take the same generation** as the frame token minted
    into the same document (D95), so both die together.
  - **Per-device state follows the device:** removing a device (on its own,
    `?devices=1`, a password change with `removeDevices`), its device
    session signing out, and rotating the owner token drop the matching
    push registrations (D94).

- **D94 — Push: a relay that sees no content, registrations that follow the
  user and the device, and `POST /api/xbin/notify` (2026-09-26).**
  plans/native.md §14 (its decisions 13 and 15), native/spec/push.md,
  relay/README.md, docs/protocol.md §Push notifications.
  - **Sealed end to end**: ephemeral X25519, HKDF-SHA256 salted with both
    public keys (info `xbin-push-v1`), AES-256-GCM, envelope `{v, epk, n,
    ct}`, published vectors checked by an independent implementation.
    Simpler than RFC 9180 HPKE and direct in CryptoKit; a new format
    changes `v` and the info. The relay (`relay/`, stdlib only, its own
    module) maps opaque handles to APNs tokens and never sees a title;
    collapse ids reach it only hashed.
  - **The relay key is permanent per workspace**; turning push off keeps it
    and every registration, and only an explicit rotate (or another relay,
    or a new key) mints a relay workspace — the relay binds each handle to
    the first workspace that pushes to it, which is what lets an app cut a
    workspace off. Registrations delivered under an old key show
    `needsNewHandle` and are skipped, never deleted.
  - **Only the relay's own error codes change a registration** (410 removes
    it; `handle_bound` / `handle_unknown` mark it stale): a 403/404 can come
    from a proxy.
  - **Registrations follow the user and the device**: sign-out-everywhere,
    disable and delete drop the user's; removing a device, its session
    signing out and owner-token rotation drop that device's (D93); nothing
    reaches a disabled user, and a reused user id gets nothing. **A
    registration is bound to the login that made it**: a device session
    registers under its own device-login id only, and any other login's
    registration (the app before enrolling, a browser, the owner token)
    carries that login's credential generation and goes when it ends —
    otherwise a stolen session could plant a registration under a made-up
    id, with an attacker's handle and key, that survives removing the
    device and the session's own end (the threat D93's step-up closes for
    device keys).
  - **`/notify`**: backends (instance principals) notify any reader of the
    tile; frontends only the person using them — a frame token sits in
    every reader's browser. Budgets are separate so tiles can't starve
    agent permission prompts; over a person's shared tile budget a
    notification is dropped with 202 (a 429 would tell one tile what others
    send). A frontend's or terminal's budget is per tile and person (a
    reader must not spend the backend's), and the per-person budgets count
    relay posts, one per device (at most 10), with registrations limited
    too: the relay's workspace budget is shared by everyone, and per-note
    charging let one person's 20 devices spend it 20 times over. The relay
    charges a workspace only for pushes to handles it may use. Links stay
    inside the tile, checked percent-decoded. The SDK
    helper is `xbin.NotifyUser` (`xbin.Notify` is the existing toast).
  - **Agent pushes** wait a 3 s grace period (a request answered at the
    desk raises nothing); a finished turn pushes unless the user cancelled
    it.
  - **Relay state is bounded**: a snapshot plus an fsynced journal written
    outside the store lock, caps (2M handles, 200k workspaces), retention,
    per-/64 and per-/48 limits, and device tokens verified with a silent
    push before a handle is stored. Retention includes keys unused for 180
    days — a key probed once was kept forever, so an anonymous flood could
    fill the cap for good — and xbind checks its key at start and daily
    (keeping it; and an environment key, never probed before, reports a
    forgotten key at once, with a hint for where it comes from). Every
    limit is a flag, and `-registration-tokens` closes registration to the
    operator's keys when anonymous opt-ins are abused (not chosen yet for
    the public relay: proof of work).
  - **Open:** who operates the public relay and at which domain (plan
    decision 13); nothing is deployed, the relay URL is configuration.

- **D95 — Strict tile asset gating: `--tile-assets=legacy|tokens|origins`,
  `legacy` the default for one release, three security fixes in every mode
  (2026-09-26).** plans/tile-asset-auth.md (its decisions 1–4, approved by
  the owner); docs/auth.md §Tile asset gating; migration note
  docs/changes/2026-09-26-tile-asset-gating.md.
  - **Two mechanisms**: `origins` (A) runs each tile on its own origin
    `t-<id>.<tiles-domain>` (id = a keyed hash of the path, so tile names
    never reach DNS, SNI or certificate logs; tiles gain their own storage);
    `tokens` (B) credentials relative URLs with a path-scoped asset token
    under an injected `<base>`. This release ships both plus detection
    (`bx doctor`, `GET /tile-assets`), the codemod (`bx fix assets`) and
    xbin-client diagnostics; the next deletes the credential-less rule.
  - **The origins exchange is a one-time, session-bound ticket** minted by
    the workspace (`?xbin_ticket=`), not the frame token — a cookie traded
    for a frame token would survive sign-out on a shared computer. The
    tile cookie `__Host-xbin_tile` lives as long as that browser session.
    The ticket is also bound to the **redeeming browser** (the
    state-parameter defence): the tile origin first keeps an exchange state
    in its own cookie and bounces to the workspace with it (`?xbin_begin`,
    `?xbin_state`), which mints the ticket for that state — without it, a
    sibling tile origin (same-site, so Fetch Metadata can't tell it from the
    shell) could plant a ticket minted from its writer's session and sign a
    reader into their account on the tile (login CSRF). The bounce is
    skipped when the tile cookie is already bound to the session.
  - **The workspace treats tile origins like the cross-site frames they
    replace**: it ignores its session cookie on requests a tile origin
    starts (all same-site ones but top-level navigations), tile pages may
    be framed only by the workspace and themselves, chrome only by the
    workspace, a cross-site open goes through a same-origin interstitial,
    and the session cookie becomes `__Host-xbin_session` in origins mode
    (switching signs every browser out once). On a tile origin backends
    never see the tile cookie and can't set cookies.
  - **Legacy is not byte-for-byte**: in every mode the `/c/` plane serves
    the very file it checked (no symlink to xbind's files, reserved trees
    or outside the workspace; FIFOs refused), a sandboxed tile's
    non-document files carry CSP `sandbox`, and **a frame token is minted
    only for a human or the tile itself** (its frame or terminal token, or
    an `xbin.window` sub-path of it; never its backend, whose instance
    token names no person — a token minted for it read as the owner's
    frame, owner reach on every tile) — plus navigations within one tile
    tree when everyone who can write the page navigating can write the
    target (a parent's writers write its whole tree; a nested page's
    writers alone, under per-path RBAC, would otherwise lift its parent's
    token by replaying "navigation" headers outside a browser). Another tile
    that fetches `/c/<tile>/` or its runtime document (D91) through its
    user's access gets the HTML without a token; before, any tile could
    lift e.g. the admin tile's token out of the HTML.
  - **Asset tokens are bound to the loading login's credential
    generation** (D93) and die with it; RBAC is checked live on every load
    for the minting tile and the tile loaded.
  - **Not chosen:** keeping the IP heuristic behind a flag; enforcing in
    this release; renaming the session cookie in every mode (a workspace
    that never uses origins would sign everyone out for nothing); an
    ancestor-only exception for multi-page tiles (back-links and siblings
    are the common case).

- **D96 — The agent template: one model, thin views, and a native view
  (2026-09-26).** plans/agent-template-native.md (its decisions 1, 2 and 4);
  builtin-templates/agent/API.md.
  - **`model/` owns state and behaviour** (plain ES modules: no lit, no
    DOM, no dialogs, no `location`; a node test enforces it and runs the
    model against a scripted backend). `createApp()` is the one wiring and
    the web drives it too — a facade only the native view used would leave
    two copies of every flow, and the web's tests would not exercise it.
    View-flavoured classes are injected (`createApp({Session, AutoPage})`);
    views keep confirmations and report failures. The old module paths stay
    as one-line re-exports; instance patch series (D72) retarget moved
    hunks to `model/<name>`.
  - **Parity is enforced**: `model/features.js` lists every UI feature by
    key; each view declares what it implements (`web-features.js`,
    `native-features.js`), and a test fails on a missing key unless
    `DIFFERENCES.<view>` lists it with a reason.
  - **The native view** (`native.js` + `native/`) draws the same model one
    surface at a time: home, a conversations drawer (`sheet
    edge="leading"` — an additive prop; a renderer that ignores it shows
    an ordinary sheet), the full-screen chat, the composer with app
    uploads, pushed screens for tools, settings and automations. The
    navigation is derived from model state on every paint, so deep links
    and stream events land where the web's would.
  - **Backend additions for a phone, all opt-in**: `GET /stream?deltas=1`
    (draft text as appended pieces, computed per connection at write time
    so the hub's coalescing, replay and resync are unchanged; `at` counts
    UTF-16 units, a mismatch reconnects), paged `GET /runs/{id}/view`
    (`limit` counts messages, steps follow by time, compacted messages are
    left out and counted), and `GET /runs/{id}/thumb` (standard library
    only; header-first size caps; EXIF orientation). The web passes none of
    the options and is byte-identical.
  - **Not yet:** the plan's per-block fold cache and device performance
    targets; "Needs you" reaching the phone as a push (now possible with
    `POST /notify`, D94).

- **D97 — Agent sessions for the app, and agent events for humans only
  (2026-09-26).** plans/native.md §13 and §20 rows 7–8 (its decision 14);
  docs/protocol.md; migration note
  docs/changes/2026-09-26-agent-events-humans-only.md.
  - **`term` and `session` events reach humans only**: the session's owner
    (their browser and app sessions), admins, and a shell's terminal token
    for sessions on its own tile — never a frame, instance, cron or bus
    principal, and never an empty user id. Matching on the home key alone
    let every tile a user opened follow their transcripts, tool output and
    patches, and every backend (whose instance token has no user, read as
    "owner") follow the owner's — a pre-existing D73/D74 leak that the new
    status op widened (plans/auth.md default-deny for element principals).
  - **An agent does not drive itself**: prompt, permissions, elicitations,
    options, restart and diff answer 403 to any agent session's own
    sandbox token — on every session, not just its own — and `POST
    /term/sessions` refuses it, so it can't approve its own permission
    requests or change its own settings, directly or through a sibling
    (one it opens in a bypass mode with wider pickers, or one already open
    on the tile, the two answering each other). A shell's token on the
    same tile still drives it (`bx agent`). Rejected: clamping a sibling
    to its creator's mode and pickers (two agents opened by the person
    would still approve each other's requests).
  - **Prompts carry files**, written inside the agent's sandbox (or a
    directory xbind owns and removes when isolation is off) and named by
    path; images go inline up to 3.75 MiB each and 4 MiB per prompt and
    degrade to files past that instead of a 413 — providers count the
    base64, and the agent keeps inline images in every later turn. The
    prompt slot is taken before the body is decoded, so concurrent prompts
    fail fast holding nothing.
  - **Full diffs** re-diff remembered tree pairs from the private snapshot
    dir, confined (D78); one per session and two per daemon, waiting
    rather than failing.
  - **Status changes ride `term` op `status`** `{id, user, status, pending,
    questions, turn}`, coalesced and never repeated, so an inbox follows
    sessions without reading logs; clients that re-list on every `term`
    event keep working.
  - **The login rides every status while signed out**, partial ones
    included, as docs/protocol.md always said — before, the next commands
    or usage update dropped the sign-in prompt.

- **D98 — `bx native tree`, `bx lint --native`, `bx preview --native`: the
  tile in headless Chromium through an embedded probe, bx's credential
  lent only to the tile's own document (2026-09-26).** plans/native.md §16;
  docs/bx.md §Native UIs.
  - **The tile runs in its real runtime document**
    (`/c/<tile>/?native=1&preview=1`) — the import map, `xbin-client`,
    frame token, sandbox and module graph the app loads — with the
    reference renderer's preview host playing the app. bx embeds
    `cmd/bx/native-probe.mjs` and runs it with node and Playwright's
    Chromium (both in the terminal rootfs); without them `tree` and
    `preview` fail with an install hint and `lint` keeps its static checks.
    A node-only runner (hack/xbn) can't load the tile's modules from xbind
    or answer "what does it render against its live backend".
  - **A loopback proxy lends bx's credential only to GET/HEAD of the probed
    tile's own document and files** — one proxy and one browser per tile,
    run one after another: a single proxy for a multi-tile run lent to
    every probed tile's paths whichever page asked, so one tile's page
    lifted another's frame token; the page never holds bx's token, and
    the tile's API calls use its frame token as in the app. From a tile's
    terminal only that tile previews with live data (another tile's
    document yields no frame token, D95) — `--data` replays a fixture
    instead.
  - **Static lint reads through xbind** (overlays and the served import map
    are what gets checked) and leaves the vocabulary to the runtime's own
    diagnostics, so bx carries no copy of `vocab.json` that could drift.
  - A run settles on quiet (no new messages for 400 ms, no non-streaming
    request in flight); the first tree gets twice the app's 5 s fallback.

- **D99 — The xbin app follows its workspaces over `/ws/events`, and each
  window navigates only itself (2026-09-26).** plans/native.md §7.7, §26;
  docs/protocol.md (`/ws/events` with a bearer session); native/AGENTS.md.
  - **One socket per workspace a foreground window shows**, closed in the
    background, authenticated with the workspace's device session as a
    bearer — a human principal, held in Swift, never handed to a web view
    or a tile. The pure policy is XbinCore's (`EventSocketPolicy`: 0.5 s
    doubling to 15 s with jitter, reset on open; one re-sign on a 401, a
    second parks; 403/404 park until the next foreground); a reopen after
    a gap re-lists sessions, catches agent feeds up and re-reads whoami.
  - **Frames route to hooks**: `reload` → the most specific open tile, as
    the web shell picks it (a native runtime remounts — its canvas islands
    reload with it — a web tile reloads), in every window showing it;
    `session` → the agent feed; `term` status → the Needs-you row in place
    (TermDirectory decodes `questions`, and no status — `error` included —
    drops a row, as in bx-frame); `branding`; `native` → a whoami re-read
    (D101).
  - **Windows are tabs.** Every window (SceneModel) owns a `WorkspaceNav`
    per workspace; a screen acts only on its own window's
    (`@Environment(WorkspaceNav.self)`, handed to what it creates:
    `WebTileController.nav`, canvas islands through `TileHatches.nav`,
    `AgentScreenModel(nav:)`). The single-window API that resolved to "the
    focused window" was removed so the compiler flags old callers: a page
    in a background Stage Manager window calling `xbin.window` after a
    fetch would otherwise push over another window's tile (spoofing). Only
    input from outside every window (a notification tap, an `xbin://`
    link) goes to the focused one. Per-window state restores through
    `@SceneStorage`; Handoff offers the current tile or session.
  - **Tile meta** (`xbin.native.meta({icon, badge})`) is remembered per
    workspace (TileMetaStore, cached) and shown in the navigator and the
    switcher. **Haptics** fire where a person acts (send, approve, a
    failure) or a watched turn settles or starts waiting on them.

- **D100 — Signed-in Safari: a one-shot device-session web ticket,
  confirmed on a "Continue as" page (2026-09-26).** docs/auth.md §Device
  login; native/spec/device-login.md §6; docs/protocol.md.
  - **Only a device-key session mints** (`POST /api/xbin/web-ticket
    {next}`; not the app's password/SSO session, a browser, a tile, a
    terminal or the owner token): the handoff trades a Face ID sign-in for
    a browser one, never something weaker. The ticket lives 60 s, is spent
    by its first open, and is bound to the minting session's generation
    (D93), so sign-out, device removal, sign-out-everywhere, disabling and
    the SSO window void it. The browser session it opens carries the
    device id and keeps the device login's created time and cap — it ends
    with the device and can't launder an old login into a fresh one. `next`
    is a same-origin path; 10 per minute per device; audit-logged.
  - **A GET never signs a signed-out browser in** (login CSRF): anyone can
    mint a ticket for their own account and hand the link over, and a link
    opened from Messages, Mail, a QR code or the address bar arrives with
    `Sec-Fetch-Site: none` exactly like the app's own open, so Fetch
    Metadata can't tell them apart. The GET shows "Continue as <name>"
    (login, email, the landing path; no script, `frame-ancestors 'none'`,
    `strict-origin` so the form's Origin isn't `null`), and only its button
    opens the session: `POST /login/web-ticket` with a one-shot nonce (2
    min) that must equal a SameSite=Strict cookie the page's response set
    (`__Host-` behind TLS) — only the browser the page was served to — and
    only as a same-origin form post (`Sec-Fetch-Site: same-origin`, or an
    Origin of this host without Fetch Metadata); a refused post spends
    nothing. A browser signed in as the same user goes straight to `next`,
    as someone else gets a 403. Rejected: matching the redeem IP to the
    device's (iCloud Private Relay splits Safari from the app; NAT defeats
    it) and Fetch Metadata alone. Cost: one tap; the app never automates it.
  - **A device login counts as the enrollment step-up** for 10 minutes: a
    fresh Secure Enclave signature behind Face ID is at least as strong as
    retyping the password, so the app's "Add another device" mints a code
    without one; an older device session needs the password.

- **D101 — Native views can be switched off without an app update: a
  remote app config (fail-open), a per-workspace admin switch, the user's
  setting (2026-09-26).** plans/native.md §23; docs/elements.md §Native app
  UI; docs/native.md.
  - **The workspace switch** is an admin setting in users.json (`GET/PUT
    /api/xbin/native-runtime`, the admin console's workspace → xbin app
    tab). Off means `whoami.native = {runtime: 0, disabled: true}` — shipped
    apps already open every tile as its web page below runtime 1 — and
    `/c/<tile>/?native=1` answers 410 with the reason (apps that cached
    discovery). `&preview=1` keeps working, so `bx native tree`, `bx preview
    --native` and `bx lint --native` still help a builder fix what the
    switch covers for. A `{"type":"native"}` hub event makes open apps
    re-read whoami at once.
  - **The remote config** (`https://xbin.dev/app/ios.json`, `{nativeRuntime:
    {disabled, disabledBuilds}}`, https only; Info.plist `XbinAppConfigURL`
    overrides it or says `off`) turns native views off only after a
    successful read: network errors, 5xx and junk keep the previous
    answer, 404/410 mean nothing is off, an answer older than 7 days is
    ignored; re-read every 6 h. It is fetched first — at launch and on
    becoming active, before the app lock and workspace refreshes — and a
    crash mark (`ForegroundMark`: a run that ended in the foreground)
    makes the next launch fetch at once and restore windows to their
    workspace's navigator until it lands: a build whose renderer crashes
    on mount would otherwise crash every launch before learning it is
    disabled. The file is deployed with the website, by hand.
  - **The user's own switch** (Settings → Native views) and a per-tile
    "Open as web page" stay; all four end in the same fallback (the tile's
    web page, and a reason in Settings).

- **D102 — Live Activities over the push relay, and proof of work on
  anonymous relay registration (2026-09-26).** native/spec/push.md §7;
  relay/README.md §Live Activities; docs/protocol.md. Extends D94.
  - **Generic state only, bodies built by the relay.** An ActivityKit
    payload is decoded by the system before app code runs, so it can't be
    sealed; xbind sends `{phase, since, pending}` (plus `ws` and a random
    `ref` on a push-to-start) and the relay builds the APNs body from
    enumerated and numeric fields (`apns-push-type: liveactivity`, topic
    `<bundle>.push-type.liveactivity`). Names stay in the card's
    attributes, on the device. The app starts, updates and ends a card
    itself while it runs; xbind keeps it current while the app is
    suspended and starts one by push for a turn still running after 30 s.
  - **Relay handle kinds**: Live Activity handles are children of a device
    handle; one push-to-start handle per parent (`start: true`, start
    events only, a new token replaces it, never evicted for cards) and at
    most 16 card handles (update and end only, LRU); the first push to any
    handle of the tree binds them all (journaled), and retention counts a
    child as bound while its parent is. An end APNs accepted deletes the
    card's handle; the app deletes the handles of cards it ends or loses.
  - **Late tokens**: xbind remembers, in memory for the card's stale time
    (4 h), push-started refs whose turn ended before the app registered
    their token, and answers `ended`; the app places a push-started card by
    its ref across its workspaces with that `ws` and ends it at once when
    it's over or unknown (404/403 from every candidate) — never just
    because the phone is locked.
  - **Proof of work** (`-registration-pow <bits>`, `GET
    /v1/workspaces/challenge`, a stateless HMAC challenge) on anonymous
    `POST /v1/workspaces`, which xbind solves (up to 28 bits). A proof is
    spent atomically before the global registration limit is asked, and
    the spend is taken back when a limit or capacity refuses — a replayed
    proof never consumes the global limit and a refusal spends nothing.
    Closes D94's open item on abuse control.

- **D103 — A native tile's escape hatches run as the tile, confined in the
  app (2026-09-26).** docs/native.md §Escape hatches, §composer;
  plans/native.md §8.5.
  - **Everything uses the tile's frame token**, never the user's session:
    the `terminal`'s pty socket (`?frame=` through xbind's proxy, the
    `/ws/term` framing), uploads, `canvas src` pages and tile images. Every
    tile-supplied reference goes through XbinCore `TileResource` and is
    refused unless it resolves to the tile's own `/api/<self>/` or
    `/c/<self>/` (no schemes, hosts, other tiles, `/api/xbin`, traversal in
    any encoding, paths a nested tile owns); upload paths keep their query
    (`{name}` percent-encoded). xbind authorizes anyway; this stops the app
    acting as the tile's proxy to anything else. `bx preview --native`
    applies the same rule — images, upload targets and methods, nested
    tiles included — through web/xb/tile-resource.js, a port checked
    against TileResourceTests' vectors (hack/xb-tile-resource.test.mjs), so
    the preview never shows working what the app refuses.
  - **Pty close semantics**: `{"op":"exit"}`, or a close with 1000 or no
    code after the socket was live, ends the terminal; any other close
    reconnects (500 ms doubling to 10 s, 6 tries), and the count starts
    over only after a socket stayed open 5 s, so a backend that accepts and
    closes every socket is given up on. xbind's own `/ws/term` sessions
    still treat every close as a drop.
  - **Every picked file is answered**: over 64 MiB it is neither read nor
    sent, and the tile gets `uploaded {name, response: {error, status:
    413}}`. For agent prompts, photos are read up to 64 MiB and redrawn
    (HEIC → JPEG, long edge 2576 px, ≤ 3.75 MiB inline) before xbind's 10
    MiB per-file limit applies; prompt bodies are built as bytes.
  - **`canvas`**: `src` is a `WebTileController` island (dialogs, JS
    alert/confirm/prompt, downloads as on the web tile screen; no
    pull-to-refresh or swipes; it pushes onto its own window, D99); `html`
    renders with JavaScript off under a CSP that loads nothing, no base
    URL, no navigation. The expanded terminal and the camera are sheets,
    not full-screen covers (a cover's onDisappear would stop the runtime).

- **D104 — Renderer: drawers, folded actions, IME-safe input, Quick Look
  (2026-09-26).** docs/native.md; native/spec/tree.md §6; the fixtures
  `drawer`, `folded-actions`, `message-files`, `ime-field`.
  - **IME**: SwiftUI's TextField exposes no marked range, so `field` and
    `composer` wrap UITextField/UITextView and ask a Foundation-only
    `TextInputGate`: no report while text is marked, one on commit; a value
    the tile sets meanwhile is applied when the composition ends and
    replaces the composed text, which is then not reported (tree.md §6).
    The Lit reference does the same. Closes the §24 risk.
  - **Drawers** (`sheet edge="leading"`) are overlays on the container
    that owns the navigation chrome (a fragment, a nav's stack, a screen's
    own), so they cover the bar; 86 % of the width, ≤ 400 pt; dismissed by
    a tap or a leading-ward drag on the backdrop (not the panel: its rows
    have swipe actions in the same direction) or the escape gesture.
  - **Folded actions**: rows keep swipe actions and the context menu and
    gain a trailing ⋯ Menu; a message's actions all fold into a ⋯ under
    the bubble (no context menu there: it would fight text selection).
  - **Message files and previews**: image files with a `src` are
    thumbnails loaded through a per-tree `XbinImages` over
    `services.imageData` (the tile's frame token); Quick Look
    (`.quickLookPreview`) opens a private temp copy, removed on dismiss.
  - **Polish**: identifier-like and monospaced words wrap at characters
    (zero-width spaces, never in selectable text) rather than being
    hyphenated; the toolbar groups status items apart from actions;
    bar-tab content clears the floating tab bar; snapshot windows use
    `UIWindow(windowScene:)` whenever a scene exists; `split` uses
    `ArrangementView` only behind `XBIN_SDK_27_1` (its name is from the
    design, unconfirmed until the 27.1 SDK is on CI).

- **D105 — The app's terminal: SwiftTerm's search, a precise selection
  mode, a settable accessory key, the Duo's tabletop (2026-09-26).**
  plans/native.md §12.
  - **Search is SwiftTerm 1.20's own** (`findNext/findPrevious/
    searchMatchSummary` over the whole scrollback), starting at the newest
    output: the actions are older/newer (Return, ⌘G, ↑ / ⇧Return, ⌘⇧G, ↓).
    XbinTerm's `TermFind` holds only the bar's state. Mouse reporting is
    off while the bar or the selection shows (SwiftTerm drops its
    selection on every output otherwise).
  - **Precise selection**: a transparent layer takes every touch (one
    finger selects by character, word or line, two scroll, the loupe
    follows, the edges autoscroll); XbinTerm's `TermSelection` decides the
    range over scroll-invariant rows and hands it to SwiftTerm's
    `SelectionService` to draw; app-check requires the copied text to equal
    SwiftTerm's own Copy.
  - **The accessory slot** keeps its first stored format (`termCustomKey`:
    missing = F1, null = none) and adds characters and snippets; ⌥ as Meta
    and ⌥-arrows persist as `termKeyboard`. Reachable from the terminal and
    from the app's Settings.
  - **Tabletop** comes from the active `.division` reserved region (layout),
    not the hinge (effects), behind `XBIN_SDK_27_1` and `#available(iOS
    27.1, *)`; the arithmetic is XbinTerm's, tested on Linux;
    `-XbinForceTabletop YES` simulates a fold.

- **D106 — The agent template reaches the phone: Needs-you pushes, a held
  ask at home, cached folds (2026-09-26).** plans/agent-template-native.md;
  builtin-templates/agent/API.md. Extends D96.
  - **Needs-you recipients come from the run's ACL, as `GET /needs`
    decides**: the owner and participant members for a question or
    approval, the owner for a failed automation run — never from a
    request, so an admin's view-as neither triggers nor receives one, and a
    team-wide participant role reaches nobody in particular. After the
    committing transaction, a 3 s grace re-reads the run; dedupe for 6 h
    per run+state+content (questions, approvals) or per run+state
    (failures); a per-person bucket (10, +1 per 6 min) on top of xbind's
    limits. The link is `#c=<run>`; kinds `question`, `approval`,
    `failed`. The sender is injectable (`agent.needs = nil` turns it off).
  - **A held ask from home uses draft keys**: the app uploads a picked
    file at once to a path named in advance, and the vocabulary has no
    event before the attach tap, so the tile names `PUT
    /ask/upload?draft=<key>&name={name}`; the first upload creates a held
    run for (caller, key), listed nowhere; `POST /ask {draft, files}`
    releases it (409 when spent or expired). Unsent drafts are deleted a
    day later when their owner starts another (no timers). Chips belong to
    where they were picked. The web keeps `hold: true`.
  - **`tool.delta`** per (run, call index) under `?deltas=1`, with the
    text deltas' resync rules; **`FoldCache`** rebuilds a block only when
    its message, result, link, step or subagent changes (objects are
    replaced, never edited in place), so unchanged blocks keep their
    identity and the bridge sends patches only; the output is identical.
    Settings, memory, files and skills calls live in `model/actions.js`.

- **D107 — The terminal window's title bar degrades on measurement, and
  the UI harness says SKIP instead of timing out (2026-09-26).**
  docs/elements.md; docs/maintenance.md.
  - The degrade ("degrade, never clip") used a fixed 640 px threshold, but
    the bar's width depends on the host and the tab (a GPU picker, D89's VM
    toggle, labels): on a GPU host the full bar overflowed the default 680
    px window and clipped the tabs and the ✕. The bar is now measured
    after every render and resize (`fitBar`); it degrades when it
    overflows and comes back once the window is as wide as it needed; the
    need is re-measured when the content changes. Order: the path and the
    network label shrink, then the bar degrades, then tabs shrink to 56
    px and the strip scrolls. Data-built pickers mark their option
    `selected` (a re-created select got its value before its options).
  - A window restored open (a reload, a second browser) now loads its
    tile state as one opened by hand; a tab absorbed by a session listing
    keeps its one-shot `run` command.
  - Harness passes write `SKIP <reason>` when the environment lacks
    something (no gocryptfs → the agent passes), never time out, and leave
    no terminal window behind; a fresh `run.sh` is the gate.

- **D108 — The Mac mini: CI one variable away, a headless box in a
  datacenter, and release signing kept away from CI (2026-09-26).**
  native/AGENTS.md "Mac mini"; .github/workflows/ios.yml, ci.yml.
  - **CI**: every ios.yml job runs on `fromJSON(vars.XBIN_IOS_RUNNER ||
    '"xcode-27"')`, so the repository's runner or the Mac is one variable;
    DerivedData and SwiftPM clones persist (actions/cache on hosted
    runners, the machine's disk on the Mac, `XBIN_CI_CACHE=off` for a
    clean build); pinned, checksummed downloads (the runner, xcodegen,
    swiftly, actionlint); a Linux `native` job in ci.yml (Swift 6.4 via
    swiftly: `swift-test`, `swift-stubcheck`, `native-check`,
    `ci-local-check.sh`). The runner is repository-scoped with a job hook
    that shuts simulators down. **The Mac enforces the security rule, not
    the repository**: a pull request's workflows run from its own merge
    commit, so it can add a job naming the runner and edit away any check
    in the tree; the job hook — the Mac's own copy, run before any step —
    fails every job but this repository's ios.yml on a push to a branch or
    a manual dispatch (a pull request's, a fork's, another workflow file's),
    with GitHub's fork approval on "all external contributors" as the gate
    before it. ci-local-check.sh's checks (ios.yml triggers only on push
    and dispatch, no ci.yml job on a self-hosted runner, no secrets) catch
    our own mistakes only.
  - **UI tests by visible labels against a real xbind** (XbinUITests),
    skipping without one; run through `mac-remote.sh e2e` over an ssh
    reverse tunnel to an xbind on the Linux box.
  - **The box**: isolated VLAN, ssh over a VPN, key-only sshd with
    `AllowUsers`, firewall and stealth mode, Screen Sharing off or
    admin-only, no Apple ID, a KVM-over-IP, an HDMI dummy plug and a
    switchable PDU; `mac-setup.sh --check` reports all of it and changes
    none. **FileVault on** over an unattended reboot: the box holds a
    release key, so a power loss waits for an unlock at the KVM, planned
    reboots use `fdesetup authrestart`, and ci is the FileVault user whose
    unlock starts the runner's session.
  - **Four users**: an admin (setup), a standard `ci` (the runner and its
    simulators), a standard `dev` (the ssh dev loop — never ci: a job is
    code from any pushed branch and could plant a shell startup file that
    reads the e2e token, reach the e2e tunnel's loopback port, or shut the
    loop's simulators down; mac-remote.sh refuses e2e and tunnel as ci, and
    the e2e xbind keeps no password login) and a standard `release` that alone holds
    the App Store Connect API key (`~/.appstoreconnect/private_keys`, mode
    600) and runs `release-build.sh` by hand: automatic signing with
    `-allowProvisioningUpdates` and the key, so the distribution
    certificate is Apple's cloud-managed one and no distribution private
    key lands on the Mac. The script refuses the CI user, admins, any
    other user, pull_request/fork-triggered workflows and any workflow
    without an explicit opt-in. The key's role is Admin (cloud signing
    for team keys, as of now); nothing has been signed yet.

- **D109 — The app's SwiftUI/UIKit code is type-checked on Linux against
  one layered set of SDK stubs, in CI (2026-09-26).** native/AGENTS.md §3b;
  native/tools/*-stubcheck. Extends D92.
  - Four tools compile what only Xcode builds, in Swift 6 mode, against
    hand-written SDK stubs and the real packages: swiftui-stubcheck (the
    renderer), term-stubcheck (App/Terminal), app-stubcheck (Model, Shell,
    a native tile's hatches and the Agent tab — together, so the seams
    between them meet the real declarations) and widget-stubcheck (the
    widget extension); uitest-stubcheck covers the UI tests.
  - **The stubs are layered, one declaration in one place**: the
    renderer's, then the terminal's, then the app's. Integrating this
    round showed why: two work packages grew the same UIKit types on
    their own sides and each tool broke the other's build. `make
    swift-stubcheck` runs them all and ci.yml's native job runs it. A pass
    still means "consistent with the stubs", not "compiles for iOS".

- **D110 — The installer turns VM sandboxes on where nobody has configured
  them, and lets AppArmor's fusermount3 mount the workspace's encrypted
  resources (2026-09-26).** deploy/install.sh; docs/overview/15-operations.md,
  docs/isolation.md §VM sandboxes, docs/resources.md §Encryption at rest.
  Supersedes D89's "off by default" for installed workspaces.
  - **VM sandboxes on by default through the installer.** Fresh installs
    and upgrades, system and user mode: when `<workspace>/.xbin/vm/policy.json`
    does not exist it writes `{"terminals": true, "backends": <kvm>}` (sizes
    unset = xbind's defaults), owned by xbind's user, mode 0644, while xbind
    is stopped (xbind reads it on first use). **Backends only with a usable
    KVM** (`/dev/kvm` opens read-write as xbind's user — an open, since
    Ubuntu 26.04's uutils `test -r/-w` ignores supplementary groups — and
    Firecracker shipped):
    an emulated VM is several times slower, and a manifest's `"vm"` should
    not silently get that; a terminal opts in per session with its toggle,
    so emulation there is a visible choice. No VM pieces in the bundle (an
    arm64 host) → no file, so a later upgrade that ships them writes it.
    **An existing file is never touched** — an admin's choice, "off"
    included; the KVM decision is taken once, when the file is written.
    xbind itself keeps "off" as its default: a bare `xbind` (dev, tests,
    a hand-rolled deployment) changes nothing.
  - **AppArmor's fusermount3 profile** (Ubuntu; seen on 26.04) allows FUSE
    mount points only under home dirs, `/mnt`, `/media`, `/tmp` and
    `/run/user/<uid>`, so every gocryptfs mount under `/opt/xbin/workspace`
    failed "Permission denied" and the tiles with file-backed resources were
    held forever. The system installer owns one marked block (`# BEGIN xbin
    (install.sh)` … `# END xbin`) in `/etc/apparmor.d/local/fusermount3` —
    the profile's `include if exists` hook, which package upgrades keep —
    with a `mount` and an `umount` rule for `<workspace>/.xbin/resenc/**/`
    (the stock rules' flags, the path quoted and escaped), and reloads the
    profile (`apparmor_parser -r -W`). It replaces the block in place
    (never duplicates), leaves every other line, drops the block when the
    stock rules already cover the workspace, restores the old file if the
    parser refuses, and leaves a hand-broken block alone with the manual
    fix; a profile without the include, a user install and a host without
    AppArmor get the exact root commands, or nothing. No capability rules:
    the kernel still logs `dac_override` and `setuid` denials for
    fusermount3 (the stock profile leaves them out on purpose, LP: #2122161),
    but mounting, unmounting on stop and remounting at start all work.
    Resource mounts are xbind's only host-side fusermount3 use:
    fuse-overlayfs sandbox roots mount inside the sandbox's user namespace
    and a VM's files go over vsock. xbind's mount error names the fix
    when it sees fusermount3's denial and the profile exists.

  **Not chosen:**
  - Editing `/etc/apparmor.d/fusermount3` itself: a package upgrade
    replaces it.
  - Moving resource mounts under `/run/user/<uid>` or `/tmp`, which the
    profile already allows: `/run/user` needs a logind session a system
    user doesn't have, `/tmp` is shared, and the mount dir is part of the
    layout sandboxes bind from.
  - Flipping xbind's own default: that would change every non-installer
    deployment's security surface without an admin in the loop.

- **D111 — The agent template's models come from a bindable, multi-provider
  `llm` interface, and a conversation picks its own; its thread tools read
  the owner's conversations only on their grant (2026-09-27).**
  builtin-templates/agent/API.md §Config, models, features · §Threads,
  schedules and grants.
  - **The slot, not a name.** The manifest declares `"llm": {"kind":
    "http", "service": "openai", "multi": true}` (the chat tile's shape) and
    drops `apps/llm-gw` from `uses`: the owner binds llm-gw — or any tile
    that provides the OpenAI API, several at once — and the binding is the
    grant. **Unbound, the agent reaches `apps/llm-gw` by name**, so an
    instance made before the slot keeps working on its old grant (never
    break users; `/models` marks it `legacy`).
  - **Model references.** A bare id while one provider is bound — every
    config written before, unchanged — or `<provider>|<id>` among several.
    A bare id goes to the first provider that lists it (the merged catalog,
    cached 60 s), else the first; a provider no longer bound falls back to
    the first with the same id. Requests, replies and the transcript carry
    the bare id, so reasoning replay (matched on the model string) is
    unaffected. llm-gw's `/preferred` is asked of whichever bound provider
    answers it.
  - **A conversation's pick.** `Config.Pick` (from the composer:
    `POST /ask {model}`, `PATCH /runs/{id} {model}` by anyone who may talk
    in it) wins over the tiers for the main loop; compaction and titles
    keep their tiers. `runs.config` is read every turn, so a switch applies
    from the next one. The person's last pick is their default for new
    chats (`/api/xbin/prefs/model`, like the tool mode). `GET /models`
    opened from managers to everyone who uses the tile — the picker needs
    it; model names aren't secrets.
  - **Not chosen:** storing the chat tile's endpoint index (binding order
    changes); a per-message model (a conversation is the unit people think
    in, and replay wants one model per stretch); keeping `uses apps/llm-gw`
    beside the slot (two ways to the same place, and the name is exactly
    what an owner could not change).
  - **Thread and schedule tools, scoped, with an owner's grant.**
    `schedules_list`/`schedule_inspect`/`threads_list`/`thread_inspect`
    (top-level only, feature `threads`). *mine* — the schedules this
    conversation created or that deliver into it, their threads, its own
    tree — is free: the agent made them. *all* is the conversation OWNER's
    reach — owned or joined threads, owned schedules — and deliberately not
    the team pool (other people's team conversations are the owner's to
    open, not their agent's to trawl) nor held drafts. It needs the
    owner's **grant**, a new kind of approval: the step parks
    (`pendingState.grant`), only the owner — the person whose data it is,
    not a manager, not the owner token, not a view-as admin — may allow it,
    once or for an hour in this conversation (`run_grants`, expiry read at
    use: no tickers); anyone who may steer may deny, and Needs you/push go
    to the owner alone. Refused outright, never parked: the web lane (the
    toolset firewall — and its *mine* sees only web automations, since a
    private schedule's goal and threads are private data), chat channels
    (no one there can answer), and conversations no person owns. An id
    outside both scopes reads as missing — no existence oracle.
  - **Not chosen for grants:** per-tool or per-thread grants (one question
    per capability is what a person can answer); a grant for the whole
    agent (it would outlive the conversation that asked); a rememberable
    "always" (the user asked for a timeout); letting participants allow it
    (it is the owner's threads being read).

- **D112 — A sandbox registry: one live list of every sandbox xbind runs,
  VM reservations charged to a tile, and a bounded ring of what the sandbox
  layer refused or failed at (2026-09-27).** `internal/sbx`;
  docs/protocol.md §xbind API (`/runtime`, `/sandboxes`); docs/isolation.md
  §VM sandboxes.
  - **Why.** VM state was spread over a two-number budget counter, a runner
    map keyed by socket, a `vm bool` per terminal session and an id-less
    sandbox handle: nobody could say which sandboxes run, whose they are,
    how they're isolated or what they are charged against — neither an
    admin nor the tile-managed sandboxes coming next
    (plans/tile-sandboxes.md).
  - **Chosen.** An in-memory registry rebuilt from lifecycle edges: whoever
    starts a sandbox adds the entry, and removes it when the process ends.
    Backends are listed per generation (blue/green shows two, sharing one
    cgroup leaf — stats count the leaf once); terminals and agents by
    session id (an agent restart unlists before it relists). Entries carry
    kind, owner tile, user, mode (vm | namespace | host), accel (kvm |
    emulate, from the VMM Apply chose), reserved memory/vCPUs, pid, cgroup
    leaf, disk, net — and `parent`, reserved with kind `tile` for sandboxes
    a tile manages. `vm.Manager.Reserve(owner, mem)` keeps the global limits
    and messages unchanged and books each VM to its tile (`UsedBy`). The
    failure ring (64, identical failures coalesced within 10 min) records
    what the sandbox layer decided or failed at: refusals (policy, budget,
    availability — `sbx.Refuse`), setup/spawn errors, the VM shim's exit
    125 and the init's 127 at start, a VM backend that never listened. A
    backend's own crashes and build errors are not the sandbox's: they stay
    in its state and log. The VM probe is cached 5 s (an admin page polls
    it). The failed cgroup alert wiring found on the way is fixed.
  - **Not chosen:** deriving the list from the cgroup tree (admin terminals,
    dev and host mode have no leaf); joining runner state and sessions in
    the handler (no single place to attribute a VM, and tile sandboxes would
    be a third join); registering confine's tool sandboxes (per-call churn;
    their failures surface where they run); a push event stream (the admin
    tab polls like the runtime tab); persisting failures across restarts.

- **D113 — Tile-managed sandboxes: xbind launches them as siblings, gated by
  `cap:sandboxes` and a policy, driven over one exec protocol in both modes
  (2026-09-27; designed, not built).** plans/tile-sandboxes.md.
  - **Chosen.** A tile owns a set of named sandboxes, launched by xbind
    through the same `Launch` (+ `vm.Apply`) path — siblings, never nested
    (D82) — independent of its backend's generations, listed in the D112
    registry as kind `tile` under their owner. Three gates: an admin-approved
    `cap:sandboxes` (revoke stops the tile's sandboxes), a workspace
    sandboxes policy (sizes, per-tile caps, a kill switch) and, for VM mode,
    `vm.Policy.tiles` with a tile sub-budget (the owner, 2026-09-28: on by
    default — a stored policy from before it follows `backends`, the
    installer's KVM rule, instead of staying off). One exec protocol for both
    modes (`proto`): a resident `bx __sbx-agent` as PID 1 in namespace mode,
    the shim as a resident router in VM mode, reached through a listener fd
    xbind owns. File I/O happens only inside sandboxes (xbind pipes opaque
    bytes). Mounts only from the tile's own reach; no token, gateway, homes
    or workspace view inside. Egress a subset of the tile's `net`, `none` by
    default, a relay always present; L4 peers later, never an L3 splice.
    Backends drive it over HTTP with chunked NDJSON and cursors (resumable
    across blue/green); humans attach over the `/ws/term` wire with a
    tile-minted ticket xbind verifies. Definitions persist in the tile's
    `data/`, state in `.xbin/sbx/<CompKey>/<name>/`; processes don't survive
    a restart; idle stop without tickers.
  - **Not chosen:** backends spawning their own (no nested KVM; it would
    bypass the budget, the registry and the policy); reusing terminal/agent
    sessions (human-plane, per-user `$HOME`, elements refused by design);
    per-sandbox API tokens in the MVP (sandbox code could drive its tile or
    siblings); a `res:<scope>/sandboxes` quota object (the scope author
    would set its own quota); host-side file operations or an xbind file
    server (D78, D89); host-mounting VM disks (the host kernel would parse a
    guest's ext4); an L3 private network via splice (VMs refuse it);
    WebSocket streams for backends (the zero-dependency SDK has none, and
    they don't resume across blue/green); hard per-directory confinement
    inside one sandbox (`Restricted` forbids nested mount namespaces — the
    modes would differ); a namespace fallback when a VM can't start
    (D78/D89).

- **D114 — The app's way in: a welcome with four doors, sign-in that asks
  the workspace, invites in the app, no token login; the shell's
  "settings" chip leads with "add a device" (2026-09-27).**
  `internal/server/loginmethods.go`, `workspace-template/shell/`
  (bx-shell.js, shell-css.js, bx-devices.js), `native/ios/App/Shell/`
  (Onboarding.swift, SignInPages.swift), XbinCore `Client/Onboarding.swift`
  + `Client/ConnectProblem.swift`; docs/auth.md §Device login and §Invites,
  native/spec/device-login.md §2/§6/§8/§9, docs/protocol.md.
  - **Discovery is an API, not the login page's HTML.** `GET
    /api/xbin/login/methods` (public, cheap, unthrottled) answers `{api,
    title, auth, password:{enabled, adminOnly}, sso:{enabled, label},
    invites}` — exactly what `/login` already shows, no version — so the
    app offers the password form and/or the SSO button (with its label)
    the workspace really has, SSO first in SSO-only mode. Older xbinds
    answer the route 401 (their /api gate) or 404 (no-auth); the app then
    probes `GET /login`: an HTML form = an older xbind (password + SSO as
    before), a redirect = one without sign-in, anything else = not an
    xbin workspace. Scraping the HTML was rejected: brittle, and the app
    needs the no-auth and SSO-only answers.
  - **Invites in the app.** `POST /api/xbin/invite/check` names the
    account without spending the link; `POST /api/xbin/invite/redeem` sets
    the password and answers `/api/xbin/login`'s token response, so the
    app enrolls straight away. Same single use, expiry and login throttle
    as the form; a password the policy refuses is a 400 that leaves the
    invite; in no-auth mode both refuse. The form is unchanged.
  - **The welcome (owner's direction): Log in · Join with an invite · Run
    your own xbin · What is xbin?** Log in = scan the QR code (always
    shown; disabled with "paste the link instead" where the camera
    scanner isn't supported) or enter the address → methods → password
    and/or SSO. "Where do I find the QR code?" shows the real shell,
    screenshotted by the UI harness (`hack/ui-harness/app-help-shots.sh`
    → `native/ios/App/Resources/Help/`; rerun when the shell changes).
    "What is xbin?" carries the privacy stance (self-hosted, nothing
    collected, the push relay as the one opt-in exception).
  - **No token login in the app** (owner): a bearer token never renews,
    skips the device key and Face ID, and was a development crutch. The
    UI tests now sign in as a person does (a password account made by
    `e2e-xbind.sh`), which also covers enrollment and device login. The
    `.token` credential kind still decodes, for build 1's Keychain.
  - **Failures told apart** (`ConnectProblem`): can't connect, TLS, not an
    xbin workspace / too old, code refused, account, throttled, server,
    other — each with its own words. The QR path checks the code's shape
    and probes the server before redeeming, so an unreachable or wrong
    address never spends a code.
  - **The shell: a "settings" chip** (no emoji — the 🔧 became a chip like
    *docs* and *sign out*) whose menu starts with **add a device**, one
    click to the QR code; *my account → devices…* stays. The add-device
    panel's **address your phone uses** puts another address in the
    link's `u` (a browser behind a tunnel or proxy the phone can't use),
    remembered per browser. The device keeps connecting to `u` and signs
    the origin its enrollment answered (`deviceOrigin ?? server`, already
    so); a web ticket's URL, built on that origin, is opened on the
    connection address. A server-side "phone address" setting was not
    added: `--external-url` stays the canonical address, the field covers
    the rest.
  - **"Sign in again" replaces** the workspace (same place in the list,
    windows and recents; the stale device deleted) instead of adding a
    duplicate.

- **D115 — Coding sandboxes come from sandbox managers: tiles that implement
  a service contract (`sandbox-manager`), which the agent and other tiles
  multi-bind; each consumer sees its own partition (2026-09-27).**
  docs/sandbox-manager.md; plans/sandbox-managers.md.
  - **Chosen.** Sandboxes are not built into the agent tile (owner): a
    *manager* tile provides the `sandbox-manager` service (role `consumer`,
    the D86 custom-role pattern), and consumers request it on a multi
    `http` slot (`sandboxes`). The builtin manager is the `coding-sandbox`
    template, on xbind's tile-sandbox runtime (D113, revised: only managers
    hold `cap:sandboxes`); anyone may write another — over a cloud's API and
    ssh. The contract (protocol 1, negotiated by `hello`) covers sandboxes
    and their lifecycle, blocking `run`, background execs read by byte
    offset with a long-poll, a TTY WebSocket on the `/ws/term` wire, files
    and tar, and optional snapshots, clones and archives. **Partitions**
    (owner): a sandbox belongs to the consumer that created it
    (`X-XBin-From`) and is shared with other consumers explicitly. A
    person is verified on a page's calls (`X-XBin-User`) and asserted by a
    consumer's backend (`Sbx-User`, recorded as asserted). No xbin identity
    ever enters a sandbox. In the agent: a sandbox is bound per conversation
    (`Config.Sandbox`, plus `Attached` for subagents), in `runs.config` like
    D111's pick; `sandbox_create` takes the owner's grant (D111).
  - **Not chosen:** sandboxes inside the agent; one manager-wide set visible
    to every bound consumer; a bindings table (the config is read every turn
    and inherited by subagents already); manager-side glob/grep (keeps the
    contract implementable on a cloud); SSE or WebSocket for exec output
    (offsets resume across restarts); the agent keeping `cap:sandboxes`
    (D113 §7 — every tile would need the cap and the runtime's attention).
  - **The builtin `devbox` tile is retired** (it never really worked):
    `bx tile import devbox` answers 410 pointing at the sandbox managers;
    imported copies keep running as they are (docs/changes/2026-09-27-devbox-retired.md).
  - **Clarified in protocol 1 while building the reference manager:**
    `exitCode` is null when a signal ended a command (`run` and execs alike,
    so a killed command never reads as exit 0); exec and snapshot
    `clientId`s are per consumer and sandbox; only the home consumer deletes
    a sandbox; `mode` is an octal string.

- **D116 — The agent's lane becomes admin-defined classes of toolsets
  (2026-09-27).** builtin-templates/agent/API.md §Classes;
  plans/sandbox-managers.md.
  - **Chosen.** A class names the toolsets a conversation gets (`files`,
    `repl`, `web`, `internal`, `sandbox`, `subagents`, `schedule`, `threads`,
    `skills`; the core tools always), which MCP servers and sandbox managers
    and which sandbox egress it may use, and optionally a model and a system
    addendum. Built in: `internal` (today's private lane), `web` (today's web
    lane) and `coding` (sandbox + web). A class is fixed per conversation.
    The toolset firewall (a run gets internal reach or egress, never both)
    becomes the class's property: a class that mixes them takes an explicit
    confirmation and says so; the built-ins never do. `toolset:
    "private"|"web"` keeps working everywhere, mapped to the built-ins, and
    `toolset()` keeps answering the lane from the class, so skills and
    channels keyed by it are unchanged.
  - **Not chosen:** a third lane for coding; switching class mid-conversation;
    per-person class grants beyond managers/everyone (not asked for yet).
  - **The UI.** The composer's class picker shows at home only — a
    conversation's class is fixed, so its top bar shows it instead (with a
    ⚠ for a mixed class). The last pick is the person's default
    (`prefs/class`; with none yet the old lane pref, then the tile's
    default), like the model pick. The editor sends back only the stored
    classes and the one edited (`GET /classes` says `stored`), so a
    built-in nobody edited keeps following the template's default; the
    built-ins list first, in their order, edited or not. Saving asks only
    about the class being saved when it mixes; other stored mixed classes
    ride along confirmed — they were confirmed when saved.

- **D117 — Runtime `cgi` is removed: xbind never executes tile code itself;
  a cgi manifest is an error, not a fallback (2026-09-27).** Owner's call
  ("nuke that feature now, it really shouldn't be a thing").
  `internal/registry` (`ValidateRuntime`), `internal/proxy`,
  `internal/runner`, `internal/scaffold`, `internal/confine/guard_test.go`;
  docs/changes/2026-09-27-cgi-removed.md, docs/elements.md §Runtimes.
  - **The hole.** The proxy served a `"runtime": "cgi"` tile by handing
    `backend/handler` to the standard library's `net/http/cgi.Handler` —
    exec'd on the host as the xbind user, `Dir` the tile directory, under
    `--isolate` too (the runner's `sandboxable` never included cgi, and the
    proxy never asked the runner). A tile is written from inside sandboxes
    (its terminals, coding agents), so "write my tile → run code as xbind"
    — exactly what D78 rules out. `TestNoDirectExec` could not see it: the
    exec happens inside the standard library.
  - **Removed, every trace:** the proxy's cgi path and its `net/http/cgi`
    import, cgi in `HasBackend`, the runner's cgi special cases, the
    scaffold's `backend/handler` template and its exec bit (also the
    builtins copier's `backend/handler` 0755 rule), `bx new --runtime cgi`,
    the openapi enum, the docs and the scaffolded workspace's AGENTS.md,
    welcome notes, shell runtime colour and admin comment.
  - **An old workspace, handled loudly — a security hole closes now
    (docs/compat.md rule 7's stated exception; no warn-first release).** A
    manifest that still says `cgi` parses; `ValidateRuntime` makes the
    removal its manifest error (the same path as an `exposes` error: `bx
    ls` MANIFEST-ERROR, `bx doctor`, `manifestError` in `/components`,
    the sidebar/admin ⚠), text naming the reason and the fix. The tile keeps
    serving its files; `HasBackend` is false; the proxy answers
    `/api/<tile>/…` **410 Gone** with the same message (not a "no backend"
    404, not 502 noise), checked before lifecycle/policy since it says
    nothing about the caller; `Runner.Ensure` refuses it too (every start
    path — ingress, stream dials, alwaysOn — goes through it), and
    `scaffold.Create`/`POST /create` refuse `cgi` by name. Only `cgi` is
    refused: other unknown runtime values stay a backend-less tile as they
    always were (rule 7: unknown tolerated).
  - **Guard:** `TestNoCGIHandler` (sibling of `TestNoDirectExec`, sharing
    its daemon-file walk) fails on any `net/http/cgi` import in daemon code
    (`internal/`, `cmd/xbind`), no exemption and no annotation. A tile's
    *own* backend may still use it (the migration note's wrapper does): it
    runs in the tile's sandbox.
  - **Migration:** a Go backend of ~20 lines (`xbin.Serve` + `cgi.Handler`
    over the unchanged script, inside the sandbox), or a rewrite as a
    go/node/python backend. What the script loses is what the sandbox
    withholds: the tile directory is read-only (state goes to resources),
    no network without a `net` binding.

  **Not chosen:**
  - Sandboxing cgi per request (a confined run or a namespace per exec):
    real work, a second backend lifecycle to keep correct, and a runtime
    nothing shipped used — the long-running runtimes already give a script
    the same reach, inside the sandbox.
  - Silently treating a cgi tile as static (what dropping it from
    `HasBackend` alone would do): the tile's API would 404 "no backend"
    with no hint why or what to do.
  - Refusing to load the tile at all: its frontend and files are harmless
    and its owner needs them to port it.

- **D118 — Hardening: trust never comes from a file a sandbox can write
  (2026-09-27).** Three defects a design review found
  (the dev-lifecycle threat model, 06-security.md §5.2 and its side
  findings), each closed without changing any on-disk layout.
  - **(a) Chrome is admin-approved.** `internal/server/static.go`
    (`trustedChrome`, `sandboxedFrame`), `internal/server/chrome.go`
    (`GET`/`PUT /api/xbin/chrome`), `internal/users/chrome.go`
    (`chromeTiles` in users.json), `cmd/bx/chrome.go`; docs/auth.md §Who is
    calling, docs/changes/2026-09-27-chrome-needs-approval.md.
    `chrome: true` turns off the frame sandbox, so the tile's frontend runs
    with the session cookie as whoever opens it (ND8, plans/auth.md §6).
    The flag lived only in the tile's own xbin.json, which its terminals
    and coding agents can write (D40). So a terminal-level user, or an agent
    following a prompt injection, could act as the next admin who opened the
    tile. The registry comment and docs/auth.md called the flag "host-set";
    it was not. Now the manifest flag is a request. It is honoured for the
    implicit chrome (root, shell), for the shipped `tiles/organisations`
    (the only shipped tile that declares it; `tiles/` is reserved for
    built-ins, D82) and for paths a workspace admin approved. An approval
    applies while the manifest still asks, so writers can drop chrome but
    never add it. Every other asking tile is served sandboxed, and
    `/components` says `chromeRequested`. `bx doctor` lists requests and
    approvals naming no component. An approval is path-keyed, so it is a
    D82 leftover: a non-admin can't create a tile at an approved path. It
    is kept next to the other admin-set workspace policy in users.json,
    never in the workspace tree, and is never grantable.
    - **Breaking** for custom chrome tiles: they run sandboxed until an
      admin approves them. A security hole closes in the release that finds
      it (docs/compat.md rule 11), so there is no warning release.
    - **Not chosen:**
      - A per-tile content hash bound to the approval: a chrome tile's
        writers are trusted by design, and every edit would need
        re-approval.
      - An `xbin:chrome` grant: grants are requested from the manifest, and
        chrome must never be an element capability.
      - Approving from the tile's own terminal token: element principals
        are never workspace admins (the API refuses them).
      - Making `tiles/organisations` chrome by its path alone: it still
        needs its manifest flag, so it behaves exactly as before.
  - **(b) One scope per data key.** `internal/registry/scopekeys.go`,
    `internal/broker/policy.go` (`guardNewComponentTree`),
    `internal/broker/backup.go`; docs/resources.md,
    docs/changes/2026-09-27-scope-json-checks.md. `util.ScopeKey` turns
    `/` into `~`, so `apps~x` and `apps/x` share
    `data/resources*/<key>`, the resenc mount and the gocryptfs password
    label `fs:<key>/<name>`. A scope at `workspace` shares the
    workspace-level resources' key, and also their kv buckets, because
    buckets are named `res:<scope path>/<name>` and the workspace's are
    `res:workspace/<name>`. `apps~x` and `apps/x` don't share buckets. Each
    key now has one holder:
    - the workspace scope always holds `workspace`;
    - otherwise the holder recorded in `data/scope-keys.json` the first time
      the key is contested, kept while that holder's directory exists;
    - otherwise the scope that held the key at the previous scan;
    - with no history, no claimant holds it.
    A refused scope stays in the scope table, so membership, same-scope
    grants and uids don't shift. Its `Resources` are dropped, so nothing is
    provisioned, chowned, mounted or backed up for it. Its tiles'
    `ManifestErr` names the holder, and resource removal and restore check
    `HoldsScopeKey`. Every creation path refuses a path whose key clashes
    (`ScopeKeyClash`).
    - **Not chosen:**
      - Rekeying (a longer or hashed key): it moves every workspace's data,
        for a collision that needs a literal `~` in a directory name.
      - Refusing `~` in paths outright: existing tiles may have one, and it
        still leaves `workspace`.
      - Always refusing every claimant: it lets a newcomer disable an
        existing scope's resources.
      - Remembering holders only in memory: a restart would forget which
        scope was first.
  - **(c) Resource names are one plain segment.**
    `internal/registry/resnames.go`, `internal/resenc/resenc.go`
    (`Ensure`), `internal/broker/backup.go` (restore); docs/resources.md.
    A name from scope.json went unchecked into
    `data/resources-enc/<key>/<name>` and `.xbin/resenc/<key>/<name>`.
    `MountEncrypted` creates, `gocryptfs -init`s and mounts those for every
    declared file resource, granted or not. So a name like `../../../x`, from
    a file the tile's terminals write, steered xbind's mkdir, init and FUSE
    mount anywhere its user can write. A name with `/` also collided kv
    buckets (`res:<scope>/<name>`) in backup, offload and restore. Names
    must now match `[A-Za-z0-9][A-Za-z0-9._-]{0,63}`, and the check runs in
    three places:
    - at parse time: scope.json drops an invalid name and records a
      manifest error on the scope's tiles. `Workspace()` leaves out invalid
      workspace-level names, logged, while `MutateWorkspace` writes the file
      back as declared;
    - in `resenc.Ensure`, which refuses any key or name that isn't one
      plain segment, the last line for every caller;
    - in restore, for the names an archive carries.
    - **Not chosen:** only rejecting `/`, `.` and `..`: the name is also a
      key label, a bucket suffix and an env name, and a small set is
      easier to reason about. Existing names with spaces or other
      characters need renaming (migration note).

- **D119 — Pause live reload: a tile can run a checkpoint instead of its work
  tree, and the zero state stays today, byte for byte (2026-09-28).**
  docs/tile-deployments.md; docs/bx.md (`bx live-reload`, `bx rollback`);
  plans/dev-lifecycle/ (05-model §13, 14-implementation M1, 16-open-questions
  §1.1 P1, P2, P5, P8, P9, P15, P16, P18, P29). The owner ratified P1–P2 and
  confirmed P18 on 2026-09-27; the rest are recorded as built, at the owner's
  direction, on 2026-09-28.
  - **(a) Deployments are pointers over an xbind-owned checkpoint store**
    (P1). A checkpoint is every file of the work tree but `.git` and nested
    components, captured by xbind; a deployment names one. The work tree is
    the default feed; tracked branches and a deploy remote are later feeds on
    the same core (M3).
  - **(b) The term is *tile deployment*** (P2); "identity" stays reserved for
    principals.
  - **(c) The zero state is the absence of a deployment record** (P5), and it
    is today byte for byte: the flat cgroup leaf, the D112 registry rows,
    storage keys, events and `/components`. Opting out removes the record;
    the checkpoint store may remain, inert. No manifest key. Zero-state
    goldens pin it.
  - **(d) Live reload attaches to at most one deployment** (P8), which runs
    the work tree directly; the default path adds no per-save cost. Paused:
    saves change the work tree and nothing else; **Reload now** checkpoints
    and deploys with the usual blue/green swap.
  - **(e) Pinned means pinned** (P9). Every restart path (crash, grant
    change, xbind restart, loss of `.xbin`, alwaysOn backoff) runs the
    record's checkpoint (after a failed move off the work tree, the attempted
    one); built artifacts are kept per checkpoint; the inbound surface
    (`template`, `exposes`, `provides`, `chrome`) follows the primary's code.
    A failed deploy is invisible to viewers: the pinned code keeps serving,
    and the failure reaches only the actor (the answer, the deploy log, a
    `deployments` event, `bx` exit 1).
  - **(f) Deployment state is xbind-owned** (P15): `data/deployments`,
    `data/checkpoints` and `.xbin/deploy`, keyed by a 128-bit hash of the
    tile path; never the root `xbin.json` or the work tree.
  - **(g) Every git or tool run touching tile code happens in confine**
    (P16, D78); the checkpoint store is confine-only; materialized trees
    (directories 0755, files 0444/0555) are served with containment and never
    followed out through symlinks (`TestNoFollowingHostWalks`). A
    read-only fetch remote (`git fetch xbin-deploy`) serves each pinned
    deployment's git view from a separate view repository.
  - **(h) Pinned or non-primary backends need isolation** (P18): without
    `--isolate` they are unsupported; static tiles pause live reload
    everywhere. (cgi no longer exists, D117.)
  - **(i) Deployment state belongs to the tile it was created for** (P29):
    collision-free path keys and a record that carries and verifies path,
    owner ref and creation stamp; a tile re-created at the path starts in the
    zero state.
  - **Why.** A busy tile had no way to try a risky change without every
    viewer running it on the next save; pausing is the smallest step that
    fixes that, and building it as pointers over checkpoints makes the later
    rungs (named deployments, D127) the same mechanism rather than a second
    one. Never breaking a tile that doesn't opt in is the hard rule, so the
    zero state is defined as "no record" and pinned by goldens.
  - **Not chosen:** building a candidate on each save while paused (NP-04-6:
    it contradicts "paused changes nothing", costs a build per save, and adds
    activation-by-hash state); a per-tile checkpoint exclude list (NP-04-4:
    excluded paths would vanish silently from pinned code; over-size captures
    are refused instead, naming the largest directories); a manifest key for
    opting in (compat rule 7); deployment state inside the work tree or
    `xbin.json`, which every terminal on the tile can write.

- **D120 — Phase 2 of coding sandboxes: xbind's tile-sandbox runtime.
  Only manager tiles drive it (`cap:sandboxes`). Terminals reach people
  only through the manager's relay. Its routes mirror the sandbox-manager
  contract (2026-09-27, owner's kickoff).**
  plans/tile-sandbox-runtime.md (the implementation plan, WP-1…WP-22).
  Revises D113 as recorded there (§0).
  - **The owner's decisions.**
    1. **`cap:sandboxes`** is held only by manager tiles and approved only by
       workspace admins, like `cap:containers`. It is never auto-granted. A
       revoke stops the tile's sandboxes and keeps their state.
    2. **Terminals: relay only.** The TTY WebSocket (the `/ws/term` wire)
       answers only the manager's instance token; the manager relays it to
       consumer pages. There are no xbind tickets in v1. Admins may list,
       stop and delete any tile's sandboxes, but never exec into one or
       attach.
    3. **D88's `noTerminal`.** xbind refuses a tty exec whose `forUser`
       claim names a user with `noTerminal`. Non-tty execs aren't restricted
       by it, and the docs say so.
    4. **Storage.** Definitions are xbind-owned: a tile never writes them,
       and everything read back is re-validated against the tile's current
       reach. Component backups carry definitions, not state; state moves by
       snapshot or archive. Running execs die with xbind: they answer `lost`,
       and sandboxes come back `stopped`.
    5. **Modes.** Namespace mode is the `Restricted` terminal lockdown: apt
       works, nested containers don't. Docker needs VM mode. Emulated VMs are
       allowed where the VM policy allows emulation, and are reported as
       such. A VM that can't start never falls back to namespace mode; the
       manager picks the mode for each sandbox.
    6. **No xbin identity inside.** A sandbox never gets an xbin token, the
       gateway socket or a route to xbind. The manager proxies everything.
    7. **Mounts in v1.** The manager's own `filesystem` resources, limited by
       role, and `{source:true}` read-only. `sqlite` is refused. `code:`
       mounts and scratch volumes come later.
    8. **Egress.** `none` by default, or a network the manager binds for its
       sandboxes through its own interface slots, so its own backend needn't
       hold it. There is no private sandbox-to-sandbox network and no port
       previews in v1.
    9. **Base images.** Each sandbox pins its base. A changed base never
       applies implicitly: restart and thaw keep the pin, and reset or rebase
       is an explicit call. GC keeps every base a sandbox references,
       archived ones included.
    10. **Offload** of a manager tile stops its sandboxes and carries their
        state into the archive. Until archive/thaw is built, offload refuses
        while any of them has state. It never drops state silently.
    11. **Defaults** are D113 §6's: 8 sandboxes per tile, 4 running, 8 GiB,
        8 vCPUs and 100 GiB; per sandbox 2 GiB, 2 vCPUs and 20 GiB, capped
        at 8 GiB, 8 and 200; idle stop after 30 min; auto-start on exec and
        file operations. The VM policy gains `tiles` and `tilesBudgetMiB`
        (default half the budget). xbind's own default for `tiles` is false.
        The installer's fresh policy sets it true where KVM is usable, like
        `backends`, and never touches an existing file.
    12. **Deployments.** Sandboxes belong to the manager tile's deployment.
        The key layout gives a non-main deployment its own set, leaving the
        deployment plumbing a seam (dev-lifecycle's handed-over WP-S5).
    13. **The VM-backend "never listens" fix** is a dependency, fixed on
        another branch.
    14. **Phase 3's needs.** A zero-dependency SDK surface that the
        coding-sandbox manager's `xbin` Backend uses to serve the contract
        almost one for one.
  - **Chosen in the plan (the designer's calls).**
    - **Transport: a `SOCK_SEQPACKET` connection factory.** It is passed as
      an inherited fd. D113's listening socket is dropped: the factory
      needs no path, leaves no stale socket and has no 108-byte limit. Its
      EOF also kills the sandbox when xbind dies.
    - **The runtime API mirrors the contract**: byte-offset output with a
      long-poll, `run`'s head and tail, the files and tar routes, the error
      enum and the TTY wire. So the manager forwards most calls unchanged
      (`sdk` `Forward`). D113's NDJSON is dropped.
    - **Egress uses a new request-side interface kind, `sandbox-net`.**
      Selectors are `none | class:<slot>`. An unbound class is `none`, with
      no org or personal auto-default. Binding reuses D20, D26, D54, D65 and
      D88.
      - The relay gets `Deny`, which covers the host's own addresses and
        xbind's listen addresses, and answers DNS with REFUSED under `none`.
      - A class change that narrows reach stops the running sandboxes of
        that class. A change that widens it waits for the next start.
      - There is no `inherit` and no rule-list subset (see below).
    - **`cap:sandboxes` has a floor like `xbin`'s**: no allowance delegates
      it, not even `cap:*`. Its ceiling class is `xbin-caps`.
    - **The VM policy also gains `tilesEmulated`** (default false). A tile VM
      runs under emulation only when an admin allows it. Otherwise VM mode is
      reported unavailable, never replaced.
    - **Removing a tile** stops its sandboxes and keeps their state. The
      state is listed as a leftover, and an admin cleans it up. This revises
      D113 §1's "deleting the tile deletes its sandboxes".
    - **Definitions live in `<ws>/data/sandboxes.json`**, keyed by tile
      (dev-lifecycle's layout). State lives in `.xbin/sbx/<CK>/<name>/`, and
      a non-main deployment's in `.xbin/deploy/<TK>/d/<d>/sbx/`.
    - **Ids and presentation.** Exec ids carry a per-boot prefix, so ids
      from before a restart answer `lost`. The TTY session frame takes the
      manager's ids (`sessionId`, `sandboxId`), so a byte relay satisfies the
      contract.
    - **Snapshots and clones are in phase 2**: the manager's images are
      clones. Archive and thaw are a later work package.
    - **Other v1 choices.**
      - `rebase` is an explicit call that keeps the upper.
      - Execs take a `uid`/`gid`; a single-uid namespace host reports
        `users: root`.
      - A missing pinned base puts that sandbox in `error`. It never gates
        xbind's boot.
      - A namespace upper records its overlay flavour.
      - File streams are framed and commit only after their terminator.
      - The data plane isn't audit-logged.
      - `PUT /vm/policy` and `PUT /sandboxes/policy` merge onto the stored
        policy, so an older admin console can't zero a new field.
      - termwire sends `exit` only when the process ends, never to a client
        dropped for being slow.
    - **The existing backup restore follows planted symlinks as xbind.** It
      is fixed first (WP-9), as phase 2 step 6 asked.
  - **Not chosen:**
    - xbind tickets for human attach: the owner's relay-only decision.
    - `inherit` egress: it makes the manager's backend hold the network,
      which is exactly what the `sandbox-net` kind avoids.
    - D113's rule-list subsets: the relay ORs IP and host rules, so an
      intersection needs a new predicate, and classes cover the need.
    - A second `net`-kind slot for sandboxes: map-order resolution, D54/D88
      auto-defaults, and every net surface is per component.
    - Policy-level egress classes: a second reach grammar that skips the
      binding approvals.
    - A listening socket in `.xbin/run/sbx/`.
    - NDJSON output with sequence numbers.
    - Namespace uppers in component backups.
    - A namespace fallback where VMs can't run.
    - Sandboxes surviving an xbind restart: the relay lives in xbind; a relay
      out of process is a later option.
    - Definitions in the tile's own data: a backend could forge mounts,
      egress or mode.
    - A deployment segment under `.xbin/sbx/<key>/`: it would collide with
      sandbox names.

- **D121 — People's terminals onto sandboxes are the `sandbox-terminal`
  builtin tile: a consumer of the sandbox-manager contract that creates
  nothing, with browser terminals straight to the manager and SSH bridged by
  its backend as an asserted person (2026-09-28).**
  builtin-tiles/sandbox-terminal (API.md); docs/sandbox-manager.md §People's
  terminals; plans/sandbox-managers.md phase 3 item 4.
  - **Chosen.**
    - **A consumer, not a manager.** `interfaces.sandboxes {kind: http,
      service: sandbox-manager, multi: true}`. It creates no sandboxes: one
      reaches it by being **shared** with it (a share naming its path, users
      `"*"` or a list) or by being its own. The owner binds managers like
      the agent's.
    - **Browser terminals** are the page's own: `<bx-terminal src>` on the
      manager's `tty` route with the frame token, so the manager sees the
      **verified** person. The backend isn't in that path.
    - **SSH** on a `stream` expose (`exposes.ssh {kind: stream, proto: tcp,
      port: 2222}`; an admin binds a host port), served by
      `golang.org/x/crypto/ssh` in the backend. **Keys are registered per
      person** (routes the page calls with its frame token: the verified
      `X-XBin-User` registers and removes their own; the tile's managers —
      write or terminal access, or the owner — list and revoke anyone's). A
      key belongs to one person. A revoke also closes that key's live
      connections.
    - **On login** the key names the person and the SSH user name names the
      sandbox: its login (the name in lower case, runs of other characters
      `-`), `<login>~<n>` when several of the person's sandboxes share one,
      or its id. Unknown or ambiguous → a message listing the choices, exit
      1. The session is authenticated first, so the message reaches the
      person instead of a bare "Permission denied". The disambiguator is
      `~` because a login never has one (the name's other characters become
      `-`): with `.<n>`, a sandbox named `web.1` and the first of two `web`s
      were both `web.1`. A login or id that matches exactly wins over the
      loose match of a name (`web~1` spells `web-1` loosely, and a sandbox
      named `web-1` beside two `web`s must not make it ambiguous).
    - **Access at every login.** A key outlives the page call that
      registered it, so the tile asks xbind what its person may do on it
      now: `GET /api/xbin/access/<user>` (`xbin.AccessOf`), a route for a
      tile's backend about itself only — `{user, level:
      none|read|write|terminal, active}`, the level `X-XBin-User-Level`
      would carry (the proxy's `users.Access.TileLevel`; xbind's own
      levels, so an admin or the tile's owner is `terminal` — there is no
      `owner` level), none for a disabled or unknown account, never a 404.
      `active` says the id is an account that can sign in, which tells a
      tile whether an id exists — accepted: ids are names people use, and
      the tile learns nothing else about them. Asked at every login, every
      session a connection opens, every key registration, every 30 s while
      a connection lives, and for everyone's keys when a manager lists them;
      kept 30 s (a failure 2 s). Without read access the person is let in
      only to be told `access revoked` (exit 1), a live connection is cut
      (its sessions told why), and their keys are **marked inactive, not
      deleted** — access may come back, and the next check that finds it
      clears the mark. No answer from xbind: logins **fail closed**; a live
      connection isn't cut over a failed check, only over a "no". A page
      request with no level (a frame token outliving its person's access)
      is refused beyond `/me` and their own keys.
    - **No new role for the SSH path** (the kickoff's open question: a
      `gateway` role for asserted users). The backend lists and opens
      sandboxes **as an asserted person** (`Sbx-User`), which the contract
      already allows a consumer's backend, and **enforces the person rules
      itself**: a shared sandbox only as far as its share names the person,
      then its owner, a member, or team. These are the same rules the agent's
      `sandbox_access.go` applies.
    - **With a pty** the session is the manager's `tty` route, dialled with
      `sdk/ws` (`Sbx-User` set): binary frames ↔ the channel, window-change
      → `resize`, the `exit` frame → exit-status (or exit-signal).
      **Without one** (`ssh host cmd`, `ssh -T`) it is a background exec
      with `stdin`: output read by byte offset, input posted, exit from the
      exec. A terminal there would echo input, turn `\n` into `\r\n` and wake
      pagers, which breaks pipes; and `exec` is required of every manager
      where `tty` isn't. stdout and stderr arrive together (the contract's
      one stream). A pty request on a manager without `tty` runs this way,
      and the tile says so.
    - **A client that leaves** ends its command, as sshd would: HUP to the
      group, then DELETE if it still runs 2 s later. A command that ended on
      its own isn't DELETEd, so work it detached into its own group survives.
    - **Rate limits.** Failed keys are a token bucket per source address
      **and claimed user name** (20, then one every 2 s). Over the rate an
      attempt is answered 2 s late: a tarpit, not a lockout, because
      xbind's relay shows one source address for everyone and a good key
      must never be locked out by a flood; keyed by the name too, a flood
      against one name doesn't tarpit another person's old keys into their
      login grace. The buckets are bounded (4096; the full ones swept, then
      arbitrary ones dropped — a forgotten bucket only starts full). At most
      32 handshakes are in flight; one more **drops a random older pending
      handshake** (randomized early drop) instead of being refused, so
      connections that never finish can't hold every slot. A 10 s login
      grace, 6 tries and 10 sessions per connection.
    - **The host key** is ed25519, made on first start and kept in the
      tile's **vault** (it is a secret). Only a vault that answers "no such
      key" gets a new one; any other error retries, so clients never see a
      changed host. Keys and settings are in the tile's kv (`state`).
    - **Nothing a manager says is stored** beyond a minute's hello cache.
      No xbin identity reaches a sandbox.
    - `x/crypto` is pinned at v0.48.0, the newest release whose `go`
      directive (1.24.0) the rootfs toolchain meets (check-pins). The
      tile's own go line stays `go 1.24`: xbind's generated go.work says
      `go 1.24`, and a module it uses with a later line (`1.24.0` counts as
      later) fails the build — now a test (`TestBuiltinTilesImport`). The
      root module's newer x/crypto compiles the same code for `make vet`.
  - **Not chosen:**
    - A `gateway` role for asserted users: the contract already covers a
      backend acting for a person.
    - Terminals relayed through the backend for the browser: the person
      would become asserted. *Superseded by the amendment of 2026-09-29
      below: terminals are part of the sandbox interface for consumer
      backends too.*
    - Resolving the sandbox during authentication: a wrong name would read
      as a bad key.
    - The `tty` route for commands without a pty: see above.
    - Per-source lockouts: behind the relay they would lock everyone out.
    - Refusing new connections at the handshake cap (sshd's MaxStartups
      "full"): a slow flood would then keep everyone out.
    - Refusing a revoked person at the key (a bare "Permission denied"),
      or deleting their keys: the first hides why, the second loses what a
      re-granted person would need again.
    - A frame/terminal token asking `/access/<user>`: the tile's frontend
      acts for the person using it, and would list who else uses the tile.
    - Port forwarding, agent forwarding, X11, sftp and client environment in
      v1: each needs its own reach decision (a forward is egress from the
      sandbox's network into the person's machine, or the reverse).
  - **Known limits.**
    - A session already running when a share is withdrawn runs on until it
      ends (the person's access to the tile is re-checked; a share is the
      manager's and isn't).
    - A person's removal reaches a live connection within a minute (a
      check every 30 s, of an answer kept up to 30 s), and a new login
      within 30 s; a re-grant reaches a refused login within 30 s — at once
      when they open the tile's page (its request's level is xbind's
      answer, and replaces the cached one).
  - **The page, the native view and the agent's share (part 2).**
    - **Tabs of terminals the manager owns.** Each tab is `<bx-terminal
      src>` on the manager's route; closing a tab leaves the shell running,
      **End** is `DELETE …/execs/{id}` from the page. Reattaching after a
      reload uses the contract's own execs list, read as the person (tty
      execs labelled `terminal`, or unlabelled — the contract now asks a
      manager to label its `tty` route's execs so): nothing is remembered
      by the page (a sandboxed frame has no storage) or the backend. They
      are everyone's who may use the sandbox — the manager already lets any
      of them attach — and are shown as such.
    - **"Published" is an address set by a tile manager.** The tile can't
      see xbind's port binding, so the ssh command shows once the listener
      is up and a manager set the address people type; until then the page
      says what an admin runs.
    - **The native view opens no terminal** (a D96 difference, like the
      agent's): the app's `terminal` dials only the tile's own routes, and
      relaying the manager's `tty` through the backend would make the
      person asserted (a reason the amendment of 2026-09-29 retires; the view
      is unchanged for now). It lists, ends and manages keys, and offers "Open in
      the browser" — `xbin.native.open` when the tile holds
      `cap:open-links`, else the link copied — rather than declaring the
      grant, which every import would then have to approve. Parity for a
      builtin tile is its `native.js` header and the `tile-*` fixture;
      the page's logic is the shared `sbxterm.js`.
    - **The agent shares, with the person rules unchanged.** "Share with a
      terminal tile…" is offered on a sandbox the person owns whose home is
      the agent (only the home consumer changes shares); the share is for
      the person (joining a list that tile's share already has) or `"*"`
      for a team sandbox. The tile's path is a field, default
      `apps/sandbox-terminal`: the agent can't know where it was imported.
      The agent's `PATCH /sandboxes/{ref}` already passed `shares` through.
      Share and Stop sharing replace the whole list, so they send the
      sandbox's `version` as it was read (the contract's lost-update
      guard); a 412 reads the sandbox again (`GET /sandboxes/{ref}`),
      computes the list afresh from it and sends it once more — a share
      someone else added meanwhile is kept, not overwritten.
    - **Not chosen:** a per-person directory of open terminals in the
      backend (the manager's execs list is the truth, and the page reads it
      as the verified person); auto-reopening running terminals as tabs on
      load (on a team sandbox they may be someone else's); a picker of
      terminal tiles in the agent (it has no view of the workspace's tiles).
  - **Addendum (2026-09-27, after wave 1): an adversarial review of waves
    2–3 and wave 1's handoffs.** plans/tile-sandbox-runtime.md (§1–§11
    amended; the follow-up WPs WP-2b … WP-14b in §12).
    - **The owner's decisions.**
      - **Mounts are same-scope only in v1.** A manager mounts only
        `filesystem` resources of its own scope that it holds (a `uses`
        entry and the grant, as `EnvFor` requires). A cross-scope resource
        is refused even when granted: handing another scope's path to a
        sandbox is what `EnvFor` never does. Granted foreign resources come
        later, with their own design. (WP-13 built it this way; §3.3's "or
        one granted to it" is withdrawn.)
      - **"Internet" is strict for tile sandboxes.** A sandbox class's
        `internet` (its `Allow`, its reported `reach` and its DNS pins)
        also excludes `100.64.0.0/10` (CGNAT, which Tailscale uses),
        `198.18.0.0/15`, `240.0.0.0/4` and `64:ff9b::/96` (NAT64), plus
        `64:ff9b:1::/48` (local-use NAT64, the same kind of range), because
        the contract says `internet` reaches no private or local network.
        Tile backends' `net:internet` is unchanged: narrowing it would
        change existing tiles' egress.
    - **Chosen in the plan (the designer's calls).**
      - **A sandbox has an immutable `uid`**, and state is keyed by it
        (`.xbin/sbx/<CK>/<name>.<uid>/`; the archive key; a backup's match).
        A name is only an address: deleting and re-creating `img-base`
        never meets the old state, its removal or its archive. Deletions
        rename into `.trash` and a confined remover empties it later. The
        state a restore swaps is one `cur/` dir (stamps, `upper/`, `work/`
        or the disk) exchanged with `renameat2(RENAME_EXCHANGE)`, so a
        crash leaves the old state or the new one, never neither. A
        definition whose state is missing is `error`, never a blank start.
      - **One key builder, and a guard for deployments.** `keyOf(p)` is the
        only place a key comes from. Until non-main keys exist, a
        principal of a non-main deployment gets 501 — read by reflection
        so the guard works the moment dev-lifecycle adds the field — and a
        reflect test over `auth.Principal`'s fields fails on any field
        nobody reviewed. Otherwise a dev deployment of a manager would
        compile unchanged and address main's sandboxes.
      - **Running sandboxes follow what they were granted.** A vault seal
        stops (synced) every sandbox with a resource mount before the views
        unmount. A `res:` grant change, a deleted resource or a ceiling
        change re-resolves running sandboxes' mounts; a lost mount, or a
        read-write one downgraded to reader, stops the sandbox with its
        state kept — the rule a narrowed egress class already had.
      - **One teardown for every way a sandbox ends** (a stop, a crash, a
        cgroup OOM, the VMM exiting, fuse-overlayfs dying). The agent exits
        when fuse-overlayfs does, and sessions carry `oom_score_adj` 500 so
        user work, not the agent, is what the OOM killer picks.
      - **Admission books atomically**, per tile and workspace-wide, and
        tile-sandbox cgroup leaves live under one per-workspace parent,
        `comp-tilesbx-<ws8>`, capped by a new `policy.total`; the init is
        started straight into its leaf (`UseCgroupFD`).
      - **The relay can't spend xbind.** Flows are capped per sandbox
        (1024 TCP, 256 UDP) and across all tile sandboxes; host locality is
        decided per flow with `RTM_GETROUTE`, not from a periodic read.
      - **Disk.** Sandbox bytes count against `perTile.diskGiB` only, never
        a scope's resource-write quota (whose 50 GiB default is below the
        100 GiB cap). Running uppers are re-measured by a confined `du`, one
        at a time; low disk stops running namespace sandboxes, largest tile
        first, and holds starts.
      - **Snapshots pin their base; a restore brings the base back too**,
        and a clone takes its snapshot's base. Only a mode (or, in namespace
        mode, an overlay-flavour) mismatch is refused. Copies are staged,
        honour `?wait`, show `creating` or a busy `stateDetail`, and are
        reconciled at boot; `cp` names `--preserve=xattr` so a lost xattr is
        an error.
      - **A clone without `snapshot` copies only a stopped source**; a
        running one answers 409 `state` (not an implicit snapshot, which
        would stop the source's work from inside someone else's create).
      - **Exec semantics follow the contract.** A stop leaves execs
        `killed`; only another boot's ids are `lost`. `signal`'s `group`
        defaults to true. Idle stop is held off by in-flight runs, file and
        tar operations and attached TTY clients. An exec arriving while a
        sandbox stops waits, then auto-starts. Reset and rebase restart a
        sandbox that was running.
      - **`Forward` takes typed routes** whose ids are checked against the
        runtime's grammars, and the runtime refuses dot and encoded-slash
        segments: a consumer's id can't retarget another sandbox of the
        same manager.
      - **An unreadable sandboxes policy file fails closed** (`enabled`
        false, the error surfaced) instead of re-enabling a kill switch.
    - **Not chosen:** a netlink address subscription for locality (its
      events arrive after the address is usable); keying state dirs by uid
      alone (unreadable for admins); stopping VM sandboxes on low disk (their
      disks are bounded); an implicit snapshot for a clone of a running
      sandbox.
  - **Amendment (2026-09-29): terminals are part of the sandbox interface
    for consumer backends.** The owner's decision in the AgTT × coding
    harnesses programme: a consumer's backend — AgTT, sandbox-terminal,
    any tile, against any manager, a cloud one included — opens PTYs in a
    manager's sandboxes and relays them to its own pages and app views, the
    person asserted. docs/sandbox-manager.md §Terminals; docs/sdk.md §A
    manager's terminals.
    - **Chosen.**
      - **The contract says so.** The `tty` routes and tty execs serve a
        consumer's backend as well as its pages: it dials through xbind with
        its instance credential and names its person in `Sbx-User`, asserted
        as on every backend call. The manager keeps the partitions and
        leaves the person rules to the consumer, and passes that person on
        where its substrate asks (`forUser`, so xbind's `noTerminal` still
        refuses them). The SSH bridge already worked this way; now every
        consumer may, for any client of its own.
      - **The consumer that relays checks first**: may the person use the
        sandbox (the contract's person rules, applied by the consumer), may
        they have a terminal. The manager can't, and the relay carries
        whatever they type.
      - **One relay, in the SDK.** `xbin.RelayManagerTTY(w, r, endpoint,
        sandboxID, opts)` dials the manager (`xbin.DialManagerTTY`, which
        a backend that drives a terminal uses alone), then upgrades the
        person's request and copies every message both ways unchanged — the
        `/ws/term` wire end to end, so `<bx-terminal>` and the app's
        `terminal` work against a consumer's route. It dials anew rather
        than tunnelling the person's handshake, so none of their headers
        can reach the manager. A request that isn't a handshake, and a
        refusal, are answered before anything starts or upgrades (a failed
        upgrade after the dial would leave a shell running); a close either
        way closes the other the same way, a lost manager as 1011 so a
        terminal reconnects. Routes are built from typed parts
        (`xbin.ManagerTTYURL`): the contract's sandbox id grammar, an exec
        id confined to one escaped segment (the contract fixes no exec id
        grammar, so none is imposed), no free-form path.
      - **Conformance.** `sdk/sandboxcontract` gains `tty/backend`: an
        asserted person who is neither owner nor member gets a terminal
        (the command's output, a resize, the exit code), attaches to a tty
        exec the backend started, opens one on a sandbox shared with its
        consumer, and another consumer is `not-found`. The reference manager
        and coding-sandbox pass it unchanged.
      - **The cloud mapping** is `ssh -t` into a holder on the instance
        (`dtach`, `tmux`) that keeps the command and its pty past the
        connection, window-change on resize: a contract terminal outlives
        its client.
    - **Not chosen:** tunnelling the person's upgrade byte for byte (as a
      manager's `Forward` does to the runtime) — their handshake's headers
      would reach the manager unless scrubbed one by one, and the relay
      couldn't see how either end closed; a new role or header for relayed terminals (the
      asserted person already says it); an exec id grammar in protocol 1
      (not additive).

- **D122 — The builtin sandbox manager is the `coding-sandbox` template: a
  contract layer over a pluggable Backend whose shapes are the SDK's
  (2026-09-28).** builtin-templates/coding-sandbox/API.md;
  plans/sandbox-managers.md phase 3 item 3.
  - **Chosen.**
    - **A template**, so each copy is a manager with its own config. It
      provides `sandboxes` (`sandbox-manager`, role `consumer`, which reaches
      `/sbx/*` only), requests `cap:sandboxes` and a sqlite `db`, and declares
      two `sandbox-net` classes, `internet` and `open`. Contract egress `none`
      is always offered; `internet` while its class is bound with reach
      `internet`, `open` while its class is bound at all. A sandbox's `egress`
      is its class's word, or its reach when that is wider.
    - **The Backend seam sits at the runtime's level, in the SDK's shapes.**
      `Fleet` (the offer; sandboxes by name: list, create — `from` clones —,
      get, patch, delete, start, stop) is exactly `*xbin.Sandboxes`, and `Box`
      (run, execs and output by offset, stdin, signals, resizes, the terminal
      relay, files, tar, snapshots) exactly `*xbin.Sandbox`; a compile-time
      check keeps it so, and the `xbin` backend needs no translation.
      Backends register like the messaging bridge's platforms. Refusals are
      `*xbin.SandboxError`; anything else answers `503 unavailable`.
    - **The contract layer owns** partitions and shares, owners (verified
      `X-XBin-User` over asserted `Sbx-User`), visibility and members,
      labels, versions, an overlay state (`creating`, `deleting`, `error`),
      and `clientId`s: creates per consumer and snapshots per consumer and
      sandbox in its table, execs per consumer and sandbox in memory (they die
      with the substrate), handed down prefixed with a hash of the consumer.
      **Contract ids (`sb-…`) and runtime names are separate** random names
      mapped in the table; a refusal that names the runtime sandbox is
      rewritten to the id.
    - **Storage:** the tile's sqlite resource (modernc, as the agent), one
      JSON document per row and an in-memory mirror written through, so a new
      field needs no migration. A restarted manager finishes the creations
      and deletions it left.
    - **Images** are the substrate's base plus an optional setup script. The
      first use makes a template sandbox, makes its workdir and home, runs
      the script as root, stops it and snapshots it; later sandboxes clone the
      snapshot. A changed script or mode, or an outdated base, rebuilds at the
      next use. Without the substrate's snapshots and clones only plain images
      are offered, and hello's `notes` says so. A create is synchronous unless
      an image builds: then it answers after `?wait`, `creating`, and a failed
      build leaves the sandbox in `error` with the script's last lines.
    - **Quotas** per consumer and per person (`owner.user`, across
      consumers), with overrides that replace the default whole; count and
      disk checked at creation, running, memory and vCPUs at a start. The
      manager starts a stopped sandbox itself before a command, a file
      operation or a terminal, so the contract's auto-start counts too.
      `hello.limits` carry the effective values (and, additively, `running`,
      `memMiB`, `vcpus`, `diskGiB`).
    - **Operators** (the owner and people with write access) get `/ops/*`:
      every sandbox's metadata, usage, lifecycle, sharing, the config and
      image builds — and no route to a sandbox's contents.
    - **Terminals** relay to the Box: session ids the contract's, `forUser`
      the person. `archive` isn't offered yet.
    - **The layout** (user, uid/gid, home, workdir, shell) is config; the
      first start makes the workdir and home with a run as root (mkdir,
      chown), so it holds on any substrate, and a backend may place the
      layout (the answer's `defaults`), which the manager then takes. A
      substrate that runs everything as root (`users: root`) gets root at
      `/root`.
  - **Not chosen:**
    - The contract id as the runtime name (tile-sandbox-runtime.md §11):
      nothing should depend on the two agreeing, and a consumer can never
      address an image's template sandbox.
    - A Backend at the contract's level: every substrate would redo
      partitions, people and ids.
    - Forwarding raw HTTP through the Backend: the fake would need a runtime
      server of its own; typed calls cost one JSON re-encode.
    - The fake backend as a shipped, configurable backend: it would run a
      consumer's commands in the manager's own sandbox, next to its xbin
      token. It lives in `_test.go` files only.
    - A JSON file for the table: the ecosystem's templates use sqlite.
  - **Part 2 (the same day): the `xbin` backend, the mode, the page.**
    - **The `xbin` backend is the SDK itself** (`xbinBackend{*xbin.Sandboxes}`),
      registered as the default; its test drives the manager against a
      double of the runtime's routes, so the mapping is pinned while the
      runtime's wave 2 is still being built (the live run is WP-21).
    - **`config.mode` is `auto | vm | namespace`** (another backend may name
      its own: `container`, `cloud-vm`). `auto` takes a VM where the runtime
      offers one now, else a namespace; a chosen mode the runtime lacks makes
      no sandbox (`503` with the runtime's reason, and `hello.notes` says so)
      — never another mode. The record keeps the mode it was made in, so
      `isolation` is right while it is `creating` too. **A new manager's
      mode is `vm`** (the owner, 2026-09-28: a coding sandbox is a VM unless
      an operator chooses otherwise); a manager made before keeps `auto`
      (a saved config without a mode, or sandboxes and no saved config).
    - **Mounts are a top-level `config.mounts`**, not `backendConfig`
      (changing that one needs every sandbox gone), checked as the runtime
      checks them; image builds get none.
    - **Operators take snapshots of any sandbox** (`/ops/sandboxes/{id}/
      snapshots…`): a backup and a restore are metadata-level acts, and
      still no route reads a sandbox's contents.
    - **Relayed terminals' refusals are rewritten** (the runtime's name for
      the sandbox → the contract id) by holding a refused answer before the
      upgrade; an upgrade passes untouched (`Unwrap` for the hijack).
    - **The page is one model, two views** (D96's mechanism: a feature
      registry, each view's declaration, a node test). Operators get every
      consumer's metadata; anyone who may open the page gets their own
      sandboxes — the page is a consumer of its own, with the verified
      person, within the per-person quota.
    - **The native view has a terminal**: the app's `terminal` dials only
      the tile's own routes, and here the tty route *is* the tile's own. It
      starts a login shell as a `tty` exec and attaches to
      `execs/{eid}/tty`, so a reconnect is the same shell; leaving the
      screen ends it. The one declared difference is uploads (the app
      uploads only from a composer).
    - **The UI harness runs a copy on the fake backend** (its test files
      copied in, renamed, over a filesystem resource of its own), bound to
      the agent beside `apps/fakesbx` for the pass and unbound after.
  - **Not chosen (part 2):**
    - The terminal as the native view's declared difference (the agent
      template's reason — another tile's route — doesn't hold here).
    - A silent `vm → namespace` fallback: a consumer's firewall and the
      operators' intent both read `isolation`.
    - The native terminal on `…/tty` directly: every reconnect would start
      another shell and leave the old one running.
  - **Addendum: follow-ups, the owner's decisions.**
    - **A change from the manager's own page needs the person's write
      access to the tile.** The page's frame calls run at the tile's own
      role (admin of itself). docs/auth.md D29's rule for mutating
      endpoints therefore applies to `/sbx/*` when `X-XBin-From` is the tile
      itself and `X-XBin-User-Level` is below write. Every change is
      `403 not-allowed` before it is routed: every method but GET and HEAD,
      and both `tty` routes, which are GET upgrades. A read (`files`, `tar`)
      never starts a stopped sandbox for such a person. `/me` says `level`
      and `write`. The page gives them a read-only view of the sandboxes
      they may use, and hides or disables every change with the reason.
      Other consumers' calls are unchanged, whatever the person's level on
      the manager: the contract trusts consumers.
    - **Operators run lifecycle, not access.** They start, stop and delete
      any sandbox, take its snapshots and set quotas. `visibility`,
      `members` and `shares` change only through the home consumer: its
      backend, or its verified owner there (`canAdmin`). `PATCH /ops/…`
      refuses those fields (`403`). The operators' page shows who may use
      each sandbox and has no control for it. A person's own sandboxes keep
      their sharing on the page (Yours, `/sbx/`).
    - **A failed rebuild keeps the previous good build.** A build carries
      `previous` (the last good one) until it is ready. The old template
      sandbox goes only then, never on a failure. While `previous` is
      current for the script and the mode, new sandboxes clone it. That
      covers an operator's rebuild that failed, and an outdated base whose
      rebuild failed. A script changed back clones it at once.
    - **A clone's creation error never names its source's runtime name.**
      The plan keeps the source's contract id (`fromId`), and an error
      says that id. An image's template sandbox, whether the build or the
      one kept, is `image:<id>`.
  - **Not chosen (addendum):**
    - Gating by `operator` (role and level) on `/sbx/*`: every other
      consumer's calls would then depend on the person's level on the
      manager, which the contract leaves to consumers.
    - A read-only person's reads starting a stopped sandbox: a start is
      lifecycle, and it counts against the quotas.
    - Letting a failed rebuild's image fall back to a build of another
      script: the build would not match the script the operators set.
      That build waits, and it serves only a script changed back.

- **D123 — Thin themed scrollbars and the focused-scroll tint (2026-09-27).**
  web/bx-scroll.js; docs/frontend-kit.md; docs/protocol.md §Tile ↔ shell
  messaging.
  - **Chosen.** On fine pointers (`@media (hover:hover) and (pointer:fine)`)
    every scrollbar is a 6px bar whose thumb is drawn 3px at the outer edge
    (a transparent border, `background-clip: padding-box`) and fattens to 6px
    while hovered or dragged — ~3px to the eye, a grab zone a mouse can hit.
    The box never changes width: xterm measures its scrollbar once, at open
    (the terminals now wait for their shadow `xterm.css` before opening, or
    xterm reads 0 and assumes 15px). The CSS is one text,
    `scrollCssText`, carried by every shadow root that scrolls (`scrollCss`,
    a shared lit CSSResult in `/vendor/scroll-css.js`) and, verbatim, by
    `theme.css` for documents (`hack/scroll-css.test.mjs` keeps the two
    equal). Chromium 121+ switches `::-webkit-scrollbar` off for an element
    with a non-auto `scrollbar-color`/`scrollbar-width`, and
    `scrollbar-color` inherits, so the standard properties (Firefox:
    `thin`, themed colours) sit behind `@supports not
    selector(::-webkit-scrollbar)` — Chromium never sees them, and a
    Chromium that someday drops the prefixed selector falls through to
    them. Touch keeps its native overlay bars.
  - **The tint.** A tracker per document keeps `data-bx-scroll` (an
    attribute, so lit class bindings never clobber it) on the scroller the
    next scroll would move: the innermost scrollable under the mouse; on a
    wheel, the first scroller on the composed path that can still move that
    way (or contains its overscroll), latched for 300 ms as Chromium latches
    a gesture; on a scroll key or focus, the focused element's. The thumb
    turns hazard amber. One tint per screen across documents: the pointer
    crossing into an (out-of-process) iframe gives the parent no reliable
    pointerout, so a framed document's tracker posts `xbin:scroll-focus` to
    its embedder, which drops its tint; leaving the iframe, the framed
    document gets its own pointerout.
  - **Reach.** The shell's and `/vendor` components' shadow roots include
    the sheet (importing it installs the tracker, so old workspace shells
    get it everywhere `/vendor` renders; their own canvas/sidebar keep native
    bars until `bx builtin update`). A tile document that links `theme.css`
    gets the rules from the sheet and the tracker from `xbin-client.js` —
    linking the theme is the opt-in D59 asked for; `<meta
    name="xbin-scroll-focus" content="off">` opts out. `theme.css` is still
    never injected.
  - **Not chosen:** a strict 3px box (unhittable with a mouse);
    `scrollbar-width: thin` in Chromium (~11px); widening on hover
    (re-layout, and xterm's width goes stale); the shell reading hover
    across the iframe boundary (it cannot); a tint on the scroll that a
    tile's wheel chains out to (the shell never sees that wheel — the
    tile's own scroller keeps the tint).

- **D124 — The Agent tab renders a window over an incremental fold
  (2026-09-27).** web/agent-fold.js, web/bx-agent.js; docs/overview/
  09-terminals.md §Agent sessions.
  - **Why.** The tab re-folded the whole event log, and re-ran markdown,
    diffs (an LCS per edit) and highlighting for every block, on every event
    and every keystroke, and rendered every block — a long or replayed
    conversation crawled, worst on resume, where the agent streams the old
    turns one event at a time.
  - **Chosen.** (1) `Fold` (pure, node-tested against the old fold over the
    captured fixtures) applies one event at a time; every block has a stable
    `key` and a version `v` bumped when it or a nested block changes, and
    `st` digests the latest status fields (the per-render log scans are
    gone). (2) At most one render per frame (`scheduleUpdate` awaits a
    frame), so a burst folds as it arrives and paints once. (3) Rows are
    `repeat`ed by key under `guard([v, ui, …])`; markdown, diffs and stripped
    output are memoized on the block per version (`cached`), and a folded
    card's body, a file patch or a thought renders only once opened. (4) Only
    a window renders: the last 30 blocks (filled to two views), a page of 30
    more whenever the reader is within 1.5 views of the top — on a touch
    scroll only once it settles, since a `scrollTop` write stops iOS
    momentum — or all of them from the "earlier entries" row (for find).
    Following the bottom with over 120 rendered and 6 views above, what is
    more than 2.5 views up is dropped (above the pinned view: invisible;
    2.5 > 1.5, so dropping and loading never ping-pong).
  - **No jumps.** `overflow-anchor: none` and one explicit anchor for every
    update: `willUpdate` records the first visible row's top (binary search
    over the rows), `updated` — inside the same frame, before paint —
    moves `scrollTop` by however far it moved, or pins the bottom when the
    reader was there. The same path covers a page prepended, a block above
    growing, the per-turn changes block spliced in. A card the reader opens
    keeps their view instead of chasing the bottom.
  - **Not chosen:** native scroll anchoring (Safari has none; with a manual
    fallback beside it, the two can double-correct); `flex-direction:
    column-reverse` (free bottom-pinning, but the view then moves whenever a
    new turn streams while the reader is scrolled up); server-side paging of
    the log (the whole ring — 5000 events — folds in milliseconds; the cost
    was rendering); `content-visibility: auto` (estimated heights shift as
    rows paint in, which is the jump this avoids); windowing a subagent
    card's children (rendered only while the card is open).

- **D125 — The app's screens: panels you swipe between, the phone's own
  arrangement, tile widgets, create-a-tile, and no Safari (2026-09-27).**
  native/ios App/Shell (PanelStack, Screens/), App/Tiles/Widgets, XbinCore
  Client/{Home,MobileScreens,TileCreate,WidgetStore}.swift; web/xb-native.js
  + web/xb/rt-runtime.js; internal/obs/prefs.go; workspace-template/shell/
  layout-sync.js; native/spec/tree.md §13, docs/native.md §Widgets,
  docs/protocol.md (the `prefs` event). The owner's direction after using
  TestFlight build 3 on a phone.
  - **Panels, not a root swap.** A window is Home → a screen → a tile (or a
    terminal/agent), side by side: a left-edge swipe goes back
    (interactive — let go early and it snaps back, a peek), a right-edge
    swipe right after goes forward to what you left; any new navigation
    drops that. A native tile's own stack pops first; web views give the
    edges up (their page back is a bar item). The panel you left stays
    mounted, so forward is instant. Before, opening a tile replaced the
    root: no back, and no way home.
  - **Home lists screens** (owner's choice), by Mine / each org / Workspace
    inside their folders, as the web sidebar files them; shortcuts on top,
    search and All tiles at the bottom, + New screen. The workspace
    default shows while the user has no screen of their own, as the shell
    seeds.
  - **A phone arrangement per user** (owner's choice): the `mobile-screens`
    pref (same bucket as `layout`) holds each screen's order, small/wide
    and hidden tiles; unset, the web layout's order. It never writes the
    web layout — the shell rewrites that object whole on its saves — except
    that a tile created on a personal screen also lands there at a free
    spot. Edit mode is a list editor (reorder handles, small/wide, hide),
    sturdier and accessible than dragging cards. The app read the layout
    from the wrong prefs bucket (`shell`; the shell's is `root`) — fixed,
    with a fallback read.
  - **Widgets** (owner's choice: this round). A native tile may render a
    second, small tree with `widget()` — its card, `small` or `wide`,
    stack/text/icon/badge/chart/progress/button/row only. The runtime sends
    it only to an app whose caps list the feature `widget` (old apps see
    byte-identical traffic); widget errors carry `target: "widget"` and
    fall back to the standard card, never the tile. The app keeps at most
    6 live runtimes (LRU; the open tile never goes), shows a cached widget
    tree when a tile isn't live, and treats a tile that sends no widget
    within 3 s of its first render as having none. Every other tile gets
    the standard card (icon, title, badge, status dot).
  - **Create a tile on the phone**: name + owner (the shell's choices) →
    `POST /api/xbin/create` → the tile opens on "What should this tile
    be?": a prompt for an agent, or a terminal.
  - **No Safari.** Chrome tiles (the admin console and the like), which
    need the user's own session, open in an in-app web view with a cookie
    store of its own, signed in by redeeming the web ticket (D100) inside
    it. The web ticket stays; the Safari hand-offs go.
  - **Prefs, shared by two editors now:** writes are serialized per bucket
    (writes to different keys could lose each other), and every write
    publishes a `prefs` event to the user's own clients with an optional
    writer id; the shell reloads its layout on another client's write
    unless an edit is in progress (existing workspaces: `bx builtin
    update`).


- **D126 — Native helpers come prebuilt from static hosting
  (xbin.dev/static/helpers), pinned by a committed manifest, and stay
  rebuildable from source (2026-09-28).**
  hack/helpers-lib.sh, hack/fetch-helpers.sh, hack/publish-helpers.sh,
  hack/helpers-static.sh, hack/s3-lib.sh, hack/helpers.sha256,
  hack/check-large-files.sh; Makefile
  `helpers`, `helpers-build`, `helpers-publish`, `integration-deps`,
  `large-files`; .github/workflows/ci.yml; docs/maintenance.md → "Prebuilt
  helpers". The owner: CI should use binaries, users must be able to
  rebuild; big binaries go on GitHub only on real release tags.
  - **Groups keyed by their build inputs**, not by version or commit:
    containerfs (gocryptfs, fuse-overlayfs) and vm (vmlinux, mkfs.erofs,
    QEMU + its two blobs, vhost-device-vsock). The key hashes the build
    scripts (the pins live in them), the patches / kernel fragment and the
    file list, so a set is never used for inputs it wasn't built from, and
    an unrelated commit doesn't invalidate it. Firecracker stays a pinned
    upstream download.
  - **The committed manifest is the trust root**: every object's and every
    file's sha256. The bucket is only transport; a mismatch is fatal and
    installs nothing. An object is never overwritten — builds aren't
    byte-reproducible and a committed manifest may pin it.
  - **Fallback, not failure**: an unpublished key (a PR that changed the
    inputs), a failed download or a version override builds from source
    with the same scripts. CI warns (annotation, pins-offline) rather than
    fails, so such a PR still lands; a maintainer publishes after.
  - **Static hosting, curl only**: the objects are served from
    `https://xbin.dev/static/helpers/<group>/<key>/amd64.tar.zst`
    (`XBIN_HELPERS_URL` overrides), so fetching needs no credentials. The
    owner's S3 can't serve public buckets, so a maintainer builds the tree
    with `hack/helpers-static.sh` (into the gitignored
    `website/static-helpers/`, copied to `dist/static/helpers` by `make
    website`) and deploys it with the website. `make helpers-publish` still
    uploads to an S3-compatible bucket for a mirror; its credentials live in
    the gitignored s3secret.env, parsed never sourced, and reach curl
    (`--aws-sigv4`) on stdin. No aws CLI dependency. Never from CI.
  - **Releases keep building from source** (deploy/publish-release.sh
    unchanged): a bundle never depends on the bucket, and an unpublished
    key only warns.
  - **No big or native binaries in git** (`make large-files`, in `check`
    and the pre-commit hook): > 1 MiB or ELF / Mach-O / PE fails unless
    hack/large-files.allow names it with a reason.

- **D127 — Tile deployments: named deployments of one tile, each with its
  own URL, data and vault, one of them the primary (2026-09-28).**
  docs/tile-deployments.md; docs/bx.md (`bx deployment`, `bx promote`);
  docs/protocol.md (`/api/xbin/deployments`, the `deployments` event,
  `/c/<tile>+<name>/`); plans/dev-lifecycle/ (05-model §13, 14-implementation
  M2, 16-open-questions §1.1). The owner ratified P3–P4 and confirmed P7,
  P19 and P22–P24 on 2026-09-27, and on 2026-09-28 revised P13, decided P17
  and extended P21 (below); the rest are recorded as built, at the owner's
  direction, on 2026-09-28. Built on D119.
  - **(a) Non-primary outbound calls go to the provider's primary,
    read-clamped, with a per-edge `block`** (P3). `match` (routing to the
    provider's same-named deployment) comes later, on the same per-edge
    record.
  - **(b) Parity** (P4): terminal-level users and their agents deploy to the
    primary as saving does today; tile managers can protect the primary (m).
  - **(c) Storage follows the deployment** (P6); `main` owns today's keys
    forever, and non-main state lives at a `.deployments` level no older
    binary lists.
  - **(d) The primary is a role** (P7). Every inbound edge resolves through
    one resolver that returns the primary for every v1 edge-policy value.
    Reassignment (tile managers only, a loud confirmation) moves routing, not
    data.
  - **(e) Promotion moves code only** (P10); data, vault, config and routing
    are late-bound to the target. Promote shows the diff and deploys exactly
    the checkpoint it showed (409 if the work tree moved since).
  - **(f) Authority stays per tile** (P11). A deployment is not a principal;
    non-primary deployments are narrowed by edge policy.
  - **(g) Self-calls stay inside the caller's deployment** (P12); tile
    principals can't call another deployment of their own tile.
  - **(h) Non-primary cron jobs and bus subscriptions are active; interface
    instances and ingress hosts are dormant** (P13, revised by the owner
    2026-09-28: "allowing cron for non-primary deployments is fine; bus is
    trickier but subscribe-only would be ok — like read binds"). A
    non-primary deployment's cron fires and its bus subscriptions deliver to
    that deployment (its backend, its data), never the primary; reading
    another scope's bus goes through the edge policy (`read` allows, `block`
    refuses), re-checked at every delivery; publishing stays in its own
    namespace. A per-deployment `deliveries` switch, on by default and
    manager-only, silences a noisy one. Notifications from a non-primary
    deployment are never pushed, and its status and build activity travel
    only in the `deployments` event, delivered by access (rules C2 and the
    event audience).
  - **(i) Non-primary data and vault start empty** (P14); seeding is
    optional, and seeding and vault copy are tile-manager acts.
  - **(j) The `+` qualifier** (P17, decided by the owner 2026-09-28).
    `/c/<tile>+<name>/` resolves only for tiles with a record and only after
    today's resolution fails; the bare URL is the primary. Signals
    (`X-XBin-Deployment`, `XBIN_DEPLOYMENT`, whoami) are absent for the
    primary; credentials and stored state are absent for `main`. `+` is kept
    because URL paths keep it; no query string ever carries a qualified ref
    (queries take `tile` and `deployment` separately, and a query `tile`
    with a `+` that names no tile is a 400, since `+` reads as a space
    there); and `+` is refused in every new tile name, for every creator
    (BREAKING, docs/changes/2026-09-28-plus-in-tile-names.md; existing
    directories keep resolving, and `bx doctor` flags them).
  - **(k) Chrome and xbin-capable tiles may pause live reload but can't have
    non-primary deployments** (P19); approving an `xbin`/`xbin:*` grant is
    refused while non-primary deployments exist, and a non-primary principal
    never satisfies a governance check.
  - **(l) A non-primary deployment is an accident boundary, not a trust
    boundary** (P20).
  - **(m) A protected primary** (P21, extended by the owner 2026-09-28) is
    never the live reload target or a session's target. Every change to its
    code is a tile-manager act naming the reviewed checkpoint
    (`checkpoint`/`expect`, a compare-and-set on the record's `seq`); its
    build products come only from manager operations. Managers act from a
    human session, from the host with the owner's root token (`bx
    deployment …`), or through the admin tile's Deployments tab: a frame of
    an `xbin`-capable tile minted under a person's own login stands in for
    that person, who must pass the manager gate, for protect, unprotect,
    reassign, deliveries and alwaysOn only. Terminal, agent, instance, cron
    and bus tokens never do, nor any frame token they mint.
  - **(n) Resource declarations are deployment-level** (P22): each
    deployment provisions what its own code declares, in its own namespace;
    a pinned primary provisions from its checkpoint's `scope.json`, read
    beneath and validated; per-deployment limits default to the tile's, are
    set by managers, never above the tile's ceilings.
  - **(o) Edges that can't be read-clamped are blocked for non-primary
    deployments in v1, with no override** (P23): custom roles (so the
    agent's sandbox managers), stream interfaces, lan-ingress links,
    net-provider splices, and host-shared networking.
  - **(p) The terminal's API select picks a session's target** (P24),
    defaulting to the primary; a protected primary is not offered, and the
    default then falls to the live reload target, else "API off"; fixed for
    the session's life.
  - **(q) Primary first** (P25): a non-primary deployment never takes the VM
    budget, CPU weight, disk quota or per-tile caps the primary needs;
    separate cgroups per deployment.
  - **(r) xbind's API is default-deny for non-primary principals** (P26):
    every `/api/xbin/*` route is classified deployment-scoped, primary-only
    or neutral, and a guard test (`TestDeploymentRouteClasses`) keeps the
    list complete.
  - **(s) Edge-policy values fail closed** (P27): an unknown or invalid value
    reads as `block`, and any `block` among several authorizing edges refuses
    the call.
  - **(t) A (scope, name) namespace is shared by the scope's same-named
    deployments** (P28); seeding, resetting or restoring it needs authority
    on every claimant and stops all of them; it is deleted with its last
    claimant; no v1 reassignment splits a scope's primary data.
  - **Why.** Pausing (D119) protects viewers but leaves one runtime; a
    developer, or an agent, needs a second copy of a busy tile with its own
    data to try things on, and a way to ship exactly what was reviewed. Doing
    it per tile, with the primary as a role and everything else narrowed by
    edge policy, keeps authority and routing where they are today.
  - **Not chosen:** events with a qualified `component` or non-primary
    activity on `reload`/`build-*`/`status` (old shells and the app would
    reload ancestors and toast; rule C2 instead); a separate target picker,
    or sessions defaulting to the live reload target (P24); `:` as the
    qualifier (`notes:dev/` parses as a scheme in a relative URL, and `:`
    separates the grant grammar's class); only the two narrow `+` refusals
    with a warning otherwise (the owner chose the full refusal); a `full`
    edge that lifts the read clamp (O3, still open: non-primary deployments
    lose LLM completions meanwhile); dormant cron and bus registrations (the
    original P13, revised); element principals passing the manager gate by
    their tile's grants alone (only the admin tile's frame, for a manager).
    Still open with their defaults built: O1 (offload of `main`'s encrypted
    volumes), O3, O5 (protection covers code, not the vault). The opt-in switch
    (`--tile-deployments`, `XBIN_TILE_DEPLOYMENTS`) defaults on (O4's
    recommended answer).

- **D128 — The app's Home: the workspace's own name and icon, All tiles as
  the web sidebar's tree, compact rows, sessions on tiles (2026-09-28).**
  native/ios App/Shell (Screens/HomeView, StandardCard, ScreenView,
  SwitcherOverlay, BrandImage, RootView's BrandIcon), App/Tiles/Widgets/
  TileCard; XbinCore Client/{Navigator,DataURI,Catalog}.swift, XbinTerm
  TermDirectory (`byTile`); XbinRendererModel Widget.swift; native/spec/
  tree.md §13, docs/native.md §Widgets; web/xb/preview-host.js. The
  owner's list after using TestFlight.
  - **Home's header is the workspace's branding** (D76): its icon and title;
    the address only when no title is set. The icon is a `data:` URI — the
    app drew only emoji, so an admin's image never showed. XbinCore decodes
    the URI once (a small cache); bitmaps go through ImageIO, an SVG is
    drawn once by an offscreen web view with script off and no navigation,
    then kept as a bitmap — the switcher, the inbox and Home share it.
  - **All tiles is the web sidebar's tree** (bx-side.js, D24/D55), rebuilt
    in XbinCore from what the app already reads (the `layout` pref's
    `side.folders`/`sharedOpen`, `/screens`' `folders[scope]`; nothing new
    fetched, nothing written): personal folders first (tiles, `#screen`,
    `#orgscreen`), then owner sections (mine, whoami's orgs, workspace)
    each with its shared folders, its unfiled tiles by label and an org's
    screens; a personally filed tile leaves its section; labels are the
    basename, or the path when two collide. Hidden, blueprint and archived
    tiles are skipped everywhere in the app (the web's show-hidden toggle
    has no counterpart). Folders open as the user left them on the web;
    folding here is the phone's own. Search stays flat.
  - **Compact.** Home, All tiles and the switcher are single-line rows of
    about 36 pt; a tile row shows its name (the path moves to the
    accessibility label, and to a trailing caption in search). The screen
    grid's cards are 132 pt tall (170 before), with 12 pt margins, a
    10 pt inset and a 28 pt icon: a widget's own box is about 157 × 112 on
    a 390 pt phone. Existing widgets stay valid — the size classes, the
    vocabulary and clipping are unchanged; only the box is smaller: the
    counter's fits at the default text size, the dense `widget-wide`
    fixture (five rows) now clips its last row. Above the default text
    size the cards grow with Dynamic Type as body text does (XbinCardHeight:
    about 179 pt at xxxLarge), so large text makes a taller card instead of
    cutting the counter's +1, as the first compact build did (the renderer
    snapshots draw each text size at its own height).
  - **Terminals and Agents leave Home**: sessions are reached through their
    tile and the inbox. "Needs you" stays, a row with its count.
  - **Sessions on tiles**: a row, a standard card and a widget card show
    `>_ n` for terminals and ✦ n for agents, amber when one waits for the
    user, from the session directory the `term` events keep live — on a
    widget, in the card's corner outside the widget's tree. The long press
    lists the tile's sessions (tap opens, with where each is) and replaces
    "Terminal here" / "Agent here" with one **New session…** (Terminal,
    Agent for now; the tile's own session screen takes it over).
  - **Not chosen:** keeping the sidebar's raw web labels out of the app
    (humanized titles) — the tree is the web's, so are its labels; a
    rasterized icon from the server (it stores the URI as given, and no
    route serves a bitmap); a nested-folder count that includes folders
    with nothing to show (bx-side.js counts them; the app leaves such a
    shared folder out).

- **D129 — The terminal window's panels: full width, or beside the
  terminal at a width you drag; the tag is "Dev API" (2026-09-28).**
  web/frame-panels.js, web/bx-deploy.js; docs/elements.md;
  docs/tile-deployments.md §The Deployments panel; plans/dev-lifecycle/10-ux
  §3.1, §3.3.
  - **Why.** The owner, after using tile deployments: Deployments opened as
    a 50/50 split with the launcher (a window without sessions always showed
    the terminal host), only code could sit beside the terminal, its divider
    was a mouse-only 20–80 % range, and the row tag "target of this
    terminal" wrapped in the side list and meant little. The owner's ruling
    names it "Dev API".
  - **Chosen.** The layout is a panel (code, logs, PRs, deployments) or the
    terminal alone, plus a per-window `beside` flag: `⇋`, now the last
    switcher button and a toggle, puts the terminal beside whatever panel
    shows (from `>_`, code — the old split); panel buttons keep the flag,
    `>_` clears it, and `open(layout)` (the shell's menus) shows a panel full
    width, so Deployments opens full width. The launcher shows only where
    the terminal does. One width, `paneW` (percent), for every panel: the
    divider drags (the shield goes up only once the pointer moves, so a
    double-click — the reset — reaches it), takes ←/→/Home/End, has
    `touch-action: none`, and neither side goes below its floor (230 px, the
    Deployments side list; 200 px of terminal). Saved in `term:<tile>`: code
    beside as `layout: 'split'` (an older frame restores the same window),
    another panel beside as `beside: true` (an older frame shows the panel
    alone), `paneW` read from the older `codeW` when absent; `'deployments'`
    still restores as `'term'` (NP-10-2). The Deployments panel's narrow
    drill-down and one-column tables follow its own width (`@container`),
    so it works in a pane; touch-sized controls stay a phone (`@media`)
    rule. The tag is `Dev API` — same meaning: the deployment the active
    tab's API calls and `bx` reach (`XBIN_DEPLOYMENT` when non-primary) —
    a one-line pill with that sentence as its tooltip, fed by the frame as
    the panel's `target` property so it follows tab switches (it read a
    stale frame before); a `● live reload` pill marks where saves go.
  - **Not chosen:** a split per panel remembered separately (a second
    width, and a toggle that meant different things per panel); pixel
    widths (a resized window would squeeze the terminal instead of both);
    the shield on press (it swallows the double-click); renaming the tile
    API select's `🔌 target:` entries (only the tag was ruled on).

- **D130 — Long agent conversations: the log pages, the client holds a
  window of it (2026-09-28).** internal/agent/page.go, internal/term/
  agentpage.go; docs/protocol.md §Agent session events → Pages. Amends
  D124, which windowed only the DOM and held the whole log. E3/E4 (the app,
  the agent template) build on this entry.
  - **Why.** Every open replayed the whole ring (5000 events / 8 MiB), and
    the web tab and the app folded and held all of it however little was on
    screen; a resume published each replayed event on `/ws/events`, whose
    64-event buffer then evicted every subscriber; a chatty command logged
    one event per output chunk; listing past sessions parsed every
    transcript whole.
  - **Chosen.** (1) `…/events?before=&limit=` (and on a past session's
    `/agent/history/{id}/events`) returns a page cut at a **safe cut**: the
    server replays the fold's bookkeeping (`safeCuts`: which event opened
    the card each later event lands on) and never starts a page inside a
    card, nor after anything still open — the text being written, the
    running turn's unfinished calls and unanswered requests and plan, and a
    snapshot the daemon has not reported yet (the snapper marks it before
    the event that asks for it is logged). So pages fold one by one to
    exactly the whole-log blocks. Each page carries a state header — the
    fold's status digest and the last turn's usage/number as of its first
    event, and on a live tail the waiting requests — so a client can start
    from the tail. Parameters are additive: `?since=` is byte-for-byte
    unchanged, an old xbind ignores them (clients detect `hasOlder`).
    (2) `tool.update` runs of bare `{id, outputDelta, parent?}` coalesce
    over 32 ms like message deltas (appending twice is appending once).
    (3) A resume's replay is logged but not published one by one: one
    `replayed` hub event `{seq:0, first, last}`, then the status that ended
    it. Old clients drop a seq-0 event and catch up on the next seq gap —
    the web and the app already refetch on one. It also stops replayed
    prompts from starting Live Activities. (4) `ListHistory` (and resume)
    read a history file's head — `meta` is written first — with a streaming
    decoder; files written with events first still read.
  - **The web tab (E2).** web/agent-pages.js, web/scroll-window.js,
    web/bx-agent.js. (5) The tab holds a run of **segments** — pages, each
    folded on its own from its state header (`Fold.seed`); block keys are
    the seqs of the events that open them, so a page dropped and fetched
    again, or refolded for a late event, keeps its keys (lit identity, open
    cards) — only the page a late event belongs to refolds, and the window
    is kept by key, so nothing snaps. (6) It opens on the tail page (an old
    xbind answers everything: one segment, nothing unloads), loads older
    pages as the reader nears the top, and drops whole pages about 3 views
    beyond the rendered rows in either direction; while the reader is far
    up the live tail goes too — live events then move only the status
    digest and a count, the "↓ N new — jump to latest" pill re-reads the
    tail, and scrolling down fetches the dropped pages back (the old tail by
    `?since=`). A live tail grown past three pages while followed is split
    at the cut the server gives for its tail page, without refolding, so
    its top can go. (7) D124's anchoring moved into `scroll-window.js`
    (framework-free, `/vendor/`, for the agent template too), generalised
    to trim and grow on both sides. (8) A streaming message re-parses only
    its last top-level markdown block and swaps only that block's DOM
    (`mdInto`; per-block HTML equals the whole parse — node-checked), so a
    selection survives. (9) A hidden tab folds but skips rendering
    (`shouldUpdate`), catching up when shown. "Load all" stays an explicit
    mode (every page, nothing unloads) until the pill.
  - **The agent template (E4).** builtin-templates/agent: model/session.js,
    chat-window.js, chat-cards.js, native/chat.js; API.md §The frontend.
    Its backend already paged the view (`view?before=&limit=`, pages cut
    at messages, a call never split from its results). (10) Both views
    turn on `page: 50` and `deltas`. The model holds a run of consecutive
    pages and lets go of messages a page or more beyond the blocks a view
    draws (`keep`), newer ones — the live tail with them, live messages
    then counted, not held — only while the reader is away from the
    bottom; `loadNewer` reads them back a page at a time. Cuts fall at a
    message, so a page read brings back exactly what went, and block ids
    (the fold's `m<id>`/`c<call>`) are stable without seq keys. A reset
    or resync re-reads the pages held instead of the newest one, so the
    rows on screen stay. (11) The web renders a window over
    `/vendor/scroll-window.js` (rows are the timeline's `[data-k]`
    children), with the pill; a message's markdown is memoized on its
    block object and the streamed answer written per top-level block (the
    template keeps its own `mdInto`: its markdown policy is its own). An
    xbind from before D130 serves no scroll-window.js — the template is
    an instance's files, which a downgraded xbind still serves — so it is
    imported dynamically and, missing, every block renders as before.
    (12) The native view renders the tail and grows it on `more`; it trims
    — and lets go above — only while `scrolled` says the reader is at the
    bottom, where the app's transcript keeps the bottom still, and never
    lets go below: until the renderer anchors a row across a prepend or a
    trim (E3's `scrollPosition(id:)`), that would move what the reader
    sees. Rows are rebuilt only when their block or open state changed,
    argument rows parsed once per block, parent screens rebuilt only when
    their blocks change. The vocabulary and wire are unchanged. Harness
    `agentTemplateLong`; on a 482-message conversation at CPU 4×, 30 more
    units at the bottom went from 8 long tasks (max 488 ms) to none, a
    reload from 470 to 279 ms (482 → 91 rows in the DOM).
  - **The app (E3).** XbinAgent Window.swift/Rows.swift/Feed.swift,
    XbinCore MarkdownLexer.Incremental, XbinRendererModel MarkdownMemo,
    XbinRenderer Chat/Transcript.swift, App/Agent/AgentScreen.swift.
    (13) `AgentWindow` ports agent-pages.js. It holds segments, each an
    `AgentTranscript` seeded from its state header. It prepends older pages
    and drops whole segments beyond a margin of items (100) around the
    visible rows, in either direction; the live tail goes only while the
    reader is away from the bottom. Detached, live events move a digest
    (status, pending requests, the Live Activity) and the `fresh` count. The
    tail is split at the server's cut once it passes three pages. Item ids
    are the seqs that open them (a tool card's was a per-fold turn counter).
    An older page is sealed: its last message stops "writing", as the next
    page's first event would have made it. (14) `AgentSessionFeed` opens
    on the tail page (an xbind that does not page answers the whole replay:
    one segment, nothing unloads), follows from its cursor, re-reads the
    tail on `replayed` (`SessionHubEvent` now lets that seq-0 frame
    through), and publishes only when the window changed — follow and
    `/ws/events` deliver every event twice. (15) `AgentRows` (main actor,
    `@Observable`) diffs each published window into one observable row per
    item, by id and by value, so a delta re-renders its own row. A row
    reads the session's status only while it may stream. Open cards are
    remembered by id across an unload. The list is its own view: a
    keystroke re-renders none of it. (16) `TranscriptView` (also the native
    tiles' `transcript`) binds `scrollPosition` to the rows' ids
    (`scrollTargetLayout`). Measured on the iOS 27 simulator with slow
    drags, this keeps the top row still through a prepend or an unload —
    but only when the change lands at rest. A page landing mid-gesture, or
    scrolled into during the drag it landed in, shifted the view. So it
    asks for more only at rest, within 1.5 screens of either end, repeating
    every 0.5 s while it stays there, and reports visible ids for unloading
    only at rest. It sticks to the bottom only while at the bottom. The
    pill scrolls down and re-reads the tail when rows below were unloaded.
    The app delivers its at-bottom reports to the feed in order, including
    one that came before the feed existed. (17) Markdown:
    `MarkdownLexer.Incremental` re-lexes a growing text from the last
    top-level block starting on a line that was already whole. Streaming
    the corpus and random junk a few characters at a time always equals
    the whole lex. The memo is an LRU per message id. Tests: XbinAgent
    WindowTests (pages fold to the whole log, unload/reload keeps ids,
    detach counts, split, late and duplicate events, old xbind, the state
    header, the feed's tail/older/replayed/jump, the rows' identity);
    XbinAgentLongTests (a 300-unit fake session: no drag moves the text
    under the finger by more than the drag across page loads and unloads;
    the counted pill). Debug builds take `-XbinAgentPageLimit` and
    `-XbinAgentKeepMargin`.
  - **Not chosen:** cuts only at turn boundaries (one long agentic turn is
    thousands of events — the tail would be the whole turn); client-computed
    cuts (the client cannot see the snapshots still to come); a
    `files.changed` late-attachment list per page (safe cuts make it
    unnecessary); a history sidecar file (a second file per entry to keep in
    step, for what the head already gives); one fold refolded on every
    prepend (new block objects each time: memos and lit identity lost);
    `content-visibility` for the rows (D124's reason stands). In the app: a
    UIKit collection view with manual anchoring (SwiftUI's id-bound scroll
    position holds once changes land at rest); a flipped list (breaks
    context menus, selection, and VoiceOver order); a copy-on-write
    transcript republished whole to one observed property (every event
    re-rendered every visible row).

- **D131 — Branch-assigned deployments: a deployment may require the work
  tree's branch; checkout-driven routing, as offers (2026-09-28).**
  docs/tile-deployments.md §Assigned branches; docs/bx.md (`bx deployment
  add --branch|--new-branch`, `bx deployment branch`, `--other-branch`);
  docs/protocol.md (`POST /deployments/branch`, feature `branches/1`, op
  `branch`); plans/dev-lifecycle/05-model.md flow H. The owner's rulings of
  2026-09-28, built on D119 and D127.
  - **(a) A requirement and a label, not a feed.** A deployment other than
    `main` and the primary may name a branch of the tile's repository
    (record: an optional per-deployment `branch`). It still follows the
    work tree or runs a checkpoint; only a work tree on that branch may feed
    it. The branch is read host-side beneath the tile (`.git/HEAD`, never a
    git run, like the `Xbin-Work-Tree-Head` trailer); a detached, missing or
    unreadable HEAD is no branch. Refused on `main` and on the primary;
    reassigning the primary to a deployment clears its branch, and a stored
    branch on either (an older binary that reassigned without knowing it)
    reads as none rather than holding the tile. Older binaries keep
    `branch` and `branchOverride` verbatim through the record's unknown
    fields.
  - **(b) Explicit ops are guarded.** Attach, resume, reload now and a
    deploy from the work tree, and add from the work tree (with attach or
    not: the first ruling covers it), answer 409 naming both branches.
    `confirm: "other-branch"` takes the work tree's branch this time; attach
    and resume keep it as the deployment's `branchOverride`, which lapses
    when live reload moves or the work tree's branch changes again. A work
    tree on no branch can't be followed even so. A capture-time trailer
    check (`Xbin-Work-Tree-Branch`, HEAD read before and after the capture)
    refuses the op when a checkout raced it. A deploy of a checkpoint,
    promote and roll back are not fed by the work tree and are not asked.
  - **(c) Saves are guarded too.** In `routeBatch`, a tile whose live reload
    target has an assigned branch hands the batch to the plane's per-tile
    guard worker, which reads HEAD once per debounced batch, takes a
    background checkpoint (its own capture budget, so people's requests
    never find it spent) whose trailer is the second check, and only then
    deploys the batch. A mismatch without an override deploys nothing:
    live reload pauses, the target pinned to the checkpoint it runs — the
    last batch deployed on its branch, else its newest deploy-log
    checkpoint taken on its branch — logged as a `pause` by `xbind`, and a
    `deployments` event op `branch` `{deployment, assigned, workTree,
    related, paused}` goes to the write audience. Pausing, or attaching live
    reload elsewhere, while the work tree is off the target's branch pins it
    the same way. Every tile without an assigned branch keeps the no-cost
    save path of D119d (one more in-memory lookup).
  - **(d) On a detected switch, offer to follow.** op `branch` names
    `related`, the deployment assigned the work tree's new branch: clients
    offer "Attach live reload to <it> (<branch>)" (resume, while paused)
    through the normal confirmation; switching back offers "Resume live
    reload on <dev>"; with no match, "Keep <dev> on <branch> this time" (the
    override) or "Add a deployment for <branch>…". This is checkout-driven
    routing — option B2 of 04-options, rejected when the design was made
    because it turns routine git use into a routing change — now the
    owner's call, kept to offers and a pause: a checkout never moves live
    reload by itself.
  - **(e) New branches only.** Add's `newBranch` creates the branch in the
    tile with a confined git switch (`internal/confine`, never a host exec;
    `git switch --create=<name> --end-of-options`: the name attached to its
    option, since `-c --end-of-options <name>` would read the marker as the
    name) that changes no file, so nothing reloads. It never switches to an
    existing branch; a narrow exception to "xbind never checks out a branch
    in a tile" (03-current-state §7.2).
  - **(f) Wire.** Feature `branches/1`; `POST /deployments/branch {tile,
    deployment, branch|null}` (terminal level, like add); `branch` and
    `newBranch` on add; `confirm: "other-branch"` on the guarded ops (on
    add, joined to `copy-data` with a comma when both apply); `workTree.branch`
    in the write audience's state, `branch`/`branchOverride` per deployment,
    `impact.branch` in dry runs, `branch` on deploy entries. Bodies are
    decoded strictly (12-compat §10.2), so clients send the new fields only
    when `features` lists `branches/1`; bx refuses the commands that need it
    against an older xbind and drops `--other-branch` there.
  - **(g) The terminal window** (phase 2): the offers of (d) lead the live
    reload chip's menu and the Deployments panel's header, computed from the
    state alone (the work tree's branch, each deployment's, the last pause's
    actor) so a window opened later offers the same; op `branch` refetches
    the state and prints the tile's terminals a grey line in place of the
    pause's. No offer shows while the target takes the work tree's branch
    this time (the user chose it). A 409 naming both branches asks "Use
    <branch> this time" and retries with `confirm: "other-branch"`. The add
    form's Branch control, the overview's Branch row (Set branch…, Clear
    branch), and `⎇ <branch>` in the side list and the deploy log, whose
    entries now name the branch their own capture was taken on (an
    `Xbin-Branch` trailer in the deploy log; the checkpoint's first
    capture's otherwise). The pure logic is `web/deploy-branch.js`, a leaf
    beside deploy-state.js and deploy-panel.js.
  - **Not chosen:** routing live reload by checkout outright (B2 as
    designed: an agent's routine checkout would move a live URL); running
    git on the tile's repository host-side to read the branch (D78);
    pinning a paused target to a capture of the other branch's work tree;
    letting xbind switch to existing branches.

- **D132 — The app's tile sessions screen: the web terminal window's
  counterpart, native, in phases — B1 the screen, its launcher and tabs;
  B2 live reload and deployments; B3 code and logs; B4 PRs (2026-09-28).**
  native/ios App/Shell/Screens/{TileWorkspace,Deployments,TileTools}.swift, App/Terminal/SessionTab.swift
  (TerminalScreen and AgentScreen hosted as tabs), App/Model/Navigation.swift
  (`Surface.sessions`), WorkspaceEvents (per-tile `deployments`); XbinTerm
  TileWorkspace.swift (SessionTabs, TileLauncher), TermDirectory
  (`deployment`); XbinCore Client/{Deployments,DeployView,DeployBranch}.swift
  (a port of web/deploy-state.js, deploy-branch.js, deploy-panel.js),
  Events.swift (`AppEvent.deployments`). The owner: "new terminal/agent
  should be one 'new terminal' that opens a tile (management)-terminal that
  matches the webui shell terminal more closely — full feature parity,
  first on new selecting harness with few boxes, tabs to switch between
  agent/terminal instances, code view/logs/PRs, live reload and deployment
  management"; ruling: native, in phases (B1…B4), each releasable.
  - **One way in.** A tile's long press has one **New session…**, which
    opens the tile's sessions screen on its launcher; its list of running
    sessions opens the screen on that session's tab; a tile screen's ⋯ has
    **Sessions & tools** and **New session…** (Terminal here / Agent here
    are gone). The screen is a panel at level 2 like a tile (D125), a new
    surface `sessions(tile:show:)` — the standalone terminal and agent
    screens stay for deep links, the inbox, Handoff and the build chooser.
  - **The launcher** is frame-launcher.js's `launcher`, its lines ported to
    XbinTerm (TileLauncher): a box for Bash and one per agent provider
    (`GET /agent/providers`), the VM switch where VMs can run — the same
    per-user, per-tile pref as the web's (`termvm:<tile>`, `wantVM`) — and
    the tile's recent agent sessions with Resume (`GET
    /agent/history?cwd=`, a `+` escaped), plus sessions running on the tile
    without a tab here. An agent is created before its tab (eager, as the
    web's launcher, so its pickers load before the first prompt); Bash opens
    a tab whose terminal makes the session. It shows first when the tile
    has no session, and on `+`.
  - **Tabs** are the session directory's (D73) sessions on the tile, shells
    and agents together, oldest first (SessionTabs keeps them in step: a
    rename elsewhere reaches the tab, a session that ends leaves its tab
    greyed until closed, a new shell's tab waits for its id and absorbs the
    row a listing made meanwhile). Hidden tabs stay mounted, like the web's
    hidden panes — the socket, scrollback and transcript survive a switch;
    the tab in front gets `panelActive` (the keyboard, VoiceOver) and puts
    its items in the bar, and its own title (a shell's OSC title) is the
    bar's. Tap a tab, or swipe the strip; a tab's long press renames, ends
    or closes it — close keeps the session running (the launcher lists it).
    A terminal hosted as a tab (`\.sessionTab`) is one session: its sheet
    keeps the network, VM and keyboard settings, and full screen is the
    standalone screen's.
  - **Tools** (a menu beside the tabs, as the web window's layout switcher
    sits in its title bar; in the app "tools" means these panels, never an
    agent's tools): **Live reload & deployments** (B2), **Code** and
    **Logs** (B3), **PRs** (B4). A tool's state lives as long as the
    screen; a file, a diff or a proposal opens as a sheet, so the tabs
    underneath keep their sockets (a pushed screen would take them down).
  - **B2, live reload and deployments** — the rungs c–e of the tile
    deployments scope. The state (`GET /deployments?tile=`) and the deploy
    log render in the web's words: the live reload sentence with Pause,
    Reload now, Resume ▸ and Attach ▸ (and Undo after a code move), the
    deployments with **Dev API** on what the last tab's session calls
    (D129) and `● live reload` on where saves go, a deployment's overview
    with a link to open `/c/<tile>+<name>/` and its **Branch** row (D131:
    Set branch…, Clear branch → `POST /deployments/branch`), and its deploy
    log with each entry's branch and Roll back. Every operation is a dry
    run first, confirmed with the server's `impact` sentences; a 409 because
    the record or the code moved re-reads and asks again; a branch mismatch
    asks "Use <branch> this time" and retries with `confirm:
    "other-branch"`. The offers to follow a branch switch come from the
    state alone (attach or resume onto `related`, "Resume live reload on
    <dev>", "Keep <dev> on <branch> this time"); "Add a deployment for
    <branch>…" points to the web. `deployments` events (op `work-tree`
    moves the count in place, the rest re-read) keep it live; they used to
    parse to `.other`. The launcher shows live reload's banner and what a
    new session calls. Add, promote, reassign, protect and the edges stay on
    the web ("Manage on the web" opens the web shell).
  - **B3, code and logs** (web/bx-code.js, bx-logs.js; XbinCore
    CodeTools.swift). Code: the tile's files as a folder tree (`GET
    /code/tree`), a file numbered and monospaced (`/code/file`), the
    history with a 30-day activity strip (`/git/log`, `/git/activity`) and
    a commit's or the uncommitted diff (`/git/diff`) — read only; a `+`
    in the tile's path is escaped (D127j). Logs: `GET /logs?follow=1`
    streamed over the agent feed's transport (the first streaming reader
    of plain text), whole lines only (a line or a character split across
    chunks waits), terminal escapes dropped, the last 3000 kept, following
    the bottom; a drop reconnects (the tail again), a refusal — logs need
    terminal access — says so and stops. A deployment's page in the
    Deployments tool has a **Logs** link (`&deployment=`).
  - **B4, PRs** (web/bx-prs.js; XbinCore Proposals.swift). The proposals
    to the tile, open or all, newest first; one opens with who filed it,
    its base and stats, the command that applies it in the tile's own
    terminal (applying is never a button, D48), the series as a diff, the
    review thread, and the target's acts the web offers: a comment, and
    Mark merged / Reject — each asking for the note for the author first,
    as the web's prompt does (closing is final). `pr` events are
    `AppEvent.pr` now (they parsed to `.other`), routed per tile; the tools
    button and the PRs chip count the open ones, as the web's ⇄ does.
  - **A deployment's page opens signed in.** The app's tile web view mints
    frame tokens by path, and `/frame-token?component=` refuses a
    `tile+name` (D127j), so `/c/<tile>+<name>/` (people with write, checked
    per request) opens as chrome tiles do (D125): the in-app web view with
    the user's cookie session — as does the web shell.
  - **Compatibility.** Everything is feature-detected: no deployments route
    (404/405) says so and shows nothing else (so does the PRs tool);
    without `branches/1` no offer,
    Branch row or new body field (bodies are decoded strictly); an older
    xbind's session rows have no `deployment` (the tag then reads the
    primary). Nothing changes on the wire.
  - **Not chosen:** the shell's window in a web view (the owner: native); a
    paged TabView for the tabs (horizontal drags belong to the terminal's
    selection and the panels' edges — the strip takes the swipe);
    unmounting hidden tabs (an agent's transcript would replay, a shell
    reattach, on every switch); add, promote and reassign in the app (the
    owner's scope for B2); a separate target picker in the app (the Dev API
    tag shows the session's; switching it stays a terminal restart on the
    web).
- **D133 — The agent template keeps its task through compaction: a
  pinned, read-only ask ledger; masking before summarising; the budget
  from the model's window (2026-09-29).** builtin-templates/agent/API.md
  §The task and compaction; `_backend/asks.go`, `compact.go`,
  `recall.go`. From an agent's retrospective on the harness: after a few
  compactions it had lost the original request and optimised a goal it had
  reconstructed. The code showed why: the request was only the first user
  message, compacted like any other; the budget defaulted to 12 000 tokens,
  so most coding turns compacted; each summary was a summary of the last
  one, without the tool calls; `recall` returned the newest 8 hits with
  every word required, so the oldest message — the request — was the first
  crowded out; and memory blocks rendered in random map order, which also
  broke the provider's cached prefix on every call.
  - **(a) A ledger, verbatim, never rewritten by a model.** `asks(run_id,
    msg_id, seq, source, who, text, at)` gets every request in the
    transaction that delivers it (POST /runs and /ask, messages, Learn
    skill, schedule and trigger firings, channel messages, a subagent's
    task and its parent's `subagent_message`s; a watcher's "check now" is
    not one). No tool writes it; `GET /runs/{id}/asks` reads it. The
    migration is additive (two tables, `messages.masked`,
    `runs.compact_note`) and pins each existing run's first user message;
    a run an older binary makes during a blue/green overlap is pinned when
    first read. Evidence: *Lost in the Middle* (Liu et al., TACL 2024) — a
    compacted first turn ends up mid-context, where it is used worst; goal
    drift follows accumulated off-goal context (Arike et al., AIES 2025);
    repeated LLM rewrites collapse context (ACE, Zhang et al., ICLR 2026);
    Letta's read-only blocks and Codex keeping user turns verbatim.
  - **(b) Pinned high, recited low, the prefix kept stable.** After the
    configured system prompt, `# Your task (verbatim — it outranks your
    notes and the summary)`: the first request, then later requests whose
    turns were compacted, newest last (capped; a cut item names the
    `message_get` that has the rest). Requests still in the conversation
    are not repeated there, so the system prompt changes only when a
    compaction runs (which breaks the cache anyway), never per message.
    The last message of every call gets a `<task-reminder>` — the title and
    the latest request, clipped — appended to its text and never stored:
    recency without touching the cached prefix (only the last message
    differs from the previous call), on both wires (a tool result's text, a
    user message's text or one more part; a trailing `user` after `tool` is
    refused by some providers, a trailing `system` is hoisted or refused by
    others). The call after a compaction is told so once. Evidence: Manus
    recites the task at the end of the context; *LLMs Get Lost in
    Multi-Turn Conversation* (Laban et al., ICLR 2026) — a consolidated
    restatement recovers most of the loss.
  - **(c) Notes, not the task.** Memory renders sorted under `# Your notes
    (you wrote these; the task above outranks them)`; `memory_delete`;
    `memory_set` says the task is pinned (don't restate it) and caps a note
    at 8000 characters. The UI's word for the blocks stays "Memory".
  - **(d) Mask first, summarise only if still over.** Stage 1 shows tool
    results older than the newest 5 steps (over 1200 bytes) as stubs that
    say how to restore them (`message_get {"seq": N}`, and `bash_output
    {job, offset: 0}` for a job) with their first and last words; the row
    and the search index keep every byte. Stage 2, only when stage 1 left
    the prompt over 75% of the budget: the summarizer gets the pinned task
    (to judge, never to restate), the prior summary and the turns with
    their tool calls; requests appear in it only by reference. Summaries are
    kept as a history (recall searches it); `runs.summary` is the latest.
    Evidence: *The Complexity Trap* (Lindenbauer et al., NeurIPS'25
    workshop) — observation masking alone matches or beats LLM
    summarisation at a fraction of the cost; SWE-agent's
    last-5-observations ablation; ACON (ICML 2026) — test the compressor
    against regression cases (the Go tests: the task survives three
    compactions verbatim; stubs restore byte for byte).
  - **(e) The budget from the window.** 60% of the model's context window
    as its provider lists it (`context_length`, `max_input_tokens`,
    `context_window`, `max_model_len`, … — llm-gw passes model objects
    through), at least 32 000 and never over 80% of the window; 32 000 when
    unknown (OpenAI's list says nothing). An explicit `tokenBudget` wins;
    `0` and `12000` — the old default every stored config carries — read as
    unset, so existing runs get the new budget. An owner who really wants
    12 000 sets 12 001.
  - **(f) recall and message_get.** bm25 ranking by default, `order:
    oldest|newest`, `match: any`, up to 20 hits; its own earlier results
    and `message_get`'s are never hits; the summary history is searched
    too; a long hit is an excerpt with its size. `message_get {seq,
    offset?}` returns any message of the run in full, 12 000 characters a
    page — compacted and masked ones included.
  - **(g) UI.** The web top bar pins the first request on a line under the
    controls, unfolding to the ledger; the native view has **Task** in the
    run menu, a screen of every request. Both read-only (feature
    `top.task`). A compaction step says what was summarised and how many
    outputs were hidden.
  - **Also fixed:** the call right after a compaction was assembled from
    the run as read before it — without the new summary (and the note).
  - **Not chosen:** an LLM "goal checker" before `finish` (measure the
    pinned task first); a task block holding every request (a new message
    would break the whole cached prefix); a trailing system message for the
    reminder; a per-model context table in the agent (a guess that goes
    stale — providers that say it are read, the rest get the floor).
  - **Open (the owner, 2026-09-29):** no budget ceiling for now — a
    1M-token window gets a 600k budget, each step resending it (masking
    and the provider's cache soften the cost); revisit with real runs.
    Follow-up: a per-model `contextWindow` override in llm-gw's config,
    injected into its `/v1/models` answer, for upstreams that list none
    (OpenAI's) — llm-gw has no config route for per-model settings yet
    (its pricing map has none either), so it is a route and a UI, not a
    field.
  - **Amendment (2026-09-29, the v0.3.64 regressions — the owner).** (g)
    changed: the web header pins the **current** request — the latest
    (`task.latest`, which the backend already sent), else the first — and
    `+N` still unfolds the ledger; the model's `# Your task` keeps the
    first (the prefix stays stable). The "✓ finished: …" line renders its
    result as markdown (web and native): models put whole answers there
    because `finish` said "the outcome / answer". `finish` is now worded
    by depth — a top-level run ends its turn with a one- or two-sentence
    status line, the answer in its reply before it (which renders); a
    subagent's result is the answer its parent receives (the contract it
    always had), and so is a channel conversation's or a trigger's
    top-level run's: its result is what is posted (channelTurnEnd), so its
    `finish` keeps "the full answer" (`runToolSpecs`). Pinned:
    TestFinishSpecByDepth, test/chat.mjs.
- **D134 — The agent's sandbox tools: jobs that don't kill themselves or
  lose their output, a tile sandbox with the rootfs toolchains, and tool
  descriptions that state their limits first (2026-09-29).**
  builtin-templates/agent/_backend/{sandbox_jobs,sandbox_wait,sandbox_tools,
  sandbox_fs,files,actor_tools}.go, model/tool-heads.js;
  internal/tilesbx/{launch,command}.go, internal/sandbox RootfsPATH;
  docker/rootfs.Dockerfile. From an agent's own retrospective after the
  owner had it process data in its coding sandbox and show a page (plan:
  "Agent template harness: fixes from an agent's retrospective", WP1): it
  found no browser, `pkill -f` killed its own job, killed jobs lost their
  output, and the tool descriptions misled it. The code showed each.
  - **The tile-sandbox PATH was ours.** Tile-sandbox commands got
    `/usr/local/sbin:…:/bin` — no `/usr/local/{go,node,bun}/bin`, unlike
    terminals, backends, their setup scripts and sandbox sessions — so an
    agent's `bash` found no node, npx or Playwright although the rootfs
    ships Node 22, Playwright and Chromium. One definition now,
    `sandbox.RootfsPATH`, for all of them, and `PLAYWRIGHT_BROWSERS_PATH=
    /usr/local/ms-playwright` where the rootfs has that directory (the
    Dockerfile's `ENV` doesn't survive `docker export`, so Playwright
    looked under `$HOME`). Additive: a command's own env and
    `defaults.env` still win. `TestLiveToolchain` runs node, go and
    `npx playwright --version` in a sandbox over the rootfs. The rootfs
    gains `xxd` (the binary-file hint already named it); its base version
    is the Dockerfile's hash, so `make rootfs` rebuilds it.
  - **No self-match.** Commands ran as `$SHELL -lc <cmd>`: the job's own
    shell had the pattern in its cmdline, and pkill excludes only itself.
    The command now reaches the shell through the environment — `cmd` is
    `eval "$AGENT_JOB_CMD"` — so its text is in no process's cmdline. The
    contract is unchanged (a consumer's `env` passes through every
    manager); the name is not `XBIN_*`, which tile sandboxes refuse in a
    command's env. The exec's label carries the command for people looking
    at the sandbox. Handles, never names (pgrep(1), cgroup v2
    `cgroup.kill`, pidfd, and the Claude Code field reports #16135, #2782,
    #29787): a poka-yoke refuses a command with `pkill -f`/`--full` or
    `killall` — "stop jobs with bash_kill {job}…", with the job list —
    and `force: true` runs it anyway. Per-job cgroups stay a later
    hardening in tilesbx: process groups already reach the tree.
  - **Output survives a stop.** `bash_kill` answers with the job's last
    output (≤ 4 KiB since it was last read) and how it ended; an
    interrupted `bash` keeps what it collected (a `partialResult` the
    engine settles the call with) and names the job; the signal is a new
    `sandbox_jobs.signal` column (an additive `ALTER`), so later reads say
    `killed by TERM`. No TTY means Python block-buffers and a TERM loses
    what is unflushed: `PYTHONUNBUFFERED=1` is in the job's env, and the
    `bash` description says to use `stdbuf -oL` for others.
  - **`jobs`** lists the conversation's jobs (the running ones asked of
    their sandboxes first), and **`yield` wakes when a job ends**: a run
    yielding with jobs of its own running sleeps as `{kind: sleep, since}`
    and wakes when one ends; `until_job` waits for one (an ended one
    answers at once). The wake is decided from the jobs table on every
    pass, so it is durable like the timer; the table learns of an end from
    whoever reads the job, or from the engine's per-run watcher — a
    long-poll per job at its manager while the run sleeps, no ticker
    (D81's rule).
  - **Waits say when they were cut.** A `timeout_s`/`wait_s` the model gave
    that the tool call's time limit (about 117 s) or the cap cut short is
    stated in the footer, never silent.
  - **Descriptions state their limits first** (ToolBeHonest, EMNLP 2024:
    "the tool exists but is limited" is the worst failure; Faghih et al.,
    EMNLP 2025: a description alone shifts tool choice 7–11×; "MCP Tool
    Descriptions Are Smelly!", 2026: 89.8% omit limitations). The first
    sentence of `bash`, `bash_output`, `bash_kill`, `jobs`, `yield`,
    `render_html` and the sandbox file tools gives the purpose, the scope
    and the key limit; `TestToolDescriptionFirstSentences` pins them, so a
    change is a reviewed change. `render_html` keeps its name (renaming
    breaks old transcripts and shifts tool choice unpredictably) and says
    first that it is a static snapshot, not a browser, pointing to
    `browser_check` and `preview_port` (other work packages add them). The
    `# Sandbox` prompt names the image's advertised tools (hello's
    `images[].tools`, kept in the binding) and the two places files live.
  - **Compatibility.** Instances pick this up by template update; the
    migration is additive, old transcripts and sleeping runs (no pending
    state: the timer alone) are unchanged, and a binding stored before has
    no `tools` (the prompt says nothing of them). The coding-sandbox
    manager's default image advertises `git, go, node, python3, rg, make,
    gcc` — not Playwright; a follow-up there can say so.
  - **Not chosen:** keeping `$SHELL -lc <cmd>` and only warning (the
    self-kill stays one typo away); `unset`ting the variable before the
    `eval` (portable only to POSIX shells, and the variable is bounded by
    the command-line limit anyway); a job-end inbox row to wake the run (a
    poke plus the table is the same durability without a row per end).

- **D135 — Live port previews: the sandbox-manager contract gains the
  optional `ports` capability, xbind proxies into a sandbox's loopback, a
  tile page mints path tickets, and the agent shows a sandbox's server in
  an opaque-origin frame (2026-09-29).** Extends D115's contract (protocol
  1, by addition) and revises D120 §8's "no port previews in v1".
  docs/sandbox-manager.md §Ports; docs/protocol.md §Tile sandboxes, §Path
  tickets; docs/auth.md §Path tickets; docs/sdk.md (`PortRoute`); the
  agent's API.md §Live previews. From an agent's retrospective: it had no
  way to show a script-driven page (the plan's section E).
  - **Chosen.**
    - **Contract.** `ANY /sbx/sandboxes/{id}/ports/{port}/{path…}`: an HTTP
      reverse proxy, WebSocket upgrades included, to `127.0.0.1:{port}`
      (else `::1`) inside the sandbox; scoped and person-checked as `exec`;
      `{path…}` and the query unchanged (a path-prefix proxy, nothing
      rewritten), `Host: localhost:{port}`; the consumer's credentials,
      identity and forwarding headers dropped in, `Set-Cookie`/`X-XBin-*`
      dropped out; never starts a sandbox (409 `state`); a new refusal
      `not-listening` (502). Optional, so `hello.caps` feature-detects it;
      the conformance suite gains a `ports` section (python3's
      `http.server` when the image has it) and its `unsupported` refusals.
    - **The bridge: a new agent-core connection kind, `port`.** xbind
      already reaches a sandbox's agent through the connection factory
      (namespace) or the resident VM router (vsock); a `port` Hello names
      the port, the agent dials the loopback, answers one `PortReply` line
      (ok / refused / error) and splices. Chosen over the egress relay
      (outbound-only by design: making it carry inbound flows would give a
      sandbox-facing component a path toward the host) and over
      `Exec.Listen` (per session and unix-socket only). The agent dials
      nothing but the loopback, and only xbind opens such a connection:
      nothing in the sandbox gains a route out. Each request is its own
      connection (no keep-alive across runs), and a request — a WebSocket
      for as long as it is open — holds off the idle stop. An agent from
      before it closes the connection: `unsupported` ("restart the
      sandbox").
    - **Path tickets (xbind).** The live page must run in an opaque-origin
      frame inside the tile's own sandboxed, credentialless frame, so its
      relative loads carry neither a frame token nor a cookie — and a
      frame token in its URL would hand hostile content the viewer's
      identity on the whole tile. A path ticket is a credential that
      reaches one prefix of the minting tile's API and nothing else:
      `POST /api/xbin/path-tickets {path}` (a tile's page only), then
      `/api/~<ticket>/<p>` → `/api/<tile>/<prefix>/<p>` as that frame
      principal. The content may read it; it gains only itself. HMAC with
      its own purpose tag; binds tile, person, login generation (dies with
      the login), deployment and view-as (stays read-only); 12 h; used only
      from an address that signed in within the hour (the `/c/`
      subresource rule), so a ticket sent elsewhere is useless; a decoded
      `.`/`..` is 400 (the proxy resolves dot segments), and the resolved
      component must be the ticket's tile; on a tile origin only that
      tile's tickets; redacted from request logs.
    - **Agent.** `preview_port {port, path?}` (its description's first
      line states what it is and its isolation) probes the page once and
      journals a `live` step. `/runs/{id}/live/{sbx}/{port}/{path…}`:
      participants only (viewers 403, strangers 404), a sandbox bound to
      that run, `sandboxUse`'s checks (cached 5 s), proxied as the binder.
      Its answers carry **`Content-Security-Policy: sandbox allow-scripts
      allow-forms`**, `Referrer-Policy: no-referrer`, `Cache-Control:
      no-store`, nosniff, and only an allowlist of the server's content
      headers (no `Set-Cookie`, `Clear-Site-Data`, NEL/`Report-To`, HSTS,
      `Alt-Svc`, CORS, `WWW-Authenticate`, `X-XBin-*`). The pane: `<iframe
      sandbox="allow-scripts allow-forms" credentialless
      referrerpolicy="no-referrer">`, never `allow-same-origin`, labelled
      live, with Reload. **Native shows no page**: the app's only WebView
      island (`canvas src=`) is a tile WebView with the tile's bridge and
      frame token, which an untrusted page must never get — its live screen
      says what is live and that it opens on the web (a dedicated untrusted
      island is future work).
    - **`frame-ancestors 'self'` is dropped** from the plan's CSP: the pane
      framing the page is itself an opaque origin (a sandboxed tile
      frame), which no source expression matches — Chromium blocked the
      frame (test/live-policy.mjs). The ticket, bound to the viewer's login
      and address, is what keeps other sites from loading it; a site that
      frames it anyway gets a sandboxed page it can't read.
  - **Accepted.** The page runs in the viewer's browser with the network
    that browser has (the owner's scenario loads a CDN import), so a
    hostile page can send what it shows elsewhere and can talk to its own
    server — a sandbox with egress `none` gains, while someone views it, a
    channel through that viewer's browser. Viewing is a participant's act;
    a stricter `connect-src` preview for `none` sandboxes is a later
    option. Root-absolute URLs in a page (`/app.js`) don't resolve under
    the prefix (the tool says so).
  - **Security review** (an adversarial reviewer on the finished diff; no
    exploitable hole found). Acted on: the native pane showed the page in
    a `canvas src=` island — dead today (iOS refuses `/api/` pages) and
    unsafe if admitted (the island carries the bridge and frame token) — so
    native shows no page; 1xx answers and trailers bypassed the header
    filters (the ReverseProxy copies a 103's headers before ModifyResponse)
    — both proxies drop 1xx but 101 and trailers, and a refusal keeps the
    live CSP; a ticket outlived a person's read access to the tile — it is
    re-checked on every use; xbind now stamps `CSP: sandbox allow-scripts
    allow-forms` and nosniff on everything a ticket reaches, whatever the
    backend sets. Accepted: a new live step opens the pane without a click,
    as a render does — the page runs sandboxed in each viewing
    participant's browser, on that browser's network (a prompt-injected
    agent could probe a viewer's LAN where Private Network Access doesn't
    apply); click-to-run is the option if that matters. `{port}` on the live
    route is any port of the bound sandbox, not only previewed ones (a
    participant steers the agent anyway). The fake manager dials the host
    loopback (test only).
  - **Not chosen:** public preview URLs (E2B/Daytona style: exposure beyond
    the run's people); rewriting HTML to prefix URLs (one sanctioned HTML
    transform, in xbind); the frame token in the page's URL (the page
    would act as the viewer on the whole tile); a cookie for the frame
    (an opaque, credentialless frame sends none); tile origins only
    (not every workspace runs them); an xbind-side browser (the plan's
    alternative, not chosen).
  - **Amendment (2026-09-29, the v0.3.64 regressions — the owner).**
    `preview_port` refused "this sandbox's manager doesn't serve ports"
    while `sandbox_info` listed `ports` in the sandbox's caps: the tools
    gated on the manager's hello, cached 5 minutes, and the sandbox's caps
    are fetched fresh — only the stale cache could disagree. Now every
    refusal a missing capability causes (`preview_port`, the live route,
    `sandbox_copy`'s tar) asks the manager again first
    (`managerHelloFresh`) and names the manager, its version and its caps;
    `sandbox_info` prints the manager's caps and whether a live preview
    works, and why not. xbind's `unsupported` for a sandbox agent from
    before ports reaches the model verbatim with "restart the sandbox"
    (there is no restart tool: the person restarts it). **Diagnostics the
    owner asked for:** the pane's status strip checks the page's own
    ticket URL (a plain fetch: the ticket rides in the URL, `Origin: null`
    gets xbind's CORS) as it loads and on Check, and says the refusal and
    what to do (not-listening, state, unsupported, the path ticket's two
    401s, the tile-origin 403) — a failing page is said, the frame hidden;
    the chat's 📡 line reopens the pane; participant routes `GET
    /runs/{id}/ports` (the tree's live previews, each probed now) and `GET
    /runs/{id}/ports/{sbx}/{port}` (one probe) — the live route's checks
    and the manager's ports route as the binder, never the page's body —
    back the ▣ popover's Ports section and the app's live screen's Check;
    the live route's refusals carry `refusal`. coding-sandbox: `GET
    /ports/{id}` and `GET /ports/{id}/{port}` for its pages' Ports rows
    (operators, or a person the port proxy would admit on the page, with
    write access; the page can't use the contract's route for another
    consumer's sandbox), and a sandbox whose runtime answered a port
    `unsupported` is `restartNeeded` until it runs again (the substrate's
    `started` after that answer). **xbind** (tilesbx) keeps each tile
    sandbox's latest 8 port requests (time, port, status or refusal, the
    calling manager) — in memory, by definition uid, gone with a delete —
    and, per run, whether its in-box agent serves ports (a reply or
    `not-listening`: serves; the connection closed without one: predates);
    `GET /api/xbin/sandboxes` (admin) carries both on each
    `tileSandboxes` row (additive) and Runtime → Sandboxes shows them.

- **D136 — The agent verifies pages in a real browser in its sandbox
  (browser_check), and sees its files in one view with two explicit places:
  session files carry a hash, a source and earlier versions; sandbox_download
  overwrites in place (2026-09-29).**
  builtin-templates/agent/_backend/{browser_check.go, browser_runner.mjs,
  files_meta.go, files_paths.go}, files.go, files_store.go, sandbox_move.go,
  attach.go (shownImages); model/tool-heads.js. From an agent's
  retrospective in the builtin template (plans: "next-because-of-the-cuddly-
  newt", WP3 — B2, C1–C3): asked to process data in its coding sandbox and
  show a page, it found "no browser" (render_html is a static srcdoc frame,
  and Node, Playwright and Chromium were in the rootfs but off the
  tile-sandbox PATH — D134's part), and the session files vs sandbox split
  confused it while `-2`, `-3` duplicates piled up (sandbox_download always
  went through the uploads' freePath, and nothing had a hash). The owner
  chose the recommended options: an in-sandbox browser tool, explicit
  places with content addresses.
  - **browser_check returns raw facts, measured in a real browser, with no
    model reading the page in between.** The final URL and title, console
    messages (level, text, location), page errors, failed requests (URL,
    error or status — a network error for an outside host with egress
    `none`, or a private address with egress `internet`, is named a sandbox
    egress block), a pruned accessibility snapshot, screenshots at the
    moments asked for, and a script's JSON return value. Evidence:
    WebArena and AgentOccam (an accessibility tree, pruned, is what agents
    act on best), ArtifactsBench (screenshots at several moments catch what
    one misses — animations, late renders), WatchPoint (2026: measured
    values, no LLM interpreting the page), Chrome DevTools MCP (console and
    network are the first things a developer reads). Its description
    states its scope in the first line (ToolBeHonest: "exists but limited"
    is the worst failure): it runs in the sandbox, its loads use the
    sandbox's egress, it needs Node and Playwright there.
  - **The runner lives in the template, runs in the sandbox.** An embedded
    ES module written to `~/.cache/xbin-browser/runner-<hash>.mjs` once per
    version, run with the contract's `run` (bounded time and output; the
    manager kills it with the call) through `sh -c` that finds
    `/usr/local/node/bin/node` by its absolute path — so it works on xbinds
    without D134's PATH fix — else `node` on the PATH. Playwright is found
    in the project first, then the global installs (the rootfs's `npm i -g`
    under /usr/local/node); browsers under `$PLAYWRIGHT_BROWSERS_PATH`, else
    /usr/local/ms-playwright. No new contract surface: an old manager runs
    it, and a sandbox without Node, Playwright or Chromium gets that said
    in so many words with the install command. It is a side effect (the
    approval gate) when the sandbox has egress, like bash. Not a numbered
    job: it is one bounded check, not a process to follow.
  - **Screenshots are session files the model is shown.** `shots/<page>-
    <ms>ms.png`, in place (the next check of the page replaces them, the
    earlier kept), with a `browser` source; the result lists them on one
    line that shownImages (attach.go) reads, as it reads file_view's —
    whose own result now names the key it shows, so a sandbox path works
    and an old transcript's result still reads.
  - **Files are content-addressed with provenance.** Each version records
    sha256, its source (a tool call, a sandbox path + etag, a person's
    upload, a screenshot) and the version it replaced; an overwrite keeps
    the replaced version (repl_file_versions: 10 per file, 1 MiB of text
    and 32 MiB of objects per run). Evidence: Venti (FAST 2002) and git's
    objects — name content by its hash, so "same content" and "changed"
    are exact; PASS (USENIX ATC 2006) — provenance recorded at write time
    is what makes a file's history answerable later; CORVUS (2026) —
    agents act on stale copies unless staleness is surfaced, hence
    file_list's "changed in the sandbox since" (one etag stat per copy,
    only while that sandbox is attached).
  - **sandbox_download overwrites in place.** The same file again writes
    nothing (the recorded etag answers without a read; else the hash);
    a changed one becomes the next version. `keep_both` keeps the old
    suffixes; freePath stays for people's uploads, whose names must never
    shadow another file the model referred to.
  - **Two places, named explicitly.** `session:<key>[@v]` or a sandbox
    path (`/…`, `./…`, `~/…`, `sandbox:…`) in file_read, file_view,
    render_html, browser_check, file_info and file_diff; a bare key still
    means a session file (old transcripts and habits), and `./x` without a
    sandbox is still the session file `x`. A miss names the other place
    when it has the file.
  - **Compatibility.** The migration is additive (four columns, one
    table). A row an older backend wrote — before, or during a blue/green
    overlap, when it bumps the version without the new columns — carries a
    `meta_ver` that isn't its version, so its hash and source read as
    unknown, never wrong. The tools' results keep their old first words
    (`downloaded … to the session file …`, `Showing …`, `rendered …`).
  - **Not chosen:** mounting the session store into the sandbox with FUSE
    (sqlite rows and blob objects; FUSE costs dominate small-file churn —
    Vangoor et al., FAST 2017); making the sandbox the store (runs without
    one, and sandboxes are deleted); an xbind-side browser service (a heavy
    dependency and a new host trust boundary — the owner chose the sandbox);
    renaming render_html (breaks transcripts; a description shift moves
    tool choice unpredictably — Faghih et al., EMNLP 2025); keeping every
    version forever (the per-run store stays bounded).
  - **Amendment (2026-09-29, the v0.3.64 regressions — the owner).**
    Reports stopped appearing: `render_html` journaled its step without
    streaming it (the client takes steps only from the stream since the
    poll went, D81), so the pane opened only on a re-read, if at all; it
    streams now, as `preview_port`'s live step does. Sandbox HTML over the
    64 KiB text cap became a binary session file that `render_html`
    refused: it shows `text/html` up to 2 MiB (`maxRenderBytes`), `GET
    /runs/{id}/file` answering a binary-stored text file's content for the
    pane (painted through the same static-snapshot CSP). A render or view
    of a sandbox path was stored under its base name, so two different
    `…/index.html` became versions of one another: the copy keeps the name
    an earlier copy of that very file holds (a repeated render is its next
    version), else the first free of base, parent/base and
    sandbox/parent/base (`copyName`); `sandbox_download` keeps "the file's
    own name" (its contract). The download note named the *replaced*
    version's source as if it were the new one: it now says `downloaded P
    from the sandbox "new" … replacing v2 (copied from the sandbox "old":
    …)`. `sandbox_copy` blamed the destination for a source that failed
    mid-stream (the source is the destination request's body): a counting
    reader keeps the source's error, and the failure says `reading
    "a":/p failed` or `writing "b":/q failed`; the source's own refusals
    name it. A sandbox deleted elsewhere stayed the active binding and every
    later call hit "is gone — ask the user": the first tool (or page) that
    finds it not-found detaches it in one transaction, promotes the first
    other attached one (the sandbox tools need an active one) and emits the
    run; the refusal says what is active now. The 🖼 line reopens a render
    (a subagent's from its own run's files).

- **D137 — Partitioned tiles, F1: the request, the recorded mode
  and "holds data" (2026-09-29).** Implements PD-02, PD-03, PD-05,
  PD-28, PD-44, PD-50, PD-51 (and the key of PD-52/PD-57) of
  plans/partitions/90-decisions.md; the design is
  plans/partitions/01-manifest-registry.md §1-§5.
  - **Chosen.**
    - **The request is code kind.** `partition`, `partitionMail` and
      `partitionNote` are deployment-level fields in `composeManifest`
      (TestManifestFieldSplit): the running code — a pinned primary's
      checkpoint — is what asks, so a rollback or promote to other code is a
      request like an edit.
    - **Fail closed on an unknown request.** `partition` parses any JSON
      value (the rest of the manifest still applies) and anything but
      `["user"]`/`["user","global"]` is invalid: no backend, nothing
      recorded — the one departure from compat rule 7, noted there. Same for
      `shared` values in a partitioned scope. `partitionMail`/`Note` are
      judged only beside a request, so a manifest without the key is never
      an error.
    - **The recorded mode is xbind state** at
      `data/partitions/<TileKey>/mode.json` (mode, request, declined,
      history), read once at boot and settled on every rescan through a
      registry hook (`Registry.PartitionModes`, the PinnedPrimary pattern):
      the component carries the settled state, so every reader of the
      registry sees one answer. No record is written while R and Q are both
      absent: a workspace that never uses partitions gets no
      `data/partitions` (TestNoPartitionGolden).
    - **"Holds data" from xbind's stores only** — kv keys (main's buckets of
      the scope the tile roots, every deployment namespace's), volume files
      beyond gocryptfs's own config, the plaintext resource dir, vault keys
      (main's and deployments'; an unreadable vault holds data),
      registrations (cron, bus, interface instances, ingress hosts, the
      deployment files). A store that can't tell counts as holding data.
      Later planes add their stores with `registerPartitionStore`.
    - **Pending and invalid hold the primary**: the proxy answers 409 with
      the pending body before the runner, ingress 503, and the runner's
      HoldReason/ShouldRunDeployment refuse the primary's spawn on every
      path (cron, bus, alwaysOn, a restart); a tile that becomes held is
      stopped. Non-primary deployments keep running (PD-17).
    - **Refusals at the edges:** approving `xbin`, `xbin:*`,
      `cap:sandboxes`, `cap:net-admin` or `cap:containers` for a tile whose
      recorded mode or request has user partitions is 409 (beside D127k's
      rule); `POST /deployments/primary` is 409 while the recorded mode has
      user partitions, pending and declined included.
    - **Decisions are recorded, not made, here**: `recordDecision` (keep /
      switch with the wiped summary, stale from/to refused) is the hook
      F13a's routes and wipe executor call.
    - **Unknown is never absent (review fixes).** Every "can't tell" fails
      toward holding, never toward a silent switch:
      - "holds data": only `fs.ErrNotExist` is "nothing there"; an EACCES or
        EIO from any store (plaintext dir, deployment levels, volumes,
        vaults, deployment records) holds data. An offloaded tile holds
        data (its data is in the offload archive; store `lifecycle`). A
        sealed vault's answer alone pauses the tile without recording a
        request, and the unseal settles again (`resettleAfterUnseal` in
        `UnsealOrInit`), so a boot before the unseal writes no spurious
        `request`.
      - A code manifest that can't be read — an `xbin.json` that doesn't
        parse (a live edit's typo), a pinned checkpoint that isn't or can't
        be prepared — is `PartitionAsk.Unread`, not "no request": a tile
        with a record is held Invalid in R with nothing written; one without
        a record stays in the zero state.
      - A `mode.json` this xbind can't read (a newer schema after a
        downgrade, a corrupt file, a tile that doesn't hash to its dir, an
        I/O error, or a file that appeared after the load) is kept apart by
        its directory: its tile is held Invalid with R unknown
        (`PartitionMode.Unknown`), and nothing is ever decided on or written
        over it (`write` and `recordDecision` refuse). Grants and `POST
        /deployments/primary` refuse as for a partitioned tile.
    - **`PartitionedScope` answers R** (the data layout) whenever R has user
      partitions, pending/invalid included; callers that must refuse during
      a pause check the root's `PartitionState().Held()`. A root with an
      unreadable record answers false with state Invalid — F4 must refuse
      on Invalid rather than fall back to the main namespace.
    - **Locks and scans.** `PartitionHoldReason` (every proxied call and
      spawn) reads `held` under its own RWMutex and never waits for a
      settle; the settle's I/O runs under `settleMu` only. `Rescan` is
      serialized (`partitionScan`): each scan settles and records, so an
      older scan can't record or publish after a newer one.
    - **Paused deliveries are quiet**: cron ticks to a held primary are
      missed and bus deliveries dropped (counted as dropped), as for a
      disabled tile, instead of 409 warnings.
    - **Readers see generic structural errors**: `partitionError` doesn't
      name a sibling tile or the grant a tile holds; the specifics are in
      `Component.PartitionErrDetail()` and the xbind log (logged once per
      tile and reason, which also covers a pre-existing custom `partition`
      key — docs/changes/2026-09-29-partition-key.md).
  - **Not chosen:** keeping the mode in the manifest alone (sandbox-writable,
    D118 — a typo or rollback would switch a tile holding data); a
    `Component.PartitionState` computed on every call from the broker (one
    settled answer per scan instead, no lock order between the registry and
    the store); judging "holds data" from content in the tile directory
    (sandbox-writable).
- **D138 — Partitioned tiles, SDK and client surface (skeleton).** The
  Go SDK and the in-frame client read what xbind will inject for
  partitioned tiles; every addition is empty or inert on an xbind without
  partitions (compat rule 8).
  - **`Partition()` / `PartitionUser()`** read `XBIN_PARTITION` as xbind set
    it (from its own state, never tile input); `PartitionUser` is the id of
    a `user:` key only.
  - **`RequirePartition()` fails closed on anything but a key xbind hands
    out**: `global`, or `user:` plus a non-empty id without whitespace or
    control characters. Absent, empty or unknown values (`org:…`, a
    malformed key) exit 3 with a stderr line naming `/docs/partitions.md`,
    so an older xbind, or a tile whose managers kept it unpartitioned,
    runs no backend instead of one shared one (PD-06). Stricter than
    "non-empty", so a later partition kind doesn't pass code written for
    `user`. It passes `global`, which is one instance for everyone who
    reaches it — including a non-primary deployment's writers whenever its
    code asks for partitions (01 §2.8, 05 §7), even over an unpartitioned
    primary — so the docs tell builders to serve per-person data only where
    `PartitionUser() != ""`. A stricter `RequireUserPartition` was not
    added: a tile with `global` must run there.
  - **`GlobalURL(path)` adds `?xbin-partition=global` only in a user
    partition**, and is the plain self URL elsewhere (the global instance,
    an unpartitioned tile, an older xbind). The parameter then never
    reaches a backend that would not consume it, mirroring the client's
    fetch option (plans/partitions/10 §A.4). It always builds
    `/api/<self>/…`.
  - **`xbin.fetch(url, {partition})`**: the option is always stripped
    before `fetch`. Its contract is the same in every document — any falsy
    value is the viewer's own partition; `'global'` is accepted only for
    the tile's own API (`/api/<self>` or below, resolved against the
    document, on its host or the workspace origin xbind names on a tile
    origin); anything else rejects with a `TypeError` — and only its effect
    depends on the document: in a user partition's `'global'` appends the
    parameter (a fragment stays last), elsewhere nothing changes (07 §2,
    10 §A.4). Rejecting misuse everywhere, not just inside partitions,
    makes a wrong URL or value fail while the tile is still unpartitioned
    instead of the day it is switched; restricting to the own API keeps the
    parameter from reaching another tile's backend (the proxy consumes it
    only for partitioned targets, 10 §A.6), and matches `GlobalURL`.
  - **A `Request` input is rebuilt explicitly** for the global URL, its
    body read from a clone (`arrayBuffer()`): `new Request(url, request)`
    takes the body from `Request.prototype.body`, which Firefox lacks, and
    would send a POST to global empty. The same change keeps a `Request`'s
    own headers when `opts.headers` is absent (they were replaced by the
    frame-token-only init before, for every `Request` input).
  - **`xbin.partition`** is spread into the frozen `window.xbin` only when
    the document carries `<meta name="xbin-partition">`, like
    `xbin.deployment`.
  - **`CallerInfo.Partition` / `PartitionID`** are plain strings, so the
    struct stays comparable (existing `==` checks in tile code keep
    compiling). `Partition` is set on calls from a partitioned tile's
    principals and on calls into a partitioned tile that act for a
    partition (a person, the root token at global, a user partition's F5
    call with `From == Self()`), per 02 §6. `PartitionID` is empty for
    global and unpartitioned callers, so keying on it makes `""` and
    `global` one consumer by construction.
  - **Not in this change:** `Mail`/`Inbox`/`Ack` (the mail pack), personal
    rows in `xbin.iface` (none needed in the client: the meta's rows pass
    through), protocol.md rows (the packs that make xbind send the
    headers, env, meta and parameter — see the records' owners list).
- **D139 — Workspace policies: an xbind-owned file read fail-closed, an
  admin-only PUT and the admin console's workspace → policies tab
  (2026-09-29).** Implements PD-55 (plans/partitions/90-decisions.md; 05 §2,
  06 §9, 06 §12.3). docs/protocol.md `/workspace-policies`, the `policies`
  event and the `policies` alert; docs/bx.md `bx policies`; the admin
  tile's API.md.
  - **Chosen.**
    - **Storage.** `data/workspace-policies.json`, `{"schema": 1,
      "partitionConsent": false, "credentialResetConfirm": false}`, written
      atomically (fsutil). Its own file because an older xbind rewriting
      `data/users.json` drops keys it doesn't know (the `data/branding.json`
      precedent, D76). A rewrite here keeps every key this xbind doesn't
      know and a newer schema number, so a newer xbind's switches survive a
      downgrade the same way.
    - **Read by exact key, fail closed.** Each switch is read by its exact
      key from the raw object (encoding/json's case-insensitive match would
      let a hand-edited `partitionconsent` shadow the real key after a PUT).
      Both switches are protections, so a problem never turns one off: a
      switch whose value can't be read — the file unreadable or not a JSON
      object, a value that isn't `true`/`false` (a newer schema that changed
      its type included), a mis-cased key — keeps the last value this xbind
      read, or is **on** when it has read none (a cold start). A switch the
      file does state cleanly reads as stated, so one bad key doesn't reset
      the other. Logged once per version of the file; admins get a `crit`
      alert of kind `policies` on `/alerts`; GET and PUT answer 500 (the
      reason names `data/workspace-policies.json`, never the host path, and
      only admins get it: a person gets a generic line); PUT never
      overwrites such a file — it is fixed by hand.
    - **Reads in xbind.** `Broker.Policies()` — cached, re-read only when
      the file's size or mtime changes (a restore or hand edit is picked up
      without a restart), so the consent check (F10) and the credential
      path (F7b/F11) can call it per request.
    - **API.** `GET /workspace-policies`: admins (the admin tile through
      `xbin:admin` included) and a person through their own session or
      device, or a terminal or agent session they drive (both carry a
      terminal token: Via `terminal`, the person in UserID); frames,
      instances and cron/bus deliveries of other tiles get 403. `PUT`:
      admin only (`Broker.IsAdmin`); strict body `{partitionConsent?,
      credentialResetConfirm?}`, at least one key; publishes `policies`
      with no data (every non-bus event reaches every socket, so the values
      stay behind GET's gate); audited twice: the generic governance line
      (who, status) and its own `audit` line with each switch's `old→new`
      (and the viewing person for a frame). Deployment route class: `GET`
      **neutral** (a read of a workspace fact, like `GET /branding` and
      `GET /native-runtime`: a person's terminal on a non-primary deployment
      runs `bx policies` too), `PUT` **primary-only** (governance, like
      `PUT /native-runtime`). The partition route class (02 §8: both
      Neutral) is F2's table, not yet present.
    - **UI.** `tabs/policies.js`, the `GROUPS` workspace entry and its
      `PLAIN_TABS` line; the nativeapp tab's pattern (GET, one-key PUT,
      re-read on `policies`). Each switch says "applies to partitioned
      tiles". Turning `partitionConsent` on asks first, inline (Turn on /
      cancel). The D20 grant ceiling stays in the organisations tab
      (linked). The tabs that take no inputs (sandboxes, deployments,
      branding, nativeapp, policies) moved into `plain-tabs.js` (their
      imports and an id → template map, one `render()` arm), which left
      `admin.js` at 313 of its 318-line budget for the packs that add tabs
      next (F12, F17a).
    - **CLI.** `bx policies [ls] [--json]`, `bx policies set
      partition-consent|credential-reset-confirm on|off`.
  - **Deviations from the spec, and how they were resolved.**
    - 05 §2 said GET is for "any signed-in person; tile principals get
      403". Terminal tokens are element principals, yet a person's terminal
      and agent sessions are allowed: they carry the driving person, and
      `bx policies` runs in them. 05 §2 now says so.
    - 02 §8 lists `PUT /workspace-policies` among the Neutral governance
      acts whose handlers "refuse instance and frame principals of every
      tile", while 06 §12.3 needs the admin tile's frame to PUT. The PUT
      judges `Broker.IsAdmin` like every governance write: any principal
      holding `xbin:admin` passes (the xbin:admin model; F1 refuses `xbin:*`
      to partitioned tiles, so no partitioned tile's code reaches it). 02 §8
      now says so in a note under its table.
  - **Not yet (later packs).** Nothing consumes the switches: F10 reads
    `partitionConsent` for the consent machinery and the approval warning
    text; F7b/F11 read `credentialResetConfirm`. The confirmation before
    turning consent on shows no ledger totals yet (06 §6.1's per-person
    egress ledger doesn't exist); F10 adds them to the `[data-policy-confirm]`
    block.
  - **Not chosen:** a users.json key (dropped by an older xbind's rewrite);
    reading an unreadable file as every switch off (fails open: a typo
    would silently drop consent enforcement); PrimaryOnly for GET (it
    refused a person's `bx policies` in a terminal on a non-primary
    deployment, and the read is a workspace fact like `GET /branding`);
    tightening PUT to "the viewing person must be an admin" for frames
    (the admin console's other tabs don't, and `xbin:admin` already covers
    strictly more); putting the values in the `policies` event (tile sockets
    receive every non-bus event).
- **D140 — Sandbox managers key a partitioned consumer's user partitions
  (the partitioned-tiles plan's PD-39; 2026-09-29).** Extends D115's
  contract (protocol 1, by addition: a capability, two optional fields) and
  D122's builtin manager. docs/sandbox-manager.md §Partitioned consumers;
  coding-sandbox API.md; plans/partitions/09 §1, 07 §4.
  - **Chosen.**
    - **Consumer = (From, partition id).** The id is
      `X-XBin-Partition-Id` (the opaque pkey of PD-43, so a recreated
      person never inherits what their old partition holds — C11) and `""`
      for the consumer's non-personal identity, `global` included (S14:
      every sandbox made before the consumer turned partitions on stays its
      global instance's). Not keyed on `X-XBin-Deployment`: user
      partitions are primary-only (PD-17), so the id never spans
      deployments, and keying today's non-primary deployments apart would
      strand the sandboxes they made. PD-14, 02 §6, 05 §4 and 07's SDK
      comment now say "plus Deployment where the provider already keys on
      it" (**owner to confirm**: PD-14 is a DEFAULT, and this relaxes its
      wording to what B1 does).
    - **The person from the partition.** A user partition's call is
      verified as its person; `Sbx-User`/`X-XBin-User` naming anyone else
      is 403, and so are partition headers that don't agree (a user
      partition without its id, an unknown kind). Fail closed, in depth:
      `contractHandler` and the operators' routes refuse such a call, and
      `callerOf` itself makes it a refused caller that is home to nothing,
      sees nothing and creates nothing — never read as global.
    - **Global-home visibility (C7).** A user partition sees what its
      person may use at the consumer's non-personal identity — a sandbox
      homed there, or shared with it, where `personOK` passes (team, owner,
      member, a share's `users`) — so the agent page's direct terminal
      dial keeps working for shared conversations. Such a sandbox reads
      `shared: true`; the partition can't change who uses it or delete it.
      Anything else of the consumer's stays `not-found`. Never the
      converse. (The plan spoke of global-home records; shares with the
      consumer's non-personal identity follow the same "what the person
      could see at global" rule.) Global-home records follow the person
      rules by user id, so a recreated person's new partition sees what
      the old one owned or was a member of there (accepted risk below).
    - **Shares keep the person rules.** A share makes a sandbox visible to
      a partition; a `private` one whose owner and members don't include
      the partition's person is `403 not-allowed` and unlisted there (as
      for any verified person) — the fix is to add them as a member. Not
      404: the home consumer chose to share it with that partition.
    - **`hello.caps` gains `partitions`** — the plan's `caps.partitions: 1`;
      `caps` is a list of words in protocol 1, so the capability is the
      word (the contract grows by addition: new capabilities). It is the
      manager's, never a sandbox's; consumers (the agent, B2a) and xbind's
      switch confirmation / `bx doctor` (F13a/F11) test
      `caps.includes("partitions")`. Every plan file that said
      `caps.partitions` is amended to say so (01, 05, 06, 07, 08, 09, 10,
      PD-14, PD-39).
    - **Operators (S19).** Metadata as before; a user partition's sandbox's
      name and labels are shown as `<consumer>/<partition id, 8> #<n>` and
      none, and its snapshots (`GET /ops/sandboxes/{id}/snapshots`) as
      `snapshot #<n>` oldest first, unless shared with the viewing operator
      (they could use it as a consumer).
    - **Runtime labels.** The runtime keeps `for` = the tile and gets the
      partition id as the label `coding-sandbox/partition` (the plan's
      "ForPartition label"; no SDK/tilesbx field was added). The admin's
      sandbox registry (`tilesbx.AdminRow`, `sbx.Entry`) carries no
      labels, so it doesn't show the partition yet: an xbind follow-up
      (below).
    - **Quotas** per consumer sum every partition of the tile; per person
      as ever. `clientId`s (create, exec, snapshot) are per (consumer,
      partition); global's stored keys are byte-identical to before.
    - **Conformance suite.** A `user-partitions` section gated on the
      capability: partitions apart, `""` ≡ `global`, global-home records by
      the person rules, shares to (consumer, partitionId) — including a
      private sandbox shared with a partition (403 until its person is a
      member) — a mismatched person refused, a recreated user (new id) sees
      nothing of the old partition's and, by user id, what global holds.
      The consumer-level `partitions` section is unchanged. The live
      isolated run (test/isolated) skips it until a partitioned consumer can
      be driven through xbind (xbind strips the headers the suite sets; I1).
    - **hack/fakesandbox** implements the same; with `Caps` lacking
      `partitions` it ignores the headers — the old-manager fixture for the
      agent's degrade path (B2a). Its mirrors follow; sandbox-terminal v3.
    - **Docs.** The contract's "Partitions, sharing and people" is retitled
      "Consumers, sharing and people" ("partition" now means a user
      partition); the suite's section names stay (builders'
      `Target.Skip` keys), with a note that `partitions` is about consumers
      and `user-partitions` about a partitioned consumer's people.
  - **Not chosen:** a top-level `hello.partitions: 1` (a second way to say
    a capability); keying on `X-XBin-Deployment` too; answering 404 to a
    partition whose person a shared private sandbox doesn't admit;
    per-partition ownership of execs and terminals inside one sandbox
    (isolation stops at the sandbox — documented; consumers keep private
    work in partition-homed sandboxes); a `rehome` route (PD-34).
- **D141 — Sealed backups: xbind encrypts every archive under per-subject
  backup keys the vault's data key wraps; erasing a key crypto-erases that
  data in every archive (2026-09-29).** Implements PD-25 (owner ruling) and
  PD-56; supersedes VD-4 (plans/vault-data.md: "backups are plaintext; the
  archiver owns archive encryption") and LC-3's "plaintext tar in" (the
  archiver contract now receives sealed archives; it stores opaque bytes, so
  older archivers keep working). plans/partitions/11-backup-encryption.md;
  docs/overview/14-lifecycle.md §Sealed archives; docs/protocol.md §Backup;
  migration note docs/changes/2026-09-29-sealed-backups.md.
  - **Chosen.**
    - **Keys.** Random 256-bit backup keys, one current per subject —
      `tile:<TileKey>` (the main archive: source, terminal layer, records),
      `ns:<main namespace's data key>` (the new data archive),
      `ns:.deployments/<escS>/<dep>` (a deployment archive); `part:` is
      F17b's — each wrapped by `Barrier.EncryptFor("backup-subkey:"+id)` in
      `data/vault/.backup-keys/<id>.json` (`{schema, id, subject, tile, gen,
      created, imported?, wrapped}`; `tile` added so an erase can name a
      tile's subjects). Not HKDF labels of the DEK: a derived key lives as
      long as the DEK and can't be erased. The directory is broker-only
      (data/vault/, no sandbox mounts it; the vault's listing skips
      directories); a key is unwrapped for one backup or restore.
    - **Format** (`internal/backup/seal.go`): `XBINSEAL` ‖ u32 header
      length (≤ 4 KiB) ‖ cleartext JSON header `{v:1, subkey, salt(32),
      chunk:65536, kind, created}` ‖ AES-256-GCM STREAM, key =
      HKDF-SHA256(subkey, salt, "xbin/archive/v1"), chunk i's nonce = 11-byte
      big-endian i ‖ last flag, chunk 0's AAD = the header. Standard library
      + the x/crypto HKDF the barrier already uses; no new dependency.
      `backup.Open` sniffs the magic, so a plaintext tar reads exactly as
      `NewReader` reads it (a golden from the previous writer pins it).
      Every restore authenticates a sealed archive whole before writing
      anything (a second decrypt pass over the in-memory body: CPU, not
      memory).
    - **One key per object: the data split.** A sealed workspace's main
      archive (schema 3) no longer holds the scope's data; `.data.<TileKey>`
      (schema 3, `kind: "data"`) does, written just before it, named by the
      main manifest's `data {key, version, subkey}`. The `subkey` lets a
      restore tell a data archive whose key was erased and whose versions
      the archiver then deleted from a missing one. Plaintext-vault
      workspaces keep schema 1, data inline.
    - **Erase** deletes the key file (fsyncing the directory), appends a
      tombstone to `erased.json` (`{id, subject, tile, gen, erasedAt,
      reason, by}`), then asks the tile's archiver `POST /archive/erase`
      (404/405 = unsupported; the dead versions stay, unreadable). The
      subject gets gen+1 at its next backup. `EraseBackupKeys(tile, all,
      reason, by)` is the hook F13a/F17b call. An admin's `bx backup erase
      <tile> --data` erases every `ns:` (and later `part:`) key of the tile,
      `--all` its `tile:` key too.
    - **Restore rules.** An erased key: `this backup's data was erased on
      <date> (<reason>)`; a main archive whose data key alone was erased
      restores source and terminal layer and answers `dataErased`. An
      unknown key: `this backup was sealed by another workspace: import its
      keys (bx backup keys import)`. A data archive is never restored as a
      tile. Single-file restores are extracted by xbind for every archive.
    - **DR.** `POST /backup-keys/export` answers `{schema, workspace,
      created, barrier (.barrier.json), keys (wrapped under the DEK),
      erased}` and records the export (`exports.json`); `POST
      /backup-keys/import {bundle, passphrase}` unwraps the other DEK in
      memory (`vault.FromKeyfile`, never persisted), re-wraps each key under
      this DEK marked `imported` (they open that workspace's archives and
      never seal new ones), and takes the tombstones — erasing only
      *imported* keys they name (a bundle's tombstones aren't sealed, so one
      never erases a key of the importing workspace's own).
    - **Nudges (owner ruling H3: seal at once).** Existing workspaces seal
      from their next backup; the admin-only `/alerts` kind `backup-keys`
      ("N backup keys aren't in any export yet") stays while any key is in
      no export; `bx doctor` and the Backup tab say the same, plus erasures
      since the last export.
    - **Not chosen (review):** deleting the data archive when the main PUT
      fails (an archiver that stored the main archive anyway would leave it
      naming a deleted version); pruning data archives by count (one stray
      version shifts it onto a kept backup's data); requiring a client
      acknowledgement before an export counts (documented instead: the
      export counts when answered; bx says so when it can't write the
      bundle).
    - **The subkey header** `X-XBin-Backup-Subkey` is set by the proxy from
      the request context (`auth.WithBackupSubkey`) after it strips every
      inbound `X-XBin-*`, so only xbind's own archive PUTs carry it.
    - **A sealed vault stops every backup**, main archives included, and
      so does a vault not set up yet (no barrier and not the
      plaintext-vault mode: `vault-locked`); only `--insecure-vault` /
      `--no-auth` write plain archives (11 §1's one exception).
    - **One backup, one pair.** A main archive and its data archive share
      a random `backupId`, and the data archive must be sealed under the
      key the main archive's pointer names; a restore never pairs them
      otherwise. No data archive for a scope with no resources.
    - **Retention by reference.** A data archive is deleted when no kept
      main archive names it (a per-tile cache,
      `data/backup-refs/<CompKey>.json`, says which data version each main
      version names; an unknown kept main archive is read before any data
      version goes), never by a count of its own; a failed main PUT no
      longer deletes its data archive (the archiver may have stored the
      main archive anyway). A main archive whose data archive is missing,
      its key not erased, restores source and terminal layer
      (`dataMissing`). The data PUT must answer a usable version, or the
      backup fails before the main archive is written.
    - **A tile's backup lock** serializes its backups, prunes and key
      erasures; the wiping hooks (F13a, F17b) hold it across the wipe and
      the erase: stop, `holdBackups`, remove the data,
      `eraseBackupSubjectsHeld`, release. `EraseBackupSubjects(tile, match,
      reason, by)` erases by subject (one `part:` key); `EraseBackupKeys`
      wraps it.
    - **Erase commits at the tombstone**: tombstones first, then the key
      files; a key a tombstone names is refused and its file removed
      wherever met. Key files are durable before use (the directory fsync
      is checked).
    - **The bundle's reach.** With the passphrase in force at export a
      bundle opens the DEK (every secret and all data at rest), which
      never rotates (VD-5): a rekey marks exports stale (`exports.json
      rekeyed`, the alert, `passphraseChanged`), and export, import and
      erase are a person's acts (`requireAdminPerson`: own session, or the
      admin tile's frame via `AdminFrameDriver`).
  - **Not chosen:** HKDF-derived subkeys (can't be erased); sealing with the
    DEK directly (one key: no erase short of rotating everything);
    encrypting in the archiver (VD-4: every archiver must get it right, and
    erasure needs a key xbind controls); buffering a decrypted copy to
    authenticate before a restore (2× memory; a second pass costs CPU only);
    the opt-in-for-one-release rollout (§H.3 alternative; the owner chose
    sealing at once with the alert).

- **D142 — Partitioned tiles, F2: identity and routing
  (2026-09-30).** Implements PD-08, PD-10, PD-11, PD-12, PD-29, and the key
  of PD-01, the liveness gate of PD-20 and the uid of PD-43, of
  plans/partitions/90-decisions.md; the design is
  plans/partitions/02-identity-routing.md.
  - **Chosen.**
    - **The key and the id.** `util.Partition` (`""` | `global` |
      `user:<id>`; `ParsePartition` strict: `user:` plus 1–128 printable
      ASCII bytes without spaces or upper case) and `util.PartitionKey(id,
      uid)` = `u-` + 32 hex of SHA-256(`xbin-partition-v1` ‖ 0 ‖ id ‖ 0 ‖
      uid) — the storage key and `X-XBin-Partition-Id`.
    - **The uid is minted lazily**, at a person's first partition
      (`users.Store.EnsureUID`, persisted before it is answered), never at
      account creation: 10 §A.1's zero-state rule ("no uid is minted for
      anyone until a partition is first used") wins over 03 §E's "minted at
      creation", which only 95's "or lazily" also allowed. Store-owned
      (Upsert and UpsertInvited keep a new account's empty, an update keeps
      the record's), never on the wire (`Public()` drops it), gone with the
      record. An older xbind's rewrite drops it; the broker's one minting
      path (`mintPartitionUID`) adopts the uid the partition records carry
      (F4's `adoptablePartitionUID`) instead of minting.
    - **Principal.Partition comes from xbind state only**: an instance
      token's registration (`RegisterInstancePartition(token, tile, dep,
      part, uid)`; global registers as today's instance, partition ""), a
      delivery's registration, or — stamped on the principal by the
      server's partition gate — the broker's answer for the tile's own
      credentials. Frame, terminal and path-ticket wire formats don't
      change.
    - **Coverage per lookup, plus eager revocation.** A user partition's
      token is looked up through `Auth.SetPartitionCoverage`
      (`Broker.PartitionCovered`: the tile `Partitioned()` with user, the
      person present with the same uid, enabled, reading the tile), so it is
      a 401 the moment it isn't covered — no window where it resolves to the
      tile's main instance. `RevokePartitionInstances(tile)` and
      `RevokeUserPartitionInstances(user)` drop tokens synchronously for the
      mode and people hooks.
    - **addressedPartition is the one function** (broker/partitionroute.go),
      02 §3's table, shared by Route, the class gate, the event filters and
      the document meta through `server.PartitionPolicy` (and F4's data
      plane through its seam). It follows the **recorded** mode R: a tile
      paused by a pending switch or an invalid request keeps its partitions
      (the proxy's 409 holds its backend), and one whose record can't be
      read is an error on the server planes (fail closed) while Route
      leaves it to F1's 409. A credential naming a user partition on a tile
      that is no longer partitioned is refused, never read as main. Without
      a person, only the tile's own credentials and the owner (`--no-auth`
      included) reach global — never an anonymous principal. Liveness is
      read from the users store, never a principal's snapshot.
    - **Route = the deployment rules, then routePartition.** Decision (and
      proxy.Decision) gain `Partition`, `CallerPartition`,
      `CallerPartitionID`, `Attribute` and `Delivery` (`cron`, `bus`,
      `mail` or ""; 02 §4's `Background bool`, carrying which delivery so a
      mail doorbell starts as mail). A call between two unpartitioned ends
      returns the deployment rules' decision untouched (TestZeroStateRoute
      unchanged). `CallerPartition` is the caller's own partition for a
      partitioned tile's credentials (on calls to unpartitioned tiles too),
      the reached partition for people, the owner and deliveries on a
      partitioned tile, and nothing for an unpartitioned tile calling in
      (02 §6: "absent for everything else"). A non-primary deployment of a
      partitioned tile is `global` (PD-17).
    - **The grant check stays Route's; the read and consent checks are
      addressedPartition's**, so the event filters apply them too.
      `partitionConsent` is read from F16's `Broker.Policies()` per
      cross-tile partition call. A tile of the event's scope (the root or a
      sibling) matches a partition's bus event by its own partition, as the
      data plane reaches its own scope.
    - **`auth.Attribution`** is the F5 attribution type both Decisions
      share, so boot's converter stays a plain type conversion.
    - **The proxy** sets the two headers after identify's strip
      (`identifyPartition`; identify's signature is unchanged), applies an
      `Attribute` as X-XBin-User / -User-Level / -Role, starts a user
      partition through `proxy.PartitionRunner` (`EnsurePartition(ctx, c,
      dep, part string, class PartitionStart)`, interactive, background or
      mail; a `text/event-stream` answer swaps the active hold for a passive
      one without a gap) and answers 503 without a runner or on a refusal
      (`sbx.ErrRefused`). Boot builds the runner through an explicit
      adapter (`partitionStarts`), never a type assertion; without one it
      logs the partitioned tiles it can't serve. Global and every
      unpartitioned tile take today's EnsureDeployment path. Ingress serves
      a partitioned tile from its global instance only, 503 without one.
    - **The partition class table** (server/partitionclass.go) has a row for
      each mounted route; `TestPartitionRouteClasses` fails on a mounted
      route without a row, a deployment-scoped route without one, a row
      naming no route (except `partitionPlanned`: rows other packs mount),
      and a route web/xbin-client.js or sdk/ calls without a row.
      PartitionScoped routes whose handler another pack converts are listed
      in `partitionUnconverted` and refused (403) to user partitions until
      then (every `POST /term/sessions*` among them); live now: `GET
      /frame-token`, `POST /path-tickets`, `GET/POST /tile-report`, and the
      prefs routes for credentials that name a person
      (`partitionPersonKeyed`: an instance token, which names none, would
      share the tile's person-less bucket with global and the owner's
      frames). GlobalOnlyDormant routes are refused until F5 stores dormant
      registrations. PersonOnly takes a person's own session, app or device
      credential only: never a tile's, the owner token, view-as or an
      anonymous principal. `PUT /workspace-policies`, `/backup-keys/*`,
      `POST /partitions/limits` and `POST /partitions/mode` are Neutral (02
      §8's governance acts; no partitioned tile holds `xbin:*`).
    - **Events.** `events.Event.Partition` (omitted when empty: today's
      bytes). A non-bus event with it reaches people whose partition on the
      tile it is and the tile's own credentials acting in it — never another
      tile's frame, view-as or an admin's blanket pass — and then the
      type's own rules still apply (a stamp narrows, never widens); a
      partition's bus event has no admin pass; a partitioned tile's
      `term`/`session` events reach only the session's person (and their
      terminal on the tile), judged in the filter so F7a's publish sites
      stay untouched. A user partition's `POST /tile-report` is stored per
      partition, keyed by its id (a recreated person reads nothing of the
      old one's), published with `Partition`, and cleared by that
      partition's own restart or the tile's rebuild; `GET /tile-report`
      answers it its own.
    - **The document meta** `<meta name="xbin-partition">` inside the D4
      injection (no new transform): the viewer's partition in the
      primary's documents, `global` in a non-primary deployment's (its
      frame token is bound to that deployment's shared instance); a view-as
      viewer of a partitioned tile gets no frame token.
  - **Not chosen:** minting the uid at account creation (a workspace without
    partitions would gain a users.json key); a per-request coverage check
    only in the broker (a token must fail at authentication, before any
    handler); stamping `global` on principals (global is today's instance
    at today's keys: handlers keep their answers); refusing a partition's
    events to its person's own shell (the shell shows the partition's
    status); wiring the runner by a type assertion (a signature drift would
    silently leave every person's partition at 503); keying an instance
    token's prefs by its partition now (refused instead, until a pack
    converts prefs).
- **D143 — Partitioned tiles, F3: people's partitions in the runner
  (2026-09-30).** Implements PD-04 (the global instance at today's runner
  keys), PD-17 (user partitions on the primary only), PD-18 (caps from
  memory, admin-settable; LRU eviction of what isn't in use; passive
  streams don't count; background starts never evict, ≤ 4 at once, mail
  ≤ 6/min per tile; the 10-minute idle stop, global exempt), PD-19 (no
  user partition without `--isolate`), PD-20's runner half (a state is
  bound to its person's uid and pkey), PD-28's network half (user
  partitions get the non-primary rule), PD-35's coverage items (hidden-tab
  streams passive, idle stop, no boot starts, one global per instance) and
  PD-46's metadata rows, of plans/partitions/90-decisions.md; the design is
  plans/partitions/03-runner-data-vault.md §A.
  - **Chosen.**
    - **A map of its own.** A person's state is keyed
      `partStateKey(tile, dep, pkey)` = stateKey + "\x00p\x00" + pkey, in
      `Runner.parts.states` apart from today's states, so nothing that walks
      today's (Status, stats, alwaysOn, D127q admission, VMs, the ingress
      dial) meets one. Stops, the reaper, Stop/StopAll, envKeep and the
      inspect rows learn it explicitly.
    - **A copy of the view names the partition.** A generation spawns from
      a copy of the primary's view with `Component.Partition`/`PartitionID`
      set (views are shared; the registry never sets them). Every spawn-time
      decision reads it: run dir `p-<16 hex>`, log
      `.xbin/partition/<TileKey>/<dep>/<pkey>/backend.log`, cgroup leaf
      `tile-<key>/p-<hash>/backend` (the tile's caps per person), token
      through `RegisterPartitionInstance`, env through `PartitionEnv`, data
      binds from its remap (never a path at itself on main), and
      `UserPartition()` for the network rule in launchSpecWith, netPlanFor,
      spawnEgress, ingressFwd and hostDialFor.
    - **XBIN_PARTITION** is set by the runner: `user:<id>` for a person's,
      `global` for the primary of a tile running user partitions and for a
      non-primary deployment whose own code asks; right after XBIN_COMPONENT
      (after XBIN_DEPLOYMENT when that is set); never on an unpartitioned
      tile (the env golden).
    - **A state is its person's incarnation.** `PartitionIdent` answers the
      pkey and the uid; the state keeps both, `ShouldRunPartition` is asked
      with that uid (PD-20: "the same uid") and the token is registered
      with it (`RegisterPartitionInstance(token, tile, dep, part, uid)`,
      02 §2). A state whose pkey is no longer PartitionIdent's answer (the
      person was deleted and made again) stops — on the next request for
      that partition and on every change — and never restarts, so an old
      incarnation's process never authenticates as the new person.
    - **The spawn window.** A build can take minutes and a stop may land
      meanwhile. Right before it spawns, a person's generation passes its
      gates again (partBegin: not stopped, the tile still runs user
      partitions and may run, the person is the state's and may run it);
      then every step is visible to stopPart under the state's lock — the
      token registered mid-spawn (registerGen registers only while the
      state isn't stopped, and keeps the token where a stop finds it), the
      generation spawned but not yet healthy, the install. So every stop
      revokes every token a partition could authenticate with before it
      returns, nothing spawns after a stop, and a waiting stop
      (StopPartition, StopPartitions, StopPartitionsOf) returns only once
      the spawn in flight has stopped what it spawned — never before a
      wipe could race a live process.
    - **Fail-closed hooks.** Identity (pkey/uid), liveness, env/data, token
      binding and event routing are PartitionHooks; while any start hook is
      nil no person's partition starts (ErrPartitionRefused), and a nil
      event hook drops a partition's runner events (a setup run's included)
      instead of publishing them tile-wide.
    - **The runner's running mode comes from the registry hook.**
      `Registry.OnPartitionChange` (additive: planes add hooks after the
      runner's, never in its place) calls `PartitionsChanged(c, old, new)`
      (running specs: Recorded while Partitioned, else none) before the scan
      is published; the runner records the spec it answers from at once, so
      no request between the hook and the publish sees the old mode. It
      revokes and stops people's instances without user partitions, and
      retires the primary's generation whose env or existence changed: its
      token revoked in the hook (01 §6), the process drained in the
      background (the hook runs under the scan lock), a start in flight
      restarted once it lands; an alwaysOn global instance is woken a
      second later, after the publish, whichever rescan it was (a mode act
      or an unseal's resettle never reaches the watcher's wake).
    - **A user-only tile never runs its primary**: startFor refuses it on
      every path (lazy start, deploy, restart), install refuses one that a
      mode change caught mid-start, ensurePrimary refuses early, alwaysOn
      skips it, and a deploy to it prepares and commits without a
      generation (deployIdle) and moves people's partitions.
    - **One build per change.** The primary's work-tree build of a
      partitioned tile goes through a flight keyed by a per-tile build
      sequence, which Changed and every work-tree deploy advance (before the
      primary's own rebuild starts). A start joins the latest flight of the
      current sequence while it is in progress, or while the change's
      restarts are still coming (its wave: the live partitions Changed or a
      deploy restarts, and Changed's primary restart), and reuses its bin —
      or its error, sticky as today. Any other start (a reap, a crash, a new
      person, the global instance's own) builds anew as the primary always
      did, which picks up go.work and dependency tiles; builds onto the
      tile's one output path never overlap. Changed restarts live
      partitions ≤ 4 at a time; a Deploy/Restart that swapped or prepared
      the primary does the same on the build it made (a deploy of what a
      partition already runs leaves it be). Checkpoints were already shared.
    - **Failures.** A person's partition keeps a build error or a crash
      loop sticky until the tile's code changes; any other failure (spawn,
      health, a gate that closed meanwhile) is answered to the requests
      waiting on that start, and the next request starts afresh through
      admission. The reaper forgets states that ran nothing for 10 minutes
      (with their run dirs) unless their failure is sticky.
    - **Eviction** takes only what isn't in use: no active connection or
      hold, no interactive request in the last 2 minutes, and no start
      answered in the last 30 s (so a delivery's socket isn't stopped before
      the proxy tracks it); a background start also spares a streaming one.
      The 503 text is exactly the documented one.
    - **Limits** live in `data/partition-limits.json` (not under
      `data/partitions`, so a switch's wipe never takes them): an admin's
      workspace cap and per-tile cap and byte ceiling, a manager's lower
      ones beside them; effective = the lower; POST refuses a manager's
      raise (403), the tile's own credentials (403), an unknown tile (404,
      after the auth check); a file that can't be read gives the defaults
      and POST 500s without overwriting. The runner reads it through
      `PartitionCapsFor`, the data plane through `Broker.PartitionBytes`.
      The defaults derive from min(MemTotal, the lowest memory.max on
      xbind's own cgroup v2 path).
    - **Metadata only for admins.** A person's instance's cgroup limit hit
      raises an admin-only alert naming "a person's partition of <tile>";
      its run dir goes when it stops (its log stays: the person's).
  - **Not chosen:** partition states in today's map (every walker of it
    would have needed a filter; a missed one would count a person's
    instance as the tile's primary); starting the primary from
    PartitionsChanged (it runs under the scan lock, possibly for a removed
    tile); the flight for every tile (would change the zero state's rebuild
    on each restart); reusing a landed build for every later start (a reap
    or a new person would miss go.work and dependency-tile changes);
    waiting stops for the whole build in flight (a person's stop would wait
    minutes for `go build`; the refused spawn is enough); a state per
    (tile, dep, person) keyed by user id (a recreated person would inherit
    the old incarnation's process).
- **D144 — Partitioned tiles, F4: people's data namespaces,
  shared resources and volumes (2026-09-30).** Implements PD-48, and the
  data side of PD-01, PD-04, PD-05, PD-13, PD-26, PD-43 and PD-45
  (plans/partitions/90-decisions.md; 03 §B, 04 §1-§2, 05 §2).
  - **Chosen.**
    - **One key function, one more level.** `nsKeysFor(scope, dep, pkey)`
      and `resKeysIn(rt, dep, pkey)` (deploydata.go) answer today's keys
      byte for byte for `pkey == ""` (`scopeKeys`/`resKeys` are now their
      wrappers); a person's namespace is
      `.partitions/<escS>/<dep>/<pkey>`, `dep` spelled for main, with its
      own `kv.db`, bucket labels (`kv:<NS>/<bucket>`) and volumes
      (`<NS>/fs/<name>`). The pkey is `u-` + 32 hex, checked on every use
      (`pkeyOK`); the workspace scope is never partitioned.
      `TestNoAdHocResourceKeys` now also refuses a `.partitions` literal or
      `partitionsLevel` joined with `+` anywhere in the broker but
      deploydata.go.
    - **One decision for which partition — the identity plane's.** The
      reach (`partitionReach`, from `reachRes`; `PartitionEnv`) asks
      `addressedPartition` (F2, through `addressedPartitionSeam`) for the
      partition a caller acts in on the scope's root: its own credential's
      on its own scope, and for everyone else the root's answer — which
      maps another partitioned tile's user partition onto the same person's
      (they read the tile; with `partitionConsent`, consented) and settles
      an unpartitioned caller (global's, 403 without a global instance) in
      one place for calls and data alike. F4 counts an allowed cross-scope
      reach of a user partition in its ledger (`partitionEdgeSeam`). A
      resource of a scope whose root's recorded mode has user partitions is
      the caller's partition's unless shared; global and shared at today's
      keys; `"read"` sets `readOnly`, which `readClamp` turns into the 403
      for every write (kv, blob, bus). A paused root (pending, invalid) or
      an unreadable mode record answers 409: never a fall-back to today's
      keys (F1's open end). Cron resources aren't partitioned here (their
      jobs are the registrant's, F5). User partitions reach data on the
      primary only (PD-17).
    - **The bus stamp is F4's** (02 §9, 04 §2): `publishPartitioned`
      (partitionbus.go) stamps every publish on a partitioned scope's own
      bus with the publisher's partition — `global` for the global
      instance's too, so a person's frame never sees global's events — and
      `busPartitionReaches` (called from `busFilter`) delivers an event
      there only to subscribers acting in the stamped partition; an
      unstamped one reaches no one there. Global's events also reach
      today's push subscriptions (all global's); a person's, only their
      partition's (F5's seam). A shared bus is unstamped, as today.
    - **Whose a namespace is**: `ns.json` gains `partition: {user, uid,
      tile, created, orphan?}`, written before anything lands (a writer's
      first kv/blob request, or the instance's first start); `tile` is
      always the scope's root, which owns the namespace and its archives.
    - **"Holds data" counts any entry under `.partitions/<escS>`** of the
      scope the tile roots (store `partition-namespaces`): a partition
      exists once written or started, and the rule leans toward asking. A
      tile without resources never gets one.
    - **Orphans only on a recorded event** (PD-26): the users store's delete
      hook (`PartitionUserDeleted(user, uid)`), the id held by someone new,
      or the tile gone from the registry (reclaimed if it returns). The uid
      decides whose a record is when both the store and the record carry
      one (the same uid is the same incarnation whatever the clocks say);
      only without one does the time decide — a record created in a second
      before the holder's — and a record made in the holder's own second is
      ambiguous: kept, logged for bx doctor, never adopted from. Swept 30
      days later (`partitionRetention`), never while the tile is paused;
      the sweep erases the partition's `part:<TileKey>/<dep>/<pkey>` backup
      subkey under the scope root's backup lock. A missing record, an
      unknown person and a mode change (pending, declined, invalid) never
      orphan.
    - **The switch's wipe** (`wipePartitionNamespaces(tile, dryRun)`): on a
      switch that deletes everything, every partition namespace of the
      scope, each under its own hold, volumes unmounted and verified first,
      kv files closed; a dry run counts namespaces, bytes and people for
      the confirmation. Removing or adding `global` keeps people's
      partitions (H1). An error keeps the switch from completing.
    - **Binds**: a user partition's remap binds its own volumes at the
      canonical paths; a shared resource's `ResBind{Shared: true}` binds
      main's data only at its own canonical path and only where the runner
      re-derives "shared" from the registry (`Registry.PartitionedScope` +
      `Resource.Shared`, `sharedResources`), never on the broker's word
      (C7); a `"read"` resource is bound read-only whatever the entry says;
      every source under `.xbin/resenc` must be a mounted view, never its
      bare mountpoint (`resenc.IsMountPoint`), so an instance never writes
      plaintext a later mount hides. `mainData` treats `.partitions` as not
      main's.
    - **Volumes (PD-48)**: resenc locks per `(dirKey, name)` (an epoch
      keeps a seal's `UnmountAll` authoritative over Ensures in flight); new
      partition volumes are initialized with `-scryptn 10`; every Ensure
      (and `Touch`, the broker's already-mounted short-circuit) restarts a
      view's idle clock, and so does each scan that finds its instance
      running, so the clock runs from the instance's stop; `Hold`/
      `UnmountIdle` unmount a partition's volume nobody used for 60 min and
      whose instance the runner doesn't report; the partition's kv file
      closes on the same rule. The disk monitor's scan runs the reaper. A
      person's partition also doesn't start while a data act holds its
      primary's namespace, where its shared resources are.
    - **Disk (03 §B.7)**: each partition namespace is its own quota bucket
      (its ceiling the tile namespace's limit, lowerable by the partitions
      API), its own candidate on low disk, alerts to admins only (the tile
      and whose partition, never contents; the alert's tile is the
      partition's quota key, which no component path equals, so
      `/tile-status` never shows it), never counted in the tile-visible
      "N tile(s) over quota" line. `/runtime` lists per-partition rows.
  - **Not chosen:** a consent and read check of F4's own beside
    `addressedPartition` (two seams F10 must fill, and calls and data could
    disagree); stamping only people's events (global's would reach every
    person's frame); reusing the deployments' sweep for partitions (its
    claimant rule would orphan every partition); orphaning on a pending,
    declined or invalid mode, a missing index or an unknown user (03 §B.8);
    trusting `ResBind.Shared` without the registry; unmounting idle volumes
    before the runner can say which instances run (the default keeps
    today's "mounted until seal").
- **D145 — Partitioned tiles, F13a: a tile manager keeps or
  switches; the switch wipes through one executor (2026-09-30).**
  Implements PD-44 (the owner's ruling: keep or switch, the grey-out), PD-49
  (who decides), PD-19 (switching to user partitions needs `--isolate`), the
  owner's ruling H1 (adding or removing `global` doesn't wipe people's
  partitions), 01 §2.4-§2.7's surfaces and 11 §3's erase on a switch;
  C12's sandbox-manager check. Design: plans/partitions/01-manifest-registry.md
  §2.4-§2.7; docs/partitions.md §The mode; docs/protocol.md `POST
  /partitions/mode`.
  - **Chosen.**
    - **One route, two acts.** `POST /api/xbin/partitions/mode {tile, act,
      from, to, confirm?, yes?, dryRun?}`; from/to are `{user, global}` or
      null (unpartitioned), the shapes the components row and the 409 body
      carry. They must still be the request's R and Q (F1's
      `recordDecision` checks again under the settle lock): a race is 409
      with the current partition state, never a decision on another
      request. keep answers an open request; switch an open or a declined
      one (managers keep seeing "Switch…" after a keep), so
      `recordDecision` takes a declined request for a switch. A dry run
      (`dryRun`) is the preview: the same counts, nothing deleted, no
      confirm needed.
    - **Who decides: a tile manager as a person.** `mayManageTile` on the
      person behind a person credential — their session, app, device or
      the root token — or the admin tile's frame under their login
      (`AdminFrameDriver`, as D127m; see Deviations: PD-49). Every other
      tile principal (instance, frame, terminal, agent session, cron, bus)
      is 403 whatever its tile holds; so are writers, readers and
      outsiders, through the admin frame too. Route class PrimaryOnly.
    - **The switch's order** (01 §2.5): the typed tile path; `--isolate`
      for user partitions; an offloaded tile refused; bound sandbox
      managers whose `GET /sbx/hello` caps lack `partitions` listed and
      refused unless `yes` (a manager that can't be asked counts as
      lacking). Then: the tile is held (`beginSwitch`; `switchStartHold`,
      read by `PartitionHoldReason`, keeps its primary *and every other
      tile of the scope it roots* from starting, even a declined tile's
      running R; `switchHold`, the tile's own, makes "holds data" answer
      yes so a rescan during the wipe never records an auto mode); every
      namespace to wipe is held (`holdNS`, act resetting: main's, every
      deployment's on disk and every deployment the record names) —
      *before* anything stops, so nothing started in between writes into
      what is wiped; every deployment of the tile and every other tile of
      its scope stops (they read its namespaces); each wipe hook's `stop`
      runs; the tile's backup lock is taken (F17a's `holdBackups`); every
      wipe hook runs; the backup keys of the wiped data are erased
      (`eraseBackupSubjectsHeld`: every `ns:` and `part:` subject of the
      tile, never `tile:`) — an erase that writes no tombstone fails the
      switch (500, nothing recorded, the request open; a retry wipes
      nothing more and erases), one that tombstoned but left key files is
      a success with `eraseError` and `wiped.keyFilesLeft`; R := Q is
      recorded with the wiped summary (history `switch`, `wiped:
      {namespaces, partitions, vaultKeys, registrations, bytes, subkeys,
      keyFilesLeft?}`); the tile reloads; a deploy-log line, the slog
      audit, and the people's notices follow. Instances start lazily in Q.
    - **The wipe executor** (`internal/broker/partitionwipe.go`): every
      store is a `wipeHook{name, stop, wipe}` registered from an init func
      (`registerWipeHook`), like F1's `registerPartitionStore`. Today's
      stores are registered here: main's namespace at today's keys —
      every kv bucket one segment below `res:<scope>/` (a resource the
      scope dropped still held data), its volumes (unmounted and verified
      first) and plaintext directory — only when that namespace is the
      scope's own (`ownsMainData`: the scope holds its data key, D118, and
      its bucket prefix isn't the workspace-level resources'
      `res:workspace/`, which a top-level tile at the path `workspace`
      renders); every deployment namespace on disk, wiped as a reset
      (`wipe`, ns.json kept and marked reset); every vault file (main's
      and every deployment's); cron jobs, bus subscriptions (main's stores
      and every deployment's live rows and files), interface instances
      and ingress hosts, then consumers re-bound and ingress reconciled.
      F1's `holdsNamespaces` counts main's namespace by the same rule, so
      "holds data" and the wipe agree. After a switch, "holds data"
      answers no (the test pins it). `TestWipeHooksCoverHoldsData` pins
      that every store "holds data" asks has a wipe hook of the same name
      (`lifecycle` exempt: an offloaded tile is refused).
    - **H1.** user ↔ unpartitioned deletes everything (`wipeEverything`);
      removing `global` deletes global's data only — its namespace at
      today's keys (its own and the shared resources), its vault and its
      registrations — and erases `ns:<main>` alone (`wipeGlobal`; nothing
      when the scope doesn't own main's namespace); adding it deletes
      nothing (`wipeNone`). Hooks read `t.Kind`. Every surface says what
      the switch deletes with `registry.SwitchDeletes`/`SwitchDeleting`
      (the page, the alert, the push, the refusal, the preflight, the
      proxy's 409): PD-44's "all data" words for user ↔ unpartitioned
      alone.
    - **A wipe that fails part-way** answers 500 with what it deleted:
      nothing is recorded, the request stays open, what went stays gone
      (the manager confirmed it), and a retry finishes it. Keys are erased
      only after every hook succeeded.
    - **The grey-out.** `internal/server/partitionpage.go`, called from
      `handleComponentStatic` after the read gate (the D36 precedent) and
      from `serveQualified` for the primary's own deployment URL
      (`/c/<tile>+<primary>/`, and a deployment origin's bare URL): a
      pending tile's document *loads* — `Sec-Fetch-Dest`
      document/iframe/frame/embed/object, or without Fetch Metadata a
      directory URL, an `.html` file or `?native=1` under a login session
      or by the tile's own frame (the app's scheme handler) — are xbind's
      own page, 409, `no-store`, CSP `default-src 'none'; style-src
      'unsafe-inline'; sandbox` (no script, no reference: it renders the
      same in every shell and the app). Any other read — a fetch of the
      HTML, a bearer's, another tile's code grant — and the tile's other
      files are served as before; another deployment's URL isn't paused; a
      caller who can't read the tile gets today's answer. The
      `partitionNote` is escaped text. `/alerts` gains kind
      `partition-switch` (warn, tile) and `partition-invalid` (warn, tile:
      the request can't run, or the mode record can't be read) for admins
      and the tile's readers (01 §6). A request opening pushes to the
      tile's managers (every enabled user `mayManageTile` passes, and the
      root token's devices) at most once per tile per 15 minutes
      (`partitionPushQuiet`), and every mode change publishes `reload` for
      the tile, so its frames swap to the page and back.
    - **Pushes** go through `push.Service.Notice`: kinds under `tile`
      (`tile.partition-switch`, `tile.partition-deleted`), so today's app,
      which registers `tile`, receives them; they spend the person's
      budget and ignore a tile's mute (they are about the person's data)
      — hence the per-tile quiet time, so a writer toggling the manifest
      can't burn the managers' budget.
    - **The deploy log.** `Plane.LogPartitionSwitch` writes an entry of how
      `partition-switch` on the primary, no checkpoint, by the deciding
      principal (`checkpoint.LogHows` gains the word; an older xbind's
      reader skips entries it doesn't know). A tile without a deployment
      record has no deploy log; the mode history and the audit line have
      the switch there.
    - **The promote/rollback preflight** (01 §2.7): a dry run of a deploy,
      roll back or promote onto the primary of code asking for another
      mode carries `impact.partition` — a pause on a tile that holds data
      (asked through the new `DataHooks.PartitionHolds`, wired to
      `Broker.PartitionHoldsData`), at once on one that holds none, or a
      mode a manager declined; `bx deploy` prints it.
    - **`bx partition switch|keep <tile>`** read R and Q from the tile's
      `/components/<tile>` row and send them back (a changed request is
      refused, never decided blind); switch previews with the dry run,
      asks for the typed path on stderr (`--confirm` answers without
      asking), takes `--yes` for C12 and `--dry-run`. A row with no
      `partition` is asked through the route itself (a dry keep): an
      xbind older than partitioned tiles has neither, and both commands
      exit 6 there; one that has the route says why there's nothing to
      decide.
  - **Not chosen:** a separate preview route (the dry run of the act itself
    keeps one code path for the counts); wiping only the resources the
    scope declares today (a dropped resource's bucket would survive and
    keep the tile "holding data"); deleting the deployments' data when
    `global` goes (they aren't global's); a new push kind outside `tile`
    (today's app wouldn't receive it); serving the switch page to people
    who can't read the tile (it names the tile's note); stopping before
    holding (a lazy start in between would write into the wipe); a
    `features` flag for bx (the route answers the question itself).
- **D146 — Partitioned tiles, F13b: templates and updates never
  switch a mode (2026-09-29).** Implements PD-52 and the instantiation of
  PD-35 (gated by PD-19) of plans/partitions/90-decisions.md; the design is
  plans/partitions/01-manifest-registry.md §2.7. docs/partitions.md §The
  mode, docs/protocol.md (`/templates`, `/templates/new`,
  `/builtins/update`), docs/bx.md, docs/elements.md.
  - **Chosen.**
    - **The default lives in the stripped block.** `builtins.InstanceOpts`
      rides `CopyTree`/`RenderTree` (their `stripTemplate bool` became
      `tpl *InstanceOpts`, nil for an import): `stripTemplateBlock` writes
      `template.partition` as the copy's top-level `partition` when
      `Partition` is set, and removes any top-level key (any case) when it
      isn't (the opt-out and "no isolation" both mean *no mode at all*).
      The strip keeps its re-marshal (pre-existing: comments dropped, keys
      sorted), so the key lands in sorted order.
      `TestTemplatesNoTopLevelPartition` holds that no builtin template
      carries a top-level `partition` and that a block default is a mode
      `registry.ParsePartition` accepts, which is what makes a `git merge
      template/main` unable to bring one in.
    - **The request.** `POST /templates/new` takes `partition` (only
      `false` means anything: `true` or absent is the default); the broker
      decides `InstanceOpts` in `templatepartition.go` (`instanceOpts`):
      opted out → "opted out", `!confine.Isolated()` → "needs --isolate"
      (`templatesIsolated`, a var tests pin). The catalog shows builtin
      defaults as written (`TemplateEntry.Partition`) and workspace defaults
      through `registry.ParsePartition`. `bx template new` filters
      `--no-partition` and parses every other token by position exactly as
      before; a 400 from an xbind older than the field says to drop the
      flag.
    - **`by` on the auto record.** The handler notes the instantiating
      person (user id; `owner` for the root token; the component for a tile
      principal) under root+tile in a package-level `sync.Map` of
      per-request pointers: `LoadOrStore` before the copy (so the watcher's
      rescan sees it), `Store` once this request's copy is the one written,
      `CompareAndDelete` of its own pointer after — a concurrent request
      for the same path never overwrites or removes the winner's note. F1's
      `decide` calls `stampAutoBy` (one line), which names them on the
      `auto` history entry the settle adds.
    - **Keys match in any case.** The registry reads `partition` into a
      struct field, so `"Partition"` is a request too: `jsonc.TopLevel`/
      `SetTopLevel(Like)`/`DeleteTopLevel` and the template strip match
      member keys with `strings.EqualFold` (the last one wins, as
      encoding/json reads it).
    - **Replace splices the installed value.** New
      `internal/jsonc/members.go`: `TopLevel`, `SetTopLevel`,
      `SetTopLevelLike` (absent → inserted after the nearest member that
      precedes it in another version of the document, so a diff doesn't
      move lines) and `DeleteTopLevel` edit one top-level member and keep
      every other byte. `ApplyReplace` writes each manifest with the
      installed value, read from the file, else from its own side of
      conflict markers (`conflictSide`), else from the mode xbind last read
      from the tile's code — `Updater.SetRecordedPartition`, filled by the
      broker's `recordedPartition` (an open or declined request's mode,
      else the recorded mode, none without a record; unknown when the
      record can't be read). When none can tell, the manifest isn't written
      and the new version isn't recorded, so the update stays offered.
    - **Merge and proposals undo upstream's change.** `undoPartition` sets
      theirs' key to base's value where base has it (or deletes it), so
      `git merge-file` carries ours' line untouched — byte for byte when
      upstream didn't change the key — and a proposal's series never
      touches it. Only a merge whose conflict takes in ours' partition line
      (resolving it one way would drop the builder's value) is merged again
      without the key and gets ours' value back: where ours has it when
      that merge is clean, else right after the opening brace beside the
      markers (`mergeKeeping`). A proposal whose only change would be the
      partition renders no series: `ProposeBuiltinPR` records the version
      (`RecordApplied`) and `/builtins/update` answers `{files: [], notes}`.
    - **Notes.** Replace notes wherever the kept value differs from
      upstream's; merge and proposals only where upstream changed the key
      (a local edit upstream left alone is the builder's, and merge keeps
      local edits anyway). A workspace that never used partitions gets no
      notes. The recorded base stays upstream's own rendering, so a kept
      difference reads as the builder's edit (`user`), never as a pending
      update.
  - **Not chosen:** writing the default at the template's top level (a
    `git merge template/main` would add or remove it); refusing
    instantiation of a template with an invalid default (the instance
    carries it as written and runs no backend until fixed, as F1 does for
    any invalid request); re-marshalling the updated manifest (drops the
    builder's comments); stripping the key from all three merge sides
    whenever they disagree (moved the builder's line and dropped its
    comment on every update); taking upstream's value for a manifest that
    doesn't parse (a conflicted merge abandoned with a replace would have
    opened a switch request); refusing the registry's non-exact-case keys
    (a manifest change in F1's area; matching them is additive).

- **D147 — Coding agents in the agent template: AgTT drives Claude Code,
  Codex, Gemini CLI and opencode over ACP inside a coding sandbox — as a
  conversation or as a child the agent spawns and steers — and terminals
  join the sandbox-manager contract (2026-09-30).** plans/agtt-harness.md
  (the spec; its §10 is what was built differently — the code and §10 are
  the final behaviour); builtin-templates/agent/API.md §Coding agents;
  docs/sandbox-manager.md (§hello `images[].harnesses`, §Terminals,
  §stdio); docs/sdk.md (§Driving a coding agent, §Testing an ACP client,
  §A manager's terminals); docs/protocol.md §Tile sandboxes. Two systems
  didn't meet: xbind's ACP client (the Agent tab, D74/D75/D77) drove the
  coding CLIs through their adapters, while coding sandboxes (D115,
  D120–D122) had one consumer, the agent template (AgTT), whose engine is
  its own LLM loop. The owner wanted AgTT an ACP client (web and native),
  the harnesses in the base images, a choice at a conversation's start,
  harness agents the agent spawns and steers, and a terminal.
  - **The owner's decisions (2026-09-29).** Terminals are part of the
    sandbox interface: consumer backends open PTYs through the manager and
    relay them to their pages and native views with the person
    **asserted** — every manager implements it, future cloud managers too
    (`ssh -t`); this supersedes D121's "not chosen: relaying terminals
    through the backend". Autonomy is each person's, per harness: **Auto**
    (the provider's auto-edit mode) or **Always approve**, for their
    conversations and the children spawned for them, and every harness
    permission request becomes an AgTT approval (cards, Needs, push, the
    child card, the board). Bypass modes are owner-only, never chosen by a
    model. Harnesses get **no MCP** for now (`session/new {mcpServers:
    []}`). The AgTT agent can spawn coding agents with tasks and steer them.
  - **Chosen, and why.**
    - **One chat model.** A harness run (`runs.engine = 'harness'`, fixed
      at creation; the engine forks at `pass()`, and such a run never
      reaches an LLM wire) writes AgTT's own rows: one assistant row per
      call (`acp:<kind>`) and one tool row whose `Meta.harness` the view
      lifts to `acp`. fold.js, chat-cards and native chat.js draw it, not
      xbind's `/vendor/agent-cards.js` — the native view can't import
      that, and Needs, push, links and digests, being engine-agnostic,
      work unchanged.
    - **Transport: a baseline every manager has, `stdio` when offered.**
      The exec routes: stdin chunked to `stdinMax` (503 retried), output
      long-polled by offset in base64, stderr to a log file through a `sh
      -c` wrapper. The additive `stdio` capability — one WebSocket per
      non-tty exec, stderr split, the newest attacher holding stdin, a
      ping's pong acknowledging stdin — is built in tilesbx, the SDK,
      coding-sandbox and fakesandbox, and the pipe prefers it: any manager
      runs a coding agent, and one that offers more saves the polling.
    - **Nothing of xbind's in the sandbox** (another tile's, with no xbin
      identity). No `bx __agent-host`: the client advertises `fs` and
      `terminal` off, and on the `_meta` flags `terminal_output(_delta)`
      (D77's terminal output still flows), `subagent-transcript` and
      `terminal-auth`, and form and URL questions — a URL one honoured
      only during an AgTT-started `authenticate`.
    - **One delegation verb**: `harness` on `subagent_spawn` (a subagent's
      link, digest and delivery) plus `harness_mode` (`approve` | `plan`:
      it only narrows the owner's setting); `maxHarness` 3 at work per
      tree; refused near the sandbox's exec cap; the parent model never
      answers a child's permission — its message waits for the person — and
      a person's message to a child reaches the parent as a notice.
    - **Governance** by class: the toolset `harness` (needs `sandbox` and
      an egress other than `none`: a coding agent must reach its provider)
      and the field `harnesses` (`"all"` | ids); the built-in `coding`
      class has both.
    - **Credentials stay in the sandbox HOME**, shared with its users,
      clones and snapshots: signing in on a shared sandbox takes a warning
      and a confirm (`authenticate` enforces `confirm: true`; a terminal
      sign-in can only warn). `authenticate` covers an API key — relayed
      once, never an inbox row, a journal line or a column — and a device
      code, which is the requester's alone (in memory; everyone else sees
      `login.device: {by}`, since whoever enters a code first signs the
      shared harness in as themselves).
    - **The handoff consistency model.** A save or restart of the agent
      never stops a coding agent: one consumer per session applies events
      in order; durable events commit their rows, `read_off`, the snapshot
      and the unflushed draft in **one fenced transaction** (the epoch),
      chunks and output deltas stay in memory and replay from `read_off`.
      Prompts and steers are **at most once**: a prompt is `sending` until
      its frame is on stdin, and a successor fails a `sending` one ("send
      it again"); a request cut off by a stdio drop is abandoned
      (`Client.Abandon`); a steer is marked before it goes, and one whose
      answer was lost is written with a note, never sent again. Answers
      are **recorded, then re-sent by rpc id** (never the pid: a
      successor's pids start over) until the adapter has them; questions
      are restored only as the record knows them.
    - **Bypass modes are default-deny.** A mode is anyone's only when the
      SDK catalog knows it never takes the agent past its own asks
      (`acp.Provider.Safe`) or the adapter opened its first session in it
      unasked; every other is owner-only wherever a mode is chosen — PATCH,
      `POST /ask`, a stored option of category `mode`, and a permission
      option that switches to one, claude-agent-acp's `exit-plan-bypass`
      and kin included (`OptionModes`; on a mode switch, any
      `allow_always` whose mode can't be placed). The review found the
      catalog-only rule let a participant into bypass through an unknown
      harness, a newer mode or a plan approval.
    - **Rollback safeguards** (to v0.3.64 or older): two triggers keep a
      harness run's `turn_steps` at `maxTurnSteps`' ceiling, so an older
      `turn()` ends any turn it starts there before a model call; the
      `harness` toolset is stored apart from `toolsets` (an older editor
      refused every class save); notices to a parent live in
      `harness_notes`, not the inbox (an older `hasWork` would wake the
      tile every minute for a notice an idle parent keeps by design).
    - Also: coding-sandbox's defaults gain `IS_SANDBOX=1` (Claude Code's
      spelling); tty execs default `TERM`, `COLORTERM`, `LANG`; an idle
      adapter is reclaimed after `harnessIdleMin` (15) by a one-shot timer.
  - **Not chosen** (spec §9): questions answered through `POST
    /runs/{id}/answer` (the text answer to `ask_user`) — a park gives
    approve's 409 semantics; the mode through `PATCH /runs/{id}` — it is an
    RPC that can fail (502) or need the owning process (503); a sign-in as
    status `error` — it is a wait (`waiting_input`, kind `login`), so a
    child's link stays open; `acp` split between the two rows; a separate
    per-sandbox harness route (folded into `?probe=`); a class switch
    forbidding bypass (owner-only already); an app change for native
    terminals (the owner chose the relay); children always in the default
    mode; the setting in xbind's prefs (per principal — the engine must
    read the owner's at spawn); deferring `stdio`; policing asserted
    persons in the manager (that changes a contract every manager in the
    wild implements — AgTT checks the caller's own `sandboxAccess(caller).Use`
    before it relays). Not in v1: MCP, llm-gw for harness traffic, a
    per-person vault key, per-turn "files changed", live text of a
    harness-internal subagent.
  - **Consequences and limits.** The sandbox's co-users can read a private
    conversation's harness traffic (every consumer sees every exec of a
    sandbox it may use); the UI and API.md say so. An idle adapter is held
    — and holds off the sandbox's idle stop — until the reclaim. codex and
    gemini refuse `session/new` signed out (`AwaitLogin` keeps them up to
    sign in), so their live mode lists are unseen: a mode the catalog
    lacks is owner-only until it lists it. The live check (`test/isolated`
    `TestHarnessLive`) drives the real adapters only with
    `XBIN_HARNESS_LIVE=1`: they reach outside services. A successor's
    attach while the manager doesn't answer is retried without a cap (2 s
    doubling to 1 min, holding the backend up); an interrupt queued just
    before that attach is lost if it fails. A device code lives in the
    process that started it: after a handoff the requester starts over. A
    rollback to v0.3.64 leaves queued `hprompt`/`hanswer` rows waking that
    build every minute (the clean-up SQL is in API.md) and a class save
    there drops every class's coding agents. The native view has one
    terminal at a time and no buttons on child cards (the app's `toolcard`).
    Conformance: no manager whose run passed fails on the upgrade —
    `caps/missing` takes a pre-`stdio` manager's `not-found`, and
    `tty/backend` (a consumer backend's terminal for an asserted person)
    only warns (skips, saying why) this release; **the next release makes
    it fail** (docs/changes/2026-09-30-manager-terminals-for-backends.md;
    `Target.Strict` holds the reference managers to it now).
  *Amended 2026-10-01 (D172): in a partitioned agent (D158) coding
  agents work only in a person's own conversations, in a sandbox homed in
  their partition — the global instance, shared and hosted conversations
  never start, drive or sign one in, and a coding agent's conversation
  never moves between homes (plans/partitions/90-decisions.md §I15); from
  a person's partition the relays' `Sbx-User` is the partition's verified
  person (D140), not asserted.*
  *Amended 2026-10-01 (D179): a coding agent signs in through a guided
  sign-in too (the CLI's own login run as an exec, its link and code
  relayed to the person), and in a person's own partition saved sign-ins
  — kept in their vault, handed to the CLI in its environment only in a
  sandbox of theirs no one else uses — win over the sandbox HOME's; an
  API key reaches each adapter's `authenticate` in its own shape.*

- **D148 — Partitioned tiles, F5: people's partitions' vaults,
  registrations and records (2026-09-30).** Implements the vault,
  registration and record halves of PD-20, PD-21, PD-26, PD-27 and PD-43,
  and C5, S15, S19, C22 (plans/partitions/90-decisions.md; 03 §C-§E,
  06 §8). Design: plans/partitions/03-runner-data-vault.md §C-§E;
  docs/partitions.md §Vault and registrations.
  - **Chosen.**
    - **One directory per person's partition, beside the mode record.**
      `data/partitions/<TileKey>/<dep>/<pkey>/` holds `partition.json` (the
      identity record: tile, dep, user, uid, created, lastStarted, state
      active|orphaned, reason, rebuilt) and the registration files
      `cron.json`, `bus-subscriptions.json`, `iface-instances.json`,
      `ingress-hosts.json` — schema 1, the deployment files' rows, plus the
      tile and the partition the file belongs to, which every load checks.
      The vault is `data/vault/.partitions/<TileKey>/<dep>/<pkey>.json`
      (the global envelope plus tile, deployment and partition; 0600;
      barrier-sealed; never bound into a sandbox). The record is written
      before anything else of the partition lands (its first registration,
      vault key or start — `PartitionEnv`, once per spawn, stamps
      `lastStarted`), so a TileKey directory always names its tile for the
      boot load. An older xbind never reads any of it, so it never fires a
      person's job as the tile's; a backup of the tile carries global's
      rows only (they never join today's maps: S15).
    - **Which store a call reaches: the principal the partition gate
      stamped.** A credential of the tile acting in a person's partition
      (`auth.Principal.Partition`, the gate's stamp; an instance token's
      own) reaches that partition's vault and registrations, always on the
      primary; everyone else — admins, other tiles, the root token —
      today's (global's). `?partition=` on a partitioned tile's vault, cron
      and bus-subscription routes is 400 for everyone (the tile's recorded
      mode has user partitions, or can't be read), ignored elsewhere (C22).
    - **The liveness gate (PD-20) is asked at every tick, publish and
      delivery, never cached**: the tile runs people's partitions (not
      paused), is enabled, the deployment is the primary, and the person
      exists, is enabled, can read the tile and has the directory's pkey
      now (the same uid). A person who regains access resumes without
      registering again; a recreated id (a new uid, a new pkey) never fires
      the old incarnation's jobs.
    - **Deliveries carry the partition** — `Principal{Component: xbin/cron
      | xbin/bus, Partition: <the registration's>}` (F2's delivery
      principals), which Route sends to that partition as a background
      start (F3). A cron tick the runner defers (503 `partition start
      deferred`) is retried with jitter (10–30 s, less near the next tick)
      until the next tick is due; one still deferred is missed and counted
      (`MissedTicks`). Caps (C5): 16 jobs, 16 subscriptions a partition,
      no schedule more often than once a minute.
    - **The bus.** `busSubs.publishStamped` takes the event's stamp and the
      tile whose credential published it: global's reaches today's
      subscriptions only; a person's (publishPartitionPushSeam) that
      person's partition's, and may start it only when the publisher is the
      subscriber's own tile (its backend, frames, terminals — the partition
      runs its own code, 03 §D); an unstamped event (a shared bus, an
      unpartitioned scope's) today's and every matching partition
      subscription. A delivery that may not start its partition is queued
      only while it runs (`partitionRunning`, the runner's side) — a
      skipped one counts as `dormantDrops`. Only partition mail (F6)
      cold-starts a partition from outside it.
    - **A subscription to a partitioned scope's bus is a reach of that
      scope by the partition (05 §1-§2, PD-13)**: `partSubReach` asks
      `addressedPartition` for the partition's instance — on another tile's
      scope, its person must read that scope's tile and, with
      `partitionConsent` on, have consented (`partitionConsentHolds`, F10's).
      Asked at registration (403), at every publish and at every delivery
      (skipped: `dormantEvents`, the reason in `lastError`), never cached;
      the row lists `dormant` meanwhile. The registration and each
      delivery count one edge in the partition's egress ledger
      (`reachPartition` → `partitionEdgeSeam`), as the data plane's reach
      does.
    - **Dormant registrations (PD-21)**: the partition gate's
      GlobalOnlyDormant arm passes, stamped; `routeTarget` answers a
      person's partition's own target, always dormant, and the stores write
      its files (a NUL-joined target in routeTarget's deployment string, so
      `netfn.go`/`ingressfn.go` stay untouched); `routesHidden` answers a
      partition's terminator no routes.
    - **Notify (PD-27)**: `notifier` gives a user partition's instance
      token its person as `self` — the frame's rule; the global instance
      keeps the backend's.
    - **Identity records (PD-43, PD-26)**: `partitionAdoptUID` now adopts
      the one uid a person's live `ns.json` **and** `partition.json` records
      agree on, made after the second their users-store record was — one
      rule, `adoptablePartitionUID`, which reads both; conflicting uids
      adopt none. Boot's load of the registrations re-adopts at once for a
      person whose uid an older xbind's rewrite of the users store dropped
      (`readoptPartitionUID`: adopts or does nothing, never mints), so
      their jobs fire without waiting for their next request.
      `partition.json` keeps the crash metadata admins see (PD-24):
      `lastExit`, `restarts` (exits the runner's crash watch saw, since the
      record was made) and `crashLoop` (cleared by a start), written
      through the runner's new `PartitionExit` hook, so they survive a
      restart of xbind. The namespaces'
      sweep also sweeps records: a missing `partition.json` is rebuilt from
      `ns.json`; the orphaning events are the namespaces' (the users-store
      delete hook through `PartitionUserDeleted`, an id held by someone new,
      the tile gone — reclaimed when it returns); an orphan is deleted
      30 days later (`partitionRetention`) with its registrations and vault,
      never while its tile is paused. A new tile at a removed tile's path
      drops the path's partitions' registrations (D85, as
      `dropDormantAt` does the deployments'); their records and vaults are
      leftovers of the path (`partitionLeftovers`).
    - **Holds data and the switch's wipe** (01 §2.2, §2.6): three stores,
      each with the wipe hook of its name — `partition-vault`,
      `partition-registrations`, `partition-records` — deleting every
      person's on `wipeEverything` (the broker's rows dropped first), none
      on `wipeGlobal`/`wipeNone` (H1); the dry run counts vault keys,
      registrations and people. The mode record stays (the switch writes
      it next). The wipes and drops hold the stores' file locks (cron's,
      the bus's, the dormant files', the vaults', the records'), and every
      write of a person's registrations or vault is refused (409) under
      that same lock while the tile is paused — the switch's hold included
      — so nothing written during a switch brings a partition back.
      "Holds data" counts vault files, never the empty levels a removal
      leaves (and removals take those levels too). A vault written in
      plain under `--insecure-vault` is sealed when the barrier is first
      initialized, as the global ones are (`migratePartitionVaults`).
  - **Not chosen:** partition rows in today's `data/cron-jobs.json` /
    `bus-subscriptions.json` with an owner field (an older xbind would fire
    them as the tile's; backups would carry them); a `?partition=` for
    admins (PD-07: an admin never reaches a person's vault or
    registrations); cold-starting a partition for a shared bus's events
    (every shared publish would start every subscriber's partition);
    storing `dormant` in `partition.json` (liveness is asked at every use,
    PD-20); changing `routeTarget`'s signature (netfn.go is F15's file this
    wave).
- **D149 — Partitioned tiles, F7a: terminals and agent sessions
  (2026-09-30).** Implements PD-09, PD-22 and the terminal half of PD-10
  of plans/partitions/90-decisions.md; the design is
  plans/partitions/06-terminals-ops.md §1-§3. docs/partitions.md
  §Terminals and agent sessions, docs/protocol.md (§/ws/term,
  `/term/sessions*`, `/agent/history`, `GET /status`, `GET /sandboxes`),
  docs/overview/09-terminals.md.
  - **Chosen.**
    - **No token change; the broker answers the session's partition at
      open.** `term.Manager` gains three hooks boot installs
      (internal/boot/partitionterm.go): `SessionPartition` = the broker's
      `TermPartition(p, path, dep)` (the owning tile via `Reg.Resolve` —
      answered for an unpartitioned tile too, as `Tile` alone;
      `addressedPartition` for the opener; the person's partition id from
      `PartitionIdent`, which mints the uid at their first partition; a
      non-primary target → `global`, PD-17; no person and no global →
      `NoAPI`; a switch of the tile running → `term.ErrPartitionSwitching`,
      409), `TilePartitioned` = `TermTilePartitioned` (recorded mode has
      user, or unreadable), `PersonPartitionKey` (stored uid only, never
      minted). `pickPartition` runs after `pickTarget` on both kinds (and in
      the restart's probe, so a refusal leaves the session running). Nil
      hooks: every session is today's.
    - **What the session keeps** (`sessionPart`, fixed at start): its
      owning tile (every session, partitioned or not — a switch's stop and
      a mode change match sessions by it, so one opened on a sub-path of
      the tile is the tile's); `XBIN_PARTITION` after
      `XBIN_COMPONENT`/`XBIN_DEPLOYMENT` in both env paths; the start
      directory `$HOME` for every session on a partitioned tile (the
      agent's ACP cwd too; its `files.changed` snapshots still watch the
      tile's work tree); the layer key `term-part/<TileKey>/<pkey>` (mapped
      by `layerDir`; a tile's own key never holds `/`), so the one-holder
      lock, VM disk, base stamp and reset are per person; the session
      frame's `partition`, `partitionNote`, `api:false` (PD-10), the row's
      `partition`. A session without a person keeps the tile's own layer.
      On an xbind without `--isolate` the note says the host shell can read
      every partition's data instead of promising separation (the session
      still opens: the same person can read those files from any other
      tile's host shell, so refusing here would hide nothing).
    - **History by partition id**: `data/agent-history/.partitions/<pkey>/
      <TileKey>/` (no user id starts with a dot), merged into the history
      calls through `PersonPartitionKey`, so list, read, delete and resume
      need no route change; a recreated person (new uid) reads none of it.
      The calls take a `term.HistoryScope{Home, ViewAs}` (`HistoryOf(p)`):
      an admin viewing as the person gets their own history only. `?cwd=`
      narrows partition entries to that exact cwd, as own history is. A
      resume stays in its store (`resumeHere`, 409): a partition's entry
      continues only in its person's session on the partitioned tile, an
      own entry only outside one, so a continuation never moves a
      partition's transcript into history that outlives the partition.
    - **Admins (PD-09)**: `adminPass` = admin and (own session, or the
      session's tile not partitioned — at open *or now*, fail closed).
      `mayReattach`, `MayDrive` (every drive route through `drive`/
      `driveOther`, `GET /term/sessions/<id>` included) and the new
      `MayRename` use it; the new `MayKill` keeps the admin pass (`DELETE
      /term/sessions/<id>`; `DELETE /ws/term` keeps `CanTouch`). **View-as**
      (a read-only principal: `UserID` the person, `Impersonator` the
      admin, which `authed` lets through on every GET) is refused on every
      session of a partitioned tile (`viewAsBarred` in `MayDrive` and
      `mayReattach`, PD-08: view-as opens no partition). Names: a person's
      sessions on a partitioned tile (`SessionInfo.Personal()` — in their
      partition or in `global` on a non-primary target, both keyed by
      their partition id) have `name` blanked in `?user=` rows, a view-as
      listing, `GET /status` rows and the admin sandbox list. Events were
      already F2's (`termEventVisible`).
    - **The routes are converted**: the seven `POST /term/sessions*` rows
      left `partitionUnconverted` — the handlers act on the caller's
      person, whose session now opens in that person's partition with its
      own layer.
    - **The switch** (01 §2.5-§2.6): wipe hook `person-terminals`
      (broker/partitionterm.go) on `wipeEverything` only: `stop` ends every
      session of the tile and waits for their teardown (an agent's history
      is saved then), `wipe` asks again (a session that won't end fails the
      switch before anything of the terminals' goes) and deletes
      `.xbin/term-part/<TileKey>/` (confined `removeLayer`, D78) and
      `.partitions/*/<TileKey>/`; the dry run counts; the partition ids map
      to people (`peopleOfPartitionKeys`), who are told. The terminal
      manager reaches the broker through `SetPartitionTerminals`
      (a `PartitionTerminals` interface, held per broker in a `sync.Map`, so
      broker.go's struct is untouched). New sessions are refused while the
      broker's switch hold is on.
    - **No open slips between a stop and a wipe** (internal/term/
      partitionhold.go): every open that passes `pickPartition` counts as
      in flight until its session registers or the open fails
      (`partOpened`), and a stop (`StopTileSessions`,
      `StopPartitionSessions`) waits for the in-flight opens it matches as
      it waits for teardowns. One partition's end holds the partition
      (`HoldPartition(tile, pkey)`: its person's new sessions answer 409
      `ErrPartitionEnding`, checked atomically with the in-flight count)
      from before its stop until its wipe is done — `wipePersonTerminalsOf`
      does, for F7b.
    - **Mode changes without a switch** (an empty tile's manifest decides
      at once): boot's `OnPartitionChange` hook, added after the runner's,
      ends the tile's sessions whose partitioned-at-open flag differs from
      the new recorded mode (`PartitionModeChanged`).
    - **Base images**: `layers.List` walks `.xbin/term-part/<TK>/<pkey>`
      (tree `term-part`, key `<TK>/<pkey>`, an unstamped one pinning the
      legacy base like a tile's), so `Pinned` — and with it the boot's
      base GC and the VM image GC — keeps every base a person layer was
      built on. `vm.ListDisks` lists people's VM disks (kind
      `person-terminal`, key the tile's `TileKey`; boot maps it to the
      tile).
  - **Not chosen:** a per-session partition claim in the terminal token
    (the credential already decides, 02 §3); a term-side hold on the tile
    during a switch (the broker's switch hold covers the whole act and
    can't leak); counting layer bytes in the wipe summary (walking layers
    of GBs on every dry run); `$HOME` only for people (the owner's global
    session in the tile directory would invite shared-code writes too);
    admin rename kept (it is not kill, the one act PD-09 keeps); refusing
    people's terminals on a partitioned tile without `--isolate` (hides
    nothing, see above); holding each person-layer key (`holdLayer`) for a
    partition's end instead of a partition hold (it keeps the layer from
    being mounted but not a new agent session's history from being
    written into the wiped partition); person layers in the boot's
    base-image gate (`CheckBaseImages` stays on `.xbin/term`: one person's
    layer on a missing base refuses that person's session with the reset
    hint rather than the whole boot, and the GC no longer releases their
    bases).
- **D150 — Partitioned tiles, F9: a user partition addresses its own
  global instance, as its person (2026-09-30).** Implements the same-tile
  path of PD-16 and S3 of plans/partitions/90-decisions.md; the design is
  plans/partitions/05-fabric-edges-global.md §6 and 02 §4 (rule 2's
  exception), §6. docs/partitions.md, docs/protocol.md, docs/auth.md.
  - **Chosen.**
    - **The proxy consumes, the broker decides.** The proxy
      (`internal/proxy/globaladdress.go`) takes `?xbin-partition` off the
      request only when the target's recorded mode has user partitions or
      can't be read (then F1's 409 gate holds it), and asks a second
      routing function, `Proxy.RouteGlobal` = `Broker.RouteGlobal`,
      installed by boot beside `Route`. Public ingress to a partitioned
      tile drops it too (`ingressQuery`; ingress reaches global anyway).
      Every other target is untouched: the parameter reaches its backend
      as before (10 §A.6, "consumed for partitioned targets only";
      TestNoPartitionGolden and TestZeroStateRoute unchanged). A separate
      function rather than a new `Route` argument keeps `Route`'s
      signature, boot's converter and every existing Route caller as they
      are.
    - **RouteGlobal = Route's deployment rules, then rule 2's exception**
      (`internal/broker/partitionglobal.go`, its own function, apart from
      routePartition's rule 4): deliveries (02 §5: no person drives them)
      and other tiles are refused before any rule runs; the deployment rules
      apply unchanged (a qualifier, write on a non-primary deployment,
      grants for people); a non-primary deployment and every credential
      without a person (the global instance, the owner token's frames and
      terminals, the root token) get routePartition's own answer — global
      already, a no-op; a person is taken from xbind state only — a user
      partition's instance token's registration, or the frame's, terminal's,
      agent session's or session's person (view-as: the viewed one) —
      checked live (PD-20: exists, enabled, reads the tile); then 404
      `util.ErrNoGlobalInstance` without `global`; else `Partition: global`,
      `CallerPartition: user:<id>` with its pkey, and for the tile's own
      credentials `Attribute{person, live level, role}`.
    - **The clamp:** `reader` for read, `writer` for write and terminal,
      never `admin` (S3's "never the self-call's admin"; terminal can
      already change the code, but admin at global reads as the tile itself
      to code such as the agent's `principal`). The level is read from the
      users store per call, so a level change or losing read applies to the
      next call.
    - **View-as is `reader`** at global, whatever the viewed person's
      level: the read-only gate refuses only non-GET methods, and a
      WebSocket upgrade is a GET.
    - **An admin calling directly keeps their own role** (rule 3) and gets
      no Attribute: identify already names them, and the clamp is about the
      tile's code acting for a person (S3's "never the self-call's admin"),
      not about people. Rule 4 grants a person nothing on `/api/<tile>/` by
      themselves (`grantedRole("", t)`), so every other person in person is
      refused with or without the parameter (`403 user:<id> is not granted
      access to <t>`), as today; their way in is the tile's frame.
    - **Path tickets** are marked on the request context by the server
      (`auth.WithPathTicket` in `servePathTicket`; `auth.ViaPathTicket`) —
      a ticket's principal is indistinguishable from its page's frame — and
      the proxy refuses them F5 (403) before RouteGlobal.
    - **Statuses:** `denyStatus` maps Route/RouteGlobal refusals — 404 for
      `util.ErrNoDeployment` and the new `util.ErrNoGlobalInstance`
      (`<tile> has no global instance`), 400 for a malformed parameter
      (any value but exactly one `global`, an empty one and a repeat
      included), 403 otherwise — the same order of gates as before.
    - **`global-address/1`** is `broker.GlobalAddressFeature`, a constant
      for the features list of `GET /api/xbin/partitions` (06 §6), which
      isn't mounted yet (F7b).
  - **Not chosen:** stripping the parameter on unpartitioned targets (a
    behaviour change for every existing tile, against compat rule 8 and the
    zero-state goldens); attributing (clamping) an admin's own direct calls
    (they hold admin on every tile, rule 3; clamped, the global instance's
    admin surface would answer only the owner token's credentials); opening F5 to readers
    in person (a new direct-call grant, wider than today's rule 4); a
    principal field for path tickets (a new field needs the tilesbx
    principal review and travels everywhere; the context marker reaches
    only the proxy); 404 before the person's liveness check (a person who
    can't read the tile learns nothing about it); passing the parameter
    through on ingress (a global instance would then see it from the
    public, against "the backend never sees it").
- **D151 — Partitioned tiles, F10: cross-tile edges — the person's
  read, consent by workspace policy, the egress ledger (2026-09-30).**
  Implements PD-12, PD-13 (decided: the grant plus the person's read;
  consent a workspace policy, off by default), PD-14 and PD-46 of
  plans/partitions/90-decisions.md; the design is 05 §1-§2 and 06 §6.1.
  - **Chosen.**
    - **The read check stays addressedPartition's** (F2): Route rule 4 and
      the data plane's cross-scope reach both ask it, so one rule decides
      calls, data and events. F10 adds the consent records it asks for and
      the tests of the whole matrix (`TestPartitionEdgeMatrix`, on the
      real identity plane).
    - **Shared resources need no consent.** A resource declared `"shared":
      true | "read"` is one copy at today's keys, no person's data:
      another tile's user partition reaches it on the grant and the
      person's read access on the scope (`reachPartition` settles it
      before the consent is asked), read-only when `"read"`. The policy
      never changes a shared reach, and the prompts, the approval warning,
      `/partitions/edges` and the ledger leave shared resources out alike.
    - **Consent records** (`internal/broker/partitionconsent.go`;
      `partitionConsentHolds` = `consentOrAsk`, filled in
      partitionwire.go): `data/partitions/consents/<uid>.json` `{schema: 1,
      user, uid, edges: {"<Z>→<X>": {at, via}}}`, read by the person's
      stored uid (never minted on a check), re-read when the file's size or
      mtime changes. A file this xbind can't read counts as no consent, is
      never written over, is named on `/alerts` (kind
      `partition-consents`, admins) and answers its person's POST/DELETE
      409. Written only by `POST /partitions/consents` (PersonOnly; the
      policy on, else 409; both tiles partitioned — 409 —, existing — 404
      — and readable by the person — 403; a path holding `→` 400, so every
      key splits at its one arrow), removed by `DELETE` in either setting
      (its answer says `revoked`). Audited; `partitions` op `consent` to
      the person's own sockets.
    - **Prompts:** a refused edge publishes `partitions` op
      `consent-needed {from, to}` and pushes (kind
      `tile.partition-consent`, link `xbin/partitions`, collapse
      `partition-consent:<from>→<to>`) — at most once a day per person and
      edge, only when the caller holds a grant reaching the callee's
      people's data (`edgeGranted`, through `resolveTarget`), and on its
      own goroutine: addressedPartition can run inside the event hub's
      filter, whose lock Publish holds. Both consent ops carry data that
      says `PersonOnly()` (`personEvent`): the server's
      `partitionEventFor` passes them to the person's own sockets only,
      never the callee's frames, terminals or instances. `GET
      /partitions/consents` lists the edges asked about in the last day
      (`asked`).
    - **Revocation:** the next call and reach ask the file again, and the
      caller tile's backend instance of the person is stopped
      (`SetPartitionEdgeStop` = `runner.StopPartition`, wired in boot). No
      volume of another scope is ever bound into a partition (EnvFor hands
      no cross-scope file resource).
    - **A consent names tiles by path, so it goes with them:** the
      `consents` wipe hook on a switch that deletes everything, and
      `PartitionTileChanged` (boot: `Registry.OnPartitionChange`) when a
      partitioned tile goes (deleted, moved: its mode now the zero one) —
      a new tile at the path never inherits what people allowed the old
      code.
    - **The ledger** (`internal/broker/partitionledger.go`;
      `partitionEdgeSeam` = `ledgerEdge`, filled in partitionwire.go): per
      user partition and day, kinds `edge` (a Route call or an authorized
      data reach into the same person's partition of another partitioned
      tile), `provider` (a Route call from a user partition to a tile that
      isn't partitioned — global binds, grants and personal binds alike —
      or to a partitioned tile's deployment beyond its primary, target
      `<tile>+<dep>`: its one global instance, `ledgerTarget`), and
      `bus`/`trigger` for the planes that register those (`ledgerCount`).
      File `data/partitions/<TileKey>/<dep>/<pkey>/ledger.json` (dep = the
      tile's primary, PD-17), `{schema: 1, tile, dep, user, uid, days:
      {day: {kind: {target: n}}}}`, 90 days. Counted in memory under one
      lock that covers the counts only: files are read, and snapshots
      written, outside it (per-ledger write order, never over a newer
      snapshot, never after a switch's wipe took the ledger). Saved when a
      day gains a row, at most once a minute otherwise (an idle ledger's
      counts at the next sweep), and on `Broker.Close`; a saved ledger idle
      for 10 minutes leaves memory. Nothing counted while the tile's switch
      runs. Rows of a person's former incarnation are never shown (the
      uid).
    - **Who reads it (PD-46):** `GET /partitions/ledger` (PersonOnly):
      `rows` — the person's own; with `?tile=`, `totals` for the tile's
      writers, managers and admins — for all but admins every personal
      tile's target is `(a personal tile)`, merged, so a total never names
      a person; `people` — per-person totals — for admins. `GET
      /partitions/edges` (admin): the edges between partitioned tiles,
      granted now or counted in the window, with `people`, `calls` and
      `consented` — the Policies tab's turn-on confirmation and `bx
      doctor`.
    - **The approval warning (S1), both settings:** `PendingGrant.Warning`
      on `GET /grants` (and the admin overview's pending rows) for a
      partitioned tile's request on another partitioned tile or one of its
      per-partition resources (not a shared one): "Z's code — and everyone
      who can change it — will be able to read and write the X data of
      every person who can read X" (policy off) / "… of every person who
      allows it" (on). Shown where grants are approved: the grants element
      (`web/bx-grants.js`), the admin console's binding → grants view, the
      organisations tile's pending approvals, and `bx grants`.
    - **Wipe hooks** (01 §2.6): `consents` (every consent naming the tile,
      as caller or callee) and `ledgers` (the tile's people's ledgers,
      counted and on disk), on a switch that deletes everything only.
      Metadata hooks (`wipeHook.meta`): they run after every data store's,
      whatever the order of registration, so a data store's failure leaves
      them whole. A dry run deletes and counts nothing but checks what the
      real one would: a consent file this xbind can't read fails the switch
      — dry run included — only when its bytes may name the tile
      (`namesTile`); an unrelated person's never blocks one. Neither is a
      "holds data" store: 01 §2.2 names neither.
    - **Route classes:** `GET/POST/DELETE /partitions/consents` and `GET
      /partitions/ledger` PersonOnly (the first real PersonOnly rows);
      `GET /partitions/edges` GlobalOnlyRefused; all five PrimaryOnly in
      the deployments table (tile credentials never reach them).
    - **CLI:** `bx partition consent <from> <to> [--revoke]`, `consent
      ls`, `ledger [<tile>] [--days n]` (exit 6 against an older xbind,
      naming the route it lacks); `--revoke` says when there was nothing
      to take back; `bx doctor` lists the granted edges for review and
      prints nothing when there are none.
    - **UI:** the admin Policies tab's partitionConsent confirmation lists
      each edge with its people and consents (nothing against an older
      xbind); the approval warning in the admin console and the
      organisations tile. The `adminPolicies` harness pass seeds a real
      edge (three partitioned org:devs tiles, one approved grant, one
      pending) and asserts the preview, both warnings, and a stubbed answer
      with totals.
  - **Not chosen:** counting a data reach at `reachRes` (before the grant
    is checked: a tile without the grant inflated the ledger — moved to
    `allowAt`); prompting for edges no grant allows (tile code could make
    people consent ahead of an admin's approval); prompting synchronously
    (a filter-side refusal would deadlock the hub); writing the ledger per
    call (a busy edge would fsync per request); making consents or ledgers
    "holds data" stores (01 §2.2); stopping every running instance when
    the policy turns on (calls are refused from the next one; see open
    ends); asking consent for shared resources (they aren't a person's
    data, and the warning, prompts and preview couldn't show them — turning
    the policy on would silently break such tiles); dropping the person's
    read check for shared resources too (their partition of Z would show
    data of a tile the person can't open; the policy never changes it, so
    nothing is silently broken); failing a switch on any consent file this
    xbind can't read (one stray file would block every switch); a stable
    tile identity in each consent (none exists across delete and re-create
    by the owner; the registry's change hook drops them instead).
- **D152 — Partitioned tiles, F15: bind types — global binds under
  today's authority, personal binds in xbind state (2026-09-30).**
  Implements PD-16 and PD-54 of plans/partitions/90-decisions.md (owner
  ruling 2026-09-29: today's bind authority, partitioned or not); the
  design is plans/partitions/05-fabric-edges-global.md §3.
  - **Chosen.**
    - **Global binds are today's.** `apiBindingSet`, the delegated paths
      (D26/D33 org admins, D88 personal owners) and `grantMutation` are
      unchanged for a partitioned requester: the people who can bind a
      provider into every partition could change the shared tile's code
      anyway (PD-23) — except a provider org's admin (D33), flagged for
      the owner below. The one addition is a bind-time refusal (05 §1): a
      **new** ref of an **unpartitioned** requester's http slot to a tile
      whose recorded mode has user partitions and **no global** instance is
      409 (`bindConflict`; Route would refuse every call). A ref the slot
      already holds is not judged again, so a multi slot holding a ref to a
      tile that later partitioned stays editable (never break users). The
      bind options mark such a provider `blocked` ("partitioned, no global
      instance"), so pickers grey it out instead of ending in the 409.
    - **Personal binds are xbind state keyed by uid.**
      `data/partitions/binds/<uid>.json` = `{schema: 1, user, uid, binds:
      [{id, requester, slot, provider, at}]}` (0600, atomic writes). A
      person's record is read only through their **stored** uid (never
      minted on a read). A record whose uid isn't its person's stored one —
      a deleted person's, or an earlier incarnation's — is **dead**: it
      applies to no one, is never listed, holds no tile's data, is neither
      counted nor named by a switch (its rows still go in the wipe) and
      matches no delete, a person's or an admin's (PD-43: a recreated id
      inherits nothing, not even a view of its predecessor's rows). Older
      binaries never read the records (PD-46).
    - **Who creates one.** `POST /partitions/binds` is **PersonOnly** (the
      partition class): a person's own session, app or device — never a
      tile principal, view-as or the root token — and **never an admin**
      (PD-54: an admin's bind is always global, even of a tile they own
      personally; 403 pointing at `POST /bindings`). The handler checks, in
      this order so that existence is never told to someone who may not
      know it: the caller owns the provider (`Owner(provider) ==
      user:<id>`, 403), then it exists (404); the caller can read the
      requester (`personLive`, 403), then it exists (404); the requester is
      partitioned and neither paused nor mid-switch, the slot is a multi
      http slot, the provider isn't partitioned and provides the slot's
      service as a plain provider (no `#instance`, v1), and isn't already
      bound on the slot for everyone (409); the ceiling allows the edge
      (403). The checks and the write run under the personal binds' lock,
      so a switch's wipe or a transfer can't land between them. The uid is
      minted at the person's first bind.
    - **Seen by one partition.** F4's `partitionIfaceEnvSeam` appends the
      person's live binds to each multi slot's `XBIN_IFACE_<SLOT>` JSON as
      `{provider, url, service, personal: true}` after the global rows
      (user partitions on the primary only; the global instance's env never
      goes through the seam). The document meta: the server asks an
      optional `PartitionInterfacesPolicy` (`docInterfaces`, the only
      static.go change) for a partitioned tile's primary document viewed in
      a person's own user partition (as `partitionHead` resolves it); every
      other document — a non-primary deployment's included — gets
      `Interfaces` exactly. `IfaceEndpoint.Personal` (omitempty) marks the
      rows.
    - **The call filter is live.** F2's `personalBindGrant` is
      `personalBindRole(from, callerPart, target)`: for a user partition
      only, its person's live record, a bind of `from` to `target` that
      still holds (`personalBindCheck`: requester partitioned, multi http
      slot, provider owned by the person, unpartitioned, providing the
      service, ceiling), granting that provide's role (first by slot).
      Route asks it only after `resolveTarget`'s `grantedRole` refused
      (`NotGrantedError`) a user partition's cross-tile call, so
      resolveTarget stays the single evaluation point
      (`TestEdgePolicyCallers`); together they are 05 §3's
      `grantedRoleIn`. Every other caller — another person's partition,
      the global instance, an unpartitioned tile, a guessed URL — keeps
      today's refusal, byte for byte.
    - **One partition restarts.** A create or delete restarts only that
      person's partition instance of the requester (`SetPartitionRestart` ←
      `Runner.StopPartition`, boot; the next request starts it with the new
      env; `TestPersonalBindRestartWired` guards the wiring) and publishes
      `grants` for the requester stamped with the person's partition, so
      only their sockets and frames reload.
    - **Lifecycle.** A provider transfer drops the rows of people who no
      longer own it and restarts their partitions
      (`executeTransferEffects` → `personalBindsProviderMoved`); a deleted
      person's record goes through `PersonalBindsUserDeleted(userID, uid)`
      (the users store's delete hook); a tile created at a bind's
      requester's or provider's path (`assignOwner`, whoever creates it)
      drops those binds — a person's consent names the removed tile, never
      the new one (D82's spirit; an admin skips `pathLeftovers`); the store
      `personal-binds` makes a tile with live personal binds hold data
      (01 §2.2) and its wipe hook removes them on a switch between user
      partitions and unpartitioned, counted with the registrations and
      naming their people — adding or removing `global` keeps them (H1).
    - **Admins list and remove, never create.** `GET` answers a person's
      own rows, or every living person's for an admin (an admin person,
      the root token, or a tile holding `xbin:admin` — the admin console),
      each with `live`/`why`; `DELETE {id}` or `{requester, slot,
      provider[, user]}` by the person or an admin. The admin console's
      wiring view labels a partitioned tile's bindings *global* and lists
      its personal binds with a remove button.
  - **Rejected.**
    - Refusing the delegated paths (org admins, personal owners) for a
      partitioned requester (the plan's rev. 2): the owner ruled today's
      authority.
    - An admin's personal bind of a tile they own (the first F15 cut read
      PD-54's "never create" as "never for someone else"): PD-54 says
      never; an admin binds globally.
    - A `grantedRoleIn` replacing `grantedRole` inside resolveTarget: it
      would give the edge policy a second evaluation point; the personal
      half rides F2's seam instead.
    - Personal binds in the workspace manifest (PD-54: a person's wiring is
      their metadata; older binaries would read it as global).
    - Judging every ref of a slot on each edit (the 409 on refs bound
      before their provider partitioned would block unrelated edits).
    - Listing a removed requester's personal binds among `pathLeftovers`
      (D82's refusal list): a removed partitioned tile already leaves its
      mode record there, and admins skip the list; dropping the binds when
      a tile takes the path covers everyone.
- **D153 — Partitioned tiles, F14: the shell's marker (design A)
  and the pending card's Keep / Switch (2026-09-30).** Implements PD-53 as
  the owner ruled it (H2: A, the teal half-split disc), the shell half of
  PD-47's scaffold surfaces and 06 §12.3's card overlay, deciding through
  F13a's route (D145). Design: plans/partitions/06-terminals-ops.md
  §12.2-§12.3, 01 §2.4, §2.8; docs/partitions.md.
  - **Chosen.**
    - **One lit-free module owns the words and the decision**
      (`workspace-template/shell/partition-mode.js`): what a row's
      `partition` reads as (`partitionView`: state, recorded mode,
      request, pending, declined, note), the marker's tooltip, the pending
      card's text (the `/alerts` `partition-switch` message of the same
      R → Q — xbind's words, as the banner shows them, without its
      `bx partition switch|keep` hint — else the same facts from the row),
      who decides (from the row's `owner`, as xbind's in-frame page says
      it), the POST bodies (from/to exactly as the row showed them,
      `null` for unpartitioned, so a changed request is xbind's 409, never
      a blind decision), the typed confirmation's `<bx-dialog>` spec from
      the dry run's answer, what the dialog's answer asks for, which
      refusals close it, and the answers after a decision (xbind's
      `deletes`, `eraseError` and `archiver`, as `bx` prints them).
      `hack/partition-mode.test.mjs` runs it in node; the registry's
      `TestShellSwitchWords` pins its switch words and mode names to
      `registry.SwitchDeletes` / `PartitionSpec.String`.
    - **The marker follows R, not the request**: shown whenever the
      recorded mode has user partitions (partitioned, pending, invalid
      alike) — a pending switch doesn't change it (06 §12.2). An 8px SVG
      ring with its left half filled, `role="img"`, the tooltip as
      `title` and `aria-label`, `cursor: default`, no border, no hover
      rule. `shell-kit.js partitionMark(c)` returns `null` for every other
      row, so the head falls back to the runtime dot and the row draws
      nothing (an xbind without partitions, a tile that never asked).
    - **A window on another deployment has no marker** (01 §2.8: a
      non-primary deployment never runs people's partitions; its one
      instance is its writers' shared one, `global` when its code asks):
      the runtime dot, and beside the `+name` tag a `shared` chip whose
      tooltip says so. The sidebar row (the tile) keeps its marker.
    - **The hue**: `--bx-part-c: var(--bx-part, #3fb5a3)` on the elements'
      hosts, and, where `light-dark()` is supported, `var(--bx-part,
      light-dark(#1f8778, #3fb5a3))` — the workspace theme is dark
      (`color-scheme: dark`), a theme that makes the page light gets the
      deeper teal (≥3:1 on white). No token is added to `theme.css`
      (setting it there would pin the dark value for every scheme).
    - **The overlay** (`bx-canvas.js _partOverlay`) sits over the card
      body only — the head stays live (menu, terminal, close, drag) — and
      only on a window showing the primary (another deployment isn't
      paused). It shows the request's words, the tile's `partitionNote`
      attributed to it ("<t> says: …", from the row's new `partition.note`
      — the box covers the in-frame page's copy of it), and either the
      buttons, for the viewer `canAdminTile` passes (the ⚙ rule: workspace
      admin, the tile's user owner, an admin of its owning org — the same
      set as `mayManageTile`), or who decides. xbind still judges every
      call (a view-as session, a race: the refusal shows in the card).
      Calls are the person's raw `fetch` (the shell tile's `xbin.fetch`
      would be a tile principal, which never decides). Keep posts at once.
      Switch… posts the dry run first, then opens the typed confirmation
      (with the note; a switch that deletes nothing — `"global"` comes —
      shows no counts, says "Nothing is deleted", and its button isn't a
      danger one); a wrong path, or the C12 "Switch anyway" box unticked
      while the dry run named sandbox managers that lack `partitions`,
      re-asks in the dialog without a call; the managers' 409 and a 500
      part-way failure re-open the dialog with xbind's error and the typed
      path kept; any other 409 (the request changed or was decided
      meanwhile, a switch already runs, the data went but the mode wasn't
      recorded) closes it, shows the error on the card and reloads.
    - **A card's decision state lives while its request is pending**:
      kept under the request's R → Q, pruned whenever `/components`
      changes and the row no longer shows that request pending
      (`pruneDecisions`: decided, withdrawn, changed, gone), and never set
      once it doesn't — so a request that closes and reopens with the same
      R → Q (keep, withdraw, ask again) shows the buttons again, not the
      last answer. After a decision the shell reloads `/components` and
      `/alerts` at once (F13a's `reload` event does it too).
    - **A row's `partition.note`** (the one wire change): the code's
      `partitionNote`, trimmed, only while the request is pending — what
      the in-frame switch page shows, so a client that draws over the
      frame can show it too; sandbox-authored, so a client shows it as
      text, attributed to the tile. Absent otherwise: every other row is
      byte-identical.
  - **Not chosen:** a second "partition chip" element beside the marker
    on the primary's window (design A's case is "no new element in the
    head"); the shell's own `_dialogs` stack for the confirmation
    (bx-shell.js sits at its size budget; bx-canvas renders its
    `<bx-dialog>` beside its ⇈ menu — the backdrop still covers the whole
    page); Switch… on a *declined* tile (06 §12.3 asks for the overlay on
    pending tiles; `bx`, the admin section and `/xbin/partitions` offer
    it); a light theme in `theme.css`; keying a card's state by a request
    identity on the wire (`request.since`) — pruning on the row does it
    without a wire change; anchoring the box away from the in-frame note
    instead of showing the note (a short card would still cover it).
- **D154 — Partitioned tiles, the I1 smoke slice (run as "S1"): an
  end-to-end smoke on a real isolated xbind; a switch mounts the tile's own
  volumes again (2026-09-30).**
  - **What it smokes:**
    - 00 §3's guarantees G1 (no route hands one person's partition data to
      another) and G2 (an admin reaches their own partition, never a
      person's; PD-08 view-as).
    - 02 §2-§4 and §10: routing, rule 2 (self-calls), rule 4 (a
      partitioned tile's call to another reaches the same person's
      partition, PD-13), the identify headers, `XBIN_PARTITION`, the
      document meta, and instance tokens bound to their generation and the
      person's uid.
    - 03 §A.4-§A.6: per-partition logs, PD-18 caps, the idle stop.
    - 03 §B: namespaces, `shared`, `"read"`, PD-17 (people's partitions on
      the primary only) and §B.9 (no seeding between partitions).
    - 03 §E: a person deleted and created again gets a new uid and a fresh
      partition.
    - 05 §5: ingress and other tiles reach global.
    - 01 §2.3-§2.6: auto, pending, keep, switch, the wipe.
  - **Tests:** `test/isolated/partitions_smoke_test.go` (dev box only: it
    skips in CI, which has no rootfs) and
    `internal/broker/partitionswitch_remount_test.go` (CI).
  - **Chosen.**
    - **One fixture: four tiles, five people.** Every tile runs one Go
      probe over the SDK.
      - The probe echoes `XBIN_PARTITION`, `xbin.Partition()`,
        `xbin.PartitionUser()`, `xbin.Caller(r)`, its instance token and a
        boot id per process.
      - It reads, writes and lists a per-partition kv (`kv`), a shared kv
        (`board`, `"shared": true`), a per-partition filesystem (`files`)
        and a read-shared filesystem (`pub`, `"shared": "read"`).
      - It logs each value it stores, returns its sandbox's
        `/proc/self/mountinfo`, and relays a call through `xbin.Client`.
      - The tiles: `apps/pt` (`["user", "global"]`, with a
        `partitionNote`); `apps/pt2` (`["user"]`, granted writer on
        `apps/pt`); `apps/pcall` (unpartitioned, granted writer on
        `apps/pt`); `apps/plain` (unpartitioned, holds data, later asks to
        partition).
      - The people: alice, bob and dave are users who read `apps/*`; carol
        and erin are workspace admins; the owner token is the fifth
        credential.
    - **Data is named after whoever stored it.** alice stores
      `alice-secret`, `alice-note`, and a key and a file named `alice-only`;
      global stores `global-only`. So any leak shows by name, and each
      listing is checked for its exact contents.
    - **Every attempt has an exact answer.** A route that reaches a person
      returns that person's own value, and never global's. A route that
      reaches global returns global's value. A refusal must have its status
      and its reason. The only enumerated answers are the
      `?xbin-partition=` rows, which accept today's answer or F9's (see the
      notes).
    - **People reach a tile's API through its frame**, as a browser does. A
      user's own session is no API principal of a tile unless they are an
      admin.
    - **The fix: a switch mounts main's volumes again, on every way out.**
      F13a's switch wipes main's namespace (user ↔ unpartitioned, or
      removing `"global"`), which unmounts and removes the tile's volumes at
      today's keys. Only `MountEncrypted` mounts main's volumes (at
      provision, unseal, a `cap:containers` change, a restore). So the
      instance at today's keys stayed held until xbind restarted or the
      workspace next changed. That instance is the unpartitioned one, or
      global; a person's shared volumes live there too.
      - `runSwitch` now defers `MountEncrypted` right before the wipe hooks
        run, when the switch owns main's data and deletes some of it.
      - So a wipe that stops part-way (500) and a decision that isn't
        recorded (409) remount too, still under the namespace holds.
    - **The idle stop runs only on request** (`XBIN_SMOKE_REAP=1`,
      `TestPartitionsSmokeReap`). `partitionIdleReap` is a 10-minute
      constant with no knob, and a knob would be new daemon surface for a
      test, so the case waits it out (≈ 11 min).
  - **Not chosen:**
    - An env knob for the idle stop: new surface, and the runner's unit
      test already drives the clock.
    - Running under `test/` without isolation: people's partitions answer
      503 there (PD-19).
    - Building a rootfs in CI to run the smoke there: the CI budget. The
      unit test carries the fix instead.

- **D155 — Partitioned tiles, F6: partition mail — xbind-owned inboxes,
  the sender table, the doorbell (2026-09-30).** Implements PD-15 of
  plans/partitions/90-decisions.md; the design is
  plans/partitions/04-shared-resources.md §3.
  - **Chosen.**
    - **One store per tile and deployment**,
      `data/partitions/<TileKey>/<dep>/mail.db` (bbolt, 0600, xbind's own
      data: never in a sandbox, D118), beside the partitions' directories:
      a bucket per addressee (`global`, each person's pkey), a `meta`
      bucket naming the tile (a TileKey is a hash), each inbox's person and
      uid, and its expired and undeliverable counts. Each call opens the
      store and closes it under its **tile's** lock (every deployment's
      store of a tile shares it; one tile's mail never waits on another's)
      — a switch's wipe or a reset never meets an open file, and a restart
      loses nothing (bbolt fsyncs each commit). A call that changes nothing
      (a read, a count, a doorbell check, a refused send) rolls its write
      transaction back: no commit, no fsync.
    - **Items are sealed like kv values** (`encodeKV`) under a label naming
      the tile, deployment and inbox, so an item moved to another inbox
      fails to open. A value is `expiry (8 bytes) | len | sender's inbox
      name | sealed item`: expiry, counts, sweeps and each sender's share
      need no vault. Ids are 12 bytes — a per-store monotonic time and 4
      random bytes — hex on the wire: they sort in arrival order (`after`
      paging) and never repeat across a wipe.
    - **Who sends, from the credential alone.** The global instance is its
      backend's instance token on the primary (`addressedPartition` →
      global); a person's partition is any tile credential the partition
      gate stamped with `user:<id>` (backend, frames, terminals, agent
      sessions). Everyone else is 403, the global instance's frames (the
      owner token's) and root terminals included: sending **as** global
      and reading global's inbox are its backend's alone. A person's
      partition mails `global` only — itself and other people 403. The
      global instance mails a person only while `personLive` holds; every
      other case answers the same 404 `no such person here: user:<id>`,
      which says nothing about why. A `from` field in the body is accepted
      and ignored.
    - **The caller's own inbox only.** `GET` and `ack` take the inbox from
      the credential; no parameter names another. An ack of an id that
      isn't in the caller's inbox does nothing.
    - **Limits** as 04 §3: topic + data ≤ 1 MiB (413), an inbox ≤ 1000
      items and ≤ 64 MiB of stored (sealed) bytes (507 to the sender),
      `ttl` in whole seconds, 1 to 30 days (default 7; 400 outside), a topic
      ≤ 256 bytes without control characters. **The global inbox gives each
      sender a share** — ≤ 100 items and ≤ 8 MiB waiting (global itself, and
      each person) — so one reader of the tile (a frame can mail) can't
      fill it for everyone: 507 goes to that sender alone. A GET page stops
      at its limit or past ~8 MiB of data; `more` says others wait. Every
      mail call answers 409 while the tile is paused (asked under the lock
      the switch's wipe takes, so nothing mailed after the wipe survives
      it); 503 while the vault is sealed for a send and for a read that
      would return items — an ack never opens an item and works sealed.
    - **An item that can't be opened** (sealed under another key or label,
      damaged — anything but a sealed vault) is dropped at the read that
      meets it and counted as `undeliverable`, logged by id: one bad item
      never wedges the items behind it (a kv value's failure is its own
      key's; mail's must not be the whole inbox's).
    - **The doorbell rides the bus's delivery path** (`SetBusDispatch`'s
      proxy dispatch): `Principal{xbin/mail, Via: mail, Role: writer,
      Partition: <addressee>}` POSTs `{partition, pending}` to the
      manifest's `partitionMail` — Route's delivery rule, so a person's
      stopped partition is a `StartMail` background start (the runner's
      admission and 6-a-minute mail rate). It rings at once for a new item
      (and starts the backoff over), and then after 1 min, 5 min, 30 min,
      2 h and every 6 h while items remain. **The addressee's start**
      (`notePartitionStart`, whatever started it) rings too — unless a
      ring was in flight at the start or came after it, which is how the
      doorbell's own ring cold-starts a stopped partition — and never
      resets the backoff: an item a handler leaves unacked wakes its
      partition less and less often, never every ~36 min for its ttl, and a
      handler that crashes on an item doesn't loop (S20). A person's inbox
      rings only while the tile is enabled, partitioned with a global
      instance, not paused, on its primary, the person live with the
      inbox's incarnation (pkey), and their `partition.json` has
      `lastStarted` — mail never makes a person's first instance (S1,
      S20). Boot rings every inbox holding items; an hourly sweep drops
      expired items and rings inboxes that lost their bell (one tile's
      lock at a time).
    - **Its data goes with the partition.** "partition-mail" is a
      holds-data store (any item, expired ones too until dropped: the rule
      leans toward asking) with the wipe hook of its name: a switch between
      user partitions and unpartitioned removes the tile's stores (their
      bytes counted in the summary's; a person named only when their
      partition exists); removing `global` deletes the global inbox only,
      people's stay with their partitions (H1); adding it deletes nothing.
      `dropPartition` (a person's reset or purge, and the orphan sweep)
      drops that person's inbox; `PartitionUserDeleted` drops a deleted
      person's inboxes at once (mail is transient). A removed tile's store
      is listed by `pathLeftovers`, removed when a tile is created at its
      path, and otherwise removed by the sweep once its items expired.
    - **The ledger's `trigger` kind** is counted from the mail itself: an
      optional `source` on the global instance's mail to a person names
      where a private trigger's event came from (08 §5), and counts one
      `trigger` row for that person — never the content. Anything else
      carrying `source` counts nothing.
    - **Data plane, not audited**: `/partitions/mail…` joins prefs, kv,
      blob and bus in `auditable`'s exclusions.
    - **The SDK and bx.** `InboxPage` answers `MailPage{Items, More}`
      (`Inbox` is its items); `bx partition mail ls|ack` is the client side
      of the routes for a person's terminal (exit 6 on an older xbind).
  - **Rejected.** Keeping stores open (a wipe or reset would race an open
    file); one lock for the workspace's mail (one tile's traffic delayed
    every other's); addressed bus events (04's rev-2 design, S4, C21); a
    doorbell dispatcher of its own wired at boot (the bus's path is the
    proxy's already); refusing a body's `from` with 400 (04 §Tests asks that
    it be stamped whatever the body says); deleting a removed tile's mail at
    once (a tile briefly unregistered would lose it); counting every mail to
    a person as `trigger` (most mail isn't a trigger's); a start that
    resets the backoff, or rings even when the doorbell's own ring reached
    it (the noisy-neighbour loop above); letting only a partition's backend
    mail global (04 §3 names frames among a partition's senders — the
    per-sender share bounds them instead); a per-sender rate limit (the
    share bounds what a sender holds, the runner bounds starts); failing a
    whole read on one item that can't open (it wedged the inbox until the
    item expired); a mail count of its own in the switch summary (the
    summary's shape is F13a's; mail is in its bytes).
- **D156 — Operating people's partitions: a per-audience listing, one
  partition's stop/reset/purge, logs that are the person's, people hooks
  and held credentials (2026-09-30).** Implements plans/partitions 06
  §4-§10 (PD-07's a+, PD-23's warnings, PD-24, PD-26, PD-46).
  docs/partitions.md §Operating people's partitions, docs/protocol.md
  (`/partitions`, `/partitions/{stop,reset,purge,share-log,
  credential-confirm,reviewed}`, `/logs`, `/tile-status`, `/backends`,
  `/users` rows, `/invite/redeem`, `/auth-settings`, `/login/sso/callback`,
  `/deployments/protect`, `/bindings`, `/lifecycle`, the `partitions`
  event), docs/bx.md.
  - **Chosen.**
    - **The listing answers per audience, one route.** `GET /partitions`
      is PartitionNeutral: the handler — not the class — decides what
      each caller sees (PD-46), and gives a tile's own credentials the
      tile-level fields only, so tile code never reads people's metadata.
      The admin tile's frame driven by an admin reads as that admin
      (AdminFrameDriver, F13a's precedent).
    - **Acts are a person's.** stop/reset/purge judge the person
      (`partitionActor`: a person's own session, app or device, the root
      token, or the admin frame's driver) and are GlobalOnlyRefused /
      PrimaryOnly in the class tables; share-log and credential-confirm
      are PersonOnly.
    - **One partition's deletion is one function, and deletes only the
      tile's own** (`dropOnePartition`, partitiondrop.go): F7a's hold is
      taken first and kept to the end (no session opens meanwhile), the
      person's terminals end and their layers and history go, then under
      the **tile's own** backup lock — in every deployment the partition
      has anything in — its namespaces (only when the tile roots its
      scope: a partitioned member uses no scope resources, 01 §3 rule 1),
      F5's records, registrations and vault, its log directory, other
      planes' stores (`partitionDropHooks`: F6's mail) — and last the
      tile's own `part:<tile>/<dep>/<pkey>` keys are erased (11 §3's
      order), never the scope root's. The reset, the purge and the orphan
      sweep share it. A reset holds the partition's starts from before its
      stop until the drop is done (`holdPartitionDrop`).
    - **Acts check rights before they say anything:** a tile the actor
      can't read answers 404 (unless they name their own partition of it),
      someone else's partition 403, and only then the mode (409) and the
      partition (404 — none held, and no one told).
    - **Logs: the credential decides, for as long as it streams.** The
      person's own at any level (their data); a named person's (`?user=`)
      only for an admin or manager in their own session while shared (the
      share lives in the partition's directory, so a reset or switch takes
      it) — a follow asks again every 2 s and ends when the answer changes;
      the global instance's with F9's `?xbin-partition=global` under
      today's rule, never for a partition's own credential; `?partition=`
      400. The answer names the partition (`X-XBin-Partition`).
    - **People hooks where the users plane already reports.** The delete
      handler reads the uid before the store forgets it and calls
      `PartitionPersonDeleted` (stop+revoke, orphan namespaces and records,
      personal binds, consents, notices, and the move of homes and agent
      history for a partition holder — a rename of the directory entry,
      never a walk of what a sandbox wrote, D78). Disabled / lost read:
      `usersEvent` — which every users-plane mutation calls — asks
      `PartitionPeopleChanged` synchronously, so the instance is stopped
      before the answer; boot also runs it on every hub `users` event (the
      SSO and device planes publish those directly).
    - **Held credentials live beside the person's notices, and fail
      closed** (`data/partitions/people/<uid>.json`, keyed by the
      incarnation). A held link is minted held (`CreateHeldInvite`: its
      stored hash carries `held:`, which no token's hash matches — an xbind
      without the gate, an older one after a downgrade, never redeems it);
      only a held link asks the gate, which lets it through once its hold's
      24 h passed and refuses it in every other case (waiting, no gate, a
      notices file it can't read, no hold naming it). A hold that can't be
      written revokes the link it would hold (500). A decision and the
      24 h lapse run under one lock, the store change first; a refusal is
      honoured while the credential is unused (even past 24 h); a link no
      longer pending (redeemed, replaced, expired) answers 409
      `already-effective` — change the password, sign out everywhere. A
      held password is hashed at once; nothing plaintext is kept.
    - **A new SSO provider is a credential** for every partition holder
      bound by email (PD-07, 10's "rebind SSO" row): audited naming them,
      each told; with the policy on their SSO sign-ins through it are held
      (the callback asks the store's SSO gate: `sso_err=held`) until they
      allow it or 24 h pass, and Refuse unbinds their email. Only the
      provider's identity counts (kind, issuer, client id). The SSO gate
      fails open on an unreadable notices file (it is asked for every SSO
      sign-in; failing closed would lock holders out after a downgrade).
    - **Reviewed code only** (PD-23): a per-tile admin switch beside the
      mode record (`reviewed.json`, only while on; unreadable counts as
      on). On needs the primary and every bound non-partitioned provider
      protected (D127m); while on the deployments plane asks
      `ReviewedOnlyRequires` before any unprotect (409, kind policy), and
      `validateBinding` refuses binding an unprotected provider in (409); a
      gap that opens otherwise is a trust warning. It binds only while the
      tile is partitioned.
    - **Notices** (a switch's wipe via F13a's `partitionNotice`, an
      admin's reset, credentials) are kept per incarnation (50, 90 days)
      and published as the `partitions` op `notice` to the person's own
      sockets only; op `mode` goes to the tile's readers. Both are gated
      by the event data's `VisibleTo` (server.go's event filter now asks
      any event whose data names its audience, as prefs did).
    - **Trust warnings** (06 §4) are computed, not stored: non-admin
      people with write level on a partitioned tile or a bound
      non-partitioned provider, while the deployments plane says saves
      reach its primary (live reload on the primary). The code writers are
      reused 15 s (dropped on any users change); the panel adds each
      primary's last code move (the deploy log's newest attempt).
    - **The listing builds only what the caller sees** — a person's own
      directory, totals from records, in-memory registrations and bytes
      measured at most once a minute; untracked files (a confined git in
      each tile's own repository, D78) only on `?untracked=1`.
    - **A removed tile's mode record** goes at the sweep once none of its
      partitions, namespaces or data is left (a tile that comes back with
      data keeps finding its recorded mode); an offloaded tile isn't
      removed: its record stays for the restore.
  - **Not chosen:** `?partition=` for admins (PD-07: no read path);
    per-person log files readable by managers without a share; a
    credential hold inside users.json (an older xbind would drop it and
    activate the credential); a ledger line for a credential notice (the
    ledger is per tile-partition; the audit line and the notice carry it);
    listing the global instance's log to a person's partition.
- **D157 — Partitioned tiles, F17b: a person's partition is archived
  under its own `part:` key, restored only into that person's partition,
  and crypto-erased with its data (2026-09-30).** Implements PD-25 (owner
  ruling: per-partition archives as ciphertext under per-partition
  subkeys) and the partition half of PD-56 and PD-26 of
  plans/partitions/90-decisions.md; the design is
  plans/partitions/11-backup-encryption.md §2-§4. Builds on the sealed
  backups decision (F17a).
  - **Chosen.**
    - **One archive object per partition, one key per partition.** A
      backup of tile T writes, after T's main archive (which never names
      them), `.partitions.<TileKey>.<dep>.<pkey>` for each person's
      partition T has on disk — the namespaces of the scope T roots and
      T's own record directories, so a partition that only keeps a vault
      or registrations has archives too — sealed under
      `part:<TileKey>/<dep>/<pkey>` (F4's `partitionBackupSubject`), the
      key's `tile` T. Contents: the namespace's data in the `data/`
      layout (its non-shared resources; kv decoded, each volume ever
      written read through its view, held with `resenc.Hold` and the kv
      file with `usePartitionKV` so the idle unmount/close leaves them),
      then `partition/partition.json`, `partition/ns.json`,
      `partition/vault.json` (the vault file as on disk: values still
      barrier-sealed) and `partition/registrations/<file>`. Manifest:
      schema 3, kind `partition`, `partition: {id, user, uid,
      deployment}`. A partition an act holds (reset, restore, removal) or
      that can't be read is reported (`partitions.failed`, and `bx backup`
      exits 1) and the others still written; the main backup has already
      succeeded. A volume the backup mounted itself is expired at once
      (`resenc.Expire`): the next idle pass unmounts it rather than keep
      every person's volume mounted for the idle hour after each backup.
    - **Only sealed.** The plaintext-vault mode archives no partition
      (`partitions.skipped`): an archive no key erases would hand a
      person's data to the archiver for good (G1).
    - **S15 is structural, and pinned.** The main manifest's cron and bus
      rows come from today's maps (`cronJobsFor`, `forComponent`), which
      people's rows never join (F5: `cr.part`, `bs.part`); the data archive
      is main's namespace at today's keys. `TestPartitionArchiveRoundTrip`
      greps the main and data archives for every person's data, rows,
      partition ids, `partition.json` and vault values.
    - **Restore rules (11 §4).** `POST /partitions/restore {tile, user?,
      partitionId?, version?, confirm, to?, dryRun?}`, `GET
      /partitions/backups`: the person in their own session, app or device
      (who must read the tile), or an admin (the root token, the admin
      tile's frame under their login); every tile principal — a
      partition's own code, frames, terminals, agent sessions — and
      view-as are refused. A person names no partition id but their own
      current one (403 before the archiver is asked, naming nobody: no
      pid→person oracle, PD-46); an admin names any. The archive must be sealed, under the backup
      key whose subject is that partition's archive key's
      (`part:<TileKey>/<primary>/<partitionId>`), with a partition
      manifest of the tile whose user and uid hash to its id: an archiver
      can neither forge one nor serve one person's (or the tile's) archive
      as another's. Same uid: after `confirm: "<tile> user:<id>"`. An
      earlier holder's uid: an admin with `to: "<id>"` (the same id), the
      person told; a person 403, without `to` 400. Never into another id.
      409 while the tile isn't partitioned now or is paused. The restore
      replaces the partition (its instance stopped; the tile's backup lock
      and the namespace's hold taken, in the sweep's order), judging again
      under the lock what the handler judged — the tile still partitioned
      in the same scope and primary, the person the same incarnation, not
      paused, the archive's key still that partition's and not erased — and
      only then reading the archive's records: a switch, reset, purge,
      sweep or erase that took the lock meanwhile wins (409, nothing
      written). Data by
      nsRestorer in the partition's keys (`pkey`), the vault re-sealed
      under this vault (one another vault sealed is left out:
      `vaultSkipped`), the registrations rewritten as the partition's
      current id and re-held, each row checked and capped (16/16). The
      person's record stays their current one.
    - **Never a tile.** `restoreFrom`, `archivedDataOK` and
      `extractMember` refuse a partition archive: a main archive never
      restores into partitions, and a partition archive never restores
      into a tile, a namespace, or out as a single file (an archiver's
      swap under the main key is refused, G2).
    - **Erase.** The switch's erase stays the executor's (F13a's
      `erasedSubjects`: `ns:` and `part:`, never `tile:`) — no second wipe
      hook, which would need the backup lock the executor already holds.
      The namespaces' sweep and — new — the records' sweep
      (`dropSweptPartition`: the backup lock, the drop, the erase) erase a
      swept partition's `part:` key; every erase of a person's partition's
      keys goes through `erasePartitionBackupsHeld(owners, dep, pkey, …)`
      (each owner tile's `part:` key — the tile's own and its scope
      root's — each erase in that tile's history), which F7b's reset,
      purge and sweep (`dropOnePartition`) call too.
    - **Retention of what is gone.** A tile's backups keep an index of the
      partition archives they wrote (`data/backup-refs/partitions/
      <CompKey>.json`, only once one is written); a scheduled retention
      deletes every version of an indexed partition that is gone and whose
      `part:` key is erased, and forgets it once the archiver lists none —
      the dead versions an archiver without erase collection kept (11 §3
      "until retention prunes them").
    - **History.** Every erase of a tile's backup keys outside a switch
      (a sweep, `bx backup erase`, a reset/purge through the helper) and
      every partition restore appends to the tile's mode history (`op:
      "backup-erase"|"partition-restore"`, `reason`, `partition`,
      `wiped.subkeys`) — only when the tile has a mode record, so a tile
      that never used partitions keeps its zero state (its erasures stay
      in slog and the tombstones). A switch's own erase is already in its
      `switch` entry (`wiped.subkeys`). A partition's own history goes
      with it, so the tile's is the record. Of the history's 200 entries
      backup ops take 100 at most, the oldest backup op going first: a
      person restoring over and over never pushes the managers' `auto`,
      `request`, `switch`, `keep` entries out.
    - **Backups from before a switch.** `POST /restore` (and a restore of
      main's data through `POST /deployments/restore`) of an archive
      whose `created` precedes the tile's last switch that deleted data
      (not one that only added "global"; a deployment's archive only for
      a switch that deleted everything) answers 409 `{error, switch: {at,
      from, to, confirm}}` unless `confirm` is the switch's date
      (YYYY-MM-DD); an archive made in the switch's second counts as
      older. It then restores as ever — into today's keys, global's. The
      switch is the mode record's `lastWipe {from, to, at}`, set by
      `recordDecision` for a switch that deleted data and never trimmed
      (a record written before it: the history's last deleting switch).
      A record this xbind can't read can't tell: every restore of the tile
      asks, the confirmation being the backup's own date (`switch:
      {unknown, error, confirm}`). The deployment archives a confirmed
      `POST /restore` lists go with the main archive, judged once.
      `POST /deployments/restore` can't confirm: it refuses with a text of
      its own naming `POST /restore`, the whole tile's (source and every
      listed deployment's data).
  - **Not chosen:** a plaintext partition archive in the plaintext-vault
    mode (unerasable); listing partition archives in the main manifest
    (the main archive would name people's partition ids, and a partition's
    retention would ride the tile's); failing the whole backup for one
    partition; restoring an earlier holder's archive by the person
    themselves; a `confirm` on `POST /deployments/restore` (it refuses a
    pre-switch archive and names `POST /restore`); finding the switch in
    the history alone (trimmed: 200 restores would have erased the
    guard); unmounting a volume the backup mounted directly (it could pull
    a view from under a holder that took it meanwhile — the idle pass
    checks holders and running instances).
- **D158 — Partitioned tiles, B2a: the agent template partitioned by
  default — three modes in one binary (2026-09-30).** Implements PD-30,
  PD-35, the agent's side of PD-34 and PD-36, and C12 (PD-39) of
  plans/partitions/90-decisions.md; the design is
  plans/partitions/08-agent-template.md §1-§2, §6-§9, §11. The template's
  API.md "Partitioned instances"; docs/partitions.md §The mode.
  - **Chosen.**
    - **One binary, three modes from `xbin.Partition()`** (mode.go): ""
      legacy (every existing instance, the opt-out, an older xbind — today's
      code path, every hook nil), `global`, `user:<id>`; an unknown kind of
      partition exits (status 3) rather than serve everyone. The legacy
      golden is the whole existing template suite, unchanged, plus
      TestLegacyModeUnchanged.
    - **The split (PD-30).** `db`, `files`, `events` stay per partition at
      their names (global's at today's keys, by F4); no person's partition
      mounts global's db (checked from /proc in the e2e). Each mode
      migrates only its own db — no code change: the partition's db is its
      own volume at the same path. A person's partition raises runs'
      AUTOINCREMENT to 2^40 − 1 at every open (idempotent, never lowers),
      so its ids start at 2^40.
    - **`conf`** (kv, `"shared": "read"`) is readable by everyone who can
      open the agent (their frame, through xbind's kv API), so it holds
      nothing managers keep from people: the config as viewers see it
      (`forView`) and without the static MCP servers that have `headers`,
      which stay global's (people's conversations don't get them). Keys:
      `halt` (its own small key, written first), `settings` (`config`,
      `classes`, a revision of the mirrored shared skills) and
      `skill:<name>`; written after every change, in the writer's goroutine
      after the commit, and at start; a key the kv refuses is logged and
      skipped (the rest still goes out) and retried every 30 s; a config or
      shared skill over 900 KiB is refused at save in a partitioned agent.
      A partition reads it through `getSetting`'s hook (cached 3 s;
      invalidated after a forwarded write; inside a transaction never
      waiting on the network — the cached copy, a refresh beside it; read
      once in `startMode` before the engine starts), refuses writing a
      mirrored key (`errSharedSetting`), lists shared skills beside its own
      and reloads its classes when conf's change. **It fails closed:**
      until both keys have been read once (global never ran — it is woken,
      at most every 5 min —, a kv error at start) the halt reads as on;
      without conf in `uses` it can never be read.
    - **Managers edit through global.** In a partition `GET`/`PUT /config`
      (the whole config: conf's is the viewers'), `PUT /classes`, `/halt`,
      and `PUT`/`DELETE` of a skill that isn't the partition's own are
      relayed by the partition's backend to `xbin.GlobalURL(path)` (F5/F9:
      attributed to its person; the relay's 30 s bound is the call's). A
      skill in a partition is its person's or everyone's: `owner` naming
      anyone else is 400; a new one with `owner` = the person stays local;
      their own saved with `owner: ""` is published (relayed, then deleted
      locally). The partition's guard checks first, global again (its
      level). The page is unchanged.
    - **The brake in a partition** (brake.go): a halt conf says is on
      cancels a run at its next step or pass (the turn loop checks it
      between steps in user mode only), as `PUT /halt` cancels live runs
      elsewhere; the manager's own partition cancels at once. While conf is
      unknown the halt reads as on without cancelling: runs park, requests
      for work are queued, and a one-shot timer with backoff (3 s → 1 min,
      only while runs are parked) re-reads conf and recovers them once the
      brake is off (`onHaltOff`: `eng.recover()`). Without conf in `uses`
      requests for work answer 503 saying so. A manager's request whose
      halt can't be lifted at global answers 503 (never queued behind the
      brake).
    - **Global takes a person's attributed calls** (agentRole): the route
      gate stays `RoleFunc("admin")` except, in global mode, a call with
      From = the tile, X-XBin-Partition `user:…`, a person and the role
      xbind clamps to (reader/writer) — read as `whoUser` with their level
      (W2's "F9's attribution reads as whoUser"). Such a call without its
      person is nobody (`whoNone`), never the tile itself.
    - **`team`** (sqlite, `"shared": true`): migrated only by global under a
      `<team>.migrate` flock (schema 1: its version row); a partition opens
      it without migrating (`openShared`), wakes global (`GET /health`) and
      waits ≤30 s when behind; `teamUnavailable` answers 503 "the shared
      space is being upgraded" for B2d's hosted features.
    - **LLM concurrency (PD-36):** `maxActiveRuns` lock files
      `llm.slot.<i>` in team's directory, try-locked (LOCK_NB, random
      start, 20→400 ms jittered backoff) after the process gate; released
      with the call; a dead process's slot frees with its fds. As the gate's
      kidCap, a subagent's call (and a title's) may take only slots
      0..n-2 when n ≥ 2, so a top-level call waits for at most one call
      tile-wide. A directory that can't hold them costs only the cap (calls
      and titles go ahead). A partition's gate: 2. The flocks need one
      kernel — xbind refuses `vm` with `partition` (F1), so a partitioned
      agent's backends never run in separate guests.
    - **Resume (C4):** a partition leaves `resume` (@every 1m) only for
      work a pass takes up without its person — running/queued runs,
      undelivered inbox rows, a settled link its parent takes (`fg` at a
      running/queued/awaiting parent; `bg` at a root that is running,
      queued, sleeping, idle, done or canceled — never an errored root or a
      non-root parent, which pass() leaves for a person's message), a run
      sleeping on a sandbox job (its end is seen only by looking); else
      `wake` (`CRON_TZ=UTC m h d M *`, the earliest `wake_at` of a sleeping
      or awaiting run, rounded up to its minute; within a minute counts as
      runnable); else nothing — and nothing at all while a known halt is
      on. Takeover deletes `wake` too. Global and legacy: today's rule.
    - **Sandboxes (C12):** a partition refuses a manager's hello without
      `partitions` (refusal `partitions` → 409) — catalog, tools, dialogs;
      global keeps using it. A partition's conversation binds and uses
      (checked again at every use) only a sandbox homed there by the
      manager's own word: `owner.via` = the tile, `owner.partitionId` = the
      partition's id (`owner.partition` = its key while the id isn't
      known), not `shared`; anything else — a manager that says nothing of
      the home included — is refused (403); its terminal still opens.
      `xbin.agent/home` on every create (`sbxConn.Create`) = the partition
      id (learned from X-XBin-Partition-Id on calls into it, kept in
      settings; the partition key until known) or `global`; none
      unpartitioned.
    - **Personal providers:** `personal: true` rows of XBIN_IFACE_LLM/MCP
      are offered only when a context says the conversation is the
      partition's person's (a turn, a compaction and a title set it from the
      root's owner; the model picker from the caller, not view-as). The
      default model's lookup uses shared providers only.
    - **Mail skeleton:** `POST /mailbox` (mounted apart: xbind rings it as
      xbin/mail with the writer role; else only the owner token or the tile
      itself — `principal()`, never callerOf's nobody-is-the-tile
      fallback) and every start pull a `mailSource`;
      a topic's handler runs with the item's id recorded in `mail_seen` in
      one transaction, then the item is acked; a redelivery is only acked;
      an unknown topic stays unacked. No handler, and the source holds
      nothing, until F6 is wired.
    - **No sharing in a partition** (until B2b): the sharing routes, `PATCH
      /runs/{id}` with a `visibility` other than private or a `teamRole`
      other than viewer, and a `team` schedule answer 409.
    - **The page's three layouts** (model/partition.js): unset —
      unchanged, not one element or call added; `user:` — no sharing (row
      menu, top bar, the Shared view; web and native), a banner for old
      sandbox managers (read at start when the slot is bound); `global` —
      a note to sign in as a person. Partitioned pages close their live
      stream while hidden and resume from the cursor.
  - **Not chosen:** the frame calling global for settings (the forward
    keeps the page unchanged and one place checks); a sqlite table or kv
    counter for the LLM cap (a write per call, and no release on death);
    one conf key holding the skills too (kv values are cut at 1 MiB);
    mirroring the static MCP headers, or a backend-only path to them
    (conf is people-readable, and a person's partition can't tell its own
    backend from its frame when it calls global); refusing `mcp[].headers`
    in a partitioned agent's config (global still uses them); parking a
    partition's runs under a known halt (they would stay `running` and the
    partition would restart every minute for the halt's length — they are
    cancelled, as unpartitioned); settings defaults while conf is unread
    (a manager's halt or classes would silently not apply); migrating team
    from any partition; dropping unknown mail topics (a newer global
    mid-deploy may be the sender); pausing unpartitioned pages' streams
    (xbind counts them as activity there today); filtering personal
    sandbox managers (hosted conversations may use the host's).

- **D159 — Partitioned tiles, B2b: the agent's shared conversations at
  the global instance, two homes on the page (2026-09-30).** Implements
  PD-31 and the agent's side of 90 §I4 (global is the realtime hub) of
  plans/partitions/90-decisions.md; the design is
  plans/partitions/08-agent-template.md §3. The template's API.md
  "Partitioned instances"; docs/partitions.md §The mode.
  - **Chosen.**
    - **Shared conversations are the global instance's**: made there with
      `POST /ask {…, share}` (the team to read or write, and/or people),
      reached by people's pages with F9's `{partition: 'global'}`; global
      reads such a call as the person (B2a's `principal`/`agentRole`), so
      D83 applies unchanged — no new ACL code.
    - **The shared space keeps shared conversations**: a person's attributed
      `POST /ask` at global without `share` (or with a draft) is 409/400,
      and their `POST /runs` and `PUT /ask/upload` (a new chat's draft) are
      409 — a buggy or old page can't start a private chat of a person's in
      global's db (B2a's open question). A person may still un-share one
      there later (an owner question). The owner token (the global-viewer
      state) asks as ever.
    - **Two homes by id** (`model/homes.js`): below 2^40 the global
      instance's, from 2^40 the person's partition; every call about a
      conversation — path `/runs/<id>…` — and the stream following it go to
      its home (`model/home-api.js` wraps the kit's `selfApi`/`xbin.fetch`);
      only a person's partition has two homes, so unpartitioned and
      global-instance pages send exactly what they did.
    - **Streams**: the page's own stream always; the global instance's
      follows a shared conversation while it is open, and the run list only
      while the list shows shared rows (or the Shared view) — else it is
      closed, so a page with nothing shared never keeps the global instance
      busy; such a page reads the shared space's first page again when it
      shows or gains focus (at most every 15 s), and lists what was shared
      with the person since. Both pause while hidden (B2a). Live for every
      member (90 §I4) holds by construction: each member's page follows
      global's stream.
    - **The merged list**: Mine reads both homes and cuts at a horizon (the
      newest of the last rows, as read, of homes that have more pages),
      holding rows below it until the next page, so paging never shows a
      row above one a later page could still bring; live events reach held
      rows too, and each conversation is listed once; Shared is global's;
      search and "needs you" read both.
    - **Copies between homes**: `POST /runs/{id}/publish {share, files?,
      keep?}` in a person's partition exports the conversation (the root's
      transcript as the model reads it — tool calls and results, folded and
      masked ones marked, the summary, the task ledger on the messages it
      pins; session files only with `files`, ≤ 16 MiB together) and sends
      it to global's `POST /import` through `callGlobal` (attributed), then
      deletes the original unless `keep`; `POST /copy {from}` fetches
      global's `GET /runs/{id}/export` the same way and imports it
      privately. A bundle over 48 MiB is 413. Subagents' transcripts,
      memory, grants and sandboxes stay behind; the copy gets a note naming
      where it came from. The four routes exist only in a partitioned
      instance.
    - **A bundle is its caller's word**: at `POST /import` a message keeps
      its writer only when that is the caller; anyone else's comes as a
      copy (`origin: "copy"`, `label` the id it named — the page shows
      "copied · <id>", the model reads no `[id]` for it) and a ledger row
      that isn't the caller's own request becomes a `copy` one. `POST
      /copy` imports the global instance's own export, whose writers stand.
    - **Sharing in a partition**: `POST /runs/{id}/members`, `/links`,
      `PATCH` visibility and team schedules keep B2a's 409 (a conversation
      there is its person's alone); `POST /join` is relayed to global (join
      links are global's); `sharing()` is true in every layout — the web's
      Share on one's own conversation opens "Share a copy", on a shared one
      the share dialog at global.
  - **Not chosen:** the frame carrying the transcript to global (08 §3's
    wording) — the partition's backend makes the same attributed F5 call,
    so the transcript never passes through the browser and the original is
    deleted only after global took the copy; a second list API merging
    homes server-side (the partition can't read global's ACL'd list as the
    person without a round trip per page); an always-open global stream
    (keeps global awake for pages with nothing shared); a membership
    doorbell for newly shared conversations (a mail or ping per share — the
    focus/visibility re-read covers the page a person is looking at);
    trusting a bundle's writers at import (any person could put words in
    another's name); refusing `share` on an unpartitioned `POST /ask` (it
    is what sharing right after does).
- **D160 — Partitioned tiles, B2c: a partitioned agent's channels,
  triggers and usage go by partition mail (2026-09-30).** Implements PD-37,
  PD-38 and the agent's part of PD-46 of plans/partitions/90-decisions.md;
  the design is plans/partitions/08-agent-template.md §5, §7. The template's
  API.md "Partitioned instances"; docs/partitions.md §The mode.
  - **Chosen.**
    - **A linked DM is a handoff** (`_backend/handoff.go`): at the global
      instance, `channelMessage` — after admission, before commands — hands a
      DM whose peer is linked (`channel_peers.xbin_user`) to the person's
      partition: a `handoffs` row (id, channel, peer, session key, reply
      address, person, trigger, source; state) and the item's payload (the
      text, the channel's lane/class/deny/system text/reset, the staged
      file ids) are written in the message's transaction and mailed after
      the commit (`handoff_send.go`). **Per person, in order:** a failure
      xbind may retry — 409, 507, 5xx, network, the blob store — holds back
      only that person's handoffs (`next_try`: 1 s doubling to 5 min), so
      one full inbox never stalls anyone else's; a refusal — 400, 403, 404,
      413 — is final and tells the chat; one still queued after 7 days (a
      mail item's default life) is given up the same way. The payload is
      cleared once mailed or given up; the staged files of a queued DM
      outlive the upload prune until then; records live 30 days. `/help`
      and `/link` are answered at global (the chat account's); the other
      commands go with the handoff. A global instance stopping with
      handoffs queued leaves its `resume` job.
    - **The first-DM notice (09 §3)** (`handoff_people.go`): a person's
      partition mails `partition/hello` at its first start (once per db);
      any mail from it says the same. Global keeps two flags per person
      (`partition_people`: ran, told) — no times. A DM for someone whose
      partition hasn't run is still mailed (it waits in their inbox), is
      answered once with "open the agent once…", and the chat isn't shown
      the agent working.
    - **The person's partition** (`_backend/handoff_user.go`) takes
      `handoff/dm` only from `global` (xbind's stamp): the session key is
      global's, the run is the person's (private, origin `channel`), the
      channel's lane/class/deny/system text as global decided them, the
      files session files; its reply address is `{"handoff": <id>}`, so what
      the run answers — a turn's end, a question, a notice, an announcing
      trigger — is a row of the partition's own outbox, mailed to global
      after the commit (`outbox/add` {handoff, key, kind, text, files}) and
      settled `delivered` once xbind took it; while one waits, the
      partition's `userWake` counts it as work (a stopping partition leaves
      its `resume` job). A handoff mailed twice is taken once (its inbox
      `client_id` `ho:<id>`).
    - **Global posts a reply** (`outbox/add`) only when `from` (xbind's) is
      `user:<the handoff's person>`, the handoff is a DM's and at most 30
      days old, and its chat account is still linked to that person (not
      blocked); the destination (channel, address, session) is global's own
      record; the row's `origin` (`user:<id>/<key>`, unique; kept 30 days,
      past the outbox's week) makes a retry post once; files are staged as
      channel files (`staged:<id>` on the row, served by
      `/adapter/files/{oid}/{i}` only for that row's channel). At the
      adapter's ack — delivered or failed — the row keeps only its dedupe
      key: body and address `{}`, staged files gone; a failed one can't be
      retried. The channel owner's outbox view lists such rows without
      text, files or address.
    - **A mail item's files are stored before its transaction**
      (`mail_prepare.go`; the db has one connection): objects named after
      the item (`handed/<item>/<i>`), kept if the handler used them,
      deleted otherwise or when the transaction failed.
    - **The trigger registry is today's `triggers` table at global**, plus a
      `host` column (`user:<id>`) on the registry rows (name, source,
      source_ref, match, owner, enabled, max_per_hour; no goal). A person's
      partition registers (`POST /triggers/registry`, attributed to them;
      upsert keyed by name, `prev` for a rename, their own rows only) before
      it saves the trigger, when name/source/match/switch/cap change, and
      removes the row before a delete (`DELETE /triggers/registry/{name}`);
      a registry that doesn't answer refuses the change (502). Rules: a
      private push trigger needs a `match`; one that is a prefix of — or
      prefixed by — any other owner's on the same source (team ones
      included) is 409. **At global a person's call from their partition
      can't keep a private automation** — `POST /triggers`, `POST
      /schedules`, an edit beyond a switch of a private one: 409 — so the
      rules can't be passed by saving a private push trigger there. A push
      matching a registry row is recorded at global (`trigger_events`: the
      dedupe and the hourly cap, kept a day; the switch, the halt) and
      handed over (`handoff/event`, mailed with `source` = the pushing tile
      → `LedgerTrigger`); the partition fires its own trigger by name with
      the event (its dedupe, class and data rules). Global keeps no last-run
      time of a registry row and answers its events and test with 409. A
      manager at global may only switch a registry row or delete it (any
      other edit 409); global never subscribes a hosted bus row (the
      partition subscribes its own). **Oversight from a manager's own
      partition:** a partition numbers its triggers from 2^40 (like its
      runs), so its Automations list the global instance's rows of other
      people (`access: "oversee"`, from one short-lived read of global's
      `GET /automations` that the channels' items share; a write the
      partition forwards there makes it stale) beside its own without an id
      clash, and `PUT`/`DELETE /triggers/{id}` below 2^40 there is forwarded
      to global.
    - **Usage totals** (`_backend/usage.go`): a partition counts each model
      call on the UTC day it was made (`usage_daily`, beside the run's own
      counters) and its conversations on the day they started, and mails
      `usage/day` {days: [{day, runs, llmCalls, promptTokens,
      completionTokens}]} for the UTC days after the last one sent up to
      yesterday (≤ 31), at start and at each UTC midnight while it runs;
      global keeps them per person for 90 days (`usage_days`); `GET
      /usage?days=` for managers (forwarded from a partition; 404
      unpartitioned).
    - **From a person's partition** the channels' routes (claim, edit,
      peers, pairing, sessions, outbox, retry), `GET /triggers/unmatched`
      and `GET /usage` are forwarded to global (`userGlobal`); its
      Automations list the global instance's channels. A `team` trigger
      answers 409 in a partition (`sharesInPartition`).
    - **Unpartitioned nothing changes:** the tables and columns are made
      only in a partitioned instance (`addHandoffSchema`, from `startMode`);
      every hook is a no-op outside its mode; the new routes answer 404.
  - **Not chosen:** mailing inside the message's transaction (the write
    lock held for a network call; the SDK client has no deadline); one
    backoff for the whole sender (one person's full inbox held everyone's);
    sending the channel's whole policy to the partition (only what the run
    needs); a separate registry table (names must stay unique with team
    triggers); letting a person's partition subscribe for pushes (the
    webhooks tile isn't partitioned: it reaches global only); logging each
    handoff or event at global (an activity timeline managers would read,
    PD-46); asking xbind whether a person's partition ever ran (no such
    read for tile code: the partition says hello instead); staging large
    files for an F5 fetch (see Deviations); a manager route listing
    handoffs (the same).
- **D161 — Partitioned tiles, F11: the partitions page,
  `/xbin/partitions` (2026-09-30).** Implements PD-47's guaranteed person
  surface (plans/partitions/06 §12.1; 90 §I). docs/partitions.md §Your
  partitions page; docs/protocol.md (the core route).
  - **Chosen.**
    - **A static page under `web/`, served at its own core route** (`GET
      /xbin/partitions`, `authed`: signed out, a browser goes to /login):
      web/partitions.html byte for byte — no HTML transform, the same bytes
      for every principal; everything it shows it reads from the partitions
      API with the person's own session (a plain same-origin fetch, never
      `xbin.fetch`), and xbind judges every act again, as for `bx`.
    - **Top-level only.** `X-Frame-Options: DENY`, CSP `frame-ancestors
      'none'` (a tile framing it could overlay a consent or a credential
      Allow — clickjacking; in origins mode a tile origin is same-site),
      COOP `same-origin` (as /docs/: an opener keeps no handle), and the
      element renders a refusal, nothing of the person's, when `window.top
      !== window`. `/vendor/` answers the page 404 (`topLevelPage`), so no
      copy of it lacks those headers; its modules load from `/vendor/` as
      every core element does.
    - **No inline script, no import map.** CSP `default-src 'self';
      script-src 'self'` (styles inline allowed); the modules import lit
      from `/vendor/lit-all.min.js` directly (the web/xb renderers'
      precedent) — a bare `lit` would need an inline import map.
      TestPersonPageSelfContained holds it.
    - **Split for size and tests:** web/partitions-kit.js (pure: the reads,
      the model, xbind's words — registry.SwitchDeletes' texts, checked by
      TestPersonPageSwitchWords — node-tested in
      hack/partitions-page.test.mjs), partitions-page.js (the element and
      its acts), partitions-sections.js / partitions-more.js (the
      sections), partitions-css.js.
    - **One round of reads per refresh:** whoami, `GET /partitions`, `GET
      /components` (owners, a paused tile's `partitionNote`), consents,
      binds, ledger (30 days), then `GET /partitions?tile=` per listed
      tile. Only the caller's own rows are kept (an admin's answer carries
      everyone's metadata rows: this is not the admin section). Refreshed
      on the `partitions` and `policies` events and after each act.
    - **Managers are recognised client-side** as the shell does (admin, the
      owner, an admin of the owning org — whoami + the component row's
      owner); view-as decides nothing. The server's `mayManageTile`
      decides.
    - **Switch decisions** follow F13a: pending first, then declined
      requests (Switch… stays offered, Keep doesn't); Switch… runs the dry
      run and shows `deletes`, the counts, `people`, the keep list, the
      sandbox managers lacking `partitions` (a "switch anyway" box), then
      needs the tile's path typed; a switch's answer stays on the page
      after its tile leaves the list.
    - **The trust panel's "used your data here"** is the person's own
      ledger (edges from their partitions of other tiles into this one), 30
      days; no new API field for granted-but-unused edges.
    - **Only the person's own rows and binds, per tile too.** An admin's
      `GET /partitions?tile=` carries every person's partition rows and
      live personal binds (I1); the page keeps the caller's (`user ===
      me.id`) for the overview's binds and each tile's alike.
    - **Held credentials: Refuse is one click, Allow asks.** Allowing a
      credential someone else made is the act that could hand the account
      over (credentialResetConfirm exists for that), and refusing costs at
      most a fresh link; so Refuse is the primary, one-click act, and
      Allow… first asks — naming who made it and when, and what it lets
      them do. Each answer stays on the page this visit after the
      credential leaves the list (as a switch's); one that was no longer
      waiting (409 `already-effective`, or 404: it took effect unanswered)
      is a warning banner (`role=alert`) in xbind's words.
    - **Times** show in the browser's zone with its name (`Intl`
      `timeZoneName: 'short'`): xbind's notice texts beside them say UTC.
    - **A deep link survives signing in.** `authed` sends a signed-out
      browser on `/xbin/partitions` to `/login?next=/xbin/partitions`
      (internal/server/loginnext.go, `loginNextPaths`; every other route
      still goes to plain `/login`); `GET /login?next=` carries it in the
      form (a hidden field) and the single sign-on link, `POST /login`
      lands there, and `GET /login/sso?next=` carries it in the signed
      state cookie to the callback. Only webticket.go's `safeNext` path is
      followed (same-origin, printable, not a sign-in route); anything else
      lands on `/`, as every sign-in did. Rejected: carrying `next` from
      every authed route (the shell's `/` and `/c/` redirects stay as they
      were).
    - **Links to the page:** the consent refusal (`… (they allow it at
      /xbin/partitions)`), the log-share refusal, the consent push's body,
      and the `tile.partition-switch` / `tile.partition-deleted` pushes'
      link (`xbin/partitions`, was `c/<tile>/`); the shell's paused card
      gains "details…". The partition-switch alert's text is unchanged (the
      F14 shell strips its bx hint by exact text), and the in-frame switch
      page keeps naming the page as text (it runs sandboxed without popups
      or top navigation, and the page refuses frames, so a link there could
      only fail).
    - `partitions-page/1` in `GET /partitions`' features.
  - **Rejected.** A Go-embedded HTML constant (the docs viewer's pattern):
    the page belongs in web/ with the other core modules. An inline import
    map allowed by a CSP hash: a per-file hash to keep in step for no gain.
    A server-side `manages` field on the overview rows: F7b's file, and the
    shell already computes it the same way.
- **D162 — The admin console's partitions view and the logs panel's
  partition switcher (2026-09-30).** Implements plans/partitions 06 §12.3
  (the admin tile's runtime → partitions) and the owner's answer I5 (a
  partition switcher on the logs tab); PD-46, PD-54 as answered in I1, I6,
  I8, I9. docs/partitions.md §Operating people's partitions,
  docs/protocol.md (`GET /partitions`, `GET /sandboxes`,
  `POST /partitions/limits`, `/partitions/binds`), docs/frontend-kit.md
  (`<bx-logs partition>`), the admin tile's API.md, docs/maintenance.md
  (the tabs and the harness passes).
  - **Chosen.**
    - **One tab, a list and a tile view.** `tabs/partitions.js` (the list,
      a `PLAIN_TABS` entry — admin.js gains only its `GROUPS` line),
      `tabs/partition-tile.js` (one tile's view, loaded when its row
      opens), `tabs/partitions-view.js` (the words and request bodies,
      lit-free and unit-tested). Every act is the existing route's, through
      the admin tile's frame. Confirmations are inline (typed text in the
      tab), so the console works outside the shell and in the harness; a
      purge sends one `{tile, partition}` per row it listed, never the
      empty body that purges every orphan there is when the click lands.
    - **The console is the driving admin's, not the tile's grant's.** The
      admin tile's frame under a person's login (AdminFrameDriver) reads
      `GET /partitions` — the overview and one tile — as an admin only when
      that person is one (`consoleAdminDriver`); driven by anyone else the
      frame is tile code there (the tile-level fields). The routes that
      judged the credential itself — `POST /partitions/limits` and
      `GET`/`DELETE /partitions/binds` — judge that frame by its driver's
      role (`partitionsAdmin`), as reset, purge, restore, reviewed-only and
      the mode decision already judged the person. The admin tile's other
      credentials (its backend, a frame its terminal minted) keep the
      tile's `xbin:admin` grant, as before. The overview for the console
      leaves out the driver's own rows, notices and credentials: it shows
      the workspace, not the person.
    - **Admins' extras on one tile** (partitionadmin.go): the mode history
      (newest first, the last 50 of the record's 200), `lastWipe`, and the
      global inbox's counts (W3a's optional `globalMail`, from the counts
      the listing already read — one scan of the store) — metadata only:
      who decided, when, the modes, wiped counts, a partition id admins
      already see on the rows; never an item.
    - **Managers see who shares with them.** sharedLog (06 §5) lets a tile
      manager read a log its person shares, but a manager's listing had no
      rows to offer it from: it now carries `logShares [{user, until}]` —
      the person chose to show their log to admins and managers, so naming
      them is theirs. Admins read it on every row.
    - **The switcher lives in `<bx-logs>`**, so the terminal window's logs
      panel and the Deployments panel's primary log both have it; its logic
      is `web/logs-partition.js` (pure). It appears only once an answer
      names a partition (`X-XBin-Partition`, which only a partitioned tile's
      log sends) or a refused default on a tile the listing calls
      partitioned: an unpartitioned tile makes no extra request and looks
      as before. The entries: the default (what the credential reaches —
      "yours", or "global" for the root token); `global` when the tile runs
      a global instance and the viewer may read it (an admin's listing says
      so; for anyone else a `tail=0` read of it answers — today's gate,
      terminal access or an admin); and each person who shares their log
      with the viewer (an admin's rows, a manager's `logShares`; never an
      orphan's). A named choice is shown only when the answer echoes it
      (the deployment echo rule). The listing is asked again when the panel
      opens anew and, on a new stream, once it is 30 s old; another
      component or deployment clears the choice.
    - **The sandboxes view's labels** come from the rows: `partition` on a
      person's backend (F3), and a new `personal` flag on a person's session
      (F7a blanked the name; the admin couldn't tell a person's session from
      a nameless one).
  - **Not chosen:** a logs view inside the admin console — the admin tile
    reads no backend log today (`canReadLogs` refuses element principals),
    and giving it one is a new read path; an admin reads a shared log in the
    shell's panel, as their own session (an owner question). A
    workspace-wide `maxRunning` editor (the overview doesn't carry the
    workspace cap; `bx partition limits` sets it). Restoring an earlier
    holder's archive of an id (`partitionId` + `to`, 11 §4) from the
    console: it stays `bx restore --partition-id … --to …` — the console
    offers the person's current partition's versions only. Importing the
    shell's `partition-mode.js` into the admin tile (another tile's module;
    the admin scaffold and the shell update apart): the words are
    re-stated, and the node test pins the switch words to the shell's. A
    `logs` flag in the listing for the global log's gate (the `tail=0`
    read asks the gate itself, and adds no field). A server-checked purge
    list (one request per confirmed row needs no new body).
- **D163 — Partitioned tiles, F14b: the partition chip on every
  partitioned tile's window, and the shell's consent prompts
  (2026-09-30).** Implements the owner's ruling I3 (a chip saying whose
  partition a window shows, beside F14's marker, D153) and 06 §12.3's
  "consent prompts (only with the policy on)" over F10's consents API and
  events (D151, PD-13). Design: plans/partitions/06-terminals-ops.md §12.3,
  05 §2, 90-decisions.md §I3; docs/partitions.md.
  - **Chosen.**
    - **One chip, four words, one function**
      (`partition-mode.js partitionChip(view, {shown, who})`): nothing on a
      tile whose recorded mode has no user partitions (the marker's rule:
      it follows R, so a pending switch doesn't change it); view-as →
      `no partition` (xbind never opens a person's partition to view-as,
      on a deployment neither); a non-primary deployment → `shared` (F14's
      `DEP_SHARED` words); a person → `yours`; the workspace token →
      `global` with a global instance, else `no partition`; nothing while
      `/whoami` isn't read (never a guess). bx-canvas gets `/whoami` as a
      `.who` property.
    - **Every window: the card and the pop-out.** A tile's pop-out window
      (`xbin.window`, drawn by bx-shell) frames a sub-path of the tile —
      its own page, in the same partition as its card — or `spec.src`,
      another tile. `framedTile(src, components)` resolves the listed tile
      (the longest path src is or lies under) and a `+name` deployment;
      `shell-kit.js spawnTitle` draws that tile's marker, the title and
      the chip (`chipTag`, shared with the card) in the pop-out's head.
      bx-shell takes `partCss` for it — no line added there.
    - **Where and how it looks:** after the path (the card) or the title
      (a pop-out) — the marker keeps the runtime dot's place at the head's
      start, and on a card the chip keeps the place F14's `shared` chip
      had, after the `+name` tag; a quiet chip in the marker's hue
      (`--bx-part-c`), a faint tint, no border, `cursor: default`, no hover
      state; `no partition` muted grey. The `shared` chip keeps its
      `.dshare` class and words, restyled into the family (owner question
      2: I3 says "beside the marker").
    - **The prompt lives in the shell's decision strip**
      (`workspace-template/shell/bx-part-consent.js`, `<bx-part-consent>`
      beside `<bx-grants>`/`<bx-bindings>`), not on the calling tile's
      card: the refused call may come from a tile whose window isn't open
      (a cron job, another tab), and the strip is where the shell already
      asks for decisions. With nothing to answer the element is `hidden`
      (no box, so the strip's flex gap doesn't move the canvas).
    - **What it shows is xbind's list:** `GET /partitions/consents`'
      `asked` (the edges asked about in the last day and not allowed
      since), only while `policy.partitionConsent` is on, each once, and
      only for tiles the shell still lists (an ask outlives a removed tile
      for its day). It reads the list once a signed-in person (not
      view-as, not the workspace token: `consentPerson`) sees a partitioned
      tile (`consentWatch` — no extra request on a workspace without
      partitioned tiles, nor a 403 for the token), then again on a
      `partitions` event with op `consent-needed` or `consent` (asked, or
      answered elsewhere), on `policies`, and after the events socket
      reconnects — events only for a person, so view-as and the token
      never call the API. The events come through the page's own
      `/ws/events` socket (`/vendor/events-socket.js`, the person's cookie)
      — the shell tile's `xbin.events` never receives PersonOnly events,
      by F10's design.
    - **Allow** is `POST /partitions/consents {from, to}` as the signed-in
      person (a raw fetch; xbind refuses tile credentials — PersonOnly);
      the panel says what it did and how to take it back (`bx partition
      consent … --revoke` until a partitions page is served), for 15 s or
      until closed; a refusal shows xbind's error on the ask, and the list
      is read again (the policy turned off meanwhile: the ask goes with
      it). **Allow is live only while nothing covers the ask:** while asks
      show, a timer hit-tests each ask's box on a 24 px grid
      (`coverPoints`; `elementFromPoint` on the shell's root must return
      the panel's host) every 200 ms, and Allow stays disabled until
      nothing has covered it — or put part of it out of view — for 600 ms,
      a new panel included (an ask that appears under a click meant for
      something else doesn't take it); the click checks again (the timer's
      look may be 200 ms old). A tile's pop-out window is placed where the
      tile says, above the strip: without this it could hide the question
      and "Don't allow" and leave Allow in view, or close the instant the
      pointer leaves it for Allow. Any window at least a grid step wide and
      tall that overlaps an ask hits a point (a pop-out is at least
      200×140).
    - **Don't allow** stores nothing on xbind (F10 has no decline record,
      and a refusal is already the state): the browser keeps the ask's
      `at` in `localStorage`, **one key per person**
      (`xbin-partition-consent-dismissed:<id>`, `consentStoreKey`), so that
      ask stays hidden there for that person (other tabs follow the
      `storage` event); a later ask — xbind asks again at most once a day —
      shows again. Dismissals are pruned to the asks xbind still lists,
      the person's own only: on a shared browser nobody's dismissals show,
      hide or prune anybody else's.
    - **An allow answers the ask (xbind).** F10's in-memory `asked` map
      kept the day's ask, and `askedOf` hid it only while a consent held:
      a consent taken back the same day brought back the old prompt,
      though the tile hadn't asked since. Now `POST` marks that prompt
      answered (`consentState.answered`, the prompt's time): `askedOf`
      leaves it out, and it still keeps xbind quiet on the edge for the
      rest of the day — someone who just took a consent back isn't asked
      again the next second; the next day's refused call asks anew. It
      fixes the shell and F11's page alike.
    - **Words and calls are pure** (partition-mode.js: `consentWatch`,
      `consentPerson`, `consentPrompts`, `keepDismissed`, `consentEventOp`,
      `consentCall`, `consentStoreKey`, `coverPoints`, `framedTile`, the
      texts), tested in node; `postMode` and `consentCall` share one
      helper (`personCall`), `postMode`'s behaviour unchanged.
  - **Not chosen:** only the owner's three words (a view-as window and the
    workspace token on a tile without a global instance reach no
    partition; `yours` or `global` would be false there, and no chip would
    break "every window"); the chip right after the marker, before the path
    (it would move the title and split the deployment window's `+name` /
    `shared` pair); the prompt on the calling tile's card (not always on
    screen) or as a toast (the strip persists until answered and survives a
    reload through xbind's list); the prompt in the top layer (a popover
    above every window: it would float over the tiles instead of sitting
    in the strip with the other decisions, and still needs the appearance
    delay); a server-side "declined" record (an API change in a scaffold
    pack; F10 decided asks in memory, once a day); dropping the ask on
    `POST` (a revocation would be re-asked the next time the tile tries,
    at once); treating the `consent` event with `allowed: false` in the
    shell (a shell closed at the revocation would still show the ask, and
    F11's page too); reading the consents on every shell load (a request
    per load on every workspace, and a 403 for the workspace token and
    view-as); the shell tile's `xbin.events` (never receives PersonOnly
    events); polling.
- **D164 — Partitioned tiles, B3: llm-gw counts per person's partition;
  the tiles around a partitioned agent stay unpartitioned (2026-09-30).**
  Implements llm-gw's side of PD-36 and the notes of 09 §2-§5 of
  plans/partitions (bridge, webhooks, sandbox-terminal, llm-gw). llm-gw's
  API.md "Partitioned callers"; docs/agent-inbox.md §A partitioned agent;
  docs/partitions.md §Providers.
  - **Chosen.**
    - **Per-caller rows only for partitioned callers.** A call counts per
      caller when it carries `X-XBin-Partition` (xbind sets it on every
      call from a partitioned tile's principals); the key is (`X-XBin-From`,
      `X-XBin-Deployment`, `X-XBin-Partition-Id`) — partitions.md's
      provider rule, so `""` and `global` are one row; the display name is
      the last call's `X-XBin-Partition`. A call from a tile that isn't
      partitioned carries neither header and counts per backend only: the
      kv, `GET /stats`, `GET /config`, `/metrics` and the page are
      byte-identical for a workspace without partitioned tiles.
    - **Metadata only:** requests, tokens in/out, cost, last use; counted
      in memory and persisted by one writer after the call (coalesced: a
      burst is one write; no call waits on kv) in the state kv's `callers`
      (at most 1000 rows, the least recently seen dropped past that). A kv
      error other than not-found never starts the table over (the load is
      retried; nothing is written until it succeeds; then the persisted
      and the new counts add up). A call that brings a new partition id for
      the same (tile, deployment, `user:<id>`) — the person was deleted and
      recreated, or their partition reset — drops the old row: the new
      person never sees it, and it isn't kept.
    - **Who sees rows (PD-46):** a person their own partitions' rows (with
      the last use and calls in flight); a manager of llm-gw (write or
      terminal; a tile granted `admin` on it, with no person behind the
      call) each calling tile's people's partitions together
      (`callerTotals`: how many, requests, tokens, cost) and the global
      instances' rows, never another person's row or partition id; the
      owner token — and llm-gw's own principals with no person behind
      them, i.e. its page opened with the owner token — every row, a
      person's with its counters only (no last use, no live counts: polling
      never draws anyone's activity over time, 08 §7); view-as nothing. A
      signed-in workspace admin reads as a manager: xbind tells a tile a
      person's level on it (admins: `terminal` everywhere), not whether
      they are an admin; per person and day, admins read each person's
      calls to llm-gw in xbind's egress ledger. `/metrics` stays per
      backend.
    - **The fairness limit** (`partitionLimit`, 0 = off, default off,
      ≤ 64; changing it needs write access) caps one user partition's
      calls in flight per caller key; excess calls wait in FIFO order for
      at most 20 s (a caller that hangs up leaves the queue; a slot handed
      to it as it left passes on), then are answered `429` with
      `Retry-After: 2`. The bound sits under the agent's 90 s stream
      watchdog, which is armed before the response headers — a call held
      longer would be cancelled as a stalled stream and retried at the
      back of the queue; the agent retries a 429 with a backoff. The global
      instance and unpartitioned callers are never held. A raised limit
      admits waiting calls at once. For the agent, whose own gates keep a
      person's partition to 2 of its 4 tile-wide model slots, the limit
      matters only below 2, and a held call keeps one of those slots idle
      (documented): it is for partitioned callers without such caps.
    - **Bridge, webhooks, sandbox-terminal: no code change.** They stay
      unpartitioned and reach a partitioned agent's global instance; their
      docs say what B2c's hand-offs mean for them (09 §2-§4) as B2c builds
      them, that binding them is an ordinary global bind by today's
      authority (PD-54, D33, D88), that a personal bind doesn't apply to
      them, and who is in whose trust base.
  - **Not chosen.** Per-caller rows for every caller (a per-tile usage
    table in every workspace: useful, but not byte-identical — an owner
    question); every row to every manager (PD-46 gives managers totals;
    per-person rows are an admin's, and a tile can't tell an admin — the
    owner token alone gets them); holding a call without bound, or an
    immediate 429 (the first cancels the agent's calls as stalled streams,
    the second fails its turns within seconds); per-partition series in
    `/metrics`; a per-day token budget (the counters make it a small
    follow-up if wanted).
  - **Fixed on the way:** llm-gw's per-backend `bumpStats` encoded its
    shared map outside its lock (a call adding a backend's first row could
    race another's persist: a concurrent map read/write); it now marshals
    under the lock.
- **D165 — Template instances merge their manifest by keys: a merge
  driver in each instance's repo, and instantiation that keeps the
  template's JSONC (2026-09-30).** Removes the manifest conflict every
  instance met in `git merge template/main` (D50's update path) on any
  upstream manifest change — since D158, every existing agent instance's.
  docs/overview/03-components.md §Templates, docs/bx.md, docs/protocol.md.
  - **Why not the served repo.** An existing instance's merge base is the
    snapshot it was seeded from (the template's JSONC) and its manifest a
    re-marshalled rewrite of it; whatever the served repo carries, git's
    line merge conflicts wherever upstream changes the manifest (checked:
    serving the normalized form still leaves B2a's additions as
    conflicts). Only a merge that isn't by lines can take them.
  - **Chosen.** `internal/manifestmerge` merges by keys (objects by
    member, arrays by element — `uses` by target), applies the instance's
    own-path rename to base and upstream, and edits ours in place in ours'
    form (an unedited old instance becomes exactly the new template
    instantiated the old way). What it can't carry is a conflict, never
    dropped: upstream changing top-level `template`/`partition` (a block
    ours doesn't carry aside), comments a manifest with comments doesn't
    take, references to the template's path where ours doesn't name its
    own. `bx template merge-manifest` is the driver — git's line merge
    first (kept when clean), by keys where it conflicts **and the other
    side is the template** (base and theirs are versions of xbin.json in
    `refs/remotes/template/*`'s history: git runs a driver for every merge
    of the file — a builder's branch, a rebase with the sides swapped, a
    stash — and exposes no MERGE_HEAD while it runs). xbind names it in
    each builtin template instance's `.git/config` and
    `.git/info/attributes` (untracked) at instantiation, on clone (the
    copy's path) and, for existing instances, at start (off the boot path)
    — confined (D78); the command falls back to `git merge-file` so a
    missing or older bx leaves git's own conflict. New instances keep the
    template's JSONC: the block goes with the comment lines right above
    it, the instance's `partition` is the line after the brace, and the
    served repo drops the same comment — an instance is the served
    manifest plus one line (in a repo this xbind created).
  - **An exception to plans/partitions/10 §A.1** ("no boot migration, no
    new file written" for workspaces without a partitioned tile): the
    start-up pass writes `.git/config` keys and `.git/info/attributes` in
    every builtin template instance's repository, partitioned or not — the
    only way existing instances' documented update command stops
    conflicting. It changes no route answer, env, header or tracked file;
    the builder can decline it (`xbin.manifestDriver = false`, their own
    driver command, the line removed, their own merge attribute for
    xbin.json) and xbind then writes nothing.
  - **Unpartitioned existing instances** take B2a's `conf`/`team` uses,
    `partitionMail` and `partitionNote` (inert unpartitioned; the
    resources arrive with scope.json anyway) and never a `partition`: the
    mode stays; switching later needs only the `partition` line.
  - **Rejected.** Serving the instance's normalized form (still conflicts,
    above); rewriting instance history or committing in the builder's repo
    (the repo is the builder's; D2: xbind never auto-commits); a committed
    `.gitattributes` (the first merge wouldn't read it, and it would ride
    into the builder's tree); an xbind-side "update" endpoint doing the
    merge (the documented command should just work, in the builder's
    terminal and their agent's); telling merges apart by MERGE_HEAD /
    REBASE_HEAD (not written while a driver runs); `core.attributesFile`
    for a lower-precedence line (it would shadow the builder's global
    attributes file).

- **D166 — Each Go tile builds with a go.work of its own, made from its
  go.mod at build time; the root go.work is for shells and gopls only
  (2026-09-30).** internal/deps/{buildwork.go, modfile.go},
  internal/runner/{gowork.go, build.go, buildcheckpoint.go}. (The numbers
  between D136 and this one, D147 aside, and the five after it are the
  unmerged partitions branch's, which left this one to this fix.) The
  owner: "go.work seems
  generally always felt sketchy to me, especially if all builds can
  influence some shared build env." D78 had given every
  confined build its own GOCACHE and GOMODCACHE, but the module graph was
  still shared: every build ran with the root go.work (99fa52da: a copy of
  it), which `use`s every Go component, and in workspace mode the go
  command runs MVS over every used module and applies every used go.mod's
  `replace` workspace-wide. So one tile's requirement bump changed the
  version every other Go tile built with, one broken go.mod broke every Go
  build, and a person who could change only tile A could point tile B's
  dependency at code of their choosing. Reproduced with the go command
  (TestConfinedGoBuildOwnWorkspace's control, and on master's confined
  build): tile A's `replace golang.org/x/sys => ./evil` and its `require
  example.com/dep v1.1.0` tied to its own code by a version-specific
  replace made B's backend print A's code. And F6 (partitions records)
  found a race: a new tile's first build could run before deps.GoWork
  listed its module — "go: no modules were found in the current
  workspace", sticky until the code changed (reproduced on master, both
  with and without isolation).
  - **Chosen: a build workspace per build, generated.** deps.BuildWork
    renders it from the registry and the tile's own files each time the
    runner builds — work tree, checkpoint, protected — and with isolation
    off too (GOWORK set; there is no boundary there, but one module graph
    per tile is the behaviour either way). It `use`s the tile's own
    modules (goModules' rule — the root or backend/ — plus the module
    holding the entry package when that is another), and each other
    module the tile reaches, and replaces the SDK as the root file does.
    Built from what the registry lists at build time, the race is gone by
    construction. The root go.work (GoWork, D40's GoWorkFor) is unchanged.
  - **Reach.** From the tile's module: its go.mod's `require` lines, its
    `replace` lines, and the imports of its Go files (go/parser,
    ImportsOnly), then on through every module used — every used go.mod's
    lines before any import, so an import is looked up only once the lines
    known by then have claimed their paths. Imports matter because
    workspace mode lets a module import another used module *without* a
    require line (verified: builds with the shared go.work, fails with only
    the tile's module) — a tile doing that must keep building. The scan
    reads every .go file but tests in every directory a package can build
    from (`_*` and testdata too: a template's entry is `./_backend`), not
    `.*`, vendor/, node_modules/ or nested modules; build tags aren't
    evaluated (a superset only adds modules the tile's own code names).
    Files are read beneath an os.Root at the module (fsutil.OpenRootIn),
    O_NONBLOCK and regular only, 20000 files and 1 MiB each at most.
  - **A reference chooses a workspace module only by the referring
    module's own lines — never a namesake.** A per-tile closure alone
    would keep a variant of the hole: tile A declares `module
    golang.org/x/crypto` and every tile that requires x/crypto reaches A;
    and a first cut that credited an import to the longest module path any
    go.mod declared was still open (review, reproduced with the go
    command): A declaring `golang.org/x/sys/unix` (with a replace hiding
    x/sys's own package), `github.com/xbin-dev/xbin/sdk/sandboxcontract`
    (every agent tile imports an SDK sub-package) or `calendar/store` got
    its code compiled into a tile importing that package, and lines in
    unrelated go.mods changed what served another tile's imports. The
    rules, read only from the referring module's go.mod and manifest and
    the modules the build already uses:
    - a `require` whose go.mod also replaces that path (at every version,
      or the required one) is the replace's alone: a directory is served
      only by the workspace module at that directory (never by path — the
      `require example.com/lib v0.0.0` + `replace => ./lib` pattern), a
      module path as a require of it would be;
    - a `require` of path p is served by a workspace module declaring p
      when p is dotless (no proxy serves `calendar`; builtins and `bx new`
      scaffolds are dotless), the version is a placeholder (v0.0.0, or the
      zero pseudo-version `go mod tidy` writes for a replaced module), the
      referring tile's manifest names the module's tile in `deps` (the
      documented way to "build against" another component — it lets that
      tile's module stand in for the path it declares, even at a published
      version, and brings its replaces: a choice of that code, documented as
      such), or only admins write it (a hand-managed use outside every
      tile). A dotted path at a published version resolves as a normal
      module even when a tile declares it;
    - an import is looked up among the workspace's modules only when no
      path a used go.mod requires or replaces, the SDK's or the go.work's
      replaces covers it (x/sys/unix under a required x/sys, sdk/ws under
      the SDK), it isn't a standard-library package (the host GOROOT), and
      no used module holds its package. Then the one workspace module that
      holds the package serves it, as the go command finds one (dirInModule:
      the directory beneath the module, no go.mod on the way — a nested
      module's — and a .go file in it): a dotless module, one nested in
      the tile's own module directory (a child component, in the tile's
      own tree), or one of a deps-named tile or an admin's. When several
      could, none does — the go command would say "ambiguous import", and
      taking either lets a namesake stand in; a deps-named one among them
      wins. A module whose
      path lies strictly under the tile's own serves only from inside the
      own module's directory (a nested component, never a namesake of the
      tile's own sub-package elsewhere);
    - never used: a module declaring the tile's own path, the SDK's or one
      beneath it (configured or not), or one the go.work replaces at every
      version. A last check drops a module served only by path or import
      whose path lies strictly under a dotted path a used go.mod requires
      at a published version, or (import only) under one a used go.mod
      requires or replaces, and resolves again without it.

    **Dotted imports without a require are no longer served by path.**
    A tile importing another tile's `example.com/lib` with no go.mod line
    built with the shared go.work; now it needs deps (or a require with a
    replace) — unless that module is nested in the tile's own module
    directory (TestConfinedBuildOfCheckpointAtCanonicalPath's
    `example.com/sub`; its sibling `example.com/y` now comes through
    deps). The alternatives: serve a unique dotted provider (a tile
    whose go.mod lacks a require for a *published* module it imports —
    exactly the tiles this change breaks — would silently compile a
    namesake's code instead of failing), or serve it unless some go.mod of
    the workspace requires that path published (the first cut: then an
    unrelated tile's go.mod line takes another tile's module away, the
    influence D166 removes). Dotless paths keep working either way: nothing
    outside the workspace could serve them, so a namesake can only make an
    import ambiguous (which failed under the shared go.work too).
    **`require M v0.0.0` without a replace is fragile regardless:** the go
    command looks M@v0.0.0 up (and fails) once a build loads the whole
    module graph — any import from a module outside the workspace
    (verified; the shared go.work had the same). Hints and docs advise
    deps, or the require with a `replace` to the module's directory.
  - **A used module brings its replace lines.** In workspace mode they
    apply to the whole build; the tile chose to build with that module's
    code, which is in its binary anyway, so its replaces give its authors
    nothing more — and dropping them would break a module that needs its
    own fork. Conflicts error as before, for the tiles that use both.
  - **A hand-managed root go.work** (no marker) was the tile build's input
    (isolation.md: "used as it is"), so its `go`, `toolchain`, `godebug`
    and `replace` lines carry into every build, and its `use`d modules are
    candidates like the components' (one inside a tile is that tile's; one
    outside every tile is admin-written and serves any reference). One that
    leaves the workspace through a symlink is never read (a warning): a
    confined build couldn't see it before either.
  - **Where it lives.** The go.work is written under the tile's (or its
    protected primary's) cache dir, `work/<16 hex digits of its sha256>/`, bound
    read-write into the build, so `go.work.sum` beside it is written only
    by builds of that same workspace — a deployment's checkpoint build and
    the work tree's no longer share one file (per-build content made the
    99fa52da single file racy). `work/` itself is bound into no build any
    more; its entries are made with openArtifacts (never followed,
    replaced when something else sits there). A new directory's
    go.work.sum is seeded from the workspace's and the tile's newest one
    (the pre-D166 `work/go.work.sum` included), so no checksum a build
    already added needs the network again; directories no build wrote for
    7 days are removed. A tile with no module builds with GOWORK=off
    ("go.mod file not found"), never the root file.
  - **A tile holding no module of its own** (apps/suite/admin: xbin.json
    and backend/ inside apps/suite's module) builds in the component module
    it sits in — the nearest go.mod above it, when that is another
    component's module or a hand-managed use, as the go command found it
    under the root go.work — read where that component's code is, its
    references that component's (review: the first cut gave it GOWORK=off,
    and it lost the SDK). A nearest go.mod no go.work used never built
    ("not one of the workspace modules") and still gets GOWORK=off.
  - **The go line** is the highest of 1.24 (the root file's), a
    hand-managed root's and the used modules' go lines, in the go command's
    order (1.24 < 1.24rc1 < 1.24.0): a go.work older than a module it uses
    is refused ("module . listed in go.work file requires go >= 1.24.0"),
    and `go mod init` writes `go 1.24.0`. (The root go.work's own `go
    1.24` is unchanged: shells and gopls, as before.)
  - **The hint names module paths and versions, never a tile.** A failed
    build whose output says "no required module provides package P" or
    "package P is not in std" gains `xbind:` lines: several workspace
    modules could provide P (their module paths — prefixes of the tile's
    own import), P is in a workspace module the build doesn't use, or a
    workspace go.mod requires its module at a published version (the
    highest). The build's output is readable by the tile's readers; naming
    the tile that requires or owns a module would leak unreadable tiles'
    names and dependency sets past D40.
  - **The root go.work is written atomically** (fsutil.WriteFileAtomic):
    every build now reads it (ReadRootWork, for a hand-managed one's
    lines), and a torn regeneration would read as hand-managed — F6's race
    in another form.
  - **Compatibility — the breaking part.** A build that relied on another
    tile's go.mod fails now: an import of a package only another tile
    requires ("no required module provides package"), an API newer than
    the tile's own requirement, another tile's replace. So does a dotted
    tile module imported without a go.mod line or deps, and a dotless
    import two workspace modules could provide (it failed as "ambiguous
    import" before, and now fails with a hint). A dotted tile module
    required at a published version now builds from the published module
    (from the proxy) unless deps names the tile. That is the hole itself
    (compat.md rule 11: a security hole closes in the release that finds
    it, with a migration note — docs/changes/2026-09-30-go-build-workspace.md).
    Every builtin tile and template, and the examples, build with only
    their own module and the SDK (checked). A protected primary's
    build.json now records `workspace: "tile"`; one recorded before D166
    compares as "inputs moved", so a restart after `.xbin/` loss holds it
    for a manager's redeploy rather than silently rebuilding it with a
    different module graph (07-runtime §3.4's rule). Checkpoint builds
    record the modules they used, not every workspace module.
  - **Not chosen:** keeping the shared graph but dropping replaces (MVS
    still lets any tile move another's versions, and a squatted path
    still wins); a synthetic module carrying other tiles' requirements for
    one release as a compat bridge (it keeps exactly the version influence
    this removes, for the tiles most exposed to it); retrying a failed
    build with more modules (the go command's error text as an API, and a
    failed build silently fixed with another tile's code); `go list` or
    `go mod graph` to compute the reach (running go on tile data needs a
    sandbox per step, and it would load the shared graph it exists to
    avoid); requiring `deps` for every cross-tile import (breaks tiles
    that import a dotless module today); crediting an import to the longest
    module path any go.mod names, or telling published paths from what
    other go.mods require (the first cut; review above).
  - **Amended 2026-10-01: the builtins' own dependencies, the root
    go.work's go line, and a release vulnerability gate.** Measured on the
    owner's workspace (66 Go tiles): with D166 no build breaks and no
    vulnerability becomes reachable, but 25 tiles silently link older
    dependency versions — the shared graph had lifted each to the highest
    version any tile's graph reached (partly by accident: one tile's
    indirect x/tools line un-pruned libc's x/tools→x/net→x/crypto chain).
    The owner chose isolation plus an admin alert naming what each owner
    should add (done separately), and fixing the builtins' stale
    dependencies at the source behind a release gate (this). govulncheck
    over every shipped module found 11 reachable advisories:
    sandbox-terminal's x/crypto v0.48.0 (ten in x/crypto/ssh, fixed by
    v0.52.0–v0.56.0) and the agent template's x/text v0.3.8 (GO-2026-5970,
    through goja); none in xbind's programs built with the release
    toolchain (go1.27.0, its standard library judged — see the review
    below). Bumped: sandbox-terminal (v6) to x/crypto v0.57.0, the agent
    template to x/text v0.42.0 (with x/sys v0.48.0 and modernc.org/sqlite
    v1.60.1), xbind's own go.mod to x/crypto v0.57.0 (the root module vets
    sandbox-terminal's backend). Every fix
    needs a go line above 1.24 (x/text ≥ v0.39.0: go 1.25.0; x/crypto ≥
    v0.56.0: go 1.26.0), so those go.mod files say go 1.26.0, with three
    consequences:
    - **The root go.work's go line is the highest of 1.24 and its
      modules'** (goModules), as a build's already was. The go command
      refuses a go.work whose go line is below a module it uses, for every
      command run with it — so the fixed `go 1.24` broke every terminal's
      go as soon as one tile said go 1.24.0 or later, which `go mod init`
      writes (sandbox-terminal's go.mod comment had worked around exactly
      this). Only a valid version, read beneath the root and never through
      a symlink, raises it; a workspace whose modules all say ≤ 1.24 gets
      the same bytes as before.
    - **The base rootfs's Go goes 1.24.0 → 1.26.3.** check-pins wants it ≥
      every shipped go line, and now also ≤ install.sh's GO_MIN: `go mod
      init` in a terminal writes the terminal's Go version as the tile's go
      line, and xbind builds that tile with the host's Go (1.26.8, the first
      pick, would have made each terminal-made tile's build fetch a
      toolchain on installer-provisioned hosts). A terminal on an older
      base (1.24.0) switches toolchains through GOTOOLCHAIN=auto (checked:
      go1.24.0 with a `go 1.26.0` go.work builds both a go 1.24 and a go
      1.26.0 module; with `go 1.24` it refuses both).
    - **Agent instances take it by the D50 merge**: a stock instance's
      go.mod (go.mod.tile renamed, its module line its own) and go.sum
      merge cleanly from master's embed to this one (checked).
    The gate: `make vulncheck` (hack/vulncheck) runs govulncheck over
    xbind's ./cmd/... with the repo's go.work (as `make build` builds
    them: CGO_ENABLED=0, linux/amd64 and, as target xbind/arm64,
    linux/arm64), the relay, the sdk, and every module in the embedded
    trees against its own go.mod.tile and go.sum, read-only, through a
    go.work shaped as the D166 build's (the sdk replaced by the checkout);
    it fails on a reachable finding hack/vulncheck-allow.txt doesn't list
    (`<id> <target> # why`, empty). `make release` runs it before the tag,
    --no-check or not; not in `make check` (network). Standard-library
    findings gate xbind's programs and the relay only — judged against the
    go running the gate, which is the one building the release; a tile's
    standard library is its host's Go. Not chosen: scanning ./... as
    "xbind" (it counts the builtin backends the root module holds for
    `make vet`, and every exported function as an entry point); keeping go
    1.24 lines over go-1.26 dependencies (an untidy go.mod that `go mod
    tidy` in a terminal turns into the failure above, and go1.24.0 refuses
    the dependency anyway); jq for the JSON (publisher hosts aren't assumed
    to have it). Open: install.sh's Go 1.26.3 (= go.mod's) has 9 reachable
    standard-library advisories in xbind's programs (GO-2026-4970, an
    os.Root escape, among them) — what a from-source install builds xbind
    and every tile with; raising GO_MIN, go.mod and the rootfs to 1.26.8
    together is the owner's call.
    Review (2026-10-01), fixed:
    - **The stdlib half of the gate was off on the release machine.**
      govulncheck v1.8.0 reads the standard library's version only from a
      bare release GOVERSION; for `go1.27.0-X:nodwarf5` (the owner's
      /usr/bin/go) or a devel build it matches no standard-library
      advisory and reports none. The gate now passes each target
      GOVERSION=<the go's release> and fails (exit 2) on a go naming no
      release; parse refuses an unreadable go_version for a target the
      standard library gates. Re-run so: xbind and xbind/arm64 clean under
      go1.27.0; under CI's go1.26.3, 9 (xbind) and 8 (relay) — the open
      item above.
    - **Builtins scanned read-only, as built.** -mod=mod could scan versions
      the go command fetched or added instead of the shipped ones. Each is
      now scanned and tile-checked (hack/tile-check.sh too, which ran `go mod
      tidy || true`) through its own D166-shaped go.work, -mod=readonly: a
      missing requirement or checksum fails. Module mode was not chosen: it
      refuses master's coding-sandbox go.mod (`go 1.24` over go-1.24.0
      dependencies), which the real workspace-mode build accepts.
    - **coding-sandbox keeps master's dependencies.** Its only finding,
      GO-2026-5024, is in x/sys/windows and never called; the bump bought
      nothing and put every coding-sandbox instance at go 1.26.0 (below).
      Tidying its go line to 1.24.0 would hit the same downgrade break.
    - **BREAKING, with a migration note**
      (docs/changes/2026-09-30-builtins-go-1-26.md). Once any module says go
      1.26.0, every `go` in a terminal on an old base (Go 1.24.0) downloads
      go1.26.0 first — or fails where its network can't reach
      proxy.golang.org — and a downgrade to v0.3.64 or older fails every Go
      tile's build (its root go.work says `go 1.24`, checked with the go
      command). Remedies: ⬆ base update; setting those go lines to `go
      1.24` before a downgrade (checked: a read-only workspace build with
      host go1.26.3 accepts sandbox-terminal at go 1.24 over x/crypto
      v0.57.0). docs/compat.md names the downgrade limit.
    - **An open restricted terminal's go.work follows the workspace**
      (term.RefreshViews, from the watch loop and structure changes): its
      view is staged once at open, and a stale `go 1.24` would refuse every
      go command after a builtin update or a raised go.mod. Rewritten in
      xbind's view dir (temp + rename) under the lock dropView takes.
    Not done: a binary-mode scan of prebuilt helpers (bin/gocryptfs,
    x/crypto v0.33.0, 21 imprecise advisories on a stripped binary) and the
    rootfs's gopls/dlv — the docs and the allow file now say they aren't
    scanned.
  - **Amended 2026-10-01: the silent downgrades, and the alert that names
    them (G1).** internal/runner/{goversions.go, goversionscheck.go,
    goversionsgo.go, goversionsreport.go}, internal/deps/sharedwork.go,
    internal/boot/goversions.go. Measured on
    the owner's workspace (66 Go tiles): no build broke and no
    vulnerability became reachable, but 25 tiles silently link older
    dependency versions — under the shared go.work MVS lifted every tile to
    the highest version any tile's graph reached; per tile each falls back
    to its own go.mod. 20 of the 25 are one pattern (modernc.org/sqlite
    v1.39.1→v1.34.5 with libc, mathutil, memory, x/sys, x/exp); the rest
    x/crypto, x/text, x/net, x/sys, coder/websocket. The shared versions
    were partly accidental: one tile's indirect x/tools line un-pruned
    libc's x/tools→x/net→x/crypto chain in the shared graph. The minimal
    require set restoring each tile's old list was 31 lines for 25 tiles
    (one line, sqlite's, covers 20). The owner: ship isolation, and an admin
    alert telling owners exactly what to add (G1); fix the builtins' own
    stale deps at the source with a vulnerability release gate (G2, apart).
    - **Chosen: compute it, per tile, with the go command, confined.** For
      each Go tile, `go list -deps` of its entry (listFormat: each
      package's module, version and replacement) twice: with the shared
      go.work (deps.SharedWork: the root go.work as builds used it — xbind's
      rendered from the registry, or a hand-managed one's lines — paths made
      absolute) and with its own build workspace (BuildWork, as the build
      renders it). Each runs as the tile's build does (goBuildCmd's binds,
      per-tile caches, network, CGO_ENABLED=0, GOFLAGS with -mod=readonly
      added unless it sets -mod; isolation off: as that build runs), with
      its go.work and go.work.sum in the tile's cache dir, versions/, made
      and written without following links (openArtifacts, writeAt) and
      bound into no build. A change is a module linked lower, no longer, or
      newly; a workspace module, a replaced one (another tile's replace is
      the hole) and a higher version are not.
    - **The fewest lines, greedily, verified.** Candidates are the raw
      differing lines (each module linked lower or no longer, at the
      version it had), the tile's go.mod's direct requirements first. Each
      round lists the build with each candidate added to the lines chosen
      so far — through a module of its own the go.work uses
      (deps.PinGoMod: in workspace mode every used module's requirements
      are roots, so it builds as if the tile's go.mod had the line; checked
      with the go command: the same list as adding the line to a tidy
      go.mod, or raising the existing one) — keeps the one leaving the
      fewest changes (one leaving none ends the round), and repeats until
      every module it had is back; then drops a chosen line the others make
      redundant. 24 lists at most per tile; when they run out or no line
      changes anything, the answer is the raw differing lines (minimal:
      false). The sqlite pattern takes one list (sqlite is the tile's own
      direct requirement), the un-pruned chain one line (x/net's).
    - **When.** Once on its own: Boot (the registry step, before any
      build) finds data/go-build-versions.json absent and a build of an
      earlier xbind — a Go tile's .xbin/build/<key>/bin, or a checkpoint
      artifact (shared or a protected primary's) whose build.json records
      no workspace of its own — and starts the pass in the background
      after the boot (30 s later, two tiles at a time), recording each
      checked tile so a restart resumes; done is the marker. A workspace
      with no such build gets the marker at once (since = the running
      version either way: the alert's "since <version>"). **The tiles it
      compares are fixed then:** the Go tiles the workspace has at that
      boot (the baseline set), and what each linked under the shared
      go.work is stored for every tile compared, affected or not (its
      baseline). Again on an admin's POST /go-build-versions/check: each
      tile of the set compared with its baseline (only modules linked both
      ways count: its code may have changed), the shared go.work listed
      only for a tile without one; a tile added since is never compared
      (it never built with the shared go.work: "to keep what it had" would
      be false), and a fresh workspace answers that there is nothing to
      compare. And a tile the alert names is listed again after a build of
      it — work tree or checkpoint — against its baseline, when what
      decides its versions changed since it was checked (a digest of the
      entry, its build go.work and every used module's go.mod: MVS reads
      nothing else; deps.Work.Inputs); its line goes once none is lower, or
      narrows to what is left. The work tree is what is listed, a pinned
      primary's too: its go.mod is the one the alert says to change and
      the next deployment builds.
    - **The surface.** One admin-only alert (kind go-build-versions, warn;
      Broker.AdminAlerts, never a tile reader's: it names tiles and their
      dependency sets, D40) naming every tile not dismissed with its lines
      — tiles needing the same lines named together, since one alert per
      tile would put 25 banners in the shell — with dismiss, the route that
      dismisses it (POST /go-build-versions/dismiss {tile?}; a tile comes
      back when its lines change); GET /go-build-versions for `bx doctor`
      (each tile, its lines, every change, the tiles it couldn't compare).
    - **Not chosen:** editing tiles' go.mods (a tile's code is its writers';
      the alert says what to add); the synthetic bridge module (above:
      keeps the influence D166 removes); `go mod graph` to predict which
      line lifts what (its pruned graph doesn't hold the higher versions'
      edges, and the list with the line is the proof anyway); comparing
      the whole build lists including drops and adds on a re-check (a
      code change drops modules no line brings back).
    - **Review (2026-10-01).** The shared go.work as first rendered (`go
      1.24`, every module used) can't hold two shapes D166 made
      buildable, and the go command then refuses it for every tile (checked
      with go 1.26.3): a module at a go line above 1.24 (`go mod init`
      writes `go 1.24.0`: "requires go >= 1.24.0, but go.work lists go
      1.24") and two tiles declaring one module path, a copied tile
      ("appears multiple times in workspace"). SharedWork now raises its go
      line as BuildWork does (the highest of 1.24, a hand-managed go.work's
      and the used modules'; it doesn't change what MVS selects), uses one
      of several namesakes — the one the tile's build uses, else the first
      (a copy's go.mod is the original's) — and drops a module the go.work
      replaces at every version (a tile declaring the SDK's path). A shared
      list that fails anyway is checked once per pass with `go list -m`
      on the same go.work (confined like the lists; it loads only the
      workspace's modules): when that fails too, the go.work is at fault —
      one workspaceError, no per-tile copies, no further list of that
      go.work in the pass; the tiles stay without a baseline for the next.
      Also: the alert and the report skip a tile no longer a Go tile (a
      pass drops it from the state); the first pass checks it is still due
      after the delay (an admin's pass may have completed it); Stop (at
      shutdown) cancels the lists' context and waits, recording nothing a
      stopped list left half done.

- **D167 — Partitioned tiles, B2d: the agent's non-secure (hosted)
  conversations and "Add a copy of my …" (2026-09-30).** Implements PD-32,
  PD-33 and the agent's side of 90 §I4 (global is the realtime hub) of
  plans/partitions/90-decisions.md; the design is
  plans/partitions/08-agent-template.md §4, §9. The template's API.md
  "Non-secure conversations"; docs/partitions.md §The mode.
  - **Chosen.**
    - **Transcript in `team`, with the agent's own run schema**, numbered
      from 2^39: the host drives it with the unchanged engine, global reads
      it with the unchanged view/stream code, and a hosted id is below 2^40
      (pages reach it at global, `homes.js` unchanged) and above global's
      own ids (global tells it by the number). Global re-applies
      `migrate()` to team at every start; a partition compares team's
      columns with its own code's schema and waits for global (W3b's rule).
    - **The host's own `hosted` table is the authority**; `team_hosts`
      (host, state, pending) is a display hint. The host engine is a second
      `Engine` over team with its own lock and epoch key per partition id,
      a scope checked at every pass (and before a run is marked driven):
      active in the table, audience within the confirmed snapshot. Global's
      engine never drives team (its team view is never started).
    - **Members at global use the routes of any shared conversation**:
      `hostedRoute` serves a route on a hosted id from team (a curated set;
      the rest 409), so the page needs no new calls; inputs go into team's
      inbox and ring the host by partition mail (`hosted/input`, coalesced
      per host; signals for interrupt/cancel/audience), which wakes a
      stopped partition.
    - **Live (90 §I4)**: the host engine's hub taps into a batched F5 post
      (`POST /hosted/events`, 20 ms, drafts coalesced), which global
      publishes on its own hub for the members' streams under team's ACL —
      run summaries re-read from team, only a run's own event types, only
      for conversations team says the caller hosts. Mail (`hosted/changed`)
      is the durable fallback when the post fails.
    - **Widening pauses** (a member added, the team let in, a viewer made a
      participant, another owner): the host engine stops at its next look
      — or at once on the audience ring — abandoning the step in flight (a
      model call writes nothing and is made again after the confirmation;
      a tool call ends as after a restart); the host is asked on the page
      and by a push; members' inputs 409; the host confirms exactly what
      they were shown (`seen`) or
      declines. Declining, taking the resources back, 7 days unanswered or
      the host's partition refusing mail for good end hosting; any
      participant then continues it at global without them (a copy back).
    - **The warning (PD-33)**: a modal each time a hosted conversation is
      opened into a page session (and before hosting), listing its members,
      the agent's managers, workspace admins and anyone who can change its
      code, and whose resources it uses; Start anyway / Open without
      sending; no "don't show again"; the composer locked until started; a
      ⚠ not private chip on the header and rows; the native view the same
      in its vocabulary.
    - **"Add a copy of my …"** copies a person's own session files into a
      shared conversation at global (`from-<person>/`, a note): no host, no
      pause, no chip; the originals stay private.
    - **Failure paths**: the move is two-phase (the partition's intent, a
      `pending` copy the partition acks; idempotent by the original's id; a
      copy never taken up is continuable after 10 minutes, one whose original
      stayed is deleted); continue claims the conversation and leaves a
      tombstone; the host engine looks at the audience before every step
      (paused again on a further change, resumed when it narrows back,
      dropped when the host is no longer in it); global re-reads every
      durable live event from team; a hosted run lacks the tools that keep
      state outside it (schedules, automation threads, skills); approvals
      are the host's; un-sharing ends hosting (90 §I10) — then AF's move takes a person's chat home.
  - **Not chosen:** a mirror of the transcript in global's db (two copies
    and id clashes; team is the ruling's home); making every handler take
    its agent from the request (≈250 call sites in shared files); a
    per-resource choice (the engine runs as the host's partition, which
    xbind attributes as a whole — a partial one would be advisory); mailing
    `hosted/changed` after every commit (the live post carries it; mail is
    the fallback); join links on hosted conversations (they'd admit people
    the host never saw); giving team's schedules their own id range and a
    fire route the host serves from team (a hosted schedule would fire with
    the host's resources long after the members stopped looking — the
    members can schedule after continuing it without the host); refusing
    to un-share a hosted conversation (a member couldn't leave).
- **D168 — Partitioned tiles, AF: the agent's un-share moves a chat
  home, staged handoff files, no first-DM notice (2026-09-30).**
  Implements the owner's rulings 90 §I10, §I11 and §I12 of
  plans/partitions/90-decisions.md (08 §3, §5), B2b's sandbox-picker seam
  and W4's flag on a channel's runs in a partition. The template's API.md
  "Partitioned instances"; docs/agent-inbox.md; docs/partitions.md §The
  mode.
  - **Chosen.**
    - **Un-sharing is the act's, on a chat** (`moveIfUnshared(root, was)`,
      homes_move.go): in the transaction of `PATCH` visibility or of
      removing a member (a member leaving), whoever acts, when the
      conversation was shared before the act (`sharedAtGlobal`: team, or
      someone in it) and isn't after, and it is a person's chat (origin
      `chat`/`api`/none, something said in it) — a `conv_moves` row
      ("asked", a random key) and a `conv/move {run, key}` mail to
      `user:<owner>` through B2c's handoffs queue (per person, retried,
      refused-for-good and 7-day give-ups abandon the move). An
      automation's thread, a draft, a PATCH that changes nothing about who
      shares it: nothing. Its join links are revoked; every change to it
      answers 409 (`refuseWhileMoving`, in `globalRoute`) except reading,
      marking read, `/cancel`, `/interrupt`, `/approve`, taking back a
      queued message and `/answer` to a run waiting for one; an
      automation's delivery into it is refused (`errConvMoving`,
      `deliverInboundTx`).
    - **The partition drives the move** (homes_move_user.go) with the
      mailed key — xbind stamps a person's page and terminals as it stamps
      their partition's backend, and the backend takes the mail as it
      arrives: the mail is recorded (`moves_in`); off the mailbox, one move at a time
      with backoff: delete an earlier attempt's hidden copy → `GET
      /moves/{id}/export?key=` (409 while anything in the tree is under
      way: an `active()` status, input not taken, a sandbox command; the
      bundle with files to 16 MiB inline, the rest one by one from `GET
      /runs/{id}/raw`; its notes, its owner's schedules reporting into it,
      their pin and archive, `behind`, and a ticket — global notes the
      tree's mark) → import hidden (origin `held`, session `move:<id>`),
      notes with it → "arrived" → `POST /moves/{id}/done {to, ticket,
      key}` → the copy shows, its schedules made (private, reporting into
      the new id), the pin set, "done". Global's done: the ticket must be
      the latest export's and the tree at rest and unchanged (its mark),
      else 412 — the partition drops its copy and reads it again; then
      "leaving" (to), the tree deleted with the schedules that went, its
      event carrying `movedTo`, "moved" (a 30-day tombstone). A
      conversation gone while "asked" left another way (B2d's hosting):
      409, no move; the partition drops its copy. Idempotent at every
      step; a stopping partition with a move under way asks to be started
      again (`userWake`). What the partition can't take — too large, a
      bundle it refuses, past its file store's limits, a class the person
      may no longer use — is given up at global (`abandon`); an export's
      404 is checked against `GET /moves/{id}` before the move is dropped.
      Anyone but the owner: 404, and done `{state: "gone"}` for every id.
    - **Never two homes**: global lists it until `done` committed, the
      partition from its flip; between the two it is listed nowhere and
      opens by its new id (the tombstone and the event name it).
    - **Only its owner's page follows it** (model/moves.js `followsMove`):
      a person's partition page, their own conversation; an address whose
      view 404s asks global's `GET /moves/{id}` first.
    - **Staged handoff files** (handoff_fetch.go): a DM's staged channel
      file past the mail budget stays at global, held (`handoff_files`,
      kept by the upload prune, its 8 days from the latest attempt to mail
      it), named in `handoff/dm`'s `fetch`; the partition reads it in
      `prepareMail` from `GET /handoffs/{id}/files/{fid}` (the handoff's
      person only, from their own partition) and acknowledges after the
      commit (`POST /handoffs/{id}/fetched`: deleted); a fetch that fails
      leaves the mail for the next pull; a file gone meanwhile is said in
      the DM's text. The reply direction mirrors it: `PUT
      /handoffs/{id}/reply-files?key=<outbox key>-<row>/<file>`
      (idempotent by key; a DM under 30 days; 10 files a chat — 413, the
      reply goes without it; 32 files / 64 MiB a person not yet sent —
      507, later) before `outbox/add` names it in `staged`; global takes
      only the caller's own staged files for that handoff, and deletes
      them at the adapter's ack like inline ones.
    - **No first-DM notice** (90 §I12): the partition's
      `partition/hello` stays, for the chat's typing status only (`idle`
      until the partition ran).
    - **A channel's runs from both homes** (automations_global.go): in a
      person's partition an automation the global instance keeps (a
      channel; a trigger registry row below 2^40) counts the global
      instance's runs with its own, and `GET
      /automations/{kind}/{aid}/runs` asks global the same page below the
      same cursor and merges by (activity, id); `POST …/read` marks both.
    - **The sandbox list at the conversation's home** (model/
      sandbox-store.js): one list per home; while a shared conversation is
      open `GET /sandboxes` and every call about a sandbox go to the
      global instance; the old-manager banner reads the partition's own.
  - **Not chosen:** global pushing the transcript to the partition by mail
    (a bundle up to 48 MiB against a 1 MiB item); the partition's copy
    visible before global's delete (two homes); cancelling a working
    conversation at the un-share act (the export waits for it to rest);
    refusing the un-share act when the conversation is too large (a member
    leaving can't be refused); moving an automation's thread (its channel
    session and outbox mapping stay at global); carrying subagents'
    transcripts, sandbox bindings or grants (they are the shared space's);
    asking xbind whether a person's partition ever ran; merging a
    channel's runs in the page.
- **D169 — Partitioned tiles, SH: the settings menu's "your partitions",
  one time format, and `yours` on a tile with a global instance
  (2026-09-30).** Implements the owner's ruling I13 (a user-menu entry to
  `/xbin/partitions`, shown when the workspace has a partitioned tile) and
  settles two things W4-wire flagged: the admin console's times without a
  zone beside the partitions page's named zone, and the chip `yours` on a
  partitioned agent's window, which since B2b also shows the global
  instance's shared conversations (I3). Design:
  plans/partitions/90-decisions.md §I3, §I13; records/W4-wire.md "Looks
  wrong" 1–2; docs/partitions.md.
  - **Chosen.**
    - **The entry lives in the settings menu's *my account* block**, after
      **devices…** and styled like it: the shell's per-person menu (its
      "user menu"), where a person's own things already are. Label
      **your partitions** (the menu's lowercase words), the marker's
      half-split disc in its teal before it and ↗ after it (it leaves the
      shell), tooltip "Your partitions page: your own data in each
      partitioned tile, the consents and personal binds you gave, and the
      switches you decide — xbind's page, in a new tab". A plain link
      (`target=_blank rel=noopener`): the page refuses frames, so it opens
      top-level; choosing it closes the menu.
    - **When it shows** (`partition-mode.js pageEntry`): a signed-in
      person (`consentPerson`: not view-as — the page shows view-as none
      of that person's partitions —, not the workspace token, which has no
      partitions of its own) whose `/components` listing holds a
      partitioned tile — the marker's rule, `anyPartitioned` (a recorded
      mode with user partitions; a switch into partitions still pending
      isn't one yet, and its card links the page with **details…**; one
      out of partitions still is, while people's partitions exist). The
      same rule as the consent prompts' `consentWatch`, which now calls
      it. "The workspace has a partitioned tile" is read from the tiles
      the viewer sees, so it needs no new request or field, and follows
      the listing live (the shell reloads it on `reload`/`users`/`grants`
      events).
    - **The "my account" block moved to a module of its own**
      (`workspace-template/shell/shell-account.js`: `accountMenu(shell)`
      — the identity line, the password form, devices…, the entry): bx-shell
      sat at its size budget (1892/1892) and now renders
      `${accountMenu(this)}` — 1853 lines. The password change is the same
      calls and messages. The marker's drawing is `shell-kit.js
      markShape`, shared by the marker and the entry.
    - **One time format for partitions:** the admin console's partitions
      view (`partitions-view.js when`) and the logs panel's shared-log
      entry (`web/logs-partition.js`) use the partitions page's format —
      local time, zone named ("2026-09-30 17:46 GMT+2", `Intl`
      `timeZoneName: 'short'`) — where they showed UTC unmarked
      (`toISOString`, minutes or the date). The logs module imports the
      page's `timeText` (core to core, one binary); the admin tile is
      scaffold, so it keeps a copy (a scaffold importing a core module's
      non-contract export would break when that export moves), and
      `hack/admin-partitions.test.mjs` asserts the two agree.
    - **`yours` stays `yours` on a tile with a global instance; its
      tooltip names the global instance** (`CHIP_YOURS_GLOBAL`): "Your
      partition: this window shows your own data in this tile — everyone
      who uses it has their own, and nobody else's partition shows here.
      Anything here that the tile shares with everyone who uses it (a
      shared chat, for example) comes from its global instance, not from
      your partition". The chip names the partition the window's calls
      reach and its credential — which a page's shared view doesn't change
      — so it is accurate; the tooltip removes the misreading ("nobody
      else's data shows here") for every tile with a global instance, not
      only the agent: any such tile's window may show global's data (F5).
      A tile without one keeps F14b's tooltip, with "nobody else's" made
      "nobody else's partition" (a shared resource written by someone else
      may show on any partitioned tile).
  - **Not chosen:** the entry at the settings menu's top beside **add a
    device** (that slot is the menu's one call to action, and the harness
    pins it as the first item); a top-bar chip (the bar is the workspace's,
    not the person's; the menu is per person); showing it for the
    workspace token (the page offers it only the switch decisions, which
    each paused card links) or for view-as; an xbind field or request for
    "the workspace has a partitioned tile" (the viewer's own listing
    answers it, and a tile they can't see isn't theirs to open a partition
    in); a chip that follows the open conversation (`global` while a
    shared conversation shows — it needs a frame → shell message the agent
    would have to send, applies to one template, and would make the chip
    change within one window whose calls still reach the person's
    partition); a fifth chip word (`yours + global`, `mixed`: I3 names
    three, and W4 built a fourth only where no partition is reached);
    UTC with its name in the console (the page shows local time: one
    format was asked for, the page's).
- **D170 — Partitioned tiles, SDK and docs finalized: realtime patterns,
  Context mail calls.** Extends the F8 skeleton's entry.
  - **Realtime between partitions is documentation, not a primitive** (90
    §I4): three patterns, each named by the job it does, on what partitioned
    tiles already have — tile-wide live state is a shared resource and a
    shared bus (reaches every reader of the tile; pages subscribe before
    they read the snapshot, so a change during the read isn't lost);
    member-scoped live state has the global instance as hub (pages attach
    through their own partition's F5 call, partitions post with
    `GlobalURL`, the hub checks the poster's membership at every post and
    stamps the sender from the call, never the body); waking one partition
    is partition mail (durable, doorbell). The member gate is "From is this
    tile, X-XBin-Partition is user:<User>, no view-as": exactly the calls a
    person makes through their own partition; other tiles, the root token
    and public requests act for no member.
  - **The hub, not xbind, ends a stream when its person loses the tile.**
    xbind judges access when a call arrives and doesn't end a stream it
    already let through, so a person removed from the tile, disabled or
    deleted would keep receiving a room's posts. The example's hub asks
    xbind about each follower at a post (`xbin.AccessOf`, a yes trusted for
    10 s) and drops one who can no longer read the tile — out of the room,
    their streams closed; members also leave, or are taken out by a member
    (`DELETE /rooms/{room}/members/{user}`, the example's rule). Not
    chosen: re-checking only on follow (it misses the open stream), or an
    xbind primitive that severs proxied streams on access loss (a platform
    change beyond docs — §Owner questions 2).
  - **A shared (`true`) resource's row key is not proof of its author**:
    anything acting in any partition may write any row or publish any
    topic on a shared bus. The page says so, and routes author-bearing
    state through global (a `"read"` resource, the author stamped from the
    call).
  - **The worked example is code that runs, and the page can't drift from
    it.** The Go backend is `Example_realtime` in the SDK
    (sdk/example_realtime_test.go, compiled by `go test`), run by a test
    against a stand-in xbind that plays each instance by `XBIN_PARTITION`
    and forwards `?xbin-partition=global` as xbind does; internal/docscheck
    checks every Go block of the section is quoted line for line, in order,
    from that file (blocks may elide at `// …` lines); the section's
    JavaScript blocks are taken from the page and run against
    xbin-client.js (hack/partitions-realtime.test.mjs). Not chosen: an
    `examples/` tile — it would need a real partitioned xbind to test, and
    the SDK's example is what `go doc` shows builders.
  - **Context variants, named `…Context`** (database/sql's convention; the
    SDK had none yet — `NotifyUser` and `AccessOf` take ctx first because
    they were born with it): `MailContext`, `MailWithContext`,
    `InboxPageContext`, `InboxContext`, `AckContext`. The plain calls are
    the variants with `context.Background()`: same requests, same answers,
    no deadline added behind a caller's back (compat rule 8). An error from
    an ended context wraps it (`errors.Is(err, context.DeadlineExceeded)`),
    including one that ends while a 200's body is read. A read that gives
    up loses nothing; an ack that gives up may have taken effect (acking
    again is nothing to do). **A send that gives up — or fails without
    xbind's answer — may or may not have made its item, and the sender
    never learns the id**: sending again makes a second item with a new
    id, which dedupe by item id (redelivery of the same item) doesn't
    catch, and `POST /partitions/mail` takes no idempotency key. A sender
    that retries puts its own key in `data` (the source event's id) and the
    addressee dedupes by it — as the agent template's handoffs (`ho:<id>`)
    and outbox (`Key`) already do — or doesn't retry. Not chosen: an
    idempotency key on the route (xbind surface; the data key covers it).
  - **The node and python mail snippets use each runtime's standard library
    alone** (node's `http` over `socketPath`, python's `http.client` with an
    AF_UNIX connect), with a silent-minute timeout; a non-JSON 200 rejects
    the call (node parses inside a try: a throw in a response listener
    would crash the backend). They are run from the page against a
    stand-in gateway (hack/sdk-mail-snippets.test.mjs; python3 when
    present).
  - **The page's status stays "in development until the release notes say
    otherwise"**: no release carries partitioned tiles; the I2 rollout's
    release notes lift it and the other docs' "(in development)" notes
    together. Its "Items marked TODO" sentence is gone: none is left.
- **D171 — Partitioned tiles, I1: where the end-to-end, compat and
  security suites live, what they don't run, and where they run
  (2026-09-30).** plans/partitions/10 §A, §B.2; 95 "I1"; records/I1.md.
  - **Chosen.**
    - **The security suite is `test/isolated/partitions_security_test.go`**
      (its longer cases in `partitions_security_cases_test.go`), not
      `test/`: people's partitions run only under `--isolate` (PD-19) and
      the partition fixture (S1's probe, people, frames) lives in
      `test/isolated`. Its header maps every row of 10 §B.2 to the test
      that makes the attempt — its own subtests (tokens, a read-shared
      resource, events, terminals, host network, the global instance's
      reach, notify, mode deciders, bind authority, personal binds,
      consent, held credentials, a person who lost read, a crash), the
      existing smokes' subtests, or, where only a package test covers a
      part of a row, that test named "in-process" — so no attempt is made
      twice and a row without a test shows. Every "it did not reach X"
      check has a positive control: X's socket gets an event of its own
      first and last, a person's refused layer is seen by their own next
      session, a refused personal-bind call is made by its owner too.
    - **`test/partitions_test.go` is the non-isolated half**: the mode rule
      (auto, pending, keep compared key by key, switch — refused towards
      user partitions for want of isolation, towards unpartitioned
      emptying every store and telling the people whose partitions went —
      a rollback onto code that asks differently, kept and then switched,
      a template instance) and what a non-isolated xbind serves of a
      partitioned tile (its global instance: root token, cron, bus,
      ingress, a person's frame asking for global; people refused at the
      backend, fail closed; xbind's data plane on a person's own
      namespace, pinned). The consent policy and personal binds need
      running partitions: the security suite.
    - **The sandbox-manager suite through xbind**: the `user-partitions`
      section's consumers are partitioned tiles and a partition is its
      person's page, so the manager meets xbind's own
      `X-XBin-Partition(-Id)`. `global` and `global-home` run through
      xbind; `apart`, `shares`, `person` and `recreated` are skipped,
      each naming why, and `user-partitions-xbind` makes their point with
      real people and real ids. A remote target before partitions or
      without `--isolate` skips the section, said so.
    - **The old-scaffold harness pass** swaps the workspace's `shell/` and
      `tiles/admin/` for the last release's (a git tag) and restores them
      byte for byte; it needs the workspace's own copies served, so the
      harness gains `HARNESS_NO_OVERLAY=1` (no `--dev-overlay`); under the
      overlay — the default run — the pass SKIPs, said so.
    - **Where they run**: the isolated suites (security, the smokes, the
      downgrade, the contract suite) need user namespaces, a base rootfs,
      gocryptfs and — the downgrade — the previous release's binary: CI's
      integration job has no rootfs, so they SKIP there, and they run on a
      dev box and in the release checklist (records/I1.md §Where these
      run). In CI: `TestPartitionsNoIsolate` and `TestNoPartitionGolden`.
  - **Not chosen:** duplicating S1's fixture in `test/` to put the
    security suite there; changing `sdk/sandboxcontract` (a builder-facing
    package) so its skipped checks can run through xbind — an owner
    question below; an end-to-end `bx builtin update` of a builtin whose
    upstream adds `partition` (the embedded builtins can't be made to
    differ in an e2e; `TestBuiltinUpdatePROnlyPartition` and
    `TestUpdaterReadsRecordedPartition` cover it in-process).

- **D172 — Coding agents under partitions: only in a person's own
  conversations (2026-10-01).** The owner's ruling
  plans/partitions/90-decisions.md §I15 built into the agent template and
  the sandbox contract (plans/partitions/96-agtt-merge.md, W6: records
  W6-F, W6-A, W6-U, W6-wire); amends D147 (coding agents) for partitioned
  instances (D158). The template's API.md "Partitioned instances" →
  "Coding agents only in your own conversations" and "Coding agents" →
  "In a partitioned instance (the UI)"; docs/sandbox-manager.md
  §Partitioned consumers, §Terminals, §stdio, §hello.
  - **Chosen.**
    - **A coding agent works only where its sign-in stays its person's**:
      a conversation of their own partition, in a sandbox homed there (a
      sign-in lives in the sandbox's `$HOME`). One predicate,
      `harnessBarred(run)` (the global instance, or a hosted run), checked
      at every door (`/ask`, `/runs`, the spawn tool's schema and call,
      sign-in and the login terminal, the catalog: `shared-space`) and in
      the engine (`harnessUse`: every spawn, attach and re-check), so no
      route or run — a channel's, a trigger's, a schedule's, a planted one
      — reaches an adapter at the global instance.
    - **No hosted coding agent**: a hosted run spawns none, one a coding
      agent answers can't be hosted, and a hosted conversation keeps off
      its host's sandboxes where a coding agent of theirs signed in or
      worked.
    - **No moves**: `exportConv`, which every copy between homes uses,
      refuses a coding agent's root and a tree whose coding agent is at
      work or still up (409 `{error, runs}`); the owner's un-share is
      refused inside its transaction; a member's own leave goes (it stays
      at the global instance with its owner).
    - **A partition's lifetime follows work**: only a coding agent at work
      (or a sign-in the agent awaits) holds a person's partition; an idle
      one leaves one `wake` at its idle stop, and the woken takeover counts
      from its last activity. **The wake-up is kept registered while the
      partition idles** — not only left at its exit — because xbind
      revokes a stopping partition's token before its process stops
      (D143): the exit's cron calls were refused (the e2e's bug), for every
      kind of wake-up of the partition, not only coding agents'.
    - **The brake by reading**, as a built-in turn does: the harness
      pass's halt branch is `onBrake`, a live turn looks at the cached conf
      at each event, and one engine timer looks every confTTL while a
      coding agent works.
    - **Sandboxes by home**: the relays (terminal, sign-in, log) check the
      hello and the home like every use; `GET /sandboxes` in a partition
      says `homed`/`why` and the pickers — the coding agent's and the
      built-in agent's — grey what isn't; the catalog keeps and probes
      homed ones only; the team's sandboxes are listed in a person's
      partition (the manager shows them `shared`), read-only there.
    - **The page follows one pure model file** (`model/harness-homes.js`):
      where one starts, whose sandbox fits, where a sign-in is offered
      (read-only elsewhere), a shared new chat's "Who answers" (the
      built-in agent), a shared-space run read not driven, no share-a-copy,
      copy or hosting on a coding agent's root; its calls follow the run's
      home.
    - **The contract, unchanged in rule** (W6-F): from a partitioned
      consumer's user partition the person is the partition's and verified
      on every route, the `tty` and `stdio` sockets included (D140);
      D147's "asserted, the consumer's to check" is an unpartitioned
      consumer's, or a global instance's. The consumer is the pair
      (`X-XBin-From`, partition id). `sdk/sandboxcontract` checks it
      (`user-partitions/sockets`, `apart`).
    - **AR-23 grows** (W6-F): partitions that see one sandbox share its
      stdio sockets (an attach takes over a coding agent's stdin) and its
      `$HOME` (a sign-in serves whoever runs the agent there, and its
      clones); a partitioned consumer should offer sign-ins only in
      sandboxes homed in the person's partition — the agent does — and
      whoever sets an image's `harnesses` commands is in its users' trust
      base.
    - **Usage**: coding agents' sessions counted per day, no content.
  - **Not chosen:** a harness check in every route alone (the engine guard
    is still needed; one predicate covers both); emptying the catalog at
    the global instance (its managers set coding agents and classes up
    there); moving a coding agent's conversation without its session (its
    sign-in and sandbox are its person's); refusing every tree that ever
    had a coding agent (a stopped one's answer is plain transcript);
    holding a partition while a coding agent waits for its person (C4);
    keeping the brake off the idle reclaim; refusing a member's leave;
    hiding the team's sandboxes in a person's partition (their terminals
    open there; `homed` says why a conversation can't use one); relying on
    the partition's exit for its wake-up (refused after the revoke), or
    changing the runner to revoke after a graceful stop's exit (the
    agent's own fix needs no platform change; the owner's question);
    keying `prefs/harness-sandbox` by home (a remembered sandbox that
    isn't the person's own doesn't fit, and is replaced).

- **D173 — A request a swap cut off goes to the generation that replaced
  it, when resending it is safe (2026-10-01).** internal/proxy/reroute.go,
  internal/runner/{runner.go (Gen), engine.go (stopGen)}. (The six numbers
  before it are the unmerged partitions branch's.) The owner: "Those tests
  should not be flaky / load/timing related".
  TestLiveReloadPauseRace/backends/runs/python failed under load: a GET
  13 µs to 180 ms after the pause's answer got `502 backend error: read unix
  …/g13.sock: read: connection reset by peer` (or EOF). The proxy had taken
  g13 from EnsureDeployment; the pause's deploy then installed g14 and
  SIGTERMed g13 (`go stopGen(old)`), and g13 ended with the request in its
  listen backlog. Not the fixture's alone: a request routed to a
  generation before a swap that reaches it after the SIGTERM finds a
  draining backend's listener closed (the SDK's Shutdown closes it first:
  the backlog is reset, a later dial refused), and a backend that exits at
  once drops what it holds too. D8's drain covers the requests the old
  generation took, not the ones still on their way to it. Three python
  clients requesting without pause across 20 save-driven swaps, on a box
  at load 300–480: master failed 30 of 81,551 requests over 40 swaps
  (resets, an EOF, a refused dial, each from a generation the swap had
  just retired); with this change 0 of 246,372 over 120 swaps.
  - **Now.** stopGen marks the generation retired before it signals —
    every stop xbind makes (a swap's old generation, a reap, Stop,
    StopAll), never the process's own exit. The proxy's transport for
    /api and for ingress (rerouting) sends a request its generation failed
    before any answer to the deployment's generation now (EnsureGen /
    EnsureDeploymentGen: the same routing again, never another
    deployment) when that generation was retired and resending is safe:
    no body, and an idempotent method (GET, HEAD, OPTIONS, TRACE, or an
    Idempotency-Key header: net/http's own retry rule) or a failed dial
    (nothing was sent). At most three times: each needs another swap
    during the request's own flight.
  - **Not chosen:** holding the SIGTERM until the requests routed to the
    old generation are answered (a request held open — the agent engine's
    keep-alive to itself, a long poll — would keep the old code running
    to the drain deadline, where the engine hands over on SIGTERM in
    milliseconds); holding it until they were taken (accept isn't visible
    from the client end of a unix socket); a delay before the SIGTERM (a
    time, not a condition); resending a body or a POST that reached the
    old generation (it may have acted on it).
  - **The test.** Its node and python fixtures exited at once on SIGTERM,
    against elements.md's graceful stop: an answer cut between its headers
    and its body was possible too. They drain now. Its waits that a
    condition ends have one hang guard (raceGuard, 3 min), the failed
    pause waits for the broken save's build-error event rather than four
    debounces, and the harness's xbind start and stop (60 s, 20 s, the
    shared daemon's 10 s) wait on the same conditions bounded by a 3-minute
    guard: a loaded boot took 63 s.
  - **Left, seen only at load ~300–480 on 192 cores:** the runner's 5 s
    health timeout (pinned, TestSeamKeepsConstants; protocol.md's
    "health-checked by socket-connect within 5 s") failed a python start in
    3 of 36 runs, failing that pause's deploy. Not changed here: it is a
    contract. The go subtest's failure under the same load is D174's.
- **D174 — A generation built from the work tree serves only if live reload
  still drives its deployment once a pause in progress ends (2026-10-01).**
  internal/runner/{runner.go (buildAndStart), deploy.go (workTreeLeft,
  SettledCodeFor)}, internal/deployments/plane.go (overlay.settle,
  SettledCodeFor). TestLiveReloadPauseRace/backends/runs/go under `-race`
  at load ~380 failed every execution (8 of 8; 1–2 of 10 pause runs each):
  after the pause's answer the code answered a save newer than the
  checkpoint (`r003-000285` against `r003-000149`) until the pause's deploy
  swapped. A work-tree build reads the record only when it starts
  (runCurrent) and reads the work tree as it builds: one started before a
  pause — here a second build queued behind the resume's, when the watcher
  flushed the run's first save after the resume committed — compiled saves
  made after the pause took its checkpoint, and swapped in, as 07-runtime
  §8.6 allowed ("the in-flight build finishes and swaps"). A slower build
  could read saves made after the pause's answer: a leak by
  SC-LIVE-RELOAD-PAUSE's own words, and a pinned deployment running its
  work tree, which runCurrent exists to prevent.
  - **Now.** Before a generation built from the work tree is installed
    (buildAndStart: the save, grant, crash and reap path), the runner asks
    SettledCodeFor: the plane waits out an operation detaching the tile's
    live reload (the pausing overlay, held from its request to its commit
    or catch-up), then answers from the record. Pinned meanwhile: the
    generation stops unserved, and the current one serves until the
    operation's queued deploy swaps the checkpoint in (with none current,
    the next request builds the checkpoint). Still the work tree (the
    operation failed and caught up, or attached live reload here): it
    serves as before. What it serves was read before the check, and the
    check precedes any later operation's mark, so it predates that
    operation's checkpoint.
  - **Not chosen:** reading the record without waiting (an install between
    a pause's capture and its commit still serves code newer than the
    checkpoint, and one racing the commit serves it after the answer); the
    plane's LiveReload (false while any operation holds the overlay, which
    would also drop the first build of the deployment an attach makes
    follow the work tree); cancelling the build when the pause begins (its
    build turn is the pause's deploy's next anyway).

- **D175 — Base auto-update: a tile's terminal layer built on an older
  base image moves to the current base at its next session start; a
  workspace setting, on by default (2026-10-01).** internal/term/base.go
  (claimLayer), internal/wssettings, internal/server/wssettings.go, the
  admin tile's workspace → terminals tab, `bx settings`. The owner: "For Go
  versions in bases lets have a knob in admin workspace settings on base
  auto-updates, default to true." A terminal's layer (.xbin/term/<key>:
  the overlay upper, a VM terminal's disk) is pinned to the base it was
  built on (component-env.md §Base images): a newer xbind's base reached
  it only when someone pressed the window's "⬆ base update". Once D166's
  builtins said `go 1.26.0`, a terminal on the old base (Go 1.24.0)
  downloaded a toolchain for every `go` command, or failed where its
  network scope couldn't reach proxy.golang.org.
  - **Chosen: the move happens when a session claims the layer.** A
    session's start takes the layer (acquireEnv: one live holder) before
    anything mounts it; if its stamp isn't the current base and the
    setting is on, the layer is moved off — put aside (one rename into
    .xbin/term-moved/, which no layer scan, VM disk scan or backup reads)
    for a background remover that removes it confined, exactly as the
    reset removes it — and a fresh layer stamped with the current base,
    and the session runs there. Put aside, not removed inline: a VM disk
    or a big upper is minutes of confined `find -delete`, and the start
    is the WebSocket's open. The shell's first output is one grey line
    saying so; an agent session logs a `notice` event its Agent tab shows
    (and its host log), and the tile's next shell says the agent's move
    once (an automation's agent session may be the first after an
    upgrade, and the packages were perhaps a shell user's). A running
    session is never touched: a second session on the tile while one
    holds the layer gets an ephemeral upper (as always), and the holder
    keeps its base until it ends or restarts. Files and $HOME are bind
    mounts, not the layer, so what is lost is what the reset loses — and
    the line says what that is: everything outside the workspace files
    and $HOME (installed packages, /etc, /var, /opt…, a VM terminal's
    whole disk with its docker images and volumes, none of it in a
    backup). Terminals run no `setup`: the backend's env layer (setup's)
    is keyed by the rootfs already (runner.setupHash) and rebuilds by
    itself.
  - **A move that can't complete fails the start** (the error says to
    open it again or reset it), and the next start tries again: the old
    layer can't be put aside and its removal in place fails (`find
    -delete` may have stopped halfway — half a layer is never mounted, on
    either base), or the fresh layer can't be stamped (its empty dir goes
    again, so the next start makes a new layer on the current base rather
    than read an unstamped one as legacy). What the background remover
    can't remove stays in term-moved for the next boot's sweep.
  - **Unreadable is never "missing" (review).** A layer stamp that is
    there but can't be read (EIO — containerfs has an unresolved one —
    EACCES, EMFILE, a link) used to read as no stamp: re-stamped `v0`
    (legacy), it would have been discarded as outdated while it was on
    the current base. A rootfs version file that can't be read used to
    read as `v0` too: every layer on the current base outdated, and the
    fresh one stamped `v0`, to be discarded again at the next start.
    Now: only a stamp that is absent (ENOENT) on an existing layer is
    legacy; an unreadable one fails the start, the layer untouched (as an
    unreadable stamp failed it before the move existed: "not installed").
    The current base's version is read once per xbind run (the installer
    stops xbind before it swaps the rootfs), a failed read is an error
    and isn't kept, and no start, stamp or move happens on it. Every
    stamp write is checked. An unstamped rootfs (current = `v0`, a dev
    one) is no base to move to.
  - **A base that isn't installed any more** (GC released it, a host move
    or DR onto a fresh install, a restore, a base deleted by hand). With
    the setting on, such a layer moves like any other; off, its start
    refuses it, as before. **The boot no longer refuses to start over one,
    either way** (CheckBaseImages logs them): the per-start refusal is what
    keeps a layer off another base, and claimLayer is the only path that
    mounts a terminal layer — so the boot gate guarded nothing the start
    doesn't, and with the setting it would have turned "admin turns
    auto-update off" into "xbind won't boot" wherever such a layer had
    been let through. Tile sandboxes were already this way (a missing
    pinned base puts that sandbox in error, never gates the boot). A
    moved layer no longer pins its old base, so the next boot's GC
    releases it.
  - **The setting** lives in data/workspace-settings.json (internal/
    wssettings): an xbind-owned JSON object, each key with a default for
    when it is absent (a missing file is every default: on); a write sets
    its keys and keeps every other key the file holds (a newer xbind's);
    an older xbind never reads the file, so a downgrade ignores it and the
    upgrade back finds it. A file that can't be read turns base
    auto-update off (a guess must not discard anything) and a PUT refuses
    to overwrite it. Not users.json, where the native-runtime switch is:
    its rewrite drops keys it doesn't know (a downgrade's first write
    would turn an admin's "off" back on), and it is the identity store.
    Not branding.json: branding's. GET /api/xbin/workspace-settings
    (authenticated), PUT (admin, audited, primary-only, publishing a
    `workspace-settings` event so open windows re-read); /ws/term/env
    gains baseAutoUpdate so the window's chooser says what the next
    session does instead of offering the button.
  - **Not chosen:** moving every outdated layer at boot (no session to
    tell, and a layer nobody opens again would lose its installs for
    nothing); rebasing (re-stamping the kept upper: dpkg's status from the
    old base over the new base's files — what the pin exists to prevent);
    tile sandboxes (internal/tilesbx): their cur/ is the whole sandbox
    state a manager tile keeps — work, not only installs — and the
    sandbox-manager contract gives the manager the choice (`base.outdated`,
    reset, rebase), so the setting doesn't touch them; a per-tile switch
    (the owner asked for one workspace knob); keeping a moved-off layer
    for a grace period to undo the move (a VM disk is tens of GB and the
    fresh layer grows beside it; nothing would offer the undo — the
    choice is the setting, made before the upgrade); a 409 on turning the
    setting off while layers on missing bases exist (moot once the boot
    stopped refusing them).
  - Numbered D175: D173 and D174 went to deflake/livereload-pause,
    in flight at the same time.

- **D176 — CI runs as parallel jobs; make integration and make test are
  split into shards by test and by package, every test exactly once
  (2026-10-01).** .github/workflows/ci.yml, hack/testshard,
  hack/integration.jsonc, hack/integration-timings.json, hack/ci-apt.sh.
  The owner: CI's wall clock at most 5 minutes, covering everything it
  covers today. One job ran make check (3.5 min), make tile-check (2.3
  min), make integration (5 min on 2026-09-29) and the VM suite in series,
  11 min in all; on 2026-10-01 (run 36841863101) integration hung 12 minutes in
  internal/confine (a vfork + in-process FUSE deadlock, fixed on its own
  in 04f41f9b) and the run failed at 21 minutes.
  - **Jobs.** A `test` matrix — check (`make -j4 -O guards`), unit 1/2 and
    2/2, tile-check (its backends now concurrent), integration 1/4…4/4 —
    plus vm (KVM, the vm helpers, the guest rootfs: `make integration
    SHARD=vm`), shards (the split's guard) and the native client's checks
    in three jobs. GitHub-hosted only (pull requests run it). The first
    sharded run: 3 min 35 s wall, green, every Go cache cold.
  - **The split is by test, from source, balanced by measured time, with
    a hash for the unmeasured.** A suite (one go test over one package,
    with its env and filters — what a Makefile line was) lists its
    top-level tests from the package's test files (go list + go/parser:
    what `go test -list` prints, checked against it in the shards job),
    and testshard assigns them longest-first onto the least-loaded shard
    from the timing file's profile ("ci" when $CI is set, else "local"),
    a suite's build and TestMain counted once per shard; a test the file
    doesn't know goes to the shard its name hashes to. So the split is
    deterministic — every job computes the same one on its own, nothing is
    passed between jobs — and adding a test moves no other. Rejected:
    sharding by package (./test alone is 3 minutes on CI; the next
    biggest 1.5), a dynamic queue (jobs would share state, and a run
    could no longer be reproduced with `SHARD=i/N` locally), and running
    the suites concurrently on one runner (4 vCPUs: the speed audit's
    1408 → 497 s needed a 192-core box).
  - **The guard is a test, not a convention.** hack/testshard's tests (in
    make test) recompute the split under both profiles and fail when an
    integration test lands in no shard or two, a unit package in no unit
    shard or two, a suite's filter matches nothing, or ci.yml doesn't run
    each shard and job exactly once, or runs `make integration`, `make
    test` or `make check` unsharded beside them. TestIntegrationPackagesListed
    reads the plan instead of the Makefile. The first sharded run's top-level
    results equal the last single-job run's for every test both have (the
    one test only the old run has was removed by D166), and none ran twice.
  - **Locally, every shard at once.** `make integration` runs the shards
    and jobs concurrently, each into a log of its own, and the latency
    budgets (the plan's `alone`) by themselves after them; `SHARD=i/N`
    runs one in the foreground, as its CI job does.
  - **Caches.** Each kind of job has its own module + build cache
    (actions/cache), restored from master's newest and saved only by
    master runs: pull requests share master's, and the repository's 10 GB
    cache budget isn't spent per branch. ACCEPTED TRADE-OFF: a pull
    request that changes go.sum starts from master's older cache, and
    master runs churn the budget by a cache per kind per run (LRU evicts
    the oldest).
  - **Not taken from the speed audit (2026-09-29):** the latency
    benchmarks to a nightly tier and the test-only timer and argon knobs —
    they change what CI covers, and the shards meet the budget without
    them. Taken: its reliability traps (xbindtest's boot-failure deadlock,
    the tilesbx fd settle; the PauseRace fixtures are another agent's
    deflake), and keeping going after a failure.

- **D177 — Landing partitioned tiles on v0.3.65: a person's partition
  takes master's runtime fixes as a deployment does; every agent instance
  becomes partitioned (2026-10-01).**
  plans/partitions/records/LAND.md (every conflict and its resolution).
  The owner: "Main thing to do now: land all of the partitions work."
  Master (bb16fded, v0.3.65) merged into `partitions` (94d061d3); what
  master's D166/D173–D176 do for a tile's instance, a person's partition
  of a partitioned tile (D148ff) now gets too, and nothing changes for a
  workspace without a partitioned tile (TestNoPartitionGolden,
  TestZeroStateRoute).
  - **D173 for partitions: rerouting, not "explicitly not needed".** A
    person's partition swaps (a save restarts every live partition onto
    the new build; stopGen retires the old generation), so a request
    routed to it just before meets the same closed socket D173 fixed. The
    proxy's partition path now answers a `rerouting` transport like the
    deployment path: again() is the same EnsurePartition — the same
    partition, the same start class, through partitionGate and admission
    again, never Route again (as D173 never re-asks Route). The call's
    one hold spans both attempts: TrackPartition holds the partition's
    state, not a generation. proxy.PartitionRunner answers a PartitionGen
    (Sock, Retired) — runner.Gen via boot's adapter over
    EnsurePartitionGen; EnsurePartition (the socket) stays for the
    runner's own callers. Rejected: leaving partitions on the socket-only
    path (a partitioned agent's GETs during a save would 502 where the
    same unpartitioned tile's don't).
  - **D174 for partitions: already asked; its discard fixed.** A
    partition's generations go through buildAndStart, so a work-tree
    build asks SettledCodeFor before it serves. But D174's discard
    stopped the generation as a primary's — while partSpawned had made it
    the partition's `starting` generation with a registered token: a
    stop and the running count kept seeing a dead generation, and its
    token lived until the process exited. discardGen stops it as install
    stops one whose state went: token revoked first, `starting` cleared.
  - **D175 for people's layers.** claimLayer finds a layer by its key
    (layerDir: `.xbin/term/<key>` or `.xbin/term-part/<TK>/<pkey>`), so
    a person's layer is pinned, refused or moved exactly as a tile's: at
    that person's next session start, never under a session of theirs
    that holds it (held → ephemeral), never the tile's or another
    person's. The boot lists a missing person layer by its layer key. A
    base-move note (an agent's start moved the layer) is keyed by the
    claimed layer, not the tile — ana's agent's move told bob's next
    shell, and that someone's agent ran (PD-09). The switch's wipe and
    the move don't meet: a switch waits for opens in flight before its
    wipe (partitionhold.go), the move happens inside an open, and what a
    move puts aside is in `.xbin/term-moved/`, the remover's alone.
  - **D166 G1:** keyed by registry path; a partition's view keeps the
    tile's path, and a partitioned tile builds once per change — one
    baseline entry, one alert line (tested).
  - **Kept apart, not unified:** `bx policies` / `/workspace-policies`
    (PD-55, the branch's, `data/workspace-policies.json`) and `bx
    settings` / `/workspace-settings` (D175, released in v0.3.65,
    `data/workspace-settings.json`) are two admin switch sets with two
    files and two console tabs. Folding them is a compat question (the
    released route and file can't move) left to the owner.
  - **Fixed on the way: the agent engine's false takeover.** Engine.fenced
    scanned the engine epoch with its error dropped; a failed read was 0,
    "another engine took over this database", and the sole engine stopped
    (CI, TestHarnessQueuedPark). readEpoch answers 0 only for a missing
    row; fenced and takeOver try the transaction again (5 tries, 20–80 ms
    apart; fn not yet run) and return what keeps failing with nothing
    written, the engine still the owner; the harness pipe's guard refuses
    that one write instead of answering errFenced. Rejected: treating a
    failed read as "still mine" (it may not be: the write must not go).
  - **Numbering:** sandbox-terminal is v8 — master released v6 (x/crypto)
    and v7 (SSH start) while the branch's fake-manager change was its v6.
  - **Every agent instance becomes partitioned (the owner, 2026-10-01).**
    "After this update all AgTT instances should become partitioned, no
    migration from legacy needed." This amends PD-52 for the agent
    template only (plans/partitions/90-decisions.md). What reaches an
    agent instance is its template's update by `git merge template/main`
    (D50; `bx builtin update` never touches template instances), so that is
    where the request comes from: the template block's new
    `"partitionOnUpdate": true` makes the served repo — under `--isolate`
    — carry `"partition": ["user","global"]` as its top-level key on the
    line after the opening brace, where a new instance already has it (the
    two merge as one line). The manifest merge driver takes an upstream
    `partition` where neither the base nor ours names one (as git's line
    merge does); a mode the builder wrote stays theirs, upstream's ask a
    conflict. PD-44 does the rest, unchanged: an instance holding data
    pauses for a manager — switch (deleting its data; no migration is
    built; the agent's `partitionNote` names what goes) or **Keep the
    current mode** (the legacy, unpartitioned path, kept) — and an empty
    one switches. Without `--isolate` no request is served (people's
    partitions can't run there; the agent stays one instance). Once a
    served repo asks it never changes or withdraws the ask, whatever a
    later template or a non-isolated restart says: a removal or a narrower
    mode would ask already partitioned instances for another switch.
    Rejected: an xbind-side rewrite of every instance's manifest at boot
    (it would edit tiles' code behind their builders and bypass the merge
    they control); writing the ask without the isolation check (a request
    no non-isolated xbind can carry out, pausing the agent for nothing).
    Tests: TestAgentTemplateUpdateRequestsPartition (with data: pending,
    untouched, keep declines; empty: switches; opted-out and new instances
    alike; the ask stays as served), TestAgentTemplateUpdateWithoutIsolation
    (no request), manifestmerge's TestKeptKeysConflict and
    TestOldInstanceTakesRequestedPartition (the driver);
    docs/changes/2026-10-01-agent-instances-partitioned.md.

- **D178 — Coding-agent sign-in: Claude Code signs in with `claude auth
  login`, guided in the Agent tab; terminal links open whole; no
  CLAUDE_CODE_REMOTE (2026-10-01).** sdk/acp signin.go, providers.go;
  web/signin-scan.js, agent-signin.js, term-links.js, term-sessions.js;
  docs/protocol.md `GET /agent/providers`. The owner: the sign-in should
  run something like `claude /exit` — which prompts for the login and
  then exits, which we catch — because `/login` "just prompts for login
  twice"; and the login link "renders really jank" in the terminal.
  - **`claude auth login`, not `/login` or `/exit`.** On a fresh `$HOME`
    Claude Code's onboarding signs in, then the `/login` command signs in
    again. `claude auth login` (2.1.41+; a pasted code since 2.1.126; one
    line of link since 2.1.202) skips onboarding, the theme picker and
    the REPL, works without a TTY, prints `If the browser didn't open,
    visit: <URL>` (OSC 8 on a terminal, plain on pipes) and `Paste code
    here if prompted > `, then `Login successful.` and exit 0, or `Login
    failed: …` and exit 1; a malformed code prints `Invalid code…` and is
    asked again. `claude /exit` works too, but walks the theme,
    login-method and trust screens — it is the **fallback**, for a CLI
    without `auth`: the command carries `--claudeai` (the subscription
    sign-in, its default anyway) so such a CLI fails at once on an
    unknown option instead of starting a session, and a run that ends
    without a link offers `claude /exit` in a terminal. Rejected: version
    detection in a shell line (long, and shown to whoever opens the
    terminal), and `/exit` for everyone (three screens to click through).
  - **The spec is data, the reader pure, twice.** `Provider.Signin`
    {command, argv, env, tty, fallback, url (a regexp RE2 and JS agree
    on), hosts, code/invalid/done/fail markers} rides `GET
    /agent/providers` as `signin` (additive; claude only — codex's device
    code, gemini's and opencode's terminal flows keep today's terminal).
    `Signin.Scan` (Go) and `scanSignin` (web/signin-scan.js) strip
    CSI/OSC, take an OSC 8 target first (Ink draws a long URL as one link
    per row, each pointing at the whole URL), else rejoin a URL
    hard-wrapped over equal-width rows, and offer only an https URL on the
    provider's hosts (subdomains count; no user part). Both are tested on
    the same captures of Claude Code 2.1.280 (sdk/acp/testdata/signin).
    Nothing is saved or minted here: the CLI keeps the login in `$HOME` as
    it always did — saved per-person sign-ins wait on the owner's policy.
  - **The guided strip** (`<bx-agent-signin>`): **Sign in** opens a
    terminal session over the existing `/ws/term` wire (cwd the tile, gpu
    none, api 0, net the tile's default — the agent's), sends
    `{op:"resize",cols:1000}` so nothing wraps, and types ` <command>;
    exit` (the leading space keeps it out of an ignorespace history; the
    `exit` ends the session when the CLI does, even when the CLI is
    missing). It shows **Open sign-in page ↗** (a real link: no popup
    blocker), **Copy link**, the link itself, a code field and
    **Finish** (the cleaned code and Enter, once the CLI asked), and a
    status line. The CLI's words decide, not the exit frame: `/ws/term`'s
    exit frame for a shell carries no code (it is sent before the reap),
    so `Login successful` is signed in, `Login failed` the reason and
    **Try again**, a new `Invalid code` paste-again; an exit with none of
    them shows the last line. Signed in, the prompt stays down until a
    turn fails again (the agent keeps reporting signed-out until a turn
    succeeds) and the tab says to send the message again — as before, the
    person re-sends. **Use a terminal instead** is today's shell tab
    running the login; the strip's session ends then.
  - **No tab for it, without new server surface.** The session directory
    lists every session of the user's on the tile, and bx-frame makes a
    tab of each — in every browser. The strip names its session
    `xbin:sign-in` (the existing rename) the moment it learns the id, and
    `visibleRows` drops that name; in this browser rows are also dropped
    by id, and while one is opening on the tile a shell row no tab holds
    waits for the next listing (the rename's `term` event brings it). The
    xbin app's directory (XbinTerm `TermDirectory.decode`) drops the name
    too, so no tab, badge or inbox row there either. A `name` on
    `/ws/term` would close the last window — another browser can show a
    "Bash" tab for the instant between open and rename — but the brief was
    no new server surface.
  - **Terminal links** (term-links.js, every `<bx-terminal>` and
    `<bx-logs>`): xterm's `linkHandler` opens OSC 8 targets (http/https,
    `noopener`, no `confirm()` — xterm's default asks "This link could
    potentially be dangerous"), so any row of Ink's wrapped link opens
    the whole URL; a link provider registered before the web-links addon
    joins a URL that reaches the right edge with the following rows made
    only of URL characters (the addon joins soft wraps only — a fragment
    gave a truncated URL); `@xterm/addon-clipboard` 0.1.0 (the one built
    for xterm 5.5, a single-file UMD like the other addons; vendored and
    pinned) answers OSC 52 so Claude Code's "c to copy" works — writes
    only, while that terminal has the focus, never a read (a sandboxed
    program must not read the person's clipboard). Its constructor takes
    (base64, provider), not what its typings say.
  - **CLAUDE_CODE_REMOTE=1 is gone from the adapter's env.** Added in D75
    for the sign-in only: under it claude-agent-acp advertises its
    full-screen `--cli` login (`claude-login`) instead of `auth login
    --claudeai|--console`, and D-harness A11 kept it believing `auth
    login` needs a localhost redirect — true before 2.1.126, not since.
    But Claude Code itself inherits the adapter's env, and to 2.1.280 the
    variable means Anthropic's own remote sessions: auto memory off, a
    2-minute API timeout instead of 5, a settings `defaultMode` of
    `bypassPermissions` refused ("only acceptEdits, plan, default, and
    auto are allowed"), git-status prefetch off, its own "Authentication
    error" wording. Nothing else needs it: the rootfs probe
    (claude-agent-acp 0.81.1, signed out) opens the session with the same
    modes and options either way; only the advertised auth methods differ.
  - **The AgTT side follows separately** (built as D179), after the partitions merge: the
    agent template's guided method (it still runs the adapter's terminal
    method, now `auth login`, or the catalog's `LoginCmd` — left as
    `CLAUDE_CODE_REMOTE=1 claude /login` for it to replace with
    `Signin`), the gemini `_meta["api-key"]` shape bug, and saved
    per-person sign-ins (the policy ruling first). When it moves to
    `Signin`, a sandbox manager's advertised login must win over the
    catalog default, so an older coding-sandbox manager's `claude /login`
    keeps working; a new one advertises `claude auth login`.
  - **Amended 2026-10-01 (security review).** Four of the choices above
    were holes; none had shipped.
    *Links (M4):* dropping xterm's `confirm()` let any program in a
    terminal — or a request path a backend logs into `<bx-logs>` — draw
    an OSC 8 link showing `https://login.xbin.dev/…` that opens another
    site. `linkHandler` now reads the cells the link covers and opens at
    once only when that text is part of the target and any host it names
    is the target's (Ink's per-row pieces of its own URL still open in one
    click); anything else asks first, naming the host and the whole URL.
    `openLink` refuses a user part, so rows joined across a hard break
    (`https://claude.ai` + `@evil.com/x`) never open.
    *OSC 52 (N15):* `@xterm/addon-clipboard` answered a `?` read into the
    program's input. Our own handler (`parseOsc52`) writes the clipboard
    only for the `c` selection, only while that terminal has the focus,
    at most 1 MiB of UTF-8, and swallows reads; the addon is no longer
    vendored.
    *No tab (L11):* hiding by the name `xbin:sign-in` let anyone who may
    rename a session (a terminal token included) or an agent's own title
    hide any session from every tab bar. The "no new server surface"
    brief gives way: `/ws/term?purpose=signin` opens a shell xbind marks
    (`SessionInfo.purpose`, omitempty, also on `term` open events; any
    other purpose is 400), clients hide shell rows with that purpose only
    — never an agent row, never by name — and the browser's id/opening
    bookkeeping is gone (the mark is there from the first listing). xbind
    still names the session `xbin:sign-in`, but nothing else can: a
    rename to that name (any case) is 400, renaming a sign-in session
    409, an agent's open/restart name and its own title skip it. A
    sign-in session ends 15 minutes after it opened (`Manager.SigninLife`,
    a timer armed at creation).
    *Allow-list (N16):* "subdomains count" and `\S+/oauth/authorize\?`
    passed a redirect-style path (`https://claude.ai/x?u=/oauth/authorize?…`)
    and any subdomain. The URL is anchored —
    `https://(claude.com/cai|claude.ai)/oauth/authorize?…` — and hosts
    match exactly, in Go and web/signin-scan.js; the JS test reads the
    spec from the server's golden, so the twins can't drift.

- **D179 — Coding-agent sign-ins in the agent template: the guided sign-in,
  saved sign-ins (several per coding agent, a person's own partition only)
  and switching accounts within a session (2026-10-01).** The agent
  template's side of D178: builtin-templates/agent/_backend/
  harness_guided.go, harness_creds.go; sdk/acp `Provider.Mint`, `Keys`,
  `Signin.Token`; model/harness-signins.js; the template's API.md §Coding
  agents ("Signing in", "The guided sign-in", "Saved sign-ins", "Guided
  and saved sign-ins in the UI"); docs/sandbox-manager.md §hello; docs/
  sdk.md. Amends D147 ("credentials stay in the sandbox HOME") and D172
  (sign-in only where credentials stay the person's: U-M4).
  - **The owner's rulings (2026-10-01).** (1) Saved sign-ins: yes — the
    guided card has "Remember for my other sandboxes", which runs the
    official `claude setup-token` through the same link-and-paste flow,
    the backend scraping the token, which never reaches the browser; or
    the person pastes an API key or token (Anthropic, OpenAI
    `CODEX_API_KEY`, `GEMINI_API_KEY`, opencode's provider keys). (2)
    Several logins per coding agent (a personal and a company
    subscription): named per harness, one the default, a conversation can
    pick one, and switching within a session should work — restart the
    adapter with the other credential and resume the same session
    (`session/load`, D75). A sandbox's `$HOME` holds one login, so
    multi-account means saved sign-ins. (3) The saved sign-in wins over the
    sandbox's own `$HOME` login; the chip says "using ‹name›". (4) Every
    agent-template instance becomes partitioned (D177); saved sign-ins
    exist only in a person's own partition, in its vault — never at the
    global instance, never in legacy mode (only on an xbind without
    `--isolate`, or after "Keep current mode"), which keeps the
    per-sandbox guided sign-in only.
  - **The policy.** The token is minted by the unmodified CLI (`claude
    setup-token`, Anthropic's own long-lived-token flow for headless use)
    with the person's own sign-in on Anthropic's page, and is kept as that
    person's own secret, used only for their own coding agents. We never
    proxy subscription credentials: no gateway, no shared pool, no copy
    of `~/.claude/.credentials.json` or `~/.codex/auth.json` between
    sandboxes (their refresh tokens are single-use: copies log each other
    out), nothing else from `~/.claude` copied either. A setup-token makes
    model requests only (no connectors, no Remote Control) and outranks a
    `$HOME` `/login` as `CLAUDE_CODE_OAUTH_TOKEN`.
  - **The guided sign-in** (`POST …/harness/authenticate {method:
    "guided", code?, remember?, name?}`) runs the provider's `Signin`
    (`claude auth login --claudeai`: plain over pipes) — or, with
    remember, its `Mint` (`claude setup-token`: a TTY, at 1000 columns so
    the URL and the token are one line each) — as a contract exec in the
    run's sandbox with stdin open, reads it with `acp.Signin.Scan`, and
    answers 202 `{signin: {url, paste}}` to the requester alone. `code`
    writes the code and Enter (`\n` over pipes, `\r` on a terminal — Ink's
    return); the CLI's words decide: done (or exit 0 for `auth login`) →
    the run's existing Retry (an inbox wake), `Invalid code` → 409 with
    the link standing, anything else → 502 with the CLI's reason line. The
    exec is deleted at every end; one per conversation, bounded at 15
    minutes (also the exec's own `timeoutMs`, so a process that dies
    leaves nothing waiting), dropped on a handoff (`letHarnessesGo`: a
    successor knows none, the person starts over); while it waits it holds
    a person's partition up (`harnessHoldsLocked`), as D172's awaited
    sign-in does. Remember is refused unless the gate below holds for the
    run's sandbox: while the exec
    lives its output (the token) is readable by whoever may use the
    sandbox (the manager's exec routes). A minted token goes straight into
    the vault; a saved sign-in of the same name is replaced (its id, and
    the conversations that picked it, stay; a refusal clears).
  - **Saved sign-ins.** harness_signins keeps the non-secret part per
    person: `{id, harness, name, kind (setup-token | api-key), env,
    mintedAt, expiresAt (a setup-token: +365 d), refusedAt, refused,
    isDefault}`; the secret only in the partition's vault
    (`harness-signin.‹id›`, through the SDK's `SetSecret`/`Secret`/
    `DeleteSecret`, which a person's partition reaches as its own vault,
    D148) — never in a row, a log line, an answer or an event (the tests
    scan every table, the log and every answer). Routes: `GET/POST
    /prefs/harness-signins`, `PUT/DELETE /prefs/harness-signins/{id}`
    (rename, default, a new secret; Forget), `PUT /runs/{id}/harness/
    signin` (the conversation's pick: default, sandbox, or an id;
    `config.harness.signin`). `env` is `acp.Provider.Keys`' (claude:
    `CLAUDE_CODE_OAUTH_TOKEN` for `sk-ant-oat…`, else
    `ANTHROPIC_API_KEY`; codex `CODEX_API_KEY`; gemini
    `GEMINI_API_KEY`; opencode by prefix or named).
  - **The injection gate** (`credWhy`, checked at every spawn): the env
    is merged into the adapter's exec request only when (a) the agent
    runs in the `user` partition state, (b) the run is the person's own
    (its root's owner is the partition's person; not hosted:
    `harnessBarred`), (c) the sandbox is private and theirs
    (`!sandboxShared(box) && box.Owner.User == person` — a co-user could
    read the process's environment), (d) the credential isn't refused or
    expired. The env is never persisted: the provider's catalog map is
    copied, never written; `harness_sessions.cred` (additive) records
    which credential the generation started with, its id only. Re-checked
    with the sandbox's use (before every prompt, at most every minute on
    durable events, at a takeover): a sandbox shared mid-run stops the
    adapter (`hstop`), and the next start leaves the credential out; a
    share through the agent's own `PATCH /sandboxes/{ref}` stops it at
    once. Left: a share made at the manager while an adapter idles is seen
    at its next message or its idle stop (≤ `harnessIdleMin`), its
    environment readable meanwhile — nothing tells the agent of it.
  - **Refusals and false refusals.** With a credential in, only the
    adapter's own refusal counts — a -32000 on a prompt (the client's
    `AuthHint` marks it) or on opening a session: it sets `refusedAt` and
    parks on the sign-in with "saved sign-in refused — sign in again". A
    status update alone doesn't: claude-agent-acp 0.81's `claude auth
    status --json` probe maps an env token's `{loggedIn: true, authMethod:
    "oauth_token"}` (no subscription it can name, Claude Code 2.1.280) to
    `_auth/status_update {kind: "none"}` though every turn works; parking
    on it would have stopped every saved sign-in. The fake (acptest)
    pushes the same status so the tests hold it.
  - **Keys an adapter takes only through `authenticate`.** codex 0.156's
    app-server reads no `CODEX_API_KEY` at start (`account/read` →
    `account: null`, checked here), and Gemini CLI with another sign-in
    selected uses its env key only after `authenticate`. When such an
    adapter refuses its session signed out and an API-key credential was
    injected, the engine calls its API-key method once with the key
    (`credAuthenticate`). Codex then keeps it in its own
    `~/.codex/auth.json` (its file store; `-c
    cli_auth_credentials_store="ephemeral"` would keep it in memory, but
    codex-acp passes no flags to its app-server): in a private sandbox of
    the person's only, by the gate, and Forget doesn't reach it there —
    said in API.md.
  - **Switching accounts within a session resumes the same session.**
    `PUT …/harness/signin` stores the pick and, for an adapter at rest,
    an `hswitch` inbox row stops it (state stopped, the session id and
    `loadable` kept, a note); the next message starts it with the other
    credential and `session/load`s the same session. claude-agent-acp
    0.81.1's `loadSession` reads the transcript from the CLI's own
    `$HOME/.claude/projects` in the sandbox (`readResumedSession`), with
    no account in it; thinking-block signatures are portable across
    accounts and platforms. TestSwitchAccountResumes checks it end to end
    with the fake (`--persist`): the same ACP session id, a new
    generation, the other account answering. Not run live against two
    real Anthropic accounts (no credentials here). A load that fails falls
    back to a fresh session with a note, as every resume does; carrying
    the context into it was not built — the load works.
  - **Which login a terminal runs.** A sandbox manager's advertised
    `login` now wins over the catalog's for a catalog id
    (`harnessProvider`), so an older coding-sandbox's `claude /login`
    keeps working and a new one's `claude auth login` is used; the
    catalog's claude `LoginCmd` drops `CLAUDE_CODE_REMOTE=1` (D178: the
    adapter's own terminal methods are `--cli auth login --claudeai |
    --console` without it). The guided sign-in runs the catalog's
    `Signin`, whatever the manager advertises: the CLI is the same
    binary. `claude /exit` stays the fallback for a CLI without `auth`
    (said when the guided sign-in ends without a link).
  - **Gemini's API-key shape** (a bug): gemini-cli 0.60's ACP
    `authenticate` reads `_meta["api-key"]` as the key itself
    (acpRpcDispatcher.ts, checked in the rootfs bundle); AgTT sent
    `{apiKey}`, read as no key. `apiKeyMeta` sends the string to gemini,
    the object to codex-acp and the fake (which now accepts both and
    records which came: TestAPIKeyMetaShape).
  - **A-M1 at the global instance**: no guided sign-in (the existing 409
    on `authenticate`), every saved-sign-in route 409, no injection
    (`credWhy`). Legacy: the guided sign-in, no Remember, no saved
    sign-ins (`GET` says `available: false` and why).
  - **The UI** (model/harness-signins.js, both views): the card's guided
    block (Open sign-in page ↗ — a real link, Copy link, the code and
    Finish, a status line, Use a terminal instead; Remember with a name
    where `rememberOf` allows it, else why), Coding-agent sign-ins (the ⚙
    Coding agents tab for managers; for everyone a dialog from the card's
    and the ▾ menu's "Saved sign-ins…", the app's Coding agent settings),
    and the account on the coding agent's ▾ with its switch.
  - **Not chosen:** copying a `/login` between sandboxes (refresh tokens
    are single-use); a proxy holding subscription credentials; writing
    the credential into the sandbox's `$HOME` (it would outlive Forget
    and travel with clones and snapshots); minting in a shared sandbox
    (its exec output is its co-users'); storing the env with the exec or
    the session; parking on the probe's "none" (above); version-sniffing
    the CLI in a shell line (D178's rejection stands).
  - **Open:** Remember mints Claude Code only (the others have no mint:
    paste a key); a live check of setup-token's success lines (the
    capture ends at the code prompt — success is the token itself, a
    refusal "OAuth error"); codex's own copy of a saved key in its
    `auth.json` (above); a partition's purge takes the vault — a person's
    saved sign-ins go with their removal, as everything of theirs does.
