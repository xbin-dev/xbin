package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

// OpenAPI describes xbind's built-in API surface (/api/xbin/*) as an
// OpenAPI 3.1 document, including the RBAC capability each endpoint needs
// (docs/auth.md, docs/protocol.md). Served at GET /api/xbin/openapi.json and
// rendered by the API-docs tile; also importable into Swagger UI / Postman.

type oapi = map[string]any

// ep is one endpoint's spec metadata; the parenthetical after each capability is
// surfaced as an x-xbin-capability extension and in the description.
type ep struct {
	method, path string
	tag          string
	summary      string
	capability   string // RBAC requirement (see capabilities below)
	desc         string
	params       []oapi
	body         oapi
	resp         string // 200 response description
}

const apiInfo = `The **built-in xbind API** — the reserved ` + "`/api/xbin/*`" + ` surface that
` + "`bx`" + `, the SDKs, and tiles are built on (docs/protocol.md). Component
backends live under ` + "`/api/<component-path>/…`" + ` and are not described here.

## Surfaces

Operations are grouped by tag; the main areas:

- **Identity & components** — caller identity, visible components, component detail.
- **Grants & interfaces** — the RBAC grant table plus typed interface bindings
  (net / http / archive). Binding is owner-only and *is* the authorization.
- **Resources** — filesystem / kv / blob / bus / cron state (docs/resources.md).
- **Lifecycle & backup** — enable / disable / offload a component, and archive or
  restore it through a bound archiver (backups are self-describing).
- **Runtime** — admin visibility into backends, namespaces, and egress.
- **Terminals & code** — terminal sessions and per-component git.

## Authentication (docs/auth.md)

Every route needs a **principal**, established by one of:

- **Owner cookie** ` + "`xbin_session`" + ` (browser login) → the *owner*.
- **Bearer token** ` + "`Authorization: Bearer <token>`" + ` → the owner, the
  element an *instance token* belongs to (backends, over the gateway unix
  socket), or the tile a *terminal token* belongs to (shells — per-session,
  tile-scoped).
- **Frame token** ` + "`X-XBin-Frame-Token`" + ` → an element *frontend*.
  Standalone: no cookie required (sandboxed tile frames hold nothing else),
  and a cookie-bearing request showing the tile fingerprint
  (` + "`Sec-Fetch-Site: cross-site`" + ` on a non-navigation) has the cookie
  dropped before resolution — tiles cannot ride the human's session.

xbind strips inbound ` + "`X-XBin-*`" + ` identity headers and re-injects verified
` + "`X-XBin-From` / `X-XBin-Role`" + ` on proxied component calls.

## Capabilities (the ` + "`x-xbin-capability`" + ` on each operation)

- **authenticated** — any valid principal.
- **owner** — the human owner (or an admin user).
- **admin** — the reserved ` + "`xbin:admin`" + ` capability (owner implies it).
- **xbin:writer** — workspace-management grant (create components, import tiles).
- **xbin:users** — user-management grant.
- **self or admin** — the element itself, or admin (e.g. its own vault).
- **admin or code[:<component>]** — admin, the component itself, or a caller
  granted read-only source access: ` + "`code:<component>`" + ` (that one
  component) or ` + "`code`" + ` (every component — tooling/scanners).
- **reader / writer (resource grant)** — a role grant on the named ` + "`res:…`" + `
  resource (docs/resources.md).

The owner can never be self-approved by an element: cross-scope grants are
owner-approved in the grants table.`

