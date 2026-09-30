// Package proxy routes /api/<component-path>/… to component backends over
// their unix sockets (blue/green targets from the runner, which sandboxes
// them). It never executes tile code itself — runtime "cgi", which did, was
// removed (D117). It enforces the gateway side of the RBAC model: strips
// inbound X-XBin-* headers, consults the policy, and injects the verified
// caller identity (plans/auth.md §3).
//
// With tile deployments, /api/<tile>+<name>/… names a deployment of a tile
// that has a deployment record (11-contract §2) (D127j), and the broker's
// routing function (Route) decides which deployment a call reaches: the
// primary for a bare URL, the caller's own for a tile's self-call (D127g).
// A tile without a record resolves, routes and is identified exactly as
// before (D119c).
package proxy

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/runner"
	"github.com/xbin-dev/xbin/internal/util"
)

const (
	HeaderFrom = "X-XBin-From"
	HeaderRole = "X-XBin-Role"
	// HeaderUser / HeaderUserLevel attribute the HUMAN driving the call (D29):
	// set when a signed-in user is behind the request — directly (session) or
	// riding an element principal (frame token, tile terminal). Absent for
	// automation (instance tokens, cron) and the bootstrap owner token.
	// Backends use these to gate in-app (read vs write UI, per-user state) —
	// trustworthy because inbound X-XBin-* is stripped.
	HeaderUser      = "X-XBin-User"
	HeaderUserLevel = "X-XBin-User-Level"
	// HeaderViewedBy is set when the attributed user is being VIEWED AS by an
	// admin (D64): it names the admin ("owner" for the root token). The
	// request reads as that user; a backend that keeps per-user private data
	// can refuse to show it to someone else looking through the user's eyes.
	HeaderViewedBy = "X-XBin-Viewed-By"
	// HeaderDeployment names a tile deployment that is not its tile's
	// primary (the role rule, 11-contract §4) (D127j). On a request: the
	// calling tile's deployment, when the caller is one of that tile's own
	// principals bound to a non-primary deployment, on its self-calls and on
	// its calls to other tiles. On a response: the deployment that answered,
	// when it isn't the target's primary (NP-11-12), set over any value the
	// backend set. Absent everywhere for the primary.
	HeaderDeployment = "X-XBin-Deployment"
)

// Policy decides whether principal p may call target, and at which role.
// Installed by the broker (phase 4); the default allows owner and
// self-calls only.
type Policy func(p auth.Principal, target *registry.Component) (role string, ok bool)

func DefaultPolicy(p auth.Principal, target *registry.Component) (string, bool) {
	if p.IsAdmin() {
		return "admin", true
	}
	if p.Component != "" && p.Component == target.Path {
		return "admin", true // an element is admin of itself
	}
	return "", false
}

// Decision is the routing function's answer for one call (09-fabric §4.1,
// §5.3): which deployment of the target it reaches and at which role, or why
// it is refused. The fields are broker.Decision's, which the proxy can't
// import, so boot converts one into the other.
type Decision struct {
	Deployment string   // the target's deployment: its primary, or the caller's own on a self-call
	Role       string   // the effective role on the target; "" when refused
	Clamped    bool     // the read clamp narrowed Role (D127a)
	Edges      []string // the caller's edges authorizing the call
	// Deny is non-nil when the call is refused: util.ErrNoDeployment is a
	// 404, anything else a 403 whose text is the answer's error.
	Deny error

	// Partitioned tiles (partitionroute.go): the target's partition the
	// call reaches, the one the caller acts in and its id, the F5
	// attribution, and the delivery ("cron", "bus", "mail") a start it
	// causes is for — a background start — "" for everything else.
	Partition         util.Partition
	CallerPartition   util.Partition
	CallerPartitionID string
	Attribute         *auth.Attribution
	Delivery          string
}

