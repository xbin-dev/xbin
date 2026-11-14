package server

// delegate.go — the delegate gate (plans/wsagent/s2-delegation.md): the one
// place a delegate credential (auth/delegate.go) is honoured. authedAPI
// hands every /api/ request that carries the delegate scheme here, before
// any other credential on it is looked at; no other plane resolves the
// scheme, so there a delegate is nobody (401).
//
// For each request the gate
//
//  1. resolves the person (auth.DelegateFromRequest: the sealed holder, the
//     live person, the off switches) — 401 for a credential that delegates
//     nothing, 403 with the reason when delegation is off;
//  2. classifies it (delegateVerdict): a row of delegateClasses for
//     /api/xbin/*, the tile class for /api/<tile>/…, refused otherwise —
//     path tickets, WebSocket upgrades, unlisted and never-listed routes;
//  3. answers a dry run (X-XBin-Dry-Run: 1) with what the call would be,
//     running nothing;
//  4. runs it as the person's principal through the handlers everyone uses
//     (handleAPI: the deployment and partition gates, the audit line; the
//     component proxy: the broker's Route), and
//  5. writes the row of the person's audit log, refusals included.

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
)

// HeaderDryRun asks the delegate gate what a call would do instead of
// making it. Only the gate reads it; the proxy strips it like every
// X-XBin-* before a backend sees the request.
const HeaderDryRun = "X-XBin-Dry-Run"

// DelegateLog is the per-person audit log the gate writes
// (delegation.Log). nil: nothing is recorded beyond the daemon's log.
type DelegateLog interface {
	Append(delegation.Call) error
	Read(user string, before time.Time, limit int) ([]delegation.Call, error)
}

// DelegatePolicy is a Policy that answers what a delegate's call to a tile
// would carry (the broker's DelegateTile): the level its person holds on
// the tile and the role the call gets, or why it is refused. The gate asks
// it for a dry run only: a real call is decided by the proxy's Route.
type DelegatePolicy interface {
	DelegateTile(p auth.Principal, tile string) (level, role string, err error)
}

// delegateVerdict is the gate's judgement of one request.
type delegateVerdict struct {
	class  string // the audit row's class
	effect bool   // a side effect
	tile   string // the tile, for a tile API call
	deny   string // the refusal; "" when the call may run
}

const (
	classTileRead  = "tile-read"
	classTileWrite = "tile-write"
)

// delegateVerdict classifies r for a delegate.
func (s *Server) delegateVerdict(r *http.Request) delegateVerdict {
	rest, ok := strings.CutPrefix(r.URL.Path, "/api/")
	switch {
	case !ok:
		return delegateVerdict{class: DelegateUnlisted.String(), deny: "an agent reaches xbind's API and tiles' APIs only"}
	case isPathTicket(r):
		return delegateVerdict{class: DelegateNever.String(), deny: neverIdentity}
	case r.Header.Get("Upgrade") != "":
		return delegateVerdict{class: DelegateUnlisted.String(), deny: "an agent makes plain requests: a socket would outlive the person pausing it"}
	}
	if rest == "xbin" || strings.HasPrefix(rest, "xbin/") {
		r2 := r.Clone(r.Context())
		if !xbinInner(r.URL, r2.URL) || dotSegment(r2.URL.Path) || s.apiMux == nil {
			return delegateVerdict{class: DelegateUnlisted.String(), deny: "no such route"}
		}
		_, pattern := s.apiMux.Handler(r2)
		row := delegateClasses[pattern]
		v := delegateVerdict{class: row.class.String(), effect: row.class == DelegateAct}
		switch {
		case pattern == "":
			v.deny = "no such route" // nothing would run; the agent learns nothing a 404 wouldn't say
		case row.class == DelegateNever:
			v.deny = row.why
		case row.class == DelegateUnlisted:
			v.deny = "this route isn't delegated to a person's agent"
		case delegateUnconverted[pattern] != "":
			v.deny = "this route isn't available to a person's agent yet (" + delegateUnconverted[pattern] + ")"
		}
		return v
	}
	v := delegateVerdict{class: classTileRead}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
		v.class, v.effect = classTileWrite, true
	}
	if s.Reg != nil {
		if comp, _, _, _, _ := s.Reg.ResolveRef(rest, nil); comp != nil {
			v.tile = comp.Path
		}
	}
	return v
}

// serveDelegate is the gate. next is the /api/ handler (handleAPI).
func (s *Server) serveDelegate(w http.ResponseWriter, r *http.Request, next http.Handler) {
	p, _, err := s.Auth.DelegateFromRequest(r)
	if err != nil {
		if errors.Is(err, auth.ErrDelegateOff) {
			WriteError(w, http.StatusForbidden, "your workspace agent is off: "+delegateOffReason(err), "/docs/auth.md")
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized) // delegates nothing: as any bad token
		return
	}
	v := s.delegateVerdict(r)
	row := delegation.Call{User: p.UserID, Agent: p.Agent, Method: r.Method, Path: r.URL.Path, Tile: v.tile, Class: v.class, Effect: v.effect}
	if v.deny != "" {
		row.Status, row.Refused = http.StatusForbidden, v.deny
		s.delegateAudit(row)
		WriteJSON(w, http.StatusForbidden, map[string]string{"error": v.deny, "refusal": "not-delegated", "class": v.class, "docs": "/docs/auth.md"})
		return
	}
	if r.Header.Get(HeaderDryRun) != "" {
		row.DryRun = true
		row.Status = s.delegateDryRun(w, p, v)
		s.delegateAudit(row)
		return
	}
	dw := &delegateWriter{ResponseWriter: w, status: http.StatusOK}
	next.ServeHTTP(dw, r.WithContext(auth.WithPrincipal(r.Context(), p)))
	row.Status = dw.status
	s.delegateAudit(row)
}

