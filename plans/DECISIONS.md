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

- **D166 — Each Go tile builds with a go.work of its own, made from its
  go.mod at build time; the root go.work is for shells and gopls only
  (2026-09-30).** internal/deps/{buildwork.go, modfile.go},
  internal/runner/{gowork.go, build.go, buildcheckpoint.go}. (The numbers
  between D136 and this one, D147 aside, are reserved by the unmerged
  partitions branch.) The owner: "go.work seems generally always felt sketchy to me, especially
  if all builds can influence some shared build env." D78 had given every
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
    `replace` lines (a directory that is a workspace module's; a module
    path), and the imports of its Go files (go/parser, ImportsOnly), then
    on through every module used. Imports matter because workspace mode
    lets a module import another used module *without* a require line
    (verified: builds with the shared go.work, fails with only the tile's
    module) — a tile doing that must keep building. The scan reads every
    .go file but tests in every directory a package can build from (`_*`
    and testdata too: a template's entry is `./_backend`), not `.*`,
    vendor/, node_modules/ or nested modules; build tags aren't evaluated
    (a superset only adds modules the tile's own code names). Files are
    read beneath an os.Root at the module (fsutil.OpenRootIn), O_NONBLOCK
    and regular only, 20000 files and 1 MiB each at most.
  - **Squatting: a reference is served only when it can mean nothing but
    the workspace.** A per-tile closure alone would keep a variant of the
    hole: tile A declares `module golang.org/x/crypto` and every tile that
    requires x/crypto reaches A. So a workspace module serves a reference
    when its path has no dot in its first element (no proxy serves
    `calendar`; builtins and `bx new` scaffolds are dotless), the
    reference requires it at a placeholder version (v0.0.0, or the zero
    pseudo-version `go mod tidy` writes for a replaced module), a replace
    names its directory, the referring tile's manifest names its tile in
    `deps` (the documented way to "build against" another component),
    only admins write it (a hand-managed use outside every tile), or it is
    an import its go.mod doesn't require — unless some go.mod of the
    workspace requires that path at a published version, which says it is
    a real module a tile builds with (so a tile that imported a published
    module without requiring it, relying on another tile's requirement,
    can't be handed a squatter's code for it). A path the go.work replaces
    at every version (the SDK's), the tile's own path, and standard-library
    imports (checked against the host toolchain's GOROOT: a tile declaring
    `module net` never reaches a build that imports net/http) never match.
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
  - **Compatibility — the breaking part.** A build that relied on another
    tile's go.mod fails now: an import of a package only another tile
    requires ("no required module provides package"), an API newer than
    the tile's own requirement, another tile's replace, a published-path
    tile module imported without deps. That is the hole itself (compat.md
    rule 11: a security hole closes in the release that finds it, with a
    migration note — docs/changes/2026-09-30-go-build-workspace.md). The
    build's output gains an `xbind:` line naming the require to add (the
    highest version another tile requires, or `v0.0.0` / deps for another
    tile's module). Every builtin tile and template, and the examples,
    build with only their own module and the SDK (checked). A protected
    primary's build.json now records `workspace: "tile"`; one recorded
    before D166 compares as "inputs moved", so a restart after `.xbin/`
    loss holds it for a manager's redeploy rather than silently rebuilding
    it with a different module graph (07-runtime §3.4's rule). Checkpoint
    builds record the modules they used, not every workspace module.
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
    that import a dotless module today).
