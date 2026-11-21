package server

// delegateweb.go — a person's workspace agent on the web (D239;
// docs/protocol/delegation.md §Web and MCP): POST /delegate/web/fetch, the
// one way the agent reads a page. Its backend's sandbox stays "model
// only"; xbind judges and makes the fetch (internal/webfetch):
//
//  1. the delegate gate (the credential, the off switches, the handle, the
//     act limit) has run before this handler;
//  2. the session's reach (webReach): the workspace policy in force (the
//     admin's settings under the host's file), the person's own setting,
//     and the rules of every org whose data the conversation holds — one
//     that keeps its data off the web takes the web from the
//     conversation;
//  3. the URL: its form (webfetch.CheckURL), the allow and deny lists,
//     never one of the workspace's own hosts;
//  4. a URL is a way out: a conversation that holds any org's data reads
//     only a URL its person approved, once (a ticket bound to the body),
//     unless every org it holds allows the web outright;
//  5. the person's web limit, then the fetch — every redirect judged as
//     the first, every address checked when its socket connects — and
//     the handle marked: this conversation has read the web.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/agentpolicy"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/webfetch"
)

const webFetchPattern = "POST /delegate/web/fetch"

func (s *Server) registerDelegateWebAPI() {
	s.RegisterAPI(webFetchPattern, s.apiDelegateWebFetch)
}

// A person's fetches and tool calls: a burst, refilled over a minute —
// reading a few pages for a question, not crawling a site — on top of the
// gate's act limit, which each counts against too. Under the person's
// ceiling each conversation has a share of its own, so one that loops
// can't starve the person's others.
const (
	webBurst       = 12
	webRefill      = 5 * time.Second
	webShareBurst  = 8
	webShareRefill = 8 * time.Second
	mcpBurst       = 30
	mcpRefill      = 2 * time.Second
	mcpShareBurst  = 20
	mcpShareRefill = 3 * time.Second
)

// netLimit is one of the two limits: the person's and a conversation's.
type netLimit struct {
	burst  float64
	refill time.Duration
}

var netLimits = map[string][2]netLimit{
	"web": {{webBurst, webRefill}, {webShareBurst, webShareRefill}},
	"mcp": {{mcpBurst, mcpRefill}, {mcpShareBurst, mcpShareRefill}},
}

// allowNet takes one fetch ("web") or tool call ("mcp") of user's
// conversation conv — from the person's bucket and the conversation's,
// both or neither. whose says which was empty ("person" or
// "conversation"); wait is how long until it has one again.
func (d *delegateRuntime) allowNet(kind, user, conv string) (ok bool, wait time.Duration, whose string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.net == nil {
		d.net = map[string]*actBucket{}
	}
	now, lim := d.clock(), netLimits[kind]
	if len(d.net) > 4096 { // a bucket untouched for 10 minutes is full again: forget it
		for k, b := range d.net {
			if now.Sub(b.at) > 10*time.Minute {
				delete(d.net, k)
			}
		}
	}
	keys := [2]string{kind + "\x00" + user, kind + "\x00" + user + "\x00" + conv}
	var bs [2]*actBucket
	for i, k := range keys {
		if bs[i] = d.net[k]; bs[i] == nil {
			bs[i] = &actBucket{}
			d.net[k] = bs[i]
		}
	}
	// look at both before taking from either
	for i, who := range []string{"person", "conversation"} {
		probe := *bs[i]
		if ok, wait := probe.take(now, lim[i].burst, lim[i].refill); !ok {
			return false, wait, who
		}
	}
	for i := range bs {
		bs[i].take(now, lim[i].burst, lim[i].refill)
	}
	return true, 0, ""
}

// netLimitWords is the refusal of a fetch or a tool call over its limit.
func netLimitWords(kind, whose string, wait time.Duration) string {
	lim := netLimits[kind][0]
	who, what := "your agent", "reads the web"
	if whose == "conversation" {
		lim, who = netLimits[kind][1], "this conversation"
	}
	unit := "pages"
	if kind == "mcp" {
		what, unit = "calls MCP tools", "calls"
	}
	return fmt.Sprintf("%s %s too fast (%d %s at once, one more every %d s): it may try again in %d s", who, what, int(lim.burst), unit, int(lim.refill/time.Second), int(wait/time.Second)+1)
}

