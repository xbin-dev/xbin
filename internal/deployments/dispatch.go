package deployments

// dispatch.go — the operation registry (NP-14-3). Each operation of the
// /deployments family registers the request type its route's body decodes
// into, how it reads its subject from the record, and its run; a handler
// decodes the body into NewRequest's value and calls Plane.Do, which judges
// the caller's authority (authz.go) before the operation runs. An operation
// this xbind doesn't build answers 501, as a reserved route does (NP-14-4),
// so an operation registers here without touching the handler file.

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/xbin-dev/xbin/internal/auth"
)

// Error is how the deployments plane refuses or fails a request: the HTTP
// status, the kind a Can reports a refusal under, and the text
// (docs/protocol.md, Tile deployments). A handler answers it with
// server.WriteError(w, e.Status, e.Msg, e.Docs()).
type Error struct {
	Status int
	Kind   string // KindAuthority, KindPolicy or KindState for a refusal; "" for a bad request or a failure
	Msg    string
}

// The kinds of a refusal, as a Can reports them.
const (
	KindAuthority = "authority" // who asks: level, tile manager, person's own session, protected primary, view-as
	KindPolicy    = "policy"    // what the tile or this xbind can't do
	KindState     = "state"     // not now: no record yet, live reload not paused, …
)

func (e *Error) Error() string { return e.Msg }

// Docs is the page the answer names: /docs/auth.md for a 403,
// /docs/protocol.md otherwise.
func (e *Error) Docs() string {
	if e.Status == http.StatusForbidden {
		return "/docs/auth.md"
	}
	return "/docs/protocol.md"
}

// Can is the refusal as a permission the state reports.
func (e *Error) Can() Can { return Can{Why: e.Msg, Kind: e.Kind} }

// Handler is what an operation registers; R is its route's request body.
type Handler[R any] struct {
	// Subject reads what the request acts on from the record as it stands,
	// and the Op it is judged as: the route's own, or a refinement its body
	// asks for (deploy with restart:true is OpRestart; see refinements). It
	// changes nothing. Its error is answered before authority is judged, so
	// it is for a malformed body (400) or a tile that doesn't exist (404); a
	// deployment that doesn't exist is Run's to answer, after Authorize, so
	// a caller who may not operate the tile learns nothing of its
	// deployments.
	Subject func(p *Plane, req *R) (Op, Subject, error)
	// Run does the operation the grant authorizes, a dry run included
	// (judged exactly as for real), and returns the answer the handler
	// encodes. Where it writes the record it calls p.Recheck(g, <the subject
	// now>) and commits only on nil. Its errors are *Error; any other error
	// answers 500.
	Run func(ctx context.Context, p *Plane, g Grant, req *R) (any, error)
}

// opEntry is a registered operation, its request type erased.
type opEntry struct {
	newReq  func() any
	subject func(p *Plane, req any) (Op, Subject, error)
	run     func(ctx context.Context, p *Plane, g Grant, req any) (any, error)
}

type opTable map[Op]opEntry

// ops is the plane's registry, filled from the operations' init functions:
// register(OpPause, Handler[PauseRequest]{…}).
var ops = opTable{}

// register adds an operation to the registry. It panics on an Op that
// isn't an operation route, or one registered twice: both are programming
// errors a test run catches.
func register[R any](op Op, h Handler[R]) { addOp(ops, op, h) }

func addOp[R any](t opTable, op Op, h Handler[R]) {
	if a, ok := acts[op]; !ok || !a.post {
		panic(fmt.Sprintf("deployments: %q is not an operation route", op))
	}
	if _, dup := t[op]; dup {
		panic(fmt.Sprintf("deployments: %q registered twice", op))
	}
	if h.Subject == nil || h.Run == nil {
		panic(fmt.Sprintf("deployments: %q registered without Subject or Run", op))
	}
	typed := func(req any) (*R, error) {
		r, ok := req.(*R)
		if !ok || r == nil {
			return nil, &Error{Status: http.StatusInternalServerError,
				Msg: fmt.Sprintf("deployments: %s takes a %T, not a %T", op, (*R)(nil), req)}
		}
		return r, nil
	}
	t[op] = opEntry{
		newReq: func() any { return new(R) },
		subject: func(p *Plane, req any) (Op, Subject, error) {
			r, err := typed(req)
			if err != nil {
				return "", Subject{}, err
			}
			return h.Subject(p, r)
		},
		run: func(ctx context.Context, p *Plane, g Grant, req any) (any, error) {
			r, err := typed(req)
			if err != nil {
				return nil, err
			}
			return h.Run(ctx, p, g, r)
		},
	}
}

// Registered reports whether this xbind builds op. A handler answers an
// unregistered route 501, as a reserved one.
func Registered(op Op) bool {
	_, ok := ops[op]
	return ok
}

// NewRequest returns a fresh value of op's request type, for the handler to
// decode the body into (server.DecodeJSON: strict, so an unknown field is a
// 400); false for an op this xbind doesn't build.
func NewRequest(op Op) (any, bool) {
	e, ok := ops[op]
	if !ok {
		return nil, false
	}
	return e.newReq(), true
}

// Do runs a decoded request: it reads the op's subject, judges the caller
// over it (Authorize), and runs the op with the grant. Every error it
// returns is an *Error: 501 for an op this xbind doesn't build.
func (p *Plane) Do(ctx context.Context, pr auth.Principal, op Op, req any) (any, error) {
	return ops.do(ctx, p, pr, op, req)
}

func (t opTable) do(ctx context.Context, p *Plane, pr auth.Principal, op Op, req any) (any, error) {
	e, ok := t[op]
	if !ok {
		return nil, &Error{Status: http.StatusNotImplemented,
			Msg: "reserved for tile deployments, not built in this xbind yet — POST /deployments/" + string(op)}
	}
	judged, s, err := e.subject(p, req)
	if err != nil {
		return nil, asError(err)
	}
	if judged == "" {
		judged = op
	}
	if judged != op && !refines(op, judged) {
		return nil, &Error{Status: http.StatusInternalServerError,
			Msg: fmt.Sprintf("deployments: %s can't be judged as %s", op, judged)}
	}
	g, err := p.Authorize(pr, judged, s)
	if err != nil {
		return nil, err
	}
	res, err := e.run(ctx, p, g, req)
	if err != nil {
		return nil, asError(err)
	}
	return res, nil
}

func refines(op, judged Op) bool {
	for _, r := range refinements[op] {
		if r == judged {
			return true
		}
	}
	return false
}

// asError is err as the *Error the handler answers: an operation's own, or
// a 500 for anything else.
func asError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{Status: http.StatusInternalServerError, Msg: err.Error()}
}
