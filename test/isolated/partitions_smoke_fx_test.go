//go:build linux && integration

package isolated

// partitions_smoke_fx_test.go — the partitions smoke's fixture
// (partitions_smoke_test.go): the probe backend every fixture tile runs, its
// manifest, the people and the helpers the cases share.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/util"
	"github.com/xbin-dev/xbin/test/xbindtest"
)

const (
	psTile   = "apps/pt"    // the partitioned fixture: ["user", "global"]
	psPeer   = "apps/pt2"   // another partitioned tile, ["user"], granted writer on apps/pt
	psCaller = "apps/pcall" // an unpartitioned tile granted writer on apps/pt
	psPlain  = "apps/plain" // an unpartitioned tile that holds data, later asked to partition
	psHost   = "pt.test"    // the fixture's published host: ingress reaches global
	psNote   = "S1 smoke fixture: <b>notes</b> live in each person's partition"
)

// psSource is the probe backend every fixture tile runs. boot names the
// process (a sandbox's pid is 1 in every instance). It logs every value it
// stores, so the log routes can be checked for them.
const psSource = `package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
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
		log.Printf("stored kv %s/%s = %s", r.PathValue("res"), r.PathValue("key"), b)
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
		b, _ := io.ReadAll(r.Body)
		if err == nil {
			err = os.WriteFile(p, b, 0o644)
		}
		if err != nil {
			fail(w, err)
			return
		}
		log.Printf("stored file %s/%s = %s", r.PathValue("res"), r.PathValue("name"), b)
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
	// mountinfo is the sandbox's mount table: what it can see of the host
	mux.HandleFunc("GET /mountinfo", func(w http.ResponseWriter, r *http.Request) {
		b, err := os.ReadFile("/proc/self/mountinfo")
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, map[string]string{"value": string(b)})
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
// The unpartitioned caller and the partitioned peer use apps/pt as writer.
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
	if tile == psCaller || tile == psPeer {
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

// psPeople are the smoke's people: users reading apps/*, and admins.
var psPeople = map[string]string{"alice": "user", "bob": "user", "dave": "user", "carol": "admin", "erin": "admin"}

// psPassword is person's password.
func psPassword(person string) string { return "pw-" + person + "-5c2e81" }

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
		e.addPerson(t, id, role)
	}
	return e
}

// addPerson makes person with role and signs them in.
func (e *psEnv) addPerson(t *testing.T, id, role string) {
	t.Helper()
	tiles := map[string]string{"apps/*": "read"}
	if role == "admin" {
		tiles = nil
	}
	e.d.AddUser(t, id, psPassword(id), role, tiles)
	e.sess[id] = e.d.Login(t, id, psPassword(id))
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

// relayWho is the /who the probe's /call relay reached, and its status.
func relayWho(t *testing.T, r xbindtest.Resp) (psWho, int) {
	t.Helper()
	var w psWho
	st, body := psOutcome(r)
	if st == 200 {
		if err := json.Unmarshal([]byte(body), &w); err != nil {
			t.Errorf("the relayed /who: %v %s", err, cut(body, 200))
		}
	}
	return w, st
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

// names is a probe listing's names (/keys: the keys, /ls: the entries),
// sorted; it fails unless the listing answers 200.
func (e *psEnv) names(t *testing.T, path string, hdrs ...xbindtest.Header) []string {
	t.Helper()
	r := e.d.Call(t, "GET", path, nil, hdrs...)
	var out struct{ Keys, Names []string }
	if r.Status != 200 {
		t.Errorf("GET %s: %d %s", path, r.Status, r)
		return nil
	}
	r.Decode(t, &out)
	names := append(out.Keys, out.Names...)
	slices.Sort(names)
	return names
}

// psWant is one answer an attempt may get: its status and — at 200 — the
// stored value exactly, or with list a name the listing holds; at any other
// status, a fragment of the refusal ("" = any text).
type psWant struct {
	status int
	value  string
	list   bool
}

func (w psWant) String() string {
	if w.list {
		return fmt.Sprintf("%d listing %q", w.status, w.value)
	}
	return fmt.Sprintf("%d %q", w.status, w.value)
}

func psOK(v string) psWant   { return psWant{status: 200, value: v} }
func psList(n string) psWant { return psWant{status: 200, value: n, list: true} }

// psBadPartitionParam is F9's answer (05 §6) to ?xbin-partition naming
// anything but global on a partitioned tile: 400, never the named
// partition's data.
var psBadPartitionParam = psWant{400, "its one value is global", false}

// psOutcome is what r answers: a probe route's status and value — through
// the probe's /call relay, the relayed call's — else its status and body.
func psOutcome(r xbindtest.Resp) (int, string) {
	st, body := r.Status, string(r.Body)
	var relay struct {
		Status int
		Body   *string
	}
	if st == 200 && json.Unmarshal(r.Body, &relay) == nil && relay.Status != 0 && relay.Body != nil {
		st, body = relay.Status, *relay.Body
	}
	var v struct{ Value *string }
	if st == 200 && json.Unmarshal([]byte(body), &v) == nil && v.Value != nil {
		return st, *v.Value
	}
	return st, strings.TrimSpace(body)
}

// psExpect checks that r is one of wants, and that its body holds none of
// forbid (other people's data, or global's for a person's route).
func psExpect(t *testing.T, name string, r xbindtest.Resp, forbid []string, wants ...psWant) {
	t.Helper()
	for _, f := range forbid {
		if strings.Contains(string(r.Body), f) {
			t.Errorf("BUG: %s: %d %s (holds %q)", name, r.Status, cut(r.String(), 300), f)
			return
		}
	}
	st, v := psOutcome(r)
	for _, w := range wants {
		switch {
		case st != w.status:
		case w.status != 200 && strings.Contains(v, w.value),
			w.status == 200 && w.list && strings.Contains(v, strconv.Quote(w.value)),
			w.status == 200 && !w.list && v == w.value:
			return
		}
	}
	t.Errorf("%s: %d %s, want %v", name, st, cut(v, 300), wants)
}

// psForbid is what person's answers must never hold: every other person's
// data, and global's too when the route is the person's own.
func psForbid(person string, own bool) []string {
	var out []string
	for p := range psPeople {
		if p != person {
			out = append(out, p+"-")
		}
	}
	if own {
		out = append(out, "global-")
	}
	slices.Sort(out)
	return out
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

// viewAs is admin's view-as session of person (D64): the cookie the
// redeemed link sets.
func (e *psEnv) viewAs(t *testing.T, admin, person string) xbindtest.Header {
	t.Helper()
	var tk struct{ URL string }
	e.d.Must(t, "POST", "/api/xbin/impersonate", map[string]string{"user": person}, 200, e.as(admin)...).Decode(t, &tk)
	st, _, hdr := psRaw(t, "GET", e.d.URL+tk.URL, xbindtest.H("Authorization", "Bearer "+e.sess[admin]))
	var cookie string
	for _, c := range (&http.Response{Header: hdr}).Cookies() {
		if c.Value != "" {
			cookie = c.Name + "=" + c.Value
		}
	}
	if st != http.StatusFound || cookie == "" {
		t.Fatalf("redeeming %s's view-as link of %s: %d, cookie %q", admin, person, st, cookie)
	}
	view := xbindtest.H("Cookie", cookie)
	if st, body, _ := psRaw(t, "GET", e.d.URL+"/api/xbin/whoami", view); st != 200 || !strings.Contains(body, "impersonatedBy") {
		t.Fatalf("the view-as session of %s: %d %s", person, st, body)
	}
	return view
}

// gocryptfs lists the gocryptfs processes serving the workspace's volumes
// under dir (relative to the workspace: data/resources-enc/<…>; apps~pt's
// never match apps~pt2's), as "<pid> <ciphertext dir>".
func (e *psEnv) gocryptfs(dir string) []string {
	var out []string
	under := filepath.Join(e.d.WS, dir) + "/"
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
			if strings.HasPrefix(a, under) {
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
