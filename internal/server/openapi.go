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
- **Deployments** — pausing live reload, a tile's deployments and promotion,
  their data and backups, and the checkpoint remote (git fetch).

A route marked **Reserved** (` + "`x-xbin-reserved`" + `) is declared for tile
deployments and not built in this xbind yet: it answers 501. A parameter or
field marked reserved is declared and not yet read or set; an xbind that
reads a ` + "`deployment`" + ` parameter echoes it in the answer.

## Authentication (docs/auth.md)

Every route needs a **principal**, established by one of:

- **Owner cookie** ` + "`xbin_session`" + ` (browser login) → the *owner*.
- **Bearer token** ` + "`Authorization: Bearer <token>`" + ` → the owner, the
  element an *instance token* belongs to (backends, over the gateway unix
  socket), the tile a *terminal token* belongs to (shells — per-session,
  tile-scoped), or — the native app's own code — the signed-in *user* of a
  device / app session (POST /login/device, POST /api/xbin/login).
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
- **` + capDeployRead + `** — a tile's deployment state; readers see only facts about the primary.
- **` + capDeployWrite + `** — the deploy log and diffs.
- **` + capDeployTerminal + `** — live reload, adding and removing deployments, a deployment's branch, deploy, promote, roll back, reset, run now.
- **` + capDeployManager + `** — the primary, protection, edge policies, deliveries, alwaysOn, limits, seeding and vault copies; terminal, agent and tile credentials never pass.
- **` + capDeployAdmin + `** — per-deployment backups and restores.
- **` + capDeployFetch + `** — the checkpoint remote.