func endpoints() []ep {
	return []ep{
		// --- info / introspection ---
		{"GET", "/whoami", "Identity", "Caller identity + permissions", "authenticated",
			"Returns the resolved principal and what it may do — how a tile discovers whether it's the owner, an element, its granted roles, etc.", nil, nil, "identity object"},
		{"GET", "/openapi.json", "Identity", "This API description", "authenticated",
			"The OpenAPI 3.1 document for the built-in API (this document).", nil, nil, "OpenAPI document"},
		{"GET", "/components", "Components", "List components", "authenticated",
			"Every component the caller may see (a user sees only tiles they may use; admins see all), with runtime, exposed roles, declared uses, deps, manifest errors, the chrome flag (trusted chrome runs unsandboxed — bx-frame reads this) and `sandbox`: the extra iframe/CSP sandbox tokens the tile's grants unlock (cap:open-links → allow-popups allow-popups-to-escape-sandbox, ND11; absent when none).", nil, nil, "array of component summaries"},
		{"GET", "/components/{path}", "Components", "Component detail + API.md", "authenticated",
			"One component's metadata plus its API.md (the docs standard).", []oapi{pathParam("path", "component path, e.g. apps/calendar")}, nil, "{component, apiDoc}"},
		{"POST", "/auth-rotate-token", "Identity", "Rotate the owner token", "admin",
			"Rewrites .xbin/token; the old token dies immediately (bearer and cookie). The new token is returned once.", nil, nil, "{token}"},
		{"POST", "/account/password", "Identity", "Change your own password", "signed-in user",
			"Self-service rotation: the current password is verified first (D38).", nil, jsonBody("passwords", oapi{"current": str(""), "new": str("")}, "current", "new"), "ok"},
		{"GET", "/frame-token", "Identity", "Mint a frame token", "authenticated",
			"Issues a short-lived per-(user×component) frame token so an element frontend can attribute its calls (xbin-client.js uses this). Humans: any tile they may read; a tile frontend: its OWN component only — cookie-less renewal included (sandboxed frames hold no other credential).", []oapi{queryParam("component", "the component the token is for", true)}, nil, "{token}"},
		{"GET", "/status", "Runtime", "Terminals + component counts + host/traffic gauges", "admin", "host = cpu jiffies, memory, workspace disk; traffic = cumulative request/byte counters (clients delta two polls for rates). Powers the shell's status footer.", nil, nil, "{components, terminals, host, traffic}"},
		{"GET", "/gpus", "Runtime", "Host NVIDIA GPUs (for gpu:* grants / terminal picker)", "admin", "", nil, nil, "{gpus:[{index,uuid,name,node}]}"},
		{"GET", "/backends", "Runtime", "Per-component backend state", "admin",
			"Compact backend states: idle | building | healthy | failed, with generation and last error.", nil, nil, "{path: {state, gen, error?}}"},
		{"GET", "/logs", "Runtime", "A tile backend's captured stdout/stderr", "self or terminal-level",
			"text/plain tail of the component's captured backend logs (all generations). Gate: admin, the tile itself, or a user with terminal-level access on it. ?tail=<bytes> (default 64K, max 1M); ?follow=1 streams appended bytes (chunked) until disconnect. The HTTP twin of `bx logs -f`; the terminal window's read-only logs tab.",
			[]oapi{queryParam("component", "component path", true), queryParam("tail", "tail size in bytes (default 65536, max 1048576)", false), queryParam("follow", "1 to stream new lines", false)}, nil, "text/plain log tail"},
		{"GET", "/runtime", "Runtime", "Full runtime visibility", "admin",
			"Host + per-backend process, namespaces, cgroup usage, and network/egress activity, plus resource sizes — powers the admin console's runtime tab (docs/isolation.md).", nil, nil, "{host, backends[], resources[]}"},
		{"GET", "/tile-status", "Runtime", "One tile's runtime metrics", "self or admin",
			"backend {state,gen,cpuSec,cgroup:{mem,pids},rssKb,fds,activeConns,egress}, disk {usage,quota,blocked}, alerts[], and net {netRef,net,netRules,netSource,netNote} — the effective network (D54). Readable from that tile's terminal (tile-scoped token); `bx status` renders it. (Distinct from /tile-report, the status a tile reports about itself.)",
			[]oapi{queryParam("component", "component path", true)}, nil, "{backend, disk, alerts, net}"},
		{"GET", "/alerts", "Runtime", "Workspace health alerts", "authenticated",
			"Disk quota / low disk / cgroup at-limit conditions: system alerts go to everyone, tile alerts to admins and that tile's users.", nil, nil, "{alerts:[{level,kind,tile?,message,system}]}"},
		{"GET", "/auth-overview", "Runtime", "Admin overview aggregate", "admin",
			"Components (with roles/uses/vault presence), the grant table, pending grants, and counts — one call powering the admin overview tab.", nil, nil, "overview object"},
		{"GET", "/resources", "Resources", "Declared resources", "admin",
			"Every resource declared across the workspace and scopes: {id, scope, name, type}.", nil, nil, "array of resources"},
		{"GET", "/vaults", "Vault", "All vaults (key names)", "admin",
			"Key names for every component vault (values via the per-key endpoint). 503 when the barrier is sealed.", nil, nil, "[{component, keys}]"},

		// --- prefs ---
		{"GET", "/prefs", "Prefs", "The caller's prefs bucket", "authenticated",
			"Per-(user×tile) preferences. Each principal reads/writes only its own bucket; the shell stores its layout here.", nil, nil, "prefs object"},
		{"GET", "/prefs/{key}", "Prefs", "Read one pref", "authenticated", "", []oapi{pathParam("key", "pref key")}, nil, "arbitrary JSON value | 404"},
		{"PUT", "/prefs/{key}", "Prefs", "Set one pref", "authenticated", "Body is the arbitrary JSON value to store.", []oapi{pathParam("key", "pref key")}, freeBody("the JSON value to store"), "ok"},
		{"DELETE", "/prefs/{key}", "Prefs", "Delete one pref", "authenticated", "", []oapi{pathParam("key", "pref key")}, nil, "ok"},

		// --- shared screens & sidebar folders (D37/D55) ---
		{"GET", "/screens", "Screens", "Shared layouts visible to the caller", "authenticated",
			"The ws-admin default screen (everyone), the caller's orgs' screens (each with rev/updatedBy/updatedAt and canEdit per the screen's edit knob), and the folder sets they may see: `ws` always, plus `org:<id>` for every org they belong to (ws-admins: all).",
			nil, nil, "{default: {tiles}|null, org: [{id,org,name,edit,tiles,rev,updatedBy,updatedAt,canEdit}], folders: {scope: {folders,rev,updatedBy,updatedAt,canEdit}}}"},
		{"PUT", "/screens/default", "Screens", "Set the workspace default screen", "admin",
			"The seed screen every new user starts from (root/index.html's <bx-frame> pins stay the fallback).",
			nil, jsonBody("layout", oapi{"tiles": arr()}, "tiles"), "ok"},
		{"PUT", "/screens/org", "Screens", "Create or update an org screen", "org admin / per edit knob",
			"Create (no id; org admin or ws-admin) returns the new id at rev 1. A tiles update is gated by the screen's edit knob (admins|write|members) and carries `rev`, the revision it was based on: a stale one is refused with 409 {error, rev, screen} unless force:true; a body without rev is the legacy overwrite. A body without tiles is a meta-only edit (name/edit — org admins), which never bumps the revision.",
			nil, jsonBody("screen", oapi{"id": str("omit to create"), "org": str(""), "name": str(""), "edit": str("admins|write|members"), "tiles": arr(), "rev": oapi{"type": "integer"}, "force": boolean()}, "org"), "{ok, id, rev, updatedBy, updatedAt} | 409 {error, rev, screen}"},
		{"DELETE", "/screens/org", "Screens", "Delete an org screen", "org admin", "",
			nil, jsonBody("screen ref", oapi{"id": str(""), "org": str("")}, "id", "org"), "ok"},
		{"PUT", "/screens/folders", "Screens", "Replace one owner section's curated sidebar tree", "admin (ws) / org admin (org:<id>)",
			"The shared folder tree members see under that owner section. `scope` is `ws` (workspace-owned tiles; ws-admin) or `org:<id>` (that org's admins, ws-admins too). Carries the revision it was based on (0 for a scope's first save); stale → 409 {error, rev, folders} unless force:true. `folders: []` clears the scope.",
			nil, jsonBody("folder set", oapi{"scope": str("ws | org:<id>"), "folders": arr(), "rev": oapi{"type": "integer"}, "force": boolean()}, "scope", "folders", "rev"), "{ok, rev, updatedBy, updatedAt} | 409 {error, rev, folders}"},

		// --- users ---
		{"GET", "/users", "Users", "List users", "xbin:users",
			"Human users and their per-tile permissions, plus sign-in facts (lastLogin, lastLoginVia, ssoGroups seen at the last SSO sign-in, ssoSyncError) and roleVia (\"sso\" when the admin role came from a group rule). Admin or the xbin:users grant.", nil, nil, "{users:[{id,name,email,role,roleVia,tiles,canCreate,termApi,termNet,disabled,invitePending,lastLogin,lastLoginVia,ssoGroups,ssoSyncError}]}"},
		{"POST", "/users", "Users", "Create a user", "xbin:users",
			"With password → ready to sign in; without → credential-less + a single-use invite link (D22); sso:true (needs email) → credential-less with NO invite: the bound email signs in through the IdP (pre-provisioning, D52). Every new account is seeded with the workspace's new-account defaults (GET /defaults newUsers) on top of the given fields; orgs joins the account to orgs at creation (validated first — no half-created account). Under SSO-only mode a non-admin needs sso:true or a password.", nil,
			jsonBody("new user", oapi{"id": str(""), "name": str(""), "role": str("admin|user"), "email": str("SSO binding"), "tiles": oapi{"type": "object", "description": "path/pattern → read|write|terminal"}, "canCreate": arr(), "termApi": boolean(), "termNet": boolean(), "password": str(""), "sso": boolean(), "orgs": arr()}, "id"), "{user, orgs?, invite?, inviteUrl?, inviteLink?, inviteExpires?}"},
		{"PATCH", "/users/{id}", "Users", "Update a user", "xbin:users", "Update fields and/or reset the password. Disabling drops the user's sessions.", []oapi{pathParam("id", "user id")}, freeBody("fields to update"), "updated user"},
		{"DELETE", "/users/{id}", "Users", "Delete a user", "xbin:users", "Removes the user and revokes their sessions.", []oapi{pathParam("id", "user id")}, nil, "ok"},
		{"POST", "/users/{id}/invite", "Users", "(Re)mint an invite link", "xbin:users, or an org admin for a non-admin member of their org",
			"Credential delivery or reset-by-link (D38): re-minting invalidates the previous link; the current password keeps working until redemption. 409 for a non-admin under SSO-only mode (D53).", []oapi{pathParam("id", "user id")}, nil, "{invite, inviteUrl, inviteLink, inviteExpires}"},
		{"DELETE", "/users/{id}/sessions", "Users", "Sign a user out everywhere", "xbin:users",
			"Ends every browser session and terminal token of the user (D53); they can sign in again — disable the account to stop that.", []oapi{pathParam("id", "user id")}, nil, "{ok, dropped}"},
		{"GET", "/sessions", "Users", "Live browser sessions", "xbin:users",
			"Live login sessions with client IPs (login IP + last-seen IP) and activity times, newest activity first. current:true marks the caller's own session for cookie-authenticated calls (tile-driven calls carry a frame token, no cookie, so no row is current there). Session ids are credentials and are never returned. Bootstrap owner-token logins are stateless and do not appear. This is the attribution view for the /c/ warm-IP gate: tile subresource loads without credentials are served only from an IP that authenticated within the last hour.",
			nil, nil, "{sessions:[{user,name,created,lastActive,ip,lastIP,current}]}"},
		{"GET", "/auth-settings", "Users", "Get auth settings", "xbin:users",
			"Sign-in policy: owner-token browser-login state (canDisable reports whether THIS caller may disable it), SSO-only mode (passwordLoginDisabled; canDisablePassword = SSO ready), and the SSO configuration (docs/auth.md §SSO) — client secret reduced to clientSecretSet; ready = configured AND --external-url set; groupSync reports whether group rules are active, every group the IdP has been seen sending (knownGroups), and the newest recorded fetch failure.", nil, nil,
			"{tokenLoginDisabled,hasAdminUser,canDisable,passwordLoginDisabled,canDisablePassword,sso:{enabled,ready,kind,preset,issuer,clientId,clientSecretSet,allowedDomains,buttonLabel,externalUrl,groupsClaim,groupsScope,adminGroups,groupSync:{rulesActive,knownGroups,lastError}}}"},
		{"PATCH", "/auth-settings", "Users", "Update auth settings", "xbin:users",
			"Enable/disable owner-token browser login (/login?token= + owner cookie; disabling needs a signed-in admin-user caller; Bearer unaffected); set the SSO config: an sso object replaces it (empty clientSecret keeps the stored one), sso:null clears it; and/or toggle SSO-only mode (passwordLoginDisabled: non-admins sign in through the IdP only; enabling needs a ready provider).",
			nil, freeBody("{tokenLoginDisabled?:bool, sso?:{kind,preset,issuer,clientId,clientSecret,allowedDomains,buttonLabel,groupsClaim,groupsScope,adminGroups}|null, passwordLoginDisabled?:bool}"), "{tokenLoginDisabled,passwordLoginDisabled,sso}"},
		{"POST", "/auth-settings/sso/test", "Users", "Test the SSO provider", "xbin:users",
			"Probes the provider without a user: OIDC discovery + a JWKS fetch, or GitHub API reachability. Tests the stored config, or an unsaved draft passed as {sso:{…}} (empty clientSecret = the stored one). Always 200 — the body is a report, ok:false included.", nil,
			freeBody("{} | {sso:{kind,preset,issuer,clientId,…}}"), "{ok,kind,issuer,redirectUri,externalUrl,ready,endpoints,jwksKeys,warnings,error}"},

		// --- orgs & teams (docs/auth.md) ---
		{"GET", "/orgs", "Orgs", "List orgs (management view)", "xbin:users",
			"Admin/xbin:users: all orgs; a signed-in org admin: their orgs; others: []. A member's own view is whoami's `orgs`. netSets/resolvedNet/netHost describe the org's network reach (D54).", nil, nil, "{orgs:[{id,name,members,tiles,sets,allow,policy,ssoGroups,resolvedAllow,ownedTiles,netSets,resolvedNet,netHost}]}"},
		{"POST", "/orgs", "Orgs", "Create an org", "xbin:users", "Workspace-level operation; org ids are immutable ([a-z0-9._-]; o/u/workspace reserved). The org owns the o/<id> path marker.", nil,
			jsonBody("new org", oapi{"id": str(""), "name": str(""), "admins": arr(), "members": arr(), "basePermission": str("read|write|\"\"")}, "id"), "created org"},
		{"PATCH", "/orgs/{org}", "Orgs", "Update an org", "xbin:users",
			"Admin/xbin:users or that org's admin. Present fields overlay; members replaces the whole list (membership provenance via/viaGroups is store-owned: kept from the previous row, ignored in the body). Workspace-admin-only fields: sets, allow, netSets (attaching network sets restarts the org's net-declaring tiles, D54).", []oapi{pathParam("org", "org id")}, freeBody("fields to update"), "updated org"},
		{"DELETE", "/orgs/{org}", "Orgs", "Delete an org", "xbin:users", "Workspace-level operation.", []oapi{pathParam("org", "org id")}, nil, "ok"},
		{"PUT", "/orgs/{org}/members/{user}", "Orgs", "Upsert one membership", "xbin:users",
			"Admin/xbin:users or that org's admin (D53). Present fields overlay; a new row starts at read. via:\"\" detaches a synced row (manual from then on); any other via is refused — provenance is written by SSO sync only.",
			[]oapi{pathParam("org", "org id"), pathParam("user", "user id")}, freeBody("{level?, create?, admin?, suspended?, via?:\"\"}"), "updated org"},
		{"DELETE", "/orgs/{org}/members/{user}", "Orgs", "Remove one membership", "xbin:users",
			"Admin/xbin:users or that org's admin. A synced row returns at the user's next sign-in while its rule stands (the response's note says so).",
			[]oapi{pathParam("org", "org id"), pathParam("user", "user id")}, nil, "{org, removed, note?} | 404"},
		{"GET", "/term-net", "Orgs", "Terminal network scopes for a tile", "terminal on the tile",
			"Which network scopes a terminal on ?tile= may take for the caller (D54): the org scope where the owning org has network sets, internet/host where allowed, offline. The picker offers exactly this list; the session frame repeats it.",
			[]oapi{queryParam("tile", "component path", true)}, nil, "{tile, scopes:[{id,label,desc}], default, label, org}"},
		{"GET", "/net-sets", "Orgs", "Organisation network sets", "xbin:users",
			"Named reach rules attached to orgs by reference (D54): internet | internet:<host|host-glob|ip|cidr>[:port] | lan:<ip|cidr>[:port] | host | provider:<tile-glob>. An org's union is the ceiling on its tiles' net bindings, what its admins may bind without an allowance, the default binding (`org`) of its net-declaring tiles, and the egress of terminals opened on them.", nil, nil, "{sets:{name:{rules,created}}, attachedTo:{name:[orgIds]}}"},
		{"PUT", "/net-sets/{name}", "Orgs", "Create/replace a network set", "xbin:users",
			"400 on grammar (one destination per rule; globs only in internet: host rules; lan: rules are addresses/CIDRs; internet: addresses must be public). Attached orgs' net-declaring tiles restart.", []oapi{pathParam("name", "set name")},
			jsonBody("rules", oapi{"rules": arr()}, "rules"), "{name, rules, created, attachedTo}"},
		{"DELETE", "/net-sets/{name}", "Orgs", "Delete a network set", "xbin:users", "409 while any org has it attached.", []oapi{pathParam("name", "set name")}, nil, "ok"},
		{"PUT", "/orgs/{org}/sso-groups", "Orgs", "Replace the org's IdP-group rules", "xbin:users",
			"Workspace-admin only (rules grant power). Each rule maps a provider group (OIDC claim value, GitHub org/team-slug, Google group email) to a membership shape; matching rules union. Applied at each member's next SSO sign-in (docs/auth.md §Group sync).",
			[]oapi{pathParam("org", "org id")}, jsonBody("rules", oapi{"rules": arr()}, "rules"), "updated org"},
		{"GET", "/permission-sets", "Orgs", "Permission sets", "xbin:users",
			"Named allowance bundles attached to orgs by reference (D28): each set carries allow entries, policy rows and the terminal flags; attachedTo lists the orgs holding each.", nil, nil, "{sets:{name:{allow,policy,termApi,termNet}}, attachedTo:{name:[orgIds]}}"},
		{"PUT", "/permission-sets/{name}", "Orgs", "Create or replace a permission set", "xbin:users",
			"Same allow grammar and floor as an org's own allow list.", []oapi{pathParam("name", "set name")}, freeBody("{allow?:[…], policy?:[…], termApi?:bool, termNet?:bool}"), "the stored set"},
		{"DELETE", "/permission-sets/{name}", "Orgs", "Delete a permission set", "xbin:users", "409 while any org has it attached.", []oapi{pathParam("name", "set name")}, nil, "ok"},
		{"GET", "/owner", "Orgs", "A tile's owner", "any principal that may read the tile", "", []oapi{queryParam("tile", "component path", true)}, nil, "{tile, owner: \"user:<id>\" | \"org:<id>\" | \"\"}"},
		{"GET", "/owner/preview", "Orgs", "Transfer impact report", "as POST /owner",
			"What a transfer to `to` would do (D39; never mutates): whether it is allowed, the caller's level before and after, the bindings and grants that die under the new owner's ceilings, and the plane changes.", []oapi{queryParam("tile", "component path", true), queryParam("to", "user:<id> | org:<id> | \"\" (workspace)", true)}, nil, "{allowed, callerLevel:{before,after}, deadBindings, deadGrants, planeChanges}"},
		{"POST", "/owner", "Orgs", "Transfer a tile's ownership", "ws-admin, the user-owner, or an owning-org admin",
			"GIVE needs one of those; RECEIVE into org:X needs X's Create knob (transfer into ≡ create in); user:<self> with the GIVE right; user:<other> and the workspace are ws-admin acts (D24/D39). Ceiling-dead binding slots are unbound, affected backends restart, and the executed report (+unbound) is returned.", nil,
			jsonBody("transfer", oapi{"tile": str("apps/x"), "to": str("user:<id> | org:<id> | \"\"")}, "tile", "to"), "the executed impact report"},
		{"GET", "/access-requests", "Orgs", "Pending access requests", "signed-in user",
			"Human access requests (D36) scoped to the viewer: mine = you filed it; manage = you may approve it (the tile's owner, its org's admins, or ws-admin).", nil, nil, "{requests:[{user,tile,level,note?,created,mine?,manage?}]}"},
		{"POST", "/access-requests", "Orgs", "Request access to a tile", "signed-in user",
			"At most 20 pending each; a same-tile refile replaces; refused for levels you already hold, tiles you are explicitly excluded from, and re-files within 24h of a manager's dismissal.", nil,
			jsonBody("request", oapi{"tile": str(""), "level": str("read|write|terminal"), "note": str("")}, "tile", "level"), "ok"},
		{"POST", "/access-requests/approve", "Orgs", "Approve an access request", "the tile's manager set",
			"Writes the exact ACL entry (level defaults to the requested one) and removes the request.", nil,
			jsonBody("approval", oapi{"user": str(""), "tile": str(""), "level": str("optional; defaults to the requested level")}, "user", "tile"), "ok"},
		{"DELETE", "/access-requests", "Orgs", "Withdraw or dismiss a request", "the requester, or the tile's manager set",
			"Withdrawing your own has no cooldown; a manager dismissing someone else's starts the 24h re-file cooldown.", nil,
			jsonBody("request", oapi{"user": str("optional; defaults to the caller"), "tile": str("")}, "tile"), "ok"},
		{"GET", "/access", "Orgs", "A tile's resolved access list", "xbin:users",
			"Admin/xbin:users or the tile's org admin. Every user/team/base entry covering the tile, with provenance (exact | pattern:<pat> | base).",
			[]oapi{queryParam("tile", "component path", true)}, nil, "{tile, org?, orgAdmins?, entries:[{kind,id,level,source}]}"},
		{"PUT", "/access", "Orgs", "Set/clear one exact access entry", "xbin:users",
			"Admin/xbin:users or the tile's org admin. level \"\" clears. Team entries only on the team's own org's tiles.", nil,
			jsonBody("entry", oapi{"tile": str(""), "kind": str("user|team"), "id": str(""), "level": str("read|write|terminal|\"\"")}, "tile", "kind", "id"), "ok"},
		{"GET", "/access-matrix", "Orgs", "Resolved users×tiles access matrix", "xbin:users",
			"Every user's effective level on every tile with full provenance (admin | org-admin:<org> | direct:<pattern> | team:<org>/<team>:<pattern> | base:<org>). Cells exist only where a level resolves; chrome and templates are excluded. Powers the admin tile's access map.", nil, nil, "{users:[{id,name,role}], tiles:[…], cells:{user:{tile:{level,via}}}}"},
		{"GET", "/users-directory", "Orgs", "Minimal people list for pickers", "xbin:users",
			"Identity only ({id,name}); reachable by org admins too so they can add existing accounts to their org/teams.", nil, nil, "{users:[{id,name}]}"},
		{"GET", "/policy", "Orgs", "Workspace policy-ceiling rows", "xbin:users",
			"Pattern-keyed ceilings on what tiles may be granted (deny: net|gpu|xbin-caps; mayCall allow-lists call targets). Enforced at approval AND evaluation.", nil, nil, "{policy:[{tiles,deny,mayCall}]}"},
		{"PUT", "/policy", "Orgs", "Replace workspace policy rows", "xbin:users", "", nil,
			jsonBody("rows", oapi{"policy": arr()}, "policy"), "ok"},
		{"GET", "/orgs/{org}/policy", "Orgs", "An org's policy rows", "xbin:users", "", []oapi{pathParam("org", "org id")}, nil, "{policy:[…]}"},
		{"PUT", "/orgs/{org}/policy", "Orgs", "Replace an org's policy rows", "xbin:users", "", []oapi{pathParam("org", "org id")},
			jsonBody("rows", oapi{"policy": arr()}, "policy"), "ok"},
		{"GET", "/defaults", "Orgs", "Workspace provisioning defaults", "xbin:users",
			"defaultTiles: the visibility baseline every user gets (D27). newUsers: what every NEW account starts with — tiles, create patterns, terminal flags, org memberships — seeded at creation (admin-added, invited, SSO auto-provisioned; D52). tileCreation: any | org-only (non-admins may only create org-owned tiles).",
			nil, nil, "{defaultTiles:{pattern:level}, newUsers:{tiles,canCreate,termApi,termNet,orgs:[{org,level,create}]}, tileCreation}"},
		{"PUT", "/defaults", "Orgs", "Replace provisioning defaults", "xbin:users",
			"Each present key replaces that setting wholesale; absent keys are left alone. Default orgs must exist; newUsers never grants admin.", nil,
			freeBody("{defaultTiles?:{pattern:level}, newUsers?:{tiles,canCreate,termApi,termNet,orgs:[{org,level,create}]}, tileCreation?:\"any\"|\"org-only\"}"), "the resulting defaults"},

		// --- workspace management ---
		{"POST", "/create", "Workspace", "Create a component", "xbin:writer",
			"Scaffolds a new component (same as `bx new`); never overwrites. Owner, or an element granted xbin:writer. owner: org:<id> (needs the org's Create knob) | user:<id> | \"\" = creator-owned, workspace-owned for admins; under the org-only tile-creation policy a non-admin's empty owner resolves to their single Create org.", nil,
			jsonBody("component to create", oapi{"path": str("apps/thing"), "runtime": str("static|go|node|python|cgi"), "title": str(""), "expose": boolean(), "owner": str("org:<id> | user:<id> | \"\"")}, "path"), "{path, files, owner?}"},
		{"POST", "/clone", "Workspace", "Clone (fork) a component", "xbin:writer",
			"Copies an existing component (git history included) and rewrites references to the old path across its files. Secrets and resource data are not copied; cross-scope uses re-enter owner approval. Rejects a copy whose uses don't resolve. owner as /create.", nil,
			jsonBody("what to clone", oapi{"from": str("apps/thing"), "to": str("apps/thing-fork"), "owner": str("org:<id> | user:<id> | \"\"")}, "from", "to"), "{path, from, rewritten, pendingGrants}"},
		{"GET", "/builtins", "Tiles", "Builtin tile catalog", "authenticated", "Optional tiles bundled in the binary; `installed` marks ones already at their default path.", nil, nil, "[{name,title,description,defaultPath,installed}]"},
		{"POST", "/builtins/import", "Tiles", "Import a builtin tile", "xbin:writer", "Copies an embedded tile into the workspace (docs/overview/14-lifecycle.md §Getting code in); returns any grants it now needs.", nil,
			jsonBody("tile to import", oapi{"name": str("llm-gw"), "path": str("optional install path"), "owner": str("as /create")}, "name"), "{path, files, pendingGrants}"},
		{"GET", "/builtins/updates", "Tiles", "Available builtin updates", "authenticated", "Scaffold + imported tiles that have a newer embedded version (docs/overview/14-lifecycle.md §Keeping code fresh).", nil, nil, "array of updatable builtins"},
		{"POST", "/builtins/update", "Tiles", "Apply a builtin update", "xbin:writer", "replace overwrites; merge 3-way-merges (git merge-file); pin/unpin stop/resume offers. Never touches template instances.", nil,
			jsonBody("update", oapi{"id": str("scaffold:shell"), "mode": str("replace|merge|pin|unpin")}, "id", "mode"), "{files}"},
		{"GET", "/templates", "Tiles", "Template blueprints", "authenticated", "Builtin ∪ workspace template components (docs/overview/03-components.md §Templates).", nil, nil, "[{id,source,title,description,defaultName}]"},
		{"POST", "/templates/new", "Tiles", "Instantiate a template", "xbin:writer", "Copies a blueprint into a named component, stripping the template marker.", nil,
			jsonBody("instantiation", oapi{"source": str("agent | apps/mytpl"), "path": str("optional target path"), "owner": str("as /create")}, "source"), "{path, files, pendingGrants}"},
		{"GET", "/templates/updates", "Tiles", "Template instances with unmerged upstream", "authenticated",
			"Instances whose builtin template gained snapshots they have not merged, filtered to tiles the caller can read. legacy marks a pre-seeding instance whose first merge needs --allow-unrelated-histories (D50).", nil, nil, "{instances:[{path,template,head,legacy}]}"},
		{"GET", "/templates/{repo}/{path}", "Tiles", "A builtin template's git repo (dumb HTTP)", "authenticated",
			"Read-only git server for a builtin template's source, e.g. /templates/agent.git/info/refs; every instance has it as its `template` remote (git fetch template && git merge template/main).", []oapi{pathParam("repo", "<template>.git"), pathParam("path", "git dumb-HTTP path")}, nil, "git protocol bytes"},

		// --- code & git ---
		{"GET", "/code/tree", "Code", "A component's files", "admin or code[:<component>]", "Admin, the component itself, or a caller granted code:<component> (that one) or code (any).", []oapi{queryParam("component", "component path", true)}, nil, "{component, files:[{path,size}]}"},
		{"GET", "/code/file", "Code", "One file's content", "admin or code[:<component>]", "Binary/oversized files are flagged, not dumped. Needs code:<component> or code (any).", []oapi{queryParam("component", "component path", true), queryParam("file", "path within the component", true)}, nil, "{path, content|binary|truncated}"},
		{"GET", "/git/log", "Code", "Component git history", "admin or code[:<component>]", "Commits from the component's OWN git repo, each with churn (add/del/files). Needs code:<component> or code (any).", []oapi{queryParam("component", "component path", true), queryParam("limit", "max commits", false)}, nil, "{repo, commits:[{hash,short,author,date,subject,add,del,files}], remote}"},
		{"GET", "/git/activity", "Code", "Commit activity over time", "admin or code[:<component>]", "Author-date timeline for the component's history and, if tracked, its upstream branch — for activity charts. Needs code:<component> or code (any).", []oapi{queryParam("component", "component path", true)}, nil, "{repo, remote, upstreamRef, local:[{t,a}], upstream:[{t,a}]|null}"},
		{"GET", "/git/remote-info", "Tiles", "Inspect a git remote before install", "xbin:writer", "git ls-remote on a URL: its default branch + tags (newest first), so the UI can offer versions.", []oapi{queryParam("url", "git URL (https/ssh/git)", true)}, nil, "{defaultBranch, tags[], remote}"},
		{"POST", "/git/import", "Tiles", "Install a component from a git remote", "xbin:writer", "Clones a component in (each component is its own repo). Optional ref = tag/branch; path defaults to apps/<repo>. Rejects non-git/local URLs and non-xbin repos.", nil,
			jsonBody("git install", oapi{"url": str("https://github.com/user/tile"), "path": str("optional; apps/<repo> by default"), "ref": str("optional tag/branch"), "owner": str("as /create")}, "url"), "{path, remote, ref, pendingGrants}"},
		{"GET", "/git/diff", "Code", "Commit diff / uncommitted changes", "admin or code[:<component>]", "rev empty = uncommitted vs HEAD; else that commit's diff. Needs code:<component> or code (any).", []oapi{queryParam("component", "component path", true), queryParam("rev", "commit hash (empty = working tree)", false)}, nil, "{repo, diff}"},

		// --- cross-tile proposals ("code PRs", D48) ---
		{"POST", "/code/prs", "Code", "Open a cross-tile proposal", "admin or code[:<component>]",
			"The target's read gate: what you may read you may propose to (D48). series is a git format-patch mbox (≤4 MiB, must contain a diff), stored verbatim; base is the target rev it was formatted against.", nil,
			jsonBody("proposal", oapi{"target": str("apps/x"), "title": str(""), "message": str(""), "base": str("target rev the series was formatted against"), "series": str("format-patch mbox")}, "target", "title", "series"), "PR meta {number, target, from:{component,user,via}, title, state, created, …}"},
		{"GET", "/code/prs", "Code", "List proposals", "admin or code[:<component>]",
			"?target=<path>[&state=…] lists a target's PRs (read gate); ?from=1 lists the caller's own outgoing PRs; admins with neither get everything.", []oapi{queryParam("target", "component path", false), queryParam("state", "open|merged|rejected|withdrawn", false), queryParam("from", "1 = my outgoing PRs", false)}, nil, "{target?, prs:[…]}"},
		{"GET", "/code/prs/summary", "Code", "Open-PR counts per target", "authenticated", "Filtered to targets the caller can read (the shell's ⇄ badges).", nil, nil, "{counts:{path:n}}"},
		{"GET", "/code/pr", "Code", "One proposal with its review thread", "admin or code[:<component>]", "", []oapi{queryParam("target", "component path", true), queryParam("n", "PR number", true)}, nil, "PR meta + events"},
		{"GET", "/code/pr/series", "Code", "A proposal's patch series", "admin or code[:<component>]", "The raw mbox, text/plain, byte-exact — review first, then pipe into `git am --3way`.", []oapi{queryParam("target", "component path", true), queryParam("n", "PR number", true)}, nil, "text/plain mbox"},
		{"POST", "/code/pr/comment", "Code", "Comment on a proposal", "admin or code[:<component>]", "Appends to the review thread (author ↔ maintainer).", nil,
			jsonBody("comment", oapi{"target": str(""), "n": oapi{"type": "integer"}, "body": str("")}, "target", "n", "body"), "ok"},
		{"POST", "/code/pr/state", "Code", "Close a proposal", "target side (merged/rejected) or the author (withdrawn)",
			"merged and rejected are the target's call (its own principals, write-level users, admins); withdrawn is the author's. Only open PRs close; no reopen — refile instead; 409 on a double close.", nil,
			jsonBody("state change", oapi{"target": str(""), "n": oapi{"type": "integer"}, "state": str("merged|rejected|withdrawn"), "comment": str("")}, "target", "n", "state"), "ok"},

		// --- grants ---
		{"GET", "/grants", "Grants", "Grant table + pending", "admin", "", nil, nil, "{grants:[{from,target,role}], pending:[…]}"},
		{"POST", "/grants", "Grants", "Approve / add a grant", "admin", "Approves a pending request or adds a grant. Targets are component paths, res:… resources, gpu:… devices, or reserved capabilities (cap:net-admin, cap:containers, cap:open-links — the last widens the tile's frontend sandbox so links open in new tabs, ND11). (Network egress is not a grant — it's a `net` interface binding; see /bindings.)", nil,
			jsonBody("grant", oapi{"from": str("apps/x"), "target": str("apps/y | res:… | gpu:0 | cap:open-links"), "role": str("reader|writer|admin|egress|…")}, "from", "target", "role"), "ok"},
		{"DELETE", "/grants", "Grants", "Revoke a grant", "admin", "", nil,
			jsonBody("grant to revoke", oapi{"from": str(""), "target": str(""), "role": str("")}, "from", "target", "role"), "ok"},
		{"GET", "/bindings", "Grants", "Interface requests/providers + bindings", "admin", "Typed capability wiring (docs/overview/11-interfaces.md). `pending` lists unbound interface slots with the providers that can satisfy each — the bind-on-install prompt; default:\"org\" marks a net slot already satisfied by the owning org's network sets (D54). `inert` lists net bindings stored but resolving to no egress, with the reason. Binding values are a ref string, or an array of refs for multi:true slots; refs are provider[#instance]. `instances` maps each instances-provider to its registered {id: pathPrefix}. `approvable` names the components whose wiring THIS caller may change (ws admin: all; org admin: their orgs' tiles); pending rows carry the same flag, and options POST would refuse for everyone (a net ref outside the owning org's network sets) are blocked:true.", nil, nil, "{bindings, instances, components:[{component,interfaces,provides}], pending:[{component,slot,kind,service,multi?,default?,approvable,options:[{id,label,blocked?}]}], inert:{comp:{slot:reason}}, approvable:{comp:true}}"},
		{"POST", "/bindings", "Grants", "Bind a component's interface slot to provider(s)", "admin", "provider = one ref; providers = the full ordered set for a multi:true http slot (replaces). Refs are provider[#instance]; an instances-provide must be bound to a specific instance. Net builtins: internet | internet:<spec> | host | lan:<cidr> | org (the owning org's network sets, org-owned tiles only) | none; on an org-owned tile with network sets the ref must be inside the sets (D54).", nil,
			jsonBody("binding", oapi{"component": str("apps/x"), "slot": str("net"), "provider": str("apps/firewall | internet | org | none | apps/imap#abc"), "providers": arr()}, "component", "slot"), "ok"},
		{"PUT", "/iface-instances", "Grants", "Register a provider's interface instances", "self or admin",
			"A provider whose provide declares {instances:true} registers its concrete instances (runtime config — accounts, profiles): {id: pathPrefix}. Prefixes are PROVIDER-RELATIVE (\"/m/1\") — xbind composes /api/<provider>+path for consumers; workspace-absolute \"/api/…\" registrations are rejected 400. Replaces the whole map; elements may only set their own; bound requesters are re-wired. Instances bind as provider#id.", nil,
			jsonBody("instances", oapi{"component": str("admin only; elements are self-scoped"), "instances": oapi{"type": "object"}}, "instances"), "{component, instances}"},
		{"DELETE", "/bindings", "Grants", "Clear a binding", "admin", "", nil,
			jsonBody("binding to clear", oapi{"component": str("apps/x"), "slot": str("net")}, "component", "slot"), "ok"},

		// --- ingress (docs/ingress.md) ---
		{"PUT", "/ingress-hosts", "Ingress", "Register a delegated zone's concrete hostnames", "self or admin",
			"A tile with a delegated-zone http expose binding registers the hostnames it serves (docs/ingress.md); replaces the set, [] clears. Every host must sit inside one of the tile's own bound zones (403 outside — the authority boundary), be a valid bare hostname, and not collide with an exact-bound host or another tile's registration (409). Routes update live; no restart.", nil,
			jsonBody("hosts", oapi{"component": str("admin only; elements are self-scoped"), "hosts": arr()}, "hosts"), "ok"},
		{"GET", "/ingress-routes", "Ingress", "Concrete host→tile routes", "terminator tiles + admin",
			"A tile with provides {kind:\"ingress\"} sees the routes bound through it (its proxy/ACME config input); admins see all; others 403.", nil, nil, "{routes:[{host,component,slot,paths,source,zone?}]}"},
		{"GET", "/ingress", "Ingress", "The whole ingress picture", "admin",
			"Every exposes slot with its binding and policy state, live routes, registered hosts, terminators, stream listener health (streams[].error/active), forwards, and the builtin HTTP listener.", nil, nil, "{exposes:[…], routes, ingressHosts, terminators, streams:[…], forwards, httpListener:{listen,tls}}"},
		{"POST", "/lifecycle", "Grants", "Set a component's lifecycle state", "admin", "Enable/disable/offload a component (docs/overview/14-lifecycle.md). A non-enabled backend won't spawn; disabling stops it now; offloaded[-full] archives + frees local bytes; enabling an offloaded component restores it. Needs an @archive binding for offload.", nil,
			jsonBody("lifecycle", oapi{"component": str("apps/x"), "state": str("enabled|disabled|offloaded|offloaded-full")}, "component", "state"), "{ok, state}"},
		{"POST", "/backup", "Backup", "Back up a component now", "admin", "Streams a self-describing tar (source + scope data + terminal env) of the component to its bound @archive provider (docs/overview/14-lifecycle.md).", nil,
			jsonBody("backup", oapi{"component": str("apps/x")}, "component"), "{ok, version}"},
		{"GET", "/backups", "Backup", "List a component's archived versions", "admin", "Passes the bound archiver's version list through: [{version, time, size}].", []oapi{queryParam("component", "the component", true)}, nil, "{versions:[{version,time,size}], archiver}"},
		{"POST", "/restore", "Backup", "Restore a version or a single file", "admin", "Restore a whole version (stops + replaces the component's data/source from the archive) or, with `file`, stream one member back without touching live state (docs/overview/14-lifecycle.md).", nil,
			jsonBody("restore", oapi{"component": str("apps/x"), "version": str("optional; default latest"), "file": str("optional; one path within the archive")}, "component"), "{ok, component, restored} or the file bytes"},
		{"GET", "/backup-schedule", "Backup", "List scheduled backups", "admin", "", nil, nil, "{schedules:[{component,schedule,retention}]}"},
		{"POST", "/backup-schedule", "Backup", "Schedule (or reschedule) backups for a component", "admin", "Owner-scheduled backup on the cron engine (docs/overview/14-lifecycle.md). retention prunes to N newest versions after each run (0 = keep all).", nil,
			jsonBody("schedule", oapi{"component": str("apps/x"), "schedule": str("0 3 * * * | @every 24h"), "retention": str("N (int)")}, "component", "schedule"), "ok"},
		{"DELETE", "/backup-schedule", "Backup", "Remove a component's backup schedule", "admin", "", []oapi{queryParam("component", "the component", true)}, nil, "ok"},

		// --- vault ---
		{"GET", "/vault-status", "Vault", "Barrier status", "admin", "mode: unsealed | sealed | unconfigured | plaintext.", nil, nil, "{initialized, sealed, mode, insecure}"},
		{"POST", "/vault-unseal", "Vault", "Unseal / initialize", "admin", "Unseals with a passphrase, or initializes the barrier on first use (created:true).", nil, jsonBody("passphrase", oapi{"passphrase": str("")}, "passphrase"), "{created}"},
		{"POST", "/vault-seal", "Vault", "Seal", "admin", "Drops the key from memory; vault get/set then 503 until unsealed.", nil, nil, "ok"},
		{"POST", "/vault-rekey", "Vault", "Change the passphrase", "admin", "Re-wraps the data key under a new passphrase (no data re-encryption). Requires the barrier unsealed and the current passphrase.", nil, jsonBody("passphrases", oapi{"current": str(""), "new": str("")}, "new"), "{rekeyed}"},
		{"GET", "/vault/{component}", "Vault", "Vault key names", "self or admin", "", []oapi{pathParam("component", "component path")}, nil, "{keys:[…]}"},
		{"GET", "/vault/{component}/{key}", "Vault", "Read a secret", "self or admin", "", []oapi{pathParam("component", "component path"), pathParam("key", "secret name")}, nil, "{value}"},
		{"PUT", "/vault/{component}/{key}", "Vault", "Write a secret", "self or admin", "", []oapi{pathParam("component", "component path"), pathParam("key", "secret name")}, jsonBody("secret", oapi{"value": str("")}, "value"), "ok"},
		{"DELETE", "/vault/{component}/{key}", "Vault", "Delete a secret", "self or admin", "", []oapi{pathParam("component", "component path"), pathParam("key", "secret name")}, nil, "ok"},

		// --- resources: kv / blob / bus / cron ---
		{"GET", "/kv/{resource}/{key}", "Resources", "KV read / list", "reader (resource grant)",
			"With an empty key (trailing slash) lists keys (optional ?prefix=); otherwise returns the raw value bytes. resource is res:<scope>/<name>.", []oapi{pathParam("resource", "res:<scope>/<name>"), pathParam("key", "key (empty = list)"), queryParam("prefix", "key prefix (list mode)", false)}, nil, "{keys} | raw bytes"},
		{"PUT", "/kv/{resource}/{key}", "Resources", "KV write", "writer (resource grant)", "Body is the value (≤ 1 MiB).", []oapi{pathParam("resource", "res:<scope>/<name>"), pathParam("key", "key")}, freeBody("value bytes"), "ok"},
		{"DELETE", "/kv/{resource}/{key}", "Resources", "KV delete", "writer (resource grant)", "", []oapi{pathParam("resource", "res:<scope>/<name>"), pathParam("key", "key")}, nil, "ok"},
		{"GET", "/blob/{resource}/{path}", "Resources", "Blob read / list", "reader (resource grant)", "File bytes, or {entries} for a directory path.", []oapi{pathParam("resource", "res:<scope>/<name>"), pathParam("path", "path within the blob store")}, nil, "file bytes | {entries}"},
		{"PUT", "/blob/{resource}/{path}", "Resources", "Blob write", "writer (resource grant)", "Body is the file content (≤ 256 MiB).", []oapi{pathParam("resource", "res:<scope>/<name>"), pathParam("path", "path")}, freeBody("file content"), "ok"},
		{"DELETE", "/blob/{resource}/{path}", "Resources", "Blob delete", "writer (resource grant)", "", []oapi{pathParam("resource", "res:<scope>/<name>"), pathParam("path", "path")}, nil, "ok"},
		{"GET", "/tile-report", "Resources", "Component status snapshot", "any signed-in user", "Status a component reported about itself, read-filtered to what you can see — {statuses:{<component>:{level,message,ts}}}. Powers the shell's sidebar/tab health indicators. (Distinct from /tile-status, which is xbind-observed runtime metrics.)", nil, nil, "{statuses}"},
		{"POST", "/tile-report", "Resources", "Report component status / notify", "element (self) or owner", "A component reports its own condition (level ok|info|warn|error; ok+empty message clears it). transient=true fires a one-shot notification instead of setting status. Publishes a `status` event. See workspace AGENTS.md.", nil,
			jsonBody("status report", oapi{"level": str("ok|info|warn|error"), "message": str("short human text"), "transient": str("optional; one-shot toast")}, "level"), "{ok}"},
		{"POST", "/bus/publish", "Resources", "Publish to a bus", "writer (resource grant)", "Delivers to owner + reader-granted elements over /ws/events.", nil,
			jsonBody("message", oapi{"resource": str("res:<scope>/<name>"), "topic": str(""), "data": freeSchema("any JSON")}, "resource", "topic"), "ok"},
		{"GET", "/cron/jobs", "Resources", "List cron jobs", "authenticated", "Own jobs; admin sees all.", nil, nil, "{jobs}"},
		{"PUT", "/cron/jobs", "Resources", "Register a cron job", "writer (resource grant)", "Registers a schedule that calls back into a component. `component` is owner-only; elements always schedule themselves.", nil,
			jsonBody("job", oapi{"name": str(""), "resource": str("res:<scope>/<name>"), "schedule": str("@every 1m | 5-field cron"), "path": str("/tick"), "role": str("optional"), "component": str("owner-only")}, "name", "resource", "schedule", "path"), "ok"},
		{"DELETE", "/cron/jobs/{name}", "Resources", "Delete a cron job", "authenticated", "Element: own jobs; admin: any (via ?component=).", []oapi{pathParam("name", "job name"), queryParam("component", "owner-only: whose job", false)}, nil, "ok"},
	}
}

