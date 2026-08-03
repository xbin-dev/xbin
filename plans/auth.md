# XBin — Auth & Inter-Element Security Design

Supersedes the "single token, honor system" sketch (old D3/D9). Mental model:
**Notion, but every block is a modifiable component** that may talk to components
above/below/sibling and to global resources (databases, search, cron, …). The human
in the terminal is root; *running elements are least-privileged tenants of the
workspace*, and the auth system's job is to make element↔element and element↔resource
access explicit, role-scoped, auditable — and eventually mechanically enforced.

New sub-decisions raised here are marked `[ND#]` and tracked in `DECISIONS.md`.

---

## 1. Principals & trust levels

| Principal | Identity | Trust |
|---|---|---|
| **Owner** (the human) | login cookie / bearer token | root of the workspace |
| **Terminal session** | per-session terminal token bound to the tile it's opened on | acts *as the tile* (self-admin + its approved grants) — never the owner; the root terminal is disabled (plans/terminal-tokens.md) |
| **Element instance** (running backend, generation N) | per-generation instance credential minted by xbind | least privilege: nothing beyond its own API/files unless granted |
| **Element frontend** (its iframe in the browser) | owner cookie **+ frame token binding the request to the element** | acts *as the element*, not as the owner (§6) |
| **xbind / broker / cron** | internal | trusted computing base |

Two planes, different rules:
- **Editing plane** (terminals, `bx`, git): full *filesystem* access to the
  component being edited, but the API credential is tile-scoped — a shell (or
  the agent in it) holds min(user, tile), not the owner. Owner-privileged
  automation lives on the host (`.xbin/token`).
- **Runtime plane** (element backends and frontends): default-deny, grant-based,
  role-scoped. Terminals now follow the same identity model (terminal tokens),
  so all three element surfaces — backend, frontend, shell — are scoped.

## 2. Element identity

- xbind spawns every backend, so it mints identity at spawn: a random
  **instance credential** per process generation, delivered via the process's private
  gateway socket handshake (`XBIN_GATEWAY=.xbin/run/<h>/gw.sock` + token in env for
  v1). Credentials die with the generation — a stale binary can't act after swap.
- Identity **is** the component path (`apps/email`). No separate id space.
- Element frontends get identity via a **frame token** injected at HTML-serve time
  (the D4 injection point): bound to (owner session × component path), short TTL,
  auto-refreshed by `xbin-client.js` `[ND2]`.

Enforcement strength is tiered (§9): at tier 1 (single uid) a hostile element can in
principle steal a sibling's env token via `/proc`; tier 2 (per-scope uids) closes
that. The *model* is identical at every tier — tiers only change how hard identity is.

## 3. Authorization model: roles + grants (RBAC)

**Roles are defined by the callee, granted by the owner, verified by xbind,
enforced at the callee.**

### Declaring (callee side)

```jsonc
// apps/calendar/xbin.json
{
  "runtime": "go",
  "expose": {
    "roles": {
      "reader": "Read events and calendars",
      "writer": "Create and modify events",
      "admin":  "Manage calendars, sharing, settings"
    }
  }
}
```

- Role names are free-form; **convention: `reader` / `writer` / `admin`** with those
  approximate meanings, so builders can guess `[ND4]`. Descriptions are mandatory —
  they render in the grant-approval UI and in `bx api`.
- No `expose.roles` → element is callable by nobody but its own frontend and the
  owner.

### Requesting (caller side)

`uses` is the single grant-request list — element APIs and global resources under one
grammar (replaces the earlier separate `resources` key). Distinct from `deps`:
**`deps` = source/code visibility (editing plane); `uses` = runtime call rights.**

```jsonc
// apps/email/xbin.json
{
  "uses": [
    { "target": "apps/calendar",    "role": "reader" },
    { "target": "res:email/db",     "role": "writer" },   // own scope's db
    { "target": "res:workspace/search", "role": "writer" },
    { "target": "res:workspace/cron",   "role": "writer" }
  ]
}
```

