// Package apicheck holds the route-inventory drift test: every route xbind
// mounts must be described in internal/server/openapi.go (the /api/xbin
// surface) and docs/protocol.md (everything), and everything those two
// describe must exist. It is its own package because the check needs the
// broker mounted on the server, and server cannot import broker.
//
// Three notations are reconciled onto one shape (method + path segments,
// wildcards as "{}"):
//
//	mux (RegisterAPI / Handler)   GET /kv/{rest...}     GET /components/{path...}   /c/
//	openapi.go                    GET /kv/{resource}/{key}
//	docs/protocol.md (in fences)  GET /kv/res:<scope>/<name>/<key>   GET /c/<component-path>/[file]
//
// A mux `{x...}` matches one or more documented segments, so one route may
// be documented as several rows (/vault/{component} and
// /vault/{component}/{key} both belong to GET /vault/{rest...}); a mux
// subtree pattern ("/c/") matches any deeper row.
package apicheck

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/broker"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
)

type route struct {
	method string   // "" = any method (mux patterns without one)
	segs   []string // "{}" = one wildcard segment
	rest   bool     // last segment is a {name...} wildcard: one or more segments
	prefix bool     // mux subtree pattern ("/c/"): any number of further segments
	src    string   // where it came from, for messages
}

func (r route) String() string {
	if r.method == "" {
		return "ANY /" + strings.Join(r.segs, "/")
	}
	return r.method + " /" + strings.Join(r.segs, "/")
}

func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// parseMux parses a net/http ServeMux pattern as RegisterAPI and Handler use them.
func parseMux(pat, src string) route {
	r := route{src: src}
	method, path, ok := strings.Cut(pat, " ")
	if !ok {
		path, method = pat, ""
	}
	r.method = method
	if path == "/{$}" {
		return r
	}
	r.prefix = strings.HasSuffix(path, "/")
	for _, seg := range splitPath(path) {
		if strings.HasPrefix(seg, "{") {
			r.rest = strings.HasSuffix(seg, "...}")
			seg = "{}"
		}
		r.segs = append(r.segs, seg)
	}
	return r
}

// parseDoc normalises a documented row: OpenAPI {name} segments, protocol.md
// <name> / [name] / res:<scope> segments, and ?query suffixes.
func parseDoc(method, path, src string) route {
	r := route{method: method, src: src}
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	for _, seg := range splitPath(path) {
		if strings.ContainsAny(seg, "<{[") || strings.HasPrefix(seg, "res:") {
			seg = "{}"
		}
		r.segs = append(r.segs, seg)
	}
	return r
}

// matches reports whether documented row d is covered by mounted route r.
func (r route) matches(d route) bool {
	if r.method != "" && r.method != d.method {
		return false
	}
	for i, seg := range r.segs {
		if r.rest && i == len(r.segs)-1 {
			return len(d.segs) > i
		}
		if i >= len(d.segs) {
			return false
		}
		if seg != "{}" && seg != d.segs[i] {
			return false
		}
	}
	if r.prefix {
		return len(d.segs) >= len(r.segs)
	}
	return len(d.segs) == len(r.segs)
}

// mount builds the broker on an empty workspace and mounts everything the
// daemon mounts, except the handlers internal/boot/api.go registers inline
// (those are read from its source by mainRoutes).
func mount(t *testing.T) *server.Server {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "xbin.json"), []byte(`{"schema":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	hub := events.NewHub()
	b, err := broker.New(reg, hub, false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b.Users = st
	srv := &server.Server{Reg: reg, Hub: hub}
	b.Register(srv)
	srv.Handler() // mounts the core routes and registerCoreAPI
	return srv
}

var registerLit = regexp.MustCompile(`RegisterAPI\("([^"]+)"`)

// mainRoutes returns the patterns internal/boot/api.go (the runtime API the
// boot mounts across runner, broker and ingress) registers inline.
func mainRoutes(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "internal", "boot", "api.go"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range registerLit.FindAllStringSubmatch(string(b), -1) {
		out = append(out, m[1])
	}
	return out
}

// sourceRoutes returns every RegisterAPI("…") literal under cmd/ and
// internal/, keyed by pattern → file. A literal the mounted set does not
// contain means a registration site the fixture never reaches — the test
// would silently stop covering it.
func sourceRoutes(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join("..", "..", dir), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			for _, m := range registerLit.FindAllStringSubmatch(string(b), -1) {
				out[m[1]] = p
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

var docRow = regexp.MustCompile(`^(GET|POST|PUT|PATCH|DELETE|ANY)\s+(\S+)`)

// protocolRows returns every route row inside a code fence of
// docs/protocol.md (continuation lines never start at column 0 with a method).
func protocolRows(t *testing.T) []route {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "protocol.md"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []route
	inFence := false
	for i, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if !inFence {
			continue
		}
		if m := docRow.FindStringSubmatch(line); m != nil {
			rows = append(rows, parseDoc(m[1], m[2], fmt.Sprintf("docs/protocol.md:%d", i+1)))
		}
	}
	return rows
}

func openapiRows(t *testing.T) []route {
	t.Helper()
	paths, _ := server.OpenAPI()["paths"].(map[string]any)
	var rows []route
	for p, item := range paths {
		ops, _ := item.(map[string]any)
		for method := range ops {
			rows = append(rows, parseDoc(strings.ToUpper(method), p, "internal/server/openapi.go"))
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].String() < rows[j].String() })
	return rows
}

