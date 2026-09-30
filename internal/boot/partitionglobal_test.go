package boot

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/broker"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers PD-16 S3 — F5 end to end on a booted xbind (plans/partitions/05
// §6): the URLs the client's xbin.fetch(url, {partition: 'global'}) and the
// Go SDK's xbin.GlobalURL build reach a partitioned tile's global instance
// — a real backend, which never sees the parameter — as the partition's
// person (X-XBin-User, their level, a clamped role, X-XBin-Partition
// user:<id> and its id), from the tile's frame and from a user partition's
// instance token alike; without the parameter the same frame reaches its
// own partition (503 here: no --isolate). The owner token reaches global as
// before; an unpartitioned tile gets the parameter as any other; a tile
// without a global instance is 404; another tile, a path ticket and a bad
// value are refused.
func TestGlobalAddressWiring(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	ws := zsWorkspace(t)
	for rel, body := range map[string]string{
		"apps/pa/xbin.json":         `{"runtime":"node","partition":["user","global"]}`,
		"apps/pa/backend/server.js": zsNodeServer,
		"apps/pa/index.html":        "<!doctype html><html><head></head><body>pa</body></html>\n",
		"apps/pn/xbin.json":         `{"runtime":"node","partition":["user"]}`,
		"apps/pn/backend/server.js": zsNodeServer,
	} {
		p := filepath.Join(ws, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	d := zsBoot(t, ws)
	for _, u := range []users.User{
		{ID: "alice", Role: users.RoleUser, Tiles: map[string]string{"apps/pa": users.LevelRead, "apps/pn": users.LevelRead}},
		{ID: "wendy", Role: users.RoleUser, Tiles: map[string]string{"apps/pa": users.LevelWrite}},
		{ID: "bob", Role: users.RoleAdmin},
	} {
		if _, err := d.st.Users.Upsert(u, "password1"); err != nil {
			t.Fatal(err)
		}
	}
	d.st.Auth.NewSession("alice", "127.0.0.1") // path tickets are used only from where someone signed in
	type answer struct {
		URL  string
		Xbin map[string]string
	}
	do := func(method, path string, hdr map[string]string, body string) (int, answer, string) {
		t.Helper()
		req, err := http.NewRequest(method, d.url+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		var a answer
		_ = json.Unmarshal(b, &a)
		return resp.StatusCode, a, string(b)
	}
	frame := func(tile, user string) map[string]string {
		return map[string]string{auth.FrameTokenHeader: d.st.Auth.MintFrameToken(tile, user, time.Hour)}
	}
	pkey := func(id string) string {
		u, ok := d.st.Users.Get(id)
		if !ok || u.UID == "" {
			t.Fatalf("%s has no uid", id)
		}
		return util.PartitionKey(id, u.UID)
	}
	attributed := func(label string, code int, a answer, raw, url, user, level, role string) {
		t.Helper()
		want := map[string]string{"x-xbin-from": "apps/pa", "x-xbin-role": role, "x-xbin-user": user, "x-xbin-user-level": level,
			"x-xbin-partition": "user:" + user, "x-xbin-partition-id": pkey(user)}
		if code != 200 || a.URL != url || len(a.Xbin) != len(want) {
			t.Errorf("%s: %d %s; want 200 at %s with %v", label, code, raw, url, want)
			return
		}
		for k, v := range want {
			if a.Xbin[k] != v {
				t.Errorf("%s: %s = %q, want %q (%s)", label, k, a.Xbin[k], v, raw)
			}
		}
	}

	// the frame's own partition without the parameter: 503, no --isolate here
	if code, _, raw := do("GET", "/api/apps/pa/hello", frame("apps/pa", "alice"), ""); code != 503 {
		t.Errorf("alice's frame, her partition: %d %s", code, raw)
	}
	// xbin.fetch's URL (the parameter after the query), from alice's frame
	code, a, raw := do("GET", "/api/apps/pa/hello?q=1&xbin-partition=global", frame("apps/pa", "alice"), "")
	attributed("alice's frame", code, a, raw, "/hello?q=1", "alice", "read", "reader")
	code, a, raw = do("POST", "/api/apps/pa/runs?xbin-partition=global", frame("apps/pa", "wendy"), `{"x":1}`)
	attributed("wendy's frame (write)", code, a, raw, "/runs", "wendy", "write", "writer")
	// GlobalURL's URL, from alice's partition's backend (its instance token)
	u, _ := d.st.Users.Get("alice")
	d.st.Auth.RegisterInstancePartition("tok-alice-pa", "apps/pa", "", util.UserPartition("alice"), u.UID)
	inst := map[string]string{"Authorization": "Bearer tok-alice-pa", "X-XBin-User": "mallory", "X-XBin-Role": "admin"}
	code, a, raw = do("GET", "/api/apps/pa/runs/42?xbin-partition=global", inst, "")
	attributed("alice's partition's instance token", code, a, raw, "/runs/42", "alice", "read", "reader")

	// the owner token is in global already; an unpartitioned tile gets the parameter as any other
	owner := map[string]string{"Authorization": "Bearer " + d.owner}
	if code, a, raw := do("GET", "/api/apps/pa/hello?xbin-partition=global", owner, ""); code != 200 || a.URL != "/hello" ||
		a.Xbin["x-xbin-partition"] != "global" || a.Xbin["x-xbin-user"] != "" || a.Xbin["x-xbin-role"] != "admin" {
		t.Errorf("the owner token: %d %s", code, raw)
	}
	if code, a, raw := do("GET", "/api/apps/zsnode/hello?xbin-partition=global", owner, ""); code != 200 || a.URL != "/hello?xbin-partition=global" ||
		a.Xbin["x-xbin-partition"] != "" {
		t.Errorf("an unpartitioned tile: %d %s", code, raw)
	}

	// refusals
	if code, _, raw := do("GET", "/api/apps/pn/hello?xbin-partition=global", frame("apps/pn", "alice"), ""); code != 404 || !strings.Contains(raw, "apps/pn has no global instance") {
		t.Errorf("no global instance: %d %s", code, raw)
	}
	if code, _, raw := do("GET", "/api/apps/pa/hello?xbin-partition=global", frame("apps/zsnode", "alice"), ""); code != 403 ||
		!strings.Contains(raw, "apps/zsnode can't use it on apps/pa") {
		t.Errorf("another tile: %d %s", code, raw)
	}
	if code, _, raw := do("GET", "/api/apps/pa/hello?xbin-partition=user:wendy", frame("apps/pa", "alice"), ""); code != 400 {
		t.Errorf("another value: %d %s", code, raw)
	}
	// a cron delivery of alice's partition, through the dispatch cron uses,
	// and a bus delivery of hers, through the bus's
	dispatch := broker.DispatchViaProxy(d.st.Proxy)
	cron := auth.Principal{Component: broker.CronPrincipal, Via: "cron", Role: "writer", Partition: util.UserPartition("alice")}
	if code, raw := dispatch(cron, "apps/pa", "/tick?xbin-partition=global"); code != 403 || !strings.Contains(raw, "a cron delivery acts in the partition it was registered for") {
		t.Errorf("alice's cron delivery: %d %s", code, raw)
	}
	deliver := broker.DispatchBodyViaProxy(d.st.Proxy)
	bus := auth.Principal{Component: broker.BusPrincipal, Via: "bus", Role: "reader", Partition: util.UserPartition("alice")}
	if code, raw := deliver(t.Context(), bus, "apps/pa", "/on-event?xbin-partition=global", []byte(`{"topic":"t"}`)); code != 403 ||
		!strings.Contains(raw, "a bus delivery acts in the partition it was registered for") {
		t.Errorf("alice's bus delivery: %d %s", code, raw)
	}
	// a person in person: only an admin passes Route's rules on /api/<tile>/
	// (their frame is a reader's way in), with or without the parameter
	cookie := func(id string) map[string]string {
		return map[string]string{"Cookie": auth.CookieName + "=" + d.st.Auth.NewSession(id, "127.0.0.1")}
	}
	if code, _, raw := do("GET", "/api/apps/pa/hello?xbin-partition=global", cookie("alice"), ""); code != 403 ||
		!strings.Contains(raw, "user:alice is not granted access to apps/pa") {
		t.Errorf("alice in person: %d %s", code, raw)
	}
	if code, a, raw := do("GET", "/api/apps/pa/hello?xbin-partition=global", cookie("bob"), ""); code != 200 || a.URL != "/hello" ||
		a.Xbin["x-xbin-user"] != "bob" || a.Xbin["x-xbin-role"] != "admin" || a.Xbin["x-xbin-partition"] != "user:bob" || a.Xbin["x-xbin-from"] != "user:bob" {
		t.Errorf("bob (an admin) in person: %d %s", code, raw)
	}
	// an admin viewing as wendy (write): her frame reaches global as her,
	// read-only — reader whatever her level, X-XBin-Viewed-By, writes refused
	bobSession := d.st.Auth.NewSession("bob", "127.0.0.1")
	br := httptest.NewRequest("GET", "/", nil)
	br.AddCookie(&http.Cookie{Name: auth.CookieName, Value: bobSession})
	admin, ok := d.st.Auth.FromRequest(br)
	if !ok {
		t.Fatal("bob's session")
	}
	vt, err := d.st.Auth.NewImpersonationTicket(admin, "wendy")
	if err != nil {
		t.Fatal(err)
	}
	view, err := d.st.Auth.RedeemImpersonation(vt, admin, bobSession, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	code, _, raw = do("GET", "/api/xbin/frame-token?component=apps/pa", map[string]string{"Cookie": auth.CookieName + "=" + view}, "")
	var ft struct{ Token string }
	if err := json.Unmarshal([]byte(raw), &ft); code != 200 || err != nil || ft.Token == "" {
		t.Fatalf("wendy's frame token, viewed by bob: %d %s", code, raw)
	}
	viewFrame := map[string]string{auth.FrameTokenHeader: ft.Token}
	if code, a, raw := do("GET", "/api/apps/pa/hello?xbin-partition=global", viewFrame, ""); code != 200 || a.URL != "/hello" ||
		a.Xbin["x-xbin-user"] != "wendy" || a.Xbin["x-xbin-user-level"] != "write" || a.Xbin["x-xbin-role"] != "reader" ||
		a.Xbin["x-xbin-viewed-by"] != "bob" || a.Xbin["x-xbin-partition"] != "user:wendy" {
		t.Errorf("wendy's frame viewed by bob: %d %s", code, raw)
	}
	if code, _, raw := do("POST", "/api/apps/pa/runs?xbin-partition=global", viewFrame, `{}`); code != 403 || !strings.Contains(raw, "read-only") {
		t.Errorf("a write from wendy's frame viewed by bob: %d %s", code, raw)
	}
	if code, _, raw := do("GET", "/api/apps/pa/hello", viewFrame, ""); code != 403 || !strings.Contains(raw, "view-as can't open it") {
		t.Errorf("wendy's frame viewed by bob, her partition: %d %s", code, raw)
	}
	// a path ticket: its own partition only
	tk := frame("apps/pa", "alice")
	tk["Content-Type"] = "application/json"
	code, _, raw = do("POST", "/api/xbin/path-tickets", tk, `{"path":"preview"}`)
	var ticket struct{ URL string }
	if err := json.Unmarshal([]byte(raw), &ticket); code != 200 || err != nil || ticket.URL == "" {
		t.Fatalf("minting a path ticket: %d %s", code, raw)
	}
	if code, _, raw := do("GET", ticket.URL+"x?xbin-partition=global", nil, ""); code != 403 || !strings.Contains(raw, "a path ticket reaches its own partition only") {
		t.Errorf("a path ticket with the parameter: %d %s", code, raw)
	}
	if code, _, raw := do("GET", ticket.URL+"x", nil, ""); code != 503 {
		t.Errorf("a path ticket without it (alice's partition, no --isolate): %d %s", code, raw)
	}
}

// covers PD-16 — the seam guard for global-address/1 (06 §6): once GET
// /api/xbin/partitions is served (F7b), its features must list
// broker.GlobalAddressFeature, the word by which a client learns that this
// xbind consumes ?xbin-partition=global. Until that route exists (404) the
// guard only logs; a merge that mounts it without the word fails here.
func TestGlobalAddressFeatureServed(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	ws := zsWorkspace(t)
	for rel, body := range map[string]string{
		"apps/pa/xbin.json":         `{"runtime":"node","partition":["user","global"]}`,
		"apps/pa/backend/server.js": zsNodeServer,
	} {
		p := filepath.Join(ws, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	d := zsBoot(t, ws)
	req, err := http.NewRequest("GET", d.url+"/api/xbin/partitions?tile=apps/pa", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+d.owner)
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		t.Logf("GET /api/xbin/partitions isn't served yet (F7b): %s", raw)
		return
	}
	var body struct {
		Features []string `json:"features"`
	}
	if err := json.Unmarshal(raw, &body); resp.StatusCode != http.StatusOK || err != nil {
		t.Fatalf("GET /api/xbin/partitions: %d %s", resp.StatusCode, raw)
	}
	if !slices.Contains(body.Features, broker.GlobalAddressFeature) {
		t.Errorf("GET /api/xbin/partitions lists features %q without %q: F5 is served (TestGlobalAddressWiring), so list it", body.Features, broker.GlobalAddressFeature)
	}
}