// ---- the session's reach ----

// The states of a reach.
const (
	reachOn   = "on"   // it may, freely
	reachAsks = "asks" // each use needs the person's approval: the conversation holds data
	reachOff  = "off"
)

// netReach is what a conversation may do of one capability now: State,
// Why in words a person and a model read, the refusal a call meets while
// it is off (Code), and — while it asks — the orgs whose data is why.
type netReach struct {
	State string   `json:"state"`
	Why   string   `json:"why,omitempty"`
	Code  string   `json:"code,omitempty"`
	Orgs  []string `json:"orgs,omitempty"`
	// Listed (mcp): how many outside servers the conversation may use (D240)
	Listed int `json:"listed,omitempty"`
}

// agentNetRules is what the broker answers about the org rules on the web
// and on MCP servers (its agentnet.go): an org's rule for its data, over
// every layer that holds for its tiles.
type agentNetRules interface {
	AgentWebIn(org string) (rule string, reason string)
	AgentMCPIn(org string) (rule string, reason string)
}

// orgWeb is org's web rule (agentpolicy.WebAsk when no rule store says
// otherwise) and its reason.
func (s *Server) orgWeb(org string) (string, string) {
	if nr, ok := s.AgentWorld.(agentNetRules); ok {
		return nr.AgentWebIn(org)
	}
	return agentpolicy.WebAsk, ""
}

// orgName is what a person reads for org.
func orgName(org string) string {
	if org == agentpolicy.WorkspaceID {
		return "the tiles in no organisation"
	}
	return org
}

// policyOffWhy is why a capability is off for the whole workspace.
func policyOffWhy(what string, byHost bool) string {
	if byHost {
		return what + " is off for this workspace — set by your host"
	}
	return what + " is off for this workspace — an admin can change it (the workspace agent's settings)"
}

// webReach is the web for the conversation under handle h of p's agent.
func (s *Server) webReach(p auth.Principal, h delegation.AgentSession) netReach {
	pol := s.Delegation.NetPolicy()
	if pol.Web.Mode != delegation.WebRead {
		return netReach{State: reachOff, Code: "web-off", Why: policyOffWhy("Reading the web", pol.Web.ByHost)}
	}
	if !s.Delegation.State(p.UserID).Web {
		return netReach{State: reachOff, Code: "web-person", Why: "Reading the web is off in your agent's settings — you can turn it on in Preferences"}
	}
	var asks []string
	for _, org := range h.Tainted {
		switch rule, reason := s.orgWeb(org); rule {
		case agentpolicy.WebNever:
			why := orgName(org) + " keeps its data off the web, and this conversation holds it"
			if reason != "" {
				why = reason + ", and this conversation holds its data"
			}
			return netReach{State: reachOff, Code: "web-org", Orgs: []string{org}, Why: why + " — a new session that hasn't read it can read the web"}
		case agentpolicy.WebAllow:
		default:
			asks = append(asks, org)
		}
	}
	if len(asks) > 0 {
		return netReach{State: reachAsks, Orgs: asks, Why: "This conversation holds data of " + strings.Join(mapNames(asks), ", ") +
			": an address can carry it out, so each page it reads is one you approve"}
	}
	return netReach{State: reachOn}
}

func mapNames(orgs []string) []string {
	out := make([]string, len(orgs))
	for i, o := range orgs {
		out[i] = orgName(o)
	}
	return out
}

// searchReach is a web search for the conversation: off while the web is,
// or asks (a search's words go out with no approval of their own), and off
// otherwise until a provider's native search is built.
func (s *Server) searchReach(p auth.Principal, h delegation.AgentSession) netReach {
	web := s.webReach(p, h)
	switch {
	case web.State == reachOff:
		return netReach{State: reachOff, Code: web.Code, Why: web.Why}
	case web.State == reachAsks:
		return netReach{State: reachOff, Code: "search-taint", Orgs: web.Orgs, Why: web.Why + "; a search can't be approved query by query, so this conversation reads only pages you approve"}
	}
	// a provider's native search, held by the LLM connector, isn't built yet
	// (D239): the policy refuses web.search, and nothing offers it
	return netReach{State: reachOff, Code: "search-off", Why: "Web search isn't available yet: the agent reads the pages whose addresses it knows"}
}

