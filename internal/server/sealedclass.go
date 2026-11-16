package server

// sealedclass.go — what /api/xbin answers when a route is asked about a
// sealed component (D227). A sealed component is listed, read and run like
// a tile, but most of what the API does to a tile doesn't exist for it: it
// has no source, no terminal, no deployments, no owner, and what it may do
// is fixed by the release. So every route pattern has a row here, as it
// has one in deployclass.go and partitionclass.go, and handleAPI asks
// sealedGate before the handler runs:
//
//   - SealedRefused: a sealed path in one of the row's parameters is
//     refused (400), for admins and the root token too;
//   - SealedSelf: only the sealed component's backend passes — its
//     instance credential (403 otherwise): its resources, secrets,
//     registrations and status are its own, not an admin's to read or
//     write, and not its page's (a frame token of one is a viewer's);
//   - SealedHandled: the handler knows the rule (What says it);
//   - SealedNoTile: the route names no tile.
//
// The namespace is structural: any name under xbin/ (and res:xbin/…) gets
// the row's answer, whether or not a component is registered there. A guard
// over the route inventory keeps the table complete (internal/apicheck's
// TestSealedRouteClasses), and internal/boot's TestSealedRouteAnswers
// drives every Refused, Self and Handled row through a real daemon.
//
// THE GATE IS NOT THE BOUNDARY. It reads a request's parameters a second
// time, beside the handler, and two readers of one body can disagree; it is
// the early answer with the reason, for a caller who made a mistake. What
// keeps a sealed component sealed are the refusals where the work is done —
// the functions that act on a component (broker/sealedguard.go lists them)
// and the credential rule for its page's token (sealedViewerAllowed) — and
// internal/boot's TestSealedBoundaryWithoutTheGate runs every Refused and
// Self row with this gate switched off.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/util"
)

// SealedClass is a route's class for sealed components.
type SealedClass uint8

const (
	SealedUnclassified SealedClass = iota // no row: the guard fails
	SealedNoTile
	SealedRefused
	SealedSelf
	SealedHandled
)

func (c SealedClass) String() string {
	switch c {
	case SealedNoTile:
		return "no-tile"
	case SealedRefused:
		return "refused"
	case SealedSelf:
		return "self"
	case SealedHandled:
		return "handled"
	}
	return "unclassified"
}

// SealedRule is one route's row: its class, and for Refused and Self where
// the route names a tile or a resource.
type SealedRule struct {
	Class SealedClass
	Query []string // query parameters
	Body  []string // top-level fields of a JSON body: a string, or a list of strings
	Rest  bool     // the path below the route's first segment
	What  string   // why, completing "<path> is a system component, managed by xbin: …"
}

// sealedNoAgentWhat completes the refusal of a sealed component as what a
// person's workspace agent would act in.
const sealedNoAgentWhat = "a person's agent does nothing in it: nothing calls it but its own page"

// sealedNoPreview completes the refusal of a sealed component as a
// preview's target.
const sealedNoPreview = "it is not previewed: its page is each person's own to open, and no render shows it"

// SealedDocs is the page a sealed-component refusal points at.
const SealedDocs = "/docs/sealed.md"

// SealedRefusal is the text of a refusal about the sealed component (or
// name under xbin/) path.
func SealedRefusal(path, what string) string { return util.SealedRefusal(path, what) }

// SealedRules returns a copy of the table, pattern → rule.
func SealedRules() map[string]SealedRule {
	out := make(map[string]SealedRule, len(sealedRules))
	for p, r := range sealedRules {
		out[p] = r
	}
	return out
}

// sealedName reports whether v names a sealed component, something below
// one, or one's resource; and the component-ish name to show.
func sealedName(v string) (string, bool) {
	v = strings.Trim(strings.TrimSpace(v), "/")
	return v, util.SealedTarget(v)
}

// sealedOwn reports whether name (a path, a path below, or a resource) is
// comp's own.
func sealedOwn(comp, name string) bool {
	name = strings.TrimPrefix(strings.TrimPrefix(name, "res:"), "code:")
	return comp != "" && (name == comp || strings.HasPrefix(name, comp+"/"))
}