// delegateOffReason is the gate's own words of an ErrDelegateOff.
func delegateOffReason(err error) string {
	if u, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range u.Unwrap() {
			if !errors.Is(e, auth.ErrDelegateOff) {
				return e.Error()
			}
		}
	}
	return "delegation isn't set up on this xbind"
}

// delegateDryRun answers what the allowed call v would be for p, running
// nothing: the class, whether it is a side effect and whether xbind can
// undo it, and for a tile call the level and role it would carry (or the
// refusal Route would give). The status it wrote is returned.
func (s *Server) delegateDryRun(w http.ResponseWriter, p auth.Principal, v delegateVerdict) int {
	out := map[string]any{"dryRun": true, "class": v.class, "as": p.UserID, "agent": p.Agent, "sideEffect": v.effect}
	if v.effect {
		out["reversible"] = false // xbind undoes nothing a tile or a tile agent did
	}
	if v.tile != "" {
		out["tile"] = v.tile
		out["owner"] = s.policy().OwnerOf(v.tile)
		if dp, ok := s.policy().(DelegatePolicy); ok {
			level, role, err := dp.DelegateTile(p, v.tile)
			if err != nil {
				out["allowed"], out["refusal"] = false, err.Error()
				WriteJSON(w, http.StatusOK, out)
				return http.StatusOK
			}
			out["level"], out["role"] = level, role
		}
	}
	out["allowed"] = true
	WriteJSON(w, http.StatusOK, out)
	return http.StatusOK
}

// delegateAudit writes one delegate call to the person's audit log and to
// the daemon's (the "audit" line governance writes use).
func (s *Server) delegateAudit(c delegation.Call) {
	if s.DelegateLog != nil {
		if err := s.DelegateLog.Append(c); err != nil {
			slog.Warn("delegate audit: row not written", "user", c.User, "err", err)
		}
	}
	if c.Effect || c.Refused != "" { // reads stay out of the daemon's log, as other reads do
		slog.Info("audit", "who", "user:"+c.User, "agent", c.Agent, "method", c.Method, "path", c.Path,
			"class", c.Class, "status", c.Status, "refused", c.Refused, "dryRun", c.DryRun)
	}
}

// delegateWriter records the status a delegate call was answered with and
// keeps streaming working (an agent follows a session's events). It doesn't
// hijack: a delegate makes no socket.
type delegateWriter struct {
	http.ResponseWriter
	status int
}

func (d *delegateWriter) WriteHeader(code int) {
	d.status = code
	d.ResponseWriter.WriteHeader(code)
}

func (d *delegateWriter) Flush() {
	if f, ok := d.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (d *delegateWriter) Unwrap() http.ResponseWriter { return d.ResponseWriter }

// apiDelegateAudit is GET /delegate/audit[?before=<RFC3339>&limit=<n>]:
// "what my agent did" — the caller's own rows, newest first. A person's own
// session, app or device only: never their agent (it would read what it
// was refused), a tile, view-as or the root token. An admin reads another
// person's with ?user=. Mounted with the workspace agent (not yet).
func (s *Server) apiDelegateAudit(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	if p.Component != "" || p.Agent != "" || p.Impersonator != "" || p.UserID == "" {
		WriteError(w, http.StatusForbidden, "this is a person's own log: sign in and read it yourself", "/docs/auth.md")
		return
	}
	user := p.UserID
	if u := r.URL.Query().Get("user"); u != "" && u != user {
		if !p.IsAdmin() {
			WriteError(w, http.StatusForbidden, "another person's agent log is an admin's to read", "/docs/auth.md")
			return
		}
		user = u
	}
	var before time.Time
	if b := r.URL.Query().Get("before"); b != "" {
		t, err := time.Parse(time.RFC3339, b)
		if err != nil {
			WriteError(w, http.StatusBadRequest, "before is an RFC 3339 time")
			return
		}
		before = t
	}
	limit := 0
	for _, c := range r.URL.Query().Get("limit") {
		if c < '0' || c > '9' || limit > 1000 {
			limit = 0
			break
		}
		limit = limit*10 + int(c-'0')
	}
	if s.DelegateLog == nil {
		WriteJSON(w, http.StatusOK, map[string]any{"user": user, "calls": []delegation.Call{}})
		return
	}
	rows, err := s.DelegateLog.Read(user, before, limit)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "the log can't be read")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"user": user, "calls": rows})
}