// reachView is a session's reach as its answers carry it.
func (s *Server) reachView(p auth.Principal, h delegation.AgentSession) map[string]netReach {
	return map[string]netReach{"web": s.webReach(p, h), "search": s.searchReach(p, h), "mcp": s.mcpReach(p, h)}
}

// mcpReach is MCP for the conversation (delegatemcp.go answers what each
// server and tool allows).
func (s *Server) mcpReach(p auth.Principal, h delegation.AgentSession) netReach {
	pol := s.Delegation.NetPolicy()
	if pol.MCP.Mode == delegation.MCPOff {
		return netReach{State: reachOff, Code: "mcp-off", Why: policyOffWhy("Using MCP tools", pol.MCP.ByHost)}
	}
	if !s.Delegation.State(p.UserID).MCP {
		return netReach{State: reachOff, Code: "mcp-person", Why: "Using MCP tools is off in your agent's settings — you can turn it on in Preferences"}
	}
	if nr, ok := s.AgentWorld.(agentNetRules); ok {
		for _, org := range h.Tainted {
			if rule, reason := nr.AgentMCPIn(org); rule == agentpolicy.MCPNever {
				return netReach{State: reachOff, Code: "mcp-org", Orgs: []string{org}, Why: orStr(reason, orgName(org)+" keeps its data from every MCP server") +
					", and this conversation holds it — a new session that hasn't read it can use MCP tools"}
			}
		}
	}
	visible, listed := 0, 0
	if pol.MCP.Mode == delegation.MCPListed {
		visible, listed = len(s.mcpVisible(p.UserID)), s.mcpExtCount(p, h)
	}
	if s.mcpTilesNone() && visible == 0 {
		why := "No tile of this workspace is an MCP server"
		if pol.MCP.Mode == delegation.MCPListed {
			why += ", and no outside server is listed for you"
		}
		return netReach{State: reachOff, Code: "mcp-none", Why: why}
	}
	return netReach{State: reachAsks, Why: "Each tool asks you the first time", Listed: listed}
}

// ---- the URL ----

// ownHost reports one of the workspace's own hosts: its external URL's,
// the tiles' domain and the names under it, and what the OwnHost hook
// names (the hostnames its tiles are published at).
func (s *Server) ownHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "xbin" {
		return true // the gateway's own name inside a sandbox
	}
	if s.ExternalURL != "" {
		if u, err := url.Parse(s.ExternalURL); err == nil && strings.EqualFold(u.Hostname(), host) {
			return true
		}
	}
	if td := s.TilesDomain; td != "" {
		if h, _, err := net.SplitHostPort(td); err == nil {
			td = h
		}
		td = strings.ToLower(td)
		if host == td || strings.HasSuffix(host, "."+td) {
			return true
		}
	}
	return s.OwnHost != nil && s.OwnHost(host)
}

// webCheck judges a URL — the first and every redirect's — against the
// lists in force and the workspace's own hosts.
func (s *Server) webCheck(pol delegation.WebInForce) func(u *url.URL) error {
	return func(u *url.URL) error {
		host := strings.ToLower(u.Hostname())
		if s.ownHost(host) {
			return &webfetch.Refusal{Code: "web-host", Status: http.StatusForbidden, Msg: host + " is this workspace's own address: a workspace agent reads the workspace through xbin, not through the web"}
		}
		if ok, which := pol.HostAllows(host); !ok {
			return &webfetch.Refusal{Code: "web-host", Status: http.StatusForbidden, Msg: host + " is refused by " + which}
		}
		return nil
	}
}

