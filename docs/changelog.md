# Changelog

Builder-visible changes to xbind, `bx`, the SDK, core elements, and builtin
tiles — newest first. Entries marked **BREAKING** link a migration note under
`/docs/changes/` with exactly what to change; **read those after every xbind
upgrade** (`curl -s -H "Authorization: Bearer $XBIN_TOKEN"
"$XBIN_URL/docs/changelog.md?raw=1"` from any terminal).

Maintainers: every builder-visible change lands an entry here in the same
commit; breaking ones add `changes/YYYY-MM-DD-<slug>.md` (rules: repo
`AGENTS.md`).

## 2026-09-09

- **`bx org add|set` no longer crash on a flag without its value.**
  `bx org set devs --name` (and `--sets`, `--net`, `--allow`, `org add
  --name`) printed a Go index panic; they now say `--name needs a value`.
- **SDK: a tile granted a bus alias role now passes the matching guard.**
  `xbin.RoleSatisfies` (and so `xbin.Role` / `RoleFunc`) folds
  `subscriber` onto `reader` and `publisher` onto `writer`, exactly as
  xbind does when it evaluates the call — a tile granted `subscriber` on a
  bus and guarding with `Role("reader")` got a 403 from its own SDK before.
  Permissive only: nothing that passed stops passing. Tiles pick it up on
  their next rebuild.
- **devbox and traefik declare their roles.** Both backends always guarded
  with `reader` / `writer` but their manifests exposed no roles, so no
  other tile could be granted them; `expose.roles` now lists both
  (tile.json v2 — `bx builtin updates` offers it). A test now refuses a
  shipped tile whose backend guards on a role its manifest does not
  declare, and `make tile-check` builds every builtin backend against its
  own `go.mod.tile` the way a workspace does.
