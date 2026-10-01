package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A trimmed govulncheck -format json stream: config, two advisories' OSV
// records, a module-level finding, a call-level x/crypto one (twice, with
// two traces), a call-level standard-library one and one only imported.
const stream = `{"config":{"protocol_version":"v1.0.0","scanner_name":"govulncheck","scanner_version":"v1.8.0","db":"https://vuln.go.dev","db_last_modified":"2026-09-28T16:43:40Z","go_version":"go1.26.3","scan_level":"symbol","scan_mode":"source"}}
{"progress":{"message":"Scanning your code and 64 packages across 3 dependent modules for known vulnerabilities..."}}
{"osv":{"id":"GO-2026-6354","summary":"Prevent DoS on deadlocked undecided channel in golang.org/x/crypto/ssh"}}
{"osv":{"id":"GO-2026-5972","summary":"Enforce maximum recursion depth in encoding/asn1"}}
{"osv":{"id":"GO-2026-5932","summary":"openpgp is unmaintained"}}
{"finding":{"osv":"GO-2026-6354","fixed_version":"v0.56.0","trace":[{"module":"golang.org/x/crypto","version":"v0.48.0"}]}}
{"finding":{"osv":"GO-2026-5932","trace":[{"module":"golang.org/x/crypto","version":"v0.48.0","package":"golang.org/x/crypto/openpgp"}]}}
{"finding":{"osv":"GO-2026-6354","fixed_version":"v0.56.0","trace":[{"module":"golang.org/x/crypto","version":"v0.48.0","package":"golang.org/x/crypto/ssh","function":"NewServerConn","position":{"filename":"ssh/server.go","line":258,"column":6}},{"module":"sandbox-terminal","package":"sandbox-terminal/backend","function":"handleConn","receiver":"*Tile","position":{"filename":"backend/sshd.go","line":344,"column":43}},{"module":"sandbox-terminal","package":"sandbox-terminal/backend","function":"main"}]}}
{"finding":{"osv":"GO-2026-6354","fixed_version":"v0.56.0","trace":[{"module":"golang.org/x/crypto","version":"v0.48.0","package":"golang.org/x/crypto/ssh","function":"Write","receiver":"*extChannel"},{"module":"sandbox-terminal","package":"sandbox-terminal/backend","function":"main","position":{"filename":"backend/main.go","line":33,"column":12}}]}}
{"finding":{"osv":"GO-2026-5972","fixed_version":"v1.26.6","trace":[{"module":"stdlib","version":"v1.26.3","package":"encoding/asn1","function":"Unmarshal"},{"module":"sandbox-terminal","package":"sandbox-terminal/backend","function":"init","position":{"filename":"backend/main.go","line":44,"column":17}}]}}
`

func TestParse(t *testing.T) {
	r, err := parse(strings.NewReader(stream), false)
	if err != nil {
		t.Fatal(err)
	}
	if r.scanner != "v1.8.0" || r.goVersion != "go1.26.3" || r.dbDate() != "2026-09-28" {
		t.Errorf("config: %q %q %q", r.scanner, r.goVersion, r.dbDate())
	}
	want := []finding{{ID: "GO-2026-6354", Module: "golang.org/x/crypto", Version: "v0.48.0", Fixed: "v0.56.0",
		Summary: "Prevent DoS on deadlocked undecided channel in golang.org/x/crypto/ssh",
		Trace:   "backend/sshd.go:344:43: backend.Tile.handleConn calls ssh.NewServerConn"}}
	if !reflect.DeepEqual(r.reachable, want) {
		t.Errorf("reachable (a tile: the standard library's don't gate):\n got %+v\nwant %+v", r.reachable, want)
	}
	if !reflect.DeepEqual(r.notCalled, []string{"GO-2026-5932"}) || !reflect.DeepEqual(r.stdlibSkipped, []string{"GO-2026-5972"}) {
		t.Errorf("not called %v, standard library skipped %v", r.notCalled, r.stdlibSkipped)
	}

	r, err = parse(strings.NewReader(stream), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.reachable) != 2 || r.reachable[0].ID != "GO-2026-5972" || r.reachable[0].Module != "stdlib" || len(r.stdlibSkipped) != 0 {
		t.Errorf("xbind: the standard library's gate it: %+v (skipped %v)", r.reachable, r.stdlibSkipped)
	}

	if _, err := parse(strings.NewReader(`{"config":`), false); err == nil {
		t.Error("a torn stream must be an error")
	}
}

