package boot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/checkpoint"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/runner"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// The fixture's checkpoint trees (full ids) and their short ids.
var (
	dplTreeA = "3f2a1c9e" + strings.Repeat("0", 32)
	dplTreeB = "7b19e02d" + strings.Repeat("1", 32)
)

// dplRecord is a record file for tile: seq, live reload, the primary, and
// each deployment's checkpoint ("" = it follows the work tree).
func dplRecord(tile string, seq int, live, last string, protected bool, deps map[string]string) string {
	type dep struct {
		Checkpoint *string `json:"checkpoint"`
		Created    string  `json:"created,omitempty"`
		By         string  `json:"by,omitempty"`
	}
	ds := map[string]dep{}
	for name, cp := range deps {
		d := dep{Created: "2026-09-27T10:00:00Z", By: "user:ana"}
		if name != util.MainDeployment {
			d.Created = "2026-09-27T11:00:00Z"
		}
		if cp != "" {
			d.Checkpoint = &cp
		}
		ds[name] = d
	}
	b, _ := json.Marshal(map[string]any{"schema": 1, "tile": tile, "owner": "", "created": "2026-09-27T10:00:00Z",
		"seq": seq, "liveReload": live, "lastLiveReload": last, "primary": "main", "protectedPrimary": protected,
		"liveReloadSince": map[string]string{"at": "2026-09-27T11:48:00Z", "by": "user:ana"},
		"nextDeploy":      3, "deployments": ds})
	return string(b)
}

// dplFix is the handlers over a real plane and registry on a temp
// workspace:
//   - apps/zs (static) and apps/node (a node backend), zero state;
//   - apps/pin (static): live reload paused, main pinned to A;
//   - apps/crm (node): main pinned to A, live reload on dev (M2's shape,
//     which the M1 record already admits);
//   - apps/prot: main protected and pinned;
//   - apps/held: a record from a newer xbind;
//   - apps/v.git/pkg: a tile path with a .git-suffixed segment, with a record;
//   - apps/other, notes+ideas: zero state; apps/crm+ghost: a plain directory.
type dplFix struct {
	ws  string
	dp  *deployments.Plane
	api *deploymentsAPI
	mux *http.ServeMux
}

type dplMux struct{ *http.ServeMux }

func (m dplMux) RegisterAPI(p string, h http.HandlerFunc) { m.HandleFunc(p, h) }