type Proxy struct {
	Reg    *registry.Registry
	Runner *runner.Runner
	Hub    *events.Hub
	// Policy decides the role of a call when no Route is installed: the
	// broker-less proxy of tests, where every call reaches the primary and
	// no URL names a deployment. nil = DefaultPolicy.
	Policy Policy
	// Route is the broker's routing function (09-fabric §4.1), installed at
	// boot in Policy's place: the deployment of target a call by p reaches,
	// at which role, or why it is refused. qualifier is the deployment the
	// URL names, "" for the bare URL.
	Route func(p auth.Principal, target *registry.Component, qualifier string) Decision
	// RouteGlobal is the broker's routing function for a call that
	// addresses a partitioned target's global instance
	// (?xbin-partition=global, consumed here; globaladdress.go), installed
	// at boot beside Route. nil = such a call is refused.
	RouteGlobal func(p auth.Principal, target *registry.Component, qualifier string) Decision
	// Deployments answers what a qualified URL and the role rule ask about a
	// tile's deployments (a record, its deployments, its primary): the
	// deployments plane. nil = no tile has a record.
	Deployments registry.DeploymentLookup
	// Partitions starts and holds user partitions (partitionroute.go); nil
	// = none runs here.
	Partitions PartitionRunner

	// UserLevel resolves the attributed user's access level on a tile for
	// the X-XBin-User-Level header (D29). Installed by main from the user
	// store; nil = header omitted.
	UserLevel func(userID, tile string) string

	trMu       sync.Mutex
	transports map[string]*http.Transport // backend socket → pooled transport
	trJanitor  sync.Once
}

// transportFor returns the pooled transport for a backend socket. One
// Transport per socket, cached: previously a FRESH Transport was built per
// proxied request, so every request stranded its keep-alive connection in a
// garbage pool — the backend parked a goroutine + buffers per RPC, forever
// (neither side closed it). Pooling reuses connections; IdleConnTimeout (90s,
// under the SDK server's 120s IdleTimeout) reaps quiet ones. Sockets are
// per-generation (g<N>.sock), so the janitor evicts entries — closing their
// idle conns — once a generation's socket is gone.
func (px *Proxy) transportFor(sock string) *http.Transport {
	px.trMu.Lock()
	defer px.trMu.Unlock()
	if tr, ok := px.transports[sock]; ok {
		return tr
	}
	if px.transports == nil {
		px.transports = map[string]*http.Transport{}
	}
	px.trJanitor.Do(func() {
		go func() {
			for range time.Tick(2 * time.Minute) {
				px.sweepTransports()
			}
		}()
	})
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
		MaxIdleConns:        32,
		MaxIdleConnsPerHost: 32, // every request targets the one "xbin" host
		IdleConnTimeout:     90 * time.Second,
	}
	px.transports[sock] = tr
	return tr
}

// sweepTransports drops transports whose backend socket no longer exists (the
// generation was torn down), closing their idle connections.
func (px *Proxy) sweepTransports() {
	px.trMu.Lock()
	defer px.trMu.Unlock()
	for sock, tr := range px.transports {
		if _, err := os.Stat(sock); err != nil {
			tr.CloseIdleConnections()
			delete(px.transports, sock)
		}
	}
}

