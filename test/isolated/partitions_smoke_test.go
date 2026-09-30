//go:build linux && integration

package isolated

// partitions_smoke_test.go — work pack S1 of the partitioned-tiles plan
// (plans/partitions/95-work-packs.md; its records: plans/partitions/records/
// S1.md): an end-to-end smoke of partitioned tiles as built through wave 1,
// on a real `xbind --isolate` with owner auth on (people's partitions run
// only under isolation, PD-19).
//
// A fixture tile, apps/pt, declares "partition": ["user", "global"]. Its Go
// backend (psSource, over the SDK) echoes what xbind told it — XBIN_PARTITION,
// xbin.Partition(), xbin.Caller(r), its instance token — and reads and writes
// a per-partition kv resource ("kv"), a shared kv resource ("board",
// "shared": true) and a per-partition filesystem resource ("files"). People
// alice, bob and dave (users) and carol (a workspace admin) use it beside the
// owner token. Each subtest names the guarantee it smokes (00 §3, 02/03's
// tests, 01 §2.4-§2.6); features later packs build (F5 bus subscriptions,
// cron, vault and notify; F7a terminals; F9 ?xbin-partition=global; F10
// consent; F15 personal binds) are out of scope.
//
//	set -a; eval "$(sed -n 's/^export \([A-Z_]*\) := \(.*\)$/\1=\2/p' .dev.mk | grep -v ^PATH)"; set +a
//	go test -tags=integration -count=1 -v -run '^TestPartitionsSmoke$' ./test/isolated/
//
// TestPartitionsSmokeReap (the idle stop: over ten minutes, no knob shortens
// it) runs only with XBIN_SMOKE_REAP=1; it runs in parallel with the main
// test. Run both with the Bash sandbox disabled on a dev box.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/util"
	"github.com/xbin-dev/xbin/test/xbindtest"
)

const (
	psTile   = "apps/pt"    // the partitioned fixture: ["user", "global"]
	psCaller = "apps/pcall" // an unpartitioned tile granted on it
	psPlain  = "apps/plain" // an unpartitioned tile that holds data, later asked to partition
	psHost   = "pt.test"    // the fixture's published host: ingress reaches global
	psNote   = "S1 smoke fixture: <b>notes</b> live in each person's partition"
)

// psSource is the probe backend every fixture tile runs. boot names the
// process (a sandbox's pid is 1 in every instance).
const psSource = `package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"

	xbin "github.com/xbin-dev/xbin/sdk"
)

var boot = func() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}()

func who(r *http.Request) map[string]any {
	return map[string]any{
		"env": os.Getenv("XBIN_PARTITION"), "partition": xbin.Partition(), "user": xbin.PartitionUser(),
		"caller": xbin.Caller(r), "boot": boot, "token": os.Getenv("XBIN_TOKEN"),
		"files": xbin.Resource("files"), "query": r.URL.RawQuery,
	}
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, err error) {
	if errors.Is(err, xbin.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		reply(w, 404, map[string]string{"error": "not found"})
		return
	}
	reply(w, 502, map[string]string{"error": err.Error()})
}

// file is {name} in the filesystem resource {res}.
func file(r *http.Request) (string, error) {
	dir := xbin.Resource(r.PathValue("res"))
	if dir == "" {
		return "", errors.New("no such filesystem resource")
	}
	return filepath.Join(dir, filepath.Base(r.PathValue("name"))), nil
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /who", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, who(r)) })
	mux.HandleFunc("GET /pub/who", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, who(r)) })
	mux.HandleFunc("GET /kv/{res}/{key}", func(w http.ResponseWriter, r *http.Request) {
		b, err := xbin.KV(xbin.Resource(r.PathValue("res"))).Get(r.PathValue("key"))
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, map[string]string{"value": string(b)})
	})
	mux.HandleFunc("PUT /kv/{res}/{key}", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if err := xbin.KV(xbin.Resource(r.PathValue("res"))).Put(r.PathValue("key"), b); err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, map[string]string{"ok": "stored"})
	})
	mux.HandleFunc("GET /keys/{res}", func(w http.ResponseWriter, r *http.Request) {
		keys, err := xbin.KV(xbin.Resource(r.PathValue("res"))).List("")
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, map[string]any{"keys": keys})
	})
	mux.HandleFunc("GET /fs/{res}/{name}", func(w http.ResponseWriter, r *http.Request) {
		p, err := file(r)
		var b []byte
		if err == nil {
			b, err = os.ReadFile(p)
		}
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, map[string]string{"value": string(b)})
	})
	mux.HandleFunc("PUT /fs/{res}/{name}", func(w http.ResponseWriter, r *http.Request) {
		p, err := file(r)
		if err == nil {
			b, _ := io.ReadAll(r.Body)
			err = os.WriteFile(p, b, 0o644)
		}
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, map[string]string{"ok": "stored"})
	})
	// ls lists a filesystem resource (?res=) or any path in the sandbox (?path=)
	mux.HandleFunc("GET /ls", func(w http.ResponseWriter, r *http.Request) {
		dir := r.URL.Query().Get("path")
		if res := r.URL.Query().Get("res"); res != "" {
			dir = xbin.Resource(res)
		}
		ents, err := os.ReadDir(dir)
		if err != nil {
			fail(w, err)
			return
		}
		names := []string{}
		for _, e := range ents {
			names = append(names, e.Name())
		}
		reply(w, 200, map[string]any{"names": names})
	})
	mux.HandleFunc("GET /call", func(w http.ResponseWriter, r *http.Request) {
		resp, err := xbin.Client().Get("http://xbin" + r.URL.Query().Get("path"))
		if err != nil {
			reply(w, 502, map[string]string{"error": err.Error()})
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		reply(w, 200, map[string]any{"status": resp.StatusCode, "body": string(b)})
	})
	xbin.Serve(mux)
}
`

// psManifest is a probe tile's xbin.json: its own scope's resources at
// writer, partition (raw JSON, "" = none), and a published http expose. The
// partitioned fixture keeps its partitionNote when it asks to be
// unpartitioned: the switch page shows the manifest's note, the new one's.
func psManifest(tile, partition string) string {
	m := `{"runtime": "go"`
	if partition != "" {
		m += `, "partition": ` + partition
	}
	if partition != "" || tile == psTile {
		m += `, "partitionNote": "` + psNote + `"`
	}
	m += `, "uses": [`
	for i, res := range []string{"kv", "board", "files", "pub"} {
		if i > 0 {
			m += ", "
		}
		m += `{"target": "res:` + tile + `/` + res + `", "role": "writer"}`
	}
	if tile == psCaller {
		m += `, {"target": "` + psTile + `", "role": "writer"}`
	}
	m += `], "exposes": {"web": {"kind": "http", "paths": ["/pub/*"]}}`
	m += `, "expose": {"roles": {"reader": "Read", "writer": "Write"}}}` + "\n"
	return m
}

// psFiles is a probe tile: the backend, a go.mod of the tile's own module
// path, its scope's resources, a page, and the manifest.
func psFiles(tile, partition string) map[string]string {
	return map[string]string{
		"go.mod":          "module ps/" + strings.ReplaceAll(tile, "/", "_") + "\n\ngo 1.24\n\nrequire github.com/xbin-dev/xbin/sdk v0.0.0\n",
		"backend/main.go": psSource,
		"scope.json":      `{"resources": {"kv": {"type": "kv"}, "board": {"type": "kv", "shared": true}, "files": {"type": "filesystem"}, "pub": {"type": "filesystem", "shared": "read"}}}` + "\n",
		"index.html":      "<!doctype html><title>probe</title><p>the probe's page</p>\n",
		"xbin.json":       psManifest(tile, partition),
	}
}

