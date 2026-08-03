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
			"Every component the caller may see (a user sees only tiles they may use; admins see all), with runtime, exposed roles, declared uses, deps, manifest errors, and the chrome flag (trusted chrome runs unsandboxed — bx-frame reads this).", nil, nil, "array of component summaries"},
		{"GET", "/components/{path}", "Components", "Component detail + API.md", "authenticated",
			"One component's metadata plus its API.md (the docs standard).", []oapi{pathParam("path", "component path, e.g. apps/calendar")}, nil, "{component, apiDoc}"},
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
			"Host + per-backend process, namespaces, cgroup usage, and network/egress activity, plus resource sizes — powers the admin console's runtime tab (plans/isolation.md).", nil, nil, "{host, backends[], resources[]}"},
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

		// --- users ---
		{"GET", "/users", "Users", "List users", "xbin:users",
			"Human users and their per-tile permissions. Admin or the xbin:users grant.", nil, nil, "[{id,name,role,tiles,terminal}]"},
		{"POST", "/users", "Users", "Create a user", "xbin:users", "", nil,
			jsonBody("new user", oapi{"id": str(""), "name": str(""), "role": str("admin|user"), "tiles": arr(), "terminal": boolean(), "password": str("")}, "id"), "created user"},
		{"PATCH", "/users/{id}", "Users", "Update a user", "xbin:users", "Update fields and/or reset the password.", []oapi{pathParam("id", "user id")}, freeBody("fields to update"), "updated user"},
		{"DELETE", "/users/{id}", "Users", "Delete a user", "xbin:users", "Removes the user and revokes their sessions.", []oapi{pathParam("id", "user id")}, nil, "ok"},
		{"GET", "/auth-settings", "Users", "Get auth settings", "xbin:users",
			"Owner-token browser-login state. canDisable reports whether THIS caller may disable it (an admin user exists and the caller is a signed-in admin user, directly or driving a tile).", nil, nil, "{tokenLoginDisabled,hasAdminUser,canDisable}"},
		{"PATCH", "/auth-settings", "Users", "Update auth settings", "xbin:users",
			"Enable/disable owner-token browser login (/login?token= + owner cookie). Disabling needs an admin user and a signed-in admin-user caller; the Bearer owner token is unaffected.",
			nil, jsonBody("settings", oapi{"tokenLoginDisabled": boolean()}, "tokenLoginDisabled"), "{tokenLoginDisabled}"},

		// --- orgs & teams (docs/auth.md) ---
		{"GET", "/orgs", "Orgs", "List orgs (management view)", "xbin:users",
			"Admin/xbin:users: all orgs; a signed-in org admin: their orgs; others: []. A member's own view is whoami's `orgs`.", nil, nil, "{orgs:[{id,name,admins,members,basePermission,policy,teams}]}"},
		{"POST", "/orgs", "Orgs", "Create an org", "xbin:users", "Workspace-level operation; org ids are immutable ([a-z0-9._-]; o/u/workspace reserved). The org owns the o/<id> path marker.", nil,
			jsonBody("new org", oapi{"id": str(""), "name": str(""), "admins": arr(), "members": arr(), "basePermission": str("read|write|\"\"")}, "id"), "created org"},
		{"PATCH", "/orgs/{org}", "Orgs", "Update an org", "xbin:users",
			"Admin/xbin:users or that org's admin. Present fields overlay; members removed here leave the org's teams too.", []oapi{pathParam("org", "org id")}, freeBody("fields to update"), "updated org"},
		{"DELETE", "/orgs/{org}", "Orgs", "Delete an org", "xbin:users", "Workspace-level operation.", []oapi{pathParam("org", "org id")}, nil, "ok"},
		{"POST", "/orgs/{org}/teams", "Orgs", "Create a team", "xbin:users",
			"Admin/xbin:users or the org's admin. Members must be org members; termApi/termNet need a workspace admin. Team tiles/canCreate patterns only ever apply inside the org.", []oapi{pathParam("org", "org id")},
			jsonBody("new team", oapi{"id": str(""), "name": str(""), "members": arr(), "tiles": oapi{"type": "object", "description": "path/pattern → read|write|terminal"}, "canCreate": arr(), "newTiles": str("read|write|terminal")}, "id"), "created team"},
		{"PATCH", "/orgs/{org}/teams/{team}", "Orgs", "Update a team", "xbin:users", "Present fields overlay (same rules as create).",
			[]oapi{pathParam("org", "org id"), pathParam("team", "team id")}, freeBody("fields to update"), "updated team"},
		{"DELETE", "/orgs/{org}/teams/{team}", "Orgs", "Delete a team", "xbin:users", "",
			[]oapi{pathParam("org", "org id"), pathParam("team", "team id")}, nil, "ok"},
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

		// --- workspace management ---
		{"POST", "/create", "Workspace", "Create a component", "xbin:writer",
			"Scaffolds a new component (same as `bx new`); never overwrites. Owner, or an element granted xbin:writer.", nil,
			jsonBody("component to create", oapi{"path": str("apps/thing"), "runtime": str("static|go|node|python|cgi"), "title": str(""), "expose": boolean()}, "path"), "{path, files}"},
		{"POST", "/clone", "Workspace", "Clone (fork) a component", "xbin:writer",
			"Copies an existing component (git history included) and rewrites references to the old path across its files. Secrets and resource data are not copied; cross-scope uses re-enter owner approval. Rejects a copy whose uses don't resolve.", nil,
			jsonBody("what to clone", oapi{"from": str("apps/thing"), "to": str("apps/thing-fork")}, "from", "to"), "{path, from, rewritten, pendingGrants}"},
		{"GET", "/builtins", "Tiles", "Builtin tile catalog", "authenticated", "Optional tiles bundled in the binary; `installed` marks ones already at their default path.", nil, nil, "[{name,title,description,defaultPath,installed}]"},
		{"POST", "/builtins/import", "Tiles", "Import a builtin tile", "xbin:writer", "Copies an embedded tile into the workspace (plans/tile-sharing.md); returns any grants it now needs.", nil,
			jsonBody("tile to import", oapi{"name": str("llm-gw"), "path": str("optional install path")}, "name"), "{path, files, pendingGrants}"},
		{"GET", "/builtins/updates", "Tiles", "Available builtin updates", "authenticated", "Scaffold + imported tiles that have a newer embedded version (plans/builtin-updates.md).", nil, nil, "array of updatable builtins"},
		{"POST", "/builtins/update", "Tiles", "Apply a builtin update", "xbin:writer", "replace overwrites; merge 3-way-merges (git merge-file); pin/unpin stop/resume offers. Never touches template instances.", nil,
			jsonBody("update", oapi{"id": str("scaffold:shell"), "mode": str("replace|merge|pin|unpin")}, "id", "mode"), "{files}"},
		{"GET", "/templates", "Tiles", "Template blueprints", "authenticated", "Builtin ∪ workspace template components (plans/templates.md).", nil, nil, "[{id,source,title,description,defaultName}]"},
		{"POST", "/templates/new", "Tiles", "Instantiate a template", "xbin:writer", "Copies a blueprint into a named component, stripping the template marker.", nil,
			jsonBody("instantiation", oapi{"source": str("agent | apps/mytpl"), "path": str("optional target path")}, "source"), "{path, files, pendingGrants}"},

		// --- code & git ---
		{"GET", "/code/tree", "Code", "A component's files", "admin or code[:<component>]", "Admin, the component itself, or a caller granted code:<component> (that one) or code (any).", []oapi{queryParam("component", "component path", true)}, nil, "{component, files:[{path,size}]}"},
		{"GET", "/code/file", "Code", "One file's content", "admin or code[:<component>]", "Binary/oversized files are flagged, not dumped. Needs code:<component> or code (any).", []oapi{queryParam("component", "component path", true), queryParam("file", "path within the component", true)}, nil, "{path, content|binary|truncated}"},
		{"GET", "/git/log", "Code", "Component git history", "admin or code[:<component>]", "Commits from the component's OWN git repo, each with churn (add/del/files). Needs code:<component> or code (any).", []oapi{queryParam("component", "component path", true), queryParam("limit", "max commits", false)}, nil, "{repo, commits:[{hash,short,author,date,subject,add,del,files}], remote}"},
		{"GET", "/git/activity", "Code", "Commit activity over time", "admin or code[:<component>]", "Author-date timeline for the component's history and, if tracked, its upstream branch — for activity charts. Needs code:<component> or code (any).", []oapi{queryParam("component", "component path", true)}, nil, "{repo, remote, upstreamRef, local:[{t,a}], upstream:[{t,a}]|null}"},
		{"GET", "/git/remote-info", "Tiles", "Inspect a git remote before install", "xbin:writer", "git ls-remote on a URL: its default branch + tags (newest first), so the UI can offer versions.", []oapi{queryParam("url", "git URL (https/ssh/git)", true)}, nil, "{defaultBranch, tags[], remote}"},
		{"POST", "/git/import", "Tiles", "Install a component from a git remote", "xbin:writer", "Clones a component in (each component is its own repo). Optional ref = tag/branch; path defaults to apps/<repo>. Rejects non-git/local URLs and non-xbin repos.", nil,
			jsonBody("git install", oapi{"url": str("https://github.com/user/tile"), "path": str("optional; apps/<repo> by default"), "ref": str("optional tag/branch")}, "url"), "{path, remote, ref, pendingGrants}"},
		{"GET", "/git/diff", "Code", "Commit diff / uncommitted changes", "admin or code[:<component>]", "rev empty = uncommitted vs HEAD; else that commit's diff. Needs code:<component> or code (any).", []oapi{queryParam("component", "component path", true), queryParam("rev", "commit hash (empty = working tree)", false)}, nil, "{repo, diff}"},

		// --- grants ---
		{"GET", "/grants", "Grants", "Grant table + pending", "admin", "", nil, nil, "{grants:[{from,target,role}], pending:[…]}"},
		{"POST", "/grants", "Grants", "Approve / add a grant", "admin", "Approves a pending request or adds a grant. Targets are component paths, res:… resources, or gpu:… devices. (Network egress is not a grant — it's a `net` interface binding; see /bindings.)", nil,
			jsonBody("grant", oapi{"from": str("apps/x"), "target": str("apps/y | res:… | gpu:0"), "role": str("reader|writer|admin|egress|…")}, "from", "target", "role"), "ok"},
		{"DELETE", "/grants", "Grants", "Revoke a grant", "admin", "", nil,
			jsonBody("grant to revoke", oapi{"from": str(""), "target": str(""), "role": str("")}, "from", "target", "role"), "ok"},
		{"GET", "/bindings", "Grants", "Interface requests/providers + bindings", "admin", "Typed capability wiring (plans/interfaces.md). `pending` lists unbound interface slots with the providers that can satisfy each — the bind-on-install prompt. Binding values are a ref string, or an array of refs for multi:true slots; refs are provider[#instance]. `instances` maps each instances-provider to its registered {id: pathPrefix}.", nil, nil, "{bindings, instances, components:[{component,interfaces,provides}], pending:[{component,slot,kind,service,multi?,options:[{id,label}]}]}"},
		{"POST", "/bindings", "Grants", "Bind a component's interface slot to provider(s)", "admin", "provider = one ref; providers = the full ordered set for a multi:true http slot (replaces). Refs are provider[#instance]; an instances-provide must be bound to a specific instance.", nil,
			jsonBody("binding", oapi{"component": str("apps/x"), "slot": str("net"), "provider": str("apps/firewall | internet | apps/imap#abc"), "providers": arr()}, "component", "slot"), "ok"},
		{"PUT", "/iface-instances", "Grants", "Register a provider's interface instances", "self or admin",
			"A provider whose provide declares {instances:true} registers its concrete instances (runtime config — accounts, profiles): {id: pathPrefix}. Prefixes are PROVIDER-RELATIVE (\"/m/1\") — xbind composes /api/<provider>+path for consumers; workspace-absolute \"/api/…\" registrations are rejected 400. Replaces the whole map; elements may only set their own; bound requesters are re-wired. Instances bind as provider#id.", nil,
			jsonBody("instances", oapi{"component": str("admin only; elements are self-scoped"), "instances": oapi{"type": "object"}}, "instances"), "{component, instances}"},
		{"DELETE", "/bindings", "Grants", "Clear a binding", "admin", "", nil,
			jsonBody("binding to clear", oapi{"component": str("apps/x"), "slot": str("net")}, "component", "slot"), "ok"},
		{"POST", "/lifecycle", "Grants", "Set a component's lifecycle state", "admin", "Enable/disable/offload a component (plans/lifecycle.md). A non-enabled backend won't spawn; disabling stops it now; offloaded[-full] archives + frees local bytes; enabling an offloaded component restores it. Needs an @archive binding for offload.", nil,
			jsonBody("lifecycle", oapi{"component": str("apps/x"), "state": str("enabled|disabled|offloaded|offloaded-full")}, "component", "state"), "{ok, state}"},
		{"POST", "/backup", "Backup", "Back up a component now", "admin", "Streams a self-describing tar (source + scope data + terminal env) of the component to its bound @archive provider (plans/lifecycle.md).", nil,
			jsonBody("backup", oapi{"component": str("apps/x")}, "component"), "{ok, version}"},
		{"GET", "/backups", "Backup", "List a component's archived versions", "admin", "Passes the bound archiver's version list through: [{version, time, size}].", []oapi{queryParam("component", "the component", true)}, nil, "{versions:[{version,time,size}], archiver}"},
		{"POST", "/restore", "Backup", "Restore a version or a single file", "admin", "Restore a whole version (stops + replaces the component's data/source from the archive) or, with `file`, stream one member back without touching live state (plans/lifecycle.md).", nil,
			jsonBody("restore", oapi{"component": str("apps/x"), "version": str("optional; default latest"), "file": str("optional; one path within the archive")}, "component"), "{ok, component, restored} or the file bytes"},
		{"GET", "/backup-schedule", "Backup", "List scheduled backups", "admin", "", nil, nil, "{schedules:[{component,schedule,retention}]}"},
		{"POST", "/backup-schedule", "Backup", "Schedule (or reschedule) backups for a component", "admin", "Owner-scheduled backup on the cron engine (plans/lifecycle.md). retention prunes to N newest versions after each run (0 = keep all).", nil,
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