func (px *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/")
	p := auth.PrincipalOf(r)
	// /api/<tile>[+<deployment>]/<endpoint> (11-contract §2.2): a path with no
	// qualifier, and every path of a tile without a deployment record,
	// resolves exactly as Reg.Resolve does (D119c). The qualifier is consumed:
	// the backend sees /<endpoint>, as for the bare URL.
	comp, qdep, qualified, endpoint, rerr := px.Reg.ResolveRef(rest, px.lookup())
	if comp == nil {
		jsonErr(w, http.StatusNotFound, "no such component", "")
		return
	}
	qualifier := ""
	if qualified {
		qualifier = qdep
	}
	// The decision is taken here and acted on in the order the gates have
	// always run: the tile-level gates answer first, the refusal after them.
	// A partitioned target's ?xbin-partition=global is consumed here and
	// decided by RouteGlobal (globaladdress.go).
	d := px.decideAddressed(r, p, comp, qualifier)
	if rerr != nil {
		// An unknown deployment, or a nested tile under a qualifier: a caller
		// who may not know the tile's deployments gets the gate's refusal,
		// whether or not the name exists (11-contract §2.2, §2.4).
		if d.Deny != nil && denyStatus(d.Deny) == http.StatusForbidden {
			jsonErr(w, http.StatusForbidden, d.Deny.Error(), "")
		} else {
			jsonErr(w, http.StatusNotFound, rerr.Error(), "")
		}
		return
	}
	primary := px.primary(comp.Path)
	target := cmp.Or(d.Deployment, util.MainDeployment)
	// comp is the tile's primary: the registry composes a pinned primary
	// from its checkpoint, so the template (inbound surface) and runtime
	// (deployment-level) gates below follow the code the primary runs, never
	// a work-tree edit made while it is pinned (D119e).
	if comp.IsTemplate() {
		jsonErr(w, http.StatusNotFound,
			fmt.Sprintf("%s is a template — instantiate it first (Tile Manager → New from template, or `bx template new`)", comp.Path), "")
		return
	}
	// The runtime and backend gates read the primary's code, so they judge
	// a call that reaches the primary (or is refused anyway). A call routed
	// to another deployment runs that deployment's own code, whose runtime
	// and backend EnsureDeployment checks (07-runtime §4.2).
	if d.Deny != nil || target == primary {
		if err := registry.ValidateRuntime(comp.Manifest); err != nil {
			// runtime "cgi" (D117): its code never runs; say why, not "no backend".
			jsonErr(w, http.StatusGone, comp.Path+": "+err.Error(), "")
			return
		}
		if !comp.HasBackend() {
			jsonErr(w, http.StatusNotFound,
				fmt.Sprintf("component %s has no backend (runtime %q)", comp.Path, comp.Manifest.Runtime), "")
			return
		}
	}
	// Lifecycle gate (plans/lifecycle.md): a disabled/offloaded component's
	// backend must not spawn. 409 with the state so callers/the frame can show a
	// placeholder; offloaded also means its data isn't local until restored.
	// Lifecycle is the tile's: it answers qualified URLs the same way.
	if state := px.Reg.LifecycleState(comp.Path); state != registry.StateEnabled {
		w.Header().Set("X-XBin-Lifecycle", state)
		msg := fmt.Sprintf("component %s is %s", comp.Path, state)
		if registry.IsOffloaded(state) {
			msg += " — restore it to use (admin)"
		} else {
			msg += " — enable it to use (admin)"
		}
		jsonErr(w, http.StatusConflict, msg, "")
		return
	}

	if d.Deny != nil {
		// 404 for a bound deployment that no longer exists (09-fabric §3.1)
		// or a global instance the tile doesn't have; else 403
		jsonErr(w, denyStatus(d.Deny), d.Deny.Error(), "")
		return
	}
	// The partition gate (partition.go): a pending or invalid mode runs no
	// instance of the primary; the caller learns why, never the runner.
	if msg, body, paused := PartitionPaused(comp); paused && target == primary {
		writePartitionPaused(w, msg, body)
		return
	}

	px.identify(r, p, d.Role, comp.Path)
	px.identifyPartition(r, d)

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	// Route's answer, and never another (09-fabric §4.1): the primary for a
	// bare URL from anyone but the tile itself, the caller's own on a
	// self-call, the named one on a qualified URL; and, on a partitioned
	// tile, the partition it reached (partitionroute.go).
	// deployment: the target Route returned.
	sock, hold, err := px.ensureTarget(ctx, comp, target, d)
	if err != nil {
		var be *runner.BuildError
		code, partErr := ensureStatus(d, err)
		switch {
		case partErr:
			jsonErr(w, code, err.Error(), "")
		case errors.As(err, &be):
			jsonErr(w, http.StatusBadGateway, "backend build failed", be.Output)
		case errors.Is(err, util.ErrNoDeployment):
			jsonErr(w, http.StatusNotFound, err.Error(), "") // removed since Route answered
		default:
			jsonErr(w, http.StatusBadGateway, err.Error(), "")
		}
		return
	}

	// Hold the backend for the whole connection: SSE and WebSocket streams
	// block in forward below, and the idle reaper must not stop a backend
	// that is mid-stream (ensureTarget took the hold).
	defer hold.done()

	answering := ""
	if target != primary {
		answering = target
	}
	px.forward(w, r, sock, endpoint, answering, hold.onResponse)
}

