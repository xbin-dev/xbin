package xbin_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// standIn is just enough of xbind to run the realtime example
// (example_realtime_test.go). This one process plays every instance of
// apps/rooms in turn: the instance calling is the one XBIN_PARTITION names
// at that moment, as xbind knows it by its token. It keeps the kv stores —
// one copy of a shared resource, one per partition of any other — records
// bus events and pushes, keeps each addressee's inbox, answers access, and
// passes a user partition's call carrying xbin-partition=global to the
// global instance as that partition's person, as xbind does. A person in
// gone has lost the tile (removed from it, disabled or deleted): access says
// none, and xbind refuses their calls and mail to them.
type standIn struct {
	global   http.Handler // the global instance's routes
	globalAt string       // … served here, for streams
	mu       sync.Mutex
	kv       map[string][]byte // resource \x00 partition ("" shared) \x00 key
	bus      []string          // "<publisher> <resource> <topic> <data>"
	pushes   []string          // "<user>|<title>|<body>|<link>"
	inbox    map[string][]xbin.MailItem
	seq      int
	forwards []string        // what reached global through a partition
	gone     map[string]bool // people who lost the tile
}

var sharedRes = map[string]bool{"res:apps/rooms/board": true, "res:apps/rooms/live": true}

// people are the workspace's people and their level on apps/rooms.
var people = map[string]string{"alice": "write", "bob": "read", "carol": "read"}

// as makes this process the instance part ("global" or "user:<id>").
func as(part string) { os.Setenv("XBIN_PARTITION", part) }

