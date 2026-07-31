// Egress approver backend: a default-deny net provider (plans/interfaces.md)
// with a human in the loop.
//
// Dataplane. xbind splices each bound client's egress into a point-to-point
// link here (bxc0, bxc1, …); our own egress is bx0. We enable IP forwarding and
// install a per-destination *forward* gate (gate_linux.go): client traffic is
// dropped unless its remote IP has been approved. The gate is forward-only, so
// this tile's own lookups below still reach the internet.
//
// Control plane. An AF_PACKET tap on the client links (sniff in gate_linux.go)
// reports every forwarded packet — even the ones the gate drops. From that we
// (a) surface each new remote IP for the owner to approve/deny, with reverse DNS
// and RDAP/whois on demand, and (b) meter bytes in/out per remote since start.
// Approvals persist in a kv resource; byte counters are memory-only by design.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// resolveTimeout bounds a reverse-DNS lookup so a slow/no-PTR address never
// leaves the UI showing "resolving…" indefinitely.
const resolveTimeout = 3 * time.Second

func lookupPTR(ip string) string {
	ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
	defer cancel()
	if names, err := net.DefaultResolver.LookupAddr(ctx, ip); err == nil && len(names) > 0 {
		return strings.TrimSuffix(names[0], ".")
	}
	return ""
}

var kv = xbin.KV(xbin.Resource("approvals"))

// stat accumulates what we know about one remote IP since the last restart.
type stat struct {
	FirstSeen time.Time
	LastSeen  time.Time
	BytesIn   int64 // internet → client
	BytesOut  int64 // client → internet
}

type approver struct {
	mu       sync.Mutex
	approved map[string]bool  // decided: allowed out (persisted)
	denied   map[string]bool  // decided: blocked, stop prompting (persisted)
	seen     map[string]*stat // every trackable remote observed
	rdns     map[string]string
	rdnsWIP  map[string]bool

	egress  string // our own egress iface (bx0), "" until found
	clients int    // number of client links we're gating
}

func newApprover() *approver {
	a := &approver{
		approved: map[string]bool{}, denied: map[string]bool{},
		seen: map[string]*stat{}, rdns: map[string]string{}, rdnsWIP: map[string]bool{},
	}
	var app, den []string
	_ = kv.GetJSON("approved", &app)
	_ = kv.GetJSON("denied", &den)
	for _, ip := range app {
		a.approved[ip] = true
	}
	for _, ip := range den {
		a.denied[ip] = true
	}
	return a
}

// onFlow records one observed packet and, for a brand-new undecided remote,
// kicks off a reverse-DNS lookup so the owner has a name to recognise it by.
func (a *approver) onFlow(remote net.IP, inbound bool, n int) {
	if !trackable(remote) {
		return
	}
	ip := remote.String()
	now := time.Now()
	a.mu.Lock()
	s := a.seen[ip]
	if s == nil {
		s = &stat{FirstSeen: now}
		a.seen[ip] = s
		if !a.approved[ip] && !a.denied[ip] && !a.rdnsWIP[ip] {
			a.rdnsWIP[ip] = true
			go a.lookupRDNS(ip)
		}
	}
	s.LastSeen = now
	if inbound {
		s.BytesIn += int64(n)
	} else {
		s.BytesOut += int64(n)
	}
	a.mu.Unlock()
}

func (a *approver) lookupRDNS(ip string) {
	name := lookupPTR(ip)
	a.mu.Lock()
	a.rdns[ip] = name
	delete(a.rdnsWIP, ip)
	a.mu.Unlock()
}

// keysSorted returns a set's members, sorted (for persistence + the gate).
func keysSorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// decide moves an IP into approved / denied / undecided and reconciles the
// kernel gate to the new approved set. cmd is "approve", "deny", or "forget".
// The in-memory maps are updated synchronously (so the very next /state — and
// the optimistic UI — reflect the decision at once); the slow parts (kv writes
// + `ip` route reconciliation) run off the request path so the click feels
// instant and never blocks /state on the lock.
func (a *approver) decide(ip, cmd string) error {
	if net.ParseIP(ip) == nil {
		return fmt.Errorf("not an IP: %q", ip)
	}
	a.mu.Lock()
	delete(a.approved, ip)
	delete(a.denied, ip)
	switch cmd {
	case "approve":
		a.approved[ip] = true
	case "deny":
		a.denied[ip] = true
	case "forget":
		// leave undecided; it will re-appear as pending if still active
	default:
		a.mu.Unlock()
		return fmt.Errorf("unknown action %q", cmd)
	}
	allowed, denied := keysSorted(a.approved), keysSorted(a.denied)
	a.mu.Unlock()

	go func() {
		_ = kv.PutJSON("approved", allowed)
		_ = kv.PutJSON("denied", denied)
		if err := applyGate(a.egress, allowed); err != nil {
			logf("apply gate: %v", err)
		}
	}()
	return nil
}