The owner can never be self-approved by an element: cross-scope grants are
owner-approved in the grants table.`

// The capabilities of the tile-deployments routes (apiInfo lists them).
const (
	capDeployRead     = "read on the tile, or the tile itself (readers get the primary only)"
	capDeployWrite    = "write on the tile, or its terminal/agent sessions"
	capDeployTerminal = "terminal-level on the tile (its own terminal/agent tokens count)"
	capDeployManager  = "tile manager in a person's own session (the tile's owner, its org's admins, a workspace admin)"
	capDeployAdmin    = "admin in a person's own session"
	capDeployFetch    = "the tile's terminal/agent sessions, or write on the tile"
)

// reserved starts the description of a route declared for tile deployments
// before its operation is built: operation() adds its 501 answer and marks it
// x-xbin-reserved (NP-14-4). reservedField starts a note on the fields an
// answer gains, declared before anything sets them.
const (
	reserved      = "**Reserved** — not built in this xbind yet: answers 501. "
	reservedField = " Reserved (tile deployments): "
)

func endpoints() []ep {
	return append([]ep{
		// --- info / introspection ---
		{"GET", "/whoami", "Identity", "Caller identity + permissions", "authenticated",
			"Returns the resolved principal and what it may do — how a tile discovers whether it's the owner, an element, its granted roles, etc. An admin's view-as session (D64) adds impersonatedBy and readOnly:true. personalTiles says whether the caller (the human behind a tile call) may own tiles personally — the org-only policy and their account's switch folded in; a signed-in non-admin also gets personal {sets, netSets, netRules, allow}: their resolved personal plane (D88). Every caller gets native {runtime: 1}: this xbind serves native runtime documents (/c/<tile>/?native=1, docs/elements.md §Native app UI) — {runtime: 0, disabled: true} while an admin has turned native tile UIs off for the workspace (PUT /native-runtime). A tile credential bound to a deployment other than the primary also gets deployment, its name.", nil, nil, "identity object"},
		{"GET", "/openapi.json", "Identity", "This API description", "authenticated",
			"The OpenAPI 3.1 document for the built-in API (this document).", nil, nil, "OpenAPI document"},
		{"POST", "/impersonate", "Identity", "View the workspace as a user", "admin",
			"Mints a one-shot ticket (2 min) for the calling admin to see the workspace exactly as `user` sees it (docs/auth.md §Viewing the workspace as a user, D64). Open the returned url TOP-LEVEL in the same browser: it swaps the session cookie for a read-only session that authenticates as that user, marked impersonatedBy in /whoami and /sessions and refused on every write (403), terminals included. The ticket is bound to the minting admin — another browser can't redeem it. Not yourself, not a disabled account, and not while already viewing as someone.",
			nil, jsonBody("target", oapi{"user": str("user id")}, "user"), "{url: /login?impersonate=…, user, expiresIn}"},
		{"POST", "/impersonate/stop", "Identity", "Stop viewing as a user", "authenticated (a view-as session)",
			"Ends the read-only view and hands the browser back to the admin's own session (or the owner cookie for a bootstrap-token admin). restored:false means that session had expired meanwhile — sign in again. POST /logout from a view-as session does the same.", nil, nil, "{ok, restored}"},
		{"GET", "/components", "Components", "List components", "authenticated",
			"Every component the caller may see (a user sees only tiles they may use; admins see all), with runtime, exposed roles, declared uses, deps, manifest errors, the chrome flag (trusted chrome runs unsandboxed — bx-frame reads this: root, shell, the shipped tiles/organisations and tiles a workspace admin approved, D118), `chromeRequested` (the manifest says chrome: true but no admin approved it, so the tile runs sandboxed — PUT /chrome) and `sandbox`: the extra iframe/CSP sandbox tokens the tile's grants unlock (cap:open-links → allow-popups allow-popups-to-escape-sandbox, ND11; absent when none), `native` {entry}: the tile's native app UI module, tile-relative, whose runtime document is /c/<path>/?native=1 (absent when none and on chrome), and under --tile-assets=origins `origin`: the tile's own origin (bx-frame loads it there). The entry of a tile with deployments gains deployments {primary, pinned, protected} (pinned: its primary doesn't follow the work tree), the same for every caller who sees it; runtime, hasIndex, native, chrome and template describe the primary's code (its checkpoint while pinned), manifestError, roles, uses and deps the work tree. Deployments are never rows. A tile whose primary's code asks for a partition mode, or that has one recorded, gains partition {state: partitioned|unpartitioned|pending|invalid, user, global, request?: {user, global, declined}} and, for an invalid request, partitionError; both are absent for every other tile.", nil, nil, "array of component summaries"},
		{"GET", "/components/{path}", "Components", "Component detail + API.md", "authenticated",
			"One component's metadata plus its API.md (the docs standard). component carries the deployments summary /components gives.", []oapi{pathParam("path", "component path, e.g. apps/calendar")}, nil, "{component, apiDoc}"},
		{"POST", "/auth-rotate-token", "Identity", "Rotate the owner token", "admin",
			"Rewrites .xbin/token; the old token dies immediately (bearer and cookie), with the frames it opened and the push registrations devices made with it. The new token is returned once.", nil, nil, "{token}"},
		{"POST", "/account/password", "Identity", "Change your own password", "signed-in user",
			"Self-service rotation: the current password is verified first (D38). removeDevices:true also removes the caller's enrolled app devices — all but the one making the call — and ends their sessions (a new password alone doesn't sign a device out).", nil, jsonBody("passwords", oapi{"current": str(""), "new": str(""), "removeDevices": boolean()}, "current", "new"), "{ok, devicesRemoved?}"},
		{"GET", "/frame-token", "Identity", "Mint a frame token", "authenticated",
			"Issues a short-lived per-(user×component) frame token so an element frontend can attribute its calls (xbin-client.js uses this). Humans: any tile they may read; a tile frontend: its OWN component only — cookie-less renewal included (sandboxed frames hold no other credential); never a backend's instance token (403: it names no person, and the token would read as the owner's frame). The token is bound to the caller's credential generation — the login session that minted it (renewals copy it) — so logout, device revocation, sign-out-everywhere and disabling the user end it (docs/auth.md).", []oapi{queryParam("component", "the component the token is for (its path; a deployment rides deployment=, never component=tile+name: 400)", true), deploymentQuery("a person asks for a deployment's token (write on the tile for a non-primary one); a tile renews its own bound deployment's; the answer echoes deployment")}, nil, "{token}"},
		{"POST", "/path-tickets", "Identity", "Mint a path ticket (a tile page's link into a prefix of its own API)", "a tile's page (frame token)",
			"{path} → {url: \"/api/~<ticket>/\", expires (unix ms)}. /api/~<ticket>/<p> then reaches /api/<tile>/<path>/<p> — any method, WebSocket too — as the minting page's frame principal, and nothing else (D135): for a document the page frames from its backend in an opaque-origin sandbox, whose relative loads carry no token or cookie. Works only from an address that signed in within the hour, dies with the login, expires after 12 h; a <p> segment decoding to . or .. is 400. path: unreserved characters (A-Z a-z 0-9 . _ ~ -), no . or .. segment, ≤ 256 bytes (400). Anyone but a tile's page: 403.",
			nil, jsonBody("the prefix", oapi{"path": str("a relative path below the tile's API")}, "path"), "{url, expires}"},
		{"GET", "/tile-assets", "Components", "The tile asset report (strict tile asset gating)", "authenticated",
			"Per sandboxed tile the caller may read: the absolute /c/ references, inject:false and escaping symlinks that strict tile asset gating (--tile-assets=tokens|origins) refuses, each with file/line, kind, the referenced component, the modes it breaks under and the relative rewrite bx fix assets makes. Without ?component= only tiles with findings are listed. `mode` is the running mode.",
			[]oapi{queryParam("component", "one tile (listed even when clean)", false)}, nil,
			"{mode, tiles:[{component, injectFalse?, files, findings:[{file,line,col,kind,ref,target,breaks,fix,note}], truncated?, breaking:{tokens,origins}}]}"},
		{"GET", "/status", "Runtime", "Terminals + component counts + host/traffic gauges", "admin", "host = cpu jiffies, memory, workspace disk; traffic = cumulative request/byte counters (clients delta two polls for rates); terminals carry kind (shell | agent) and vm, and deployment when the session targets a named deployment. Powers the shell's status footer.", nil, nil, "{components, terminals, host, traffic}"},
		{"GET", "/gpus", "Runtime", "Host NVIDIA GPUs (for gpu:* grants / terminal picker)", "admin", "", nil, nil, "{gpus:[{index,uuid,name,node}]}"},
		{"GET", "/backends", "Runtime", "Per-component backend state", "admin",
			"Compact backend states: idle | building | healthy | failed, with generation and last error. The row stays the primary's; a tile with deployments adds deployment (the primary's name), and admins in their own session also get deployments {<name>: {state, gen, error?}} for the others.", nil, nil, "{path: {state, gen, error?}}"},
		{"GET", "/term/sessions", "Runtime", "The caller's live terminal sessions (the session directory, D73)", "authenticated",
			"Every browser the user signs into sees the same tabs: id, tile (cwd), effective net scope + label + pickable scopes, gpu/api pickers, tab name, created/lastActive, attached client count, envHeld. Oldest first. ?cwd= narrows to one tile. A user without terminal rights gets []; sessions on a tile the user may no longer open a terminal on are omitted. Admins may pass ?user= for another user's (what GET /status shows). Readable in a view-as session. A row whose session targets a named deployment carries deployment.",
			[]oapi{queryParam("cwd", "one tile's sessions only", false), queryParam("user", "admin: another user's sessions", false)}, nil, "[{id,cwd,net,label,scopes,gpu,api,name,created,lastActive,clients,envHeld}]"},
		{"PATCH", "/term/sessions/{id}", "Runtime", "Name a terminal tab", "creator or admin",
			"The name lives on the session, so it follows the user to every browser. Empty clears it (the tab shows its number). Refused in a view-as session.",
			[]oapi{pathParam("id", "session id")}, jsonBody("name", oapi{"name": str("")}, "name"), "ok"},
		{"GET", "/agent/providers", "Agent sessions", "The coding agents this daemon can run", "authenticated",
			"[{id, name, modes:[{id, name, explicit?}], defaultMode, login}]. `explicit` modes (bypass / full access) are never defaults and must be asked for by name. `login` is the shell command that signs the CLI in — the agent authenticates from the session's per-user $HOME, not a vault key (D74).", nil, nil, "array of providers"},
		{"POST", "/term/sessions", "Agent sessions", "Open an agent session on a tile", "terminal-level on the tile (a shell's own terminal token counts; an agent sandbox's token never: 403)",
			"{cwd, kind:\"agent\", provider, mode?, net?, api?, gpu?, name?, resume?} → the SessionInfo row (kind agent, status starting). net / api / gpu are the sandbox pickers a shell's socket takes (network scope, clamped like a terminal's; api false = a code-only sandbox with no terminal token; gpu none | all | <index>). The tile's sandbox runs the provider's ACP adapter instead of a shell (docs/overview/09-terminals.md §Agent sessions); the agent authenticates from the session's per-user $HOME (a login done in a shell terminal, no vault key), and the handshake completes in the background. `resume` names one of your past sessions on this tile (GET /agent/history) to reopen: the provider, mode and name carry over, the agent replays the earlier turns (session/load), then continues — 409 when that agent cannot reopen sessions (start a new one). 400 unknown provider/mode or a missing kind, 403 no terminal level, 409 the per-user session limit, 503 bx is missing. Shell sessions still open on /ws/term.",
			[]oapi{deploymentQuery("the session's target, fixed for its life: a deployment of the tile (the primary's name follows the primary; a protected primary 403; unknown 404; not a name 400); the SessionInfo echoes deployment — no echo means this xbind can't target deployments")}, jsonBody("agent session", oapi{"cwd": str("tile path"), "kind": str("\"agent\""), "provider": str("claude | codex | gemini | opencode"), "mode": str("a provider mode id (default: the provider's conservative one)"), "model": str("a model option value the agent offers (shorthand for options.model)"), "options": oapi{"type": "object", "description": "initial config options by id (model, effort, …), applied after the CLI loaded its settings"}, "net": str("network scope"), "api": oapi{"type": "boolean", "description": "tile-API access (default true; false = no terminal token)"}, "gpu": str("none | all | <index>"), "name": str("tab name"), "resume": str("a past session id (GET /agent/history) to reopen instead of starting fresh")}, "cwd", "kind"), "SessionInfo"},
		{"GET", "/term/sessions/{id}", "Agent sessions", "One session + what it waits on", "creator or admin",
			"{session: SessionInfo, permissions: [{pid, toolCall, options}], elicitations: [{eid, toolCallId?, message, schema}]}. Either kind; agent rows carry provider, mode, status (starting | idle | running | waiting_permission | cancelling | error | exited), pending (unanswered permission requests) and questions (unanswered elicitations). permissions and elicitations are the unanswered requests, oldest first, in their event payloads' shape — a client that (re)connects renders the cards without replaying the log.", []oapi{pathParam("id", "session id")}, nil, "{session, permissions, elicitations}"},
		{"DELETE", "/term/sessions/{id}", "Agent sessions", "End a session (either kind)", "creator or admin",
			"The API-side twin of DELETE /ws/term?session=. 204.", []oapi{pathParam("id", "session id")}, nil, "204"},
		{"POST", "/term/sessions/{id}/prompt", "Agent sessions", "Start a turn", "creator or admin; not an agent sandbox's token, the session's own or another's (403)",
			"{text, attachments?} → {ok, turn}. Waits for the handshake if the session is still starting. 409 while a turn runs or another prompt is being taken — refused before this body is read — (cancel it or wait for turn.end), and when a cancel lands while this prompt's files are handed over (no turn starts). attachments: [{name, mime?, data (standard base64)}] — at most 10, each ≤ 10 MiB, 20 MiB together (413 past a limit; the body is capped at 32 MiB); text may be empty when there are attachments. Every file is written inside the agent's sandbox (its private /tmp; with isolation off a directory xbind makes and removes with the session; the newest 64 MiB of a session are kept) and handed to the agent by path (an ACP resource_link); a png/jpeg/gif/webp — decided by the bytes — also goes inline as an image block (400 when the agent did not advertise promptCapabilities.image) when it is ≤ 3.75 MiB (5 MiB as base64, the strictest model API's limit) and the prompt's inline images stay ≤ 4 MiB together — past either it is a file only (the agent keeps inline images and resends them every turn: downscale photos). A text file ≤ 128 KiB goes inline as an embedded resource when the agent takes embedded context. The log's user message.delta lists {name, mime, size, inline?} per file, never the bytes. Not audited per call (the data plane).", []oapi{pathParam("id", "session id")},
			jsonBody("prompt", oapi{"text": str("the user's message"), "attachments": oapi{"type": "array", "description": "files: [{name, mime?, data}] — data standard base64", "items": oapi{"type": "object", "properties": oapi{"name": str("file name"), "mime": str("media type (optional: sniffed / from the name)"), "data": str("the bytes, standard base64")}}}}), "{ok, turn}"},
		{"POST", "/term/sessions/{id}/restart", "Agent sessions", "Restart an agent with other sandbox settings", "the session's creator; not an agent sandbox's token, the session's own or another's (403)",
			"{net?, api?, gpu?} → {session: SessionInfo, resumed}. The network scope, tile-API access and GPU are fixed when a sandbox starts, so changing one restarts the agent: the session ends and a new one opens on the same tile with the same provider, mode, settings (model, effort, …) and name. Where the agent can reopen its own session (loadSession — resume) the new one resumes it: the agent replays the earlier turns, then continues (resumed true; the history entry is superseded). Otherwise — or when the session never took a prompt — it starts fresh (resumed false) and the old transcript stays in GET /agent/history. 403 for anyone but the creator (the new session mounts the caller's $HOME).",
			[]oapi{pathParam("id", "session id"), deploymentQuery("restart onto this target (403 a protected primary, 404 unknown, 400 not a name; a refused target leaves the session running); the new SessionInfo echoes deployment")}, jsonBody("pickers", oapi{"net": str("network scope"), "api": oapi{"type": "boolean", "description": "tile-API access (default true)"}, "gpu": str("none | all | <index>")}), "{session, resumed}"},
		{"POST", "/term/sessions/{id}/cancel", "Agent sessions", "Interrupt the running turn", "creator or admin",
			"Pending permissions are answered cancelled, unfinished tool calls marked cancelled, the turn ends with stopReason cancelled. A prompt still handing its files to the agent is aborted instead (it answers 409; no turn starts). A no-op when idle.", []oapi{pathParam("id", "session id")}, nil, "ok"},
		{"POST", "/term/sessions/{id}/options", "Agent sessions", "Change a session setting (model, effort, …)", "creator or admin; not an agent sandbox's token, the session's own or another's (403)",
			"{id, value} — one of the config options the agent advertised (the `options` list on the session's idle `status` event: id, name, category, currentValue, options[]). The agent applies it to the next turn and the refreshed list rides the next status event. The Agent tab's model/effort pickers and `bx agent set` use this.", []oapi{pathParam("id", "session id")}, jsonBody("setting", oapi{"id": str("config option id, e.g. model"), "value": str("one of its option values")}, "id", "value"), "ok"},
		{"POST", "/term/sessions/{id}/permissions/{pid}", "Agent sessions", "Answer a permission request", "creator or admin; not an agent sandbox's token, the session's own or another's (403)",
			"{optionId} (one of the request's options) or {decision: allow_once | allow_always | reject_once | reject_always} (the first option of that kind). Any attached client may answer; the first answer wins — 404 afterwards. allow_always records a session rule: later requests with the same kind and title are answered automatically (permission.resolved by \"auto\"). Nothing is written to xbin.json.", []oapi{pathParam("id", "session id"), pathParam("pid", "the request's pid (from the permission.request event)")}, jsonBody("answer", oapi{"optionId": str("option id"), "decision": str("option kind")}), "ok"},
		{"POST", "/term/sessions/{id}/elicitations/{eid}", "Agent sessions", "Answer a question the agent asked", "creator or admin; not an agent sandbox's token, the session's own or another's (403)",
			"{action: accept | decline | cancel, content?} — the answer to an elicitation.request (a form: Claude's AskUserQuestion, an MCP server's form). content is an object of the form's values (keys of the request's schema.properties) and goes with accept; decline tells the agent the user skipped, cancel aborts the question. Any attached client may answer; the first answer wins — 404 afterwards. Cancelling the turn answers cancel.", []oapi{pathParam("id", "session id"), pathParam("eid", "the question's eid (from the elicitation.request event)")}, jsonBody("answer", oapi{"action": str("accept | decline | cancel"), "content": str("object: the form's values")}, "action"), "ok"},
		{"GET", "/term/sessions/{id}/events", "Agent sessions", "Replay or follow the session's event log", "creator or admin",
			"?since=<seq> (0 = from the start) → {events:[{seq, ts, type, data}], next, truncated} — truncated: the cursor predates the oldest kept event (5000 events / 8 MiB per session; show a gap). ?follow=1 streams the same as NDJSON (one event per line, flushed; the response head at once, before any event) from the cursor until the client or the session goes. ?before=<seq>&limit=<n> (either one; D130) → a page instead: {events, hasOlder, nextBefore, truncated, next, last, state:{status, usage, turn, permissions?, elicitations?}} — the tail (no before) or the events before a seq, at most about limit (default 200, max 5000), cut where a fold may start (never inside a card: a tool call and its updates, a message run, a request and its answer…); state is what a fold holds before the page (the tail of a live session adds the requests waiting now). Next page: before=nextBefore while hasOlder. An older xbind ignores both and replays everything (no hasOlder). Event types and payloads: docs/protocol.md §Agent session events. The same events ride /ws/events live as `session` events; a client dedupes on seq.",
			[]oapi{pathParam("id", "session id"), queryParam("since", "replay after this seq", false), queryParam("follow", "1 to stream NDJSON", false),
				queryParam("before", "a page of the events before this seq (a previous page's nextBefore)", false), queryParam("limit", "a page of about this many events (default 200, max 5000); alone: the tail page", false)}, nil, "{events, next, truncated} | a page | NDJSON"},
		{"GET", "/term/sessions/{id}/diff", "Agent sessions", "The full patch of a tool call's or a turn's changes", "creator or admin; not an agent sandbox's token, the session's own or another's (403)",
			"text/x-diff: the complete git patch behind a files.changed event, whose own patch is capped (64 KiB per call, 192 KiB per turn) — ?toolCallId=<id> or ?turn=<n> as the event names it (edit calls, which report their own diff, have one too); ?path=<file> narrows it to one file (a clean path relative to the tile). Diffed again from the session's private snapshots (confined, D78), up to 16 MiB (X-Truncated: true when cut). One diff runs at a time per session and two across xbind; a request past that waits. Snapshots live as long as the session: 404 once it ended, for a call that changed nothing, or on a tile that is not a git repo.",
			[]oapi{pathParam("id", "session id"), queryParam("toolCallId", "a tool call's id (files.changed toolCallId)", false), queryParam("turn", "a turn number (files.changed turn)", false), queryParam("path", "one file of the patch", false)}, nil, "text/x-diff"},
		{"GET", "/term/sessions/{id}/log", "Agent sessions", "The session's text log", "creator or admin",
			"text/plain: the agent host's stderr (the adapter's own output) and the driver's notes — for a session that will not start.", []oapi{pathParam("id", "session id")}, nil, "text/plain"},
		{"GET", "/agent/history", "Agent sessions", "Your past agent sessions", "terminal-level",
			"[{id, cwd, provider, mode, name, created, ended, turns, preview, loadable}], newest first — the transcripts kept when a session ended (or the daemon stopped), per user and per tile (data/agent-history/, the newest 20 per tile; a session that never took a prompt is not kept). ?cwd= narrows to a tile. `loadable`: the agent said it can reopen the session (resume via POST /term/sessions {resume}); otherwise it is read-only history. An entry of a session that targeted a named deployment carries deployment.",
			[]oapi{queryParam("cwd", "a tile path", false)}, nil, "array of past sessions"},
		{"GET", "/agent/history/{id}/events", "Agent sessions", "A past session's transcript", "terminal-level (own sessions)",
			"{meta, events:[{seq, ts, type, data}]} — the persisted event log, in the live /term/sessions/{id}/events shape, so the same client renders it read-only. ?before=&limit= → {meta, …a page} as on a live session (D130). 404 when it is not yours or is gone.",
			[]oapi{pathParam("id", "past session id"), queryParam("before", "a page of the events before this seq", false), queryParam("limit", "a page of about this many events; alone: the tail page", false)}, nil, "{meta, events} | {meta, …page}"},
		{"DELETE", "/agent/history/{id}", "Agent sessions", "Forget a past session", "terminal-level (own sessions)",
			"Removes the persisted transcript. 204.", []oapi{pathParam("id", "past session id")}, nil, "204"},
		{"GET", "/logs", "Runtime", "A tile backend's captured stdout/stderr", "self or terminal-level",
			"text/plain tail of the component's captured backend logs (all generations). Gate: admin, the tile itself, or a user with terminal-level access on it. ?tail=<bytes> (default 64K, max 1M); ?follow=1 streams appended bytes (chunked) until disconnect. The HTTP twin of `bx logs -f`; the terminal window's read-only logs tab.",
			[]oapi{queryParam("component", "component path (its path; a deployment rides deployment=, never component=tile+name: 400)", true), queryParam("tail", "tail size in bytes (default 65536, max 1048576)", false), queryParam("follow", "1 to stream new lines", false), deploymentQuery("one deployment's log (default: the caller's bound deployment, else the primary; a tile's frames and backends read their own only); 404 unknown; a non-primary answer carries X-XBin-Deployment, the echo")}, nil, "text/plain log tail"},
		{"GET", "/runtime", "Runtime", "Full runtime visibility", "admin",
			"Host + per-backend process, namespaces, cgroup usage, and network/egress activity, plus resource sizes — powers the admin console's runtime tab (docs/isolation.md). Each backend says how it is isolated (sandbox: vm | namespace | host) and a VM generation its size (vm {memMiB, vcpus, emulated}); a VM backend's pid/namespaces/rss are its host-side jail's, cgroup covers the guest (D112). A row whose running generation runs a checkpoint (live reload paused) names it: checkpoint, its full tree id; absent while it runs the work tree. backends[] keeps one row per tile, the primary's, which gains deployment on a tile with deployments; the others' rows (the same shape plus deployment) are listed in deploymentBackends[], to people only, absent while none runs.", nil, nil, "{host, backends[], resources[]}"},
		{"GET", "/vm", "Runtime", "VM sandbox availability and policy", "authenticated",
			"Whether Firecracker VM sandboxes can start on this host ({available, reason}: KVM, the shipped assets) and the workspace policy: terminals/backends/tiles switches, tilesEmulated, per-VM memMiB and vcpus, maxVMs, budgetMiB, tilesBudgetMiB; admins also get used {vms, memMiB} and usedTiles, the part tile sandboxes hold (plans/vm-sandbox.md, D120).", nil, nil, "{status:{available,reason}, policy, used?, usedTiles?}"},
		{"PUT", "/vm/policy", "Runtime", "Set the VM sandbox policy", "admin",
			"Turns VM terminals / VM backends / VM tile sandboxes on or off and sizes them. The body is merged onto the stored policy: a field it leaves out keeps its value. Zero sizes mean the defaults (2048 MiB, 2 vCPUs, 8 VMs, budget = maxVMs × memMiB, a 20 GiB VM terminal disk, tilesBudgetMiB = half the budget). Tile sandboxes' VMs also count against tilesBudgetMiB (≤ budgetMiB); tilesEmulated allows them where VMs run emulated (D120). 400 on unknown fields or out-of-range sizes; 409 without isolation.", nil,
			jsonBody("policy (partial: absent fields keep their stored values)", oapi{"terminals": oapi{"type": "boolean"}, "backends": oapi{"type": "boolean"}, "memMiB": oapi{"type": "integer"}, "vcpus": oapi{"type": "integer"}, "maxVMs": oapi{"type": "integer"}, "budgetMiB": oapi{"type": "integer"}, "diskGiB": oapi{"type": "integer"}, "tiles": oapi{"type": "boolean"}, "tilesBudgetMiB": oapi{"type": "integer"}, "tilesEmulated": oapi{"type": "boolean"}}), "{status, policy, stored}"},
		{"GET", "/sandboxes", "Runtime", "Every sandbox xbind runs", "admin, or a manager tile (cap:sandboxes)",
			"For an admin, the sandbox registry (D112): each backend generation, terminal and agent session, and each running tile sandbox (kind tile, with its name and its manager's for/forUser claims), with its tile, user, isolation mode (vm | namespace | host), VMM (kvm | emulate), reserved memory/vCPUs, pid, cgroup leaf, disk and live stats; the VM disks on the host (a tile's terminal layer's, kind terminal, and its tile sandboxes', kind tile with the sandbox's name and sandboxUid); the newest 64 things the sandbox layer refused or failed at (stage refused | start | health | exit, coalesced with a count); the host's health — isolation tier and guards, whether VMs can run and what is missing, the VM policy as effective and as stored, and what running VMs hold (usedTiles: the part tile sandboxes hold) per tile, and health.tileSandboxes {cgroup (why tile sandboxes run without cgroup limits; empty when they have them, or nothing does), flows {used, cap} (their relays' shared connection budget), total {memMiB {used, cap} (the memory the running ones may take, of the sandboxes policy's total), pids {used, cap} (the processes they hold; used -1 = unknown)}, policyError (why the sandboxes policy file can't be read; tile sandboxes are off meanwhile), lowDisk (starts held: the workspace disk is low), trash {entries, bytes} (state deleted or reset, waiting for its confined removal)}; and tileSandboxes: every tile sandbox definition (D120) — [{tile, name, uid, state, stateDetail?, mode, accel?, memMiB, vcpus, diskGiB, diskBytes, for?, forUser?, lastActive?, tileExists}], stopped ones and those of removed tiles too. main's backend rows are as before deployments (id backend:<key>:g<gen>, no deployment, stats scope \"tile\"); another deployment's have the id backend+<name>:<key>:g<gen>, deployment, tile, and stats scope \"deployment\" (its own cgroup leaf); while a tile runs a deployment beyond main, main's \"tile\" stats are its own leaf's. failures of another deployment carry deployment. For a manager tile's backend (cap:sandboxes), its own tile sandboxes: {sandboxes:[SandboxInfo]} (the Tile sandboxes routes; 501 unsupported without --isolate).",
			[]oapi{queryParam("tile", "admin: one tile's sandboxes only", false), deploymentQuery("admin: one deployment's rows and failures (main: the rows without a deployment); echoed as a top-level deployment; 400 for a malformed name")}, nil, "{sandboxes[], disks[], failures[], failureCounts, cgroup, intervalSec, health, tileSandboxes[]} | {sandboxes:[SandboxInfo]}"},
		{"GET", "/tile-status", "Runtime", "One tile's runtime metrics", "self or admin",
			"backend {state,gen,sandbox,vm?,cpuSec,cgroup:{mem,pids},rssKb,fds,activeConns,egress}, disk {usage,quota,blocked}, alerts[], and net {netRef,net,netRules,netSource,netNote} — the effective network (D54). Readable from that tile's terminal (tile-scoped token); `bx status` renders it. (Distinct from /tile-report, the status a tile reports about itself.)",
			[]oapi{queryParam("component", "component path (its path; a deployment rides deployment=, never component=tile+name: 400)", true), deploymentQuery("the deployment reported (default: the caller's bound deployment, else the primary; a tile's frames and backends only their own); the answer gains deployment on a tile with deployments, and for admins and the tile's terminal/agent tokens deployments {primary, liveReload, items:[{name, state, gen, checkpoint?}]}")}, nil, "{backend, disk, alerts, net}"},
		{"GET", "/alerts", "Runtime", "Workspace health alerts", "authenticated",
			"Disk quota / low disk / cgroup at-limit conditions: system alerts go to everyone, tile alerts to admins and that tile's users; admins also get kind backup-keys while backup keys aren't in any exported bundle.", nil, nil, "{alerts:[{level,kind,tile?,message,system}]}"},
		{"GET", "/auth-overview", "Runtime", "Admin overview aggregate", "admin",
			"Components (with roles/uses/vault presence, and vm {memMiB?, vcpus?} when the manifest asks for a VM backend), the grant table, pending grants, and counts — one call powering the admin overview tab.", nil, nil, "overview object"},
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

		// --- push notifications (the xbin app, through the push relay) ---
		{"POST", "/notify", "Push", "Notify a person who can read this tile", "element: the tile's backend (a frontend or shell token: only the person using it)",
			"{user, title, body?, link?, kind?, collapseId?} → 202 {ok:true}. A push notification to the user's registered app devices, sealed end to end (docs/protocol.md §Push notifications). The user must be able to read the calling tile (403 otherwise, and for unknown or disabled users). A frame or terminal token may notify only its own user (403 for anyone else). link is tile-relative (#fragment, ?query, or a path inside the tile; no dot segments, percent-encoded or not); kind makes the push kind tile.<kind>; notifications sharing a collapseId replace each other. 429 + Retry-After over the per-tile limit. Best-effort: 202 also when push is off, the user has no device for the kind, muted the tile (nothing is counted), or is over the per-user limit (dropped). SDK: xbin.NotifyUser. From a non-primary deployment nothing is sent: the answer gains suppressed:true and the line is listed as would-notify.",
			nil, jsonBody("notification", oapi{"user": str("the user id to notify (\"user:<id>\" accepted)"), "title": str("plain text"), "body": str("plain text"), "link": str("relative to the tile: #fragment, ?query or a path inside it"), "kind": str("a–z 0–9 -, at most 32: the push kind becomes tile.<kind>"), "collapseId": str("A–Z a–z 0–9 . _ : -, at most 64")}, "user", "title"), "{ok:true} (202)"},
		{"POST", "/devices/push", "Push", "Register a device for push", "signed-in person (not a tile)",
			"{deviceId, handle, publicKey, kinds?, startHandle?} → {device:{deviceId, kinds, created, updated, lastSent?, pushToStart?, activities?}, workspace, enabled}. startHandle: the relay's Live Activity handle of the app's push-to-start token (\"\" removes it, absent keeps it; a new handle drops it and the device's Live Activities). The app's device session registers (or refreshes) where this user's pushes go: handle = the push relay's handle for this app install and workspace; publicKey = the device's X25519 key (base64url, 32 bytes) every payload is sealed to; kinds = agent | agent.permission | agent.question | agent.turn | tile | tile.<kind> (a kind matches the ones under it; none = all). One registration per (user, deviceId); a handle belongs to one registration. Bound to the login making it: a device session registers under its own device-login id only (403 otherwise); any other login's registration ends with that login and can't take over an enrolled device's id (409). workspace is the `ws` each payload carries. device.needsNewHandle: the relay no longer delivers to this handle for this workspace — create a fresh handle at the relay and register again.",
			nil, jsonBody("registration", oapi{"deviceId": str("the app's device id"), "handle": str("the relay handle"), "publicKey": str("X25519 public key, base64url"), "kinds": arr(), "startHandle": str("the relay's Live Activity handle of the push-to-start token")}, "deviceId", "handle", "publicKey"), "{device, workspace, enabled}"},
		{"GET", "/devices/push", "Push", "Your push registrations", "signed-in person", "{workspace, enabled, devices:[{deviceId, kinds, created, updated, lastSent?, needsNewHandle?, relayError?, pushToStart?, activities?}]}. needsNewHandle (relayError handle_bound | handle_unknown): the relay no longer delivers to that handle for this workspace; the app creates a fresh handle and registers again.", nil, nil, "{workspace, enabled, devices}"},
		{"DELETE", "/devices/push/{deviceId}", "Push", "Unregister a device", "signed-in person (own)", "204; 404 when that device has no registration.", []oapi{pathParam("deviceId", "the app's device id")}, nil, "204"},
		{"POST", "/devices/push/activities", "Push", "Register a Live Activity", "signed-in person (own registration; a device session: its own deviceId)",
			"{deviceId, session | ref, handle, since?} → {activity:{session, created, ended?}}. A Live Activity the device shows for one of your agent sessions: handle = the push relay's Live Activity handle of its ActivityKit update token; ref = the one an activity xbind started by push carries (the answer names the session); since = the turn's start the card shows (unix seconds; taken only for a turn xbind did not see begin). xbind then pushes the turn's generic state (phase running | waiting | idle, since, pending) and its end to it. ended: the turn is over already — xbind sends the card its end and keeps no registration (so too, for 4 hours, for a push-started card whose turn ended before its token came). 404 for a session that is not yours or not there, an unknown ref, or a device with no registration; 429 over the per-person limit.",
			nil, jsonBody("activity", oapi{"deviceId": str("the app's device id"), "session": str("an agent session id"), "ref": str("the ref of a push-started activity"), "handle": str("the relay's Live Activity handle"), "since": oapi{"type": "integer", "description": "unix seconds the card's turn started"}}, "deviceId", "handle"), "{activity}"},
		{"DELETE", "/devices/push/{deviceId}/activities/{session}", "Push", "Unregister a Live Activity", "signed-in person (own)", "The device stopped showing it: xbind stops pushing to it. 204; 404 when none is registered.",
			[]oapi{pathParam("deviceId", "the app's device id"), pathParam("session", "the agent session id")}, nil, "204"},
		{"GET", "/push/prefs", "Push", "Your push preferences", "signed-in person", "{mutedTiles:[path]}: tiles whose /notify reaches none of your devices.", nil, nil, "{mutedTiles}"},
		{"PUT", "/push/prefs", "Push", "Set your push preferences", "signed-in person", "Replaces them (at most 1000 muted tiles) → the stored prefs.", nil, jsonBody("prefs", oapi{"mutedTiles": arr()}, "mutedTiles"), "{mutedTiles}"},
		{"POST", "/push/test", "Push", "Send yourself a test push", "signed-in person", "To every device you registered (kinds don't filter it) → 202 {devices}; 409 when push is off or none is registered.", nil, nil, "{devices} (202)"},
		{"GET", "/push/config", "Push", "The workspace's push relay", "admin",
			"{enabled, source?: env | admin, relay?, relayWorkspace?, keySet?, defaultRelay?, set?, by?, workspace, devices, staleDevices?, stats:{queued, sent, retried, failed, dropped, limited, lastError?, lastErrorAt?}, keyChecked?, keyError?}. The relay key is never shown; the relay stays listed while push is off. xbind checks the key in force with the relay at start and daily (keyChecked; keyError when the relay no longer knows it — PUT {rotate:true} for an admin key, a new XBIN_PUSH_RELAY_KEY for the environment's).", nil, nil, "push configuration"},
		{"PUT", "/push/config", "Push", "Turn push on", "admin",
			"{relay?, key?, rotate?}: relay is an https:// URL (http only on localhost; default: the stored relay, else XBIN_PUSH_RELAY). A key is checked with the relay (GET /v1/workspace; 400 when it does not know it). Without key: the stored key when the relay is the same (409 when the relay no longer knows it), else xbind registers this workspace with the relay (POST /v1/workspaces). rotate:true registers anew: every handle that delivered under the old key then reads needsNewHandle until its app renews it. 409 when the environment configures the relay; 502 when the relay cannot be reached. → the /push/config view.",
			nil, jsonBody("relay", oapi{"relay": str("the relay's base URL"), "key": str("this workspace's key at the relay"), "rotate": boolean()}), "push configuration"},
		{"DELETE", "/push/config", "Push", "Turn push off", "admin", "The relay key and every registration stay, so PUT {} turns it back on with nothing to do in the apps. 409 when the environment configures the relay. 204.", nil, nil, "204"},
		{"GET", "/push/devices", "Push", "Every push registration", "admin", "{devices:[{user, deviceId, kinds, created, updated, lastSent?, needsNewHandle?, relayError?}]}; ?user= narrows to one user.",
			[]oapi{queryParam("user", "one user's registrations", false)}, nil, "{devices}"},
		{"DELETE", "/push/devices/{user}", "Push", "Revoke a user's push registrations", "admin", "Every registration of the user → {removed}; 404 when there is none.", []oapi{pathParam("user", "the user id")}, nil, "{removed}"},
		{"DELETE", "/push/devices/{user}/{deviceId}", "Push", "Revoke one push registration", "admin", "A lost phone → {removed}; 404 when there is none.", []oapi{pathParam("user", "the user id"), pathParam("deviceId", "the app's device id")}, nil, "{removed}"},

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
			"Human users and their per-tile permissions, plus sign-in facts (lastLogin, lastLoginVia, lastSSO = the last SSO sign-in, ssoGroups seen at the last SSO sign-in, ssoSyncError) and roleVia (\"sso\" when the admin role came from a group rule). canCreate is deprecated and ignored (D82) — creation follows ownership. The personal plane (D88): noPersonalTiles, noTerminal, sets, netSets as set on the account, and personal = the resolved plane (workspace personal defaults ∪ own) for non-admins. Admin or the xbin:users grant.", nil, nil, "{users:[{id,name,email,role,roleVia,tiles,canCreate,termApi,termNet,noPersonalTiles,noTerminal,sets,netSets,personal,disabled,invitePending,deviceCount,lastLogin,lastLoginVia,lastSSO,ssoGroups,ssoSyncError}]}"},
		{"POST", "/users", "Users", "Create a user", "xbin:users",
			"With password → ready to sign in; without → credential-less + a single-use invite link (D22); sso:true (needs email) → credential-less with NO invite: the bound email signs in through the IdP (pre-provisioning, D52). Every new account is seeded with the workspace's new-account defaults (GET /defaults newUsers) on top of the given fields; orgs joins the account to orgs at creation (validated first — no half-created account). Under SSO-only mode a non-admin needs sso:true or a password.", nil,
			jsonBody("new user", oapi{"id": str(""), "name": str(""), "role": str("admin|user"), "email": str("SSO binding"), "tiles": oapi{"type": "object", "description": "path/pattern → read|write|terminal"}, "canCreate": deprecatedArr("deprecated, ignored (D82) — accepted for compatibility"), "termApi": boolean(), "termNet": boolean(), "password": str(""), "sso": boolean(), "orgs": arr(),
				"noPersonalTiles": boolean(), "noTerminal": boolean(), "sets": arr(), "netSets": arr()}, "id"), "{user, orgs?, invite?, inviteUrl?, inviteLink?, inviteExpires?}"},
		{"PATCH", "/users/{id}", "Users", "Update a user", "xbin:users", "Update fields and/or reset the password. Disabling drops the user's sessions. The personal plane (D88): noPersonalTiles, noTerminal (switching it on ends the user's live terminal and agent sessions), sets and netSets (permission / network sets for the tiles they own; set names must exist).", []oapi{pathParam("id", "user id")}, freeBody("fields to update"), "updated user"},
		{"DELETE", "/users/{id}", "Users", "Delete a user", "xbin:users", "Removes the user and revokes their sessions.", []oapi{pathParam("id", "user id")}, nil, "ok"},
		{"POST", "/users/{id}/invite", "Users", "(Re)mint an invite link", "xbin:users, or an org admin for a non-admin member of their org",
			"Credential delivery or reset-by-link (D38): re-minting invalidates the previous link; the current password keeps working until redemption. 409 for a non-admin under SSO-only mode (D53).", []oapi{pathParam("id", "user id")}, nil, "{invite, inviteUrl, inviteLink, inviteExpires}"},
		{"DELETE", "/users/{id}/sessions", "Users", "Sign a user out everywhere", "xbin:users",
			"Ends every browser and app session, terminal token and frame token of the user and voids their pending device-enrollment codes (D53); they can sign in again — disable the account to stop that. Their enrolled app devices STAY (devicesLeft counts them; each signs in again without a password) unless devices=1 removes them too.", []oapi{pathParam("id", "user id"), queryParam("devices", "1: also remove every enrolled app device of the user", false)}, nil, "{ok, dropped, devicesLeft, devicesRemoved?}"},
		{"GET", "/sessions", "Users", "Live browser sessions", "xbin:users",
			"Live login sessions with client IPs (login IP + last-seen IP) and activity times, newest activity first. via: session (a browser), device (the native app, device key — device names it) or app (the native app's password/SSO sign-in). current:true marks the caller's own session for cookie-authenticated calls (tile-driven calls carry a frame token, no cookie, so no row is current there). Session ids are credentials and are never returned. Bootstrap owner-token logins are stateless and do not appear. This is the attribution view for the /c/ warm-IP gate: tile subresource loads without credentials are served only from an IP that authenticated within the last hour.",
			nil, nil, "{sessions:[{user,name,created,lastActive,ip,lastIP,current,impersonatedBy?,via,device?}]}"},

		// --- devices & the app's sign-in (docs/auth.md §Device login, native/spec/device-login.md) ---
		{"POST", "/login", "Devices", "The app's password sign-in", "none (the password is the credential; throttled)",
			"The native app's JSON variant of the form login at POST /login: same rules (the login throttle, disabled accounts refused, SSO-only mode refuses non-admins with 403). Returns a human session carried as Authorization: Bearer — same principal and lifetimes as a browser session (12 h idle / 30 d max by default). The app uses it to enroll a device (POST /devices/enroll-code, then /devices/enroll).",
			nil, jsonBody("credentials", oapi{"username": str(""), "password": str("")}, "username", "password"), "{token, tokenType:\"Bearer\", user:{id,name,role}, expiresIdle, expiresMax} (unix seconds)"},
		{"GET", "/login/methods", "Devices", "Which sign-in methods this workspace offers (the app)", "none (public: what the login page shows anyone; not throttled)",
			"Sign-in discovery for the native app: the login page's choices as data, so the app shows the password form and/or the SSO button (with the page's label) and knows whether invite links work. title: the workspace's branding title, else \"xbin\". auth:false in no-auth mode (then everything else is off). password.enabled: the workspace has accounts (a password can sign someone in); adminOnly: SSO-only mode (D53) — non-admins sign in with SSO. sso.enabled: the login page's SSO button is shown (configured and --external-url set); label: its text. invites: invite links can be redeemed (POST /invite/check, /invite/redeem). Cache-Control: no-store. api versions the shape (fields are only ever added).",
			nil, nil, "{api:1, title, auth, password:{enabled,adminOnly}, sso:{enabled,label}, invites}"},
		{"POST", "/invite/check", "Devices", "Whom an invite link is for (the app)", "none (the invite is the credential; throttled)",
			"The invite set-password page (GET /login?invite=) as data: the account the invite belongs to, when it expires and the workspace's title — without spending it. 403 {error:\"invalid or expired invite\"} for an unknown, used, expired or disabled account's invite (counts against the login throttle); 429 throttled.",
			nil, jsonBody("the invite", oapi{"invite": str("the token from <origin>/login?invite=<token>")}, "invite"), "{user:{id,name}, expires, title}"},
		{"POST", "/invite/redeem", "Devices", "Redeem an invite: set the password and sign in (the app)", "none (the invite is the credential; throttled)",
			"The invite form (POST /login/invite) for the native app: sets the invitee's first password, spends the single-use invite and returns the token response of POST /login (a Bearer session, via app; last sign-in via invite; audit-logged). 400 {error} when the password fails the policy (min 8 characters) — the invite is NOT spent; 403 invalid/expired/used; 429 throttled.",
			nil, jsonBody("invite + new password", oapi{"invite": str(""), "password": str("the new password (min 8 characters)")}, "invite", "password"), "{token, tokenType:\"Bearer\", user:{id,name,role}, expiresIdle, expiresMax} (unix seconds)"},
		{"POST", "/devices/enroll-code", "Devices", "Mint a device enrollment code", "a signed-in user (browser or app session)",
			"A one-time code (5 min) that enrolls ONE device for the calling user. url is the xbin://enroll link the shell shows as a QR code; origin is what the device will sign into its logins — the --external-url origin, else the request's scheme://host. Not for tiles, view-as sessions or the bootstrap token (no user account of their own). Step-up: a device outlives the session that adds it, so the caller's sign-in must be under 10 minutes old, or the body carries their password; otherwise 403 {error, stepUp: \"password\" (resend with it) | \"signin\" (sign in again — an SSO account, or SSO-only mode)}. Wrong passwords count against the login throttle.",
			nil, func() oapi {
				b := jsonBody("step-up (optional): the account password", oapi{"password": str("")})
				b["required"] = false
				return b
			}(), "{code, url:\"xbin://enroll?u=<origin>&c=<code>\", origin, expires}"},
		{"POST", "/devices/enroll", "Devices", "Enroll a device (the app)", "none (the enrollment code is the credential; throttled)",
			"Registers the app's device key for the user the code was minted by. publicKey: SPKI DER of an EC P-256 key, base64url without padding (validated before the code is spent). The code is single-use; a wrong one counts against the login throttle. 409 past 32 devices per user.",
			nil, jsonBody("enrollment", oapi{"code": str("from the xbin://enroll link"), "name": str("shown in device lists (≤ 64 chars)"), "platform": str("ios | ipados | android | …"), "publicKey": str("SPKI DER, base64url")}, "code", "publicKey"), "{deviceId, user, origin, name}"},
		{"POST", "/web-ticket", "Devices", "Open the workspace in a browser, signed in (the app)", "the app's device-key session (via device) only",
			"Signed-in Safari: a one-shot ticket (60 s) the app opens top-level — url is <device origin>/login?ticket=<t>&next=<path>. GET /login?ticket= never signs a browser in by itself: a browser signed in as the same user lands on next; a signed-out one gets a \"Continue as <name>\" page naming the account, and its button (POST /login/web-ticket, a same-origin form post from that browser only) opens an ordinary browser session of the same user, landing on next — anyone can mint a link for their own account and hand it over, and a link another app opens looks like the app's own open, so the page is the login-CSRF defence. Only a device-key session mints (not a browser, the app's password/SSO session, a tile, a terminal or the owner token → 403). The ticket is bound to that device session: the app signing out, removing the device, sign-out-everywhere and disabling the account void it, and the browser session it opens ends with the device and the app's sign-out, keeps the device login's time (the enrollment step-up counts from the Face ID sign-in) and its SSO window (D93; 403 {reauth:\"sso\"} past it). next: a path on this workspace — one leading slash, printable ASCII, no backslash, ≤ 2048, not /login… or /logout (400 otherwise); default /. The redeem also refuses a navigation a page started (Sec-Fetch-Site other than none), a browser signed in as someone else, and an altered next. 10 per minute per device (429 + Retry-After); failed redeems count against the login throttle.",
			nil, func() oapi {
				b := jsonBody("where to land (optional)", oapi{"next": str("a path on this workspace, e.g. /c/apps/x/ (default /)")})
				b["required"] = false
				return b
			}(), "{url: \"<origin>/login?ticket=…&next=…\", expires, expiresIn}"},
		{"GET", "/devices", "Devices", "Your enrolled devices", "a signed-in user",
			"The caller's app devices, oldest first. current marks the device behind the calling app session. The public key is never returned.",
			nil, nil, "{devices:[{id,name,platform,origin,created,lastUsed,lastIP,current}]}"},
		{"DELETE", "/devices/{id}", "Devices", "Revoke a device", "the device's own user, or xbin:users",
			"Removes the device key and ends every session it opened; frame tokens those sessions minted die with them. 404 for a device that isn't the caller's (unless admin).",
			[]oapi{pathParam("id", "device id")}, nil, "{ok, user, dropped}"},
		{"GET", "/users/{id}/devices", "Users", "A user's enrolled devices", "xbin:users",
			"The admin view of one user's app devices (same shape as GET /devices, without current).",
			[]oapi{pathParam("id", "user id")}, nil, "{devices:[{id,name,platform,origin,created,lastUsed,lastIP}]}"},
		{"GET", "/auth-settings", "Users", "Get auth settings", "xbin:users",
			"Sign-in policy: owner-token browser-login state (canDisable reports whether THIS caller may disable it), SSO-only mode (passwordLoginDisabled; canDisablePassword = SSO ready), and the SSO configuration (docs/auth.md §SSO) — client secret reduced to clientSecretSet; ready = configured AND --external-url set; groupSync reports whether group rules are active, every group the IdP has been seen sending (knownGroups), and the newest recorded fetch failure.", nil, nil,
			"{tokenLoginDisabled,hasAdminUser,canDisable,passwordLoginDisabled,canDisablePassword,sso:{enabled,ready,kind,preset,issuer,clientId,clientSecretSet,allowedDomains,buttonLabel,externalUrl,groupsClaim,groupsScope,adminGroups,groupSync:{rulesActive,knownGroups,lastError}}}"},
		{"PATCH", "/auth-settings", "Users", "Update auth settings", "xbin:users",
			"Enable/disable owner-token browser login (/login?token= + owner cookie; disabling needs a signed-in admin-user caller; Bearer unaffected); set the SSO config: an sso object replaces it (empty clientSecret keeps the stored one), sso:null clears it; and/or toggle SSO-only mode (passwordLoginDisabled: non-admins sign in through the IdP only; enabling needs a ready provider).",
			nil, freeBody("{tokenLoginDisabled?:bool, sso?:{kind,preset,issuer,clientId,clientSecret,allowedDomains,buttonLabel,groupsClaim,groupsScope,adminGroups}|null, passwordLoginDisabled?:bool}"), "{tokenLoginDisabled,passwordLoginDisabled,sso}"},
		{"POST", "/auth-settings/sso/test", "Users", "Test the SSO provider", "xbin:users",
			"Probes the provider without a user: OIDC discovery + a JWKS fetch, or GitHub API reachability. Tests the stored config, or an unsaved draft passed as {sso:{…}} (empty clientSecret = the stored one). Always 200 — the body is a report, ok:false included.", nil,
			freeBody("{} | {sso:{kind,preset,issuer,clientId,…}}"), "{ok,kind,issuer,redirectUri,externalUrl,ready,endpoints,jwksKeys,warnings,error}"},
		{"GET", "/branding", "Workspace", "The workspace's title and icon", "authenticated",
			"{title, icon, hasIcon} — the branding an admin set (D76): title replaces the word \"workspace\" in the shell header and the browser tab (\"<title> · xbin\"); icon is a data: URI (image/svg+xml | png | jpeg | webp | x-icon, ≤ 256 KiB) that replaces xbin's mark as the favicon and logo, on the workspace page and the sign-in / invite pages. Empty = xbin's own. The shell reads this at boot and again on the `branding` event.", nil, nil, "{title, icon, hasIcon}"},
		{"PUT", "/branding", "Workspace", "Set the workspace's title and/or icon", "admin",
			"{title?, icon?} — each present key is a whole-value replace, \"\" clears it, an absent key leaves it alone. title ≤ 64 characters, no control characters; icon a base64 data: URI of an allowed image type whose bytes match it (≤ 256 KiB decoded; body ≤ 512 KiB). Persisted in data/branding.json (readable while the vault is sealed, so the sign-in page can show it). Publishes a `branding` event; audited.",
			nil, freeBody("{title?:string, icon?:string}"), "{title, icon, hasIcon}"},
		{"GET", "/native-runtime", "Workspace", "Whether the xbin app may open native tile UIs", "authenticated",
			"{enabled, runtime, version} — the workspace's native-runtime switch (docs/elements.md §Native app UI). runtime is what whoami's native.runtime says: version (the runtime-document generation this xbind serves) while enabled, 0 while an admin has turned native tile UIs off.", nil, nil, "{enabled, runtime, version}"},
		{"PUT", "/native-runtime", "Workspace", "Turn the xbin app's native tile UIs on or off", "admin",
			"{enabled: bool}. Off: whoami reports native {runtime: 0, disabled: true} — the app opens every tile as its web page — and the app's runtime documents (/c/<tile>/?native=1) answer 410 with the reason; previews (&preview=1: bx native tree / preview / lint) keep working. Kept in users.json with the workspace policy; publishes a `native` event (open apps re-read whoami); audited.",
			nil, jsonBody("the switch", oapi{"enabled": boolean()}, "enabled"), "{enabled, runtime, version}"},
		{"GET", "/workspace-policies", "Workspace", "The workspace's policies for partitioned tiles", "a person (their terminal or agent session included), or admin (other tile principals: 403)",
			"{schema: 1, partitionConsent, credentialResetConfirm} — both off by default. partitionConsent: another partitioned tile uses a person's data only with their consent; credentialResetConfirm: an admin-set sign-in link, password or SSO email for someone who holds partitions waits for them to confirm (or 24 h after they are notified). Both apply to partitioned tiles only. Kept in data/workspace-policies.json (PD-55); while xbind can't read it, 500, and a switch it can't read keeps its last value or is on.", nil, nil, "{schema, partitionConsent, credentialResetConfirm}"},
		{"PUT", "/workspace-policies", "Workspace", "Set the workspace's policies", "admin",
			"{partitionConsent?, credentialResetConfirm?} — each present key replaces that switch, an absent one leaves it alone; at least one. → the full view. A file xbind can't read is 500 and not overwritten. Publishes a `policies` event (re-read GET); audited with each switch's old→new.", nil, jsonBody("the switches", oapi{"partitionConsent": boolean(), "credentialResetConfirm": boolean()}), "{schema, partitionConsent, credentialResetConfirm}"},
		{"GET", "/chrome", "Workspace", "Tiles asking for, or approved as, trusted chrome", "admin",
			"{tiles: [{path, requested, approved, shipped?, chrome, missing?}]} — every component whose xbin.json says chrome: true, and every path a workspace admin approved (D118). chrome is the effective answer: a manifest's flag makes a tile chrome (unsandboxed, acting as whoever opens it) only when it is shipped chrome (tiles/organisations) or approved. missing marks an approval with no component at the path any more. root and shell are chrome implicitly and not listed.", nil, nil, "{tiles}"},
		{"PUT", "/chrome", "Workspace", "Approve a tile as trusted chrome, or withdraw it", "admin",
			"{path, approved: bool}. Approving needs a component at the path, and takes effect while its xbin.json says chrome: true — its documents then run unsandboxed with the session cookie, acting as whoever opens the tile, so approve only a tile whose every writer (terminal users, their coding agents) you trust as much as the shell (D118). root/shell are refused (nothing to approve). Kept in users.json; an approval is a leftover a new tile at the path would inherit, so non-admin creation there is refused. Publishes `grants` for the tile (open frames re-create with the new sandbox); audited.",
			nil, jsonBody("the approval", oapi{"path": str("tile path"), "approved": boolean()}, "path", "approved"), "{path, requested, approved, shipped?, chrome, missing?}"},

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
			"Which network scopes a terminal on ?tile= may take for the caller (D54): the org scope where the owning org has network sets, the personal scope where a personal tile's owner has network sets (D88 — added to internet, not replacing it), one set:<name> scope per named set the caller may pick (the owner's sets; every workspace set for a workspace admin — D65), internet/host where allowed, offline. The picker offers exactly this list; the session frame repeats it; default is never a set.",
			[]oapi{queryParam("tile", "component path", true)}, nil, "{tile, scopes:[{id,label,desc}], default, label, org, personal}"},
		{"GET", "/net-sets", "Orgs", "Organisation network sets", "xbin:users",
			"Named reach rules attached to orgs by reference (D54): internet | internet:<host|host-glob|ip|cidr>[:port] | lan:<ip|cidr>[:port] | host | provider:<tile-glob>. An org's union is the ceiling on its tiles' net bindings, what its admins may bind without an allowance, the default binding (`org`) of its net-declaring tiles, and the egress of terminals opened on them. A set is also a pickable terminal scope and a workspace-admin binding ref, set:<name> (D65); boundBy lists the tiles bound to each. Sets also make up users' personal networks (D88); heldBy lists user:<id>, personal-defaults and new-accounts holders.", nil, nil, "{sets:{name:{rules,created}}, attachedTo:{name:[orgIds]}, boundBy:{name:[tiles]}, heldBy:{name:[holders]}}"},
		{"PUT", "/net-sets/{name}", "Orgs", "Create/replace a network set", "xbin:users",
			"400 on grammar (one destination per rule; globs only in internet: host rules; lan: rules are addresses/CIDRs; internet: addresses must be public). Attached orgs' net-declaring tiles and every tile bound to set:<name> restart.", []oapi{pathParam("name", "set name")},
			jsonBody("rules", oapi{"rules": arr()}, "rules"), "{name, rules, created, attachedTo}"},
		{"DELETE", "/net-sets/{name}", "Orgs", "Delete a network set", "xbin:users", "409 while any org, user, the personal defaults or the new-account defaults hold it, or any tile is bound to set:<name> (D65, D88).", []oapi{pathParam("name", "set name")}, nil, "ok"},
		{"PUT", "/orgs/{org}/sso-groups", "Orgs", "Replace the org's IdP-group rules", "xbin:users",
			"Workspace-admin only (rules grant power). Each rule maps a provider group (OIDC claim value, GitHub org/team-slug, Google group email) to a membership shape; matching rules union. Applied at each member's next SSO sign-in (docs/auth.md §Group sync).",
			[]oapi{pathParam("org", "org id")}, jsonBody("rules", oapi{"rules": arr()}, "rules"), "updated org"},
		{"GET", "/permission-sets", "Orgs", "Permission sets", "xbin:users",
			"Named allowance bundles attached to orgs by reference (D28): each set carries allow entries, policy rows and the terminal flags; attachedTo lists the orgs holding each, heldBy the users / personal-defaults / new-accounts (D88: a user's sets govern the tiles they own).", nil, nil, "{sets:{name:{allow,policy,termApi,termNet}}, attachedTo:{name:[orgIds]}, heldBy:{name:[holders]}}"},
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
		{"GET", "/access/{user}", "Orgs", "A person's level on the calling tile", "element: the tile's backend (instance token)",
			"{user, level: none|read|write|terminal, active}. What a tile's backend asks when a credential of its own outlives a call — an SSH key a person registered on its page — to learn whether that person may still use it: the level X-XBin-User-Level would carry now (ownership, org membership and shares, the person's entries, the workspace defaults, the personal plane), none for a disabled or unknown account. active: the id is an account that can sign in. Only the calling tile, never another; a frame or terminal token (acting for the person using the tile), a person and an admin get 403. An unknown id is {level: none, active: false}, never 404. SDK: xbin.AccessOf.",
			[]oapi{pathParam("user", "the person's user id (\"user:<id>\" accepted)")}, nil, "{user, level, active}"},
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
			"defaultTiles: the visibility baseline every user gets (D27). newUsers: what every NEW account starts with — tiles, terminal flags, org memberships — seeded at creation (admin-added, invited, SSO auto-provisioned; D52; newUsers.canCreate is deprecated and ignored, D82). newUsers also seeds the personal-plane switches (OR'd — a seed can only restrict) and sets (D88). tileCreation: any | org-only (non-admins may only create org-owned tiles). personalDefaults: the live personal plane — permission and network sets every non-admin gets on top of their own, for the tiles they own (D88).",
			nil, nil, "{defaultTiles:{pattern:level}, newUsers:{tiles,canCreate,termApi,termNet,orgs:[{org,level,create}],noPersonalTiles,noTerminal,sets,netSets}, tileCreation, personalDefaults:{sets,netSets}}"},
		{"PUT", "/defaults", "Orgs", "Replace provisioning defaults", "xbin:users",
			"Each present key replaces that setting wholesale; absent keys are left alone. Default orgs must exist; newUsers never grants admin.", nil,
			freeBody("{defaultTiles?:{pattern:level}, newUsers?:{tiles,termApi,termNet,orgs:[{org,level,create}],noPersonalTiles,noTerminal,sets,netSets}, tileCreation?:\"any\"|\"org-only\", personalDefaults?:{sets,netSets}}"), "the resulting defaults"},

		// --- workspace management ---
		{"POST", "/create", "Workspace", "Create a component", "xbin:writer",
			"Scaffolds a new component (same as `bx new`); never overwrites. Owner, or an element granted xbin:writer. owner: org:<id> (needs the org's Create knob) | user:<id> | \"\" = creator-owned, workspace-owned for admins; under the org-only tile-creation policy a non-admin's empty owner resolves to their single Create org. A path holding '+' in any segment is refused (403) for every creator, admins included, on every creation route: <tile>+<name> is a tile deployment's URL (an existing directory named so keeps resolving).", nil,
			jsonBody("component to create", oapi{"path": str("apps/thing"), "runtime": str("static|go|node|python"), "title": str(""), "expose": boolean(), "owner": str("org:<id> | user:<id> | \"\"")}, "path"), "{path, files, owner?}"},
		{"POST", "/clone", "Workspace", "Clone (fork) a component", "xbin:writer",
			"Copies an existing component (git history included) and rewrites references to the old path across its files. Secrets and resource data are not copied; cross-scope uses re-enter owner approval. Rejects a copy whose uses don't resolve. owner as /create.", nil,
			jsonBody("what to clone", oapi{"from": str("apps/thing"), "to": str("apps/thing-fork"), "owner": str("org:<id> | user:<id> | \"\"")}, "from", "to"), "{path, from, rewritten, pendingGrants}"},
		{"GET", "/builtins", "Tiles", "Builtin tile catalog", "authenticated", "Optional tiles bundled in the binary; `installed` marks ones already at their default path.", nil, nil, "[{name,title,description,defaultPath,installed}]"},
		{"POST", "/builtins/import", "Tiles", "Import a builtin tile", "xbin:writer", "Copies an embedded tile into the workspace (docs/overview/14-lifecycle.md §Getting code in); returns any grants it now needs.", nil,
			jsonBody("tile to import", oapi{"name": str("llm-gw"), "path": str("optional install path"), "owner": str("as /create")}, "name"), "{path, files, pendingGrants}"},
		{"GET", "/builtins/updates", "Tiles", "Available builtin updates", "authenticated", "Scaffold + imported tiles that have a newer embedded version (docs/overview/14-lifecycle.md §Keeping code fresh).", nil, nil, "array of updatable builtins"},
		{"POST", "/builtins/update", "Tiles", "Apply a builtin update", "xbin:writer", "replace overwrites; merge 3-way-merges (git merge-file); pr files the update as a change proposal; pin/unpin stop/resume offers. Never touches template instances. Each xbin.json keeps its installed partition (docs/partitions.md); notes say where upstream asks otherwise. pr answers {files: [], notes} instead of {pr} when upstream changed nothing but a partition (recorded as applied).", nil,
			jsonBody("update", oapi{"id": str("scaffold:shell"), "mode": str("replace|merge|pr|pin|unpin")}, "id", "mode"), "{files, notes?} | {pr}"},
		{"GET", "/templates", "Tiles", "Template blueprints", "authenticated", "Builtin ∪ workspace template components (docs/overview/03-components.md §Templates). partition: the mode new instances start in; partitionSkipped: why this xbind won't write it (\"needs --isolate\"); both absent when the template names none.", nil, nil, "[{id,source,title,description,defaultName,partition?,partitionSkipped?}]"},
		{"POST", "/templates/new", "Tiles", "Instantiate a template", "xbin:writer", "Copies a blueprint into a named component, stripping the template marker. The instance starts in the template's partition mode (docs/partitions.md) unless partition is false or xbind runs without --isolate.", nil,
			jsonBody("instantiation", oapi{"source": str("agent | apps/mytpl"), "path": str("optional target path"), "owner": str("as /create"), "partition": boolean()}, "source"), "{path, files, pendingGrants, partition?, partitionSkipped?}"},
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
		{"POST", "/grants", "Grants", "Approve / add a grant", "admin", "Approves a pending request or adds a grant. Targets are component paths, res:… resources, gpu:… devices, or reserved capabilities (cap:net-admin, cap:containers, cap:open-links — which widens the tile's frontend sandbox so links open in new tabs, ND11 — and cap:sandboxes, a sandbox manager's backend driving xbind's tile sandboxes: a workspace admin approves it, no allowance delegates it, D120). (Network egress is not a grant — it's a `net` interface binding; see /bindings.) Granting xbin or an xbin:* target to a tile that has non-primary deployments answers 409: remove them first. Granting xbin, xbin:*, cap:sandboxes, cap:net-admin or cap:containers to a tile whose recorded partition mode, or whose code's request, has user partitions answers 409: a partitioned tile can't hold them.", nil,
			jsonBody("grant", oapi{"from": str("apps/x"), "target": str("apps/y | res:… | gpu:0 | cap:open-links"), "role": str("reader|writer|admin|egress|…")}, "from", "target", "role"), "ok"},
		{"DELETE", "/grants", "Grants", "Revoke a grant", "admin", "", nil,
			jsonBody("grant to revoke", oapi{"from": str(""), "target": str(""), "role": str("")}, "from", "target", "role"), "ok"},
		{"GET", "/bindings", "Grants", "Interface requests/providers + bindings", "admin", "Typed capability wiring (docs/overview/11-interfaces.md). `pending` lists unbound interface slots with the providers that can satisfy each — the bind-on-install prompt; default:\"org\" marks a net slot already satisfied by the owning org's network sets (D54). `inert` lists net bindings stored but resolving to no egress, with the reason. Binding values are a ref string, or an array of refs for multi:true slots; refs are provider[#instance]. `instances` maps each instances-provider to its registered {id: pathPrefix}. `approvable` names the components whose wiring THIS caller may change (ws admin: all; org admin: their orgs' tiles); pending rows carry the same flag, and options POST would refuse — for everyone (a net ref outside the owning org's network sets) or for this caller (a set:<name> row from an org admin, D65) — are blocked:true. Net options carry one set:<name> row per network set; `netOptions` maps every visible net slot's component to its full option list, bound or not (the re-bind pickers read it); `sandboxNetOptions` does the same for components with sandbox-net slots (a sandbox manager's network classes: no host, no provider tiles, no default — unbound is none), whose inert classes are listed in `inert` by slot. `exposes` is every exposed endpoint in view, bound or not, with all its routes (an endpoint takes many, D79) and the sources it can bind — the add-a-route data. `partitioned` (components rows) is true for a tile whose recorded partition mode has user partitions: its bindings are global binds (docs/partitions.md §Bind types); an http option naming a partitioned tile without a global instance is blocked:true on an unpartitioned requester's slot (POST answers 409).", nil, nil, "{bindings, instances, components:[{component,interfaces,provides,partitioned?}], pending:[{component,slot,kind,service,multi?,default?,approvable,options:[{id,label,blocked?}]}], exposes:[{component,slot,kind,paths?,proto?,port?,approvable,routes:[{source,host?,zone?,listen?}],options}], inert:{comp:{slot:reason}}, approvable:{comp:true}, netOptions:{comp:[{id,label,blocked?}]}, sandboxNetOptions:{comp:[{id,label,blocked?}]}}"},
		{"POST", "/bindings", "Grants", "Bind a component's interface slot to provider(s)", "admin", "provider = one ref; providers = the full ordered set for a multi:true http slot (replaces). Refs are provider[#instance]; an instances-provide must be bound to a specific instance. Net builtins: internet | internet:<spec> | host | lan:<cidr> | org (the owning org's network sets, org-owned tiles only) | none | set:<name> (one named network set — a workspace-admin act: 403 for org admins, provider-only sets refused, D65); on an org-owned tile with network sets the ref must be inside the sets (D54). A sandbox-net slot (a sandbox manager's network class) takes none | internet | internet:<spec> | lan:<cidr> | org | personal | set:<name> — never host, a provider tile or a set that says host — under the same approvals, and binding it restarts nothing. An unpartitioned component's http slot bound to a tile whose partition mode has user partitions and no global instance is 409: no call of it would reach that tile (docs/partitions.md).", nil,
			jsonBody("binding", oapi{"component": str("apps/x"), "slot": str("net"), "provider": str("apps/firewall | internet | org | none | set:devs-net | apps/imap#abc"), "providers": arr(),
				"host": str("exposed http endpoint: an exact public hostname"), "zone": str("exposed http endpoint: a delegated zone '*.suffix'"), "listen": str("exposed stream endpoint: the host listen address ':port'"),
				"add": oapi{"type": "boolean", "description": "append this one route to an exposed endpoint instead of replacing its routes (an endpoint takes many — another hostname, zone or host port; D79)"}}, "component", "slot"), "ok"},
		{"PUT", "/iface-instances", "Grants", "Register a provider's interface instances", "self or admin",
			"A provider whose provide declares {instances:true} registers its concrete instances (runtime config — accounts, profiles): {id: pathPrefix}. Prefixes are PROVIDER-RELATIVE (\"/m/1\") — xbind composes /api/<provider>+path for consumers; workspace-absolute \"/api/…\" registrations are rejected 400. Replaces the whole map; elements may only set their own; bound requesters are re-wired. Instances bind as provider#id. From a non-primary deployment the map is stored dormant and never routed (no grants event, no re-wiring); the answer gains dormant:true.", nil,
			jsonBody("instances", oapi{"component": str("admin only; elements are self-scoped"), "instances": oapi{"type": "object"}}, "instances"), "{component, instances}"},
		{"DELETE", "/bindings", "Grants", "Clear a binding", "admin", "Without a route: clear the slot (an exposed endpoint is unpublished). With provider and/or host|zone|listen: remove only the matching route(s) of the endpoint — 404 if none.", nil,
			jsonBody("binding to clear", oapi{"component": str("apps/x"), "slot": str("net"), "provider": str("only routes through this source"), "host": str("only this hostname's route"), "zone": str("only this zone's route"), "listen": str("only this host port's route")}, "component", "slot"), "ok"},

		// --- ingress (docs/ingress.md) ---
		{"PUT", "/ingress-hosts", "Ingress", "Register a delegated zone's concrete hostnames", "self or admin",
			"A tile with a delegated-zone http expose binding registers the hostnames it serves (docs/ingress.md); replaces the set, [] clears. Every host must sit inside one of the tile's own bound zones (403 outside — the authority boundary), be a valid bare hostname, and not collide with an exact-bound host or another tile's registration (409). Routes update live; no restart. From a non-primary deployment the set is stored dormant and never routed; the answer gains dormant:true.", nil,
			jsonBody("hosts", oapi{"component": str("admin only; elements are self-scoped"), "hosts": arr()}, "hosts"), "ok"},
		{"GET", "/ingress-routes", "Ingress", "Concrete host→tile routes", "terminator tiles + admin",
			"A tile with provides {kind:\"ingress\"} sees the routes bound through it (its proxy/ACME config input); admins see all; others 403. A non-primary terminator's credentials read an empty list.", nil, nil, "{routes:[{host,component,slot,paths,source,zone?}]}"},
		{"GET", "/ingress", "Ingress", "The whole ingress picture", "admin",
			"Every exposes slot with its binding and policy state (routes: every route of the slot, D79; the scalar source/host/zone/listen repeat the first), live routes, registered hosts, terminators, stream listener health (streams[].error/active), forwards, and the builtin HTTP listener.", nil, nil, "{exposes:[…], routes, ingressHosts, terminators, streams:[…], forwards, httpListener:{listen,tls}}"},
		{"POST", "/lifecycle", "Grants", "Set a component's lifecycle state", "admin", "Enable/disable/offload a component (docs/overview/14-lifecycle.md). A non-enabled backend won't spawn; disabling stops it now, and the tile sandboxes it manages (state kept); offloaded[-full] archives + frees local bytes — refused 409 while its tile sandboxes hold state; enabling an offloaded component restores it. Needs an @archive binding for offload.", nil,
			jsonBody("lifecycle", oapi{"component": str("apps/x"), "state": str("enabled|disabled|offloaded|offloaded-full")}, "component", "state"), "{ok, state}"},
		{"POST", "/backup", "Backup", "Back up a component now", "admin", "Streams a self-describing tar (source + scope data + terminal env + a manager tile's sandbox definitions, never their state) of the component to its bound @archive provider (docs/overview/14-lifecycle.md). With a vault barrier every archive is sealed under a backup key (XBINSEAL), and a scope root's data goes to a data archive of its own (.data.<tile-key>) the main archive names (schema 3); 502 while the vault is sealed.", nil,
			jsonBody("backup", oapi{"component": str("apps/x")}, "component"), "{ok, version}"},
		{"GET", "/backups", "Backup", "List a component's archived versions", "admin", "Passes the bound archiver's version list through: [{version, time, size}].", []oapi{queryParam("component", "the component", true)}, nil, "{versions:[{version,time,size}], archiver}"},
		{"POST", "/restore", "Backup", "Restore a version or a single file", "admin", "Restore a whole version (stops + replaces the component's data/source from the archive; a manager tile's sandbox definitions come back by uid, stopped — sandboxesSkipped lists those left out) or, with `file`, stream one member back without touching live state (docs/overview/14-lifecycle.md).", nil,
			jsonBody("restore", oapi{"component": str("apps/x"), "version": str("optional; default latest"), "file": str("optional; one path within the archive")}, "component"), "{ok, component, restored, sandboxesSkipped?} or the file bytes"},
		{"GET", "/backup-schedule", "Backup", "List scheduled backups", "admin", "", nil, nil, "{schedules:[{component,schedule,retention}]}"},
		{"POST", "/backup-schedule", "Backup", "Schedule (or reschedule) backups for a component", "admin", "Owner-scheduled backup on the cron engine (docs/overview/14-lifecycle.md). retention prunes to N newest versions after each run (0 = keep all).", nil,
			jsonBody("schedule", oapi{"component": str("apps/x"), "schedule": str("0 3 * * * | @every 24h"), "retention": str("N (int)")}, "component", "schedule"), "ok"},
		{"DELETE", "/backup-schedule", "Backup", "Remove a component's backup schedule", "admin", "", []oapi{queryParam("component", "the component", true)}, nil, "ok"},
		{"GET", "/backup-keys", "Backup", "Backup keys: sealing mode and export status", "admin", "mode sealed | plaintext (no vault barrier: archives are plain tars) | vault-sealed (no backups until unsealed); keys, unexported (keys no exported bundle holds), lastExport, exports, erased, erasedSinceExport.", nil, nil, "{mode, keys, unexported, lastExport?, exports, erased, erasedSinceExport}"},
		{"POST", "/backup-keys/export", "Backup", "Export the backup key bundle", "admin", "The disaster-recovery bundle: the vault's barrier descriptor (the data key wrapped under the passphrase), every backup key wrapped under the data key, and the erase tombstones — useless without the vault passphrase. Recorded as an export (the backup-keys alert clears). 409 without a vault barrier.", nil, nil, "{schema, workspace, created, barrier, keys:[…], erased:[…]}"},
		{"POST", "/backup-keys/import", "Backup", "Import another workspace's backup keys", "admin", "Unwraps the bundle's data key in memory with its workspace's vault passphrase, re-wraps each backup key under this vault (never adopting the other key or passphrase) and takes its tombstones. 400 for a wrong passphrase or a bad bundle; needs this vault unsealed.", nil,
			jsonBody("import", oapi{"bundle": oapi{"type": "object"}, "passphrase": str("the exporting workspace's vault passphrase")}, "bundle", "passphrase"), "{imported, present, erased}"},
		{"POST", "/backup/erase", "Backup", "Crypto-erase a tile's backups", "admin", "Deletes the tile's backup keys — data: every data key (its source stays restorable); all: every key — tombstones them, and asks the archiver to delete the versions sealed under them (POST /archive/erase, optional). The data is unreadable in every archive from then on.", nil,
			jsonBody("erase", oapi{"component": str("apps/x"), "what": str("data|all")}, "component", "what"), "{ok, component, erased:[{id,subject,gen}], archiver}"},

		// --- tile deployments: live reload, deployments, promotion (docs/protocol.md) ---
		{"GET", "/deployments", "Deployments", "A tile's deployments and live reload state", capDeployRead,
			"The state in the caller's view: full (the tile's writers), deployment (a non-primary deployment's own credentials: the primary and that deployment) or reader (facts about the primary only: no other deployment's name, no count). A tile without deployments answers record:false and nothing is written. features lists what this xbind speaks; a plain 404 or 405 means an older xbind. deployment= selects one (echoed as selected; a non-primary one needs write).", []oapi{queryParam("tile", "a tile's path; a query names a deployment with deployment=, never as tile+name (400: a '+' in a query string reads as a space)", true), queryParam("deployment", "select one deployment (echoed as selected)", false)}, nil, "State {tile, record, schema, seq, features, view, primary, liveReload, deployments, edges, caller, …}"},
		{"GET", "/deployments/log", "Deployments", "A tile's deploy log", capDeployWrite,
			"Every finished deploy attempt, newest first, failed ones included; queued and in-flight ones too. A non-primary deployment's own credentials see its entries only. ?id= one attempt; wait (at most 25 s) holds the answer until it finishes.", []oapi{queryParam("tile", "a tile's path; a query names a deployment with deployment=, never as tile+name (400: a '+' in a query string reads as a space)", true), queryParam("deployment", "one deployment's entries", false), queryParam("limit", "default 50, max 200", false), queryParam("before", "entries older than this id", false), queryParam("id", "one attempt", false), queryParam("wait", "seconds to wait for id to finish (max 25)", false)}, nil, "{tile, entries:[DeployEntry], more} | {entry}"},
		{"GET", "/deployments/diff", "Deployments", "Diff checkpoints, deployments or the work tree", capDeployWrite,
			"A git patch between two specs (c:<id> | deployment:<name> | work-tree; default from the primary to the work tree), with X-XBin-Checkpoint-From/-To naming the resolved checkpoints; X-Truncated past 16 MiB. A work-tree side captures a checkpoint and needs terminal level. Runs confined. 409 on a tile without deployments (nothing to diff, nothing captured); 429 while one diff runs and one waits for the tile; 504 past 30 s.", []oapi{queryParam("tile", "a tile's path; a query names a deployment with deployment=, never as tile+name (400: a '+' in a query string reads as a space)", true), queryParam("from", "c:<id> | deployment:<name> | work-tree", false), queryParam("to", "c:<id> | deployment:<name> | work-tree", false), queryParam("path", "one file", false), queryParam("stat", "1: per-file counts as JSON", false)}, nil, "text/x-diff | {from, to, files:[{path, status, added, removed, binary?}], truncated}"},
		{"POST", "/deployments/live-reload/pause", "Deployments", "Pause live reload", capDeployTerminal,
			"Saves stop reaching the live reload target, which keeps a fresh checkpoint of the work tree. On a tile without deployments this is an opt-in: the record and the checkpoint store are created. Idempotent. 409 for a backend tile without --isolate.", nil, deployBody("pause", oapi{}), "{state, deploy?}"},
		{"POST", "/deployments/live-reload/resume", "Deployments", "Resume live reload", capDeployTerminal,
			"deployment (default: where live reload last was) follows the work tree and every save again. Resuming onto main while it is the only deployment and every setting is at its default removes the record (record:false). 409 onto a protected primary.", nil, deployBody("resume", oapi{"deployment": str("default: where live reload last was"), "confirm": str("\"other-branch\" (branches/1)")}), "{state, deploy}"},
		{"POST", "/deployments/live-reload/now", "Deployments", "Reload now, keeping live reload paused", capDeployTerminal,
			"Checkpoints the work tree and deploys it once where live reload last was, which stays pinned; unchanged:true when nothing moved. Onto a protected primary: a tile manager in a person's own session, with expect and seq. confirm:\"other-branch\" (branches/1): feed a deployment assigned a branch from a work tree on another one this time; without it, 409 naming both branches.", nil, deployBody("reload now", oapi{"expect": str("c:<id>: the checkpoint the caller reviewed"), "confirm": str("\"other-branch\" (branches/1)")}), "{state, deploy?}"},
		{"POST", "/deployments/live-reload/attach", "Deployments", "Attach live reload to another deployment", capDeployTerminal,
			"The work tree is checkpointed and the former target keeps that checkpoint; deployment then follows the work tree. 409 while live reload is paused (resume instead) and onto a protected primary. confirm:\"other-branch\" (branches/1): feed a deployment assigned a branch from a work tree on another one this time; without it, 409 naming both branches. It lasts until live reload moves or the work tree's branch changes again.", nil, deployBody("attach", oapi{"deployment": str(""), "confirm": str("\"other-branch\" (branches/1)")}, "deployment"), "{state, deploy}"},
		{"POST", "/deployments/add", "Deployments", "Add a deployment", capDeployTerminal,
			"A new deployment with its code (a fresh checkpoint of the work tree, the primary's, or c:<id>), its own data (empty, or seeded from the primary's with confirm:\"copy-data\": a tile manager's act, like joining data already seeded or restored), a vault holding the primary's key names without values, deliveries on (its cron jobs and bus subscriptions deliver to it) and alwaysOn off. attach:true (only with from work-tree) moves live reload onto it. Names: lowercase letters, digits and -, a letter first, at most 24. An opt-in on a tile without deployments; its dry run there captures nothing (impact.code null). data:\"seed\" seeds it once it is added (409 on a workspace-scope tile, whose data has one namespace); a seed refused then leaves it added, empty. branches/1: branch assigns it a branch; newBranch creates one in the tile first (never an existing one: 409), checks it out and assigns it; code from a work tree on another branch than the one assigned needs confirm \"other-branch\" (joined to \"copy-data\" with a comma when both apply).", nil,
			deployBody("add", oapi{"deployment": str("the new name"), "from": str("work-tree | primary | c:<id> (default work-tree)"), "data": str("empty | seed (default empty)"), "attach": boolean(), "confirm": str("\"copy-data\" with data:seed; \"other-branch\" (branches/1); both: \"copy-data,other-branch\""), "branch": str("branches/1: the branch it requires"), "newBranch": str("branches/1: a branch to create, check out and assign")}, "deployment"), "{state, deploy, joins?}"},
		{"POST", "/deployments/remove", "Deployments", "Remove a deployment", capDeployTerminal,
			"Stops it and deletes its vault, logs, registrations and build products, and its data when this tile is the last to claim them; checkpoints stay until GC. 409 for main or the primary.", nil, deployBody("remove", oapi{"deployment": str(""), "confirm": str("\"erase\"")}, "deployment", "confirm"), "{state}"},
		{"POST", "/deployments/deploy", "Deployments", "Deploy a checkpoint to a deployment, or restart it", capDeployTerminal,
			"Puts checkpoint (default: a fresh checkpoint of the work tree, equal to expect when given) on deployment; if live reload was attached to it, live reload pauses. restart:true starts a new generation of its current code and clears the crash breaker. Asynchronous: follow the deploy entry (GET /deployments/log?id=, deployments events). Onto a protected primary: a tile manager in a person's own session, with checkpoint and seq. confirm:\"other-branch\" (branches/1): feed a deployment assigned a branch from a work tree on another one this time; without it, 409 naming both branches.", nil,
			deployBody("deploy", oapi{"deployment": str(""), "checkpoint": str("c:<id>"), "expect": str("c:<id>, without checkpoint: the capture must equal it"), "restart": boolean(), "confirm": str("\"other-branch\" (branches/1), for a deploy of the work tree")}, "deployment"), "{state, deploy?}"},
		{"POST", "/deployments/promote", "Deployments", "Promote one deployment's code to another", capDeployTerminal,
			"to receives from's current code (its checkpoint, or a fresh checkpoint of the work tree while from follows it): code only, data stays. The target's rules are deploy's; onto a protected primary, expect and seq.", nil, deployBody("promote", oapi{"from": str(""), "to": str(""), "expect": str("c:<id>")}, "from", "to"), "{state, deploy?}"},
		{"POST", "/deployments/rollback", "Deployments", "Roll a deployment back", capDeployTerminal,
			"Deploys checkpoint (default: the newest ok entry of its deploy log with other code) to deployment; data stays. Onto a protected primary checkpoint and seq are required. 409 when there is nothing earlier.", nil, deployBody("rollback", oapi{"deployment": str(""), "checkpoint": str("c:<id>")}, "deployment"), "{state, deploy?}"},
		{"POST", "/deployments/primary", "Deployments", "Make another deployment the primary", capDeployManager,
			"The bare URL moves to deployment; data does not move and xbin.json is never written. It starts first, then the old primary restarts or stops; their registrations swap; sessions named after either restart. 409 unless it is healthy, when it would split a scope's data, or when the tile's recorded partition mode has user partitions (every person's data stays with the primary: promote instead). While the primary is protected: expect and seq.", nil,
			deployBody("primary", oapi{"deployment": str(""), "confirm": str("\"data-stays\""), "expect": str("c:<id>")}, "deployment", "confirm"), "{state, deploy?, inactiveHosts?}"},
		{"POST", "/deployments/protect", "Deployments", "Protect the primary, or stop protecting it", capDeployManager,
			"on: the primary is pinned in place (while live reload is on it, to a fresh checkpoint equal to expect), and only tile managers change its code from then on, naming the checkpoint they reviewed; sessions following it restart. off: nothing moves. An opt-in on a tile without deployments. nested lists the components nested in the tile ({tile, protected, manager}); warnings name each unprotected one, and \"not enforced: authentication is off\" under --no-auth.", nil, deployBody("protect", oapi{"on": boolean(), "expect": str("c:<id>")}, "on"), "{state, deploy?, nested?, warnings?}"},
		{"POST", "/deployments/edge", "Deployments", "Set an outbound edge's policy for non-primary deployments", capDeployManager,
			"edge: slot:<slot> or grant:<target>; policy: read | block | inherit as the edge takes them (its values), or default to remove the override. Applies at their next call.", nil, deployBody("edge", oapi{"edge": str("slot:<slot> | grant:<target>"), "policy": str("read | block | inherit | default")}, "edge", "policy"), "{state}"},
		{"POST", "/deployments/deliveries", "Deployments", "Switch a deployment's deliveries off, or back on", capDeployManager,
			"The off switch of its cron jobs and bus push subscriptions, on by default: off keeps them registered but dormant, on makes them fire for it again (interface instances and ingress hosts never activate off the primary). 409 for the primary.", nil, deployBody("deliveries", oapi{"deployment": str(""), "on": boolean()}, "deployment", "on"), "{state}"},
		{"POST", "/deployments/always-on", "Deployments", "Turn a deployment's alwaysOn on or off", capDeployManager,
			"409 for the primary, and when its code doesn't declare alwaysOn.", nil, deployBody("always-on", oapi{"deployment": str(""), "on": boolean()}, "deployment", "on"), "{state}"},
		{"POST", "/deployments/limits", "Deployments", "Lower a deployment's resource limits", capDeployManager,
			"limits {memMiB?, pids?, diskGiB?}: each override lowers the tile's ceiling for this deployment; null removes it, an absent key stays. memMiB and pids apply from its next generation; diskGiB is its data's quota, set on the scope's root tile. 400 above the ceiling.", nil, deployBody("limits", oapi{"deployment": str(""), "limits": oapi{"type": "object", "description": "memMiB, pids, diskGiB: a positive integer, or null"}}, "deployment", "limits"), "{state}"},
		{"POST", "/deployments/seed", "Deployments", "Seed a deployment's data from the primary's", capDeployManager,
			"Fills its data from the primary's, consistently; stop:true takes a stopped point-in-time copy. Stops every deployment using that data, sibling tiles' included, and needs the level on every tile claiming it. Completion is a deployments event.", nil, deployBody("seed", oapi{"deployment": str(""), "confirm": str("\"copy-data\""), "stop": boolean()}, "deployment", "confirm"), "{state}"},
		{"POST", "/deployments/reset", "Deployments", "Empty a deployment's data", capDeployTerminal,
			"Empties its data; vault:true also empties its vault back to key names. Resetting main is a tile manager's act, and every tile claiming the data needs the level. 409 for the primary.", nil, deployBody("reset", oapi{"deployment": str(""), "confirm": str("\"erase-data\""), "vault": boolean()}, "deployment", "confirm"), "{state}"},
		{"POST", "/deployments/vault-copy", "Deployments", "Copy vault values from the primary", capDeployManager,
			"keys, or all:true (exactly one): the primary's values overwrite the deployment's, never the other way; the audit names keys, never values. 409 for the primary.", nil, deployBody("vault copy", oapi{"deployment": str(""), "keys": arr(), "all": boolean()}, "deployment"), "{state, copied, missing}"},
		{"POST", "/deployments/backup", "Deployments", "Back up a deployment's data now", capDeployAdmin,
			"Archives its data through the tile's bound @archive provider, under its own archive key; POST /backup is unchanged. 502 with no archiver bound.", nil, deployBody("backup", oapi{"deployment": str("")}, "deployment"), "{ok, deployment, version}"},
		{"GET", "/deployments/backups", "Deployments", "A deployment's archived versions", capDeployAdmin,
			"Empty versions without an archiver, as GET /backups. deployment defaults to the primary; a removed deployment's archives are still listed.", []oapi{queryParam("tile", "a tile's path; a query names a deployment with deployment=, never as tile+name (400: a '+' in a query string reads as a space)", true), queryParam("deployment", "whose archive (default: the primary)", false)}, nil, "{deployment, versions:[{version, time, size}], archiver}"},
		{"POST", "/deployments/restore", "Deployments", "Restore a deployment's data", capDeployAdmin,
			"Data only, never the work tree (POST /restore is unchanged): deployment's archive (main: the data part of the tile's), version (default latest), into a target (default the archive's own). Stops every deployment using the target's data; also needs the reset level on every tile claiming it.", nil,
			deployBody("restore", oapi{"deployment": str("whose archive"), "version": str("default latest"), "into": str("the target deployment"), "replace": boolean(), "confirm": str("\"erase-data\" when the target has data")}, "deployment"), "{ok, deployment, into, restored, skipped}"},
		{"POST", "/deployments/backup-schedule", "Deployments", "Schedule a deployment's backups", capDeployAdmin,
			"A non-primary deployment's data on a schedule; \"\" removes it.", nil, deployBody("schedule", oapi{"deployment": str(""), "schedule": str("5-field cron, or \"\""), "retention": oapi{"type": "integer", "description": "versions kept (default 3)"}}, "deployment", "schedule"), "{state}"},
		{"POST", "/deployments/run-now", "Deployments", "Run a deployment's cron job now", capDeployTerminal,
			"Delivers job once to deployment as xbin/cron, dormant or not, and waits for the handler (at most 2 min; one run per job at a time). 409 for the primary, whose jobs fire on schedule.", nil, deployBody("run now", oapi{"deployment": str(""), "job": str("")}, "deployment", "job"), "{state, delivery:{status, ms}}"},
		{"POST", "/deployments/purge", "Deployments", "Purge a checkpoint", capDeployManager,
			"Removes checkpoint from every deploy log of the tile (those entries then name no checkpoint) and prunes its objects, its git view and its materialized tree at once; archives made earlier keep it. Accepted on a tile without deployments, whose store outlives the opt-out. 409 while any deployment runs it (the record points at it, a deploy of it hasn't finished, a running generation binds it); 404 for a checkpoint the tile doesn't have.", nil,
			deployBody("purge", oapi{"checkpoint": str("c:<id>")}, "checkpoint"), "{state, purged, entries}"},
		{"POST", "/deployments/branch", "Deployments", "Assign a deployment a branch, or clear it", capDeployTerminal,
			"branches/1. The work tree's branch the deployment requires: attach, resume, reload now, a deploy of the work tree and an add from it refuse a work tree on another branch (409 naming both) unless confirm:\"other-branch\", and a save on another branch while live reload follows it deploys nothing and pauses live reload (a deployments event op branch). null clears it. 409 for main and the primary.", nil,
			deployBody("branch", oapi{"deployment": str(""), "branch": oapi{"type": []string{"string", "null"}, "description": "a branch name, or null to clear"}}, "deployment", "branch"), "{state}"},
		{"GET", "/checkpoints/{tile}.git/{path}", "Deployments", "A tile's deployed checkpoints over git (dumb HTTP)", capDeployFetch,
			"git fetch from the tile's view repository: refs/heads/deploy/<name> for each pinned deployment (its checkpoint's git view) and HEAD naming the primary's, nothing else. Read-only: HEAD, info/refs, objects/info/packs, packs and loose objects; any other path 404. Frame and instance tokens and code: grants are refused.", []oapi{pathParam("tile", "tile path"), pathParam("path", "git path")}, nil, "the git file"},

		// --- vault ---
		{"GET", "/vault-status", "Vault", "Barrier status", "admin", "mode: unsealed | sealed | unconfigured | plaintext.", nil, nil, "{initialized, sealed, mode, insecure}"},
		{"POST", "/vault-unseal", "Vault", "Unseal / initialize", "admin", "Unseals with a passphrase, or initializes the barrier on first use (created:true).", nil, jsonBody("passphrase", oapi{"passphrase": str("")}, "passphrase"), "{created}"},
		{"POST", "/vault-seal", "Vault", "Seal", "admin", "Drops the key from memory; vault get/set then 503 until unsealed.", nil, nil, "ok"},
		{"POST", "/vault-rekey", "Vault", "Change the passphrase", "admin", "Re-wraps the data key under a new passphrase (no data re-encryption). Requires the barrier unsealed and the current passphrase.", nil, jsonBody("passphrases", oapi{"current": str(""), "new": str("")}, "new"), "{rekeyed}"},
		{"GET", "/vault/{component}", "Vault", "Vault key names", "self or admin", "A non-primary deployment's list adds placeholders: the primary's key names it has no value for (reading one answers 404 until a tile manager copies it, POST /deployments/vault-copy).", []oapi{pathParam("component", "component path"), deploymentQuery("a deployment of the tile: admins, and tile managers in their own session (one who isn't an admin reaches non-primary vaults only); a tile's own credentials act on their bound deployment's (naming another: 403); echoed as deployment")}, nil, "{keys:[…], placeholders?, deployment?}"},
		{"GET", "/vault/{component}/{key}", "Vault", "Read a secret", "self or admin", "", []oapi{pathParam("component", "component path"), pathParam("key", "secret name"), deploymentQuery("a deployment of the tile: admins, and tile managers in their own session (one who isn't an admin reaches non-primary vaults only); a tile's own credentials act on their bound deployment's (naming another: 403); echoed as deployment")}, nil, "{value}"},
		{"PUT", "/vault/{component}/{key}", "Vault", "Write a secret", "self or admin", "", []oapi{pathParam("component", "component path"), pathParam("key", "secret name"), deploymentQuery("a deployment of the tile: admins, and tile managers in their own session (one who isn't an admin reaches non-primary vaults only); a tile's own credentials act on their bound deployment's (naming another: 403); echoed as deployment")}, jsonBody("secret", oapi{"value": str("")}, "value"), "ok"},
		{"DELETE", "/vault/{component}/{key}", "Vault", "Delete a secret", "self or admin", "", []oapi{pathParam("component", "component path"), pathParam("key", "secret name"), deploymentQuery("a deployment of the tile: admins, and tile managers in their own session (one who isn't an admin reaches non-primary vaults only); a tile's own credentials act on their bound deployment's (naming another: 403); echoed as deployment")}, nil, "ok"},

		// --- resources: kv / blob / bus / cron ---
		{"GET", "/kv/{resource}/{key}", "Resources", "KV read / list", "reader (resource grant)",
			"With an empty key (trailing slash) lists keys (optional ?prefix=); otherwise returns the raw value bytes. resource is res:<scope>/<name>.", []oapi{pathParam("resource", "res:<scope>/<name>"), pathParam("key", "key (empty = list)"), queryParam("prefix", "key prefix (list mode)", false)}, nil, "{keys} | raw bytes"},
		{"PUT", "/kv/{resource}/{key}", "Resources", "KV write", "writer (resource grant)", "Body is the value (≤ 1 MiB).", []oapi{pathParam("resource", "res:<scope>/<name>"), pathParam("key", "key")}, freeBody("value bytes"), "ok"},
		{"DELETE", "/kv/{resource}/{key}", "Resources", "KV delete", "writer (resource grant)", "", []oapi{pathParam("resource", "res:<scope>/<name>"), pathParam("key", "key")}, nil, "ok"},
		{"GET", "/blob/{resource}/{path}", "Resources", "Blob read / list", "reader (resource grant)", "File bytes, or {entries} for a directory path.", []oapi{pathParam("resource", "res:<scope>/<name>"), pathParam("path", "path within the blob store")}, nil, "file bytes | {entries}"},
		{"PUT", "/blob/{resource}/{path}", "Resources", "Blob write", "writer (resource grant)", "Body is the file content (≤ 256 MiB).", []oapi{pathParam("resource", "res:<scope>/<name>"), pathParam("path", "path")}, freeBody("file content"), "ok"},
		{"DELETE", "/blob/{resource}/{path}", "Resources", "Blob delete", "writer (resource grant)", "", []oapi{pathParam("resource", "res:<scope>/<name>"), pathParam("path", "path")}, nil, "ok"},
		{"GET", "/tile-report", "Resources", "Component status snapshot", "any signed-in user", "Status a component reported about itself, read-filtered to what you can see — {statuses:{<component>:{level,message,ts}}}. Powers the shell's sidebar/tab health indicators. (Distinct from /tile-status, which is xbind-observed runtime metrics.)", nil, nil, "{statuses}"},
		{"POST", "/tile-report", "Resources", "Report component status / notify", "element (self) or owner", "A component reports its own condition (level ok|info|warn|error; ok+empty message clears it). transient=true fires a one-shot notification instead of setting status. Publishes a `status` event. See workspace AGENTS.md. From a non-primary deployment's credentials the status is stored for that deployment and published as a deployments event, never status.", nil,
			jsonBody("status report", oapi{"level": str("ok|info|warn|error"), "message": str("short human text"), "transient": str("optional; one-shot toast")}, "level"), "{ok}"},
		{"POST", "/bus/publish", "Resources", "Publish to a bus", "writer (resource grant)", "Delivers to owner + reader-granted elements over /ws/events.", nil,
			jsonBody("message", oapi{"resource": str("res:<scope>/<name>"), "topic": str(""), "data": freeSchema("any JSON")}, "resource", "topic"), "ok"},
		{"GET", "/bus/subscriptions", "Resources", "List bus push subscriptions", "authenticated", "Own subscriptions (admin: all), with delivered/dropped/failed counters since the daemon started. A tile credential lists its own deployment's; rows carry deployment (absent for main), dormant (absent while active) and dormantEvents.", []oapi{deploymentQuery("admin: one deployment's, echoed as deployment")}, nil, "{subscriptions}"},
		{"PUT", "/bus/subscriptions", "Resources", "Subscribe a backend to a bus", "reader (resource grant, the subscriber's)", "xbind POSTs each event whose topic starts with prefix to the component's path as From: xbin/bus, body {id, subscription, resource, topic, data, ts}; lazily starts the backend. At-most-once: 256-event queue, one in flight, no retries, 100/s loop guard. Idempotent by name, ≤64 per component. `component` is owner-only; elements always subscribe themselves. A tile credential subscribes its own deployment: from a non-primary deployment the subscription is its own (≤64 per deployment), dormant while its deliveries are off, and the answer gains dormant:true.", []oapi{deploymentQuery("admin: subscribe that deployment (echoed); a tile credential naming another deployment: 403")},
			jsonBody("subscription", oapi{"name": str("[A-Za-z0-9._-]{1,64}"), "resource": str("res:<scope>/<name>"), "prefix": str("optional topic prefix"), "path": str("/on-event"), "role": str("optional, default writer"), "component": str("owner-only")}, "name", "resource", "path"), "ok"},
		{"DELETE", "/bus/subscriptions/{name}", "Resources", "Delete a bus push subscription", "authenticated", "Element: own; admin: any (via ?component=).", []oapi{pathParam("name", "subscription name"), queryParam("component", "owner-only: whose subscription", false), deploymentQuery("admin: that deployment's")}, nil, "ok"},
		{"GET", "/cron/jobs", "Resources", "List cron jobs", "authenticated", "Own jobs; admin sees all. A tile credential lists its own deployment's; rows carry deployment (absent for main) and dormant (absent while active).", []oapi{deploymentQuery("admin: one deployment's, echoed as deployment")}, nil, "{jobs}"},
		{"PUT", "/cron/jobs", "Resources", "Register a cron job", "writer (resource grant)", "Registers a schedule that calls back into a component. `component` is owner-only; elements always schedule themselves. A tile credential schedules for its own deployment: from a non-primary deployment the job is its own, dormant while its deliveries are off, and the answer gains dormant:true.", []oapi{deploymentQuery("admin: schedule for that deployment (echoed); a tile credential naming another deployment: 403")},
			jsonBody("job", oapi{"name": str(""), "resource": str("res:<scope>/<name>"), "schedule": str("@every 1m | 5-field cron"), "path": str("/tick"), "role": str("optional"), "component": str("owner-only")}, "name", "resource", "schedule", "path"), "ok"},
		{"DELETE", "/cron/jobs/{name}", "Resources", "Delete a cron job", "authenticated", "Element: own jobs; admin: any (via ?component=).", []oapi{pathParam("name", "job name"), queryParam("component", "owner-only: whose job", false), deploymentQuery("admin: that deployment's")}, nil, "ok"},
	}, append(append(append(append(sandboxEndpoints(), partitionEndpoints()...), partitionModeEndpoints()...), partitionConsentEndpoints()...), personalBindEndpoints()...)...)
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
				"bearerAuth": oapi{"type": "http", "scheme": "bearer", "description": "Owner, element instance or terminal token — or the native app's human session (device / app sign-in)."},
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
	if strings.HasPrefix(e.desc, reserved) {
		op["x-xbin-reserved"] = true
		op["responses"].(oapi)["501"] = errResp("reserved: not built in this xbind yet")
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

// deprecatedArr is a string-array field kept only for compatibility.
func deprecatedArr(desc string) oapi {
	return oapi{"type": "array", "items": oapi{"type": "string"}, "deprecated": true, "description": desc}
}

func pathParam(name, desc string) oapi {
	return oapi{"name": name, "in": "path", "required": true, "schema": oapi{"type": "string"}, "description": desc}
}
func queryParam(name, desc string, required bool) oapi {
	return oapi{"name": name, "in": "query", "required": required, "schema": oapi{"type": "string"}, "description": desc}
}

// deploymentParam is the ?deployment= an existing route gains for tile
// deployments, declared before anything reads it. An xbind that doesn't know
// it answers for the primary, so one that does echoes it (a deployment field,
// or the X-XBin-Deployment header on a non-JSON answer). No row uses it
// since M2 built every one; it stays for a later rung's reserved parameters.
func deploymentParam(desc string) oapi {
	p := queryParam("deployment", "Reserved (tile deployments): "+desc, false)
	p["x-xbin-reserved"] = true
	return p
}

// deploymentQuery is such a ?deployment= once this xbind reads it: optional,
// and no longer marked reserved (14-implementation §4.3 step 5).
func deploymentQuery(desc string) oapi { return queryParam("deployment", desc, false) }

// deployBody is a /deployments POST body: every one names the tile, as a
// tile ref, and takes seq and dryRun.
func deployBody(desc string, props oapi, required ...string) oapi {
	props["tile"] = str("tile ref: apps/crm, or apps/crm+dev for one deployment")
	props["seq"] = oapi{"type": "integer", "description": "the record's seq the caller acted on: 409 when it moved (required onto a protected primary)"}
	props["dryRun"] = oapi{"type": "boolean", "description": "judge the request as for real and change nothing → {state, impact}"}
	return jsonBody(desc, props, append([]string{"tile"}, required...)...)
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