// webURL is raw as a URL the conversation may read, or why not.
func (s *Server) webURL(raw string, pol delegation.WebInForce) (*url.URL, error) {
	u, err := webfetch.CheckURL(raw)
	if err != nil {
		return nil, err
	}
	if err := s.webCheck(pol)(u); err != nil {
		return nil, err
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !s.webFetcher(pol).Dials(ip) {
		return nil, &webfetch.Refusal{Code: "web-address", Status: http.StatusForbidden, Msg: ip.Unmap().String() + " isn't a public address: a workspace agent reads the public web only"}
	}
	return u, nil
}

func (s *Server) webFetcher(pol delegation.WebInForce) *webfetch.Fetcher {
	f := webfetch.Fetcher{}
	if s.WebFetch != nil {
		f = *s.WebFetch // tests: a resolver of their own, their servers' address
	}
	f.Check = s.webCheck(pol)
	return &f
}

// webBody reads the body a fetch is asked with: exactly the bytes a ticket
// binds, and the URL they name.
func webBody(w http.ResponseWriter, r *http.Request) ([]byte, string, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<10))
	if err != nil {
		return nil, "", errors.New("the body is at most 8 KiB: {url}")
	}
	var in struct {
		URL string `json:"url"`
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if dec.Decode(&in) != nil || strings.TrimSpace(in.URL) == "" {
		return nil, "", errors.New(`need {url: "https://…"}`)
	}
	return body, in.URL, nil
}

// ---- the route ----

type delegateRowKey struct{}

// delegateRowOf is the audit row the gate writes for r when the handler is
// done (nil for a request the gate didn't run): a handler that knows more
// of what the call was says it here.
func delegateRowOf(r *http.Request) *delegation.Call {
	c, _ := r.Context().Value(delegateRowKey{}).(*delegation.Call)
	return c
}

// webRefuse answers a fetch's refusal and says it in the audit row.
func webRefuse(w http.ResponseWriter, row *delegation.Call, status int, refusal, msg string) {
	if row != nil {
		row.Refused = msg
	}
	WriteJSON(w, status, map[string]any{"error": msg, "refusal": refusal, "class": "web", "docs": delegationDocs})
}

// apiDelegateWebFetch — POST /delegate/web/fetch {url}: the person's agent
// reads one page. → {url, finalUrl, status, contentType, title?, text,
// bytes, truncated, redirects, readWeb}.
func (s *Server) apiDelegateWebFetch(w http.ResponseWriter, r *http.Request) {
	a, ok := s.delegateCaller(w, r, false, false, true)
	if !ok {
		return
	}
	p := a.p
	row := delegateRowOf(r)
	if row != nil {
		row.Class = "web"
	}
	h, ok := s.handleOf(p)
	if !ok {
		webRefuse(w, row, http.StatusForbidden, "no-session", "a fetch runs under an agent session (X-XBin-Agent-Session)")
		return
	}
	body, raw, err := webBody(w, r)
	if err != nil {
		webRefuse(w, row, http.StatusBadRequest, "web-url", err.Error())
		return
	}
	if row != nil {
		row.Path = webfetch.Describe(raw)
	}
	reach := s.webReach(p, h)
	if reach.State == reachOff {
		webRefuse(w, row, http.StatusForbidden, reach.Code, reach.Why)
		return
	}
	pol := s.Delegation.NetPolicy().Web
	u, err := s.webURL(raw, pol)
	if err != nil {
		var rf *webfetch.Refusal
		errors.As(err, &rf)
		webRefuse(w, row, rf.Status, rf.Code, rf.Msg)
		return
	}
	if row != nil {
		row.Path = webfetch.Describe(u.String())
	}
	// the limit before the ticket: a fetch over it spends nothing the person gave
	if ok, wait, whose := s.delegateRT.allowNet("web", p.UserID, h.ID); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait/time.Second)+1))
		webRefuse(w, row, http.StatusTooManyRequests, "rate-limited", netLimitWords("web", whose, wait))
		return
	}
	if reach.State == reachAsks {
		ticket := strings.TrimSpace(r.Header.Get(HeaderTicket))
		if ticket == "" {
			webRefuse(w, row, http.StatusForbidden, "needs-approval", reach.Why+" — ask for this one: POST /api/xbin/delegate/requests {method: \"POST\", path: \"/api/xbin/delegate/web/fetch\", body}, then send the same body with its id as X-XBin-Ticket once "+p.UserID+" approved")
			return
		}
		// the call as the agent made it (its path below /api/xbin is what the
		// handler sees): what the request it filed binds
		req, err := s.Delegation.Redeem(p.UserID, ticket, http.MethodPost, "/api/xbin"+requestTarget(r), "", contentTypeOf(r.Header.Get("Content-Type")), body)
		if err != nil {
			webRefuse(w, row, http.StatusForbidden, "ticket", err.Error())
			return
		}
		if row != nil {
			row.Ticket = req.ID
		}
		s.delegateEvent(p.UserID, "request", req.ID)
	}
	res, err := s.webFetcher(pol).Fetch(r.Context(), u.String())
	if err != nil {
		var rf *webfetch.Refusal
		if errors.As(err, &rf) {
			webRefuse(w, row, rf.Status, rf.Code, rf.Msg)
			return
		}
		webRefuse(w, row, http.StatusBadGateway, "web-unreachable", "the fetch ended: "+err.Error())
		return
	}
	// what the conversation now holds: the web's words
	_ = s.Delegation.MarkWeb(p.UserID, p.Agent, h.ID)
	if row != nil {
		row.Bytes = res.Bytes
	}
	WriteJSON(w, http.StatusOK, map[string]any{"url": res.URL, "finalUrl": res.FinalURL, "status": res.Status, "contentType": res.ContentType,
		"title": res.Title, "text": res.Text, "bytes": res.Bytes, "truncated": res.Truncated, "redirects": res.Redirects, "readWeb": true})
}