// --- JSON API (served to this tile's own frontend) ---------------------------

type ipView struct {
	IP        string `json:"ip"`
	RDNS      string `json:"rdns"`
	FirstSeen int64  `json:"firstSeen"` // unix ms
	LastSeen  int64  `json:"lastSeen"`
	BytesIn   int64  `json:"bytesIn"`
	BytesOut  int64  `json:"bytesOut"`
}

func (a *approver) view(ip string, s *stat) ipView {
	ms := func(t time.Time) int64 {
		if t.IsZero() {
			return 0
		}
		return t.UnixMilli()
	}
	return ipView{ip, a.rdns[ip], ms(s.FirstSeen), ms(s.LastSeen), s.BytesIn, s.BytesOut}
}

func (a *approver) handleState(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	var pending, approved, denied []ipView
	for ip, s := range a.seen {
		v := a.view(ip, s)
		switch {
		case a.approved[ip]:
			approved = append(approved, v)
		case a.denied[ip]:
			denied = append(denied, v)
		default:
			pending = append(pending, v)
		}
	}
	// A decided IP we haven't seen traffic from yet still belongs in its list.
	for ip := range a.approved {
		if a.seen[ip] == nil {
			approved = append(approved, ipView{IP: ip, RDNS: a.rdns[ip]})
		}
	}
	for ip := range a.denied {
		if a.seen[ip] == nil {
			denied = append(denied, ipView{IP: ip, RDNS: a.rdns[ip]})
		}
	}
	clients, egress := a.clients, a.egress != ""
	a.mu.Unlock()

	sort.Slice(pending, func(i, j int) bool { return pending[i].FirstSeen < pending[j].FirstSeen })
	byRecent := func(v []ipView) func(i, j int) bool {
		return func(i, j int) bool { return v[i].LastSeen > v[j].LastSeen }
	}
	sort.Slice(approved, byRecent(approved))
	sort.Slice(denied, byRecent(denied))

	writeJSON(w, map[string]any{
		"pending": pending, "approved": approved, "denied": denied,
		"clients": clients, "egressReady": egress,
	})
}

func (a *approver) handleDecision(cmd string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct{ IP string }
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.IP == "" {
			http.Error(w, `{"error":"need {ip}"}`, http.StatusBadRequest)
			return
		}
		if err := a.decide(body.IP, cmd); err != nil {
			http.Error(w, `{"error":`+jsonQuote(err.Error())+`}`, http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	}
}

// handleDetail returns reverse DNS + a live RDAP (whois) lookup for one IP.
func (a *approver) handleDetail(w http.ResponseWriter, r *http.Request) {
	ip := r.URL.Query().Get("ip")
	if net.ParseIP(ip) == nil {
		http.Error(w, `{"error":"need ?ip="}`, http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	rdns := a.rdns[ip]
	a.mu.Unlock()
	if rdns == "" {
		rdns = lookupPTR(ip)
	}
	writeJSON(w, map[string]any{"ip": ip, "rdns": rdns, "rdap": rdapLookup(ip)})
}

func (a *approver) handleStatus(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	writeJSON(w, map[string]any{"clients": a.clients, "egress": a.egress, "approved": len(a.approved)})
}

func main() {
	a := newApprover()

	// Bring up the dataplane: find our links, enable forwarding, install the
	// default-deny gate, and load persisted approvals into it. Failures here are
	// logged but non-fatal — the control plane still runs so the owner sees state
	// (e.g. when running outside a sandbox, or before any client is bound).
	a.egress, a.clients = discoverLinks()
	if err := setupGate(a.egress); err != nil {
		logf("gate setup: %v", err)
	}
	if err := applyGate(a.egress, keysSorted(a.approved)); err != nil {
		logf("apply approvals: %v", err)
	}
	go sniffClients(a.onFlow)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /state", a.handleState)
	mux.HandleFunc("POST /approve", a.handleDecision("approve"))
	mux.HandleFunc("POST /deny", a.handleDecision("deny"))
	mux.HandleFunc("POST /forget", a.handleDecision("forget"))
	mux.HandleFunc("GET /detail", a.handleDetail)
	mux.HandleFunc("GET /status", a.handleStatus)
	xbin.Serve(mux)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// jsonQuote JSON-quotes a string (small helper for inline error bodies).
func jsonQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

func logf(format string, args ...any) { fmt.Printf("egress-approver: "+format+"\n", args...) }