// forward proxies r to the backend listening on sock, at /<endpoint>.
// answering names the deployment that answers when it isn't the target's
// primary: the response then carries X-XBin-Deployment, set over any value
// the backend set (NP-11-12). A primary's responses pass as they always
// have. onResponse, when set, sees the backend's response before it is
// copied back (a user partition's hold, partitionroute.go).
func (px *Proxy) forward(w http.ResponseWriter, r *http.Request, sock, endpoint, answering string, onResponse func(*http.Response)) {
	// The ?frame= auth credential (browser WS attribution) is consumed
	// here; never forward it — the callee could replay it as the caller.
	outQuery := r.URL.Query()
	outQuery.Del("frame")
	rawQuery := outQuery.Encode()

	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = "xbin" // ignored by the unix transport
			pr.Out.URL.Path = "/" + endpoint
			pr.Out.URL.RawQuery = rawQuery
			pr.Out.Host = "xbin"
			// Rewrite mode strips hop-by-hop headers before this runs;
			// protocol upgrades (WebSocket) need them restored explicitly.
			if u := pr.In.Header.Get("Upgrade"); u != "" {
				pr.Out.Header.Set("Connection", "Upgrade")
				pr.Out.Header.Set("Upgrade", u)
			}
		},
		// A tile origin's /api (--tile-assets=origins) sets no cookies: a
		// Domain=<parent> Set-Cookie would reach the workspace and sibling
		// tiles (auth.WithNoSetCookie). Runs before a 101's hijack too.
		ModifyResponse: func(res *http.Response) error {
			if auth.NoSetCookie(res.Request.Context()) {
				res.Header.Del("Set-Cookie")
			}
			if answering != "" {
				res.Header.Set(HeaderDeployment, answering)
			}
			if onResponse != nil {
				onResponse(res)
			}
			return nil
		},
		Transport:     px.transportFor(sock),
		FlushInterval: -1, // stream (SSE etc.)
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			jsonErr(w, http.StatusBadGateway, "backend error: "+err.Error(), "")
		},
	}
	rp.ServeHTTP(w, r)
}

// decide is the routing decision for a call by p to comp, the URL naming
// deployment qualifier ("" for the bare URL): Route's answer when the
// broker installed one. Without it, Policy's role, on the primary, which is
// where every call went before tile deployments; a principal bound to a
// deployment that isn't the primary is refused there (F6: fail closed), as
// only Route knows where its calls may go.
func (px *Proxy) decide(p auth.Principal, comp *registry.Component, qualifier string) Decision {
	if px.Route != nil {
		return px.Route(p, comp, qualifier)
	}
	pol := px.Policy
	if pol == nil {
		pol = DefaultPolicy
	}
	role, allowed := pol(p, comp)
	if !allowed {
		return Decision{Deny: fmt.Errorf(
			"%s is not granted access to %s — declare it in \"uses\" and approve the grant (bx grant, or the grants panel)",
			p.From(), comp.Path)}
	}
	if dep := px.nonPrimaryDeployment(p); dep != "" {
		return Decision{Deny: fmt.Errorf("%s's deployment %q can't call through this proxy: it has no deployment routing, so only a primary's credentials reach a tile",
			p.Component, dep)}
	}
	return Decision{Deployment: px.primary(comp.Path), Role: role}
}

// lookup is what ResolveRef asks about deployment records: none without a
// routing function, since only Route may send a call to a deployment the
// URL names.
func (px *Proxy) lookup() registry.DeploymentLookup {
	if px.Route == nil {
		return nil
	}
	return px.Deployments
}