// OpenAPI builds the OpenAPI 3.1 document.
func OpenAPI() oapi {
	paths := oapi{}
	tagSet := map[string]bool{}
	for _, e := range endpoints() {
		tagSet[e.tag] = true
		item, ok := paths[e.path].(oapi)
		if !ok {
			item = oapi{}
			paths[e.path] = item
		}
		item[strings.ToLower(e.method)] = operation(e)
	}
	tags := make([]oapi, 0, len(tagSet))
	for _, t := range sortedKeys(tagSet) {
		tags = append(tags, oapi{"name": t})
	}
	return oapi{
		"openapi": "3.1.0",
		"info": oapi{
			"title":       "xbin built-in API",
			"version":     "1",
			"description": apiInfo,
		},
		"servers": []oapi{{"url": "/api/xbin", "description": "xbind reserved API"}},
		"tags":    tags,
		"paths":   paths,
		"components": oapi{
			"securitySchemes": oapi{
				"bearerAuth": oapi{"type": "http", "scheme": "bearer", "description": "Owner or element instance token."},
				"cookieAuth": oapi{"type": "apiKey", "in": "cookie", "name": "xbin_session", "description": "Browser owner session."},
				"frameToken": oapi{"type": "apiKey", "in": "header", "name": "X-XBin-Frame-Token", "description": "Element frontend (standalone — no cookie required)."},
			},
			"schemas": oapi{
				"Error": oapi{"type": "object", "properties": oapi{
					"error":  str("human-readable message"),
					"docs":   str("link to the relevant docs"),
					"detail": str("extra detail (e.g. compiler output)"),
				}},
			},
		},
		// Any one of the schemes authenticates; the x-xbin-capability on each
		// operation is the finer RBAC requirement.
		"security": []oapi{{"bearerAuth": []any{}}, {"cookieAuth": []any{}}, {"frameToken": []any{}}},
	}
}