// psWrite writes a probe tile, its manifest last (so no scan sees a
// partitioned manifest without its scope), and waits for it to register.
func psWrite(t *testing.T, d *xbindtest.Daemon, tile, partition string) {
	t.Helper()
	files := psFiles(tile, partition)
	manifest := files["xbin.json"]
	delete(files, "xbin.json")
	if err := d.WriteFiles(tile, files); err != nil {
		t.Fatal(err)
	}
	d.WriteTile(t, tile, map[string]string{"xbin.json": manifest})
}

// psWho is what the probe's /who answers.
type psWho struct {
	Env       string `json:"env"`
	Partition string `json:"partition"`
	User      string `json:"user"`
	Caller    struct {
		From, Role, User, UserLevel, ViewedBy, Deployment, Partition, PartitionID string
		Owner                                                                     bool
	} `json:"caller"`
	Boot  string `json:"boot"`
	Token string `json:"token"`
	Files string `json:"files"`
	Query string `json:"query"`
}

// psEnv is the smoke's daemon and its people. A person reaches a tile's API
// through the tile's frame, which their session mints (a user's session is
// no API principal of a tile unless they are an admin); an admin's session
// reaches it too.
type psEnv struct {
	d       *xbindtest.Daemon
	sess    map[string]string           // person → their session (a bearer)
	frames  map[string]xbindtest.Header // tile + "\x00" + person → a frame token of theirs
	ingress string                      // the ingress listener's address
}

var psPeople = map[string]string{"alice": "user", "bob": "user", "dave": "user", "carol": "admin"}

// psSetup boots an isolated xbind with owner auth, the vault and an ingress
// listener, and makes and signs in the people.
func psSetup(t *testing.T) *psEnv {
	t.Helper()
	a := xbindtest.Require(t)
	ing := freeLoopback(t)
	d := xbindtest.Start(t, a, xbindtest.Options{
		Auth: true,
		Env:  []string{"XBIN_VAULT_PASSPHRASE=xbindtest-vault"},
		Args: []string{"--ingress-listen", ing},
	})
	e := &psEnv{d: d, sess: map[string]string{}, frames: map[string]xbindtest.Header{}, ingress: ing}
	for id, role := range psPeople {
		tiles := map[string]string{"apps/*": "read"}
		if role == "admin" {
			tiles = nil
		}
		d.AddUser(t, id, "pw-"+id+"-5c2e81", role, tiles)
		e.sess[id] = d.Login(t, id, "pw-"+id+"-5c2e81")
	}
	return e
}

// as is the credential of person ("" = the owner token, xbindtest's default).
func (e *psEnv) as(person string) []xbindtest.Header {
	if person == "" {
		return nil
	}
	return []xbindtest.Header{xbindtest.H("Authorization", "Bearer "+e.sess[person])}
}

// frame is a frame token of tile minted by person ("" = the owner).
func (e *psEnv) frame(t *testing.T, tile, person string) xbindtest.Header {
	t.Helper()
	var out struct{ Token string }
	e.d.Must(t, "GET", "/api/xbin/frame-token?component="+tile, nil, 200, e.as(person)...).Decode(t, &out)
	if out.Token == "" {
		t.Fatalf("no frame token of %s for %q", tile, person)
	}
	return xbindtest.FrameHeader(out.Token)
}

// fr is frame, minted once per tile and person.
func (e *psEnv) fr(t *testing.T, tile, person string) xbindtest.Header {
	t.Helper()
	k := tile + "\x00" + person
	h, ok := e.frames[k]
	if !ok {
		h = e.frame(t, tile, person)
		e.frames[k] = h
	}
	return h
}

// forget drops person's frame tokens (fr mints new ones).
func (e *psEnv) forget(person string) {
	for k := range e.frames {
		if strings.HasSuffix(k, "\x00"+person) {
			delete(e.frames, k)
		}
	}
}

// who is the probe's /who at path through hdrs, waiting (a cold start
// builds) until it answers 200; a refusal (401, 403, 404, 409) fails at once.
func (e *psEnv) who(t *testing.T, path string, hdrs ...xbindtest.Header) psWho {
	t.Helper()
	var r xbindtest.Resp
	xbindtest.Eventually(t, 4*time.Minute, "GET "+path+" answers", func() (bool, string) {
		r = e.d.Call(t, "GET", path, nil, hdrs...)
		switch {
		case r.Status == 502 && strings.Contains(string(r.Body), "build failed"):
			t.Fatalf("GET %s: the probe doesn't build: %s", path, r)
		case r.Status == 401 || r.Status == 403 || r.Status == 404 || r.Status == 409:
			t.Fatalf("GET %s: %d %s", path, r.Status, r)
		}
		return r.Status == 200, fmt.Sprint(r.Status, " ", r)
	})
	var w psWho
	r.Decode(t, &w)
	return w
}

// value is a probe route's stored value ("" and the status when it isn't 200).
func (e *psEnv) value(t *testing.T, path string, hdrs ...xbindtest.Header) (string, int, string) {
	t.Helper()
	r := e.d.Call(t, "GET", path, nil, hdrs...)
	var v struct{ Value string }
	if r.Status == 200 {
		r.Decode(t, &v)
	}
	return v.Value, r.Status, r.String()
}

// put stores body at a probe route, failing unless it answers 200.
func (e *psEnv) put(t *testing.T, path, body string, hdrs ...xbindtest.Header) {
	t.Helper()
	e.d.Must(t, "PUT", path, body, 200, hdrs...)
}

// row is the tile's components row's partition entry (nil: none).
type psRow struct {
	State   string `json:"state"`
	User    bool   `json:"user"`
	Global  bool   `json:"global"`
	Request *struct {
		User, Global, Declined bool
	} `json:"request"`
}

func (e *psEnv) row(t *testing.T, tile string) *psRow {
	t.Helper()
	var out struct {
		Component struct {
			Partition *psRow `json:"partition"`
		}
	}
	e.d.Must(t, "GET", "/api/xbin/components/"+tile, nil, 200).Decode(t, &out)
	return out.Component.Partition
}

// waitState waits for tile's row to be in state ("" = unpartitioned: no
// partition entry, or one in state unpartitioned asking nothing).
func (e *psEnv) waitState(t *testing.T, tile, state string) *psRow {
	t.Helper()
	var row *psRow
	xbindtest.Eventually(t, 30*time.Second, tile+"'s partition state is "+state, func() (bool, string) {
		row = e.row(t, tile)
		switch {
		case row == nil:
			return state == "", "no partition entry"
		case state == "": // unpartitioned: no entry, or one saying so
			return row.State == "unpartitioned" && !row.User && !row.Global && row.Request == nil, fmt.Sprintf("%+v", *row)
		}
		return row.State == state, fmt.Sprintf("%+v", *row)
	})
	return row
}

// uid is person's uid in the users store ("" = none minted).
func (e *psEnv) uid(t *testing.T, person string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.d.WS, "data", "users.json"))
	if err != nil {
		t.Fatal(err)
	}
	var users struct {
		Users []struct{ ID, UID string }
	}
	if err := json.Unmarshal(b, &users); err != nil {
		t.Fatalf("users.json: %v", err)
	}
	for _, u := range users.Users {
		if u.ID == person {
			return u.UID
		}
	}
	return ""
}

// modeRecord is apps/pt's mode.json, decoded loosely.
type psModeRecord struct {
	Mode    *struct{ User, Global bool }
	Request *struct{ Spec *struct{ User, Global bool } }
	History []struct {
		Op    string
		Wiped map[string]int64
	}
}

