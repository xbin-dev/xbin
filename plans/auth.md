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
| **Terminal session** | inherits owner | root — full fs, can edit grants, read anything via `bx`. Deliberately privileged: it's the editing plane. |
| **Element instance** (running backend, generation N) | per-generation instance credential minted by xbind | least privilege: nothing beyond its own API/files unless granted |
| **Element frontend** (its iframe in the browser) | owner cookie **+ frame token binding the request to the element** | acts *as the element*, not as the owner (§6) |
| **xbind / broker / cron** | internal | trusted computing base |

Two planes, different rules:
- **Editing plane** (terminals, `bx`, git): owner-privileged, unrestricted. Securing
  the owner against themselves is a non-goal.
- **Runtime plane** (element backends and frontends): default-deny, grant-based,
  role-scoped. This is where attack surface is reduced.

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
| `search` | `reader`/`writer` | workspace full-text index (bleve); writers index docs under their own namespace, readers query across namespaces they could otherwise reach — phase 5 |

## 6. Browser-side model

The problem: everything is same-origin, so without extra measure any element's
frontend JS could hit any other element's `/api/*` riding the owner cookie —
RBAC with a hole in it. Fix `[ND2]`:

- The owner cookie **authenticates the human**; a per-frame token **attributes the
  request to an element**. `xbin.fetch()` (in `xbin-client.js`) attaches
  `X-XBin-Frame-Token` automatically.
- `/api/<elem>/…` policy: frame token for `<elem>` itself → own-API access (always
  allowed); frame token for another element → treated as element→element, grants
  consulted; cookie with **no** frame token → owner (only from non-element pages:
  xbind's own UI; element-served pages always carry a token, and `Sec-Fetch-Dest`/
  `Referer` are used to flag anomalies).
- Cookie is `HttpOnly` + `SameSite=Lax` (CSRF), `Secure` behind https proxy.
- This is attribution, not isolation: a malicious element's JS still executes
  same-origin (can read its parent's DOM, etc.). Real browser isolation =
  subdomain-per-scope, phase 5. Documented, not hidden.

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
- Browser-plane isolation beyond attribution (until subdomains).
