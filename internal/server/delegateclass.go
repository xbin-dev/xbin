package server

// delegateclass.go — the delegation cap (docs/auth.md §Delegation; D228):
// xbind's API is default-deny for a delegate, a person's workspace agent
// acting as them (auth/delegate.go), as deployclass.go is for non-primary
// deployments and partitionclass.go for a person's partition. A delegate is
// the person's principal, so a handler it reached would judge it as the
// person — their tile levels, their sessions, their org admin rights. So
// the gate (delegate.go) lets a delegate reach a route only when this
// table says so, and the table names EVERY route xbind mounts (the guard,
// internal/apicheck's TestDelegateRouteClasses: a new route fails the build
// until someone decides its row):
//
//   - DelegateRead: a read at the person's level. Rows marked audit are
//     written to the person's audit log (code, logs, a session's log and
//     diff); the listing reads a UI makes all day are not;
//   - DelegateAct: a side effect the person could cause themselves. Always
//     audited, counted by the gate's rate limit, and described without
//     running by a dry run. Rows marked ticket pass only with the person's
//     approval (delegate.go: a one-shot ticket, or their preference);
//   - DelegateAdminRead: a read of what admin change sets cover, open only
//     while the person is an admin who opted in (delegation.State.
//     AdminChanges), answered as the person — an admin's view, read-only;
//   - DelegateNever: never delegated — the plan's never-list, with the
//     reason the refusal gives (TestDelegateNeverList pins it);
//   - DelegateRefused: not delegated — nothing a workspace agent does needs
//     it, with the reason.
//
// A route without a row is refused too: a table edit can't open anything
// by omission.
//
// /api/<tile>/… is not a row: every tile API call is a delegated class of
// its own, decided by the broker's Route (broker/delegation.go).
//
// A row's tile function names the tile a request is about, so the gate can
// ask the tile's org's agent rules (delegation.Policy.TileAllows) before
// the handler runs, and write the tile into the audit row.
//
// Some routes mix classes — one pattern, several acts — and a table of
// patterns can't split them. Each is narrowed where the act is known:
// POST /term/sessions by term.Manager.delegateOpen (the mode decides whether
// the session asks its person); the session routes that drive by
// delegateDrives (a delegate drives only the sessions it opened);
// POST /templates/new by the person's own creation rule. Listings are
// filtered row by row in their handlers (delegateHides). Routes whose
// handler isn't converted yet are in delegateUnconverted and refused.

import (
	"net/http"
	"reflect"
	"runtime"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/term"
)

// DelegateClass is a /api/xbin/* route's class for a delegate.
type DelegateClass uint8

const (
	// DelegateUnlisted: no row. Refused.
	DelegateUnlisted DelegateClass = iota
	// DelegateRead: a read at the person's level.
	DelegateRead
	// DelegateAct: a side effect at the person's level.
	DelegateAct
	// DelegateAdminRead: a read of what change sets cover, for an admin who
	// opted in.
	DelegateAdminRead
	// DelegateNever: never delegated; the row carries the reason.
	DelegateNever
	// DelegateRefused: not delegated; the row carries the reason.
	DelegateRefused
)

func (c DelegateClass) String() string {
	switch c {
	case DelegateRead:
		return "read"
	case DelegateAct:
		return "act"
	case DelegateAdminRead:
		return "admin-read"
	case DelegateNever:
		return "never"
	case DelegateRefused:
		return "refused"
	}
	return "unlisted"
}

// Allowed reports a class a delegate may reach.
func (c DelegateClass) Allowed() bool {
	return c == DelegateRead || c == DelegateAct || c == DelegateAdminRead
}

type delegateRow struct {
	class  DelegateClass
	why    string // DelegateNever, DelegateRefused: the refusal
	ticket bool   // DelegateAct: passes only with the person's approval
	audit  bool   // DelegateRead: written to the audit log
	// tile names the tile the request is about ("" none, or unknown): rest
	// is the path below /api/xbin/.
	tile func(s *Server, p auth.Principal, r *http.Request, rest string) string
	// judge is the handler's own narrowing of a DelegateAct, for a dry run
	// (delegatedry.go).
	judge dryJudge
}

func dRead() delegateRow                    { return delegateRow{class: DelegateRead} }
func dAct() delegateRow                     { return delegateRow{class: DelegateAct} }
func dAdminRead() delegateRow               { return delegateRow{class: DelegateAdminRead, audit: true} }
func dNever(why string) delegateRow         { return delegateRow{class: DelegateNever, why: why} }
func dNo(why string) delegateRow            { return delegateRow{class: DelegateRefused, why: why} }
func (r delegateRow) logged() delegateRow   { r.audit = true; return r }
func (r delegateRow) ticketed() delegateRow { r.ticket = true; return r }
func (r delegateRow) on(f func(*Server, auth.Principal, *http.Request, string) string) delegateRow {
	r.tile = f
	return r
}