// primary names tile's primary deployment: main without a record, and
// without the deployments plane. Read on every request, since a tile
// manager may reassign it at any time (09-fabric §3.6).
func (px *Proxy) primary(tile string) string {
	if d := px.Deployments; d != nil {
		return d.Primary(tile)
	}
	return util.MainDeployment
}

// nonPrimaryDeployment is the deployment p acts in when p is one of a
// tile's own principals (an instance, frame or terminal credential) bound
// to a deployment that isn't that tile's primary; "" for everyone else:
// humans, the owner, cron and bus deliveries, and every principal of a
// primary. A frame or instance credential with no deployment is main's (the
// name rule); a terminal or agent session with none follows the primary.
func (px *Proxy) nonPrimaryDeployment(p auth.Principal) string {
	if p.Component == "" || (p.Via != "instance" && p.Via != "frame" && p.Via != "terminal") {
		return ""
	}
	dep := p.Deployment
	if dep == "" {
		if p.Via == "terminal" {
			return ""
		}
		dep = util.MainDeployment
	}
	if dep == px.primary(p.Component) {
		return ""
	}
	return dep
}

// identify scrubs any spoofed identity from r and injects the verified one.
// It strips every X-XBin-* an inbound caller might set — including
// X-XBin-Ingress-Host, which only the ingress path (ForwardIngress)
// legitimately injects; on this authenticated /api path a backend must never
// receive a caller-supplied one (a tile could otherwise fake a public
// hostname). And it strips xbind's own credentials: the caller's session
// cookie (either name — a link followed to /api/<tile>/…, a chrome page's
// call), the tile-origin cookie and an Authorization bearer (the owner
// token, a terminal's or instance's token, an app session). A backend is
// written by the tile's writers; replaying one of those it would act as the
// caller — an admin included — beyond any grant. Everything else (the
// tile's own cookies, a non-bearer Authorization) passes.
func (px *Proxy) identify(r *http.Request, p auth.Principal, role, tile string) {
	for k := range r.Header {
		if strings.HasPrefix(http.CanonicalHeaderKey(k), "X-Xbin-") {
			r.Header.Del(k)
		}
	}
	stripCookies(r, auth.CookieName, auth.SessionCookieHostName, auth.TileCookieName, auth.HostTileCookieName)
	if h := r.Header.Get("Authorization"); len(h) >= 7 && strings.EqualFold(h[:7], "Bearer ") {
		r.Header.Del("Authorization")
	}
	r.Header.Set(HeaderFrom, p.From())
	r.Header.Set(HeaderRole, role)
	setBackupSubkey(r) // xbind's own archive PUTs only: from the context, never the request
	// The role rule (11-contract §4) (D127j): a tile's own principal bound to
	// a deployment that isn't its primary names that deployment, on its
	// self-calls and on its calls to other tiles; X-XBin-From stays the
	// tile path. A primary's calls carry exactly the headers they always
	// have, and an inbound X-XBin-Deployment was stripped above, so a
	// backend that receives one knows xbind set it.
	if dep := px.nonPrimaryDeployment(p); dep != "" {
		r.Header.Set(HeaderDeployment, dep)
	}
	// Attribute the driving human (D29): frame/terminal principals carry the
	// user id; session principals are the user. Backends can then tell WHO
	// clicked — the tile's own UI at `read` is not a blank check anymore.
	uid := p.UserID
	if uid == "" && p.User != nil {
		uid = p.User.ID
	}
	if uid != "" {
		r.Header.Set(HeaderUser, uid)
		if px.UserLevel != nil {
			if l := px.UserLevel(uid, tile); l != "" {
				r.Header.Set(HeaderUserLevel, l)
			}
		}
		if p.Impersonator != "" {
			r.Header.Set(HeaderViewedBy, p.Impersonator)
		}
	}
}

func jsonErr(w http.ResponseWriter, code int, msg, detail string) {
	docs := "/docs/protocol.md"
	if code == http.StatusForbidden {
		docs = "/docs/auth.md"
	}
	b := map[string]string{"error": msg, "docs": docs}
	if detail != "" {
		b["detail"] = detail
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(b)
}