func newDplFix(t *testing.T) *dplFix {
	t.Helper()
	ws := filepath.Join(t.TempDir(), "ws")
	files := map[string]string{"xbin.json": `{"schema":1}`, "apps/crm+ghost/notes.txt": "not a tile\n"}
	for tile, manifest := range map[string]string{"apps/zs": `{}`, "apps/node": `{"runtime":"node"}`,
		"apps/pin": `{}`, "apps/crm": `{"runtime":"node"}`, "apps/prot": `{}`, "apps/held": `{}`,
		"apps/v.git/pkg": `{}`, "apps/other": `{}`, "notes+ideas": `{}`} {
		files[tile+"/xbin.json"] = manifest
		files[tile+"/index.html"] = "<!doctype html><p>" + tile + "</p>\n"
	}
	for tile, rec := range map[string]string{
		"apps/pin":       dplRecord("apps/pin", 3, "", "main", false, map[string]string{"main": dplTreeA}),
		"apps/crm":       dplRecord("apps/crm", 7, "dev", "dev", false, map[string]string{"main": dplTreeA, "dev": ""}),
		"apps/prot":      dplRecord("apps/prot", 2, "", "main", true, map[string]string{"main": dplTreeB}),
		"apps/v.git/pkg": dplRecord("apps/v.git/pkg", 1, "", "main", false, map[string]string{"main": dplTreeA}),
		"apps/held":      strings.Replace(dplRecord("apps/held", 1, "", "main", false, map[string]string{"main": dplTreeA}), `"schema":1`, `"schema":99`, 1),
	} {
		files["data/deployments/"+util.TileKey(tile)+".json"] = rec
	}
	for rel, body := range files {
		p := filepath.Join(ws, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f := &dplFix{ws: ws}
	f.boot(t)
	return f
}

// boot (re)builds the registry, the plane and the handlers from disk.
func (f *dplFix) boot(t *testing.T) {
	t.Helper()
	reg, err := registry.Open(f.ws)
	if err != nil {
		t.Fatal(err)
	}
	f.dp = &deployments.Plane{Root: f.ws, Reg: reg, MayManage: func(p auth.Principal, tile string) bool {
		return p.Component == "" && (p.IsAdmin() || p.UserID == "mia")
	}}
	if err := f.dp.Boot(); err != nil {
		t.Fatal(err)
	}
	f.api = &deploymentsAPI{dp: f.dp, owner: func(string) string { return "org:devs" },
		ops: opRegistry{
			registered: func(o op) bool { return o == deployments.OpPause || o == deployments.OpDeploy },
			newRequest: func(op) (any, bool) { return &dplReq{}, true },
			do: func(context.Context, auth.Principal, op, any) (any, error) {
				return nil, errors.New("no operation in this fixture")
			},
		},
		reads: deployReads{
			status: func(c *registry.Component, dep string) depStatus {
				if !c.HasBackend() {
					return depStatus{State: "static"}
				}
				return depStatus{State: "healthy", Gen: 4, Error: strings.Repeat("é", 300)}
			},
			checkpoint: func(_ context.Context, _, tree string) (checkpoint.Checkpoint, error) {
				return checkpoint.Checkpoint{ID: "c:" + tree[:7], Hash: tree, Feed: "work-tree",
					At: time.Date(2026, 9, 27, 11, 48, 0, 0, time.UTC), By: "user:ana"}, nil
			},
			queue: func(tile, dep string) (any, []any) {
				if tile == "apps/pin" {
					return map[string]any{"id": 9, "how": "reload-now", "result": "running", "phase": "build"},
						[]any{map[string]any{"id": 10, "how": "deploy"}}
				}
				return nil, nil
			},
			lastDeploy: func(_ context.Context, tile, dep string) (any, error) {
				return map[string]any{"id": 2, "how": "pause", "from": "x", "at": "2026-09-27T11:48:00Z", "by": "user:ana", "result": "ok"}, nil
			},
			workTree: func(tile string) (int, bool) { return 4, true },
		}}
	f.mux = http.NewServeMux()
	mountDeploymentsAPI(dplMux{f.mux}, f.api)
}

type dplReq struct {
	Tile string `json:"tile"`
	Seq  int64  `json:"seq,omitempty"`
}

func (f *dplFix) do(t *testing.T, pr auth.Principal, method, target, body string) (int, http.Header, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req = req.WithContext(auth.WithPrincipal(req.Context(), pr))
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec.Code, rec.Header(), rec.Body.Bytes()
}

// get answers a GET as JSON, failing unless it has status want.
func (f *dplFix) get(t *testing.T, pr auth.Principal, target string, want int) map[string]any {
	t.Helper()
	code, _, body := f.do(t, pr, "GET", target, "")
	if code != want {
		t.Fatalf("GET %s = %d %s, want %d", target, code, body, want)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("GET %s: %v: %s", target, err, body)
	}
	return m
}

// wantError asserts the {"error","docs"} shape, the status and a prefix of
// the text.
func wantError(t *testing.T, what string, code int, body []byte, status int, prefix string) {
	t.Helper()
	var e map[string]string
	if err := json.Unmarshal(body, &e); err != nil || code != status || !strings.HasPrefix(e["error"], prefix) ||
		len(e) != 2 || e["docs"] == "" {
		t.Errorf("%s = %d %s, want %d {error: %q…, docs}", what, code, body, status, prefix)
		return
	}
	if docs := map[bool]string{true: "/docs/auth.md", false: "/docs/protocol.md"}[status == http.StatusForbidden]; e["docs"] != docs {
		t.Errorf("%s: docs %q, want %q", what, e["docs"], docs)
	}
}

// Principals built without a users store: the gates fall back to User.
func dplUser(id, level string) *users.User {
	u := &users.User{ID: id, Role: users.RoleUser, Tiles: map[string]string{}}
	for _, tile := range []string{"apps/zs", "apps/node", "apps/pin", "apps/crm", "apps/prot", "apps/held", "apps/v.git/pkg", "notes+ideas"} {
		u.Tiles[tile] = level
	}
	return u
}

var (
	dplOwner  = auth.Principal{Owner: true, Via: "bearer"}
	dplAdmin  = auth.Principal{UserID: "ada", User: &users.User{ID: "ada", Role: users.RoleAdmin}, Via: "session"}
	dplReader = auth.Principal{UserID: "rita", User: dplUser("rita", users.LevelRead), Via: "session"}
	dplWriter = auth.Principal{UserID: "wes", User: dplUser("wes", users.LevelWrite), Via: "session"}
	dplTerm   = auth.Principal{UserID: "tom", User: dplUser("tom", users.LevelTerminal), Via: "session"}
	dplNobody = auth.Principal{UserID: "nat", User: &users.User{ID: "nat", Role: users.RoleUser}, Via: "session"}
)

// tok is tile's own credential of kind via ("terminal", "frame",
// "instance"), driven by pr's user (none for an instance token).
func tok(tile, via string, pr auth.Principal) auth.Principal {
	p := auth.Principal{Component: tile, Via: via}
	if via != "instance" {
		p.UserID, p.User = pr.UserID, pr.User
	}
	return p
}

// covers D119c PO-7 Z1 Z10 D127l T7 — reading deployment state is pure, and a
// reader gets primary-scoped facts only. GET /deployments by every kind of
// caller, on zero-state tiles and on tiles with records, bare and
// qualified, leaves every file of the workspace byte-identical and creates
// no data/deployments entry, data/checkpoints or .xbin/deploy. The reader
// rows: a person with read and the primary's frame and instance tokens get
// the reader view — the primary's row alone, none of the writers-only keys,
// no other deployment's name, every Can refused with "needs write access" —
// and removing the non-primary deployment leaves the reader's answer
// byte-identical, so nothing counts it. Through the real daemon, reading a
// zero-state tile's state creates nothing either.
func TestDeploymentStateReadIsPure(t *testing.T) {
	f := newDplFix(t)
	before := dplSnapshot(t, f.ws)
	callers := []auth.Principal{dplOwner, dplAdmin, dplReader, dplWriter, dplTerm, dplNobody,
		tok("apps/crm", "terminal", dplTerm), tok("apps/crm", "frame", dplReader), tok("apps/crm", "instance", dplOwner),
		tok("apps/other", "terminal", dplTerm), func() auth.Principal { p := dplReader; p.Impersonator = "ada"; return p }()}
	for _, pr := range callers {
		for _, q := range []string{"apps/zs", "apps/node", "apps/pin", "apps/pin&deployment=main", "apps/crm", "apps/crm&deployment=dev",
			"apps/crm&deployment=nope", "apps/crm%2Bdev", "apps/prot", "apps/held", "apps/zs&deployment=main", "notes%2Bideas", "nope"} {
			f.do(t, pr, "GET", "/deployments?tile="+q, "")
		}
	}
	if after := dplSnapshot(t, f.ws); !slices.Equal(before, after) {
		t.Fatalf("reading deployment state changed the workspace:\nbefore %v\nafter  %v", before, after)
	}
	for _, rel := range []string{"data/checkpoints", ".xbin/deploy"} {
		if _, err := os.Lstat(filepath.Join(f.ws, rel)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s exists after reads (%v)", rel, err)
		}
	}

	// The reader rows.
	readerKeys := map[string]bool{"tile": true, "record": true, "schema": true, "features": true, "owner": true,
		"view": true, "primary": true, "liveReload": true, "protectedPrimary": true, "deployments": true, "caller": true, "selected": true}
	rowKeys := map[string]bool{"name": true, "primary": true, "liveReload": true, "checkpoint": true, "status": true,
		"url": true, "api": true, "origin": true, "lastDeploy": true}
	readers := map[string]auth.Principal{"a person with read": dplReader, "the primary's frame token": tok("apps/crm", "frame", dplWriter),
		"the tile's instance token": tok("apps/crm", "instance", dplOwner)}
	answers := map[string][]byte{}
	for who, pr := range readers {
		code, _, body := f.do(t, pr, "GET", "/deployments?tile=apps/crm", "")
		if code != http.StatusOK {
			t.Fatalf("%s: %d %s", who, code, body)
		}
		answers[who] = body
		var s map[string]any
		_ = json.Unmarshal(body, &s)
		if s["view"] != "reader" || s["liveReload"] != "" || s["record"] != true {
			t.Errorf("%s: view %v liveReload %q record %v", who, s["view"], s["liveReload"], s["record"])
		}
		for k := range s {
			if !readerKeys[k] {
				t.Errorf("%s: the reader view carries %q", who, k)
			}
		}
		if regexp.MustCompile(`\bdev\b`).Match(body) {
			t.Errorf("%s: the reader view names the non-primary deployment: %s", who, body)
		}
		rows := s["deployments"].([]any)
		if len(rows) != 1 {
			t.Fatalf("%s: %d rows, want the primary alone", who, len(rows))
		}
		row := rows[0].(map[string]any)
		for k := range row {
			if !rowKeys[k] {
				t.Errorf("%s: the primary's row carries %q", who, k)
			}
		}
		if st := row["status"].(map[string]any); st["error"] != nil || st["queued"] != nil {
			t.Errorf("%s: status %v carries writers-only facts", who, st)
		}
		if l := row["lastDeploy"].(map[string]any); l["id"] != nil || l["how"] != nil || l["from"] != nil {
			t.Errorf("%s: lastDeploy %v carries id, how or from", who, l)
		}
		for op, c := range s["caller"].(map[string]any)["can"].(map[string]any) {
			if c.(map[string]any)["ok"] != false || c.(map[string]any)["why"] != "deployments of apps/crm need write access" {
				t.Errorf("%s: can.%s = %v", who, op, c)
			}
		}
	}
	// Removing dev changes nothing a reader sees.
	rel := "data/deployments/" + util.TileKey("apps/crm") + ".json"
	if err := os.WriteFile(filepath.Join(f.ws, rel), []byte(dplRecord("apps/crm", 7, "", "main", false,
		map[string]string{"main": dplTreeA})), 0o600); err != nil {
		t.Fatal(err)
	}
	f.boot(t)
	for who, pr := range readers {
		if _, _, body := f.do(t, pr, "GET", "/deployments?tile=apps/crm", ""); !bytes.Equal(body, answers[who]) {
			t.Errorf("%s: removing a non-primary deployment changed the reader's answer:\n%s\n%s", who, answers[who], body)
		}
	}

	if testing.Short() {
		t.Skip("boots a workspace")
	}
	ws := zsWorkspace(t)
	d := zsBoot(t, ws)
	code, body := d.do(t, "GET", "/api/xbin/deployments?tile=apps/zs", "Bearer "+d.owner)
	var s map[string]any
	if err := json.Unmarshal(body, &s); code != http.StatusOK || err != nil || s["record"] != false || s["tile"] != "apps/zs" {
		t.Fatalf("the daemon's GET /deployments = %d %s", code, body)
	}
	keys := map[string]bool{}
	for _, c := range d.st.Reg.Components() {
		keys[util.CompKey(c.Path)] = true
	}
	if bad := zsDeploymentState(t, ws, keys); len(bad) > 0 {
		t.Fatalf("reading the state created deployment state:\n  %s", strings.Join(bad, "\n  "))
	}
}

// dplSnapshot lists every file under ws with a hash of its content and its
// mode and mtime.
func dplSnapshot(t *testing.T, ws string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(ws, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		line := p + " " + fi.Mode().String() + " " + fi.ModTime().String()
		if !d.IsDir() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			line += " " + fmt.Sprintf("%x", sha256.Sum256(b))
		}
		out = append(out, line)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// covers D119g T15 — the checkpoint remote's read gate, judged on every
// request: the tile's own terminal and agent sessions while their user
// holds write, and people with write at their current level (admins and
// the owner included), fetch; a reader, a writer demoted to read, the
// primary's frame token, the tile's instance token, a terminal token whose
// user holds only read, another tile's terminal token, another tile's
// credential (as a code: grant would be), a zero-state tile and an unknown
// one are refused, and the view repository is never reached for them.
// <tile>.git splits at the last .git segment whose prefix is a tile, on the
// decoded path.
func TestFetchRemoteReadGate(t *testing.T) {
	f := newDplFix(t)
	var served []string
	f.api.reads.fetch = func(w http.ResponseWriter, r *http.Request, tile, gitPath string) {
		served = append(served, tile+" "+gitPath)
		_, _ = w.Write([]byte("ref: refs/heads/deploy/main\n"))
	}
	for _, c := range []struct {
		who  string
		pr   auth.Principal
		path string
		want int
		msg  string
	}{
		{"a writer", dplWriter, "apps/pin.git/HEAD", 200, ""},
		{"an admin", dplAdmin, "apps/pin.git/info/refs", 200, ""},
		{"the owner", dplOwner, "apps/pin.git/objects/info/packs", 200, ""},
		{"a terminal-level person", dplTerm, "apps/pin.git/HEAD", 200, ""},
		{"the tile's terminal token", tok("apps/pin", "terminal", dplTerm), "apps/pin.git/HEAD", 200, ""},
		{"the tile's terminal token, driven by a writer", tok("apps/pin", "terminal", dplWriter), "apps/pin.git/HEAD", 200, ""},
		{"a .git-suffixed tile segment", dplWriter, "apps/v.git/pkg.git/info/refs", 200, ""},
		{"an escaped segment", dplWriter, "apps/v%2Egit/pkg.git/HEAD", 200, ""},
		{"a reader", dplReader, "apps/pin.git/HEAD", 403, "deployments of apps/pin need write access"},
		{"a writer demoted to read", auth.Principal{UserID: "wes", User: dplUser("wes", users.LevelRead), Via: "session"},
			"apps/pin.git/HEAD", 403, "deployments of apps/pin need write access"},
		{"the primary's frame token", tok("apps/pin", "frame", dplWriter), "apps/pin.git/HEAD", 403, "deployments of apps/pin need write access"},
		{"the tile's instance token", tok("apps/pin", "instance", dplOwner), "apps/pin.git/HEAD", 403, "deployments of apps/pin need write access"},
		{"the tile's terminal token, driven by a reader", tok("apps/pin", "terminal", dplReader), "apps/pin.git/HEAD", 403, "deployments of apps/pin need write access"},
		{"another tile's terminal token", tok("apps/other", "terminal", dplTerm), "apps/pin.git/HEAD", 403, "deployments of apps/pin need"},
		{"another tile's credential", auth.Principal{Component: "apps/other", Via: "frame"}, "apps/pin.git/HEAD", 403, "deployments of apps/pin need write access"},
		{"a stranger", dplNobody, "apps/pin.git/HEAD", 403, "deployments of apps/pin need"},
		{"a zero-state tile", dplWriter, "apps/zs.git/HEAD", 404, "apps/zs has no deployments yet"},
		{"a held tile", dplWriter, "apps/held.git/HEAD", 409, "apps/held's deployment record was written by a newer xbind (schema 99)"},
		{"an unknown tile", dplWriter, "apps/nope.git/HEAD", 404, "no such tile: apps/nope"},
		{"no .git segment", dplWriter, "apps/pin/HEAD", 404, "no such tile"},
	} {
		served = nil
		code, _, body := f.do(t, c.pr, "GET", "/checkpoints/"+c.path, "")
		if c.want == 200 {
			if code != 200 || len(served) != 1 {
				t.Errorf("%s: GET %s = %d %s (served %v), want 200", c.who, c.path, code, body, served)
			}
			continue
		}
		wantError(t, c.who+": GET "+c.path, code, body, c.want, c.msg)
		if len(served) != 0 {
			t.Errorf("%s: the view repository was reached: %v", c.who, served)
		}
	}
	served = nil
	f.do(t, dplWriter, "GET", "/checkpoints/apps/v.git/pkg.git/objects/ab/cdef0123456789abcdef0123456789abcdef01", "")
	if want := []string{"apps/v.git/pkg objects/ab/cdef0123456789abcdef0123456789abcdef01"}; !slices.Equal(served, want) {
		t.Errorf("split %v, want %v", served, want)
	}
	f.api.reads.fetch = nil
	code, _, body := f.do(t, dplWriter, "GET", "/checkpoints/apps/pin.git/HEAD", "")
	wantError(t, "without a view repository", code, body, 501, "reserved for tile deployments")
}

// covers T10 D119g D119c — a diff with a work-tree side captures a checkpoint, so
// it needs terminal level; between checkpoints the write audience may diff.
// A deployment that follows the work tree counts as the work tree. The
// primary's frame token and the tile's instance token never diff; a
// terminal token whose user holds only write diffs checkpoints but not the
// work tree. On a tile without a record the diff answers 409 and nothing is
// captured: the source is never reached.
func TestDiffCaptureNeedsTerminalLevel(t *testing.T) {
	f := newDplFix(t)
	var got []diffQuery
	f.api.reads.diff = func(w http.ResponseWriter, r *http.Request, q diffQuery) error {
		got = append(got, q)
		w.Header().Set("X-XBin-Checkpoint-From", "c:3f2a1c9")
		_, _ = w.Write([]byte("diff --git\n"))
		return nil
	}
	const cp = "&from=c:3f2a1c9&to=deployment:main"
	for _, c := range []struct {
		who   string
		pr    auth.Principal
		query string
		want  int
		msg   string
	}{
		{"a writer, between checkpoints", dplWriter, "apps/pin" + cp, 200, ""},
		{"a writer, the default (the primary to the work tree)", dplWriter, "apps/pin", 403, "a diff of the work tree needs terminal-level access on apps/pin"},
		{"a writer, to the work tree", dplWriter, "apps/pin&from=deployment:main&to=work-tree", 403, "a diff of the work tree needs terminal-level access"},
		{"a writer, to a deployment following the work tree", dplWriter, "apps/crm&from=deployment:main&to=deployment:dev", 403, "a diff of the work tree needs terminal-level access"},
		{"a terminal-level person, the default", dplTerm, "apps/pin", 200, ""},
		{"an admin, the default", dplAdmin, "apps/pin", 200, ""},
		{"the tile's terminal token", tok("apps/pin", "terminal", dplTerm), "apps/pin", 200, ""},
		{"a writer's terminal token, between checkpoints", tok("apps/pin", "terminal", dplWriter), "apps/pin" + cp, 200, ""},
		{"a writer's terminal token, the work tree", tok("apps/pin", "terminal", dplWriter), "apps/pin", 403, "a diff of the work tree needs terminal-level access"},
		{"the primary's frame token", tok("apps/pin", "frame", dplTerm), "apps/pin" + cp, 403, "deployments of apps/pin need write access"},
		{"the tile's instance token", tok("apps/pin", "instance", dplOwner), "apps/pin" + cp, 403, "deployments of apps/pin need write access"},
		{"a reader", dplReader, "apps/pin" + cp, 403, "deployments of apps/pin need write access"},
		{"a zero-state tile", dplTerm, "apps/zs", 409, "apps/zs has no deployments: its work tree is what runs, so there is nothing to diff"},
		{"a held tile", dplTerm, "apps/held", 409, "apps/held's deployment record was written by a newer xbind (schema 99)"},
		{"a bad spec", dplTerm, "apps/pin&from=HEAD", 400, `bad spec "HEAD"`},
		{"a bad deployment name", dplTerm, "apps/pin&to=deployment:Main", 400, "bad spec"},
		{"a path outside the tile", dplTerm, "apps/pin&path=../x", 400, "path is one clean tile-relative file"},
		{"an unknown stat", dplTerm, "apps/pin&stat=yes", 400, "path is one clean tile-relative file; stat"},
		{"an unknown deployment", dplTerm, "apps/pin&to=deployment:dev", 404, `apps/pin has no deployment "dev"`},
	} {
		got = nil
		code, _, body := f.do(t, c.pr, "GET", "/deployments/diff?tile="+c.query, "")
		if c.want == 200 {
			if code != 200 || len(got) != 1 {
				t.Errorf("%s: %d %s (diffs %d), want 200", c.who, code, body, len(got))
			}
			continue
		}
		wantError(t, c.who, code, body, c.want, c.msg)
		if len(got) != 0 {
			t.Errorf("%s: the diff ran (%+v)", c.who, got)
		}
	}

	got = nil
	f.do(t, dplTerm, "GET", "/deployments/diff?tile=apps/pin&path=src/a.js&stat=1", "")
	if len(got) != 1 {
		t.Fatal("no diff ran")
	}
	q := got[0]
	if q.Tile != "apps/pin" || q.From != (diffSide{Spec: "deployment:main", Deployment: "main", Tree: dplTreeA}) ||
		q.To != (diffSide{Spec: "work-tree", WorkTree: true}) || q.Path != "src/a.js" || !q.Stat || q.By.UserID != "tom" {
		t.Errorf("the diff was asked %+v", q)
	}
	got = nil
	f.do(t, dplTerm, "GET", "/deployments/diff?tile=apps/crm&from=c:7b19e02&to=deployment:dev", "")
	if len(got) != 1 || got[0].From != (diffSide{Spec: "c:7b19e02", ID: "c:7b19e02"}) || !got[0].To.WorkTree || got[0].To.Deployment != "dev" {
		t.Errorf("the diff was asked %+v", got)
	}

	// The source's conditions keep their statuses.
	for _, c := range []struct {
		err    error
		status int
	}{
		{&checkpoint.RateLimited{Tile: "apps/pin", RetryAfter: 2500 * time.Millisecond}, 429},
		{checkpoint.ErrAmbiguousID, 409}, {checkpoint.ErrUnknownCheckpoint, 404}, {context.DeadlineExceeded, 504},
		{&deployments.Error{Status: 429, Msg: "apps/pin already has a diff running and one waiting; retry shortly"}, 429},
		{errors.New("boom"), 500},
	} {
		f.api.reads.diff = func(http.ResponseWriter, *http.Request, diffQuery) error { return c.err }
		code, h, body := f.do(t, dplTerm, "GET", "/deployments/diff?tile=apps/pin", "")
		wantError(t, "diff failing with "+c.err.Error(), code, body, c.status, "")
		if _, ok := c.err.(*checkpoint.RateLimited); ok && h.Get("Retry-After") != "3" {
			t.Errorf("rate limited: Retry-After %q, want 3", h.Get("Retry-After"))
		}
	}
	f.api.reads.diff = nil
	code, _, body := f.do(t, dplTerm, "GET", "/deployments/diff?tile=apps/pin", "")
	wantError(t, "without a diff", code, body, 501, "reserved for tile deployments")
}

// covers D127l D119c D127m NP-14-4 NP-14-5 T7 — GET /deployments answers the State
// in the caller's view. The zero state is synthesized (record:false, main
// following the work tree) and the same for every view but the caller's
// own permissions; a tile with a record answers from it, with the store's
// checkpoint, the runner's status, the queue, the deploy log's last entry
// and the moved-files count; features and every Can say what this xbind
// builds; tile refs resolve as 11-contract §2.2 says (a component or a
// directory at the whole ref wins, '+' qualifies only a tile with a record,
// a non-primary deployment's name is refused to readers whether or not it
// exists); the deployment view, a held record, the ship-dark switch and
// view-as.
func TestDeploymentsStateHandler(t *testing.T) {
	f := newDplFix(t)

	// Resolution and refusals. A query names a deployment with deployment=,
	// never as tile+name (D127j): an escaped '+' that names no tile, and an
	// unescaped one read as a space, are both a 400.
	const qualifiedInQuery = "a deployment is named with deployment=, not tile+name (a '+' in a query string reads as a space)"
	for _, c := range []struct {
		pr     auth.Principal
		query  string
		status int
		msg    string
	}{
		{dplOwner, "", 400, "need ?tile="},
		{dplOwner, "nope", 404, "no such tile: nope"},
		{dplOwner, "apps/../apps/zs", 404, "no such tile"},
		{dplOwner, "apps/zs%2Bmain", 400, qualifiedInQuery},
		{dplOwner, "apps/held%2Bmain", 400, qualifiedInQuery},
		{dplWriter, "apps/crm%2Bdev", 400, qualifiedInQuery},
		{dplWriter, "apps/crm+dev", 400, qualifiedInQuery},
		{dplOwner, "apps/crm%2Bghost", 400, qualifiedInQuery},
		{dplOwner, "nope%2Bdev", 400, qualifiedInQuery},
		{dplOwner, "apps/crm&deployment=Dev", 400, "deployment names are lowercase letters"},
		{dplWriter, "apps/crm&deployment=nope", 404, `apps/crm has no deployment "nope"`},
		{dplReader, "apps/crm&deployment=dev", 403, "deployment URLs need write access on apps/crm"},
		{dplReader, "apps/crm&deployment=nope", 403, "deployment URLs need write access on apps/crm"},
		{tok("apps/crm", "frame", dplWriter), "apps/crm&deployment=dev", 403, "deployment URLs need write access"},
		{dplNobody, "apps/pin", 403, "deployments of apps/pin need read access"},
		{tok("apps/other", "terminal", dplTerm), "apps/pin", 403, "deployments of apps/pin need read access"},
		{dplNobody, "apps/held", 403, "deployments of apps/held need read access"},
		{dplReader, "apps/held", 409, "apps/held's deployment record was written by a newer xbind (schema 99)"},
	} {
		code, _, body := f.do(t, c.pr, "GET", "/deployments?tile="+c.query, "")
		wantError(t, "GET "+c.query, code, body, c.status, c.msg)
	}
	if s := f.get(t, dplOwner, "/deployments?tile=notes%2Bideas", 200); s["tile"] != "notes+ideas" || s["selected"] != nil {
		t.Errorf("a tile whose path holds + (an exact match): %v", s)
	}
	if s := f.get(t, dplOwner, "/deployments?tile=apps/zs&deployment=main", 200); s["tile"] != "apps/zs" || s["selected"] != "main" {
		t.Errorf("a zero-state tile's main, named: %v", s)
	}

	// The zero state.
	zs := f.get(t, dplOwner, "/deployments?tile=apps/zs", 200)
	want := map[string]any{"tile": "apps/zs", "record": false, "schema": 0.0, "seq": 0.0, "features": []any{"live-reload/1"},
		"owner": "org:devs", "view": "full", "primary": "main", "liveReload": "main", "lastLiveReload": "main", "protectedPrimary": false}
	for k, v := range want {
		if got, _ := json.Marshal(zs[k]); string(got) != dplJSON(v) {
			t.Errorf("zero state %s = %s, want %s", k, got, dplJSON(v))
		}
	}
	for _, k := range []string{"workTree", "liveReloadSince", "selected", "caps", "edges"} {
		if _, ok := zs[k]; ok {
			t.Errorf("the zero state carries %s", k)
		}
	}
	row := zs["deployments"].([]any)[0].(map[string]any)
	if dplJSON(pick(row, "name", "primary", "liveReload", "checkpoint", "status", "url", "api")) !=
		`{"checkpoint":null,"liveReload":true,"name":"main","primary":true,"status":{"gen":0,"serving":"work-tree","state":"static"},"url":"/c/apps/zs/"}` {
		t.Errorf("the zero state's main: %s", dplJSON(row))
	}
	dplCans(t, "zero state, the owner", zs, map[string]string{"caller.pause": "ok", "caller.resume": "state",
		"caller.add": "policy", "caller.edges": "state", "caller.protect": "policy", "allowed.pause": "ok",
		"allowed.deployments": "policy", "main.deploy": "state", "main.open": "ok"})
	if c := zs["caller"].(map[string]any); c["level"] != "admin" || c["manager"] != true || c["humanSession"] != true || c["bound"] != nil {
		t.Errorf("the owner's caller: %v", c)
	}
	// The same for every view, but for the caller's own permissions.
	for _, pr := range []auth.Principal{dplReader, dplWriter, tok("apps/zs", "frame", dplReader), tok("apps/zs", "terminal", dplTerm)} {
		s := f.get(t, pr, "/deployments?tile=apps/zs", 200)
		if dplJSON(dplStrip(s)) != dplJSON(dplStrip(zs)) {
			t.Errorf("the zero state differs for %s/%s:\n%s\n%s", pr.Via, pr.UserID, dplJSON(dplStrip(s)), dplJSON(dplStrip(zs)))
		}
	}
	lv := map[string]string{}
	for name, pr := range map[string]auth.Principal{"read": dplReader, "write": dplWriter, "terminal": dplTerm,
		"tile": tok("apps/zs", "frame", dplTerm), "terminal ": tok("apps/zs", "terminal", dplTerm)} {
		c := f.get(t, pr, "/deployments?tile=apps/zs", 200)["caller"].(map[string]any)
		lv[strings.TrimSpace(name)] = c["level"].(string)
		if pr.Component != "" && c["bound"] != map[string]string{"frame": "main", "terminal": ""}[pr.Via] {
			t.Errorf("%s token: bound %v", pr.Via, c["bound"])
		}
	}
	if lv["read"] != "read" || lv["write"] != "write" || lv["terminal"] != "terminal" || lv["tile"] != "tile" {
		t.Errorf("levels %v", lv)
	}
	if n := f.get(t, dplOwner, "/deployments?tile=apps/node", 200)["deployments"].([]any)[0].(map[string]any); n["api"] != "/api/apps/node/" ||
		n["status"].(map[string]any)["state"] != "healthy" || len(n["status"].(map[string]any)["error"].(string)) > 500 {
		t.Errorf("a backend tile's main: %v", n)
	}

	// A tile with a record, paused.
	pin := f.get(t, dplTerm, "/deployments?tile=apps/pin", 200)
	for k, v := range map[string]any{"record": true, "schema": 1.0, "seq": 3.0, "view": "full", "liveReload": "",
		"lastLiveReload": "main", "workTree": map[string]any{"changed": 4.0, "since": "c:3f2a1c9"},
		"liveReloadSince": map[string]any{"at": "2026-09-27T11:48:00Z", "by": "user:ana"}} {
		if dplJSON(pin[k]) != dplJSON(v) {
			t.Errorf("apps/pin %s = %s, want %s", k, dplJSON(pin[k]), dplJSON(v))
		}
	}
	main := pin["deployments"].([]any)[0].(map[string]any)
	if dplJSON(main["checkpoint"]) != `{"at":"2026-09-27T11:48:00Z","by":"user:ana","feed":"work-tree","hash":"`+dplTreeA+`","id":"c:3f2a1c9"}` ||
		dplJSON(main["status"]) != `{"deploying":{"how":"reload-now","id":9,"phase":"build","result":"running"},"gen":0,"queued":[{"how":"deploy","id":10}],"serving":"c:3f2a1c9","state":"static"}` ||
		main["liveReload"] != false || main["created"] != "2026-09-27T10:00:00Z" || main["by"] != "user:ana" ||
		main["lastDeploy"].(map[string]any)["how"] != "pause" {
		t.Errorf("apps/pin's main: %s", dplJSON(main))
	}
	dplCans(t, "apps/pin, a terminal-level person", pin, map[string]string{"caller.pause": "ok", "caller.resume": "policy",
		"caller.reloadNow": "policy", "caller.protect": "authority", "main.deploy": "ok", "main.restart": "ok",
		"main.rollback": "policy", "main.remove": "policy"})
	if s := f.get(t, dplTerm, "/deployments?tile=apps/pin&deployment=main", 200); s["selected"] != "main" {
		t.Errorf("the primary alias: selected %v", s["selected"])
	}
	if s := f.get(t, dplReader, "/deployments?tile=apps/pin&deployment=main", 200); s["selected"] != "main" || s["view"] != "reader" {
		t.Errorf("the primary alias for a reader: %v", s)
	}
	f.api.reads = deployReads{} // no sources: the state leaves their facts out
	bare := f.get(t, dplTerm, "/deployments?tile=apps/pin", 200)
	if b := bare["deployments"].([]any)[0].(map[string]any); dplJSON(b["checkpoint"]) != `{"hash":"`+dplTreeA+`","id":"c:3f2a1c9"}` ||
		dplJSON(b["status"]) != `{"gen":0,"serving":"c:3f2a1c9","state":"idle"}` || b["lastDeploy"] != nil ||
		dplJSON(bare["workTree"]) != `{"since":"c:3f2a1c9"}` {
		t.Errorf("without sources: %s %s", dplJSON(b), dplJSON(bare["workTree"]))
	}
	f.boot(t)

	// The full view with a non-primary deployment, the deployment view.
	crm := f.get(t, dplWriter, "/deployments?tile=apps/crm&deployment=dev", 200)
	var names []string
	for _, r := range crm["deployments"].([]any) {
		names = append(names, r.(map[string]any)["name"].(string))
	}
	if !slices.Equal(names, []string{"main", "dev"}) || crm["selected"] != "dev" || crm["liveReload"] != "dev" {
		t.Errorf("apps/crm+dev for a writer: %v selected %v", names, crm["selected"])
	}
	if dev := crm["deployments"].([]any)[1].(map[string]any); dev["url"] != "/c/apps/crm+dev/" || dev["api"] != "/api/apps/crm+dev/" ||
		dev["checkpoint"] != nil || dev["liveReload"] != true {
		t.Errorf("dev: %v", dev)
	}
	dplCans(t, "apps/crm, a writer", crm, map[string]string{"caller.pause": "authority", "dev.open": "ok", "main.deploy": "authority"})
	own := tok("apps/crm", "frame", dplWriter)
	own.Deployment = "dev"
	dv := f.get(t, own, "/deployments?tile=apps/crm", 200)
	if dv["view"] != "deployment" || dv["liveReload"] != "dev" || len(dv["deployments"].([]any)) != 2 ||
		dv["allowed"] != nil || dv["workTree"] != nil || dv["caller"].(map[string]any)["bound"] != "dev" {
		t.Errorf("the deployment view: %s", dplJSON(dv))
	}
	if f.get(t, own, "/deployments?tile=apps/crm&deployment=dev", 200)["selected"] != "dev" {
		t.Error("dev's own token can't name dev")
	}

	// Protection turns the protect act into unprotecting.
	prot := f.get(t, auth.Principal{UserID: "mia", User: dplUser("mia", users.LevelTerminal), Via: "session"}, "/deployments?tile=apps/prot", 200)
	dplCans(t, "apps/prot, a manager", prot, map[string]string{"caller.protect": "policy", "main.deploy": "ok"})
	dplCans(t, "apps/prot, a terminal-level person", f.get(t, dplTerm, "/deployments?tile=apps/prot", 200),
		map[string]string{"caller.protect": "authority", "main.deploy": "authority"})

	// The ship-dark switch.
	f.dp.OptInClosed = true
	closed := f.get(t, dplOwner, "/deployments?tile=apps/zs", 200)
	if dplJSON(closed["features"]) != `[]` {
		t.Errorf("switch off: features %s", dplJSON(closed["features"]))
	}
	dplCans(t, "switch off", closed, map[string]string{"allowed.pause": "policy", "caller.pause": "policy"})
	f.dp.OptInClosed = false

	// View-as reads as the viewed user, flagged read-only.
	va := dplWriter
	va.Impersonator = "ada"
	if c := f.get(t, va, "/deployments?tile=apps/pin", 200)["caller"].(map[string]any); c["readOnly"] != true || c["level"] != "write" {
		t.Errorf("view-as: %v", c)
	}
}

// dplCans asserts permissions by path ("caller.<act>", "allowed.<act>",
// "<deployment>.<act>"): "ok", or the kind of the refusal.
func dplCans(t *testing.T, what string, s map[string]any, want map[string]string) {
	t.Helper()
	for p, w := range want {
		scope, act, _ := strings.Cut(p, ".")
		var cans map[string]any
		switch scope {
		case "caller":
			cans = s["caller"].(map[string]any)["can"].(map[string]any)
		case "allowed":
			cans = s["allowed"].(map[string]any)
		default:
			for _, r := range s["deployments"].([]any) {
				if r.(map[string]any)["name"] == scope {
					cans = r.(map[string]any)["can"].(map[string]any)
				}
			}
		}
		c, _ := cans[act].(map[string]any)
		got := "missing"
		if c != nil && c["ok"] == true {
			got = "ok"
		} else if c != nil {
			got, _ = c["kind"].(string)
			if c["why"] == "" {
				got += " (no why)"
			}
		}
		if got != w {
			t.Errorf("%s: %s = %s (%v), want %s", what, p, got, c, w)
		}
	}
}

// dplStrip drops what depends on the caller: view, caller, each row's can.
func dplStrip(s map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range s {
		if k != "view" && k != "caller" {
			out[k] = v
		}
	}
	var rows []any
	for _, r := range s["deployments"].([]any) {
		m := map[string]any{}
		for k, v := range r.(map[string]any) {
			if k != "can" {
				m[k] = v
			}
		}
		rows = append(rows, m)
	}
	out["deployments"] = rows
	return out
}

func dplJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// covers T7 D127l — GET /deployments/log: the write audience reads every
// entry; a non-primary deployment's own principals read only theirs;
// readers, the primary's frame token and the tile's instance token are
// refused; a tile without a record answers 409; limit (default 50, at most
// 200), before, id and wait (at most 25 s) reach the log as asked, and bad
// values are 400s; without a deploy log the route is reserved.
func TestDeploymentsLogHandler(t *testing.T) {
	f := newDplFix(t)
	type ask struct {
		tile, dep  string
		limit      int
		before, id int64
		wait       time.Duration
	}
	var asked []ask
	f.api.reads.log = func(_ context.Context, tile, dep string, limit int, before int64) ([]any, bool, error) {
		asked = append(asked, ask{tile: tile, dep: dep, limit: limit, before: before})
		return []any{map[string]any{"id": 2, "deployment": "main", "how": "pause", "result": "ok"}}, true, nil
	}
	f.api.reads.entry = func(_ context.Context, tile string, id int64, wait time.Duration) (any, error) {
		asked = append(asked, ask{tile: tile, id: id, wait: wait})
		if id == 2 {
			return map[string]any{"id": 2, "deployment": "main", "how": "pause", "result": "ok"}, nil
		}
		return nil, nil
	}
	own := tok("apps/crm", "frame", dplWriter)
	own.Deployment = "dev"
	for _, c := range []struct {
		who, query string
		pr         auth.Principal
		status     int
		msg        string
		want       *ask
	}{
		{"a writer", "apps/pin", dplWriter, 200, "", &ask{tile: "apps/pin", limit: 50}},
		{"a writer, bounded", "apps/pin&limit=500&before=7&deployment=main", dplWriter, 200, "", &ask{tile: "apps/pin", dep: "main", limit: 200, before: 7}},
		{"a named deployment", "apps/crm&deployment=dev", dplWriter, 200, "", &ask{tile: "apps/crm", dep: "dev", limit: 50}},
		{"dev's own token", "apps/crm", own, 200, "", &ask{tile: "apps/crm", dep: "dev", limit: 50}},
		{"one attempt, waiting", "apps/pin&id=2&wait=90", dplTerm, 200, "", &ask{tile: "apps/pin", id: 2, wait: 25 * time.Second}},
		{"the tile's terminal token", "apps/pin", tok("apps/pin", "terminal", dplTerm), 200, "", &ask{tile: "apps/pin", limit: 50}},
		{"dev's own token, main's entries", "apps/crm&deployment=main", own, 403, "a tile's own credentials act only on their own deployment (dev)", nil},
		{"dev's own token, main's attempt", "apps/crm&id=2", own, 404, "apps/crm has no deploy 2", nil},
		{"an unknown attempt", "apps/pin&id=5", dplWriter, 404, "apps/pin has no deploy 5", nil},
		{"a qualified ref (D127j)", "apps/crm%2Bdev", dplWriter, 400, "a deployment is named with deployment=, not tile+name", nil},
		{"an unescaped qualified ref", "apps/crm+dev&deployment=main", dplWriter, 400, "a deployment is named with deployment=, not tile+name", nil},
		{"an unknown deployment", "apps/pin&deployment=dev", dplWriter, 404, `apps/pin has no deployment "dev"`, nil},
		{"a reader", "apps/pin", dplReader, 403, "deployments of apps/pin need write access", nil},
		{"the primary's frame token", "apps/pin", tok("apps/pin", "frame", dplWriter), 403, "deployments of apps/pin need write access", nil},
		{"the tile's instance token", "apps/pin", tok("apps/pin", "instance", dplOwner), 403, "deployments of apps/pin need write access", nil},
		{"a zero-state tile", "apps/zs", dplWriter, 409, "apps/zs has no deployments yet: pause live reload or add a deployment first", nil},
		{"limit 0", "apps/pin&limit=0", dplWriter, 400, "id and before are deploy ids", nil},
		{"a bad before", "apps/pin&before=x", dplWriter, 400, "id and before are deploy ids", nil},
		{"a negative wait", "apps/pin&id=2&wait=-1", dplWriter, 400, "id and before are deploy ids", nil},
	} {
		asked = nil
		code, _, body := f.do(t, c.pr, "GET", "/deployments/log?tile="+c.query, "")
		if c.status != 200 {
			wantError(t, c.who, code, body, c.status, c.msg)
			if c.want == nil && len(asked) > 0 && c.status != 404 {
				t.Errorf("%s: the log was read: %v", c.who, asked)
			}
			continue
		}
		if code != 200 || len(asked) != 1 || asked[0] != *c.want {
			t.Errorf("%s: %d %s, asked %v, want %v", c.who, code, body, asked, *c.want)
		}
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		if c.want.id == 0 && (m["tile"] != c.want.tile || m["more"] != true || len(m["entries"].([]any)) != 1) {
			t.Errorf("%s: %s", c.who, body)
		}
		if c.want.id != 0 && m["entry"].(map[string]any)["id"] != 2.0 {
			t.Errorf("%s: %s", c.who, body)
		}
	}
	f.api.reads.log, f.api.reads.entry = nil, nil
	code, _, body := f.do(t, dplWriter, "GET", "/deployments/log?tile=apps/pin", "")
	wantError(t, "without a deploy log", code, body, 501, "reserved for tile deployments")
}

// covers NP-14-3 NP-14-4 PO-14 — the generic operation handler: a route
// whose op this xbind doesn't register answers 501 (reserved) without
// reading its body; a registered op's body decodes strictly into its
// request type (an unknown field, an empty or oversized body is a 400 with
// the decoder's error); the registry gets the decoded request and the
// caller; its refusals and failures keep their statuses and docs; and the
// answer carries the caller's state as it stands after the operation,
// unless the operation answered one.
func TestDeploymentsOperationHandler(t *testing.T) {
	f := newDplFix(t)
	for _, o := range []op{deployments.OpResume, deployments.OpAdd, deployments.OpBackupSchedule} {
		code, _, body := f.do(t, dplOwner, "POST", "/deployments/"+string(o), `{"tile":"apps/pin"}`)
		wantError(t, "unregistered "+string(o), code, body, 501, "reserved for tile deployments, not built in this xbind yet — POST /deployments/"+string(o))
	}
	var gotReq *dplReq
	var gotPr auth.Principal
	var answer any
	var failure error
	f.api.ops.do = func(_ context.Context, pr auth.Principal, o op, req any) (any, error) {
		gotReq, gotPr = req.(*dplReq), pr
		return answer, failure
	}
	for _, c := range []struct{ what, body, msg string }{
		{"an unknown field", `{"tile":"apps/pin","sequence":3}`, `bad request body: json: unknown field "sequence"`},
		{"no body", ``, "bad request body: EOF"},
		{"an oversized body", `{"tile":"` + strings.Repeat("x", 70<<10) + `"}`, "bad request body: http: request body too large"},
	} {
		gotReq = nil
		code, _, body := f.do(t, dplTerm, "POST", "/deployments/live-reload/pause", c.body)
		wantError(t, c.what, code, body, 400, c.msg)
		if gotReq != nil {
			t.Errorf("%s: the operation ran", c.what)
		}
	}

	answer = map[string]any{"deploy": map[string]any{"id": 3, "how": "pause", "result": "running"}}
	code, _, body := f.do(t, dplTerm, "POST", "/deployments/live-reload/pause", `{"tile":"apps/pin","seq":3}`)
	var m map[string]any
	if err := json.Unmarshal(body, &m); code != 200 || err != nil || gotReq == nil || *gotReq != (dplReq{Tile: "apps/pin", Seq: 3}) || gotPr.UserID != "tom" {
		t.Fatalf("pause = %d %s (request %+v by %q)", code, body, gotReq, gotPr.UserID)
	}
	if st, _ := m["state"].(map[string]any); m["deploy"] == nil || st["tile"] != "apps/pin" || st["view"] != "full" || st["seq"] != 3.0 {
		t.Errorf("the answer: %s", body)
	}
	answer = map[string]any{"state": "the operation's own"}
	if _, _, body := f.do(t, dplTerm, "POST", "/deployments/live-reload/pause", `{"tile":"apps/pin"}`); string(body) != `{"state":"the operation's own"}`+"\n" {
		t.Errorf("an answer with its own state: %s", body)
	}
	answer = nil
	if _, _, body := f.do(t, dplTerm, "POST", "/deployments/deploy", `{"tile":"apps/zs"}`); !bytes.HasPrefix(body, []byte(`{"state":{`)) ||
		!bytes.Contains(body, []byte(`"record":false`)) {
		t.Errorf("a nil answer: %s", body)
	}
	answer = []int{1}
	if _, _, body := f.do(t, dplTerm, "POST", "/deployments/deploy", `{"tile":"apps/zs"}`); string(body) != "[1]\n" {
		t.Errorf("a non-object answer: %s", body)
	}
	answer = map[string]any{"unchanged": true}
	if _, _, body := f.do(t, dplTerm, "POST", "/deployments/deploy", `{"tile":"nope"}`); string(body) != `{"unchanged":true}`+"\n" {
		t.Errorf("an answer for a tile that doesn't resolve: %s", body)
	}

	for _, c := range []struct {
		err    error
		status int
		msg    string
	}{
		{&deployments.Error{Status: 403, Kind: deployments.KindAuthority, Msg: "pausing live reload needs terminal-level access on apps/pin"}, 403, "pausing live reload needs"},
		{&deployments.Error{Status: 409, Kind: deployments.KindState, Msg: "the deployments of apps/pin changed (seq 4); reload and retry"}, 409, "the deployments of apps/pin changed"},
		{&checkpoint.RateLimited{Tile: "apps/pin", RetryAfter: time.Second}, 429, "apps/pin was checkpointed too often"},
		{&deployments.StaleSeqError{Tile: "apps/pin", Seq: 4}, 409, "the deployments of apps/pin changed (seq 4)"},
		{errors.New("disk on fire"), 500, "disk on fire"},
	} {
		answer, failure = nil, c.err
		code, _, body := f.do(t, dplTerm, "POST", "/deployments/live-reload/pause", `{"tile":"apps/pin"}`)
		wantError(t, "failing with "+c.err.Error(), code, body, c.status, c.msg)
	}

	// The daemon's own registry: every route whose op isn't built answers 501.
	srv := &dplMux{http.NewServeMux()}
	registerDeploymentsAPIOn(srv, f.dp)
	for _, o := range dplActs(t, true) {
		if deployments.Registered(o) {
			continue // the operation's own tests cover it
		}
		req := httptest.NewRequest("POST", "/deployments/"+string(o), strings.NewReader(`{"tile":"apps/pin"}`))
		req = req.WithContext(auth.WithPrincipal(req.Context(), dplOwner))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		wantError(t, "the daemon's "+string(o), rec.Code, rec.Body.Bytes(), 501, "reserved for tile deployments")
	}
}

// registerDeploymentsAPIOn mounts the handlers as registerDeploymentsAPI
// does, on a test's mux.
func registerDeploymentsAPIOn(m apiMounter, dp *deployments.Plane) {
	mountDeploymentsAPI(m, &deploymentsAPI{dp: dp, owner: func(string) string { return "" },
		ops:   opRegistry{deployments.Registered, deployments.NewRequest, dp.Do},
		reads: planeReads(dp)})
}

// covers NP-14-3 PO-14 — each route reaches its own act. Every POST route
// dispatches exactly the op its path names, and every act with a route of
// its own in internal/deployments/authz.go (post: true) is mounted — so a
// literal wired to another op's handler, which the source comparison of
// TestOperationsNameTheRoutes (internal/deployments) can't see, fails here;
// the refinements (deploy/restart, protect/off, diff/work-tree) are never
// routes; each read act has its GET.
func TestDeploymentRoutesDispatchTheirOps(t *testing.T) {
	f := newDplFix(t)
	var asked []op
	f.api.ops = opRegistry{
		registered: func(op) bool { return true },
		newRequest: func(op) (any, bool) { return &dplReq{}, true },
		do: func(_ context.Context, _ auth.Principal, o op, _ any) (any, error) {
			asked = append(asked, o)
			return map[string]any{}, nil
		},
	}
	m := &dplRecorder{}
	mountDeploymentsAPI(m, f.api)
	mux := http.NewServeMux()
	mountDeploymentsAPI(dplMux{mux}, f.api)
	var posts []string
	for _, p := range m.patterns {
		rest, ok := strings.CutPrefix(p, "POST /deployments/")
		if !ok {
			if strings.HasPrefix(p, "POST ") {
				t.Errorf("a POST outside /deployments: %s", p)
			}
			continue
		}
		posts = append(posts, rest)
		asked = nil
		req := httptest.NewRequest("POST", "/deployments/"+rest, strings.NewReader(`{"tile":"apps/zs"}`))
		req = req.WithContext(auth.WithPrincipal(req.Context(), dplOwner))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != 200 || !slices.Equal(asked, []op{op(rest)}) {
			t.Errorf("POST /deployments/%s = %d, dispatched %v", rest, rec.Code, asked)
		}
	}
	var want []string
	for _, o := range dplActs(t, true) {
		want = append(want, string(o))
	}
	slices.Sort(posts)
	slices.Sort(want)
	if !slices.Equal(posts, want) {
		t.Fatalf("POST routes %v\nacts with a route %v", posts, want)
	}
	reads := map[op]string{deployments.OpState: "GET /deployments", deployments.OpLog: "GET /deployments/log",
		deployments.OpDiff: "GET /deployments/diff", deployments.OpFetch: "GET /checkpoints/{rest...}",
		deployments.OpBackups: "GET /deployments/backups"}
	for _, o := range dplActs(t, false) {
		switch r, isRead := reads[o]; {
		case o == deployments.OpRestart || o == deployments.OpUnprotect || o == deployments.OpDiffWorkTree || o == deployments.OpBranchClear:
			if slices.ContainsFunc(m.patterns, func(p string) bool { return strings.HasSuffix(p, "/"+string(o)) }) {
				t.Errorf("the refinement %s is a route", o)
			}
		case isRead && !slices.Contains(m.patterns, r):
			t.Errorf("the read %s has no route %s", o, r)
		case !isRead && !slices.Contains(want, string(o)):
			t.Errorf("the act %s has no route", o)
		}
	}
}

type dplRecorder struct{ patterns []string }

func (r *dplRecorder) RegisterAPI(p string, _ http.HandlerFunc) { r.patterns = append(r.patterns, p) }

// dplActs reads the authority table (internal/deployments/authz.go): the
// acts with a route of their own (post: true), or every act.
func dplActs(t *testing.T, post bool) []op {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join("..", "deployments", "authz.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	consts := map[string]string{}
	var table *ast.CompositeLit
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.ValueSpec:
			for i, name := range v.Names {
				if i < len(v.Values) {
					if lit, ok := v.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						consts[name.Name], _ = strconv.Unquote(lit.Value)
					}
					if name.Name == "acts" {
						table, _ = v.Values[i].(*ast.CompositeLit)
					}
				}
			}
		}
		return true
	})
	if table == nil || len(table.Elts) < 20 {
		t.Fatal("authz.go has no acts table")
	}
	var out []op
	for _, e := range table.Elts {
		kv := e.(*ast.KeyValueExpr)
		name, ok := consts[kv.Key.(*ast.Ident).Name]
		if !ok {
			t.Fatalf("the act %s isn't a string constant", kv.Key.(*ast.Ident).Name)
		}
		isPost := false
		for _, fe := range kv.Value.(*ast.CompositeLit).Elts {
			if fkv, ok := fe.(*ast.KeyValueExpr); ok && fkv.Key.(*ast.Ident).Name == "post" {
				isPost = fkv.Value.(*ast.Ident).Name == "true"
			}
		}
		if !post || isPost {
			out = append(out, op(name))
		}
	}
	return out
}