// ---- which tile a request is about ----

// byComponent: ?component=<tile>.
func byComponent(_ *Server, _ auth.Principal, r *http.Request, _ string) string {
	return strings.Trim(r.URL.Query().Get("component"), "/")
}

// byComponentPath: /components/<tile>.
func byComponentPath(_ *Server, _ auth.Principal, _ *http.Request, rest string) string {
	t, _ := strings.CutPrefix(rest, "components/")
	return strings.Trim(t, "/")
}

// bySession: /term/sessions/<id>…: the tile the live session runs on.
func bySession(s *Server, _ auth.Principal, _ *http.Request, rest string) string {
	seg := strings.Split(rest, "/")
	if len(seg) < 3 || s.Term == nil {
		return ""
	}
	info, _ := s.Term.Info(seg[2])
	return info.Cwd
}

// byHistory: /agent/history/<id>…: the tile the past session ran on.
func byHistory(s *Server, p auth.Principal, _ *http.Request, rest string) string {
	seg := strings.Split(rest, "/")
	if len(seg) < 3 || s.Term == nil {
		return ""
	}
	meta, err := s.Term.HistoryMeta(term.HistoryOf(p), seg[2])
	if err != nil {
		return ""
	}
	return meta.Cwd
}

// The never-list's reasons (docs/auth.md §Delegation).
const (
	neverAnswer     = "a tile agent's permission requests and questions are answered by the person, never by their agent"
	neverSettings   = "a tile agent's mode and settings decide what it asks its person: they are the person's to change"
	neverDelete     = "deleting, disabling or offloading is the person's act"
	neverCode       = "an agent doesn't write code or decide pull requests: it asks a tile agent, whose changes its person reviews"
	neverAccount    = "passwords, devices, enrolment and sign-in tickets are the person's own"
	neverIdentity   = "a credential for another identity (a tile's frame, a path ticket, view-as) is never minted for an agent"
	neverConsent    = "a person's consents, access requests and partition decisions are their own acts"
	neverAdmin      = "the admin API is never delegated: an admin's agent proposes a change set, which the person approves"
	neverData       = "a tile's stores and registrations are the tile's: an agent reads and writes a tile's data through the tile's API"
	neverApprove    = "approving, pausing and the agent's own log are the person's: their agent asks, they answer"
	neverAgentRules = "what a person's agent may do without asking is the person's to set, never the agent's"
)

// Why the rest isn't delegated.
const (
	noPrefs       = "interface preferences are the person's browsers' and apps'"
	noSandboxes   = "tile sandboxes are driven by the tile that owns them"
	noDeployments = "a tile's deployments are the work of the people on it"
	noNotYet      = "not delegated: nothing a workspace agent does needs it"
	noAgentRules  = "the agent rules and the connection view are read by the person and applied by xbind: an agent's listings already leave out what the rules keep it from"
	noLLM         = "a component calls the LLM connector as itself, through its llm binding: never as a person"
	neverLLMKey   = "an LLM key is asked for by a tile's backend with its own credential: never in a person's name"
)

// DelegateClassOf is the class of the route RegisterAPI mounted as pattern.
func DelegateClassOf(pattern string) DelegateClass { return delegateClasses[pattern].class }

// DelegateClasses returns a copy of the table, pattern → class.
func DelegateClasses() map[string]DelegateClass {
	out := make(map[string]DelegateClass, len(delegateClasses))
	for p, r := range delegateClasses {
		out[p] = r.class
	}
	return out
}

// DelegateRowFacts is the rest of a row, for the guard that pins the table
// (internal/apicheck): whether a read is written to the audit log, and
// the name of the function that derives the tile a request is about ("":
// none — the gate asks no tile's rules for the route).
func DelegateRowFacts(pattern string) (audit bool, tileBy string) {
	row := delegateClasses[pattern]
	if row.tile != nil {
		name := runtime.FuncForPC(reflect.ValueOf(row.tile).Pointer()).Name()
		tileBy = name[strings.LastIndexByte(name, '.')+1:]
	}
	return row.audit, tileBy
}

// DelegateTicketed reports whether a delegate's call to pattern needs the
// person's approval.
func DelegateTicketed(pattern string) bool { return delegateClasses[pattern].ticket }

// DelegateUnconverted returns a copy of delegateUnconverted.
func DelegateUnconverted() map[string]string {
	out := make(map[string]string, len(delegateUnconverted))
	for p, why := range delegateUnconverted {
		out[p] = why
	}
	return out
}

