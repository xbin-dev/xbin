package server

// delegateclass.go — the delegation cap (plans/wsagent/s2-delegation.md):
// xbind's API is default-deny for a delegate, a person's workspace agent
// acting as them (auth/delegate.go), as deployclass.go is for non-primary
// deployments and partitionclass.go for a person's partition. A delegate is
// the person's principal, so a handler it reached would judge it as the
// person — their tile levels, their sessions, their org admin rights. So
// the gate (delegate.go) lets a delegate reach a route only when this
// table lists it:
//
//   - DelegateRead: a read the person's UI makes, answered at their level;
//   - DelegateAct: a side effect the person could cause by clicking, which
//     the gate marks in the audit log and can describe without running (a
//     dry run);
//   - DelegateNever: named so the refusal says why, and so a later edit
//     can't quietly list it (TestDelegateNeverList);
//   - no row: refused. A new route is not delegated until someone decides.
//
// /api/<tile>/… is not a row: every tile API call is a delegated class of
// its own, decided by the broker's Route (broker/delegation.go).
//
// Some routes mix classes — one pattern, several acts — and a table of
// patterns can't split them. Each is narrowed where the act is known:
// POST /term/sessions by term.delegateOpenRefusal (the mode decides whether
// the session asks its person), POST /templates/new by the person's own
// creation rule. Routes whose handler answers a person only through their
// tile's frame are listed in delegateUnconverted and refused until the
// handler judges a delegate by its person's level.

// DelegateClass is a /api/xbin/* route's class for a delegate.
type DelegateClass uint8

const (
	// DelegateUnlisted: no row. Refused.
	DelegateUnlisted DelegateClass = iota
	// DelegateRead: a read at the person's level.
	DelegateRead
	// DelegateAct: a side effect at the person's level; audited as one.
	DelegateAct
	// DelegateNever: never delegated; the row carries the reason.
	DelegateNever
)

func (c DelegateClass) String() string {
	switch c {
	case DelegateRead:
		return "read"
	case DelegateAct:
		return "act"
	case DelegateNever:
		return "never"
	}
	return "unlisted"
}

type delegateRow struct {
	class DelegateClass
	why   string // DelegateNever: the refusal
}

func dRead() delegateRow            { return delegateRow{class: DelegateRead} }
func dAct() delegateRow             { return delegateRow{class: DelegateAct} }
func dNever(why string) delegateRow { return delegateRow{class: DelegateNever, why: why} }

// The never-list's reasons (plans/workspace-agent.md §4).
const (
	neverAnswer   = "a tile agent's permission requests and questions are answered by the person, never by their agent"
	neverSettings = "a tile agent's mode and settings decide what it asks its person: they are the person's to change"
	neverDelete   = "ending, deleting, disabling or offloading is the person's act"
	neverCode     = "an agent doesn't write code or decide pull requests: it asks a tile agent, whose changes its person reviews"
	neverAccount  = "passwords, devices, enrolment and sign-in tickets are the person's own"
	neverIdentity = "a credential for another identity (a tile's frame, a path ticket, view-as) is never minted for an agent"
	neverConsent  = "a person's consents and partition decisions are their own acts"
	neverAdmin    = "the admin API is never delegated: an admin's agent proposes a change set, which a person approves"
	neverData     = "a tile's stores are the tile's: an agent reads and writes a tile's data through the tile's API"
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

// DelegateUnconverted returns a copy of delegateUnconverted.
func DelegateUnconverted() map[string]string {
	out := make(map[string]string, len(delegateUnconverted))
	for p, why := range delegateUnconverted {
		out[p] = why
	}
	return out
}

// delegateUnconverted names listed routes whose handler doesn't answer a
// delegate at its person's level yet, and what it lacks: refused until the
// change that converts the handler removes the row.
var delegateUnconverted = map[string]string{
	"GET /tile-status": "its handler answers admins and the tile itself: it needs the person's read level on the tile",
}

// delegateClasses is the table.
var delegateClasses = map[string]delegateRow{
	// ---- reads the UI makes: who am I, what exists, how it is doing ----
	"GET /whoami":               dRead(),
	"GET /components":           dRead(), // the tiles the person can read
	"GET /components/{path...}": dRead(),
	"GET /screens":              dRead(),
	"GET /tile-status":          dRead(), // unconverted
	"GET /tile-report":          dRead(), // the statuses of tiles the person can read
	"GET /alerts":               dRead(),
	"GET /logs":                 dRead(), // terminal level on the tile, as for the person
	"GET /templates":            dRead(), // what a tile can be created from
	"GET /agent/providers":      dRead(),

	// ---- the session directory and agent history: the person's own ----
	"GET /term/sessions":             dRead(),
	"GET /term/sessions/{id}":        dRead(),
	"GET /term/sessions/{id}/events": dRead(),
	"GET /agent/history":             dRead(),
	"GET /agent/history/{id}/events": dRead(),

	// ---- code reads, for the research sub-agent: read on the tile ----
	"GET /code/tree": dRead(),
	"GET /code/file": dRead(),

	// ---- acts: tile agent sessions (create narrowed in term), tile creation ----
	"POST /term/sessions":                    dAct(),
	"POST /term/sessions/{id}/prompt":        dAct(),
	"POST /term/sessions/{id}/queue":         dAct(),
	"DELETE /term/sessions/{id}/queue":       dAct(), // taking back what it queued
	"DELETE /term/sessions/{id}/queue/{qid}": dAct(),
	"POST /term/sessions/{id}/cancel":        dAct(),
	"POST /templates/new":                    dAct(), // where the person may create

	// ---- never: answering a tile agent, or deciding what it asks ----
	"POST /term/sessions/{id}/permissions/{pid}":  dNever(neverAnswer),
	"POST /term/sessions/{id}/elicitations/{eid}": dNever(neverAnswer),
	"POST /term/sessions/{id}/options":            dNever(neverSettings),
	"POST /term/sessions/{id}/restart":            dNever(neverSettings),
	"PATCH /term/sessions/{id}":                   dNever(neverSettings),
	"DELETE /term/sessions/{id}":                  dNever(neverDelete),
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
}