// webDry is a fetch's dry run: why it would be refused ("" when it would
// run), and whether it would need its person's approval.
func (s *Server) webDry(p auth.Principal, h delegation.AgentSession, body []byte) (why string, asks bool) {
	reach := s.webReach(p, h)
	if reach.State == reachOff {
		return reach.Why, false
	}
	var in struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(body, &in) != nil || strings.TrimSpace(in.URL) == "" {
		return `a fetch's body is {url: "https://…"}`, false
	}
	if _, err := s.webURL(in.URL, s.Delegation.NetPolicy().Web); err != nil {
		return err.Error(), false
	}
	return "", reach.State == reachAsks
}

// webRequestPreview is what a person is shown of a fetch their agent asks
// to make — or why it can't be asked: a fetch that needs no approval, one
// the reach refuses whatever approves it, a URL that is refused anyway.
func (s *Server) webRequestPreview(p auth.Principal, body []byte) (map[string]any, int, string) {
	h, ok := s.handleOf(p)
	if !ok {
		return nil, http.StatusForbidden, "a fetch is asked for under an agent session (X-XBin-Agent-Session)"
	}
	var in struct {
		URL string `json:"url"`
	}
	if StrictDecode(body, &in) != nil || strings.TrimSpace(in.URL) == "" { // read one way only (D252)
		return nil, http.StatusBadRequest, `a fetch's body is {url: "https://…"}, exactly as it will be sent: "url" once, in lower case, nothing else`
	}
	reach := s.webReach(p, h)
	switch reach.State {
	case reachOff:
		return nil, http.StatusForbidden, reach.Why
	case reachOn:
		return nil, http.StatusBadRequest, "this fetch needs no approval: make it"
	}
	u, err := s.webURL(in.URL, s.Delegation.NetPolicy().Web)
	if err != nil {
		var rf *webfetch.Refusal
		errors.As(err, &rf)
		return nil, rf.Status, rf.Msg
	}
	// the person is shown the address in the one spelling the fetch will
	// name (CheckURL's: the host in its ASCII form, lower case, no
	// fragment); the ticket binds the body that spells it
	return map[string]any{"class": "web", "url": u.String(), "host": u.Hostname(), "orgs": reach.Orgs, "why": reach.Why, "reversible": false}, 0, ""
}
