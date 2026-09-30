package boot

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/xbin-dev/xbin/internal/users"
)

// npEnvServer is a node backend that answers every request with the X-XBin-*
// headers it received and the names of its XBIN_* environment (with
// XBIN_PARTITION's value, were it set).
const npEnvServer = `const http = require('http');
http.createServer((req, res) => {
  const h = {};
  for (const [k, v] of Object.entries(req.headers)) if (k.startsWith('x-xbin-')) h[k] = v;
  const env = Object.keys(process.env).filter((k) => k.startsWith('XBIN_')).sort();
  res.setHeader('Content-Type', 'application/json');
  res.end(JSON.stringify({xbin: h, env, partition: process.env.XBIN_PARTITION}));
}).listen(process.env.XBIN_SOCKET);
`

// npGoldenWire is the rest of TestNoPartitionGolden (plans/partitions/10
// §A.1; work pack I1): on the same workspace without partitions, a
// backend's env and the X-XBin-* headers it receives (the owner's call and
// a reader's frame; a node backend, so its subtest SKIPs without node on
// PATH), bus event JSON, the cron store, ?partition= on vault, cron and
// logs (ignored: the answer is today's), and the users store after a
// delete and a recreate through the users API (no uid, no orphan) — none
// carries anything of partitions, and still no partition store is written.
func npGoldenWire(t *testing.T, d *zsDaemon, ws, owner, ana string) {
	t.Helper()
	for rel, body := range map[string]string{
		"apps/npnode/scope.json":        `{"resources":{"ev":{"type":"bus"},"tick":{"type":"cron"}}}`,
		"apps/npnode/backend/server.js": npEnvServer,
		"apps/npnode/xbin.json":         `{"runtime":"node","uses":[{"target":"res:apps/npnode/ev","role":"writer"},{"target":"res:apps/npnode/tick","role":"writer"}]}`,
		"apps/npnode/index.html":        "<!doctype html><html><head></head><body>npnode</body></html>\n",
	} {
		p := filepath.Join(ws, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.st.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.st.Users.Upsert(users.User{ID: "ana", Role: users.RoleUser,
		Tiles: map[string]string{"apps/zs": users.LevelRead, "notes+ideas": users.LevelWrite, "apps/np": users.LevelRead, "apps/npnode": users.LevelRead}}, ""); err != nil {
		t.Fatal(err)
	}
	code, body := d.do(t, "GET", "/api/xbin/frame-token?component=apps/npnode", ana)
	var ft struct{ Token string }
	if code != 200 || json.Unmarshal(body, &ft) != nil || ft.Token == "" {
		t.Fatalf("ana's frame token of apps/npnode: %d %s", code, body)
	}
	t.Run("backend", func(t *testing.T) { npGoldenBackend(t, d, owner, ft.Token) })
	npGoldenStores(t, d, ws, owner)
}

// npGoldenBackend: the backend's env and the headers it receives, today's.
func npGoldenBackend(t *testing.T, d *zsDaemon, owner, frameToken string) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("no node on PATH: the node backend's env and headers are not checked (the rest of the wire is)")
	}
	type echo struct {
		Xbin      map[string]string
		Env       []string
		Partition *string
	}
	call := func(name string, hdr ...string) echo {
		t.Helper()
		rq, _ := http.NewRequest("GET", d.url+"/api/apps/npnode/echo", nil)
		for i := 0; i+1 < len(hdr); i += 2 {
			rq.Header.Set(hdr[i], hdr[i+1])
		}
		r, err := (&http.Client{Timeout: time.Minute}).Do(rq)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		var e echo
		if r.StatusCode != 200 || json.Unmarshal(b, &e) != nil {
			t.Fatalf("%s's call to the node backend: %d %s", name, r.StatusCode, b)
		}
		for k := range r.Header {
			if strings.HasPrefix(strings.ToLower(k), "x-xbin-partition") {
				t.Errorf("%s: xbind answered with %s", name, k)
			}
		}
		return e
	}
	for name, hdr := range map[string][]string{
		"the owner":   {"Authorization", owner},
		"ana's frame": {"X-XBin-Frame-Token", frameToken, "X-XBin-Partition", "user:ana", "X-XBin-Partition-Id", "u-forged"},
	} {
		e := call(name, hdr...)
		for k := range e.Xbin {
			if strings.HasPrefix(k, "x-xbin-partition") {
				t.Errorf("%s: the backend received %s: %v", name, k, e.Xbin)
			}
		}
		for _, k := range e.Env {
			if strings.HasPrefix(k, "XBIN_PARTITION") {
				t.Errorf("%s: the backend's env has %s", name, k)
			}
		}
		if e.Partition != nil {
			t.Errorf("%s: XBIN_PARTITION=%q", name, *e.Partition)
		}
		if name == "the owner" && (len(e.Xbin) != 2 || e.Xbin["x-xbin-from"] != "owner" || e.Xbin["x-xbin-role"] != "admin") {
			t.Errorf("what the owner's call carried: %v (today's: from owner, role admin)", e.Xbin)
		}
		if name == "ana's frame" && (e.Xbin["x-xbin-user"] != "ana" || e.Xbin["x-xbin-from"] != "apps/npnode") {
			t.Errorf("what ana's frame's call carried: %v", e.Xbin)
		}
	}
}

