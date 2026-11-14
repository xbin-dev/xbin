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
//   - SealedSelf: only the sealed component's own credentials pass (403
//     otherwise): its resources, secrets and registrations are its own, not
//     an admin's to read or write;
//   - SealedHandled: the handler knows the rule (What says it);
//   - SealedNoTile: the route names no tile.
//
// The namespace is structural: any name under xbin/ (and res:xbin/…) gets
// the row's answer, whether or not a component is registered there. A guard
// over the route inventory keeps the table complete (internal/apicheck's
// TestSealedRouteClasses), and internal/boot's TestSealedRouteAnswers
// drives every Refused, Self and Handled row through a real daemon.

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

// SealedDocs is the page a sealed-component refusal points at.
const SealedDocs = "/docs/sealed.md"

// SealedRefusal is the text of a refusal about the sealed component (or
// name under xbin/) path.
func SealedRefusal(path, what string) string {
	return path + " is a system component, managed by xbin: " + what
}

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
	if rest, ok := strings.CutPrefix(v, "res:"); ok {
		return v, util.SealedPath(rest)
	}
	return v, util.SealedPath(v)
}

// sealedOwn reports whether name (a path, a path below, or a resource) is
// comp's own.
func sealedOwn(comp, name string) bool {
	name = strings.TrimPrefix(name, "res:")
	return comp != "" && (name == comp || strings.HasPrefix(name, comp+"/"))
}

// sealedGate answers a request that names a sealed component on a Refused
// or Self route; nil lets the handler run. r2 is the inner request (its
// path what follows /api/xbin). A body it had to read is put back.
func (s *Server) sealedGate(r2 *http.Request) http.HandlerFunc {
	if s.apiMux == nil {
		return nil
	}
	_, pattern := s.apiMux.Handler(r2)
	rule := sealedRules[pattern]
	if rule.Class != SealedRefused && rule.Class != SealedSelf {
		return nil
	}
	var names []string
	q := r2.URL.Query()
	for _, k := range rule.Query {
		names = append(names, q[k]...)
	}
	if rule.Rest {
		if _, rest, ok := strings.Cut(strings.TrimPrefix(r2.URL.Path, "/"), "/"); ok {
			names = append(names, rest)
		}
	}
	if len(rule.Body) > 0 && r2.Body != nil && r2.Body != http.NoBody {
		// The whole body, as the handler's own decoder would buffer it (these
		// are JSON control calls); put back for the handler.
		b, err := io.ReadAll(r2.Body)
		r2.Body.Close()
		if err != nil {
			return refusal(http.StatusBadRequest, "the request body couldn't be read")
		}
		r2.Body = io.NopCloser(bytes.NewReader(b))
		names = append(names, sealedBodyNames(b, rule.Body)...)
	}
	p := auth.PrincipalOf(r2)
	for _, v := range names {
		name, sealed := sealedName(v)
		switch {
		case !sealed:
		case rule.Class == SealedSelf && sealedOwn(p.Component, name):
		case rule.Class == SealedSelf:
			return sealedRefusal(http.StatusForbidden, name, rule.What)
		default:
			return sealedRefusal(http.StatusBadRequest, name, rule.What)
		}
	}
	return nil
}

// sealedBodyNames are the values of the fields keys in the JSON object body
// begins with, read the way the handlers' decoder (encoding/json into a
// struct) reads them, so the gate sees every value a handler could: the
// first JSON value of the body (what follows it is ignored there too), and
// keys matched without regard to case — "Target" fills a field tagged
// "target". Every match counts, though the decoder keeps the last. A value
// is a string or a list of strings; anything else names nothing.
func sealedBodyNames(body []byte, keys []string) []string {
	var fields map[string]json.RawMessage
	if json.NewDecoder(bytes.NewReader(body)).Decode(&fields) != nil {
		return nil // not an object: the handler refuses it
	}
	var names []string
	for name, raw := range fields {
		if !slices.ContainsFunc(keys, func(k string) bool { return strings.EqualFold(k, name) }) {
			continue
		}
		var one string
		var many []string
		switch {
		case json.Unmarshal(raw, &one) == nil:
			names = append(names, one)
		case json.Unmarshal(raw, &many) == nil:
			names = append(names, many...)
		}
	}
	return names
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
	"DELETE /partitions/consents":          {Class: SealedRefused, Query: []string{"to"}, Body: []string{"to"}, What: "no tile reaches people's partitions of it"},
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
	"POST /partitions/consents":            {Class: SealedRefused, Body: []string{"to"}, What: "no tile reaches people's partitions of it"},
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
	"POST /tile-report":                                    {Class: SealedNoTile},
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
	"GET /agent/prefs":                {Class: SealedNoTile},
	"PUT /agent/prefs":                {Class: SealedNoTile},
}