- **`/vendor/bx-kit.js` — the frontend helper kit, and a page saying what
  tiles may import.** `api`/`xbinApi`/`selfApi` (fetch through the in-frame
  client, JSON out, the server's `error` thrown), `jbody`, `esc`,
  `deepActive`, `pathHas`, `clampBox` — the helpers the shell, the admin and
  organisations tiles, llm-gw and the agent template each carried a copy
  of. [frontend-kit.md](/docs/frontend-kit.md) lists every `/vendor/` module
  a tile may import, the shell-only ones, the absolute-URL rule, and the lit
  pitfalls met in this codebase. `bx-code.js` additionally exports `hl` and
  `langFor`. The literal fallbacks core elements carry for the theme tokens
  now equal `theme.css` (they had drifted to an old light palette), so a
  core element embedded in a document that never linked the theme renders
  in the workspace's colours; the theme is still never injected into a
  tile's document.
- **`/api/xbin/openapi.json` now describes the whole built-in API.** 27
  endpoints that existed but were missing from the OpenAPI document are
  in (permission sets, ownership transfer, access requests, code PRs,
  ingress, template updates and repos, tile status, alerts, invites,
  password change, token rotation), and three rows for the removed
  org-teams endpoints are gone. `docs/protocol.md` gained the `GET /gpus`
  row and route rows for the `/ws/term` and `/ws/events` WebSockets. A
  test now reconciles the mounted routes, the OpenAPI document and
  protocol.md on every build, so the three cannot drift again.
- **Design-record citations now point at pages you can open.** Every
  citation of a design record by its repository path in the served docs,
  the scaffolded `AGENTS.md`, the builtin tiles and the templates was a dead
  path from inside a workspace (the design records are not shipped); they
  now cite the docs page or the decision ID. Two new pages: [compat.md](/docs/compat.md) — what a
  workspace can rely on across xbind upgrades — and
  [maintenance.md](/docs/maintenance.md) for xbin contributors. `bx tile
  import` and *new from template* now skip a stray `backend/backend` build
  artefact instead of copying it into the new component (xbind builds
  backends into `.xbin/build/`; a `cgi` handler binary is unaffected).
- **Copy from a tile's context menu; paste stays native.** Right-clicking
  selected text inside a tile opened the tile menu with no way to copy: the
  native menu was suppressed and a sandboxed frame has no
  `navigator.clipboard`. The injected client now sends the selection (up to
  64 KiB) along with the relayed right-click and the tile menu leads with
  **Copy** (the snippet as its hint); the shell writes the clipboard —
  `navigator.clipboard` on https / localhost, `execCommand('copy')` elsewhere
  — and toasts *copied*. Inputs, textareas, editable text and links keep the
  native menu (Paste lives there), as does any right-click on selected text
  in the shell's chrome — now also inside nested panels such as grants and
  bindings, where a shadow-retargeting gap let the shell menu open over
  their inputs. On touch a live selection defers to the platform's selection
  toolbar. Terminal, code, logs and proposals pop-ups are unchanged.

## 2026-09-08

- **Links in new tabs from a tile: the `cap:open-links` grant (ND11).**
  The tile sandbox dropped every `<a target="_blank">` and `window.open`
  silently — including the shipped tiles' own "docs ↗" links and the agent
  template's markdown links. A tile now declares
  `uses: [{target: "cap:open-links", role: "writer"}]`; once a workspace
  admin (or an org admin whose allowance covers it) approves, that tile's
  iframe sandbox **and** its CSP header gain `allow-popups
  allow-popups-to-escape-sandbox`, so links and `window.open` work natively,
  framed and full-page; the frame reloads on approval, no backend restart.
  It is a grant, not a default like downloads, because the opened window is
  a full-origin, cookie-bearing page at a URL the tile chose — add
  `rel="noopener"` on your links; the workspace's own pages sever the
  opener; in a credentialless frame `window.open()` returns `null` (the tab
  still opens). Without the grant the console now says which grant a
  blocked link needs. The shipped `tiles/admin`, `tiles/apidocs` and
  `apps/welcome` declare it and a new workspace pre-approves it; the agent
  template declares it (pending on instantiation). **Existing workspaces:**
  see [changes/2026-09-08-open-links-cap.md](changes/2026-09-08-open-links-cap.md)
  — update the shipped tiles and approve three pending rows. Also: the
  approval prompts describe reserved capabilities in words (`cap:*` rows
  used to show a bare target), `/components` reports `sandbox` (the extra
  tokens a tile's grants unlock), and a git import / auth overview no longer
  warns "no such component" for `cap:*` / `xbin:*` uses.

## 2026-09-07

- **Admin console: a proper permission-set creator (D57).** The permission
  sets tab used to take the allowance grammar as free text. It now builds a
  set from typed rows — *Use a tile* (path or pattern + role cap), *Bind an
  interface* (service, optional provider tile + instance), *Use a resource*,
  *Hold a capability*, *Use a GPU*, *Publish a hostname / under a zone / on
  a host port*, *Network reach* (pointing at network sets), and a raw entry
  for anything else — each row shows the entry in words and the exact
  string, a bad field is flagged with the reason and blocks create/save,
  and one save writes the set **and attaches it to the chosen
  organisations**. Existing sets reopen with their entries parsed back into
  rows; stored entries are listed in words on the card; an org's *extra
  allow* entries use the same rows. The grammar lives in one shared browser
  module (`web/bx-allow.js`: parse, format, validate, describe) that
  mirrors the server's parser, so the UI, the org card and the organisations
  tile cannot drift.

- **Floating windows always stay reachable.** A tile's terminal / code /
  logs pop-up remembers its position per tile in the browser; restored on a
  smaller browser window, another monitor or a different zoom it could land
  entirely off-screen — the terminal ran, the session streamed, nothing was
  visible ("no terminal opens, no errors"). Every floating window (tile
  pop-ups, `xbin.window()` windows, the ⚙ popover, float tiles) is now
  clamped to the viewport when it opens or restores, is pulled back in when
  the browser window shrinks (windows larger than the viewport shrink to
  fit), and the canvas right-click menu gains **Bring windows on-screen**
  for anything still parked out of reach (it also fixes up a float tile's
  saved geometry). A caller-supplied `x`/`y` on `xbin.window()` is clamped
  the same way.

- **A tile reload no longer steals focus or hoists the tile.** When a tile's
  document focused an input on load (autofocus, a script), the shell read
  the resulting window blur as a click into that tile and brought its float
  to the front — over the terminal you were typing in, right after your
  edit reloaded the tile. A frame now reports itself as reloading until the
  new document has loaded, the shell leaves the z-order alone meanwhile, and
  focus pulled into the reloaded frame goes back to where it was (unless the
  pointer is over that tile, i.e. you really clicked it).

## 2026-09-06

- **Net pickers no longer show a refused bind as a success.** Picking
  `host` (or any ref outside the owning org's network sets, or anything at
  all on a tile you may not wire) in the ⚙ tile popover left the select on
  the refused value and the refresh wiped the error — it looked granted
  while the server had said no (the D54 gate itself held: host on an org
  tile is 400 for everyone, a personal tile is wired by workspace admins
  only). Now `GET /bindings` marks options the sets refuse `blocked` and
  says which tiles the caller may wire (`approvable`, on the map and on
  each pending row); every picker (tile popover, admin console binding tab,
  organisations tile, the root bind prompt) greys refused options out, a
  refused `custom…` ref snaps the select back and shows the reason inside
  the section, a tile you can't wire shows its wiring read-only, the root
  prompt and the ⚑ badge list only slots you may bind. The ⚙ popover also
  sizes to its content instead of opening at 70 % of the window. (v0.3.39
  shipped the popover's snap-back reading a stale snapshot, so a
  *successful* re-bind jumped back to the previous value until a second
  pick — fixed right after.)

- **Installer: missing distro tools are installed on every system run.**
  The package step (`uidmap`, `fuse3`, git/curl/tar) was only planned for a
  fresh build-from-source install; a prebuilt install, and any upgrade,
  skipped it even after preflight had said "will install uidmap" — so
  `newuidmap`/`newgidmap` could stay missing and uid-range mapping degraded.
  The step now runs whenever a required tool is absent, prebuilt or source,
  fresh or upgrade.

- **Menus polish.** A right-click (or touch long-press) anywhere inside a
  tile now opens its menu — the injected client relays the event out of the
  iframe, unless the tile handled it itself or the target is an input, link
  or editable text. The tile menu is a little wider and the four squares'
  labels line up. The ⚙ tile admin is a popover rather than a window: still
  wide and resizable, but click outside or Escape closes it, nothing to
  drag, no ✕ (a sheet on phones keeps one). Fixed: opening a tile's logs
  before its terminal had ever loaded could throw ("Terminal is not a
  constructor") — the two xterm loaders now wait for the shared script tag.

- **Context menus that carry a tile's actions; the tile admin is a window
  (D56).** Right-click the empty canvas for **open tile** — the five most
  recently opened tiles that aren't on this screen plus a find box over
  every readable tile — **create a new tile** as a specific owner (mine ·
  each org you hold Create in · workspace; `tileCreation: org-only` hides
  the personal entry), and **new screen** ("new sidebar folder" left the
  menu; the sidebar's ＋ folder stays). Right-click a card head or a
  sidebar row, or press the new **⋯** on either, for the **tile menu**: four
  squares — terminal · logs · source · change proposals (with the open
  count) — then open/close on this screen, pin/unpin, open full page, and
  for admins the lifecycle actions plus Access… / Runtime… / Vault… / Roles
  & grants… / Interfaces… / Backup… / Cron…, each opening the tile's admin
  at that section. The ⚙ button now opens a real **admin window**:
  draggable, resizable, one per tile, Escape closes, a full-screen sheet on
  phones — replacing the 340px popover whose binding pickers overflowed.
  Long provider and interface names ellipsize with the full text on hover
  instead of forcing a scroll, and `bx-multiselect` lists are now
  viewport-fixed so no container clips them. Menus and the admin window
  are bottom sheets on narrow screens, opened from ⋯ or a long-press on a
  card head, a sidebar row or the empty canvas. New core element
  `/vendor/bx-menu.js` (shell chrome, not a tile API); `bx-frame` gains
  `open(layout)`. Docs: overview/04-frontend.md, getting-started.md,
  elements.md.

## 2026-09-05

- **Org screens edit like dashboards; the sidebar is one tree per owner
  (D55).** Org screens no longer auto-save every drag: the shell shows a
  shared screen read-only (even to editors), **edit layout** opens a
  personal draft, and **Save and update for everyone** publishes it — or
  **Discard**. Saves carry a **revision**; someone else saving first gets
  you a conflict dialog (*reload theirs* / *overwrite with mine* / keep
  editing) instead of a silent clobber; the screen bar shows who saved
  last and when; drafts survive a reload, and a draft whose screen is
  deleted is copied to your own screens. Members can hide an org tab (it
  stays in the org's sidebar section to reopen), order org tabs among
  their own, and **copy to my screens**; org admins rename from the tab
  and can **replace** an existing org screen with a personal one. The
  sidebar drops the APPS/TILES directory headers: every owner section
  (mine · each org · workspace) is one tree — **shared folders** curated
  by the org's admins (or ws-admins for workspace-owned tiles) with the
  same draft/save flow, plus every remaining readable tile flat at the
  root, and the org's screens. Personal top-level folders work as before.
  Protocol: `GET /screens` gains `rev/updatedBy/updatedAt` per org screen
  and a `folders` map; `PUT /screens/org` takes `rev`/`force` (stale →
  409 with the current screen), returns `{id, rev, …}`, and accepts a
  tiles-less meta-only body; new `PUT /screens/folders`. Non-breaking: a
  tiles write without `rev` is still accepted as a legacy overwrite, so a
  workspace whose `shell/` predates this keeps working — take the
  `scaffold:shell` builtin update for the new UX. Docs: auth.md §Shared
  screens and folders, overview/04-frontend.md, protocol.md.

- **Organisation network sets — per-org egress policy (D54).** A startup's
  orgs rarely want the same network: `devs` the office LAN + internet,
  `infra` all of `10/8` + the host network, `sales` the internet only. A
  **network set** is a named rule list (`internet`, `internet:<host|*.glob|
  ip|cidr>[:port]`, `lan:<ip|cidr>[:port]`, `host`, `provider:<tile-glob>`
  — the `net:` allowance forms without the prefix) a workspace admin attaches
  to orgs by reference. For an org's **own tiles** the union of its sets is
  the **ceiling** on `net` bindings (an uncovered ref is refused at write
  naming the set; one that turns uncovered later — a narrowed set, a
  transfer — goes **inert** with the reason surfaced everywhere the binding
  shows), the org admins' **allowance** (bind anything inside without
  asking), the **default binding** — the new builtin `org`, the live union,
  so an org tile that declares `net` simply has its org's reach — and the
  egress of **terminals** opened on those tiles (new scope `org`; members
  need no `termNet`, which now governs personal/workspace tiles only). A set
  may grant `host` (every org-bound tile and terminal shares the host netns
  — loud warning). New builtin `none` pins any tile offline. Same-org
  provider tiles need no rule. Set/attachment edits restart the affected org
  tiles. Surfaces: admin console **network sets** tab (typed-row editor with
  inline hints + reach preview) and the org card's **network** block; the
  binding tab, tile popover and organisations tile offer `org`/`none`/
  `custom…` and label refused options "not covered"; the terminal scope menu
  is rendered from the session frame (`scopes`, `netNote`) and shows 🏢 *org
  network (…)*; `bx netset ls|set|rm`, `bx org set --net`, `bx org ls`
  `net:`/`reach:`, `bx bind … net=org|none`, `bx iface` `default:org`/inert,
  `bx status` `net org → …`, `bx doctor` checks. Protocol: `GET/PUT/DELETE
  /net-sets`, `PATCH /orgs {netSets}`, `orgs[].netSets/resolvedNet/netHost`,
  `/bindings` `pending[].default` + `inert`, `/runtime` + `/tile-status`
  `netRef/net/netSource/netNote`, `GET /term-net`, terminal `?net=` absent =
  default. Non-breaking: old bindings and personal/workspace tiles behave as
  before; an older xbind resolves `org`/`none` to no egress (fail closed) and
  drops `netSets` on its next persist. Hostname globs are allowed in set
  rules only (per-tile bindings unchanged, D35). Docs: auth.md §Network
  sets, overview/12-egress.md, isolation.md, protocol.md, bx.md.

- **SSO-driven org management for multi-org teams (D53).** Everything a
  small company with exec / sales / infra / compliance orgs needs to run
  xbin on its identity provider without hand-editing memberships:
  - **IdP groups → members.** Each org card gains rules mapping a provider
    group to a membership shape (`sales@corp.com → developer`;
    `PUT /orgs/<org>/sso-groups`, `bx org sso-groups`). At every SSO sign-in
    the user's groups are reconciled: rule-wanted memberships are created or
    re-set with provenance (⟳ *synced*), and a synced membership whose group
    **or rule** is gone is **removed** — offboarding by group. Manual rows
    are never touched (manual wins); *detach* turns a synced row manual. If
    the provider fails to return groups, nothing is removed and the failure
    shows in the console. Providers: generic OIDC `groups` claim (name
    configurable, UserInfo fallback), GitHub teams/orgs (`read:org`),
    Google Workspace via the Cloud Identity API (group emails). Extra scopes
    are requested only while rules exist.
  - **Workspace admins by group** (`adminGroups`), guarded: a hand-promoted
    admin is never demoted by the IdP, the last enabled admin never loses the
    role, every change is audited. The console warns about self-joinable
    groups.
  - **Admin console.** A **sign-in** sub-tab (token login, SSO with group
    sync, *test connection*, *groups seen* — what the IdP actually sends —
    and **SSO-only sign-in**). The users table scales: filter by id/name/
    email/org, chips (admins · disabled · invited · never signed in · stale
    30d+ · no org · one per org) that AND-combine, a **last sign-in** column,
    org memberships as first-class pills (★ org admin, ⟳ synced), an
    **orgs…** row editor (join/leave/preset/detach, one request per click),
    a *more ▾* menu for the rare actions (incl. **sign out everywhere**),
    and **disable all shown** for offboarding. Add-user defaults to SSO when
    it is active, fills the id from the email, and can join an org at
    creation. Org cards show member counts, an *add member … as* preset,
    synced pills with *detach*, and the IdP-group rules with a datalist of
    groups seen. Sessions tab: filter + per-user sign out. The manager
    tile's owner picker now applies to clone / template / import / git too.
  - **API.** `PUT`/`DELETE /orgs/<org>/members/<user>` (single membership;
    org admins too), `PUT /orgs/<org>/sso-groups`, `DELETE
    /users/<id>/sessions`, `POST /auth-settings/sso/test`; `POST /users`
    takes `orgs`; `GET /users` carries `lastLogin`/`lastLoginVia`/`roleVia`/
    `ssoGroups`; `/auth-settings` carries `passwordLoginDisabled` and
    `sso.groupSync`; members/whoami carry `via`. `bx org member` edits one
    row per call (`--detach`), `bx user add --org`, `bx user signout`,
    `bx user ls` shows last sign-in. Downgrade note: an older xbind drops the
    new fields on its next save (rules would need re-entry).
- **FIX: SSO email bindings were lost on daemon restart.** `users.json`
  loading dropped the `email` field (v0.3.31–v0.3.32), so every binding set
  with `--email` / the users table vanished on the next xbind restart — and
  the next save persisted the loss, after which a returning SSO user was
  JIT-provisioned as `<id>-2`. Fixed with a reload regression test. **If
  your daemon restarted since binding emails, re-set them** (`bx user set
  <id> --email …`); a stray `<id>-2` account can be deleted.

## 2026-09-04

- **Provisioning: SSO pre-provisioning, new-account defaults (incl. default
  org membership), and an org-only tile-creation policy (D52).** Three
  gaps in running a workspace on SSO, closed together:
  - **Pre-provision SSO-only accounts** — no password, no invite link: the
    add-user form's *sign-in: SSO* mode, `bx user add <id> --sso --email
    a@corp.com`, or `POST /users {sso:true, email}`. The bound email's IdP
    sign-in is the credential. The users table also gained an **email**
    button to set/clear a row's binding.
  - **New-account defaults** — admin console → orgs → *new accounts* (`bx
    defaults`, `GET/PUT /defaults {newUsers}`): tiles, create patterns,
    term-api/term-net, and **org memberships** (org + level + Create) that
    every new account starts with — admin-added, invited, *and* SSO
    auto-provisioned. Seeded once at creation as a union with the request;
    rows stay individually editable; never grants admin (JIT accounts are
    always role `user`). This is "everyone from the SSO domain lands in org
    X as a developer".
  - **Tile-creation policy** — `tileCreation: org-only` (same card / `bx
    defaults set --tile-creation org-only`) stops non-admins from owning
    tiles personally: all five creation paths refuse a `user:` owner and
    resolve an unspecified owner to the user's single Create org (several →
    name one; none → refused). Admins unaffected. `GET /whoami` reports
    `tileCreation`; the manager tile's owner picker drops "me" under it.
  - `POST /clone`, `/builtins/import`, `/templates/new`, `/git/import` now
    accept `owner` like `/create` (previously always creator-owned).
- **Base image: agent CLIs updated and PINNED — claude-code 2.1.260, codex
  0.153.2, opencode 1.18.27.** The claude-code/codex installs were unpinned
  "latest", which in practice froze at whatever was current when the docker
  layer was first built — the cache served claude-code 2.1.207 across a
  month of releases. All three now pin via Dockerfile ARGs, so updating the
  base image is: bump the ARG → release (the base version is a hash of the
  recipe, so a bump automatically rolls `xbin-base-version` — upgraded
  installs swap the base and open terminals offer "⬆ base update"). The
  rebuild also refreshed the @latest Go tools (gopls, dlv, golangci-lint)
  baked into the image.

## 2026-08-25

- **SSO sign-in: "Sign in with Google / Keycloak / Okta / Entra / authentik
  / GitHub".** Generic OIDC (authorization code + PKCE, ID token verified
  against the issuer's JWKS) with provider presets, plus a GitHub OAuth2
  path (verified primary email; GitHub has no OIDC). Configure it in the
  admin console's sign-in security panel or `PATCH /auth-settings {sso:…}`.
  No self-signup, ever: a verified IdP email signs in as the user row whose
  new **`email` field** matches (`bx user set <id> --email a@corp.com`), or
  — when you list **allowed domains** — JIT-provisions a default-access
  `user` account (Google additionally requires the Workspace `hd` claim, so
  consumer accounts can't slip through). Requires the new **`--external-url`**
  / `XBIN_EXTERNAL_URL` (the stable public console URL the
  `/login/sso/callback` redirect URI is registered under; also improves the
  printed boot-login and invite links). The client secret is write-only and
  lives with the user store (the vault is sealed at boot — a login secret
  there would be circular); SSO discovery/token calls are the daemon's only
  outbound HTTP (`HTTPS_PROXY` honored). Apple is deliberately unsupported
  (no static client secret; private-relay emails defeat domain rules).
  Password login continues to work alongside. New deps: `golang.org/x/oauth2`,
  `coreos/go-oidc/v3`. Details: docs/auth.md §SSO, D51.

## 2026-08-24

- **Tiles can trigger file downloads again: `allow-downloads` +
  `xbin.download` / `xbin.url`.** The ND8 frame sandbox silently blocked
  every tile-initiated download (blob + `<a download>` clicks AND
  `Content-Disposition: attachment` navigations) — it even broke the admin
  tile's backup restore-one-file flow. The sandbox (iframe attribute + CSP
  header) now carries `allow-downloads` for all tiles (ND10): downloads
  cross no workspace/session/tile boundary, and the browser's download UI
  is the consent surface; popups/top-navigation stay blocked. New client
  helpers: `xbin.download(filename, data, type?)` hands a
  Blob/ArrayBuffer/string to the browser as a named download, and
  `xbin.url(path)` returns a frame-token-carrying URL string for tag-driven
  requests that can't set headers — the way to stream large downloads
  straight from your backend (`<a href="${xbin.url(...)}" download>` + an
  attachment response). Build such URLs at click time (the token is
  short-lived), and trigger downloads from a user gesture. See
  docs/elements.md §Isolation and docs/sdk.md.

## 2026-08-14

- **security: credential-less tile-subresource reads now require a
  recently-authenticated source IP.** The `/c/<tile>/` exception for
  sandboxed-frame asset loads (JS/CSS/images — browsers can't attach
  credentials to tag loads) was authorized by Fetch-Metadata headers alone,
  which any non-browser client can forge. xbind now additionally requires a
  successful authentication from the request's source IP within the last
  hour, so drive-by internet scanners with no login get 401 even with
  perfectly forged headers. **No tile/builder action needed** — a tile's
  subresource loads always follow its authenticated document load from the
  same IP, so browsers are unaffected. Note for **operators behind a reverse
  proxy**: set the new `--trusted-proxies` / `XBIN_TRUSTED_PROXIES` (proxy
  IPs/CIDRs) or all clients share the proxy's IP for this gate (and for the
  login throttle, and session IP attribution).
- **security: `X-Forwarded-For` is no longer trusted from arbitrary
  clients.** It is honored only when the immediate peer matches
  `--trusted-proxies`, and the chain is read from the **rightmost untrusted
  hop** (the address the proxy appended and vouched for) — a client behind
  the proxy prepending its own XFF entries gains nothing. Previously any
  client could spoof it to bypass the per-IP login throttle (or lock a
  victim IP out). Default behavior with no flag: the peer IP is always
  authoritative.
- **new endpoint: `GET /api/xbin/sessions`** (admin / `xbin:users`) — live
  browser sessions with login/last-seen client IPs and activity times; the
  caller's own row is marked `current`. Session ids are credentials and are
  never returned; stateless bootstrap-token logins don't appear.
- **admin tile: new "sessions" tab** under user management — the session/IP
  table above, so IP attribution (and the warm-IP gate's view of the world)
  is operator-visible.

## 2026-08-12

- **Installer: prebuilt bundles by default, fail-fast network preflight,
  pinned-input currency checks.** A real-world install died ten minutes in:
  the fuse-overlayfs build container couldn't fetch the Alpine package index
  (host networking fine, container networking broken — the classic fresh
  podman trap). Three structural fixes: **(1)** when the pinned release
  publishes a bundle for the host arch, `install.sh` now uses it
  automatically — no podman, no Go, no build containers on the target at
  all (`--build-from-source` opts out; untagged refs, repo checkouts, and
  unbundled arches still build). **(2)** Source builds preflight their
  remote inputs before anything mutates — Alpine index + Go tarball
  reachability from the host, and, once an engine exists, an in-container
  egress probe with a host-vs-container diagnosis — and the build steps
  retry once on transient registry/CDN errors. **(3)** New
  `hack/check-pins.sh` (run by `deploy/publish-release.sh`, fatal) guards
  pin drift, EOL currency via the endoflife.date API, and artifact
  reachability — it immediately caught that the `alpine:3.20` build pin
  had been **4 months past EOL**; the pin is now 3.22. The behavior change
  rides the next tag (the xbin.dev bootstrap always delegates to the
  tagged installer).

## 2026-08-11

- **Template updates actually merge now.** Instances of builtin templates
  always had a `template` git remote and a documented
  `git fetch template && git merge template/main` upgrade path — but their
  repos started from an unrelated `git init` root, so that merge refused
  with "unrelated histories". New instances are now **seeded from the
  template's repo** (D50): main = the template snapshot plus one commit of
  instantiate rewrites, so upstream template fixes merge cleanly with the
  snapshot as the common ancestor (you still pick what to adopt — an
  instance is a fork). `bx template updates` and the Tile Manager's
  Template tab list instances that are behind; instances created before
  this release are flagged `legacy` — their first merge needs
  `--allow-unrelated-histories`, after which they're normal.

- **Agent template: markdown responses, folded tool output, capped tool
  results.** Assistant messages (and the streaming draft) render as
  sanitized markdown (raw HTML from the model is shown escaped, links are
  scheme-checked and open in a new tab). Long tool outputs in the timeline
  collapse behind a "show full output" expander instead of walls of text,
  and the backend now middle-elides tool results over 16 KiB before they
  enter the transcript — an unbounded file read no longer rides every
  subsequent LLM call until compaction. Existing agent instances pick all
  of this up via the template-updates path above.

## 2026-08-10

- **Builtin updates can arrive as PRs: `bx builtin update <id> --pr` /
  "Propose as PR".** The old conflict path wrote `<<<<<<<` markers straight
  into a live tile's files (and recorded the new base before you resolved).
  Mode `pr` instead files the update as a change proposal against the tile —
  kind "builtin update", carrying the version bump, changelog, and per-file
  status — which the tile's own terminal/agent reviews and merges with
  `git am --3way` (clone-first recipe in the PR body keeps markers out of
  live files entirely). Update tracking refreshes **when the PR closes
  merged**, not before; rejecting keeps the update offered; a newer xbind
  auto-withdraws stale open proposals. The Tile Manager's Updates tab makes
  "Propose as PR" the primary action for customized/conflicted builtins;
  adopted units (no recorded base) — which `--merge` refuses — get a working
  ours→upstream proposal too. `--merge` remains as the legacy path.
  Design: D49 (building on the D48 PR channel).

- **Cross-tile change proposals ("code PRs"): `bx code pr`.** A terminal can
  write only its own tile; changes for a *sibling* tile now travel as
  proposals instead of workarounds: clone the target's repo out of the
  read-only mount, commit, `git format-patch`, then
  `bx code pr <target> --title … -m … *.patch`. The target's own
  terminal/agent lists its inbox (`bx code prs`), reviews the diff, applies
  with `git am --3way`, and closes `--merged`/`--rejected` (with a note the
  author's agent reads); `--withdrawn` is the author's. Opening needs no
  grant — read visibility *is* the suggest capability (D48) — and xbind
  never applies a patch itself, so the target plane stays in control.
  Surfaces: `/api/xbin/code/prs*` + `/code/pr*`
  ([protocol.md](/docs/protocol.md)), a **⇄ PRs tab** in the terminal
  window (diff view, review thread, merge/reject), **⇄ badges** in the
  shell sidebar and card headers for tiles with open proposals, and a `pr`
  event on `/ws/events` (read-filtered). Agent workflow + rules (review
  before apply, close the loop): workspace `AGENTS.md` §Suggesting changes.
  Design: D48.

## 2026-08-08

- **Prebuilt install bundles: `install.sh --prebuilt-rootfs`.** Skip the
  multi-minute, multi-GB build (podman/docker + Go + apt + Chromium): download
  a prebuilt bundle for the host arch — native binaries + base rootfs + SDK,
  one `.tar.zst` per arch — verify its sha256, unpack, done. No podman, no Go,
  no build. With no value it fetches this release's manifest from its GitHub
  Release; `SPEC` may be a `release-manifest.json` URL/path, a bundle tarball,
  or a local unpacked bundle dir (also `XBIN_PREBUILT=<spec>`). Maintainers
  build + publish with `deploy/publish-release.sh`. The base rootfs Dockerfile
  is now architecture-parameterized, so **amd64 and arm64** bundles are both
  possible (arm64 previously never built — the toolchains were x64-hardcoded).
  See [operations.md](/docs/overview/15-operations.md).

## 2026-08-07

- **Rootfs: agent CLIs install on CPUs without AVX2 (bun SIGILL fix).**
  v0.3.22 installed the JS agent CLIs via bun, but bun's stock x64 build
  requires AVX2 (Haswell+) and crashed with "Illegal instruction" on cloud
  VMs without it — shipping (loudly, via the inventory step) an image
  missing claude-code, codex, pnpm and yarn. The rootfs now selects bun's
  build by the host's CPU (baseline/x86-64-v2 unless avx2 is present; the
  rootfs runs on the host it's built on), verifies bun actually executes,
  and falls back to npm per-tool when it can't — so the agent CLIs land on
  any hardware. opencode (release binary) was unaffected. Rebuild the
  rootfs to pick this up.

- **Rootfs: agent CLIs install reliably; bun ships in the base.** The base
  image installed claude-code, opencode and codex in ONE npm transaction —
  opencode's broken npm postinstall rolled all three back, and the
  best-effort guard shipped a green image with no agent CLIs. Now: each
  tool installs in its own step (failures are independent), claude-code
  and codex install via **bun** (now in the base at `/usr/local/bun`, on
  terminal/backend PATH — installs are ~10× faster and it's a runtime
  builders use anyway), and opencode ships as its pinned official release
  binary, sidestepping its npm postinstall entirely. A final inventory
  step prints any missing tool loudly in the build log and stamps
  `/etc/xbin-rootfs-tools` into the image. Also: node bumped to 22.23.2 —
  current pnpm requires ≥22.13, so the old pin shipped a silently broken
  pnpm. Rebuild the rootfs (`make rootfs`, or the installer's upgrade
  path) to pick all of this up.

## 2026-08-04

- **Fixed: code grants also cover component discovery.** Yesterday's fix
  opened `/c/` reads for `code`-granted elements, but `GET
  /api/xbin/components` still filtered by tile read access — a tile with
  the bare `code` grant (read all source) listed only itself plus chrome,
  so tooling like a code-stats tile saw 3 of 30+ components. The listing
  now includes everything the element's code grant covers.

- **Fixed: `code`/`code:<component>` grants read source via `/c/` again.**
  The org-improvements read clamp (2026-08-02) made element principals
  self-only on the static plane, which also cut off elements holding a
  source-read grant — tooling backends (linters, search, stats) could no
  longer fetch sibling files. Grant-holding elements now pass the `/c/`
  read gate (broker-installed hook; grants stay in the grant table); the
  D4 injection still mints frame tokens only for principals with tile
  read access, so grant-based reads never receive the other tile's
  credential. Also fixed: the admin workspace-totals charts rendered
  invisible (SVG-namespace bug in the multi-line sparkline).

- **`code`/`code:<comp>` grants now open the `/c/` static plane for element
  principals.** The 2026-08-02 element read clamp had made backend instance
  tokens self-only on `/c/`, so a code-granted tooling tile could no longer
  fetch sibling source over plain HTTP (only via `/api/xbin/code/*`). With an
  approved `code:<comp>` (or blanket `code`) grant, `/c/<comp>/<file>` reads
  now work for the granted element too; HTML served this way never carries
  the target tile's frame token.

- **Resource sizes no longer re-walk trees on every poll.** `GET /runtime`
  measured walk-heavy resources inline — a full recursive walk of every
  filesystem/blob tree (for an encrypted container store, hundreds of
  thousands of cipher files) and a full kv bucket iteration, per 2s admin
  poll. Sizes are now TTL-cached (30s) and refreshed by a single background
  measurer; `/runtime` never blocks on a walk. A just-declared resource
  briefly shows "measuring…". sqlite (one stat), cron and bus counts stay
  live.

- **Admin resources tab: workspace totals + per-type resource tabs.** The
  live-stats view now opens with four workspace-total charts (CPU, memory,
  I/O, IOPS summed across tiles, ~3 min of history) with a "by org" toggle
  that splits each chart into one line per owner. The brokered-resources
  table below became one tab per resource type (filesystem, sqlite, kv,
  blob, bus, cron) with type-appropriate columns — bus resources show live
  **events/min** (from a new cumulative publish counter on `GET /runtime`
  resources; in-memory, resets with the daemon). Also fixed: `filesystem`
  resources reported no size in the table.

- **Tile frontend isolation: tiles now run in sandboxed opaque origins —
  BREAKING.** Every non-chrome tile document is served with a CSP `sandbox`
  header and framed by `bx-frame` with the `sandbox` attribute (plus
  `credentialless` in Chromium): a tile's JS can no longer read the shell's
  or a sibling tile's DOM, `localStorage`/IndexedDB/cookies are gone inside
  tile frames, and the ambient session cookie is worthless on requests out of
  a tile — xbind drops it via a Fetch-Metadata gate, so a raw `fetch` without
  the frame token no longer authenticates as the signed-in human. The frame
  token is now a tile's *only* credential and works standalone (cookie-less
  renewal included). Tiles that legitimately act as the human must become
  trusted chrome (`"chrome": true` in xbin.json, host-set only — as
  tiles/organisations now is). Migration:
  [/docs/changes/2026-08-04-tile-frontend-isolation.md](/docs/changes/2026-08-04-tile-frontend-isolation.md)

## 2026-08-03

- **Container stores: writeback now opt-in; round-trip diet shipped
  instead.** The writeback cache (and the 60s kernel cache timeouts) moved
  behind `XBIN_GOCRYPTFS_WRITEBACK=1` after a production build hit an
  unexplained `close()`→EIO that correlated with it (never reproduced
  elsewhere, including a full subuid-topology rerun of the same build —
  both ways). The default path got faster differently, by deleting FUSE
  round trips: every `close()` no longer sends a FLUSH (`FOPEN_NOFLUSH` —
  ours was a semantic no-op), the kernel no longer asks for
  `security.capability` before every write (`FUSE_HANDLE_KILLPRIV`, with
  the implied suid/sgid/fscap clearing on write+truncate now done — and
  tested — daemon-side), `fsync` uses the open handle instead of a path
  re-walk plus open/close, and the async request backlog is 64 deep
  (was 12). Census on a real dpkg upgrade through fuse-overlayfs: FLUSH
  ops −100%, GETATTR −40%, ~13% fewer round trips overall; the win grows
  with per-op latency (loaded servers), and append-style workloads now
  beat the pre-writeback baseline. Rebuild gocryptfs (`make gocryptfs`).

- **`XBIN_GOCRYPTFS_NOWRITEBACK=1`** (set in xbind's environment) reverts
  single-tenant mounts to pre-writeback kernel caching — an A/B and
  mitigation switch for suspected writeback interactions; mounts pick it
  up on the next remount (restart).

- **Container stores: FUSE writeback cache.** Single-tenant (container-store)
  mounts now opt into the kernel's writeback cache, via an xbin patch on
  go-fuse (`hack/gofuse-patches/` — upstream carries the capability flag but
  never wired an opt-in). Small writes batch in the page cache and reach the
  encryption daemon as few large requests (measured: 4096 sixteen-byte
  writes on one fd arrive as a single 64KiB WRITE), and **shared writable
  mmap now works** on these mounts — notably fixing tools that mmap their
  state, e.g. SQLite in WAL mode. Chunked-write workloads measure ~25%
  faster; image-commit speed is unchanged (its cost is per-file FUSE round
  trips, not data writes). Sound because a single-tenant mount's backing
  tree has exactly one writer — the mount itself. Attr/entry/negative-dentry
  kernel caching is also stretched to 60s on these mounts (same argument),
  and no-op chmod/chowns (tar extraction re-applying what create already
  set) skip the encrypted identity-xattr rewrite. Rebuild gocryptfs
  (`make gocryptfs`) to pick it up.

- **Fixed: container-store image builds could die with `Permission denied`
  mid-install.** The per-inode identity/capability caching added earlier
  today had no invalidation when a file was deleted. On backing
  filesystems that recycle inode numbers (ext4, xfs — btrfs never does),
  a freshly created file could be served the *deleted previous
  occupant's* cached identity: an `update-alternatives` symlink (`which`)
  reported as a regular non-executable file, or a phantom
  `security.capability` on a just-unpacked binary — both make `execve`
  fail with `EACCES`, killing `dpkg` maintainer scripts (exit 126)
  partway through large `apt-get install`s. The gocryptfs patch now
  drops cache entries when an inode loses its last link (unlink, rmdir,
  replacing rename), never applies a cached identity to a symlink, and
  reads through the cache for unlinked-but-open files. No store
  migration; rebuild gocryptfs (`make build` / `hack/build-gocryptfs.sh`)
  so mounts pick up the fix.

- **Live per-tile stats in the admin console.** The resources tab now leads
  with a live table of every running tile: CPU %, memory, I/O MB/s and
  IOPS (read+write, syscall-level — resource/FUSE I/O included), pids —
  each with an inline sparkline, click a row for full-size charts (~3 min
  of history at 2s cadence). Columns sort on click, a prefix filter
  narrows by tile name, and "group by org" buckets rows under their
  owner with per-org totals. Backed by a new demand-driven sampler in
  xbind (cgroup-v2-exact under the installed service's `Delegate=yes`;
  /proc-tree sampling in dev) surfaced as `stats` on `GET /runtime`
  ([protocol.md](/docs/protocol.md)).

- **Container-store speed: identity caching + overlay storage.** Two fixes
  for slow small-file work (npm, image builds) on single-tenant stores:
  the gocryptfs patch now caches the virtualized identities *and* the
  `security.capability` answer the kernel re-requests before every write
  in memory, per inode (sound because the daemon is the sole writer of a
  single-tenant cipher tree) — stat/chmod-heavy work is back at stock
  gocryptfs speed. And the devbox tile now prefers the `overlay` storage
  driver via fuse-overlayfs when the sandbox has `/dev/fuse` (a build
  step writes only its diff; `vfs` copied the entire rootfs chain per
  layer — quadratic, and brutally slow through an encrypted store). `vfs`
  remains the fallback and `DEVBOX_STORAGE_DRIVER` still overrides;
  switching drivers keeps the old driver's images on disk — re-pull.

- **Container stores work on encrypted resources.** A `filesystem` resource
  of a `cap:containers` scope now mounts in gocryptfs *single-tenant mode*
  (an xbin patchset on the pinned gocryptfs, `hack/gocryptfs-patches/`):
  ownership/mode/whiteouts/file-caps are virtualized into encrypted xattrs
  and in-mount permission checks are skipped, so podman's layer store —
  0555 dirs, sub-uid chowns, `security.capability` — round-trips on the
  encrypted mount. No new manifest surface and no on-disk format change;
  the mode follows the `cap:containers` grant automatically. Container
  tiles also get a cgroup2 view at `/sys/fs/cgroup` (libpod requires one)
  plus `/dev/net/tun` and `/dev/fuse` device nodes (pasta/slirp4netns
  networking, fuse-overlayfs storage) — and can drop their self-mount
  workarounds. Host requirement:
  `user_allow_other` in `/etc/fuse.conf` (system installs enable it;
  `bx doctor` checks). See [resources.md](/docs/resources.md).

- **The installer knows when xbin is already there.** A no-flag non-root
  run on a box with a system-wide install now leads with that fact
  (version, running state, listen address) and makes upgrading the
  default — plain Enter at the chooser sudo-upgrades; user mode is
  offered as a separate second instance. Side-by-side installs stop
  colliding on the port: a fresh user instance auto-moves to the next
  free port when 8642 is taken (stated in the plan and the summary),
  upgrades preserve the existing unit's port, and an explicitly
  requested busy `XBIN_LISTEN` fails the plan up front.

- **Ubuntu 26.04 LTS everywhere**: the base rootfs image (terminal +
  backend sandboxes; existing terminal env layers stay pinned to their
  old base, which upgrades preserve as `rootfs-<version>`) and the macOS
  Lima VM image both move from 24.04 to the current LTS. Bonus for
  container-host tiles: 26.04's podman ships netavark, so bridged
  container networking works out of the box.

## 2026-08-02

- **SECURITY — restricted terminals now mount an allow-list view (D40).**
  A production check showed a non-admin tile terminal could enumerate
  unreadable siblings' NAMES (each deny-mask is a visible tmpfs) and read
  the workspace root `xbin.json` — the entire grants/bindings topology,
  public hostnames included. Restricted terminals now get a staged view:
  only readable tiles are mounted (unreadable ones are absent, names and
  all), `xbin.json` is redacted to rows referencing only readable
  components, `go.work` is filtered to readable modules (so builds don't
  chase absent dirs), and `.xbin`/`data`/other homes simply don't exist
  inside — no masks, no resenc names in the mount table. Admin terminals
  keep the full read-only view. If your agent tooling relied on reading
  another (unreadable) tile's source from a non-admin terminal, that was
  the leak: ask for `read` on it.

- **Upgrades backfill essential tiles, and terminator owners control their
  domains (D41).** Workspaces created before `tiles/organisations` existed
  now get it installed at boot (the shell's ⚑ button targets it) — once:
  the backfill is ledgered in `data/backfills.json`, so deleting the tile
  afterwards sticks. `bx builtin updates` lists missing essential tiles
  and `bx builtin update tiles/organisations` installs one (bare names
  resolve). Workspaces already using defaultTiles get a read entry for the
  new tile; others are left for the admin to decide. And ingress consent
  now follows terminator ownership: an org admin can publish org tiles
  through the org's own terminator — and approve outside tiles publishing
  through it — without any allowance; host ports and the builtin listener
  still need one.

- **The installer grew a user-only mode and a plan-before-approve flow.**
  `curl … | bash -s -- --user` installs xbin entirely under your own
  account: `~/.local/opt/xbin`, a systemd *user* unit with lingering, no
  root anywhere — anything that would need root (missing distro packages,
  a missing subuid range, an AppArmor userns restriction) is caught in
  preflight and reported with the exact one-line root command, before
  anything is touched. Both modes now print a numbered plan of exactly
  what this run will do (steps already in place listed as skipped) and
  ask once; `--check-only` stops after preflight + plan. The website
  shows both commands.

- **Hidden tiles (D42).** Tiles can now be hidden: a lifecycle state that
  is exactly `disabled` (backend stopped, refuses to spawn) plus removal
  from sidebars and listings. The workspace sidebar, the admin console's
  components table and access map, and the organisations tile's org-tiles
  list all filter hidden tiles behind a "show hidden (N)" toggle, render
  them dimmed with a badge when shown, and offer hide/unhide wherever
  lifecycle controls live (the ⚙ panel too). Same D24 gate as the rest of
  lifecycle: the tile's owner, its org's admins, or a ws-admin. `bx hide` /
  `bx unhide`; /components rows now carry the lifecycle `state`. Hiding an
  offloaded tile is refused (restore first); placed tiles stay on screens,
  rendering their disabled state.

- **macOS installs, for real.** `curl -fsSL https://xbin.dev/install.sh |
  sh` on a Mac now sets up a lightweight Linux VM via Lima (Apple's vz
  runtime; qemu fallback pre-macOS 13) sized by you at install time
  (defaults 32 GiB thin disk / 4 GiB RAM / 4 CPUs), runs the regular Linux
  installer in system mode inside it pinned to the same release, forwards
  the UI to the Mac's loopback :8642, and prints the one-time login URL —
  with the same numbered plan-before-approve contract as the Linux
  installer. Requires Homebrew for Lima (never installs Homebrew itself);
  re-running upgrades xbin inside the existing VM; `limactl delete xbin`
  uninstalls. The Linux installer's macOS refusal now points at the
  one-liner instead of a bare "get a VM".

- **The installer chooses with you, not for you.** Run without sudo and
  without a mode flag and it explains system vs user mode (system creates
  a dedicated `xbin` user for better separation), prints BOTH numbered
  plans — the system plan from read-only probes, re-verified after
  escalation — and asks: [s]udo into the system install from right there,
  [u]ser-only, or quit. `--system` without root now shows the full
  read-only plan and offers to sudo instead of dying; `--yes` never
  guesses a mode.

- **Publishing from the bind dialogs actually works now, and org admins
  got a wiring surface.** The root page's bind panel (and the
  organisations tile's pending-binds card) rendered exposed endpoints
  with just a provider picker — no way to enter the hostname/zone/port,
  so "publish" always died with a silent 400. Expose rows now carry the
  route editor (host or zone for http, listen for stream), the button
  stays disabled until the route is filled, and every server refusal
  renders inline. The organisations tile gained a **wiring & ingress**
  card: pending slots for org tiles with the full editor, active
  bindings with their routes and one-click unbind — publishing through
  your org's own terminator needs no allowance (D41); host ports and
  the builtin listener still do.

- **BREAKING — transfers grew a preview, side effects, and a tighter
  receive rule (D39).** ([migration note](changes/2026-08-02-transfer-create-bound.md))
  Every transfer surface (organisations tile, new admin-console owner
  editor, `bx owner --transfer`) now shows an impact report before the
  confirm: your own post-transfer access level, bindings/grants that die
  under the new owner's ceilings, and approval-plane changes. The
  transfer then unbinds fully-dead binding slots and restarts affected
  backends, so a tile moved into a net-denying org loses egress NOW, not
  at some future restart. Receiving a tile INTO an org now requires that
  org's **Create** knob (previously any member could) — transferring in
  is creating, capability-wise. The shell's right-click "Create a new
  tile" dialog gained the owner picker (me / your create-orgs /
  workspace for admins).

- **Org screens, a first-screen editor, sidebar that knows whose tile is
  whose, and self-service passwords (D37/D38).** The sidebar now groups
  tiles into *mine / each org / workspace* with an org filter (tree view
  preserved inside groups). Ws-admins can save any screen as the
  **workspace default** every new user starts from — no more hand-editing
  root/index.html — and org admins can share screens to their org: **org
  screens** appear as tabs for every member, with an edit knob choosing
  who may rearrange (admins / write-level / all members; everyone else
  views read-only). Users change their own password from the new
  my-account section (POST /account/password), and org admins can reset a
  non-admin member's password by re-minting their invite link — the
  forgotten-kid-password case no longer needs the workspace admin. Admin
  console polish: successes render green (not in the error slot), the
  rotated owner token and invite links land in copy-fields instead of
  prompt() dialogs, and secrets/resets use inline forms. The organisations
  tile gained org-admin deep visibility: ceiling rows (read-only),
  member presets, per-member override badges ("clamps org level" /
  "excluded"), and org-screen management.

- **Re-review fix batch (incl. one security fix).** A second five-story
  review of the shipped model confirmed every prior finding fixed and
  surfaced a short list, all addressed:
  - *SECURITY:* per-tile read RBAC now binds **element principals** on the
    `/c/` static plane — previously any tile's frame token could read every
    other tile's source/manifest regardless of the driving user's access
    (pre-existing, not from the recent waves). Elements read their own tile
    always; beyond it the attributed user's access decides; unattributed
    backend tokens are self-only.
  - Provider-org admins can now actually **withdraw** a consumer's binding
    to their tile (DELETE carried no refs, so the documented D33 withdraw
    lever never matched; the stored binding decides now).
  - Allowance hostname globs cover the apex: `net:internet:*.stripe.com`
    also matches `stripe.com` (the TLS-wildcard footgun pushed admins back
    to unfiltered internet).
  - Request dismissals stick: a manager's dismissal starts a 24h re-file
    cooldown, and users excluded by an exact `none` entry get told so
    instead of re-filing forever. Withdrawing your own request stays free.
  - Pending-request hints now include the self-serve detour
    (`transfer:org:<id>`) when moving a personal tile into your org would
    put it under the org's allowance.
  - `bx doctor`: recognizes D35 `net:internet:<spec>` allowances (was a
    false positive), treats suspended org admins as absent, and flags exact
    entries that clamp below a member's org level (stale approvals). The
    access map renders exact `none` exclusions instead of flattening them
    into "never granted"; disabled users no longer show "invited".

- **Disable, suspend, ask-for-access, and hostname-granular egress
  (D34-D36).** The second wave of review follow-ups, all additive:
  - *Account disable (D34):* `bx user set <id> --disable` (admin tile
    toggle) pauses an account — login, sessions, live terminals and invite
    links all refuse — while keeping every grant, membership and owned tile
    for `--enable`. Lockout-guarded. Org admins get the org-scoped little
    sibling: a member's **suspended** knob pauses one membership (confers
    nothing, stays listed).
  - *Ask for access (D36):* navigating to a tile you can't read now shows a
    request-access page (owner named, one click) instead of a bare 403;
    `bx access <tile> request [level]` does the same from a shell. The
    tile's owner / org admins see the queue in the organisations tile (and
    `bx access <tile>`), approve into an exact entry or dismiss; requesters
    can withdraw. The ⚑ badge counts these too.
  - *Filtered internet egress (D35):* `bx bind apps/x
    net=internet:api.stripe.com:443` restricts a tile's egress to named
    hosts/CIDRs/ports — hostnames enforced by DNS pinning in the egress
    relay (rebinding into the LAN pins nothing). Allowances grant it with
    globs and CIDR containment: `net:internet:*.stripe.com` or a
    `net:lan:10.0.0.0/8` entry that lets org admins carve out any narrower
    subnet. Complex filtering stays in net-provider tiles.
  - Invite links now also return an absolute `inviteLink`, the invite page
    warns when you're already signed in (so you don't burn someone else's
    single-use link), and the login/invite cards no longer clip on small
    phones. A fresh workspace's shell shows a one-time "secure this
    workspace" checklist until the first admin account exists.

- **BREAKING — the ownership model grew teeth: org-governed access, exact
  overrides, provider-side approvals, backend-only secrets, user
  attribution.** ([migration note](changes/2026-08-02-ownership-fixes.md))
  The fixes from the five-story UX review, in one wave:
  - *Access resolution (D31):* an org-owned tile is governed by the org —
    personal pattern grants and workspace defaults no longer leak into org
    tiles; an **exact per-user entry is authoritative** (override down, or
    `none` to exclude one member from one tile).
  - *Create-as-org actually works:* a member with the org's Create knob can
    create org-owned tiles with no personal canCreate pattern (the D25
    promise, previously dead code) — manager tile, organisations tile, `bx
    new --owner org:<id>`, raw POST /create.
  - *Provider-side consent + nobody is blind (D33):* admins of the org that
    owns a tile now SEE its consumers and may approve/withdraw grants and
    bindings targeting their property; requesters see their own pending
    requests with who-can-approve hints; the shell ⚑ badge counts real
    pending approvals and updates live (new-pending events); grant rows
    record who approved (`approvedBy`).
  - *Allowance granularity (D32):* `tile:<pat>@<role>` / `res:<glob>@<role>`
    cap the delegable role; `iface:<svc>@<tile>#<instance>` pins a provider
    and instance (share the dev instance, not prod); entries validate per
    class at write time instead of storing dead strings.
  - *Vault (D30, BREAKING):* secret VALUES are readable by the tile's
    backend only — admins and tile terminals list/set/rotate but never
    read; tile frontends can't reach the vault API at all.
  - *User attribution (D29):* backends receive `X-XBin-User` +
    `X-XBin-User-Level` for the driving human; SDK `Caller(r)` carries
    `User`/`UserLevel` + `UserCanWrite()` so tiles can gate in-app.
  - Also: lifecycle is now the owner's (org admins can stop their runaway
    tile), org admins can read their org's policy rows, deleting a user
    reports the tiles that fell to workspace-owned, `bx doctor` learned
    ownership checks, and `bx new --team` (dead teams-era flag) became
    `--owner`.

- **Invite links: onboard users without sharing passwords (D22).** Create a
  user with no password (admin tile, `bx user add --invite`, or plain
  `POST /users`) and you get a **single-use, 72h invite link** to send them —
  they set their own password on a themed page and are signed in.
  `bx user invite <id>` / the users-tab **invite** button re-mint (and
  invalidate) links for existing users — credential delivery and
  reset-by-link in one. Tokens are hashed at rest and redemption is
  login-throttled. **No self-signup**: accounts still only come from admins.
  The login page's stale "one-time token URL" note was fixed too.

- **The ownership UX: organisations tile, admin-console rework, owner
  pickers.** A new pre-installed **`tiles/organisations`** tile is the
  delegated surface: members see their orgs, owned tiles and sharing; org
  admins manage members, org-tile ACLs/transfers, and one-click-approve the
  pending grants/bindings their allowance covers (with the resolved allowance
  shown). The admin console's user-management group is now *users ·
  organisations · permission sets · access map* — member role editors with
  Admin/Developer/Viewer presets, a permission-set editor, an allowance/owned-
  tiles view per org, a workspace **defaults** editor, and owner-based
  provenance in the access map. The Tile Manager's create form gained an
  **Owner** picker (me / orgs where you may create); `/components` now carries
  each tile's owner; the shell's ⚑ button opens the organisations tile (the
  old org popover is gone) and first-screen seeding is filtered to tiles the
  user can read. New `bx owner`, `bx permset`, and reworked `bx org
  member/sets/allow` commands; a fresh workspace seeds `defaultTiles`
  (welcome/apidocs/organisations → read).

- **BREAKING: ownership replaces teams & positional org paths.** Components
  now have an **owner** (a user or an org, transferable) recorded outside the
  workspace; org membership is a flat `{level, create, admin}` role applied
  org-wide to org-owned tiles; sharing a tile is an ownership right (exact
  `user:`/`org:` ACL entries); workspace admins can delegate grant/binding
  approval to org admins via **allowances** and reusable **permission sets**
  (`cap:containers`, `net:*`, `gpu:*`, ingress publication — everything except
  the `xbin` capability family); `defaultTiles` gives every user baseline
  visibility. Teams, `basePermission` and the `o/<org>/` path convention are
  removed — no released workspace used them, so there is no data migration.
  See [changes/2026-08-02-ownership.md](changes/2026-08-02-ownership.md),
  docs/protocol.md, and docs/auth.md §Ownership (D24–D28).

## 2026-08-01

- **`bx` works from the host without fiddling with tokens.** Run on the host
  (not inside a terminal), `bx` now auto-reads the workspace **owner token** from
  `.xbin/token` — locating the workspace via `XBIN_WORKSPACE`, a walk-up from the
  cwd, or the default `/opt/xbin/workspace` — so `sudo -u xbin bx ls` just works.
  A non-privileged user still can't read the 0600 token (unchanged). When no
  token is found, the 401 now explains how to fix it instead of only "sign in at
  /login". (`bx term`/`bx login` were never commands — bx prints usage for
  unknown ones.)

- **Sidebar & tabs: filter, nested folders, reorderable + parkable tabs.** The
  component tree gains a **filter box** (matches tile and tab names, auto-expands
  folders). **Folders nest** — drop a folder onto another to nest it, onto empty
  space to un-nest. **Screen tabs drag to reorder**, and can be **dropped into a
  folder** to park them in the tree: the tree entry is the *live* screen (not a
  snapshot), so closing its tab keeps the layout and clicking it restores the
  screen exactly. Parked screens leave the tab bar until reopened.

- **Mobile workspace mode.** The shell now adapts below 820px: the sidebar
  becomes an off-canvas drawer (tap ☰), tiles stack full-width instead of the
  mouse-driven snap-grid (drag/resize off on touch), and terminals and floating
  windows open as full-screen sheets. Desktop is unchanged. Tile authors: your
  tile becomes a full-width card on phones (its own height, content scrolls
  inside) — make sure it's usable narrow (see workspace `AGENTS.md`).

## 2026-07-30

- **Tiles can report status & notifications to the workspace.** A new
  self-scoped channel lets a component surface its condition: `xbin.status(level,
  message)` / `xbin.clearStatus()` (frontend) and `xbin.Status` / `xbin.ClearStatus`
  / `xbin.Notify` (Go SDK), levels `ok|info|warn|error`. The shell renders it as a
  breathing dot on the tile's sidebar entry (and its folder), a tint on the
  screen tab holding an affected tile, and a mark in the browser-tab title;
  `xbin.notify` raises a one-shot toast. Status is **persistent and
  self-clearing** (set `ok` to clear) and **resets when the backend restarts**.
  New `GET/POST /api/xbin/tile-report` + a `status` event on `/ws/events`
  ([protocol.md](protocol.md), [elements.md](elements.md)). Guidelines — when to
  use each level and the always-clear-it rule — are in the workspace `AGENTS.md`.

- **Code browser: line numbers, change counts, an Analysis tab, and live
  refresh.** The terminal's code panel (`bx-code`) now shows **line numbers** in
  the file view, a **change-count summary** (`+add −del · N files`) on the
  working-tree/commit diff and per-commit in the Changes list, and a new
  **Analysis** tab charting commit activity over time (commits/week for the last
  year, top authors, totals) — including the **upstream** tracking branch when a
  component was git-imported. The file/changes views also **refresh
  automatically** when a tile's files change on disk (agent or terminal edits),
  instead of showing stale content. `GET /git/log` now returns `add`/`del`/
  `files` per commit and there's a new `GET /git/activity` endpoint
  ([protocol.md](protocol.md)).

## 2026-07-14

- **New capability: `cap:containers` — run containers inside a tile**
  ([container-host tiles](/docs/changes/2026-07-14-container-tiles.md)). A **container-host tile**
  (rootless Podman/Docker spawning sub-containers — the substrate for "dev
  sandbox" tiles) declares `uses: [{target:"cap:containers", role:"writer"}]`.
  It's an **admin-only** reserved grant (lands pending on import) that keeps the
  tile's user-namespace capabilities and swaps the backend seccomp block-list
  for a **minimal floor** (only host-damaging syscalls — module/kexec/reboot/
  swap/clock), so the mount family / `pivot_root` / `setns` a container runtime
  needs are available. Still fully rootless and namespaced — no host reach, no
  other-tile reach; the policy `xbin-caps` deny strips it. The tile supplies the
  runtime itself (Podman + subuid seeding + storage + networking, in its
  `setup`/manifest). Worked example: the new **devbox** builtin tile. See
  [changes/2026-07-14-container-tiles.md](changes/2026-07-14-container-tiles.md).

## 2026-07-12

- **Fix: disk quotas now count encrypted resources.** On an encrypted
  workspace (the production default) a `filesystem`/`sqlite`/`blob` resource's
  bytes are ciphertext under `data/resources-enc/…`, but the per-scope quota
  measurement (and the admin "resources" size view) only scanned the plaintext
  `data/resources/…` tree — which is an empty mountpoint once encrypted. So
  those resources counted **~0** toward a scope's quota and the low-disk
  block/alert never fired for the file plane. Both now measure the ciphertext
  footprint (summed with the plaintext tree for unencrypted / mid-migration
  scopes). `kv` was always counted correctly.
- **Security: `owner` is now a reserved component path.** A top-level
  component literally named `owner` would have made the proxy inject
  `X-XBin-From: owner`, so a callee's SDK (`Caller().Owner`) would treat it as
  the human owner — an impersonation across the identity spine. It joins
  `ingress`/`runtime` as a reserved top-level name (creating one is refused;
  an existing such dir stops being scanned as a component). The element proxy
  also now strips **every** inbound `X-XBin-*` header before injecting the
  verified identity (previously it enumerated three), so a caller can't feed a
  backend a spoofed `X-XBin-Ingress-Host` on the authenticated `/api` path.
  Low real-world likelihood (naming a top-level `owner` needs admin/top-level
  create rights), but a latent contract break — now closed.
- **Admin console: two-level navigation + filtering, built for large
  deployments.** The flat tab row is now grouped — **runtime**
  (components · resources · backup · cron), **user management** (users ·
  organisations · teams · access map), **vault**, **binding** (roles ·
  grants · interface providers · binding), **ingress** (endpoints ·
  services / expose). Every list view gained a live text filter (and
  scope/org category chips on the component + team lists) so it scales to
  thousands of tiles. The component list now merges manifest data with live
  backend state and expands to show **who can reach each tile** (its access
  relations) alongside the backend's runtime detail. Ingress **publishing is
  now a first-class surface** (ingress → services / expose) with the
  live routing table under ingress → endpoints. Old hash deep-links
  (`#overview`, `#runtime`, `#interfaces`) redirect to their new homes.
- **New: a top-down system overview** at [/docs/overview/](/docs/overview/00-index.md)
  (start at `00-index.md`). 17 short chapters walking the whole architecture —
  the core model, workspace anatomy, components & the backend lifecycle, the
  frontend & shell, identity & authorization, users/orgs/teams, the backend
  sandbox and the terminal plane, resources & the vault, interfaces & bindings,
  egress & net-provider tiles, ingress, tile lifecycle, deployment/operations,
  and extending xbin — explaining how the subsystems compose and why. The
  existing reference docs stay the field-level truth; the overview is the map
  that puts them in context. Served at `/docs/overview/` and on disk in every
  terminal (`$XBIN_DOCS/overview/`).
- **Fix: terminal read guard blocked the SDK, and workspace-root files like
  `AGENTS.md`.** Two bugs in the Landlock read guard, both surfacing as
  `Permission denied` on world-readable files in tile terminals (while `ls`
  worked, since only file *reads* are restricted):
  1. On the standard nested layout (`/opt/xbin/workspace`) the guard skipped
     the workspace's whole top-level path component, so nothing else under
     `/opt` was readable — `cat /opt/xbin/sdk/xbin.go` failed and backend
     `go build` couldn't read the SDK. The guard now grants siblings level by
     level down to the workspace root, and terminals' explicit read-only
     mounts (the SDK bind) are always allowed.
  2. Files granted directly at the workspace root (`AGENTS.md`, `go.work`,
     the `CLAUDE.md` symlink) were read-blocked because the grant carried
     `LANDLOCK_ACCESS_FS_REFER`, which the kernel rejects on a non-directory —
     the rule was silently dropped. A file is now granted read-only without
     `REFER` (directories keep it, so `apt`'s cross-directory renames still
     work). Workspaces at a top-level path (`/workspace`) were unaffected by
     (1); (2) affected every layout. The deny set (`.xbin/`, `data/`, other
     users' `homes/`) is unchanged. Restart xbind and reopen the terminal —
     the guard is per-process and inherited, so live sessions keep the old one.
- **Ingress — publishing tiles** ([docs/ingress.md](/docs/ingress.md)): tiles
  can now be reached from OUTSIDE the workspace, deliberately and
  owner-gated. A new manifest section `exposes` declares endpoints —
  `{kind:"http", paths:[…]}` (a hostname-routed site/API with a **public
  path allowlist**, default-deny) or `{kind:"stream", proto, port}` (the
  backend just `net.Listen`s; xbind relays a host port in, TCP or UDP).
  Declaring is inert: the **owner binds** each slot to an ingress source
  (`bx expose <tile> <slot>=<source> --host/--zone/--listen`, or admin →
  interfaces → ingress), exactly like interface bindings — the binding
  carries the route. Public traffic reaches the one bound tile as the new
  anonymous **`ingress` principal** (`X-XBin-From: ingress`, SDK
  `Caller(r).Ingress()`, public host in `X-XBin-Ingress-Host`), confined to
  the declared paths, with no reach into `/api/xbin/*` or sibling tiles.
  Sources: **`runtime`** — xbind's own second listener (`xbind
  --ingress-listen`, BYO TLS via `--ingress-cert/-key`) + host-port stream
  relays — or an **ingress terminator tile**: the new **Public HTTPS
  (Traefik)** builtin does automatic Let's Encrypt TLS in a sandboxed tile
  (certs in its own resource, never in the daemon). Also: delegated
  wildcard **zones** with tile self-registration bounded to the zone (`PUT
  /api/xbin/ingress-hosts`), direct tile→tile TCP via `{kind:"stream"}`
  interfaces (`bx bind app db=apps/postgres#pg` → `XBIN_IFACE_DB_ADDR`),
  `{kind:"lan-ingress"}` links for VPN/router-tile inbound, split-horizon
  hairpin (a tile using its own public URL routes straight back), a new
  policy-ceiling deny kind **`ingress`**, `bx ingress` + an admin ingress
  panel, and `GET /api/xbin/ingress[-routes]`. Host ports <1024 need
  `AmbientCapabilities=CAP_NET_BIND_SERVICE` on the xbind unit. Additive —
  nothing is published until you bind it.
- **BREAKING (net-provider tiles): `cap:net-admin` grant required.** A
  regression on 2026-07-10 (making every tile backend fully unprivileged)
  broke **net-provider tiles** — routers/firewalls that splice other tiles'
  egress (the `egress-approver` builtin, `examples/netrouter`): building their
  dataplane needs network-admin capabilities the sandbox now drops, so they
  failed at startup with `operation not permitted` (ip_forward / ip route /
  AF_PACKET). A provider now declares `uses {target:"cap:net-admin",
  role:"writer"}` — an **admin-only** reserved grant that makes the sandbox
  keep CAP_NET_ADMIN / CAP_NET_RAW / CAP_NET_BIND_SERVICE **inside the tile's
  own network namespace** (nothing reaches the host; every other cap still
  dropped, seccomp block-list unchanged). The shipped provider tiles declare
  it; approve it once in the grants panel (it lands pending). A workspace/org
  policy `net` deny strips it. See
  [changes/2026-07-12-net-provider-cap.md](changes/2026-07-12-net-provider-cap.md).
- Terminal window: a **read-only "logs" tab** (the ▤ button next to `>_`
  `{ }` `⇋`) streams the tile backend's captured stdout/stderr live —
  rendered in an xterm view for ANSI colors + scrollback, no input. It's
  gated exactly like the tile's terminal (admin, the tile itself, or a
  **terminal-level** user — read/write users don't get it, since backend
  output can carry secrets), so it only appears where a shell would. Backed
  by `GET /api/xbin/logs?component=<p>[&tail=<bytes>][&follow=1]` (text/plain
  tail + chunked follow; the HTTP twin of `bx logs -f`) — see protocol.md.

## 2026-07-11

- **Organizations & teams** (docs/auth.md → "Organizations & teams"): GitHub-
  style grouping on top of users. Orgs own the `o/<org>` path namespace
  (tiles at `apps/o/<org>/…`); teams grant tile access to members by union
  (effective level = max of own entries, team entries inside the org, org
  base permission); tiles can be created *in a team* (`POST /create
  {team:"<org>/<team>"}`, `bx new --team`, the manager tile's picker — the
  team is auto-granted its `newTiles` level); per-tile access is viewable/
  editable at `GET/PUT /api/xbin/access` (`bx access`, or the shell's
  per-tile ⚙ → access, which now also opens for org admins). **Org policy
  ceilings** (`/api/xbin/policy`, `/orgs/<org>/policy`, `bx org policy`, or
  the admin tile's row editor): pattern-keyed rows `{tiles,
  deny[net|gpu|xbin-caps], mayCall[]}` capping what the covered tiles may
  be granted — enforced at approval *and* at every evaluation, so
  hand-edited grants/bindings under a ceiling are inert (`mayCall` governs
  external reach only: a tile's own scope is always exempt, and capability
  targets are classified — bare `code` sits under the `xbin-caps` deny
  class, `code:<comp>` is governed like calling that component — so a path
  allow-list never silently strips `code:reader` source access; unapprovable
  pending requests are annotated `blocked`). Delegated **org admins**
  manage their org's teams/members/access from the shell's "orgs & teams"
  popover and per-tile ⚙ (workspace-security knobs — policy, term flags,
  org create/delete — stay workspace-admin); the sidebar groups org tiles
  under `o/<org>`. New API: `/api/xbin/orgs*`, `/access`, `/policy`;
  `whoami` gains `orgs` and, on element principals, the attributed driving
  `user` — scoped by tile trust (identity only; +own-org slice for org
  tiles; full list for `xbin`-capable tiles) — all in protocol.md. New CLI:
  `bx org|team|access`. **Tile-creation authority** is now uniform across
  create/clone/git-import/tile-import/template-instantiate: a user's
  `canCreate` patterns work everywhere (previously admin/capability-only
  for the copy/import routes), copy-shaped routes need read on the source,
  creating inside an org needs membership, and an element's
  workspace-management grant no longer extends the *driving* user's own
  create rights (the confused-deputy clamp). **BREAKING (edge case):** the
  path segments `o` and `u` are now reserved in NEW tile paths — see
  [changes/2026-07-11-orgs-and-teams.md](changes/2026-07-11-orgs-and-teams.md),
  which also covers updating the workspace chrome (`bx builtin update`) to
  get the new shell/admin/manager UI on existing workspaces.
- Admin UI: **permissions are fully click-through** — people are picked from
  chip dropdowns (small workspaces: everything enumerable), tile grants from
  row editors with a datalist of real paths/patterns (inert org patterns get
  a live ⚠), replacing every free-text spec/prompt in the users, orgs, shell
  org popover and ⚙ access surfaces. New **access map** tab in the admin
  tile: a visual org/teams/people structure (policy ceilings marked ⛔) and
  the resolved users × tiles **effective-access matrix** — click any cell
  for the full derivation (which entry/team/base/adminship contributes and
  which wins). Backed by `GET /api/xbin/access-matrix` (resolved server-side
  with provenance) and `GET /api/xbin/users-directory` (identity-only people
  list, reachable by org admins for their pickers) — both in protocol.md.
- terminal: **fixed multi-tab terminals rendering stacked in the first tab after
  a reload**, and **added per-tab close buttons**. On restore, every saved
  terminal reattached at once and each forced itself visible (`bx-terminal`'s
  `connectedCallback` set an inline `display:block` that overrode the host's
  `display:none` for inactive tabs), so they piled up in a vertical split until
  each tab was clicked. The element no longer sets that inline display (the
  `:host` rule is the standalone default). Tabs now carry an ✕ to close one
  (the whole window still has its own close), and typing `exit` closes just
  that tab — closing the last one closes the window. (web/bx-terminal.js,
  web/bx-frame.js.)
- terminal: **`apt install` of packages that create system users fixed** (was
  failing with `chown … Invalid argument` mid-configure on systemd/dbus/etc.,
  while simple packages installed fine). Two parts:
  - **Bug:** `/etc/subgid` is keyed by the *user*, but xbind looked it up by
    *gid* — so for any account whose uid ≠ gid (a `useradd --system` user like
    `xbin` at uid 999 / gid 988), a correctly-delegated sub-gid range was never
    found and the sandbox silently fell back to **single-uid mode**, where only
    container-root is mapped and dpkg's chown-to-system-user fails with EINVAL.
    Fixed to match both sub-id files by the user (name/uid).
  - **Diagnosis:** xbind now logs the uid-mapping mode at startup and warns
    loudly on the single-uid fallback (naming what's missing); `bx doctor`
    flags it from inside a terminal (`/proc/self/uid_map`). If the warning
    persists after upgrading, the host genuinely lacks the delegation — add the
    user to `/etc/subuid` + `/etc/subgid` and install `uidmap`
    (`deploy/install.sh` does both), then restart.
- terminal: **mouse/selection no longer drifts when the workspace font size is
  changed.** The workspace scales via CSS `zoom`, but xterm measures its cell
  size on a canvas (which ignores an ancestor's zoom) while reading pointer
  coords that honor it — so clicks, right-click, and text selection landed off
  by the zoom factor, worse the further from the terminal's top-left. The
  terminal now detects the ambient zoom (walking its ancestors' computed
  `zoom`, across shadow-DOM boundaries), counters it so xterm renders at
  net-zoom-1 (exact coordinate math), and re-applies the scale through xterm's
  own font size — same visual size, correct mouse. Ships with xbind; works even
  on a workspace whose shell predates this change. (web/bx-terminal.js.)
- terminal: **fixed terminals failing to start** (`exit 127` at spawn) on any
  workspace with encrypted resources. The read-only workspace bind had been
  made *non-recursive* to hide other tiles' resource (resenc) mounts from
  `mount`; but in a rootless user namespace those inherited mounts are locked,
  and the kernel rejects a non-recursive bind of a subtree with locked
  children (`EINVAL`) — the exact mirror of not being able to unmount them.
  Reverted to a recursive bind. Resource *contents* stay masked; their mount
  names may reappear in `mount` (benign — a terminal can already `ls` every
  tile; docs/isolation.md). Ships with xbind.
- terminal/sandbox: a terminal that dies during sandbox init now surfaces the
  reason instead of a blank pane — the failing pane replays the init's error,
  the daemon logs a sanitized tail of it (`journalctl -u xbin`), and
  `XBIN_SANDBOX_DEBUG=1` adds a per-step init trace. (Diagnostics; no
  behavior change.)
- users: **BREAKING — per-tile access tiers.** A user's `tiles` is now a
  `{path: level}` map with levels `read < write < terminal`, plus `canCreate`
  (path patterns the user may scaffold tiles under; creating auto-grants them
  terminal on it) and `termApi`/`termNet` (whether a non-admin's terminals
  get the live tile-API token / internet egress — both default off, `net=host`
  stays admin-only). `users.json` and legacy API bodies migrate automatically
  (array entries → `write`, `terminal: true` → `terminal`); API *responses*
  return the new shape, so update your workspace's admin tile. `bx user` flags
  changed accordingly (`--tiles a=terminal,b`, `--create`, `--term-api`,
  `--term-net`). Admin/owner behavior is unchanged. Migration note:
  [changes/2026-07-11-tile-access-tiers.md](/docs/changes/2026-07-11-tile-access-tiers.md);
  model: [auth.md](/docs/auth.md).
- terminal: **non-admin terminals are locked down by default**
  (isolation.md): source of tiles below the user's `read` level is masked out
  of the mount; no tile-API token without the `termApi` grant; no egress
  without `termNet` (query params are clamped, the session still opens); and,
  under cgroup delegation, each restricted session gets the same
  memory/pids/CPU caps as a tile backend. Admin terminals unchanged.
- xbind: terminal session logs now include spawn failures (ERROR + cwd) and
  exit status + uptime on session end, so a shell that dies at start is
  diagnosable from the server log.
- terminal: **`apt` fully fixed** (`apt update` + `apt install`) — the real
  cause of the `rename … Invalid cross-device link` was **not** fuse-overlayfs
  but the terminal's Landlock read guard: on an ABI-2+ kernel, enforcing any
  Landlock ruleset denies *reparenting* (cross-directory rename/link) with
  EXDEV unless the ruleset handles `LANDLOCK_ACCESS_FS_REFER`. The read guard
  handled only file-reads, so apt's `partial/ → parent` rename failed on every
  filesystem. The guard now handles + grants REFER on the same paths it already
  allows reading, so cross-directory renames work while secret reads stay
  denied. Ships with xbind — **no `make rootfs` needed** (the earlier apt
  working-dir relocation was treating the wrong cause and is reverted in the
  next base image, which also adds `strace`). (docs/isolation.md.)
- terminal: **restricted tier for non-admin users** (isolation.md). A shell
  opened by a non-admin user drops `CAP_SYS_ADMIN`/`CAP_SYS_RESOURCE` (+ other
  privileged caps) but keeps the file caps `apt` needs, and its user namespace
  is pinned so no nested user/mount namespace can be created — so it can't
  regain privilege via `unshare -Ur` or mount over its masks. Admin/owner
  terminals are unchanged (full caps for dev work). Ships with xbind; dormant
  until non-admin users exist. (D18.)

## 2026-07-10

- terminal: **resource mounts no longer leak in `mount`.** A tile terminal
  binds the workspace read-only+recursive, which cloned in every tile's
  gocryptfs resource (resenc) mount; their contents were already masked, but
  `mount`/mountinfo still listed other tiles by resource name. The terminal
  now detaches those submounts before masking `.xbin`/`data`. Safe by
  construction (both dirs are fully masked; the sandbox root is
  MS_REC|MS_PRIVATE, so the host's live mounts are untouched). No rebuild —
  ships with xbind.
- terminal: **`apt install` fixed** (`rename … Invalid cross-device link`)
  via apt config only — `Dir::Cache::Archives` moves the download cache to
  `/var/cache/xbin-apt`, a path absent from the base image, so at runtime it
  lives entirely in the writable overlay upper and apt's partial/ → archives/
  rename never crosses layers. No overlay-mount option (an earlier
  `redirect_dir=on` attempt on the shared overlay broke component backends'
  state and was reverted). Needs a `make rootfs` rebuild.
- terminal: fixed **doubled keystroke echo after a base-image upgrade / sandbox
  reset**. Resetting kills the session server-side then reconnects; the killed
  socket's onclose could still schedule a reconnect that reattached to the new
  session, leaving two sockets writing to one terminal (every byte, incl. the
  echo of what you typed, twice). A connection epoch now ensures only the
  latest socket drives the terminal or reconnects — also closing the same
  latent race on network/GPU/API-scope switches.
- base rootfs: added the tools agents reach for — full `vim`, OpenAI `codex`
  CLI, `chromium`+Playwright (system browser path), `gh`, `fd`/`bat`/`shellcheck`,
  Go `gopls`/`dlv`/`golangci-lint`, and `pnpm`/`yarn` — so a fresh
  terminal doesn't re-install them. (docker/rootfs.Dockerfile; `make rootfs`.)
- terminal: **`bx status`** now shows the current tile's runtime metrics
  (backend state/cpu/mem/pids/fds/conns/egress, disk usage vs quota, alerts)
  via the new read-only `GET /api/xbin/tile-status?component=` — readable with
  the tile-scoped terminal token (self), or any tile for admins. `--all` keeps
  the admin global view.
- terminal pop-up: a **code browser / git-review panel** (`bx-code`) beside
  the terminal. A collapsible file tree + syntax-highlighted viewer (vendored
  highlight.js, no build step) and a **Changes** tab showing the working-tree
  diff and per-commit diffs for review. Title-bar layout switch: terminal /
  code / resizable split. Read-only (editing stays the terminal's job);
  backed by the existing grant-gated /api/xbin/code/* + /git/* endpoints.
- **resource limits (blast-radius containment for shared/mixed-team
  workspaces).** Per-tile cgroup caps are now *enforced*, not just measured:
  memory.max 2 GiB (+ memory.high), pids.max max(512, ncpu×8), cpu.weight
  fair-share (burst when idle). Per-scope **disk quota** 50 GiB with `507` on
  kv/blob writes over it, plus low-disk (<10% free) write-blocking of the
  biggest users. Backends now run **capability-dropped + seccomp block-list**
  (mount/module/kexec/reboot/ptrace/bpf/keyrings/…). Terminals capped at 32
  per user. **Alerts** (`GET /api/xbin/alerts`) surface at-limit / low-disk /
  blocking events as a banner in the workspace shell and the admin console.
  Tunable via `XBIN_LIMIT_MEM` / `XBIN_LIMIT_DISK`. Details: docs/isolation.md
  §resource limits.
- terminals: **workspace secrets are masked from tile terminals** (Gap 0).
  The isolated terminal's read-only workspace mount now covers `.xbin/`
  (owner token + frame secret), `data/` (vault, encrypted resource state,
  password hashes), and other users' `homes/` with an empty overlay — so a
  shell (or an agent in it) can read every tile's *source* for API work but
  can no longer `cat .xbin/token` to re-grant owner, which had undercut the
  tile-scoped terminal token. Applies to every terminal, including your own
  tiles. Own tile dir + `$HOME` stay read-write. New per-terminal **tile-API
  toggle** (titlebar, default on): off mints no token — the shell sees code
  but every API call is unauthorized. Isolation property of `--isolate`;
  see docs/isolation.md for the honest bound.
- terminals: **seccomp mount guard** makes those masks umount-proof. A tile
  terminal shell keeps `CAP_SYS_ADMIN` (apt / nested namespaces / profiling
  still work) but a seccomp filter — installed before the shell, inherited
  across execve/unshare — denies `umount2`, `move_mount`, `open_tree`, and
  `mount(MS_MOVE)`, the four ways to remove or relocate a mask. So a shell
  that is root in its user namespace still can't peel a mask off to read the
  owner token. Collateral: `fusermount` and nested container/browser
  sandboxes that detach their old root get `EPERM` (run them on the host).
- terminals: **Landlock read guard** — a second layer that denies reading
  the secret *files* (`.xbin`/`data`/other `homes/`) at the VFS level, so even
  if a mask were peeled the owner token / vault / password hashes / other
  agents' credentials still can't be opened. seccomp can't filter by path
  (it can't deref the `open` arg); Landlock can. Restricts only READ_FILE, so
  exec/readdir/writes are untouched (collateral nil); best-effort where
  Landlock is unavailable. The admin **runtime** tab now shows each guard's
  kernel support (*terminal guard: mount ✓ · read ✓ (ABI n)*).

## 2026-07-09

- auth: **server-side session expiry** — browser logins now die after 12 h
  idle (sliding) or 30 days absolute, whichever first, so a stolen session
  cookie can't authenticate indefinitely (previously: valid until logout or
  restart). Tunable via `XBIN_SESSION_IDLE_TTL` / `XBIN_SESSION_MAX_TTL`.
- auth: **audit log** — every mutating core-API call (`POST/PUT/PATCH/DELETE`
  on `/api/xbin/…`, minus the `prefs`/`kv`/`blob`/`bus` data plane) logs an `audit` line
  with actor + path + status; a who-changed-what trail for governance actions
  (users, grants, lifecycle, vault, token rotation).
- users: **password floor** — accounts created/updated through the API need
  an 8-character minimum (enforced at the API so the dev admin/tests, which
  write the store directly, are unaffected); the admin UI surfaces create/
  reset errors instead of silently clearing the form.
- users: **user ids are now validated at creation and immutable** (D15) —
  `[a-z0-9][a-z0-9._-]{0,31}`, `owner` reserved. An id is the permanent key
  for `homes/<user>`, the prefs bucket, and `user:<id>` attribution, so it
  can't be renamed or contain path/attribution separators; the constraint is
  set before GA because it can't be tightened once ids exist on disk.
  Existing users (loaded from disk) are unaffected — only new creates are
  gated.
- auth: **owner-token rotation** — `POST /api/xbin/auth-rotate-token` (admin;
  a button under the admin console's Users → sign-in security) rewrites
  `.xbin/token` and swaps it live: the old token stops authenticating
  immediately, bearer and cookie alike. **Rotate once after upgrading to
  terminal-scoped tokens** — before 2026-07-09 every terminal carried the
  owner token, so old agent transcripts/shell histories under `homes/` may
  hold it. (Terminal tokens themselves already revoke automatically: each
  dies with its session; deleting a user kills theirs instantly.)
- shell: tiles' **title bars gain a `>_` terminal button** (replacing the
  tiny 7×7 corner square on shell cards; standalone frames keep the corner
  button), and a **workspace settings** menu (🔧 in the top bar) with a
  per-user **global font size** (persisted via prefs, applied as a clean
  zoom). The terminal settings icon is now a wrench (🔧) instead of a gear.
- **connection-leak fix, both sides of the proxy.** xbind built a fresh HTTP
  transport per proxied request, stranding one keep-alive connection per RPC
  — on the backend that parked a goroutine + ~15 KB forever (the SDK server
  had no idle timeout), and xbind leaked the matching client conn. The proxy
  now pools one transport per backend socket (reused across requests, idle
  conns reaped at 90s, evicted when a generation's socket goes away), and
  `xbin.Serve` sets `IdleTimeout: 120s` + `ReadHeaderTimeout: 30s` (hijacked
  WebSocket conns exempt; no blanket read/write timeouts — SSE/streams still
  run indefinitely). xbind's own TCP + gateway listeners got the same
  timeouts. Backends pick up the SDK fix on their next rebuild — every spawn
  rebuilds, so an xbind upgrade + restart covers the fleet.
- terminals: **tile-scoped tokens — the owner token no longer enters any
  terminal.** A terminal's `XBIN_TOKEN` is now a per-session token resolving to
  the TILE the terminal is opened on (its element principal: admin of itself +
  its approved grants/bindings — the frame-token model, for shells), never the
  human's privilege. Agents in tile terminals can no longer read other tiles'
  admin config, call `/api/xbin` admin endpoints, or **self-approve grants**
  (now enforced, not just documented). Session-open gates: terminals only on
  tiles the user may use; **the root terminal is disabled** (workspace-wide
  work: the browser UI, or the host shell — the owner token lives only in
  `.xbin/token`). Kill/reattach of another user's session: admins only.
  Deleting a user kills their live shells' API access. **Breaking** for
  workflows that ran admin `bx` from inside tile terminals — migration:
  `docs/changes/2026-07-09-terminal-scoped-tokens.md`.
- terminals: **$HOME is now per user** — `homes/<user>` instead of one shared
  `home/`, so each human's agent-CLI config (`~/.claude`, credentials), shell
  history, and dotfiles stay their own (the root token uses `homes/owner`;
  seeded lazily on first terminal; a non-admin can't attach to another user's
  session). A legacy shared `home/` **migrates automatically at startup** to
  the workspace's sole user/admin (else `homes/owner`) — xbind refuses to
  start only when both forms hold real data, so nothing is merged by guesswork.
  Migration note: `docs/changes/2026-07-09-per-user-homes.md`.
- terminals: **base-image versioning + safe upgrades.** A terminal's
  persistent sandbox layer (apt installs / system changes) is now stamped with
  the base image it was built on and **pinned** to it — xbind never stacks that
  overlay on a different base (which corrupts apt/dpkg state). When a newer
  base is installed the terminal tile shows a **base-update** banner; clicking
  it (or the reset ⟲) rebuilds on the current base — installed packages are
  wiped, your workspace files and `$HOME` are kept. The `/ws/term` `session`
  message carries `baseOutdated`. Base images are stamped by `build-rootfs.sh`;
  `install.sh` preserves the old base as `rootfs-<version>` on upgrade (legacy
  unstamped bases become `rootfs-v0`); xbind aborts startup if a pinned base is
  missing and GCs preserved bases once no terminal pins them. Design:
  `docs/isolation.md` §The dev layer.

## 2026-07-08

- terminal: a **settings menu** (the ⚙ that appears top-right on hover),
  starting with a **theme picker** — Default (dark-steel), Dracula, Nord,
  Solarized Dark/Light, Monokai, Gruvbox Dark, One Dark, Tango Dark, GitHub
  Light — plus a font-size stepper. Applied live, shared across open terminals
  (same document + other tabs), and persisted in the browser.

- lifecycle: **offloading a component now fully quiesces it.** Previously an
  offloaded tile kept firing cron, still surfaced pending access/binding
  requests, and stayed visible in the shell + the admin principals list.
  Now: the cron scheduler skips jobs for non-enabled components; `Pending()`
  and pending-bindings omit offloaded components; the shell hides them (out of
  the sidebar/folders, and any open card is closed on the reload); the admin
  console lists offloaded tiles in a separate section, not the main table.
  `GET /api/xbin/components` now carries each component’s lifecycle `state`.

- interfaces: **fixed a binding-role flap**. A provider with several http
  provides (e.g. llm-gw: `openai`=writer + `metrics`=reader) resolved a
  binding’s granted role from an *arbitrary* provide — provides live in a map
  with no stable order — so a caller bound to `openai` (writer) intermittently
  got `reader`, failing ~15–30% of calls with "role writer required". The
  role/endpoint/validation now select the provide **by the requester slot’s
  service**, deterministically. (Introduced when llm-gw gained its second
  provide; single-provide tiles were never affected.)
- llm-gw: the stats table groups digits with **commas** (`123,142`) instead of
  a thin space, which was hard to read against the column gaps.

- vault: **secret values are now readable only by the element they belong to** —
  not even the owner or an `xbin:admin` tile. Admins can still list keys and
  set/rotate/delete any vault (the admin console + per-tile mini-admin drop the
  "reveal" button and show masked values), but `GET …/vault/<comp>/<key>` is
  self-only, so a compromised admin tile can't exfiltrate secrets. **Breaking**
  for any admin/owner flow that read another element's secret value via the API
  (`bx vault get <other-component>` now 403s); the owning element's own
  `xbin.Secret()` is unaffected. Migration:
  `docs/changes/2026-07-08-vault-read-lockdown.md`; see `docs/auth.md` §Vault.
- shell: the sidebar bottom shows the **running xbind build commit** (`⬡ xbind
  <commit>`). The commit is baked in at build time (`make build` ldflags →
  `git describe`, falling back to the VCS revision Go stamps into the binary)
  and surfaced on `GET /status` as `version`.

## 2026-07-07

- Templates: a **template-instance update path**. Each builtin template is
  served read-only as a git repo (`GET /api/xbin/templates/<name>.git/…`, admin)
  and added to every instance as a `template` remote, so a builder pulls
  upstream fixes with `git fetch template && git merge template/main` (or
  cherry-pick) — in control of what they adopt, no auto-merge clobber. The
  builtin-tile updater still never touches template instances. Phase 7 of the
  agent-v2 program. Terminals now resolve the SDK's `http://xbin/…` gateway host
  for raw `git`/`curl` too (rewritten to `XBIN_URL` + owner bearer, scoped to
  xbind), so the `template` remote fetches. Existing instances (pre-Phase-7) add
  the remote once by hand — see `docs/overview/03-components.md` §Templates.
- New builtin tile **prometheus-viewer**: binds one or more components that
  expose Prometheus metrics (service `prometheus`, multi:true — e.g. llm-gw)
  and renders their `/metrics` as a live dashboard (per-source counters/gauges,
  current value + per-second rate + inline sparklines). Phase 6 of the agent-v2
  program.
- llm-gw: **preferred models** — one workspace default per use-type (`agent`,
  `chat`, `pipeline`, `vlm`, `coding`, `summarizing`), set on the tile and read
  by callers via `GET /preferred`, so a model choice lives in one place. Plus
  per-model **cost** tracking, a Prometheus `GET /metrics` endpoint (bindable via
  the new `metrics`/`prometheus` provide interface), and transient-error
  **retry/backoff** on the proxy (429/5xx, pre-first-byte, honors `Retry-After`).
  First slice of the agent-v2 program.
- agent template (v2, backend): model **tiers** (`general`/`code`/`memory`/
  `vlm`) — each empty tier resolved from llm-gw's preferred model for the mapped
  use-type, so a workspace sets models once and agents inherit; compaction and
  memory work run on the `memory` tier. Plus LLM-call **retry/backoff**
  (transport + 429/5xx, so a gateway reload no longer fails a run), the
  **provider-reported prompt tokens** as the compaction trigger (not a char/4
  estimate), a per-tool `toolTimeout` config field, and a **date-only**
  system-prompt prefix so prompt caching keeps hitting. Tools in one turn now
  run **in parallel** (bounded, each under `toolTimeout`); several
  `spawn_subagent` calls in a turn run as **parallel subagents**. Control tools
  (finish/ask_user/yield) stay sequential and stop the turn. MCP servers are now
  **bound** via an `mcp` interface (multi:true, service `mcp`, like the chat
  tile) rather than a static config list — the owner binds MCP providers in the
  Interfaces tab and their tools reach the model through the binding. New
  **`recall`** tool: FTS5 full-text search over the run's whole history
  (including turns compacted out of context), so detail folded into a summary
  is still retrievable. The wake heartbeat is now **on-demand** — registered
  only while runs are sleeping or mid-drive and removed when idle, so an agent
  with nothing pending stops polling (cron is no longer always-on).
- agent template (v2, scheduling): **cron-agents** — a session or the agent
  itself schedules recurring runs (`schedule`/`unschedule` tools, `/schedules`
  CRUD), each registering its own cron job. **Watcher mode**: a watcher schedule
  re-drives one persistent run with a "check now" nudge and, via a
  `state_changed` tool, keeps only the rounds that reported a change — no-change
  rounds are rolled back, so a long-running watch stays compact. A **Features**
  menu (`GET /features`, toggled in the config's `features` map) gates optional
  capabilities: recall, skills, streaming, vision, parallelTools, watcher.
- agent template (v2): **streaming** — with the streaming feature on, LLM calls
  stream tokens into a live per-run draft (`GET /runs/{id}` → `draft`) so the
  tile shows progress as it generates; retries only before the first token.
  **Multimodal tiers** — a message carrying image content routes to the `vlm`
  tier when the task model isn't vision-capable (name heuristic; override by
  setting the vlm model). `wireMsg` content is now text or an OpenAI
  content-parts array.
- agent template (v2): **skills** — a self-improving skill library (Hermes-
  inspired). The agent authors reusable procedures with `skill_manage`, lists
  them (`skills_list`), and loads one on demand (`skill_view`); the compact
  name+description list is injected into context. **`POST /runs/{id}/learn`**
  distills a run into a skill. `/skills` CRUD + storage in the agent's sqlite.
  Gated by the `skills` feature. (Background auto-curator deferred.)
- agent template (v2, tile): the control tile is rebuilt ~2× taller with a
  settings overlay — **Config** (model tiers + budget/iters/timeout), a
  **Features** menu (toggle recall/skills/streaming/vision/parallelTools/
  watcher), **Memory** CRUD, **Schedules** (create/enable/run-now cron-agents),
  **Skills** (view/edit), and **MCP** bind status — plus a **live streaming**
  draft in the timeline and a "learn skill from this run" action.

- auth: the `code` capability now also has a **blanket form** — `uses {target:
  "code", role:"reader"}` grants read-only source access to **every** component
  (for scanners/linters/stats), alongside `code:<component>` for one. Honored
  on `/api/xbin/code/*` and `/git/{log,diff}`.
- docs: corrected the workspace `AGENTS.md` §Sandbox — `deps/` symlinks are
  editing-plane only; their targets are NOT mounted in the backend sandbox
  (matches docs/isolation.md). Read a sibling's source via a `code:` grant, or
  call it over HTTP.

## 2026-07-06

- frontend: tiles can spawn workspace-level UI via the shell — `xbin.dialog(spec)`
  (a data-driven modal that escapes the iframe; resolves `{button, values}`) and
  `xbin.window(spec)` (a floating window framing one of the tile's own sub-paths,
  a real tile frame that uses `xbin.fetch` normally). New core element
  `<bx-dialog>`. Docs: elements.md §Dialogs & windows, sdk.md, protocol.md.

- interfaces/auth: new `code:<component>` capability — grant a component
  read-only access to *another* component's source (files + git log/diff via
  `/api/xbin/code/*`, `/git/{log,diff}`), owner-approved like any cross-scope
  grant. A component always reads its own source; admin reads any.

- **BREAKING** — interfaces: instance path prefixes registered via
  `PUT /api/xbin/iface-instances` are **provider-relative** and
  workspace-absolute (`/api/<self>/…`) registrations are now rejected with
  a 400. Providers that registered absolute paths must re-register.
  → [migration note](/docs/changes/2026-07-06-instance-paths.md)
- interfaces: multi-input slots (`{kind:"http", multi:true}` — a slot binds a
  SET of providers) and provider instances (`provides {instances:true}` +
  runtime registration, bound as `provider#id`). Injection shapes documented
  in [elements.md](/docs/elements.md); multiselect binding UI everywhere.
- interfaces (admin UI): http bind options are filtered by the slot's
  `service` contract (an `s3basic` slot no longer offers an `openai`
  provider).
- chat tile: binds multiple `llm` providers (model picker aggregates all) and
  any number of `mcp` servers — Model Context Protocol tools are offered to
  the model and executed in an agent loop. MCP providers serve
  Streamable-HTTP JSON-RPC at `/mcp` under themselves (`service: "mcp"`).
- llm-gw tile: multiplexes multiple named upstream backends; model ids become
  `<backend>/<model>` once more than one is configured; per-backend usage
  (reqs, tokens in/out, in-flight) shown on the tile.
- shell: per-tile ⚙ mini-admin on card title bars (admins), sidebar folders /
  collapse / resize, and a system-status footer (cpu, mem, disk, services,
  vault state, req/s). `GET /api/xbin/status` grew `host` and `traffic`
  gauges.
- workspace: `POST /api/xbin/clone {from,to}` forks a component (git history
  kept, old-path references rewritten); Tile Manager grew a clone tab.
  Prefer it over `cp -r`.
- terminals: `IN_SANDBOX=1` / `IS_SANDBOX=1` set in sandboxed terminals;
  terminal panes use a distinct `--bx-term-bg` theme token.
- vault: admin-tile barrier UI (seal state, first-time passphrase, unseal,
  rekey), `POST /api/xbin/vault-rekey`, `bx vault rekey`, and a red
  vault-sealed banner on the admin overview.
- auth: `PATCH /api/xbin/auth-settings` can disable owner-token *browser*
  login once an admin user exists (`bx` Bearer token unaffected).
- terminals: Ctrl+W now reaches the shell as word-erase instead of closing the
  browser tab (best-effort — some browsers reserve it); overlapping floating
  windows get a higher-contrast edge.
- terminals: exiting the shell now closes that terminal (its tab, or the
  window if it was the last one) instead of leaving a dead "[session ended]"
  pane; terminal tabs can be renamed (double-click).

## 2026-07-05

- **BREAKING** — the project renamed **buxon → xbin**: module
  `github.com/xbin-dev/xbin`, daemon `xbind`, env `XBIN_*`, API `/api/xbin/*`,
  headers `X-XBin-*`, manifest `xbin.json`, runtime dir `.xbin/`, JS global
  `xbin`. Workspaces are migrated on upgrade; external tiles need the same
  rename (see the bx-term-tile migration for the pattern).
- resources: encryption at rest is **always on** — filesystem/sqlite/blob are
  per-resource gocryptfs mounts, kv is envelope-encrypted; sealed vault ⇒
  resource unavailable + component held, never silent plaintext
  ([resources.md](/docs/resources.md)).
- terminals: sessions are persistent server-side (reattach with exact
  scrollback); component terminals are scoped read-only outside the
  component's own dir + `$HOME`.
- shell: tiles can be unpinned into floating windows (persisted per user).
- imports: a component whose `uses` reference nonexistent resources is
  rejected at import time instead of silently landing broken.
