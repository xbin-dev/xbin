package proxy

import (
	"errors"
	"net"
	"net/http"
)

// generation is the backend generation a request is sent to: its socket,
// and whether xbind has retired it since (runner.Gen).
type generation interface {
	Sock() string
	Retired() bool
}

// maxReroutes bounds how many times one request follows its deployment to a
// newer generation. Each reroute needs another swap to retire the
// generation the request went to while it was in flight, so a request
// reroutes at most once in practice; the bound only keeps a deployment
// swapping without pause from holding a request forever.
const maxReroutes = 3

// rerouting is the transport of one proxied request (D173). It sends the
// request to the generation the request was routed to; when that
// generation fails it before any answer and xbind has retired it meanwhile
// — what a swap does to a request routed just before it: the old
// generation's SIGTERM closes its socket under the request (a connection
// queued in its listen backlog is reset, a dial after the close is
// refused) — the request goes again to the generation the deployment has
// now, when sending it again is safe (resendable). Any other failure is the
// request's answer, as before: a crash is not a retirement.
type rerouting struct {
	px  *Proxy
	gen generation
	// again is the deployment's generation now: the same routing that chose
	// gen, asked again.
	again func() (generation, error)
}

func (t *rerouting) RoundTrip(req *http.Request) (*http.Response, error) {
	for n := 0; ; n++ {
		res, err := t.px.transportFor(t.gen.Sock()).RoundTrip(req)
		if err == nil || n == maxReroutes || !t.gen.Retired() || !resendable(req, err) {
			return res, err
		}
		gen, gerr := t.again()
		if gerr != nil {
			return nil, gerr // why the deployment has no generation to answer now
		}
		t.gen = gen
	}
}

// resendable says whether req, which failed with err before any answer, may
// be sent again: it carries no body (a body was consumed by the failed
// attempt), its caller is still waiting, and either nothing of it reached
// the backend (the dial failed) or its method is idempotent (RFC 9110 §9.2.2;
// net/http's own retry rule, with its Idempotency-Key headers).
func resendable(req *http.Request, err error) bool {
	if req.Body != nil && req.Body != http.NoBody {
		return false
	}
	if req.Context().Err() != nil {
		return false
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return true
	}
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	}
	_, key := req.Header["Idempotency-Key"]
	_, xkey := req.Header["X-Idempotency-Key"]
	return key || xkey
}