func TestAllow(t *testing.T) {
	src := `# comment

GO-2026-6354 builtin-tiles/sandbox-terminal # no fix for the tile's toolchain yet
GO-2026-5972 *   # the host's Go
`
	allow, err := parseAllow(strings.NewReader(src), "allow.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(allow) != 2 || allow[0].line != 3 || allow[0].reason != "no fix for the tile's toolchain yet" || allow[1].target != "*" {
		t.Fatalf("parsed %+v", allow)
	}
	if a := allowed(allow, "builtin-tiles/sandbox-terminal", "GO-2026-6354"); a != allow[0] {
		t.Error("exact target")
	}
	if allowed(allow, "xbind", "GO-2026-6354") != nil {
		t.Error("an entry allows only its own target")
	}
	if a := allowed(allow, "xbind", "GO-2026-5972"); a != allow[1] {
		t.Error("* allows every target")
	}

	for _, bad := range []string{
		"GO-2026-6354 builtin-tiles/sandbox-terminal\n",   // no why
		"GO-2026-6354 # why\n",                            // no target
		"CVE-2026-1234 xbind # why\n",                     // not a Go id
		"GO-2026-6354 xbind sdk # why\n",                  // two targets
		"GO-2026-6354 builtin-tiles/sandbox-terminal #\n", // empty why
	} {
		if _, err := parseAllow(strings.NewReader(bad), "allow.txt"); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

func TestReport(t *testing.T) {
	r, err := parse(strings.NewReader(stream), true)
	if err != nil {
		t.Fatal(err)
	}
	allow, _ := parseAllow(strings.NewReader("GO-2026-5972 xbind # the release builds with a newer go\n"), "allow.txt")
	var out bytes.Buffer
	if n := report(&out, target{name: "xbind"}, r, allow, "allow.txt"); n != 1 {
		t.Errorf("gating %d, want 1:\n%s", n, out.String())
	}
	if !allow[0].used {
		t.Error("the matching entry must be marked used")
	}
	for _, want := range []string{
		"✗ xbind: 1 reachable (1 more required or imported, never called)",
		"GO-2026-6354 golang.org/x/crypto@v0.48.0 (fixed in v0.56.0): Prevent DoS",
		"backend/sshd.go:344:43: backend.Tile.handleConn calls ssh.NewServerConn",
		"GO-2026-5972 allowed by allow.txt:1: the release builds with a newer go",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report lacks %q:\n%s", want, out.String())
		}
	}
}

// Every builtin with Go code is a target, under its tree-relative name, and
// a template's _backend is among its patterns.
func TestDiscover(t *testing.T) {
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	ts, err := discover(repo)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]target{}
	for _, tg := range ts {
		names[tg.name] = tg
	}
	for _, want := range []string{"xbind", "relay", "sdk", "builtin-tiles/sandbox-terminal", "builtin-templates/agent"} {
		if _, ok := names[want]; !ok {
			t.Errorf("no target %s in %v", want, names)
		}
	}
	if !names["xbind"].stdlib || names["sdk"].stdlib || names["builtin-templates/agent"].stdlib {
		t.Error("the standard library gates xbind only (and the relay)")
	}
	tiles, _ := filepath.Glob(filepath.Join(repo, "builtin-*", "*", "go.mod.tile"))
	if got := len(ts) - 3; got != len(tiles) {
		t.Errorf("%d builtin targets, %d go.mod.tile files", got, len(tiles))
	}

	dst := t.TempDir()
	src := filepath.Join(t.TempDir(), "tpl")
	for rel, body := range map[string]string{
		"go.mod.tile":                  "module tpl\n\ngo 1.24\n",
		"_backend/main.go":             "package main\n\nfunc main() {}\n",
		"_backend/store/store.go":      "package store\n",
		"_backend/store/store_test.go": "package store\n",
		"_backend/testdata/x.go":       "package x\n",
		"web/app.js":                   "export {};\n",
	} {
		p := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pats, err := packageDirs(src)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"./_backend", "./_backend/store"}; !reflect.DeepEqual(pats, want) {
		t.Errorf("patterns %v, want %v", pats, want)
	}
	if err := copyTile(target{src: src}, repo, dst); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dst, "go.mod"))
	if err != nil || !strings.Contains(string(b), "replace "+sdkModule+" => "+filepath.Join(repo, "sdk")) {
		t.Errorf("go.mod (%v):\n%s", err, b)
	}
}