// npGoldenStores: bus event JSON, the cron store, ?partition= and the users
// store after a delete and a recreate.
func npGoldenStores(t *testing.T, d *zsDaemon, ws, owner string) {
	t.Helper()
	// bus event JSON: no partition member
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(d.url, "http")+"/ws/events", http.Header{"Authorization": {owner}})
	if err != nil {
		t.Fatalf("/ws/events: %v", err)
	}
	defer conn.Close()
	time.Sleep(200 * time.Millisecond)
	if code, body := npDo(t, d, "POST", "/api/xbin/bus/publish", owner, `{"resource":"res:apps/npnode/ev","topic":"t","data":"np-evt"}`); code != 200 {
		t.Fatalf("publishing: %d %s", code, body)
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("no bus event on /ws/events: %v", err)
		}
		var ev map[string]any
		if json.Unmarshal(msg, &ev) != nil || ev["type"] != "bus" {
			continue
		}
		keys := make([]string, 0, len(ev))
		for k := range ev {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if strings.Join(keys, ",") != "data,topic,type" || ev["topic"] != "res:apps/npnode/ev/t" || ev["data"] != "np-evt" {
			t.Errorf("the bus event: %s (today's: {type, topic, data})", msg)
		}
		break
	}

	// the cron store and its listing
	if code, body := npDo(t, d, "PUT", "/api/xbin/cron/jobs", owner,
		`{"name":"np","resource":"res:apps/npnode/tick","schedule":"@every 1h","path":"/tick","role":"writer","component":"apps/npnode"}`); code != 200 {
		t.Fatalf("a cron job: %d %s", code, body)
	}
	if code, body := d.do(t, "GET", "/api/xbin/cron/jobs", owner); code != 200 || bytes.Contains(body, []byte("partition")) {
		t.Errorf("GET /cron/jobs: %d %s", code, body)
	}
	if b, err := os.ReadFile(filepath.Join(ws, "data", "cron-jobs.json")); err != nil || bytes.Contains(b, []byte("partition")) {
		t.Errorf("data/cron-jobs.json: %v %s", err, b)
	}

	// ?partition= is ignored on a tile that isn't partitioned (C22)
	if code, body := npDo(t, d, "PUT", "/api/xbin/vault/apps/npnode/k", owner, `{"value":"np-vault"}`); code/100 != 2 {
		t.Fatalf("a vault key: %d %s", code, body)
	}
	for _, p := range []string{"/api/xbin/vault/apps/npnode", "/api/xbin/vault/apps/npnode/k", "/api/xbin/cron/jobs", "/api/xbin/bus/subscriptions"} {
		sep := "?"
		if strings.Contains(p, "?") {
			sep = "&"
		}
		c1, b1 := d.do(t, "GET", p, owner)
		c2, b2 := d.do(t, "GET", p+sep+"partition=user:ana", owner)
		if c1 != c2 || !bytes.Equal(b1, b2) || c1/100 == 5 {
			t.Errorf("GET %s with ?partition=: %d %s, without: %d %s", p, c2, b2, c1, b1)
		}
	}
	c1, _ := d.do(t, "GET", "/api/xbin/logs?component=apps/npnode&tail=5", owner)
	c2, b2 := d.do(t, "GET", "/api/xbin/logs?component=apps/npnode&tail=5&partition=user:ana", owner)
	if c1 != c2 || bytes.Contains(b2, []byte("partition")) {
		t.Errorf("GET /logs with ?partition=: %d %s, without: %d", c2, b2, c1)
	}

	// the users store after a delete and a recreate through the users API
	// (DELETE /users/{id} runs the partition-aware delete hooks — for a
	// person who never held a partition, nothing): no uid, no orphan
	for _, step := range []struct{ method, path, body string }{
		{"DELETE", "/api/xbin/users/ana", ""},
		{"POST", "/api/xbin/users", `{"id":"ana","role":"user","tiles":{"apps/np":"read"},"password":"password2-np"}`},
	} {
		if code, body := npDo(t, d, step.method, step.path, owner, step.body); code != 200 {
			t.Fatalf("%s %s: %d %s", step.method, step.path, code, body)
		}
		if b, err := os.ReadFile(filepath.Join(ws, "data", "users.json")); err != nil || bytes.Contains(b, []byte(`"uid"`)) {
			t.Errorf("data/users.json after %s %s: %v %s", step.method, step.path, err, b)
		}
	}
	for _, rel := range []string{"data/partitions", "data/orphans"} {
		if _, err := os.Stat(filepath.Join(ws, filepath.FromSlash(rel))); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s exists after the wire checks: %v", rel, err)
		}
	}
}

// npDo sends one request with a JSON body and the credential (a bearer).
func npDo(t *testing.T, d *zsDaemon, method, path, cred, body string) (int, []byte) {
	t.Helper()
	rq, err := http.NewRequest(method, d.url+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	rq.Header.Set("Content-Type", "application/json")
	rq.Header.Set("Authorization", cred)
	r, err := (&http.Client{Timeout: time.Minute}).Do(rq)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, b
}