func startStandIn(t *testing.T) *standIn {
	t.Helper()
	for k, v := range map[string]string{
		"XBIN_COMPONENT": "apps/rooms", "XBIN_TOKEN": "tok", "XBIN_PARTITION": "global",
		"XBIN_RES_BOARD": "res:apps/rooms/board", "XBIN_RES_LIVE": "res:apps/rooms/live",
		"XBIN_RES_MINE": "res:apps/rooms/mine",
	} {
		t.Setenv(k, v)
	}
	x := &standIn{kv: map[string][]byte{}, inbox: map[string][]xbin.MailItem{}, gone: map[string]bool{}}
	sock := filepath.Join(t.TempDir(), "gw.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	gw := &http.Server{Handler: x}
	go gw.Serve(ln)
	t.Setenv("XBIN_GATEWAY", sock)
	xbin.ResetClient()
	x.global = roomsRoutes() // built as global: it has the hub
	srv := httptest.NewServer(x.global)
	x.globalAt = srv.URL
	t.Cleanup(func() { srv.Close(); gw.Close(); xbin.ResetClient() })
	return x
}

// stamp gives req the identity xbind gives a call that reaches the global
// instance for person user through their own partition (a page's
// {partition: 'global'}, a partition's GlobalURL).
func stamp(req *http.Request, user string) {
	role := map[string]string{"read": "reader", "write": "writer"}[people[user]]
	for k, v := range map[string]string{
		"X-XBin-From": "apps/rooms", "X-XBin-Role": role, "X-XBin-User": user,
		"X-XBin-User-Level": people[user], "X-XBin-Partition": "user:" + user,
		"X-XBin-Partition-Id": "u-" + fmt.Sprintf("%032x", len(user)),
	} {
		req.Header.Set(k, v)
	}
}

func (x *standIn) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	caller := os.Getenv("XBIN_PARTITION")
	p := r.URL.EscapedPath()
	body, _ := io.ReadAll(r.Body)
	x.mu.Lock()
	defer x.mu.Unlock()
	switch {
	case strings.HasPrefix(p, "/api/xbin/kv/"):
		rest := strings.TrimPrefix(p, "/api/xbin/kv/")
		i := strings.LastIndex(rest, "/")
		res, key := rest[:i], rest[i+1:]
		key, _ = url.PathUnescape(key)
		ns := caller
		if sharedRes[res] {
			ns = ""
		}
		base := res + "\x00" + ns + "\x00"
		switch {
		case r.Method == "PUT":
			x.kv[base+key] = body
			w.WriteHeader(200)
		case key == "": // a list
			keys := []string{}
			for k := range x.kv {
				if strings.HasPrefix(k, base+r.URL.Query().Get("prefix")) {
					keys = append(keys, strings.TrimPrefix(k, base))
				}
			}
			sort.Strings(keys)
			xbin.WriteJSON(w, 200, map[string]any{"keys": keys})
		default:
			v, ok := x.kv[base+key]
			if !ok {
				http.Error(w, "not found", 404)
				return
			}
			_, _ = w.Write(v)
		}
	case p == "/api/xbin/bus/publish":
		var in struct {
			Resource, Topic string
			Data            json.RawMessage
		}
		_ = json.Unmarshal(body, &in)
		x.bus = append(x.bus, fmt.Sprintf("%s %s %s %s", caller, in.Resource, in.Topic, in.Data))
		w.WriteHeader(200)
	case p == "/api/xbin/notify":
		var in struct{ User, Title, Body, Link string }
		_ = json.Unmarshal(body, &in)
		if caller != "global" && caller != "user:"+in.User { // a person's partition notifies its person only
			http.Error(w, "not yours to notify", 403)
			return
		}
		x.pushes = append(x.pushes, strings.Join([]string{in.User, in.Title, in.Body, in.Link}, "|"))
		w.WriteHeader(200)
	case strings.HasPrefix(p, "/api/xbin/access/"):
		u := strings.TrimPrefix(p, "/api/xbin/access/")
		lvl, ok := people[u]
		if !ok || x.gone[u] {
			lvl, ok = "none", false
		}
		xbin.WriteJSON(w, 200, map[string]any{"user": u, "level": lvl, "active": ok})
	case p == "/api/xbin/partitions/mail" && r.Method == "POST":
		var in struct {
			To, Topic string
			Data      json.RawMessage
		}
		_ = json.Unmarshal(body, &in)
		if caller != "global" && in.To != "global" {
			http.Error(w, "a person's partition mails global only", 403)
			return
		}
		if u, ok := strings.CutPrefix(in.To, "user:"); ok && (people[u] == "" || x.gone[u]) || !ok && in.To != "global" {
			http.Error(w, `{"error":"no such person here: `+in.To+`"}`, 404)
			return
		}
		x.seq++
		it := xbin.MailItem{ID: fmt.Sprintf("%024x", x.seq), From: caller, Topic: in.Topic, Data: in.Data, At: time.Now()}
		x.inbox[in.To] = append(x.inbox[in.To], it)
		xbin.WriteJSON(w, 200, map[string]any{"ok": true, "id": it.ID})
	case p == "/api/xbin/partitions/mail": // GET: the caller's own inbox
		items := []xbin.MailItem{}
		for _, it := range x.inbox[caller] {
			if it.ID > r.URL.Query().Get("after") {
				items = append(items, it)
			}
		}
		xbin.WriteJSON(w, 200, map[string]any{"items": items, "more": false})
	case p == "/api/xbin/partitions/mail/ack":
		var in struct{ IDs []string }
		_ = json.Unmarshal(body, &in)
		kept := x.inbox[caller][:0]
		for _, it := range x.inbox[caller] {
			if !contains(in.IDs, it.ID) {
				kept = append(kept, it)
			}
		}
		x.inbox[caller] = kept
		xbin.WriteJSON(w, 200, map[string]any{"ok": true})
	case strings.HasPrefix(p, "/api/apps/rooms/") && r.URL.Query().Get("xbin-partition") == "global":
		user, ok := strings.CutPrefix(caller, "user:")
		if !ok {
			http.Error(w, "only a user partition addresses global", 400)
			return
		}
		if x.gone[user] {
			http.Error(w, "no access", 403)
			return
		}
		q := r.URL.Query()
		q.Del("xbin-partition")
		target := strings.TrimPrefix(p, "/api/apps/rooms")
		x.forwards = append(x.forwards, r.Method+" "+r.URL.RequestURI())
		fw := httptest.NewRequest(r.Method, target+"?"+q.Encode(), strings.NewReader(string(body)))
		stamp(fw, user)
		rec := httptest.NewRecorder()
		x.mu.Unlock()
		as("global") // the global instance runs it
		x.global.ServeHTTP(rec, fw)
		as(caller)
		x.mu.Lock()
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	default:
		http.Error(w, "the stand-in doesn't serve "+r.Method+" "+p, 501)
	}
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// call runs one request against h (an instance's routes) as the person's
// own page would reach it, and answers its status and body.
func call(h http.Handler, method, path, user, body string) (int, string) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if user != "" {
		stamp(req, user)
	}
	rec := serve(h, req)
	return rec.Code, strings.TrimSpace(rec.Body.String())
}