func TestRouteInventory(t *testing.T) {
	srv := mount(t)
	var api []route
	for _, p := range srv.APIRoutes() {
		api = append(api, parseMux(p, "RegisterAPI"))
	}
	for _, p := range mainRoutes(t) {
		api = append(api, parseMux(p, "internal/boot/api.go"))
	}
	var core []route
	for _, p := range srv.CoreRoutes() {
		core = append(core, parseMux(p, "server.Handler"))
	}
	if len(api) < 100 || len(core) < 10 {
		t.Fatalf("mounted only %d API + %d core routes — is the fixture wiring intact?", len(api), len(core))
	}

	var problems []string
	add := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }

	// Every RegisterAPI literal in the tree must be one the fixture mounted.
	mounted := map[string]bool{}
	for _, r := range api {
		mounted[r.src+" "+r.String()] = true
	}
	seen := map[string]bool{}
	for _, p := range srv.APIRoutes() {
		seen[p] = true
	}
	for _, p := range mainRoutes(t) {
		seen[p] = true
	}
	for pat, file := range sourceRoutes(t) {
		if !seen[pat] {
			add("%s registers %q, which this test's fixture never mounts — wire it into mount() or mainRoutes()", filepath.ToSlash(file), pat)
		}
	}

	// Documented ⇒ mounted.
	apiDocs := make([]int, len(api))  // openapi rows per API route
	apiProto := make([]int, len(api)) // protocol rows per API route
	coreProto := make([]int, len(core))
	for _, d := range openapiRows(t) {
		hit := false
		for i, r := range api {
			if r.matches(d) {
				apiDocs[i]++
				hit = true
			}
		}
		if !hit {
			add("%s: %s is not a registered route — delete the row or register the route", d.src, d)
		}
	}
	for _, d := range protocolRows(t) {
		hit := false
		for i, r := range api {
			if r.matches(d) {
				apiProto[i]++
				hit = true
			}
		}
		if !hit {
			for i, r := range core {
				if r.matches(d) {
					coreProto[i]++
					hit = true
				}
			}
		}
		if !hit {
			add("%s: %s is not a registered route — fix the row or register the route", d.src, d)
		}
	}

	// Mounted ⇒ documented, twice over for the API surface.
	for i, r := range api {
		if apiDocs[i] == 0 {
			add("%s (%s) has no internal/server/openapi.go entry", r, r.src)
		}
		if apiProto[i] == 0 {
			add("%s (%s) has no docs/protocol.md row", r, r.src)
		}
	}
	for i, r := range core {
		if coreProto[i] == 0 {
			add("%s (%s) has no docs/protocol.md row", r, r.src)
		}
	}

	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
	if len(problems) > 0 {
		t.Logf("%d API + %d core routes mounted; %d drift problem(s). docs/maintenance.md → \"Route inventory\".", len(api), len(core), len(problems))
	}
}

func TestMatching(t *testing.T) {
	cases := []struct {
		mux, method, doc string
		want             bool
	}{
		{"GET /kv/{rest...}", "GET", "/kv/res:<scope>/<name>/<key>", true},
		{"GET /kv/{rest...}", "GET", "/kv/res:<scope>/<name>/?prefix=", true},
		{"GET /kv/{rest...}", "GET", "/kv", false},
		{"GET /kv/{rest...}", "PUT", "/kv/{resource}/{key}", false},
		{"GET /components/{path...}", "GET", "/components", false},
		{"GET /components", "GET", "/components", true},
		{"GET /components/{path...}", "GET", "/components/<path>", true},
		{"DELETE /cron/jobs/{name}", "DELETE", "/cron/jobs/<name>[?component=]", true},
		{"DELETE /cron/jobs/{name}", "DELETE", "/cron/jobs", false},
		{"GET /{$}", "GET", "/", true},
		{"GET /{$}", "GET", "/x", false},
		{"/c/", "GET", "/c/<component-path>/[file]", true},
		{"/api/", "ANY", "/api/xbin/<p>", true},
		{"GET /docs/", "GET", "/docs/<file>.md", true},
		{"GET /docs/", "POST", "/docs/<file>.md", false},
		{"GET /status", "GET", "/status", true},
		{"GET /status", "GET", "/status/x", false},
	}
	for _, c := range cases {
		r := parseMux(c.mux, "t")
		d := parseDoc(c.method, c.doc, "t")
		if got := r.matches(d); got != c.want {
			t.Errorf("%q matches %s %q = %v, want %v", c.mux, c.method, c.doc, got, c.want)
		}
	}
}