### Granting

- Same-scope targets: **auto-approved** at the requested role (a scope is one app,
  one trust unit) `[ND5]`.
- Cross-scope / workspace-global targets: pending until the owner approves — once,
  via the grants panel (xbind-served UI) or `bx grant apps/email apps/calendar:reader`.
- Grants live in workspace `xbin.json`: a versioned, diffable, git-tracked
  capability table. Revoke = delete the line (xbind hot-reloads policy).
- A grant is `(caller component, target, role)`. It covers the caller's **backend and
  frontend** equally — where the call originates doesn't matter, *which element is
  calling* does.

### Verifying & enforcing

All cross-element traffic flows through **xbind's gateway** — there is no direct
element↔element path (sockets are private per element; at tier 2, permissions make
that real).

```
caller backend ──unix gw socket──▶ xbind ──callee's socket──▶ callee
caller frontend ──cookie+frame token──▶ xbind ────────────────▶ callee
```

xbind: authenticates the caller → looks up the grant → **default-deny** → strips any
inbound `X-XBin-*` → injects verified headers:

```
X-XBin-From: apps/email        (or "owner", or "xbin/cron")
X-XBin-Role: reader
```

Callee enforces per-route with SDK helpers:

```go
mux.Handle("GET /events", h)                          // any granted role
mux.Handle("POST /events", xbin.Role("writer", h))   // writer+
xbin.Caller(r)  // → {From: "apps/email", Role: "reader", Owner: false}
```

Role implication: `admin` ⊃ `writer` ⊃ `reader` for the conventional names (SDK
knows this ordering; custom roles are exact-match unless the manifest declares
`"implies"`).

The **owner** principal (browser on a non-element page, `bx`, curl with bearer)
passes every check as role `admin` — the human is root on the runtime plane too.

## 4. Vault: per-element secrets

Every element gets a private key-value vault for third-party credentials (IMAP
passwords, API keys…), so secrets stop living in source trees or env files.

- API (broker-owned): `GET/PUT/DELETE /api/xbin/vault/<own-path>/<key>`. An element
  can only reach **its own** vault; there is no cross-element vault grant — if two
  elements need the same secret, each stores it, or the owning element exposes a
  role-guarded API that *uses* the secret without revealing it (the right pattern).
- Owner manages any vault via the UI panel and `bx vault set apps/email imap-pass`.
- Storage: `data/vault/<scope-key>/…`, owned by xbind's uid, mode 0600 — readable by
  no element at any tier, and not by terminals via fs either (terminals go through
  `bx vault`, which is allowed because owner=root, but raw file reads are closed off
  once xbind runs as its own uid). At-rest encryption is v1-optional `[ND3]`:
  plaintext-on-disk under 0600 first; optional workspace master key (env-provided)
  later. Vault dirs are excluded from `bx backup` by default (opt-in flag), and
  `data/vault` is gitignored unconditionally.
- Delivery to code: fetched at runtime via SDK (`xbin.Secret("imap-pass")`), never
  injected into env (env leaks into `/proc`, logs, and child processes).

## 5. Global resources = elements with roles

Databases, search, cron are **broker-owned targets in the same RBAC grammar** —
`res:<scope>/<name>` or `res:workspace/<name>` — so "element uses calendar API" and
"element uses global search" are the same concept, grant flow, and docs shape.