// sealedGate answers a request that names a sealed component on a Refused
// or Self route; nil lets the handler run. r2 is the inner request (its
// path what follows /api/xbin). A body it had to read is put back.
func (s *Server) sealedGate(r2 *http.Request) http.HandlerFunc {
	if s.apiMux == nil || s.SealedGateOff {
		return nil
	}
	_, pattern := s.apiMux.Handler(r2)
	rule, classified := sealedRules[pattern]
	q := r2.URL.Query()
	_, tail, _ := strings.Cut(strings.TrimPrefix(r2.URL.Path, "/"), "/")
	// One spelling of a sealed path reaches a handler, on every route:
	// "/xbin/x", "xbin//x" and "./xbin/x/" are refused here, in the path
	// and the query, so nothing behind holds two spellings of one
	// component (util.SealedSpelledOddly; the sealed tests themselves see
	// every spelling, util.SealedPath).
	odd := util.SealedSpelledOddly(tail)
	for _, vs := range q {
		for _, v := range vs {
			odd = odd || util.SealedSpelledOddly(v)
		}
	}
	if odd {
		return refusal(http.StatusBadRequest, sealedSpelling)
	}
	if pattern != "" && !classified {
		// A route without a row (the CI guard, apicheck, fails for one):
		// until it has a rule it takes no sealed path at all.
		mentions := util.SealedTarget(tail)
		for _, vs := range q {
			for _, v := range vs {
				mentions = mentions || util.SealedTarget(v)
			}
		}
		if !mentions && r2.Body != nil && r2.Body != http.NoBody {
			body := r2.Body
			b, _ := io.ReadAll(io.LimitReader(body, sealedGateBody+1))
			r2.Body = struct {
				io.Reader
				io.Closer
			}{io.MultiReader(bytes.NewReader(b), body), body}
			for _, form := range []string{`"xbin/`, `"/xbin/`, `"res:xbin/`, `"code:xbin/`, `"xbin\/`, `"\/xbin\/`, `"res:xbin\/`} {
				mentions = mentions || bytes.Contains(b, []byte(form)) // a JSON string that begins as a sealed path
			}
		}
		if mentions {
			return refusal(http.StatusBadRequest, "this route has no rule for system components yet (server/sealedclass.go): it takes no path under xbin/")
		}
		return nil
	}
	var names []string
	for _, k := range rule.Query {
		names = append(names, q[k]...)
	}
	if rule.Rest && tail != "" {
		names = append(names, tail)
	}
	bodyKeys := rule.Body
	if rule.Class == SealedHandled {
		bodyKeys = sealedTileFields // the spelling alone is checked for these rows
	}
	if len(bodyKeys) > 0 && r2.Body != nil && r2.Body != http.NoBody {
		// These are JSON control calls: the first sealedGateBody bytes are
		// read and put back. A longer body is the handler's alone to read —
		// the gate is the early answer, not the boundary.
		body := r2.Body
		b, err := io.ReadAll(io.LimitReader(body, sealedGateBody+1))
		if err != nil {
			body.Close()
			return refusal(http.StatusBadRequest, "the request body couldn't be read")
		}
		r2.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(b), body), body}
		if len(b) <= sealedGateBody {
			got, twice := sealedBodyNames(b, bodyKeys)
			if twice != "" {
				return refusal(http.StatusBadRequest, "the request body names "+twice+" twice")
			}
			for _, v := range append(got, sealedBodyKeys(b)...) {
				if util.SealedSpelledOddly(v) {
					return refusal(http.StatusBadRequest, sealedSpelling)
				}
			}
			names = append(names, got...)
		}
	}
	if rule.Class != SealedRefused && rule.Class != SealedSelf {
		return nil
	}
	p := auth.PrincipalOf(r2)
	for _, v := range names {
		name, sealed := sealedName(v)
		switch {
		case !sealed:
		case rule.Class == SealedSelf && sealedOwn(p.Component, name) && p.Via == "instance":
			// its backend's own credential — never its page's, a viewer's (sealed.go)
		case rule.Class == SealedSelf:
			return sealedRefusal(http.StatusForbidden, name, rule.What)
		default:
			return sealedRefusal(http.StatusBadRequest, name, rule.What)
		}
	}
	return nil
}

// sealedSpelling is the refusal of a sealed path written another way than
// xbin/<name>.
const sealedSpelling = "write a system component's path as xbin/<name>: no leading, trailing or doubled slash, no \"./\" or \"../\""

// sealedTileFields are the body fields a route that the handler decides
// (SealedHandled) may name a tile in: checked for the spelling only.
var sealedTileFields = []string{"component", "tile", "path", "target", "from", "to", "cwd", "scope", "resource", "provider", "providers", "source", "id"}

// sealedBodyKeys are the keys of the objects nested in the JSON object body
// begins with (to any depth): where a body is a map from tile paths
// ({"tiles": {"xbin/x": "read"}}), the paths are keys.
func sealedBodyKeys(body []byte) []string {
	dec := json.NewDecoder(bytes.NewReader(body))
	var keys []string
	type frame struct{ object, key bool }
	var stack []frame
	for {
		t, err := dec.Token()
		if err != nil {
			return keys
		}
		switch v := t.(type) {
		case json.Delim:
			switch v {
			case '{':
				stack = append(stack, frame{object: true, key: true})
				continue
			case '[':
				stack = append(stack, frame{})
				continue
			default:
				if len(stack) > 0 {
					stack = stack[:len(stack)-1]
				}
				if len(stack) == 0 {
					return keys // the first value, as the handlers' decoder reads
				}
			}
		case string:
			if n := len(stack); n > 0 && stack[n-1].object && stack[n-1].key {
				if n > 1 { // nested: the top level's keys are field names
					keys = append(keys, v)
				}
				stack[n-1].key = false
				continue
			}
		}
		if n := len(stack); n > 0 && stack[n-1].object {
			stack[n-1].key = true // a value ended: the next string is a key
		}
	}
}

// sealedGateBody is how much of a body the gate reads.
const sealedGateBody = 1 << 20

// sealedBodyNames are the values of the fields keys in the JSON object body
// begins with, read the way the handlers' decoder (encoding/json into a
// struct) reads them: the first JSON value of the body (what follows it is
// ignored there too), and keys matched without regard to case — "Target"
// fills a field tagged "target". A value is a string or a list of strings;
// anything else names nothing. twice names a key of keys the object has
// more than once (in any case): a struct decode and this one can then
// disagree about its value — {"tile":"xbin/x","tile":null} leaves a struct
// field at xbin/x — so the gate refuses such a body outright.
func sealedBodyNames(body []byte, keys []string) (names []string, twice string) {
	dec := json.NewDecoder(bytes.NewReader(body))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, "" // not an object: the handler refuses it
	}
	seen := map[string]bool{}
	for dec.More() {
		t, err := dec.Token()
		name, ok := t.(string)
		var raw json.RawMessage
		if err != nil || !ok || dec.Decode(&raw) != nil {
			return names, "" // malformed from here: the handler's decoder fails too
		}
		i := slices.IndexFunc(keys, func(k string) bool { return strings.EqualFold(k, name) })
		if i < 0 {
			continue
		}
		if seen[keys[i]] {
			return nil, keys[i]
		}
		seen[keys[i]] = true
		var one string
		var many []string
		switch {
		case json.Unmarshal(raw, &one) == nil:
			names = append(names, one)
		case json.Unmarshal(raw, &many) == nil:
			names = append(names, many...)
		}
	}
	return names, ""
}

// sealedRefusal answers with the API's error shape, pointing at the sealed
// components page.
func sealedRefusal(status int, name, what string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		WriteError(w, status, SealedRefusal(name, what), SealedDocs)
	}
}