func operation(e ep) oapi {
	desc := e.desc
	cap := "**Requires:** " + e.capability + "."
	if desc == "" {
		desc = cap
	} else {
		desc = cap + "\n\n" + desc
	}
	op := oapi{
		"tags":              []string{e.tag},
		"summary":           e.summary,
		"description":       desc,
		"operationId":       operationID(e),
		"x-xbin-capability": e.capability,
		"responses": oapi{
			"200": oapi{"description": orDefault(e.resp, "success")},
			"400": errResp("bad request"),
			"403": errResp("insufficient capability (see x-xbin-capability)"),
			"404": errResp("not found"),
		},
	}
	if len(e.params) > 0 {
		op["parameters"] = e.params
	}
	if e.body != nil {
		op["requestBody"] = e.body
	}
	return op
}

// --- handler ---

func (s *Server) apiOpenAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(OpenAPI())
}

// --- small builders ---

func operationID(e ep) string {
	s := strings.ToLower(e.method) + e.path
	s = strings.NewReplacer("/", "_", "{", "", "}", "", ".", "_", ":", "").Replace(s)
	return strings.Trim(s, "_")
}

func str(desc string) oapi     { return oapi{"type": "string", "description": desc} }
func boolean() oapi            { return oapi{"type": "boolean"} }
func arr() oapi                { return oapi{"type": "array", "items": oapi{"type": "string"}} }
func freeSchema(d string) oapi { return oapi{"description": d} }

func pathParam(name, desc string) oapi {
	return oapi{"name": name, "in": "path", "required": true, "schema": oapi{"type": "string"}, "description": desc}
}
func queryParam(name, desc string, required bool) oapi {
	return oapi{"name": name, "in": "query", "required": required, "schema": oapi{"type": "string"}, "description": desc}
}

func jsonBody(desc string, props oapi, required ...string) oapi {
	schema := oapi{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return oapi{"required": true, "content": oapi{"application/json": oapi{"schema": schema}}, "description": desc}
}
func freeBody(desc string) oapi {
	return oapi{"content": oapi{"*/*": oapi{"schema": oapi{"type": "string", "format": "binary"}}}, "description": desc}
}

func errResp(desc string) oapi {
	return oapi{"description": desc, "content": oapi{"application/json": oapi{"schema": oapi{"$ref": "#/components/schemas/Error"}}}}
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
