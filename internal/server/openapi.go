package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

// OpenAPI describes buxond's built-in API surface (/api/buxon/*) as an
// OpenAPI 3.1 document, including the RBAC capability each endpoint needs
// (docs/auth.md, docs/protocol.md). Served at GET /api/buxon/openapi.json and
// rendered by the API-docs tile; also importable into Swagger UI / Postman.

type oapi = map[string]any

// ep is one endpoint's spec metadata; the parenthetical after each capability is
// surfaced as an x-buxon-capability extension and in the description.
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

const apiInfo = `The **built-in buxond API** — the reserved ` + "`/api/buxon/*`" + ` surface that
` + "`bx`" + `, the SDKs, and tiles are built on (docs/protocol.md). Component
backends live under ` + "`/api/<component-path>/…`" + ` and are not described here.

## Authentication (docs/auth.md)

Every route needs a **principal**, established by one of:

- **Owner cookie** ` + "`buxon_session`" + ` (browser login) → the *owner*.
- **Bearer token** ` + "`Authorization: Bearer <token>`" + ` → the owner, or the
  element an *instance token* belongs to (backends use this over the gateway
  unix socket).
- **Frame token** ` + "`X-Buxon-Frame-Token`" + ` (with the owner cookie) → an
  element *frontend*.

buxond strips inbound ` + "`X-Buxon-*`" + ` identity headers and re-injects verified
` + "`X-Buxon-From` / `X-Buxon-Role`" + ` on proxied component calls.

## Capabilities (the ` + "`x-buxon-capability`" + ` on each operation)

- **authenticated** — any valid principal.
- **owner** — the human owner (or an admin user).
- **admin** — the reserved ` + "`buxon:admin`" + ` capability (owner implies it).
- **buxon:writer** — workspace-management grant (create components, import tiles).
- **buxon:users** — user-management grant.
- **self or admin** — the element itself, or admin (e.g. its own vault).
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
			"Every component the caller may see (a user sees only tiles they may use; admins see all), with runtime, exposed roles, declared uses, deps, and manifest errors.", nil, nil, "array of component summaries"},
		{"GET", "/components/{path}", "Components", "Component detail + API.md", "authenticated",
			"One component's metadata plus its API.md (the docs standard).", []oapi{pathParam("path", "component path, e.g. apps/calendar")}, nil, "{component, apiDoc}"},
		{"GET", "/frame-token", "Identity", "Mint a frame token", "authenticated",
			"Issues a short-lived per-(user×component) frame token so an element frontend can attribute its calls (buxon-client.js uses this).", []oapi{queryParam("component", "the component the token is for", true)}, nil, "{token}"},
		{"GET", "/status", "Runtime", "Terminals + component counts", "admin", "", nil, nil, "status summary"},
		{"GET", "/gpus", "Runtime", "Host NVIDIA GPUs (for gpu:* grants / terminal picker)", "admin", "", nil, nil, "{gpus:[{index,uuid,name,node}]}"},
		{"GET", "/backends", "Runtime", "Per-component backend state", "admin",
			"Compact backend states: idle | building | healthy | failed, with generation and last error.", nil, nil, "{path: {state, gen, error?}}"},
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
		{"GET", "/users", "Users", "List users", "buxon:users",
			"Human users and their per-tile permissions. Admin or the buxon:users grant.", nil, nil, "[{id,name,role,tiles,terminal}]"},
		{"POST", "/users", "Users", "Create a user", "buxon:users", "", nil,
			jsonBody("new user", oapi{"id": str(""), "name": str(""), "role": str("admin|user"), "tiles": arr(), "terminal": boolean(), "password": str("")}, "id"), "created user"},
		{"PATCH", "/users/{id}", "Users", "Update a user", "buxon:users", "Update fields and/or reset the password.", []oapi{pathParam("id", "user id")}, freeBody("fields to update"), "updated user"},
		{"DELETE", "/users/{id}", "Users", "Delete a user", "buxon:users", "Removes the user and revokes their sessions.", []oapi{pathParam("id", "user id")}, nil, "ok"},

		// --- workspace management ---
		{"POST", "/create", "Workspace", "Create a component", "buxon:writer",
			"Scaffolds a new component (same as `bx new`); never overwrites. Owner, or an element granted buxon:writer.", nil,
			jsonBody("component to create", oapi{"path": str("apps/thing"), "runtime": str("static|go|node|python|cgi"), "title": str(""), "expose": boolean()}, "path"), "{path, files}"},
		{"GET", "/builtins", "Tiles", "Builtin tile catalog", "authenticated", "Optional tiles bundled in the binary; `installed` marks ones already at their default path.", nil, nil, "[{name,title,description,defaultPath,installed}]"},
		{"POST", "/builtins/import", "Tiles", "Import a builtin tile", "buxon:writer", "Copies an embedded tile into the workspace (plans/tile-sharing.md); returns any grants it now needs.", nil,
			jsonBody("tile to import", oapi{"name": str("llm-gw"), "path": str("optional install path")}, "name"), "{path, files, pendingGrants}"},
		{"GET", "/builtins/updates", "Tiles", "Available builtin updates", "authenticated", "Scaffold + imported tiles that have a newer embedded version (plans/builtin-updates.md).", nil, nil, "array of updatable builtins"},
		{"POST", "/builtins/update", "Tiles", "Apply a builtin update", "buxon:writer", "replace overwrites; merge 3-way-merges (git merge-file); pin/unpin stop/resume offers. Never touches template instances.", nil,
			jsonBody("update", oapi{"id": str("scaffold:shell"), "mode": str("replace|merge|pin|unpin")}, "id", "mode"), "{files}"},
		{"GET", "/templates", "Tiles", "Template blueprints", "authenticated", "Builtin ∪ workspace template components (plans/templates.md).", nil, nil, "[{id,source,title,description,defaultName}]"},
		{"POST", "/templates/new", "Tiles", "Instantiate a template", "buxon:writer", "Copies a blueprint into a named component, stripping the template marker.", nil,
			jsonBody("instantiation", oapi{"source": str("agent | apps/mytpl"), "path": str("optional target path")}, "source"), "{path, files, pendingGrants}"},

		// --- code & git ---
		{"GET", "/code/tree", "Code", "A component's files", "admin", "", []oapi{queryParam("component", "component path", true)}, nil, "{component, files:[{path,size}]}"},
		{"GET", "/code/file", "Code", "One file's content", "admin", "Binary/oversized files are flagged, not dumped.", []oapi{queryParam("component", "component path", true), queryParam("file", "path within the component", true)}, nil, "{path, content|binary|truncated}"},
		{"GET", "/git/log", "Code", "Component git history", "admin", "Commits touching the component, scoped to its path in the single workspace repo.", []oapi{queryParam("component", "component path", true), queryParam("limit", "max commits", false)}, nil, "{repo, commits[]}"},
		{"GET", "/git/diff", "Code", "Commit diff / uncommitted changes", "admin", "rev empty = uncommitted changes vs HEAD; else that commit's diff, scoped to the component.", []oapi{queryParam("component", "component path", true), queryParam("rev", "commit hash (empty = working tree)", false)}, nil, "{repo, diff}"},

		// --- grants ---
		{"GET", "/grants", "Grants", "Grant table + pending", "admin", "", nil, nil, "{grants:[{from,target,role}], pending:[…]}"},
		{"POST", "/grants", "Grants", "Approve / add a grant", "admin", "Approves a pending request or adds a grant. Targets are component paths, res:… resources, or gpu:… devices. (Network egress is not a grant — it's a `net` interface binding; see /bindings.)", nil,
			jsonBody("grant", oapi{"from": str("apps/x"), "target": str("apps/y | res:… | gpu:0"), "role": str("reader|writer|admin|egress|…")}, "from", "target", "role"), "ok"},
		{"DELETE", "/grants", "Grants", "Revoke a grant", "admin", "", nil,
			jsonBody("grant to revoke", oapi{"from": str(""), "target": str(""), "role": str("")}, "from", "target", "role"), "ok"},
		{"GET", "/bindings", "Grants", "Interface requests/providers + bindings", "admin", "Typed capability wiring (plans/interfaces.md). `pending` lists unbound interface slots with the providers that can satisfy each — the bind-on-install prompt.", nil, nil, "{bindings, components:[{component,interfaces,provides}], pending:[{component,slot,kind,service,options:[{id,label}]}]}"},
		{"POST", "/bindings", "Grants", "Bind a component's interface slot to a provider", "admin", "", nil,
			jsonBody("binding", oapi{"component": str("apps/x"), "slot": str("net"), "provider": str("apps/firewall | internet | host")}, "component", "slot", "provider"), "ok"},
		{"DELETE", "/bindings", "Grants", "Clear a binding", "admin", "", nil,
			jsonBody("binding to clear", oapi{"component": str("apps/x"), "slot": str("net")}, "component", "slot"), "ok"},
		{"POST", "/lifecycle", "Grants", "Set a component's lifecycle state", "admin", "Enable/disable a component (plans/lifecycle.md). A non-enabled backend won't spawn; disabling stops it now. offloaded states need an archiver (later).", nil,
			jsonBody("lifecycle", oapi{"component": str("apps/x"), "state": str("enabled|disabled")}, "component", "state"), "{ok, state}"},

		// --- vault ---
		{"GET", "/vault-status", "Vault", "Barrier status", "admin", "", nil, nil, "{initialized, sealed, insecure}"},
		{"POST", "/vault-unseal", "Vault", "Unseal / initialize", "admin", "Unseals with a passphrase, or initializes the barrier on first use.", nil, jsonBody("passphrase", oapi{"passphrase": str("")}, "passphrase"), "{created}"},
		{"POST", "/vault-seal", "Vault", "Seal", "admin", "Drops the key from memory; vault get/set then 503 until unsealed.", nil, nil, "ok"},
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
			"title":       "buxon built-in API",
			"version":     "1",
			"description": apiInfo,
		},
		"servers": []oapi{{"url": "/api/buxon", "description": "buxond reserved API"}},
		"tags":    tags,
		"paths":   paths,
		"components": oapi{
			"securitySchemes": oapi{
				"bearerAuth": oapi{"type": "http", "scheme": "bearer", "description": "Owner or element instance token."},
				"cookieAuth": oapi{"type": "apiKey", "in": "cookie", "name": "buxon_session", "description": "Browser owner session."},
				"frameToken": oapi{"type": "apiKey", "in": "header", "name": "X-Buxon-Frame-Token", "description": "Element frontend (with the owner cookie)."},
			},
			"schemas": oapi{
				"Error": oapi{"type": "object", "properties": oapi{
					"error":  str("human-readable message"),
					"docs":   str("link to the relevant docs"),
					"detail": str("extra detail (e.g. compiler output)"),
				}},
			},
		},
		// Any one of the schemes authenticates; the x-buxon-capability on each
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
		"tags":               []string{e.tag},
		"summary":            e.summary,
		"description":        desc,
		"operationId":        operationID(e),
		"x-buxon-capability": e.capability,
		"responses": oapi{
			"200": oapi{"description": orDefault(e.resp, "success")},
			"400": errResp("bad request"),
			"403": errResp("insufficient capability (see x-buxon-capability)"),
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