| Type | Roles | Notes |
|---|---|---|
| `sqlite` | `reader`/`writer` | same-scope: direct file path handed at spawn; cross-scope: brokered HTTP(query) API only — this replaces the old honor-system `ro` file grant (closes old D9 at tier 2; at tier 1 the file path simply isn't disclosed cross-scope) |
| `kv`, `blob` | `reader`/`writer` | brokered API |
| `bus` | `subscriber`/`publisher` | topics per resource |
| `cron` | `writer` (manage own jobs) | element registers jobs targeting **its own** endpoints; xbind invokes them as `X-XBin-From: xbin/cron` + a role chosen by the element at registration, bounded by the element's own roles. Cron can never be aimed at a third element. |
| `search` | `reader`/`writer` | workspace full-text index (bleve); writers index docs under their own namespace, readers query across namespaces they could otherwise reach — **not implemented** (design placeholder; the shipped resource types are kv/blob/bus/cron/sqlite/filesystem) |

## 6. Browser-side model

The problem: everything is same-origin, so without extra measure any element's
frontend JS could hit any other element's `/api/*` riding the owner cookie —
RBAC with a hole in it. Attribution came first `[ND2]`; **enforcement** landed
with sandboxed tile frames `[ND8]`:

- Every non-chrome tile document is served with
  `Content-Security-Policy: sandbox allow-scripts allow-forms allow-modals`
  (covers direct-tab opens) and framed by `bx-frame` with the matching
  `sandbox` attribute. The tile runs in an **opaque origin**: no parent or
  sibling DOM access (either direction), no localStorage/IDB/cookies/SW, and
  subresource requests carry no ambient credentials (Chromium; elsewhere the
  server gate below strips them). Communication with the shell is
  postMessage-only, identity = `event.source` window comparison.
- The owner cookie **authenticates the human**, and humans act only from
  **chrome** — the shell, plus components whose manifest carries the host-set
  trust flag `chrome: true` (e.g. tiles/organisations, which deliberately
  raw-fetches as the signed-in user). Chrome frames are never sandboxed.
- The per-frame token is the tile's **only credential** and now authenticates
  **standalone** (no cookie required): sandboxed frames hold nothing else.
  `xbin.fetch()`/`xbin.ws()` attach it; renewal at `/api/xbin/frame-token`
  works cookie-less for the tile's own component. The token's embedded user
  id rides for attribution (D29) and per-tile static-file clamping only.
- **Fetch-Metadata cookie gate** (in the auth middleware): a cookie-bearing
  request with the opaque-origin fingerprint — `Sec-Fetch-Site: cross-site`
  on a non-navigation, or a non-GET navigation to `/api/*`/`/ws/*` (form-POST
  CSRF) — has the cookie dropped before principal resolution. The signal is
  unforgeable both ways (unsandboxed JS can't produce cross-site toward its
  own origin; sandboxed JS can't shed it). Top-level GET navigations keep the
  cookie (external links into the workspace, Lax-legit). Browsers without
  Fetch Metadata (pre-2023) fail open; the CSP sandbox still confines them.
- `/c/<tile>/` **subresource** loads (module scripts, CSS, images) arrive
  with NO credential at all — opaque origins strip cookies in both
  directions (verified in Chromium) and downgrade the Referer to nothing
  (strict-origin-when-cross-origin against an unserializable origin), and
  headers can't be attached to tag loads. They're authorized by the one
  signal the browser still produces: the opaque-origin Fetch-Metadata
  fingerprint (`Sec-Fetch-Site: cross-site` — `same-site` accepted for
  engine variants — plus a subresource `Sec-Fetch-Dest`:
  script/style/image/font/media/worker; never documents, frames, fetch, or
  `.html`). Unsandboxed JS cannot produce that fingerprint toward its own
  origin. Honest scope: headers are client-settable, so a determined
  non-browser client can spoof this to read tile source — the rule confines
  tile JS; it is no substitute for the vault. Humans keep cookie+RBAC reads
  for direct navigation.
- Tile `fetch()` needs CORS: an opaque-origin fetch is `Origin: null`, so
  xbind answers `Access-Control-Allow-Origin: null` (+ preflight) — safe
  because tile requests carry no ambient credentials anyway (cookie dropped;
  cross-site cookies never sent), the frame token stays the only way in.
- `bx-frame` also sets `credentialless` where supported (Chromium 110+):
  no cookies even on the document navigation, which then authenticates with a
  bootstrap `?frame=` token minted by the embedding chrome.
- Cookie is `HttpOnly` + `SameSite=Lax` (CSRF), `Secure` behind https proxy.
- Residual, documented: same-origin tiles share a renderer process, so a
  *browser exploit* crosses all of this — per-origin process isolation still
  means subdomain-per-scope (phase 5), and the `/c/` URL scheme maps onto
  that cleanly. Until then the VM/host remains the real outer boundary.

## 7. Documentation standard (elements teach their own auth)

Every exposing element ships a **standard, tooling-visible contract** so a builder in
a terminal can integrate in minutes:

1. `xbin.json` `expose.roles` — names + mandatory human descriptions (machine-readable
   source of truth; drives the grants UI).
2. **`API.md` in the component root** — required by convention when `expose` is set
   (`bx doctor` warns if missing). Standard skeleton (scaffolded by `bx new`):
   *Overview → Roles (table mirroring the manifest) → Endpoints (method, path,
   minimum role, request/response example) → Bus topics → Copy-paste `uses` snippet.*
3. `bx api apps/calendar` — renders roles + API.md in the terminal;
   `GET /api/xbin/components/<path>` serves the same JSON+markdown for UIs.
4. Optional `openapi.json` for the ambitious; never required, never validated in v1.

The point: the docs artifact is standardized *shape*, not generated magic — writable
in vim, diffable in git, readable by both `bx` and grant-approval UI.

## 8. SDK surface (Go; snippets for node/python)

```go
xbin.Serve(mux)                    // listen on element socket, graceful shutdown
xbin.Caller(r) CallerInfo          // verified {From, Role, Owner}
xbin.Role("writer", h)             // role-guard middleware (knows reader<writer<admin)
xbin.Client()                      // *http.Client through the gateway (auth automatic)
  client.Get("xbin://apps/calendar/events?day=…")
xbin.Secret(name)                  // own vault read
xbin.Bus().Publish/Subscribe(topic)
xbin.Cron().Register(spec, selfPath, role)
```

Frontend (`xbin-client.js`): `xbin.fetch()`, `xbin.bus.on/emit()`, `xbin.self`.

## 9. Enforcement tiers (attack-surface roadmap)

The model (identities, roles, grants, gateway, vault) is **fixed from phase 2 on**;
tiers only harden the floor under it. Running elements get less able to hurt each
other at each tier; the terminal stays root throughout.

| Tier | Mechanism | What a hostile element can still do | When |
|---|---|---|---|
| 0 | `--dev`, no auth | everything (dev only) | — |
| 1 | single uid; instance tokens; gateway default-deny; vault broker-owned | read sibling env via `/proc` (steal tokens), open sibling sockets, write any workspace file | phase 2–3 |
| 2 | **per-scope uids** `[ND1]`: xbind (root, per D13) spawns each scope's backends as a dedicated uid (20000+n); source dirs owner-writable/world-readable → elements **cannot modify any source, incl. their own**; sockets/vault/data enforced by fs perms; `/proc` closed | abuse whatever it was *granted*; local network egress | phase 4 |
| 3 | **mount ns (fs isolation) + netns egress control** (full design: `plans/isolation.md`); wazero syscall caps and subdomain-per-scope browser isolation as further options | approximately nothing ungranted | phase 5+ |

Tier 2 detail worth naming: **running elements lose write access to code** — editing
is exclusively the terminal's (owner's) power. Self-modification stays a first-class
*workspace* property while ceasing to be a *runtime* capability. Cross-scope shared-rw
sqlite at tier 2 uses a per-resource unix group (setgid data dir) or falls back to
brokered access; brokered is the default recommendation.

## 10. Non-goals (v1)

- Protecting the workspace from its owner or their terminals.
- Multi-user / multi-tenant (the model leaves room: `owner` becomes a set of users
  with per-user role grants — later).
- Secrets safe against a root-compromised container.
- Browser-plane isolation against a compromised renderer (shared-process
  exploits) — that still requires separate origins (subdomain-per-scope).
