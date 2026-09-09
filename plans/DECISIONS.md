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