func (e *psEnv) modeRecord(t *testing.T, tile string) (psModeRecord, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.d.WS, "data", "partitions", util.TileKey(tile), "mode.json"))
	if os.IsNotExist(err) {
		return psModeRecord{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	var m psModeRecord
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s's mode.json: %v", tile, err)
	}
	return m, true
}

// partDir is person's partition namespace of scope on disk (ciphertext).
func (e *psEnv) partDir(scope, pkey string) string {
	return filepath.Join(e.d.WS, "data", "resources-enc", ".partitions", strings.ReplaceAll(scope, "/", "~"), "main", pkey)
}

var psPkey = regexp.MustCompile(`^u-[0-9a-f]{32}$`)

// psRaw sends one request with exactly hdrs — no owner token added, no
// redirect followed — and returns the status, body and headers.
func psRaw(t *testing.T, method, url string, hdrs ...xbindtest.Header) (int, string, http.Header) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hdrs {
		req.Header.Set(h.K, h.V)
	}
	c := &http.Client{Timeout: time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

// gocryptfs lists the gocryptfs processes serving the workspace's volumes
// under dir (relative to the workspace: data/resources-enc/<…>), as
// "<pid> <ciphertext dir>".
func (e *psEnv) gocryptfs(dir string) []string {
	var out []string
	ents, _ := os.ReadDir("/proc")
	for _, ent := range ents {
		b, err := os.ReadFile(filepath.Join("/proc", ent.Name(), "cmdline"))
		if err != nil {
			continue
		}
		args := strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
		if len(args) < 2 || filepath.Base(args[0]) != "gocryptfs" {
			continue
		}
		for _, a := range args {
			if strings.HasPrefix(a, filepath.Join(e.d.WS, dir)) {
				out = append(out, ent.Name()+" "+strings.TrimPrefix(a, e.d.WS+"/"))
				break
			}
		}
	}
	return out
}

// mode is POST /partitions/mode as person.
func (e *psEnv) mode(t *testing.T, person string, body map[string]any) xbindtest.Resp {
	t.Helper()
	return e.d.Call(t, "POST", "/api/xbin/partitions/mode", body, e.as(person)...)
}

var (
	psBoth = map[string]bool{"user": true, "global": true}
	psUser = map[string]bool{"user": true, "global": false}
)

func TestPartitionsSmoke(t *testing.T) {
	t.Parallel()
	e := psSetup(t)
	d := e.d

	// the unpartitioned caller and the tile that holds data before it asks
	// to partition, both built before any partitioned tile exists
	psWrite(t, d, psCaller, "")
	psWrite(t, d, psPlain, "")

	t.Run("zero-state", func(t *testing.T) {
		// 10 §A.1: a workspace with no partitioned tile — no header, env,
		// meta, row field, uid or partition store
		for _, who := range []psWho{
			e.who(t, "/api/"+psCaller+"/who"),
			e.who(t, "/api/"+psCaller+"/who", e.fr(t, psCaller, "alice")),
			e.who(t, "/api/"+psCaller+"/who", e.as("carol")...),
		} {
			if who.Env != "" || who.Partition != "" || who.Caller.Partition != "" || who.Caller.PartitionID != "" {
				t.Errorf("an unpartitioned tile sees partition state: %+v", who)
			}
		}
		if row := e.row(t, psCaller); row != nil {
			t.Errorf("an unpartitioned tile's row has a partition entry: %+v", *row)
		}
		doc := d.Call(t, "GET", "/c/"+psCaller+"/", nil, e.as("alice")...)
		if doc.Status != 200 || strings.Contains(string(doc.Body), "xbin-partition") {
			t.Errorf("an unpartitioned tile's document: %d, meta present: %v", doc.Status, strings.Contains(string(doc.Body), "xbin-partition"))
		}
		for p := range psPeople {
			if u := e.uid(t, p); u != "" {
				t.Errorf("%s has a uid (%s) before any partition was used", p, u)
			}
		}
		for _, p := range []string{filepath.Join(d.WS, "data", "partitions"), filepath.Join(d.WS, "data", "resources-enc", ".partitions")} {
			if _, err := os.Stat(p); err == nil {
				t.Errorf("%s exists in a workspace without a partitioned tile", p)
			}
		}
	})

	// the unpartitioned tile's data, before it asks to partition (below)
	e.put(t, "/api/"+psPlain+"/kv/kv/secret", "plain-data")

	psWrite(t, d, psTile, `["user", "global"]`)
	d.Grant(t, psCaller, psTile, "writer")
	// the published host, bound before any instance starts: a binding
	// restarts every live instance of the tile, people's too, which would
	// race the boot comparisons below
	d.Must(t, "POST", "/api/xbin/bindings", map[string]string{"component": psTile, "slot": "web", "provider": "runtime", "host": psHost}, 200)

	t.Run("auto-mode", func(t *testing.T) {
		// 01 §2.3: a tile that holds no data takes the manifest's mode at once
		row := e.waitState(t, psTile, "partitioned")
		if !row.User || !row.Global || row.Request != nil {
			t.Errorf("the recorded mode: %+v", *row)
		}
		m, ok := e.modeRecord(t, psTile)
		if !ok || m.Mode == nil || !m.Mode.User || !m.Mode.Global || len(m.History) == 0 || m.History[0].Op != "auto" {
			t.Errorf("mode.json: %v %+v", ok, m)
		}
	})

	ids := map[string]string{}    // person → their partition id
	boots := map[string]string{}  // person → their instance's boot id
	tokens := map[string]string{} // person → their instance token
	t.Run("own-partition", func(t *testing.T) {
		// 02 §3/§6: a person's frame reaches their own partition, which
		// learns it from XBIN_PARTITION and the headers; an admin's session
		// reaches the admin's own
		for _, p := range []string{"alice", "bob", "carol"} {
			hdr := e.fr(t, psTile, p)
			w := e.who(t, "/api/"+psTile+"/who", hdr)
			part := "user:" + p
			if w.Env != part || w.Partition != part || w.User != p || w.Caller.Partition != part || w.Caller.User != p {
				t.Errorf("%s's frame reaches %+v, want %s", p, w, part)
			}
			if !psPkey.MatchString(w.Caller.PartitionID) {
				t.Errorf("%s's X-XBin-Partition-Id %q", p, w.Caller.PartitionID)
			}
			if uid := e.uid(t, p); uid == "" || util.PartitionKey(p, uid) != w.Caller.PartitionID {
				t.Errorf("%s's partition id %q isn't PartitionKey(%s, uid %q)", p, w.Caller.PartitionID, p, uid)
			}
			ids[p], boots[p], tokens[p] = w.Caller.PartitionID, w.Boot, w.Token
			t.Logf("%s: boot %s, id %s, caller %+v", p, w.Boot, w.Caller.PartitionID, w.Caller)

			doc := d.Call(t, "GET", "/c/"+psTile+"/", nil, e.as(p)...)
			if want := `<meta name="xbin-partition" content="` + part + `">`; doc.Status != 200 || !strings.Contains(string(doc.Body), want) {
				t.Errorf("%s's document: %d, want %s in %s", p, doc.Status, want, cut(doc.String(), 600))
			}
		}
		if w := e.who(t, "/api/"+psTile+"/who", e.as("carol")...); w.Env != "user:carol" || w.Boot != boots["carol"] {
			t.Errorf("carol's (admin) session reaches %+v, her frame boot %s", w, boots["carol"])
		}
		if r := d.Call(t, "GET", "/api/"+psTile+"/who", nil, e.as("alice")...); r.Status != 403 {
			t.Logf("alice's own session on the tile's API (a user's session isn't its principal): %d %s", r.Status, r)
		}
		if ids["alice"] == ids["bob"] || boots["alice"] == boots["bob"] || tokens["alice"] == tokens["bob"] || tokens["alice"] == "" {
			t.Errorf("alice and bob share an instance: ids %v boots %v", ids, boots)
		}
	})

	t.Run("data-apart", func(t *testing.T) {
		// 03 §B: each partition's kv and filesystem are its own; a shared
		// resource is one copy
		for _, p := range []string{"alice", "bob"} {
			e.put(t, "/api/"+psTile+"/kv/kv/secret", p+"-secret", e.fr(t, psTile, p))
			e.put(t, "/api/"+psTile+"/fs/files/note", p+"-note", e.fr(t, psTile, p))
		}
		e.put(t, "/api/"+psTile+"/kv/board/shared", "from-alice", e.fr(t, psTile, "alice"))
		for _, p := range []string{"alice", "bob"} {
			if v, st, body := e.value(t, "/api/"+psTile+"/kv/kv/secret", e.fr(t, psTile, p)); v != p+"-secret" {
				t.Errorf("%s's kv: %d %s", p, st, body)
			}
			if v, st, body := e.value(t, "/api/"+psTile+"/fs/files/note", e.fr(t, psTile, p)); v != p+"-note" {
				t.Errorf("%s's file: %d %s", p, st, body)
			}
			if v, st, body := e.value(t, "/api/"+psTile+"/kv/board/shared", e.fr(t, psTile, p)); v != "from-alice" {
				t.Errorf("the shared board for %s: %d %s", p, st, body)
			}
			if _, err := os.Stat(e.partDir(psTile, ids[p])); err != nil {
				t.Errorf("%s's partition namespace on disk: %v", p, err)
			}
		}
		// inside alice's sandbox: her files at XBIN_RES_FILES, and nothing
		// of bob's where the host keeps people's volumes (03 §Tests)
		if r := d.Call(t, "GET", "/api/"+psTile+"/ls?res=files", nil, e.fr(t, psTile, "alice")); r.Status != 200 || !strings.Contains(string(r.Body), `"names":["note"]`) {
			t.Errorf("alice's files resource: %d %s", r.Status, r)
		}
		for _, p := range []string{
			filepath.Join(d.WS, "data", "resources-enc", ".partitions", "apps~pt", "main"),
			filepath.Join(d.WS, ".xbin", "resenc", ".partitions", "apps~pt", "main"),
			filepath.Join(d.WS, ".xbin", "resenc", ".partitions", "apps~pt", "main", ids["bob"]),
		} {
			r := d.Call(t, "GET", "/api/"+psTile+"/ls?path="+p, nil, e.fr(t, psTile, "alice"))
			if strings.Contains(string(r.Body), ids["bob"]) || strings.Contains(string(r.Body), "note") {
				t.Errorf("BUG: alice's sandbox sees bob's volume at %s: %d %s", p, r.Status, r)
			}
			t.Logf("alice's sandbox, ls %s: %d %s", p, r.Status, cut(r.String(), 120))
		}
		// "shared": "read" — global writes, people's partitions only read
		e.put(t, "/api/"+psTile+"/fs/pub/readme", "from-global")
		for _, p := range []string{"alice", "bob"} {
			if v, st, body := e.value(t, "/api/"+psTile+"/fs/pub/readme", e.fr(t, psTile, p)); v != "from-global" {
				t.Errorf("the read-shared filesystem for %s: %d %s", p, st, body)
			}
		}
		if r := d.Call(t, "PUT", "/api/"+psTile+"/fs/pub/mine", "alice-pub", e.fr(t, psTile, "alice")); r.Status == 200 {
			t.Errorf("alice wrote the read-shared filesystem: %d %s", r.Status, r)
		} else {
			t.Logf("alice's write to the read-shared filesystem: %d %s", r.Status, cut(r.String(), 160))
		}
	})

	t.Run("deployment", func(t *testing.T) {
		// PD-17, 03 §B.9: people's partitions live on the primary alone; a
		// non-primary deployment seeded from the primary's data gets global's
		// (today's keys), never a person's — and serves one shared instance
		e.put(t, "/api/"+psTile+"/kv/kv/secret", "global-before-dev")
		add := d.Call(t, "POST", "/api/xbin/deployments/add", map[string]any{"tile": psTile, "deployment": "dev", "data": "seed", "confirm": "copy-data"})
		if add.Status != 200 {
			t.Fatalf("adding dev, seeded: %d %s", add.Status, add)
		}
		t.Logf("adding dev: %s", cut(add.String(), 300))
		dev := "/api/" + psTile + "+dev"
		w := e.who(t, dev+"/who")
		t.Logf("the owner token on dev: %+v", w)
		if w.Caller.Partition == "user:alice" || w.Caller.PartitionID != "" || strings.HasPrefix(w.Env, "user:") {
			t.Errorf("dev's instance is a person's: %+v", w)
		}
		if v, st, body := e.value(t, dev+"/kv/kv/secret"); v != "global-before-dev" {
			t.Errorf("dev's seeded kv: %d %s (want global's)", st, body)
		}
		for _, p := range []string{dev + "/fs/files/note", dev + "/ls?res=files", dev + "/keys/kv", dev + "/ls?path=" + filepath.Join(w.Files, "..")} {
			r := d.Call(t, "GET", p, nil)
			if strings.Contains(string(r.Body), "alice-") || strings.Contains(string(r.Body), "bob-") || strings.Contains(string(r.Body), `"note"`) {
				t.Errorf("BUG: dev seeded a person's data, %s: %d %s", p, r.Status, r)
			}
			t.Logf("dev, %s: %d %s", p, r.Status, cut(r.String(), 160))
		}
		// carol (admin, a tile writer) reaches dev's shared instance; bob (a
		// reader) gets no frame of it
		var ft struct{ Token string }
		if r := d.Call(t, "GET", "/api/xbin/frame-token?component="+psTile+"&deployment=dev", nil, e.as("carol")...); r.Status == 200 {
			r.Decode(t, &ft)
			cw := e.who(t, dev+"/who", xbindtest.FrameHeader(ft.Token))
			if strings.HasPrefix(cw.Env, "user:") || cw.Caller.PartitionID != "" || cw.Boot != w.Boot {
				t.Errorf("carol's frame of dev reaches %+v, want dev's one instance (boot %s)", cw, w.Boot)
			}
			if v, st, body := e.value(t, dev+"/kv/kv/secret", xbindtest.FrameHeader(ft.Token)); v != "global-before-dev" {
				t.Errorf("carol's frame of dev reads: %d %s", st, body)
			}
		} else {
			t.Errorf("carol's frame token of dev: %d %s", r.Status, r)
		}
		if r := d.Call(t, "GET", "/api/xbin/frame-token?component="+psTile+"&deployment=dev", nil, e.as("bob")...); r.Status == 200 {
			t.Errorf("bob (a reader) got a frame token of dev: %s", r)
		}
		// alice's frame of the primary on dev's URL: never her partition there
		r := d.Call(t, "GET", dev+"/kv/kv/secret", nil, e.fr(t, psTile, "alice"))
		if strings.Contains(string(r.Body), "alice-") {
			t.Errorf("BUG: alice's frame reads her partition through dev's URL: %d %s", r.Status, r)
		}
		t.Logf("alice's primary frame on dev's URL: %d %s", r.Status, cut(r.String(), 160))
		e.put(t, "/api/"+psTile+"/kv/kv/secret", "global-secret") // global's, as before
	})

	t.Run("bob-never-sees-alice", func(t *testing.T) {
		// G1: no route hands alice's partition data to bob
		bobFrame := e.fr(t, psTile, "bob")
		callerFrame := e.fr(t, psCaller, "bob")
		bobSess := e.as("bob")[0]
		bobTok := xbindtest.H("Authorization", "Bearer "+tokens["bob"])
		spoofs := []xbindtest.Header{xbindtest.H("X-XBin-Partition", "user:alice"), xbindtest.H("X-XBin-Partition-Id", ids["alice"]),
			xbindtest.H("X-XBin-User", "alice"), xbindtest.H("X-XBin-From", psTile), xbindtest.H("X-XBin-Deployment", "main")}
		with := func(h xbindtest.Header, more ...xbindtest.Header) []xbindtest.Header {
			return append([]xbindtest.Header{h}, more...)
		}
		type attempt struct {
			name string
			path string
			hdrs []xbindtest.Header
			want string // "" = any answer but alice's data; else bob's own value, 200
		}
		kv, file, api := "/api/"+psTile+"/kv/kv/secret", "/api/"+psTile+"/fs/files/note", "/api/xbin/kv/res:"+psTile+"/kv/secret"
		atts := []attempt{
			{"frame", kv, with(bobFrame), "bob-secret"},
			{"frame, file", file, with(bobFrame), "bob-note"},
			{"frame, ?xbin-partition=global", kv + "?xbin-partition=global", with(bobFrame), ""},
			{"frame, ?xbin-partition=user:alice", file + "?xbin-partition=user:alice", with(bobFrame), ""},
			{"frame, spoofed X-XBin-* headers", kv, with(bobFrame, spoofs...), "bob-secret"},
			{"frame, spoofed headers, file", file, with(bobFrame, spoofs...), "bob-note"},
			{"session", kv, with(bobSess), ""},
			{"session, spoofed headers", kv, with(bobSess, spoofs...), ""},
			{"the primary's deployment URL", "/api/" + psTile + "+main/kv/kv/secret", with(bobFrame), ""},
			{"the primary's deployment URL, session", "/api/" + psTile + "+main/fs/files/note", with(bobSess), ""},
			{"the primary's deployment URL, spoofed headers", "/api/" + psTile + "+main/fs/files/note", with(bobFrame, spoofs...), ""},
			{"dev's URL, frame", "/api/" + psTile + "+dev/kv/kv/secret", with(bobFrame), ""},
			{"dev's URL, frame, file", "/api/" + psTile + "+dev/fs/files/note", with(bobFrame), ""},
			{"dev's URL, session", "/api/" + psTile + "+dev/fs/files/note", with(bobSess), ""},
			{"kv API, frame", api, with(bobFrame), "bob-secret"},
			{"kv API, frame, spoofed headers", api, with(bobFrame, spoofs...), "bob-secret"},
			{"kv API list, frame", "/api/xbin/kv/res:" + psTile + "/kv/?prefix=", with(bobFrame), ""},
			{"kv API, session", api, with(bobSess), ""},
			{"kv API, bob's instance token", api, with(bobTok), "bob-secret"},
			{"kv API, bob's instance token, spoofed headers", api, with(bobTok, spoofs...), "bob-secret"},
			{"bob's instance token on the tile's API", kv, with(bobTok), ""},
			{"an unpartitioned tile's frame of bob's", kv, with(callerFrame), ""},
			{"the unpartitioned tile's backend, as bob's frame", "/api/" + psCaller + "/call?path=" + kv, with(callerFrame), ""},
		}
		for _, a := range atts {
			r := d.Call(t, "GET", a.path, nil, a.hdrs...)
			if strings.Contains(string(r.Body), "alice-") {
				t.Errorf("BUG: %s: bob reads alice's data: %d %s", a.name, r.Status, r)
				continue
			}
			if a.want != "" {
				got := string(r.Body)
				var v struct{ Value string }
				if json.Unmarshal(r.Body, &v) == nil && v.Value != "" {
					got = v.Value
				}
				if r.Status != 200 || got != a.want {
					t.Errorf("%s: %d %s, want bob's own %q", a.name, r.Status, r, a.want)
				}
			}
			t.Logf("%s: %d %s", a.name, r.Status, cut(r.String(), 160))
		}
		// his documents name his partition, the primary's deployment URL's too
		for _, p := range []string{"/c/" + psTile + "/", "/c/" + psTile + "+main/", "/c/" + psTile + "+dev/"} {
			doc := d.Call(t, "GET", p, nil, bobSess)
			if strings.Contains(string(doc.Body), "user:alice") {
				t.Errorf("BUG: %s names alice's partition to bob", p)
			}
			t.Logf("%s for bob: %d, his meta: %v", p, doc.Status, strings.Contains(string(doc.Body), `content="user:bob"`))
		}
	})

	t.Run("global", func(t *testing.T) {
		// 02 §3, 05 §5: the root token, another (unpartitioned) tile and
		// ingress reach the global instance, at today's keys
		w := e.who(t, "/api/"+psTile+"/who")
		if w.Env != "global" || w.Partition != "global" || w.User != "" || w.Caller.Partition != "global" || w.Caller.PartitionID != "" {
			t.Errorf("the owner token reaches %+v, want global", w)
		}
		e.put(t, "/api/"+psTile+"/kv/kv/secret", "global-secret")
		if v, st, body := e.value(t, "/api/"+psTile+"/kv/kv/secret"); v != "global-secret" {
			t.Errorf("global's kv: %d %s", st, body)
		}
		if v, _, _ := e.value(t, "/api/"+psTile+"/kv/kv/secret", e.fr(t, psTile, "alice")); v != "alice-secret" {
			t.Errorf("alice's kv after global's write: %q", v)
		}
		// the unpartitioned tile's backend (its grant) and its frame
		var c struct {
			Status int
			Body   string
		}
		d.Must(t, "GET", "/api/"+psCaller+"/call?path=/api/"+psTile+"/who", nil, 200).Decode(t, &c)
		var cw psWho
		if c.Status != 200 || json.Unmarshal([]byte(c.Body), &cw) != nil || cw.Env != "global" || cw.Caller.From != psCaller || cw.Caller.Partition != "" {
			t.Errorf("the unpartitioned tile's backend reaches: %d %s", c.Status, c.Body)
		}
		fw := e.who(t, "/api/"+psTile+"/who", e.fr(t, psCaller, ""))
		if fw.Env != "global" || fw.Caller.From != psCaller || fw.Caller.Partition != "" || fw.Boot != w.Boot {
			t.Errorf("the unpartitioned tile's frame reaches %+v, want global (boot %s)", fw, w.Boot)
		}
		aw := e.who(t, "/api/"+psTile+"/who", e.fr(t, psCaller, "alice"))
		if aw.Env != "global" || aw.Caller.Partition != "" {
			t.Errorf("alice's frame of the unpartitioned tile reaches %+v, want global", aw)
		}
		d.Must(t, "GET", "/api/"+psCaller+"/call?path=/api/"+psTile+"/kv/kv/secret", nil, 200).Decode(t, &c)
		if !strings.Contains(c.Body, "global-secret") {
			t.Errorf("the unpartitioned tile's backend reads: %d %s", c.Status, c.Body)
		}
		// ingress: the published host (bound above) reaches global only
		var pub psWho
		xbindtest.Eventually(t, time.Minute, "ingress serves "+psHost, func() (bool, string) {
			req, _ := http.NewRequest("GET", "http://"+e.ingress+"/pub/who", nil)
			req.Host = psHost
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return false, err.Error()
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			return resp.StatusCode == 200 && json.Unmarshal(b, &pub) == nil, fmt.Sprint(resp.StatusCode, " ", string(b))
		})
		if pub.Env != "global" || pub.Caller.From != "ingress" || pub.Caller.Partition != "" || pub.Boot != w.Boot {
			t.Errorf("ingress reaches %+v, want global (boot %s)", pub, w.Boot)
		}
	})

	t.Run("admin", func(t *testing.T) {
		// G2: an admin reaches their own partition, never a person's; the
		// root token reaches global
		for _, p := range []string{
			"/api/" + psTile + "/kv/kv/secret",
			"/api/" + psTile + "/kv/kv/secret?xbin-partition=user:alice",
			"/api/" + psTile + "/fs/files/note",
			"/api/" + psTile + "/ls?res=files",
			"/api/" + psTile + "/keys/kv",
			"/api/xbin/kv/res:" + psTile + "/kv/secret",
			"/api/xbin/kv/res:" + psTile + "/kv/?prefix=",
		} {
			for _, hdrs := range [][]xbindtest.Header{e.as("carol"), {e.fr(t, psTile, "carol")}, nil} {
				r := d.Call(t, "GET", p, nil, hdrs...)
				if strings.Contains(string(r.Body), "alice-") || strings.Contains(string(r.Body), "bob-") {
					t.Errorf("BUG: an admin credential reads a person's data at %s: %d %s", p, r.Status, r)
				}
			}
		}
		// admins see who runs (metadata), never what it holds
		r := d.Must(t, "GET", "/api/xbin/sandboxes", nil, 200, e.as("carol")...)
		if strings.Contains(string(r.Body), "alice-") {
			t.Errorf("BUG: the sandbox list shows alice's data")
		}
		t.Logf("the sandbox list names alice's partition: %v", strings.Contains(string(r.Body), `"user:alice"`))
	})

	t.Run("view-as", func(t *testing.T) {
		// 02 §3, G2: an admin viewing the workspace as alice (D64) reaches
		// no partition of hers — no frame token, no data
		var tk struct{ URL string }
		d.Must(t, "POST", "/api/xbin/impersonate", map[string]string{"user": "alice"}, 200, e.as("carol")...).Decode(t, &tk)
		st, _, hdr := psRaw(t, "GET", d.URL+tk.URL, xbindtest.H("Authorization", "Bearer "+e.sess["carol"]))
		var cookie string
		for _, c := range (&http.Response{Header: hdr}).Cookies() {
			if c.Value != "" {
				cookie = c.Name + "=" + c.Value
			}
		}
		if st != http.StatusFound || cookie == "" {
			t.Fatalf("redeeming the view-as link: %d, cookie %q", st, cookie)
		}
		view := xbindtest.H("Cookie", cookie)
		if st, body, _ := psRaw(t, "GET", d.URL+"/api/xbin/whoami", view); st != 200 || !strings.Contains(body, "impersonatedBy") {
			t.Fatalf("the view-as session: %d %s", st, body)
		}
		paths := []string{
			"/api/" + psTile + "/kv/kv/secret",
			"/api/" + psTile + "/fs/files/note",
			"/api/" + psTile + "/who",
			"/api/xbin/kv/res:" + psTile + "/kv/secret",
			"/c/" + psTile + "/",
		}
		for _, p := range paths {
			st, body, _ := psRaw(t, "GET", d.URL+p, view)
			if strings.Contains(body, "alice-") || strings.Contains(body, `content="user:alice"`) {
				t.Errorf("BUG: view-as alice reads her partition at %s: %d %s", p, st, body)
			}
			if strings.HasPrefix(p, "/c/") && strings.Contains(body, `name="xbin-frame-token" content="`) &&
				!strings.Contains(body, `name="xbin-frame-token" content=""`) {
				t.Errorf("view-as alice's document of the partitioned tile carries a frame token")
			}
			t.Logf("view-as alice, %s: %d %s", p, st, cut(body, 160))
		}
		// the renewal route still mints one (02 §10 keeps renewal as is); it
		// carries the impersonator, so it opens nothing of alice's either
		st, body, _ := psRaw(t, "GET", d.URL+"/api/xbin/frame-token?component="+psTile, view)
		t.Logf("view-as alice, GET /frame-token: %d %s", st, cut(body, 60))
		var ft struct{ Token string }
		if st == 200 && json.Unmarshal([]byte(body), &ft) == nil && ft.Token != "" {
			for _, p := range paths[:4] {
				r := d.Call(t, "GET", p, nil, xbindtest.FrameHeader(ft.Token))
				if strings.Contains(string(r.Body), "alice-") || strings.Contains(string(r.Body), "user:alice") {
					t.Errorf("BUG: view-as alice's renewed frame token reads her partition at %s: %d %s", p, r.Status, r)
				}
				t.Logf("view-as alice's renewed frame token, %s: %d %s", p, r.Status, cut(r.String(), 160))
			}
		}
	})

	t.Run("caps", func(t *testing.T) {
		// 03 §A.5 (PD-18): past the tile's cap, with every partition in use,
		// a new person's start is 503; nothing of another person's is stopped
		running := 0
		for _, p := range []string{"alice", "bob", "carol"} {
			e.who(t, "/api/"+psTile+"/who", e.fr(t, psTile, p)) // in use: an interactive request now
			running++
		}
		d.Must(t, "POST", "/api/xbin/partitions/limits", map[string]any{"tile": psTile, "maxRunning": running}, 200)
		r := d.Call(t, "GET", "/api/"+psTile+"/who", nil, e.fr(t, psTile, "dave"))
		if r.Status != 503 || !strings.Contains(string(r.Body), "too many people's instances of "+psTile) {
			t.Errorf("dave past the cap: %d %s", r.Status, r)
		}
		if w := e.who(t, "/api/"+psTile+"/who", e.fr(t, psTile, "alice")); w.Boot != boots["alice"] {
			t.Errorf("alice's instance changed (boot %s → %s) under the cap", boots["alice"], w.Boot)
		}
		d.Must(t, "POST", "/api/xbin/partitions/limits", map[string]any{"tile": psTile, "maxRunning": 0}, 200)
		if w := e.who(t, "/api/"+psTile+"/who", e.fr(t, psTile, "dave")); w.Env != "user:dave" {
			t.Errorf("dave after the cap was cleared: %+v", w)
		}
	})

	t.Run("token-revocation", func(t *testing.T) {
		// 02 §2: a user partition's instance token authenticates only while
		// its partition is covered (the person enabled, reading the tile)
		kv := "/api/xbin/kv/res:" + psTile + "/kv/secret"
		cur := e.who(t, "/api/"+psTile+"/who", e.fr(t, psTile, "alice"))
		if cur.Token != tokens["alice"] {
			// a restart since (the ingress binding, say) revoked the earlier
			// generation's token: it must not authenticate any more
			r := d.Call(t, "GET", kv, nil, xbindtest.H("Authorization", "Bearer "+tokens["alice"]))
			t.Logf("alice's instance restarted since own-partition (boot %s → %s); its earlier token: %d", boots["alice"], cur.Boot, r.Status)
			if r.Status != 401 {
				t.Errorf("alice's earlier generation's token after a restart: %d %s (want 401)", r.Status, r)
			}
		}
		tokens["alice"], boots["alice"] = cur.Token, cur.Boot
		tok := xbindtest.H("Authorization", "Bearer "+tokens["alice"])
		if r := d.Call(t, "GET", kv, nil, tok); r.Status != 200 || string(r.Body) != "alice-secret" {
			t.Fatalf("alice's instance token before: %d %s", r.Status, r)
		}
		d.Must(t, "PATCH", "/api/xbin/users/alice", map[string]any{"disabled": true}, 200)
		if r := d.Call(t, "GET", kv, nil, tok); r.Status != 401 {
			t.Errorf("alice's instance token while she is disabled: %d %s (want 401)", r.Status, r)
		}
		if r := d.Call(t, "GET", "/api/"+psTile+"/kv/kv/secret", nil, e.fr(t, psTile, "alice")); r.Status == 200 {
			t.Errorf("alice's frame while she is disabled: %d %s", r.Status, r)
		}
		d.Must(t, "PATCH", "/api/xbin/users/alice", map[string]any{"disabled": false}, 200)
		r := d.Call(t, "GET", kv, nil, tok)
		t.Logf("alice's old instance token once she is enabled again: %d %s", r.Status, cut(r.String(), 120))
		if d.Call(t, "GET", "/api/xbin/whoami", nil, e.as("alice")...).Status != 200 {
			e.sess["alice"] = d.Login(t, "alice", "pw-alice-5c2e81")
		}
		e.forget("alice")
		w := e.who(t, "/api/"+psTile+"/who", e.fr(t, psTile, "alice"))
		t.Logf("alice's instance after she is enabled again: boot %s (before %s)", w.Boot, boots["alice"])
		tokens["alice"] = w.Token
		if v, st, body := e.value(t, "/api/"+psTile+"/kv/kv/secret", e.fr(t, psTile, "alice")); v != "alice-secret" {
			t.Errorf("alice's data after she is enabled again: %d %s", st, body)
		}
		// losing read of the tile uncovers her partition too
		tok = xbindtest.H("Authorization", "Bearer "+tokens["alice"])
		if r := d.Call(t, "GET", kv, nil, tok); r.Status != 200 {
			t.Fatalf("alice's current instance token: %d %s", r.Status, r)
		}
		d.Must(t, "PATCH", "/api/xbin/users/alice", map[string]any{"tiles": map[string]string{"apps/pcall": "read", "apps/plain": "read"}}, 200)
		if r := d.Call(t, "GET", kv, nil, tok); r.Status != 401 {
			t.Errorf("alice's instance token once she can't read %s: %d %s (want 401)", psTile, r.Status, r)
		}
		d.Must(t, "PATCH", "/api/xbin/users/alice", map[string]any{"tiles": map[string]string{"apps/*": "read"}}, 200)
		e.forget("alice")
		tokens["alice"] = e.who(t, "/api/"+psTile+"/who", e.fr(t, psTile, "alice")).Token
	})

	t.Run("mode-keep", func(t *testing.T) {
		// 01 §2.4: a manifest change on a tile that holds data is a request;
		// nothing runs while it's pending; keep runs the recorded mode again
		d.WriteTile(t, psTile, map[string]string{"xbin.json": psManifest(psTile, "")})
		row := e.waitState(t, psTile, "pending")
		if !row.User || !row.Global || row.Request == nil || row.Request.User || row.Request.Global || row.Request.Declined {
			t.Errorf("the pending row: %+v %+v", *row, row.Request)
		}
		for _, who := range [][]xbindtest.Header{{e.fr(t, psTile, "alice")}, nil} {
			r := d.Call(t, "GET", "/api/"+psTile+"/who", nil, who...)
			var body struct {
				Error     string
				Partition struct{ State string }
			}
			_ = json.Unmarshal(r.Body, &body)
			if r.Status != 409 || body.Partition.State != "pending" || !strings.Contains(body.Error, "partition mode switch is requested") {
				t.Errorf("a call while pending: %d %s", r.Status, r)
			}
		}
		page := d.Call(t, "GET", "/c/"+psTile+"/", nil, append(e.as("alice"), xbindtest.H("Sec-Fetch-Dest", "iframe"))...)
		if page.Status != 409 || !strings.Contains(string(page.Body), "partition mode switch is requested") ||
			!strings.Contains(string(page.Body), "All data in this tile will be deleted") || !strings.Contains(string(page.Body), "&lt;b&gt;notes&lt;/b&gt;") ||
			strings.Contains(string(page.Body), "<b>notes") || strings.Contains(string(page.Body), "<script") {
			t.Errorf("the switch page: %d %s", page.Status, page)
		}
		if f := d.Call(t, "GET", "/c/"+psTile+"/", nil, e.as("alice")...); f.Status != 200 || !strings.Contains(string(f.Body), "the probe's page") {
			t.Errorf("a token's read of the paused tile's page: %d %s", f.Status, cut(f.String(), 200))
		}
		if a := d.Call(t, "GET", "/api/xbin/alerts", nil, e.as("alice")...); !strings.Contains(string(a.Body), "partition-switch") {
			t.Errorf("alice's alerts while pending: %d %s", a.Status, a)
		}
		keep := map[string]any{"tile": psTile, "act": "keep", "from": psBoth, "to": nil}
		if r := e.mode(t, "bob", keep); r.Status != 403 {
			t.Errorf("bob (a reader) keeps: %d %s", r.Status, r)
		}
		if r := e.d.Call(t, "POST", "/api/xbin/partitions/mode", keep, e.fr(t, psTile, "carol")); r.Status != 403 {
			t.Errorf("carol's frame of the tile keeps: %d %s", r.Status, r)
		}
		if r := e.mode(t, "carol", map[string]any{"tile": psTile, "act": "keep", "from": psUser, "to": nil}); r.Status != 409 {
			t.Errorf("a keep of another request: %d %s", r.Status, r)
		}
		if r := e.mode(t, "carol", keep); r.Status != 200 {
			t.Fatalf("carol (admin) keeps: %d %s", r.Status, r)
		}
		row = e.waitState(t, psTile, "partitioned")
		if row.Request == nil || !row.Request.Declined {
			t.Errorf("the kept row: %+v %+v", *row, row.Request)
		}
		w := e.who(t, "/api/"+psTile+"/who", e.fr(t, psTile, "alice"))
		if w.Env != "user:alice" {
			t.Errorf("alice after keep reaches %+v", w)
		}
		tokens["alice"] = w.Token
		if v, st, body := e.value(t, "/api/"+psTile+"/fs/files/note", e.fr(t, psTile, "alice")); v != "alice-note" {
			t.Errorf("alice's file after keep: %d %s", st, body)
		}
		if v, st, body := e.value(t, "/api/"+psTile+"/kv/kv/secret"); v != "global-secret" {
			t.Errorf("global's kv after keep: %d %s", st, body)
		}
	})

	t.Run("mode-switch", func(t *testing.T) {
		// 01 §2.5-§2.6: a switch wipes every namespace — people's and
		// global's — revokes people's tokens and records the new mode
		sw := func(extra map[string]any) map[string]any {
			m := map[string]any{"tile": psTile, "act": "switch", "from": psBoth, "to": nil}
			for k, v := range extra {
				m[k] = v
			}
			return m
		}
		var preview struct{ Wiped map[string]int64 }
		r := e.mode(t, "carol", sw(map[string]any{"dryRun": true}))
		if r.Status != 200 {
			t.Fatalf("the dry run: %d %s", r.Status, r)
		}
		r.Decode(t, &preview)
		t.Logf("the dry run: %s", cut(r.String(), 600))
		if preview.Wiped["partitions"] < 3 || preview.Wiped["namespaces"] < 3 {
			t.Errorf("the dry run's counts: %v", preview.Wiped)
		}
		for _, p := range []string{"alice", "bob"} {
			if _, err := os.Stat(e.partDir(psTile, ids[p])); err != nil {
				t.Errorf("the dry run removed %s's namespace: %v", p, err)
			}
		}
		if r := e.mode(t, "carol", sw(map[string]any{"confirm": "apps/other"})); r.Status != 400 {
			t.Errorf("a switch with the wrong path typed: %d %s", r.Status, r)
		}
		if r := e.mode(t, "bob", sw(map[string]any{"confirm": psTile})); r.Status != 403 {
			t.Errorf("bob (a reader) switches: %d %s", r.Status, r)
		}
		old := xbindtest.H("Authorization", "Bearer "+tokens["alice"])
		volumes := func() []string { // the tile's volumes' gocryptfs: main's and people's
			return append(e.gocryptfs("data/resources-enc/apps~pt/"), e.gocryptfs("data/resources-enc/.partitions/apps~pt/")...)
		}
		before := volumes()
		t.Logf("the tile's volumes before the switch: %v", before)
		r = e.mode(t, "carol", sw(map[string]any{"confirm": psTile}))
		if r.Status != 200 {
			t.Fatalf("the switch: %d %s", r.Status, r)
		}
		var left []string
		for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(250 * time.Millisecond) {
			left = nil
			for _, v := range volumes() {
				if slices.Contains(before, v) {
					left = append(left, v)
				}
			}
			if len(left) == 0 || time.Now().After(deadline) {
				break
			}
		}
		if len(left) > 0 {
			t.Errorf("the wiped volumes' gocryptfs still run after the switch: %v", left)
		}
		t.Logf("the switch: %s", cut(r.String(), 600))
		var done struct{ Wiped map[string]int64 }
		r.Decode(t, &done)
		if done.Wiped["partitions"] < 3 {
			t.Errorf("the switch's counts: %v", done.Wiped)
		}
		e.waitState(t, psTile, "")
		m, ok := e.modeRecord(t, psTile)
		if !ok || m.Mode != nil && (m.Mode.User || m.Mode.Global) || m.Request != nil || len(m.History) == 0 || m.History[len(m.History)-1].Op != "switch" {
			t.Errorf("mode.json after the switch: %v %+v", ok, m)
		}
		if r := d.Call(t, "GET", "/api/xbin/kv/res:"+psTile+"/kv/secret", nil, old); r.Status != 401 {
			t.Errorf("alice's old instance token after the switch: %d %s (want 401)", r.Status, r)
		}
		for _, p := range []string{"alice", "bob"} {
			if _, err := os.Stat(e.partDir(psTile, ids[p])); !os.IsNotExist(err) {
				t.Errorf("%s's partition namespace after the switch: %v", p, err)
			}
		}
		if ents, _ := os.ReadDir(filepath.Join(d.WS, "data", "resources-enc", ".partitions", strings.ReplaceAll(psTile, "/", "~"))); len(ents) > 0 {
			t.Errorf("people's partition data of %s left on disk: %v", psTile, ents)
		}
		e.forget("alice")
		w := e.who(t, "/api/"+psTile+"/who", e.fr(t, psTile, "alice"))
		if w.Env != "" || w.Caller.Partition != "" || w.Caller.PartitionID != "" {
			t.Errorf("alice after the switch reaches %+v, want the one unpartitioned instance", w)
		}
		for _, p := range []string{"/kv/kv/secret", "/fs/files/note", "/kv/board/shared"} {
			for _, hdrs := range [][]xbindtest.Header{{e.fr(t, psTile, "alice")}, nil} {
				if v, st, body := e.value(t, "/api/"+psTile+p, hdrs...); st != 404 {
					t.Errorf("%s after the switch: %d %q %s", p, st, v, body)
				}
			}
		}
		if r := d.Call(t, "GET", "/api/"+psTile+"/ls?res=files", nil); !strings.Contains(string(r.Body), `"names":[]`) {
			t.Errorf("the files after the switch: %d %s", r.Status, r)
		}
		// every deployment's namespace goes too: dev's seeded copy of global's
		r = d.Call(t, "GET", "/api/"+psTile+"+dev/kv/kv/secret", nil)
		if strings.Contains(string(r.Body), "global-before-dev") {
			t.Errorf("dev's data survived the switch: %d %s", r.Status, r)
		}
		t.Logf("dev's kv after the switch: %d %s", r.Status, cut(r.String(), 200))
	})

	t.Run("mode-pending-unpartitioned", func(t *testing.T) {
		// 01 §2.4 the other way: an unpartitioned tile that holds data asks
		// for partitions; keep leaves it one instance on its data
		before := e.who(t, "/api/"+psPlain+"/who", e.fr(t, psPlain, "alice"))
		d.WriteTile(t, psPlain, map[string]string{"xbin.json": psManifest(psPlain, `["user", "global"]`)})
		e.waitState(t, psPlain, "pending")
		if r := d.Call(t, "GET", "/api/"+psPlain+"/who", nil, e.fr(t, psPlain, "alice")); r.Status != 409 {
			t.Errorf("alice's call while pending: %d %s", r.Status, r)
		}
		if r := e.mode(t, "carol", map[string]any{"tile": psPlain, "act": "keep", "from": nil, "to": psBoth}); r.Status != 200 {
			t.Fatalf("carol keeps: %d %s", r.Status, r)
		}
		w := e.who(t, "/api/"+psPlain+"/who", e.fr(t, psPlain, "alice"))
		if w.Env != "" || w.Caller.Partition != "" {
			t.Errorf("alice after keep reaches %+v, want the unpartitioned instance (before: boot %s)", w, before.Boot)
		}
		if v, st, body := e.value(t, "/api/"+psPlain+"/kv/kv/secret", e.fr(t, psPlain, "alice")); v != "plain-data" {
			t.Errorf("the kept tile's data: %d %s", st, body)
		}
	})
}

// TestPartitionsSmokeReap: a person's partition stops after 10 idle minutes
// (PD-18, 03 §A.6) — its instance token revoked — and starts again, on its
// data, at their next use. The idle time has no knob, so this waits for it:
// opt in with XBIN_SMOKE_REAP=1.
func TestPartitionsSmokeReap(t *testing.T) {
	if os.Getenv("XBIN_SMOKE_REAP") != "1" {
		t.Skip("the idle stop takes over ten minutes: XBIN_SMOKE_REAP=1 runs it")
	}
	t.Parallel()
	e := psSetup(t)
	d := e.d
	psWrite(t, d, psTile, `["user"]`)
	e.waitState(t, psTile, "partitioned")
	w := e.who(t, "/api/"+psTile+"/who", e.fr(t, psTile, "alice"))
	if w.Env != "user:alice" {
		t.Fatalf("alice reaches %+v", w)
	}
	e.put(t, "/api/"+psTile+"/kv/kv/secret", "alice-secret", e.fr(t, psTile, "alice"))
	e.put(t, "/api/"+psTile+"/fs/files/note", "alice-note", e.fr(t, psTile, "alice"))
	tok := xbindtest.H("Authorization", "Bearer "+w.Token)
	kv := "/api/xbin/kv/res:" + psTile + "/kv/secret"
	if r := d.Call(t, "GET", kv, nil, tok); r.Status != 200 {
		t.Fatalf("alice's instance token: %d %s", r.Status, r)
	}
	start := time.Now()
	for {
		r := d.Call(t, "GET", kv, nil, tok) // the kv API: no use of the backend
		if r.Status == 401 {
			break
		}
		if time.Since(start) > 14*time.Minute {
			t.Fatalf("alice's partition wasn't reaped within %s: her token answers %d %s", time.Since(start), r.Status, r)
		}
		time.Sleep(15 * time.Second)
	}
	took := time.Since(start)
	t.Logf("alice's partition was reaped (its token revoked) after %s idle", took.Round(time.Second))
	if took < 9*time.Minute {
		t.Errorf("reaped after %s, before the 10 idle minutes", took)
	}
	again := e.who(t, "/api/"+psTile+"/who", e.fr(t, psTile, "alice"))
	if again.Env != "user:alice" || again.Boot == w.Boot {
		t.Errorf("alice's next use: %+v (boot before %s)", again, w.Boot)
	}
	if v, st, body := e.value(t, "/api/"+psTile+"/fs/files/note", e.fr(t, psTile, "alice")); v != "alice-note" {
		t.Errorf("alice's file after the reap: %d %s", st, body)
	}
}