// delegateUnconverted names delegated routes whose handler doesn't answer a
// delegate correctly yet, and what it lacks: refused until the change that
// converts the handler removes the row. Empty now, as partitionUnconverted
// is: a route goes here when it is classed before its handler is ready.
//
// A LISTING is converted by dropping, in its handler, each row whose tile
// Server.DelegateHides(p, tile) reports — the ONE place the orgs' agent
// rules are asked for listings (it asks the installed delegation.Policy:
// the broker's DelegationPolicy over internal/agentpolicy). /components,
// /tile-report, /term/sessions, /agent/history and the status listings do,
// and so do the broker's GET /alerts and GET /screens (its delegatelist.go).
var delegateUnconverted = map[string]string{}

// delegateClasses is the table.
var delegateClasses = map[string]delegateRow{
	// ---- reads the UI makes: who am I, what exists, how it is doing.
	// Listings are filtered row by row (delegateHides) ----
	"GET /whoami": dRead(),
	// Previews (D234). Refused until the workspace agent's preview tool is
	// decided: a delegate previews AS ITS PERSON, and the one place that
	// says who a preview renders as is previewPerson (preview.go) — the
	// delegate's case goes there, with these rows, in the same change.
	"POST /preview":             dAct().judged(dryPreview), // as its person: one tile, in the session's scope and the orgs' rules; rate-limited as an act, no ticket (preview.go previewDelegate)
	"GET /preview":              dRead(),                   // whether previews are available, and the limits
	"GET /components":           dRead(),                   // the tiles the person can read
	"GET /components/{path...}": dRead().on(byComponentPath),
	"GET /screens":              dRead(), // its tiles and folder items, filtered (the broker's delegatelist.go)
	"GET /tile-status":          dRead().on(byComponent),
	"GET /tile-report":          dRead(),                          // the statuses of tiles the person can read
	"GET /alerts":               dRead(),                          // the workspace's, and the tiles' the agent is let into
	"GET /logs":                 dRead().on(byComponent).logged(), // terminal level on the tile, as for the person
	"GET /templates":            dRead(),                          // what a tile can be created from
	"GET /agent/providers":      dRead(),

	// ---- the session directory and agent history: the person's own ----
	"GET /term/sessions":             dRead(),
	"GET /term/sessions/{id}":        dRead().on(bySession),
	"GET /term/sessions/{id}/events": dRead().on(bySession),
	"GET /term/sessions/{id}/log":    dRead().on(bySession).logged(),
	"GET /term/sessions/{id}/diff":   dRead().on(bySession).logged(), // code: where the person may read the tile's
	"GET /agent/history":             dRead(),
	"GET /agent/history/{id}/events": dRead().on(byHistory),

	// ---- code reads, for the research sub-agent: read on the tile ----
	"GET /code/tree": dRead().on(byComponent).logged(),
	"GET /code/file": dRead().on(byComponent).logged(),

	// ---- acts: tile agent sessions and tile creation. A start and a
	// create need the person's approval; driving is for sessions the
	// delegate opened (delegateDrives) ----
	"POST /term/sessions":                    dAct().ticketed().judged(dryStart), // or the person's preference; the tile is the body's cwd
	"POST /term/sessions/{id}/prompt":        dAct().on(bySession).judged(drySend),
	"POST /term/sessions/{id}/queue":         dAct().on(bySession).judged(drySend),
	"DELETE /term/sessions/{id}/queue":       dAct().on(bySession).judged(dryDrive),
	"DELETE /term/sessions/{id}/queue/{qid}": dAct().on(bySession).judged(dryDrive),
	"POST /term/sessions/{id}/cancel":        dAct().on(bySession).judged(dryDrive),
	"DELETE /term/sessions/{id}":             dAct().on(bySession).judged(dryDrive), // only a session it opened
	"POST /templates/new":                    dAct().ticketed().judged(dryCreate),   // where the person may create

	// ---- its own asking: approval requests and change-set proposals ----
	"POST /delegate/requests":        dAct(),
	"GET /delegate/requests/{id}":    dRead(),
	"DELETE /delegate/requests/{id}": dAct(),
	"GET /delegate/state":            dRead(),
	// agent sessions (handles): the conversation's scope and model, in
	// xbind's record; its connection view (blocked tiles as counts); the
	// workspace map; the docs (delegatesessions.go, docsapi.go)
	"POST /delegate/sessions":                dAct(),
	"GET /delegate/sessions/{id}":            dRead(),
	"PATCH /delegate/sessions/{id}":          dAct(),
	"DELETE /delegate/sessions/{id}":         dAct(),
	"GET /delegate/sessions/{id}/connection": dRead(),
	"GET /delegate/map":                      dRead(),
	// what a start would resolve to on a tile; whether the session's model
	// satisfies some orgs' rules; taint for what the agent took in elsewhere
	"GET /delegate/start-defaults":        dRead().on(byTileQuery),
	"POST /delegate/sessions/{id}/allows": dAct(),
	"POST /delegate/sessions/{id}/taint":  dAct(),
	"GET /docs/search":                    dRead(),
	"GET /docs/page":                      dRead(),
	"GET /delegate/approvals":             dRead(),
	// the web (D239, delegateweb.go): one page read by xbind for the
	// conversation, under the workspace's policy, the orgs' rules and the
	// person's setting; a conversation holding any org's data needs a
	// ticket for each URL — the handler asks, as the web's reach decides
	"POST /delegate/web/fetch": dAct().judged(dryWeb),
	// MCP tiles (D239, delegatemcp.go): one tool's call, xbind the client,
	// as the person; a ticket per call unless a standing approval of that
	// tool, or the person's "read-only tools don't ask" for a tool the
	// server marks read-only, covers it — the handler asks
	"POST /delegate/mcp/call":          dAct().judged(dryMCP),
	"POST /delegate/changesets":        dAct(),
	"POST /delegate/plans":             dAct(), // a plan: a change set that may create tiles and start tile agents (plan.go)
	"GET /delegate/changesets":         dRead(),
	"GET /delegate/changesets/{id}":    dRead(),
	"DELETE /delegate/changesets/{id}": dAct(),

	// ---- never: answering itself ----
	"GET /delegate/requests":                 dNever(neverApprove),
	"POST /delegate/requests/{id}/approve":   dNever(neverApprove),
	"POST /delegate/requests/{id}/deny":      dNever(neverApprove),
	"POST /delegate/approvals":               dNever(neverApprove),
	"DELETE /delegate/approvals/{id}":        dNever(neverApprove),
	"PUT /delegate/state":                    dNever(neverApprove),
	"GET /delegate/workspace":                dNever(neverApprove),
	"PUT /delegate/workspace":                dNever(neverApprove),
	"POST /delegate/page/dismiss":            dNever(neverApprove),
	"GET /delegate/audit":                    dNever(neverApprove),
	"POST /delegate/changesets/{id}/approve": dNever(neverApprove),
	"POST /delegate/changesets/{id}/ask":     dNever(neverApprove), // asking an admin is the person's own act
	"POST /delegate/changesets/{id}/reject":  dNever(neverApprove),
	"POST /delegate/changesets/{id}/undo":    dNever(neverApprove),

	// ---- never: answering a tile agent, or deciding what it asks ----
	"POST /term/sessions/{id}/permissions/{pid}":  dNever(neverAnswer),
	"POST /term/sessions/{id}/elicitations/{eid}": dNever(neverAnswer),
	"POST /term/sessions/{id}/options":            dNever(neverSettings),
	"POST /term/sessions/{id}/restart":            dNever(neverSettings),
	"PATCH /term/sessions/{id}":                   dNever(neverSettings),
	"DELETE /agent/history/{id}":                  dNever(neverDelete),

	// ---- never: a tile's life, code writes ----
	"POST /lifecycle":       dNever(neverDelete),
	"POST /create":          dNever(neverCode),
	"POST /clone":           dNever(neverCode),
	"POST /git/import":      dNever(neverCode),
	"POST /builtins/import": dNever(neverCode),
	"POST /builtins/update": dNever(neverCode),
	"POST /code/prs":        dNever(neverCode),
	"POST /code/pr/comment": dNever(neverCode),
	"POST /code/pr/state":   dNever(neverCode),

	// ---- never: the person's account, devices, sign-in ----
	"POST /login":                        dNever(neverAccount),
	"POST /invite/redeem":                dNever(neverAccount),
	"POST /account/password":             dNever(neverAccount),
	"GET /first-run":                     dNever(neverAccount),
	"POST /first-run/session":            dNever(neverAccount),
	"POST /devices/enroll":               dNever(neverAccount),
	"POST /devices/enroll-code":          dNever(neverAccount),
	"DELETE /devices/{id}":               dNever(neverAccount),
	"POST /devices/pairing/{id}/confirm": dNever(neverAccount),
	"POST /devices/{id}/tunnel":          dNever(neverAccount),
	"POST /devices/push":                 dNever(neverAccount),
	"POST /web-ticket":                   dNever(neverAccount),

	// ---- never: other identities ----
	"GET /frame-token":       dNever(neverIdentity),
	"POST /path-tickets":     dNever(neverIdentity),
	"POST /impersonate":      dNever(neverIdentity),
	"POST /impersonate/stop": dNever(neverIdentity),

	// ---- never: the person's own decisions about their data ----
	"GET /partitions/consents":            dNever(neverConsent),
	"POST /partitions/consents":           dNever(neverConsent),
	"DELETE /partitions/consents":         dNever(neverConsent),
	"POST /partitions/binds":              dNever(neverConsent),
	"DELETE /partitions/binds":            dNever(neverConsent),
	"POST /partitions/share-log":          dNever(neverConsent),
	"DELETE /partitions/share-log":        dNever(neverConsent),
	"POST /partitions/credential-confirm": dNever(neverConsent),

	// ---- never: a tile's own stores ----
	"GET /kv/{rest...}":       dNever(neverData),
	"PUT /kv/{rest...}":       dNever(neverData),
	"DELETE /kv/{rest...}":    dNever(neverData),
	"GET /blob/{rest...}":     dNever(neverData),
	"PUT /blob/{rest...}":     dNever(neverData),
	"DELETE /blob/{rest...}":  dNever(neverData),
	"GET /vault/{rest...}":    dNever(neverData),
	"PUT /vault/{rest...}":    dNever(neverData),
	"DELETE /vault/{rest...}": dNever(neverData),
	"POST /bus/publish":       dNever(neverData),

	// ---- never: the admin API (the first change-set kinds, and the areas
	// no change set may ever touch) ----
	"POST /grants":              dNever(neverAdmin),
	"DELETE /grants":            dNever(neverAdmin),
	"POST /bindings":            dNever(neverAdmin),
	"DELETE /bindings":          dNever(neverAdmin),
	"POST /limits":              dNever(neverAdmin),
	"DELETE /limits":            dNever(neverAdmin),
	"PUT /access":               dNever(neverAdmin),
	"POST /owner":               dNever(neverAdmin),
	"POST /users":               dNever(neverAdmin),
	"PATCH /users/{id}":         dNever(neverAdmin),
	"DELETE /users/{id}":        dNever(neverAdmin),
	"POST /users/{id}/invite":   dNever(neverAdmin),
	"POST /orgs":                dNever(neverAdmin),
	"PATCH /orgs/{org}":         dNever(neverAdmin),
	"DELETE /orgs/{org}":        dNever(neverAdmin),
	"PUT /orgs/{org}/policy":    dNever(neverAdmin),
	"PUT /orgs/{org}/limits":    dNever(neverAdmin),
	"PUT /policy":               dNever(neverAdmin),
	"PATCH /auth-settings":      dNever(neverAdmin),
	"POST /auth-rotate-token":   dNever(neverAdmin),
	"POST /vault-unseal":        dNever(neverAdmin),
	"POST /vault-seal":          dNever(neverAdmin),
	"POST /vault-rekey":         dNever(neverAdmin),
	"POST /backup":              dNever(neverAdmin),
	"POST /restore":             dNever(neverAdmin),
	"PUT /workspace-settings":   dNever(neverAdmin),
	"PUT /workspace-policies":   dNever(neverAdmin),
	"POST /partitions/mode":     dNever(neverAdmin),
	"POST /partitions/purge":    dNever(neverAdmin),
	"POST /deployments/promote": dNever(neverAdmin),

	// ---- everything else xbind mounts ----
	"GET /cron/jobs":                   dNever(neverData),
	"PUT /cron/jobs":                   dNever(neverData),
	"DELETE /cron/jobs/{name}":         dNever(neverData),
	"GET /bus/subscriptions":           dNever(neverData),
	"PUT /bus/subscriptions":           dNever(neverData),
	"DELETE /bus/subscriptions/{name}": dNever(neverData),
	"PUT /iface-instances":             dNever(neverData),
	"PUT /ingress-hosts":               dNever(neverData),
	"GET /ingress-routes":              dNever(neverData),
	"POST /notify":                     dNever(neverData),
	"POST /tile-report":                dNever(neverData),
	"POST /partitions/mail":            dNever(neverData),
	"GET /partitions/mail":             dNever(neverData),
	"POST /partitions/mail/ack":        dNever(neverData),

	"GET /prefs":          dNo(noPrefs),
	"GET /prefs/{key}":    dNo(noPrefs),
	"PUT /prefs/{key}":    dNo(noPrefs),
	"DELETE /prefs/{key}": dNo(noPrefs),

	"GET /sandboxes":                                 dNo(noSandboxes),
	"DELETE /sandboxes/{name}":                       dNo(noSandboxes),
	"/sandboxes/{name}/ports/{port}/{path...}":       dNo(noSandboxes),
	"DELETE /sandboxes/{name}/execs/{id}":            dNo(noSandboxes),
	"DELETE /sandboxes/{name}/snapshots/{sid}":       dNo(noSandboxes),
	"GET /sandboxes/{name}":                          dNo(noSandboxes),
	"GET /sandboxes/{name}/execs":                    dNo(noSandboxes),
	"GET /sandboxes/{name}/execs/{id}":               dNo(noSandboxes),
	"GET /sandboxes/{name}/execs/{id}/output":        dNo(noSandboxes),
	"GET /sandboxes/{name}/execs/{id}/tty":           dNo(noSandboxes),
	"GET /sandboxes/{name}/execs/{id}/stdio":         dNo(noSandboxes),
	"GET /sandboxes/{name}/files/content":            dNo(noSandboxes),
	"GET /sandboxes/{name}/files/list":               dNo(noSandboxes),
	"GET /sandboxes/{name}/files/stat":               dNo(noSandboxes),
	"GET /sandboxes/{name}/snapshots":                dNo(noSandboxes),
	"GET /sandboxes/{name}/tar":                      dNo(noSandboxes),
	"GET /sandboxes/{name}/tty":                      dNo(noSandboxes),
	"GET /sandboxes/policy":                          dNo(noSandboxes),
	"GET /sandboxes/runtime":                         dNo(noSandboxes),
	"PATCH /sandboxes/{name}":                        dNo(noSandboxes),
	"POST /sandboxes":                                dNo(noSandboxes),
	"POST /sandboxes/copy":                           dNo(noSandboxes),
	"POST /sandboxes/{name}/execs":                   dNo(noSandboxes),
	"POST /sandboxes/{name}/execs/{id}/resize":       dNo(noSandboxes),
	"POST /sandboxes/{name}/execs/{id}/signal":       dNo(noSandboxes),
	"POST /sandboxes/{name}/execs/{id}/stdin":        dNo(noSandboxes),
	"POST /sandboxes/{name}/files/mkdir":             dNo(noSandboxes),
	"POST /sandboxes/{name}/files/move":              dNo(noSandboxes),
	"POST /sandboxes/{name}/files/remove":            dNo(noSandboxes),
	"POST /sandboxes/{name}/rebase":                  dNo(noSandboxes),
	"POST /sandboxes/{name}/reset":                   dNo(noSandboxes),
	"POST /sandboxes/{name}/run":                     dNo(noSandboxes),
	"POST /sandboxes/{name}/snapshots":               dNo(noSandboxes),
	"POST /sandboxes/{name}/snapshots/{sid}/restore": dNo(noSandboxes),
	"POST /sandboxes/{name}/start":                   dNo(noSandboxes),
	"POST /sandboxes/{name}/stop":                    dNo(noSandboxes),
	"PUT /sandboxes/{name}/files/content":            dNo(noSandboxes),
	"PUT /sandboxes/{name}/tar":                      dNo(noSandboxes),
	"PUT /sandboxes/policy":                          dNo(noSandboxes),

	"GET /deployments":                     dNo(noDeployments),
	"GET /deployments/log":                 dNo(noDeployments),
	"GET /deployments/diff":                dNo(noDeployments),
	"POST /deployments/live-reload/pause":  dNo(noDeployments),
	"POST /deployments/live-reload/resume": dNo(noDeployments),
	"POST /deployments/live-reload/now":    dNo(noDeployments),
	"POST /deployments/live-reload/attach": dNo(noDeployments),
	"POST /deployments/add":                dNo(noDeployments),
	"POST /deployments/remove":             dNo(noDeployments),
	"POST /deployments/deploy":             dNo(noDeployments),
	"POST /deployments/rollback":           dNo(noDeployments),
	"POST /deployments/primary":            dNo(noDeployments),
	"POST /deployments/protect":            dNo(noDeployments),
	"POST /deployments/edge":               dNo(noDeployments),
	"POST /deployments/deliveries":         dNo(noDeployments),
	"POST /deployments/always-on":          dNo(noDeployments),
	"POST /deployments/limits":             dNo(noDeployments),
	"POST /deployments/seed":               dNo(noDeployments),
	"POST /deployments/reset":              dNo(noDeployments),
	"POST /deployments/vault-copy":         dNo(noDeployments),
	"POST /deployments/backup":             dNo(noDeployments),
	"GET /deployments/backups":             dNo(noDeployments),
	"POST /deployments/restore":            dNo(noDeployments),
	"POST /deployments/backup-schedule":    dNo(noDeployments),
	"POST /deployments/run-now":            dNo(noDeployments),
	"POST /deployments/purge":              dNo(noDeployments),
	"POST /deployments/branch":             dNo(noDeployments),
	"GET /checkpoints/{rest...}":           dNo(noDeployments),

	"GET /git/log":                    dNo(noNotYet),
	"GET /git/diff":                   dNo(noNotYet),
	"GET /git/activity":               dNo(noNotYet),
	"GET /git/remote-info":            dNo(noNotYet),
	"GET /code/prs":                   dNo(noNotYet),
	"GET /code/prs/summary":           dNo(noNotYet),
	"GET /code/pr":                    dNo(noNotYet),
	"GET /code/pr/series":             dNo(noNotYet),
	"GET /templates/updates":          dNo(noNotYet),
	"GET /templates/{repo}/{rest...}": dNo(noNotYet),
	"GET /builtins":                   dNo(noNotYet),
	"GET /builtins/updates":           dNo(noNotYet),
	"GET /partitions/binds":           dNo(noNotYet),
	"GET /owner":                      dNo(noNotYet),
	"GET /access/{user}":              dNo(noNotYet),
	"GET /status":                     dNo(noNotYet),
	"GET /tile-assets":                dNo(noNotYet),
	"GET /openapi.json":               dNo(noNotYet),
	"GET /vm":                         dNo(noNotYet),
	"GET /term-net":                   dNo(noNotYet),
	"GET /native-runtime":             dNo(noNotYet),
	"GET /chrome":                     dNo(noNotYet),
	"GET /branding":                   dNo(noNotYet),
	"GET /workspace-policies":         dNo(noNotYet),
	"GET /workspace-settings":         dNo(noNotYet),
	"GET /login/methods":              dNo(noNotYet),
	"GET /partitions":                 dNo(noNotYet),

	"GET /grants":   dAdminRead(),
	"GET /bindings": dAdminRead(),
	"GET /orgs":     dAdminRead(),
	"GET /limits":   dAdminRead(),

	"GET /owner/preview":                     dNever(neverAdmin),
	"GET /access":                            dNever(neverAdmin),
	"GET /access-matrix":                     dNever(neverAdmin),
	"POST /access-requests/approve":          dNever(neverAdmin),
	"GET /users":                             dNever(neverAdmin),
	"DELETE /users/{id}/sessions":            dNever(neverAdmin),
	"GET /users/{id}/devices":                dNever(neverAdmin),
	"GET /devices/limits":                    dNever(neverAdmin),
	"PUT /devices/limits":                    dNever(neverAdmin),
	"GET /sessions":                          dNever(neverAdmin),
	"GET /users-directory":                   dNever(neverAdmin),
	"GET /auth-settings":                     dNever(neverAdmin),
	"POST /auth-settings/sso/test":           dNever(neverAdmin),
	"PUT /orgs/{org}/members/{user}":         dNever(neverAdmin),
	"DELETE /orgs/{org}/members/{user}":      dNever(neverAdmin),
	"PUT /orgs/{org}/sso-groups":             dNever(neverAdmin),
	"GET /orgs/{org}/policy":                 dNever(neverAdmin),
	"GET /policy":                            dNever(neverAdmin),
	"GET /defaults":                          dNever(neverAdmin),
	"PUT /defaults":                          dNever(neverAdmin),
	"GET /net-sets":                          dNever(neverAdmin),
	"PUT /net-sets/{name}":                   dNever(neverAdmin),
	"DELETE /net-sets/{name}":                dNever(neverAdmin),
	"GET /permission-sets":                   dNever(neverAdmin),
	"PUT /permission-sets/{name}":            dNever(neverAdmin),
	"DELETE /permission-sets/{name}":         dNever(neverAdmin),
	"PUT /screens/default":                   dNever(neverAdmin),
	"PUT /screens/org":                       dNever(neverAdmin),
	"DELETE /screens/org":                    dNever(neverAdmin),
	"PUT /screens/folders":                   dNever(neverAdmin),
	"GET /backups":                           dNever(neverAdmin),
	"GET /backup-schedule":                   dNever(neverAdmin),
	"POST /backup-schedule":                  dNever(neverAdmin),
	"DELETE /backup-schedule":                dNever(neverAdmin),
	"GET /backup-keys":                       dNever(neverAdmin),
	"POST /backup-keys/export":               dNever(neverAdmin),
	"POST /backup-keys/import":               dNever(neverAdmin),
	"POST /backup/erase":                     dNever(neverAdmin),
	"GET /partitions/backups":                dNever(neverAdmin),
	"POST /partitions/restore":               dNever(neverAdmin),
	"GET /vault-status":                      dNever(neverAdmin),
	"GET /vaults":                            dNever(neverAdmin),
	"GET /resources":                         dNever(neverAdmin),
	"GET /auth-overview":                     dNever(neverAdmin),
	"GET /backends":                          dNever(neverAdmin),
	"GET /runtime":                           dNever(neverAdmin),
	"GET /ingress":                           dNever(neverAdmin),
	"GET /gpus":                              dNever(neverAdmin),
	"GET /go-build-versions":                 dNever(neverAdmin),
	"POST /go-build-versions/check":          dNever(neverAdmin),
	"POST /go-build-versions/dismiss":        dNever(neverAdmin),
	"PUT /vm/policy":                         dNever(neverAdmin),
	"PUT /native-runtime":                    dNever(neverAdmin),
	"PUT /chrome":                            dNever(neverAdmin),
	"PUT /branding":                          dNever(neverAdmin),
	"POST /partitions/limits":                dNever(neverAdmin),
	"POST /limits/dismiss":                   dNever(neverAdmin),
	"GET /push/config":                       dNever(neverAdmin),
	"PUT /push/config":                       dNever(neverAdmin),
	"GET /app-tunnel":                        dNever(neverAdmin),
	"PUT /app-tunnel":                        dNever(neverAdmin),
	"POST /app-tunnel/relay-tokens":          dNever(neverAdmin),
	"POST /app-tunnel/test":                  dNever(neverAdmin),
	"DELETE /push/config":                    dNever(neverAdmin),
	"GET /push/devices":                      dNever(neverAdmin),
	"DELETE /push/devices/{user}":            dNever(neverAdmin),
	"DELETE /push/devices/{user}/{deviceId}": dNever(neverAdmin),
	"GET /partitions/edges":                  dNever(neverAdmin),
	"POST /partitions/stop":                  dNever(neverAdmin),
	"POST /partitions/reset":                 dNever(neverAdmin),
	"POST /partitions/reviewed":              dNever(neverAdmin),

	"GET /access-requests":    dNever(neverConsent),
	"POST /access-requests":   dNever(neverConsent),
	"DELETE /access-requests": dNever(neverConsent),
	"GET /partitions/ledger":  dNever(neverConsent),

	"POST /invite/check":                                   dNever(neverAccount),
	"GET /devices":                                         dNever(neverAccount),
	"GET /devices/pairing/{id}":                            dNever(neverAccount),
	"GET /app-tunnel/client":                               dNever(neverAccount),
	"GET /devices/push":                                    dNever(neverAccount),
	"DELETE /devices/push/{deviceId}":                      dNever(neverAccount),
	"POST /devices/push/activities":                        dNever(neverAccount),
	"DELETE /devices/push/{deviceId}/activities/{session}": dNever(neverAccount),
	"GET /push/prefs":                                      dNever(neverAccount),
	"PUT /push/prefs":                                      dNever(neverAccount),
	"POST /push/test":                                      dNever(neverAccount),

	// ---- the agent rules themselves (D229) and the LLM connector (D226) ----
	// What an agent may do unasked is the person's to set, and what agents
	// may do in an org its admins': never the agent's. The connection view
	// names the tiles an org keeps agents out of, with the reason — it is
	// for the person's own page; an agent's listings leave those tiles out
	// (delegateHides). A component calls the LLM connector as itself,
	// through its own llm binding, never as a person.
	"GET /agent-levels":               dNo(noAgentRules),
	"PUT /agent-levels":               dNever(neverAgentRules),
	"GET /agent/prefs":                dNo(noAgentRules),
	"PUT /agent/prefs":                dNever(neverAgentRules),
	"GET /agents/policy":              dNo(noAgentRules),
	"PUT /agents/policy":              dNever(neverAdmin),
	"PUT /orgs/{org}/agents":          dNever(neverAdmin),
	"PUT /orgs/{org}/agents/pin":      dNever(neverAdmin),
	"GET /agents/connection":          dNo(noAgentRules),
	"GET /sealed":                     dNever(neverAdmin), // system components, the agent's own among them: an admin's to look at, in their own session
	"GET /llm":                        dNo(noLLM),
	"GET /llm/config":                 dNo(noLLM),
	"PUT /llm/config":                 dNever(neverAdmin),
	"GET /llm/preferred":              dNo(noLLM),
	"GET /llm/quotas":                 dNo(noLLM),
	"PUT /llm/quotas":                 dNever(neverAdmin),
	"GET /llm/usage":                  dNo(noLLM),
	"PUT /llm/upstreams/{name}":       dNever(neverAdmin),
	"DELETE /llm/upstreams/{name}":    dNever(neverAdmin),
	"POST /llm/upstreams/{name}/test": dNever(neverAdmin),
	"POST /llm/setup/dismiss":         dNo(noLLM),
	"GET /llm/v1/models":              dNo(noLLM),
	"POST /llm/v1/chat/completions":   dNo(noLLM),
	"POST /llm/v1/responses":          dNo(noLLM),
	// LLM keys (D226): a tile's backend asks for them with its own
	// credential. A key is a credential that outlives the call that made
	// it, so no agent mints, renews or ends one in a person's name.
	"POST /llm/keys":            dNever(neverLLMKey),
	"POST /llm/keys/{id}/renew": dNever(neverLLMKey),
	"DELETE /llm/keys/{id}":     dNever(neverLLMKey),
	"DELETE /llm/keys":          dNever(neverLLMKey),
}