// sealedRules is the table.
var sealedRules = map[string]SealedRule{
	// ---- refused: the operation doesn't exist for a sealed component ----
	"DELETE /bindings":                     {Class: SealedRefused, Body: []string{"provider", "providers"}, What: "it is never the provider of a binding"},
	"DELETE /grants":                       {Class: SealedRefused, Body: []string{"from", "target"}, What: "its grants are fixed by the xbin release, and nothing is granted a call into it or its resources"},
	"DELETE /partitions/consents":          {Class: SealedRefused, Query: []string{"from", "to"}, Body: []string{"from", "to"}, What: "no tile reaches people's partitions of it"},
	"DELETE /sandboxes/{name}":             {Class: SealedRefused, Query: []string{"tile"}, What: "it runs no tile sandboxes"},
	"GET /checkpoints/{rest...}":           {Class: SealedRefused, Rest: true, What: "it has no checkpoints: its code is the xbin release's"},
	"GET /code/file":                       {Class: SealedRefused, Query: []string{"component"}, What: "it has no source to read: its code is the xbin release's"},
	"GET /code/pr":                         {Class: SealedRefused, Query: []string{"target"}, What: "it takes no change proposals: its code is the xbin release's"},
	"GET /code/pr/series":                  {Class: SealedRefused, Query: []string{"target"}, What: "it takes no change proposals: its code is the xbin release's"},
	"GET /code/prs":                        {Class: SealedRefused, Query: []string{"target", "from"}, What: "it takes no change proposals: its code is the xbin release's"},
	"GET /code/tree":                       {Class: SealedRefused, Query: []string{"component"}, What: "it has no source to read: its code is the xbin release's"},
	"GET /deployments":                     {Class: SealedRefused, Query: []string{"tile"}, Body: []string{"tile"}, What: "it has no deployments: it always runs the xbin release's code"},
	"GET /deployments/backups":             {Class: SealedRefused, Query: []string{"tile"}, Body: []string{"tile"}, What: "it has no deployments: it always runs the xbin release's code"},
	"GET /deployments/diff":                {Class: SealedRefused, Query: []string{"tile"}, Body: []string{"tile"}, What: "it has no deployments: it always runs the xbin release's code"},
	"GET /deployments/log":                 {Class: SealedRefused, Query: []string{"tile"}, Body: []string{"tile"}, What: "it has no deployments: it always runs the xbin release's code"},
	"GET /git/activity":                    {Class: SealedRefused, Query: []string{"component"}, What: "it has no source to read: its code is the xbin release's"},
	"GET /git/diff":                        {Class: SealedRefused, Query: []string{"component"}, What: "it has no source to read: its code is the xbin release's"},
	"GET /git/log":                         {Class: SealedRefused, Query: []string{"component"}, What: "it has no source to read: its code is the xbin release's"},
	"GET /owner/preview":                   {Class: SealedRefused, Query: []string{"tile"}, What: "it has no owner: workspace admins manage it"},
	"GET /term-net":                        {Class: SealedRefused, Query: []string{"tile"}, What: "it has no terminal"},
	"POST /bindings":                       {Class: SealedRefused, Body: []string{"provider", "providers"}, What: "it is never the provider of a binding"},
	"POST /builtins/import":                {Class: SealedRefused, Body: []string{"path"}, What: "nothing is created under xbin/"},
	"POST /builtins/update":                {Class: SealedRefused, Body: []string{"id"}, What: "it is updated with xbin itself"},
	"POST /clone":                          {Class: SealedRefused, Body: []string{"from", "to"}, What: "it can't be cloned, and nothing is created under xbin/"},
	"POST /code/pr/comment":                {Class: SealedRefused, Body: []string{"target", "from"}, What: "it takes no change proposals: its code is the xbin release's"},
	"POST /code/pr/state":                  {Class: SealedRefused, Body: []string{"target", "from"}, What: "it takes no change proposals: its code is the xbin release's"},
	"POST /code/prs":                       {Class: SealedRefused, Body: []string{"target", "from"}, What: "it takes no change proposals: its code is the xbin release's"},
	"POST /create":                         {Class: SealedRefused, Body: []string{"path"}, What: "nothing is created under xbin/"},
	"POST /deployments/add":                {Class: SealedRefused, Query: []string{"tile"}, Body: []string{"tile"}, What: "it has no deployments: it always runs the xbin release's code"},
	"POST /deployments/always-on":          {Class: SealedRefused, Query: []string{"tile"}, Body: []string{"tile"}, What: "it has no deployments: it always runs the xbin release's code"},
	"POST /deployments/backup":             {Class: SealedRefused, Query: []string{"tile"}, Body: []string{"tile"}, What: "it has no deployments: it always runs the xbin release's code"},
	"POST /deployments/backup-schedule":    {Class: SealedRefused, Query: []string{"tile"}, Body: []string{"tile"}, What: "it has no deployments: it always runs the xbin release's code"},
	"POST /deployments/branch":             {Class: SealedRefused, Query: []string{"tile"}, Body: []string{"tile"}, What: "it has no deployments: it always runs the xbin release's code"},
	"POST /deployments/deliveries":         {Class: SealedRefused, Query: []string{"tile"}, Body: []string{"tile"}, What: "it has no deployments: it always runs the xbin release's code"},
	"POST /deployments/deploy":             {Class: SealedRefused, Query: []string{"tile"}, Body: []string{"tile"}, What: "it has no deployments: it always runs the xbin release's code"},
	"POST /deployments/edge":               {Class: SealedRefused, Query: []string{"tile"}, Body: []string{"tile"}, What: "it has no deployments: it always runs the xbin release's code"},
	"POST /deployments/limits":             {Class: SealedRefused, Query: []string{"tile"}, Body: []string{"tile"}, What: "it has no deployments: it always runs the xbin release's code"},
	"POST /deployments/live-reload/attach": {Class: SealedRefused, Query: []string{"tile"}, Body: []string{"tile"}, What: "it has no deployments: it always runs the xbin release's code"},
	"POST /deployments/live-reload/now":    {Class: SealedRefused, Query: []string{"tile"}, Body: []string{"tile"}, What: "it has no deployments: it always runs the xbin release's code"},
	"POST /deployments/live-reload/pause":  {Class: SealedRefused, Query: []string{"tile"}, Body: []string{"tile"}, What: "it has no deployments: it always runs the xbin release's code"},
	"POST /deployments/live-reload/resume": {Class: SealedRefused, Query: []string{"tile"}, Body: []string{"tile"}, What: "it has no deployments: it always runs the xbin release's code"},
	"POST /deployments/primary":            {Class: SealedRefused, Query: []string{"tile"}, Body: []string{"tile"}, What: "it has no deployments: it always runs the xbin release's code"},
	"POST /deployments/promote":            {Class: SealedRefused, Query: []string{"tile"}, Body: []string{"tile"}, What: "it has no deployments: it always runs the xbin release's code"},
	"POST /deployments/protect":            {Class: SealedRefused, Query: []string{"tile"}, Body: []string{"tile"}, What: "it has no deployments: it always runs the xbin release's code"},
	"POST /deployments/purge":              {Class: SealedRefused, Query: []string{"tile"}, Body: []string{"tile"}, What: "it has no deployments: it always runs the xbin release's code"},
	"POST /deployments/remove":             {Class: SealedRefused, Query: []string{"tile"}, Body: []string{"tile"}, What: "it has no deployments: it always runs the xbin release's code"},
	"POST /deployments/reset":              {Class: SealedRefused, Query: []string{"tile"}, Body: []string{"tile"}, What: "it has no deployments: it always runs the xbin release's code"},
	"POST /deployments/restore":            {Class: SealedRefused, Query: []string{"tile"}, Body: []string{"tile"}, What: "it has no deployments: it always runs the xbin release's code"},
	"POST /deployments/rollback":           {Class: SealedRefused, Query: []string{"tile"}, Body: []string{"tile"}, What: "it has no deployments: it always runs the xbin release's code"},
	"POST /deployments/run-now":            {Class: SealedRefused, Query: []string{"tile"}, Body: []string{"tile"}, What: "it has no deployments: it always runs the xbin release's code"},
	"POST /deployments/seed":               {Class: SealedRefused, Query: []string{"tile"}, Body: []string{"tile"}, What: "it has no deployments: it always runs the xbin release's code"},
	"POST /deployments/vault-copy":         {Class: SealedRefused, Query: []string{"tile"}, Body: []string{"tile"}, What: "it has no deployments: it always runs the xbin release's code"},
	"POST /git/import":                     {Class: SealedRefused, Body: []string{"path"}, What: "nothing is created under xbin/"},
	"POST /go-build-versions/dismiss":      {Class: SealedRefused, Body: []string{"tile"}, What: "it is built by the xbin release"},
	"POST /grants":                         {Class: SealedRefused, Body: []string{"from", "target"}, What: "its grants are fixed by the xbin release, and nothing is granted a call into it or its resources"},
	"POST /owner":                          {Class: SealedRefused, Body: []string{"tile"}, What: "it has no owner: workspace admins manage it"},
	"POST /partitions/consents":            {Class: SealedRefused, Query: []string{"from", "to"}, Body: []string{"from", "to"}, What: "no tile reaches people's partitions of it"},
	"POST /partitions/mode":                {Class: SealedRefused, Body: []string{"tile"}, What: "its partition mode and its code are the xbin release's"},
	"POST /partitions/reviewed":            {Class: SealedRefused, Body: []string{"tile"}, What: "its partition mode and its code are the xbin release's"},
	"POST /sandboxes":                      {Class: SealedRefused, Body: []string{"tile"}, What: "it runs no tile sandboxes"},
	"POST /sandboxes/{name}/stop":          {Class: SealedRefused, Query: []string{"tile"}, What: "it runs no tile sandboxes"},
	"POST /templates/new":                  {Class: SealedRefused, Body: []string{"path", "source"}, What: "it is no template, and nothing is created under xbin/"},
	"POST /term/sessions":                  {Class: SealedRefused, Body: []string{"cwd"}, What: "it has no terminal and no agent sessions"},
	"PUT /chrome":                          {Class: SealedRefused, Body: []string{"path"}, What: "what it is trusted with is fixed by the xbin release"},
	"PUT /iface-instances":                 {Class: SealedRefused, Body: []string{"component"}, What: "it provides no interface and publishes no endpoint"},
	"PUT /ingress-hosts":                   {Class: SealedRefused, Body: []string{"component"}, What: "it provides no interface and publishes no endpoint"},
	// ---- its own alone: its resources, secrets and registrations ----
	"DELETE /blob/{rest...}":           {Class: SealedSelf, Rest: true, What: "only it reads and writes its own resources"},
	"DELETE /bus/subscriptions/{name}": {Class: SealedSelf, Query: []string{"component"}, What: "only it registers its own deliveries"},
	"DELETE /cron/jobs/{name}":         {Class: SealedSelf, Query: []string{"component"}, What: "only it registers its own deliveries"},
	"DELETE /kv/{rest...}":             {Class: SealedSelf, Rest: true, What: "only it reads and writes its own resources"},
	"DELETE /vault/{rest...}":          {Class: SealedSelf, Rest: true, What: "only it reads and writes its own secrets"},
	"GET /blob/{rest...}":              {Class: SealedSelf, Rest: true, What: "only it reads and writes its own resources"},
	"GET /kv/{rest...}":                {Class: SealedSelf, Rest: true, What: "only it reads and writes its own resources"},
	"GET /vault/{rest...}":             {Class: SealedSelf, Rest: true, What: "only it reads and writes its own secrets"},
	"POST /bus/publish":                {Class: SealedSelf, Body: []string{"resource"}, What: "only it publishes on its own bus"},
	"POST /tile-report":                {Class: SealedSelf, Query: []string{"component"}, Body: []string{"component"}, What: "only it reports its own status"},
	"PUT /blob/{rest...}":              {Class: SealedSelf, Rest: true, What: "only it reads and writes its own resources"},
	"PUT /bus/subscriptions":           {Class: SealedSelf, Body: []string{"component", "resource"}, What: "only it registers its own deliveries"},
	"PUT /cron/jobs":                   {Class: SealedSelf, Body: []string{"component", "resource"}, What: "only it registers its own deliveries"},
	"PUT /kv/{rest...}":                {Class: SealedSelf, Rest: true, What: "only it reads and writes its own resources"},
	"PUT /vault/{rest...}":             {Class: SealedSelf, Rest: true, What: "only it reads and writes its own secrets"},
	// ---- the handler applies the rule (what) ----
	"DELETE /access-requests":       {Class: SealedHandled, What: "as for a tile"},
	"DELETE /backup-schedule":       {Class: SealedHandled, What: "its data alone is archived and restored; a restore goes through its data stamp"},
	"DELETE /limits":                {Class: SealedHandled, What: "an admin sets its limits as a tile's"},
	"DELETE /partitions/binds":      {Class: SealedHandled, What: "people's partitions of it are managed as a partitioned tile's"},
	"DELETE /partitions/share-log":  {Class: SealedHandled, What: "people's partitions of it are managed as a partitioned tile's"},
	"GET /access":                   {Class: SealedHandled, What: "as for a tile"},
	"GET /access-matrix":            {Class: SealedHandled, What: "listed only when asked (?sealed=1)"},
	"GET /agent/history":            {Class: SealedHandled, What: "a filter that matches nothing"},
	"GET /auth-overview":            {Class: SealedHandled, What: "listed only when asked (?sealed=1)"},
	"GET /backends":                 {Class: SealedHandled, What: "listed only when asked (?sealed=1)"},
	"GET /backups":                  {Class: SealedHandled, What: "its data alone is archived and restored; a restore goes through its data stamp"},
	"GET /bus/subscriptions":        {Class: SealedHandled, What: "listed only when asked (?sealed=1)"},
	"GET /components":               {Class: SealedHandled, What: "listed only when asked (?sealed=1)"},
	"GET /components/{path...}":     {Class: SealedHandled, What: "as for a tile"},
	"GET /cron/jobs":                {Class: SealedHandled, What: "listed only when asked (?sealed=1)"},
	"GET /frame-token":              {Class: SealedHandled, What: "as for a tile"},
	"GET /grants":                   {Class: SealedHandled, What: "listed only when asked (?sealed=1)"},
	"GET /limits":                   {Class: SealedHandled, What: "an admin sets its limits as a tile's"},
	"GET /logs":                     {Class: SealedHandled, What: "its own and the admins'"},
	"GET /owner":                    {Class: SealedHandled, What: "as for a tile"},
	"GET /partitions":               {Class: SealedHandled, What: "people's partitions of it are managed as a partitioned tile's"},
	"GET /partitions/backups":       {Class: SealedHandled, What: "people's partitions of it are managed as a partitioned tile's"},
	"GET /partitions/binds":         {Class: SealedHandled, What: "people's partitions of it are managed as a partitioned tile's"},
	"GET /partitions/ledger":        {Class: SealedHandled, What: "people's partitions of it are managed as a partitioned tile's"},
	"GET /resources":                {Class: SealedHandled, What: "listed only when asked (?sealed=1)"},
	"GET /runtime":                  {Class: SealedHandled, What: "listed only when asked (?sealed=1)"},
	"GET /sandboxes":                {Class: SealedHandled, What: "a filter that matches nothing"},
	"GET /sealed":                   {Class: SealedHandled, What: "as for a tile"},
	"GET /term/sessions":            {Class: SealedHandled, What: "a filter that matches nothing"},
	"GET /tile-assets":              {Class: SealedHandled, What: "as for a tile"},
	"GET /tile-status":              {Class: SealedHandled, What: "as for a tile"},
	"GET /vaults":                   {Class: SealedHandled, What: "listed only when asked (?sealed=1)"},
	"PATCH /orgs/{org}":             {Class: SealedHandled, What: "an entry above read for it is refused; a pattern gives read at most"},
	"PATCH /users/{id}":             {Class: SealedHandled, What: "an entry above read for it is refused; a pattern gives read at most"},
	"POST /access-requests":         {Class: SealedHandled, What: "read access or none: the levels above read don't exist on it"},
	"POST /access-requests/approve": {Class: SealedHandled, What: "read access or none: the levels above read don't exist on it"},
	"POST /backup":                  {Class: SealedHandled, What: "its data alone is archived and restored; a restore goes through its data stamp"},
	"POST /backup-schedule":         {Class: SealedHandled, What: "its data alone is archived and restored; a restore goes through its data stamp"},
	"POST /backup/erase":            {Class: SealedHandled, What: "its data alone is archived and restored; a restore goes through its data stamp"},
	"POST /lifecycle":               {Class: SealedHandled, What: "enabled, disabled or hidden; never offloaded"},
	"POST /limits":                  {Class: SealedHandled, What: "an admin sets its limits as a tile's"},
	"POST /orgs":                    {Class: SealedHandled, What: "an entry above read for it is refused; a pattern gives read at most"},
	"POST /partitions/binds":        {Class: SealedHandled, What: "people's partitions of it are managed as a partitioned tile's"},
	"POST /partitions/limits":       {Class: SealedHandled, What: "an admin sets its limits as a tile's"},
	"POST /partitions/purge":        {Class: SealedHandled, What: "people's partitions of it are managed as a partitioned tile's"},
	"POST /partitions/reset":        {Class: SealedHandled, What: "people's partitions of it are managed as a partitioned tile's"},
	"POST /partitions/restore":      {Class: SealedHandled, What: "people's partitions of it are managed as a partitioned tile's"},
	"POST /partitions/share-log":    {Class: SealedHandled, What: "people's partitions of it are managed as a partitioned tile's"},
	"POST /partitions/stop":         {Class: SealedHandled, What: "people's partitions of it are managed as a partitioned tile's"},
	"POST /restore":                 {Class: SealedHandled, What: "its data alone is archived and restored; a restore goes through its data stamp"},
	"POST /users":                   {Class: SealedHandled, What: "an entry above read for it is refused; a pattern gives read at most"},
	"PUT /access":                   {Class: SealedHandled, What: "read access or none: the levels above read don't exist on it"},
	"PUT /defaults":                 {Class: SealedHandled, What: "an entry above read for it is refused; a pattern gives read at most"},
	"PUT /permission-sets/{name}":   {Class: SealedHandled, What: "an entry above read for it is refused; a pattern gives read at most"},
	"PUT /screens/default":          {Class: SealedHandled, What: "it may be placed on a screen like a tile"},
	"PUT /screens/folders":          {Class: SealedHandled, What: "it may be placed on a screen like a tile"},
	"PUT /screens/org":              {Class: SealedHandled, What: "it may be placed on a screen like a tile"},
	// ---- no tile is named ----
	"/sandboxes/{name}/ports/{port}/{path...}":             {Class: SealedNoTile},
	"DELETE /agent/history/{id}":                           {Class: SealedNoTile},
	"DELETE /devices/push/{deviceId}":                      {Class: SealedNoTile},
	"DELETE /devices/push/{deviceId}/activities/{session}": {Class: SealedNoTile},
	"DELETE /devices/{id}":                                 {Class: SealedNoTile},
	"DELETE /net-sets/{name}":                              {Class: SealedNoTile},
	"DELETE /orgs/{org}":                                   {Class: SealedNoTile},
	"DELETE /orgs/{org}/members/{user}":                    {Class: SealedNoTile},
	"DELETE /permission-sets/{name}":                       {Class: SealedNoTile},
	"DELETE /prefs/{key}":                                  {Class: SealedNoTile},
	"DELETE /push/config":                                  {Class: SealedNoTile},
	"DELETE /push/devices/{user}":                          {Class: SealedNoTile},
	"DELETE /push/devices/{user}/{deviceId}":               {Class: SealedNoTile},
	"DELETE /sandboxes/{name}/execs/{id}":                  {Class: SealedNoTile},
	"DELETE /sandboxes/{name}/snapshots/{sid}":             {Class: SealedNoTile},
	"DELETE /screens/org":                                  {Class: SealedNoTile},
	"DELETE /term/sessions/{id}":                           {Class: SealedNoTile},
	"DELETE /term/sessions/{id}/queue":                     {Class: SealedNoTile},
	"DELETE /term/sessions/{id}/queue/{qid}":               {Class: SealedNoTile},
	"DELETE /users/{id}":                                   {Class: SealedNoTile},
	"DELETE /users/{id}/sessions":                          {Class: SealedNoTile},
	"GET /access-requests":                                 {Class: SealedNoTile},
	"GET /access/{user}":                                   {Class: SealedNoTile},
	"GET /agent/history/{id}/events":                       {Class: SealedNoTile},
	"GET /agent/providers":                                 {Class: SealedNoTile},
	"GET /alerts":                                          {Class: SealedNoTile},
	"GET /app-tunnel":                                      {Class: SealedNoTile},
	"GET /app-tunnel/client":                               {Class: SealedNoTile},
	"GET /auth-settings":                                   {Class: SealedNoTile},
	"GET /backup-keys":                                     {Class: SealedNoTile},
	"GET /backup-schedule":                                 {Class: SealedNoTile},
	"GET /bindings":                                        {Class: SealedNoTile},
	"GET /branding":                                        {Class: SealedNoTile},
	"GET /builtins":                                        {Class: SealedNoTile},
	"GET /builtins/updates":                                {Class: SealedNoTile},
	"GET /chrome":                                          {Class: SealedNoTile},
	"GET /code/prs/summary":                                {Class: SealedNoTile},
	"GET /defaults":                                        {Class: SealedNoTile},
	"GET /devices":                                         {Class: SealedNoTile},
	"GET /devices/limits":                                  {Class: SealedNoTile},
	"GET /devices/pairing/{id}":                            {Class: SealedNoTile},
	"GET /devices/push":                                    {Class: SealedNoTile},
	"GET /git/remote-info":                                 {Class: SealedNoTile},
	"GET /go-build-versions":                               {Class: SealedNoTile},
	"GET /gpus":                                            {Class: SealedNoTile},
	"GET /ingress":                                         {Class: SealedNoTile},
	"GET /ingress-routes":                                  {Class: SealedNoTile},
	"GET /login/methods":                                   {Class: SealedNoTile},
	"GET /native-runtime":                                  {Class: SealedNoTile},
	"GET /net-sets":                                        {Class: SealedNoTile},
	"GET /openapi.json":                                    {Class: SealedNoTile},
	"GET /orgs":                                            {Class: SealedNoTile},
	"GET /orgs/{org}/policy":                               {Class: SealedNoTile},
	"GET /partitions/consents":                             {Class: SealedNoTile},
	"GET /partitions/edges":                                {Class: SealedNoTile},
	"GET /partitions/mail":                                 {Class: SealedNoTile},
	"GET /permission-sets":                                 {Class: SealedNoTile},
	"GET /policy":                                          {Class: SealedNoTile},
	"GET /prefs":                                           {Class: SealedNoTile},
	"GET /prefs/{key}":                                     {Class: SealedNoTile},
	"GET /push/config":                                     {Class: SealedNoTile},
	"GET /push/devices":                                    {Class: SealedNoTile},
	"GET /push/prefs":                                      {Class: SealedNoTile},
	"GET /sandboxes/policy":                                {Class: SealedNoTile},
	"GET /sandboxes/runtime":                               {Class: SealedNoTile},
	"GET /sandboxes/{name}":                                {Class: SealedNoTile},
	"GET /sandboxes/{name}/execs":                          {Class: SealedNoTile},
	"GET /sandboxes/{name}/execs/{id}":                     {Class: SealedNoTile},
	"GET /sandboxes/{name}/execs/{id}/output":              {Class: SealedNoTile},
	"GET /sandboxes/{name}/execs/{id}/stdio":               {Class: SealedNoTile},
	"GET /sandboxes/{name}/execs/{id}/tty":                 {Class: SealedNoTile},
	"GET /sandboxes/{name}/files/content":                  {Class: SealedNoTile},
	"GET /sandboxes/{name}/files/list":                     {Class: SealedNoTile},
	"GET /sandboxes/{name}/files/stat":                     {Class: SealedNoTile},
	"GET /sandboxes/{name}/snapshots":                      {Class: SealedNoTile},
	"GET /sandboxes/{name}/tar":                            {Class: SealedNoTile},
	"GET /sandboxes/{name}/tty":                            {Class: SealedNoTile},
	"GET /screens":                                         {Class: SealedNoTile},
	"GET /sessions":                                        {Class: SealedNoTile},
	"GET /status":                                          {Class: SealedNoTile},
	"GET /templates":                                       {Class: SealedNoTile},
	"GET /templates/updates":                               {Class: SealedNoTile},
	"GET /templates/{repo}/{rest...}":                      {Class: SealedNoTile},
	"GET /term/sessions/{id}":                              {Class: SealedNoTile},
	"GET /term/sessions/{id}/diff":                         {Class: SealedNoTile},
	"GET /term/sessions/{id}/events":                       {Class: SealedNoTile},
	"GET /term/sessions/{id}/log":                          {Class: SealedNoTile},
	"GET /tile-report":                                     {Class: SealedNoTile},
	"GET /users":                                           {Class: SealedNoTile},
	"GET /users-directory":                                 {Class: SealedNoTile},
	"GET /users/{id}/devices":                              {Class: SealedNoTile},
	"GET /vault-status":                                    {Class: SealedNoTile},
	"GET /vm":                                              {Class: SealedNoTile},
	"GET /whoami":                                          {Class: SealedNoTile},
	"GET /workspace-policies":                              {Class: SealedNoTile},
	"GET /workspace-settings":                              {Class: SealedNoTile},
	"PATCH /auth-settings":                                 {Class: SealedNoTile},
	"PATCH /sandboxes/{name}":                              {Class: SealedNoTile},
	"PATCH /term/sessions/{id}":                            {Class: SealedNoTile},
	"POST /account/password":                               {Class: SealedNoTile},
	"POST /app-tunnel/relay-tokens":                        {Class: SealedNoTile},
	"POST /app-tunnel/test":                                {Class: SealedNoTile},
	"POST /auth-rotate-token":                              {Class: SealedNoTile},
	"POST /auth-settings/sso/test":                         {Class: SealedNoTile},
	"POST /backup-keys/export":                             {Class: SealedNoTile},
	"POST /backup-keys/import":                             {Class: SealedNoTile},
	"POST /devices/enroll":                                 {Class: SealedNoTile},
	"POST /devices/enroll-code":                            {Class: SealedNoTile},
	"POST /devices/pairing/{id}/confirm":                   {Class: SealedNoTile},
	"POST /devices/push":                                   {Class: SealedNoTile},
	"POST /devices/push/activities":                        {Class: SealedNoTile},
	"POST /devices/{id}/tunnel":                            {Class: SealedNoTile},
	"POST /go-build-versions/check":                        {Class: SealedNoTile},
	"POST /impersonate":                                    {Class: SealedNoTile},
	"POST /impersonate/stop":                               {Class: SealedNoTile},
	"POST /invite/check":                                   {Class: SealedNoTile},
	"POST /invite/redeem":                                  {Class: SealedNoTile},
	"POST /limits/dismiss":                                 {Class: SealedNoTile},
	"POST /login":                                          {Class: SealedNoTile},
	"POST /notify":                                         {Class: SealedNoTile},
	"POST /partitions/credential-confirm":                  {Class: SealedNoTile},
	"POST /partitions/mail":                                {Class: SealedNoTile},
	"POST /partitions/mail/ack":                            {Class: SealedNoTile},
	"POST /path-tickets":                                   {Class: SealedNoTile},
	"POST /push/test":                                      {Class: SealedNoTile},
	"POST /sandboxes/copy":                                 {Class: SealedNoTile},
	"POST /sandboxes/{name}/execs":                         {Class: SealedNoTile},
	"POST /sandboxes/{name}/execs/{id}/resize":             {Class: SealedNoTile},
	"POST /sandboxes/{name}/execs/{id}/signal":             {Class: SealedNoTile},
	"POST /sandboxes/{name}/execs/{id}/stdin":              {Class: SealedNoTile},
	"POST /sandboxes/{name}/files/mkdir":                   {Class: SealedNoTile},
	"POST /sandboxes/{name}/files/move":                    {Class: SealedNoTile},
	"POST /sandboxes/{name}/files/remove":                  {Class: SealedNoTile},
	"POST /sandboxes/{name}/rebase":                        {Class: SealedNoTile},
	"POST /sandboxes/{name}/reset":                         {Class: SealedNoTile},
	"POST /sandboxes/{name}/run":                           {Class: SealedNoTile},
	"POST /sandboxes/{name}/snapshots":                     {Class: SealedNoTile},
	"POST /sandboxes/{name}/snapshots/{sid}/restore":       {Class: SealedNoTile},
	"POST /sandboxes/{name}/start":                         {Class: SealedNoTile},
	"POST /term/sessions/{id}/cancel":                      {Class: SealedNoTile},
	"POST /term/sessions/{id}/elicitations/{eid}":          {Class: SealedNoTile},
	"POST /term/sessions/{id}/options":                     {Class: SealedNoTile},
	"POST /term/sessions/{id}/permissions/{pid}":           {Class: SealedNoTile},
	"POST /term/sessions/{id}/prompt":                      {Class: SealedNoTile},
	"POST /term/sessions/{id}/queue":                       {Class: SealedNoTile},
	"POST /term/sessions/{id}/restart":                     {Class: SealedNoTile},
	"POST /users/{id}/invite":                              {Class: SealedNoTile},
	"POST /vault-rekey":                                    {Class: SealedNoTile},
	"POST /vault-seal":                                     {Class: SealedNoTile},
	"POST /vault-unseal":                                   {Class: SealedNoTile},
	"POST /web-ticket":                                     {Class: SealedNoTile},
	"PUT /app-tunnel":                                      {Class: SealedNoTile},
	"PUT /branding":                                        {Class: SealedNoTile},
	"PUT /devices/limits":                                  {Class: SealedNoTile},
	"PUT /native-runtime":                                  {Class: SealedNoTile},
	"PUT /net-sets/{name}":                                 {Class: SealedNoTile},
	"PUT /orgs/{org}/limits":                               {Class: SealedNoTile},
	"PUT /orgs/{org}/members/{user}":                       {Class: SealedNoTile},
	"PUT /orgs/{org}/policy":                               {Class: SealedNoTile},
	"PUT /orgs/{org}/sso-groups":                           {Class: SealedNoTile},
	"PUT /policy":                                          {Class: SealedNoTile},
	"PUT /prefs/{key}":                                     {Class: SealedNoTile},
	"PUT /push/config":                                     {Class: SealedNoTile},
	"PUT /push/prefs":                                      {Class: SealedNoTile},
	"PUT /sandboxes/policy":                                {Class: SealedNoTile},
	"PUT /sandboxes/{name}/files/content":                  {Class: SealedNoTile},
	"PUT /sandboxes/{name}/tar":                            {Class: SealedNoTile},
	"PUT /vm/policy":                                       {Class: SealedNoTile},
	"PUT /workspace-policies":                              {Class: SealedNoTile},
	"PUT /workspace-settings":                              {Class: SealedNoTile},
	// ---- the LLM connector and agent policy (D226, D229) ----
	"GET /llm/usage":                  {Class: SealedHandled, What: "its model usage reads as a tile's (?component=)"},
	"GET /llm":                        {Class: SealedNoTile},
	"GET /llm/config":                 {Class: SealedNoTile},
	"PUT /llm/config":                 {Class: SealedNoTile},
	"GET /llm/preferred":              {Class: SealedNoTile},
	"GET /llm/quotas":                 {Class: SealedNoTile},
	"PUT /llm/quotas":                 {Class: SealedNoTile},
	"GET /llm/v1/models":              {Class: SealedNoTile},
	"POST /llm/v1/chat/completions":   {Class: SealedNoTile},
	"POST /llm/v1/responses":          {Class: SealedNoTile},
	"PUT /llm/upstreams/{name}":       {Class: SealedNoTile},
	"DELETE /llm/upstreams/{name}":    {Class: SealedNoTile},
	"POST /llm/upstreams/{name}/test": {Class: SealedNoTile},
	"GET /agents/connection":          {Class: SealedNoTile},
	"GET /agents/policy":              {Class: SealedNoTile},
	"PUT /agents/policy":              {Class: SealedNoTile},
	"PUT /orgs/{org}/agents":          {Class: SealedNoTile},
	"PUT /orgs/{org}/agents/pin":      {Class: SealedNoTile},
	"POST /llm/setup/dismiss":         {Class: SealedNoTile},
	// ---- people's workspace agents (delegation, D228) ----
	// A person's agent acts on tiles. A request, a change set and the audit
	// name calls by method and URL path, judged when the call runs (the
	// delegate gate asks broker.DelegateTile, which refuses every sealed
	// component); the two routes that name a tile outright refuse one.
	"PUT /agent-levels":                      {Class: SealedRefused, Body: []string{"tile"}, What: sealedNoAgentWhat},
	"POST /delegate/approvals":               {Class: SealedRefused, Body: []string{"tile"}, What: sealedNoAgentWhat},
	"GET /agent-levels":                      {Class: SealedHandled, What: "never listed: it has no agent level"},
	"GET /delegate/approvals":                {Class: SealedNoTile},
	"DELETE /delegate/approvals/{id}":        {Class: SealedNoTile},
	"GET /delegate/requests":                 {Class: SealedNoTile},
	"POST /delegate/requests":                {Class: SealedNoTile},
	"GET /delegate/requests/{id}":            {Class: SealedNoTile},
	"POST /delegate/requests/{id}/approve":   {Class: SealedNoTile},
	"POST /delegate/requests/{id}/deny":      {Class: SealedNoTile},
	"DELETE /delegate/requests/{id}":         {Class: SealedNoTile},
	"GET /delegate/changesets":               {Class: SealedNoTile},
	"POST /delegate/changesets":              {Class: SealedNoTile},
	"GET /delegate/changesets/{id}":          {Class: SealedNoTile},
	"DELETE /delegate/changesets/{id}":       {Class: SealedNoTile},
	"POST /delegate/changesets/{id}/approve": {Class: SealedNoTile},
	"POST /delegate/changesets/{id}/reject":  {Class: SealedNoTile},
	"POST /delegate/changesets/{id}/undo":    {Class: SealedNoTile},
	"GET /delegate/audit":                    {Class: SealedNoTile},
	"GET /delegate/state":                    {Class: SealedNoTile},
	"PUT /delegate/state":                    {Class: SealedNoTile},
	"GET /delegate/workspace":                {Class: SealedNoTile},
	"PUT /delegate/workspace":                {Class: SealedNoTile},
	// agent sessions (handles), the map and the connection view: a handle's
	// scope narrows what its calls may reach, and every call is still judged
	// when it runs (a sealed component is in no agent's reach)
	"POST /delegate/sessions":                {Class: SealedNoTile},
	"GET /delegate/sessions/{id}":            {Class: SealedNoTile},
	"PATCH /delegate/sessions/{id}":          {Class: SealedNoTile},
	"DELETE /delegate/sessions/{id}":         {Class: SealedNoTile},
	"GET /delegate/sessions/{id}/connection": {Class: SealedNoTile},
	"GET /delegate/map":                      {Class: SealedNoTile},
	"POST /delegate/sessions/{id}/allows":    {Class: SealedNoTile}, // orgs
	"POST /delegate/sessions/{id}/taint":     {Class: SealedNoTile}, // orgs
	"POST /delegate/page/dismiss":            {Class: SealedNoTile}, // a person's own notice
	"POST /delegate/web/fetch":               {Class: SealedNoTile}, // a URL, never a tile
	"POST /delegate/mcp/call":                {Class: SealedRefused, Body: []string{"server"}, What: sealedNoAgentWhat},
	// what a tile agent started on ?tile= would open with: none starts on one
	// (auth.CanTerminalTileVia, and the delegate gate's DelegateTile)
	"GET /delegate/start-defaults": {Class: SealedRefused, Query: []string{"tile"}, What: "it has no terminal and no agent sessions"},
	// previews (D234): a render shows tiles, never a system component's page
	"POST /preview":             {Class: SealedRefused, Body: []string{"tile"}, What: sealedNoPreview},
	"GET /preview":              {Class: SealedNoTile},
	"GET /docs/search":          {Class: SealedNoTile},
	"GET /docs/page":            {Class: SealedNoTile},
	"POST /llm/keys":            {Class: SealedNoTile},
	"DELETE /llm/keys":          {Class: SealedNoTile},
	"DELETE /llm/keys/{id}":     {Class: SealedNoTile},
	"POST /llm/keys/{id}/renew": {Class: SealedNoTile},
	"GET /agent/prefs":          {Class: SealedNoTile},
	"PUT /agent/prefs":          {Class: SealedNoTile},
}