// dplRunner is the runner the plane is handed, with today's Status.
type dplRunner struct{ status map[string]any }

func (dplRunner) Deploy(context.Context, *registry.Component, string, runner.Code, func() error, runner.DeployProgress) error {
	return nil
}
func (dplRunner) ChangedDeployment(*registry.Component, string) {}
func (dplRunner) ChangedTile(*registry.Component)               {}
func (dplRunner) StopDeployment(string, string)                 {}
func (dplRunner) RootsInUse() []string                          { return nil }
func (r dplRunner) Status() map[string]any                      { return r.status }

// covers D119e — the state's status reads the runner xbind hands the plane:
// its generations are the primary's (keyed by tile path), so another
// deployment is idle; a tile without a backend is static; a runner that
// can't say is idle.
func TestDeploymentStatusFromRunner(t *testing.T) {
	f := newDplFix(t)
	f.dp.Run = dplRunner{status: map[string]any{
		"apps/crm":  map[string]any{"state": "failed", "gen": 3, "error": "exit 1"},
		"apps/node": map[string]any{"state": "healthy", "gen": 2},
	}}
	st := runnerStatus(f.dp)
	for _, c := range []struct {
		tile, dep string
		want      depStatus
	}{
		{"apps/crm", "main", depStatus{State: "failed", Gen: 3, Error: "exit 1"}},
		{"apps/crm", "dev", depStatus{State: "idle"}},
		{"apps/node", "main", depStatus{State: "healthy", Gen: 2}},
		{"apps/pin", "main", depStatus{State: "static"}},
	} {
		comp, _ := f.dp.Reg.Component(c.tile)
		if got := st(comp, c.dep); got != c.want {
			t.Errorf("%s %s: %+v, want %+v", c.tile, c.dep, got, c.want)
		}
	}
	f.dp.Run = nil
	comp, _ := f.dp.Reg.Component("apps/node")
	if got := runnerStatus(f.dp)(comp, "main"); got != (depStatus{State: "idle"}) {
		t.Errorf("without a runner: %+v", got)
	}
}