// serve runs req against h within 2 s: a follow let in where it should be
// refused streams until then and answers 200 — failing the test, not
// hanging it.
func serve(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
	defer cancel()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

// followRoom has user's page follow room at the global instance (served at
// x.globalAt, as the stream is): each line it gets arrives on the channel,
// which closes when the stream ends.
func followRoom(t *testing.T, x *standIn, room, user string) <-chan string {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	req, _ := http.NewRequestWithContext(ctx, "GET", x.globalAt+"/rooms/"+room+"/follow", nil)
	stamp(req, user)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("%s follows room %s: %v %v", user, room, resp, err)
	}
	lines := make(chan string, 16)
	go func() {
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	return lines
}

// next is the next line a follower gets, or "(ended)" when its stream ends
// — failing when neither comes within 5 s.
func next(t *testing.T, lines <-chan string, who string) string {
	t.Helper()
	select {
	case l, ok := <-lines:
		if !ok {
			return "(ended)"
		}
		return l
	case <-time.After(5 * time.Second):
		t.Fatalf("%s's page got nothing", who)
		return ""
	}
}

// The realtime example's three patterns, run: a status on the shared board
// reaches every reader; a room at the global instance streams a member's
// partition's post to another member's page and refuses everyone else; a
// mention mailed to a person's partition is kept there once and pushed.
func TestRealtimeExample(t *testing.T) {
	fresh := accessFresh
	accessFresh = 0 // every post asks xbind about each follower
	t.Cleanup(func() { accessFresh = fresh })
	x := startStandIn(t)
	ctx := context.Background()

	// ---- 1. the board: a shared kv and a shared bus ----
	as("user:alice")
	alice := roomsRoutes()
	if code, body := call(alice, "POST", "/status", "alice", `{"text":"in a meeting"}`); code != 204 {
		t.Fatalf("alice's status: %d %s", code, body)
	}
	if got := strings.Join(x.bus, "\n"); got != `user:alice res:apps/rooms/live status/alice {"text":"in a meeting"}` {
		t.Errorf("bus: %s", got)
	}
	as("user:bob")
	bob := roomsRoutes()
	if code, body := call(bob, "GET", "/board", "bob", ""); code != 200 || body != `{"alice":{"text":"in a meeting"}}` {
		t.Errorf("bob's board: %d %s", code, body)
	}
	if _, body := call(bob, "GET", "/rooms/7/follow", "bob", ""); !strings.Contains(body, "404") {
		t.Errorf("a person's partition serves the hub's routes: %s", body)
	}
	as("global")
	if code, _ := call(x.global, "POST", "/status", "", `{"text":"x"}`); code != 403 {
		t.Errorf("the global instance set a status: %d", code)
	}

	// ---- 2. rooms: the global instance as hub ----
	if code, body := call(x.global, "POST", "/rooms/7/members", "alice", `{"user":"bob"}`); code != 204 {
		t.Fatalf("alice makes room 7 with bob: %d %s", code, body)
	}
	if code, _ := call(x.global, "POST", "/rooms/7/members", "alice", `{"user":"zed"}`); code != 404 {
		t.Errorf("inviting someone who can't read the tile: %d", code)
	}
	if code, _ := call(x.global, "POST", "/rooms/7/members", "carol", `{"user":"carol"}`); code != 403 {
		t.Errorf("carol let herself in: %d", code)
	}
	// bob is a member: each case below is his call with ONE thing changed, so
	// each clause of person() is what refuses it (carol's is the membership)
	for name, mod := range map[string]func(*http.Request){
		"carol, not a member": func(r *http.Request) { stamp(r, "carol") },
		"another tile, for bob": func(r *http.Request) {
			stamp(r, "bob")
			r.Header.Set("X-XBin-From", "apps/other")
		},
		"bob, in the global instance": func(r *http.Request) { stamp(r, "bob"); r.Header.Set("X-XBin-Partition", "global") },
		"bob, in carol's partition":   func(r *http.Request) { stamp(r, "bob"); r.Header.Set("X-XBin-Partition", "user:carol") },
		"bob, with no partition":      func(r *http.Request) { stamp(r, "bob"); r.Header.Del("X-XBin-Partition") },
		"view-as bob":                 func(r *http.Request) { stamp(r, "bob"); r.Header.Set("X-XBin-Viewed-By", "admin") },
		"the root token": func(r *http.Request) {
			r.Header.Set("X-XBin-From", "owner")
			r.Header.Set("X-XBin-Partition", "global")
		},
	} {
		req := httptest.NewRequest("GET", "/rooms/7/follow", nil)
		mod(req)
		if rec := serve(x.global, req); rec.Code != 403 {
			t.Errorf("%s follows room 7: %d", name, rec.Code)
		}
	}

	// bob's page follows room 7 at global (the person() gate passes him)
	req := httptest.NewRequest("GET", "/rooms/7/follow", nil)
	stamp(req, "bob")
	if who := person(req); who != "bob" {
		t.Fatalf("bob's own call acts for %q", who)
	}
	bobs := followRoom(t, x, "7", "bob")

	// alice's partition posts, through global, as alice
	as("user:alice")
	if err := say(ctx, "7", "the deploy is green, @bob", "bob", "carol"); err != nil {
		t.Fatalf("alice's partition posts: %v", err)
	}
	if got := strings.Join(x.forwards, "\n"); got != "POST /api/apps/rooms/rooms/7/posts?xbin-partition=global" {
		t.Errorf("reached global as: %s", got)
	}
	if l := next(t, bobs, "bob"); l != `{"room":"7","from":"alice","text":"the deploy is green, @bob"}` {
		t.Errorf("bob's page got %s", l)
	}
	as("user:carol")
	if err := say(ctx, "7", "let me in"); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("carol's partition posts to a room she isn't in: %v", err)
	}

	// ---- 3. the mention: mail wakes bob's partition ----
	if n := len(x.inbox["user:bob"]); n != 1 || x.inbox["user:bob"][0].From != "global" || len(x.inbox["user:carol"]) != 0 {
		t.Fatalf("inboxes after the post: bob %+v, carol %+v", x.inbox["user:bob"], x.inbox["user:carol"])
	}
	item := x.inbox["user:bob"][0]
	as("user:bob")
	ring := func() {
		t.Helper()
		req := httptest.NewRequest("POST", "/mailbox", strings.NewReader(`{"partition":"user:bob","pending":1}`))
		req.Header.Set("X-XBin-From", "xbin/mail")
		rec := httptest.NewRecorder()
		bob.ServeHTTP(rec, req)
		if rec.Code != 204 {
			t.Fatalf("the doorbell: %d %s", rec.Code, rec.Body)
		}
	}
	ring()
	x.inbox["user:bob"] = append(x.inbox["user:bob"], item) // at least once: it comes again
	ring()
	if len(x.inbox["user:bob"]) != 0 {
		t.Errorf("bob's inbox after the rings: %+v", x.inbox["user:bob"])
	}
	if got := strings.Join(x.pushes, "\n"); got != "bob|alice in 7|the deploy is green, @bob|#room=7" {
		t.Errorf("pushes: %s", got)
	}
	if code, body := call(bob, "GET", "/mentions", "bob", ""); code != 200 || body != `[{"room":"7","from":"alice","text":"the deploy is green, @bob"}]` {
		t.Errorf("bob's mentions: %d %s", code, body)
	}
	as("user:alice")
	if code, body := call(alice, "GET", "/mentions", "alice", ""); code != 200 || body != `[]` {
		t.Errorf("alice's mentions: %d %s", code, body)
	}

	// ---- 2, again: who stops getting a room's posts ----
	// bob brings carol in, and her page follows too
	as("global")
	if code, body := call(x.global, "POST", "/rooms/7/members", "bob", `{"user":"carol"}`); code != 204 {
		t.Fatalf("bob adds carol: %d %s", code, body)
	}
	carols := followRoom(t, x, "7", "carol")
	// bob loses the tile (removed from it, disabled, deleted) while his page
	// follows: the next post drops him — his stream ends without it — and
	// still reaches carol
	x.mu.Lock()
	x.gone["bob"] = true
	x.mu.Unlock()
	as("user:alice")
	if err := say(ctx, "7", "bob is off the tile"); err != nil {
		t.Fatalf("alice's partition posts: %v", err)
	}
	if l := next(t, carols, "carol"); l != `{"room":"7","from":"alice","text":"bob is off the tile"}` {
		t.Errorf("carol's page got %s", l)
	}
	if l := next(t, bobs, "bob"); l != "(ended)" {
		t.Errorf("bob's page, after he lost the tile, got %s", l)
	}
	as("global")
	if code, _ := call(x.global, "GET", "/rooms/7/follow", "bob", ""); code != 403 {
		t.Errorf("bob, dropped, follows room 7 again: %d", code)
	}
	// carol leaves: her stream ends, and a member's post no longer reaches her
	if code, _ := call(x.global, "DELETE", "/rooms/7/members/carol", "bob", ""); code != 403 {
		t.Errorf("bob, dropped, takes carol out: %d", code)
	}
	if code, body := call(x.global, "DELETE", "/rooms/7/members/carol", "carol", ""); code != 204 {
		t.Fatalf("carol leaves room 7: %d %s", code, body)
	}
	if l := next(t, carols, "carol"); l != "(ended)" {
		t.Errorf("carol's page, after she left, got %s", l)
	}
	if code, _ := call(x.global, "POST", "/rooms/7/posts", "carol", `{"text":"still here?"}`); code != 403 {
		t.Errorf("carol posts to a room she left: %d", code)
	}
	if code, _ := call(x.global, "POST", "/rooms/7/posts", "alice", `{"text":"just me"}`); code != 204 {
		t.Errorf("alice, the last member, posts: %d", code)
	}
}
