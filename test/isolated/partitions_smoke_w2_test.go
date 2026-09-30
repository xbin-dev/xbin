//go:build linux && integration

package isolated

// partitions_smoke_w2_test.go — the partitions smoke's wave-2 cases, on a
// real `xbind --isolate` with owner auth on, beside TestPartitionsSmoke
// (partitions_smoke_test.go, whose fixture and helpers it shares): a
// person's partition's vault (F5), its cron job (F5), a person's terminal
// on the partitioned tile (F7a), a personal bind (F15) and
// ?xbin-partition=global (F9). Records: plans/partitions/records/W2-wire.md.
//
// It runs on a dev box only (user namespaces, a base rootfs, gocryptfs):
//
//	set -a; eval "$(sed -n 's/^export \([A-Z_]*\) := \(.*\)$/\1=\2/p' .dev.mk | grep -v ^PATH)"; set +a
//	go test -tags=integration -count=1 -v -run '^TestPartitionsSmokeW2$' ./test/isolated/
//
// The cron case waits for the job's first tick (a minute: no schedule is
// allowed more often), so the test takes over a minute. Like the main
// smoke, run it with the Bash sandbox off; CI skips it (no rootfs).

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

const (
	w2Tile  = "apps/pw2"        // the partitioned fixture: ["user", "global"], a multi http slot mcp
	w2Mcp   = "users/alice/mcp" // alice's personal tile, providing mcp
	w2Alice = "alice-key-7f3c"  // alice's partition's vault key
	w2Glob  = "global-key-19ad" // the global instance's vault key
)

// w2Routes are the probe's wave-2 routes: its vault (the SDK's Secret and
// SetSecret, instance token), an env variable, and the cron job's handler,
// which stores who ticked it in the partition's own kv.
const w2Routes = `	mux.HandleFunc("GET /secret/{key}", func(w http.ResponseWriter, r *http.Request) {
		v, err := xbin.Secret(r.PathValue("key"))
		switch {
		case err != nil && strings.Contains(err.Error(), "404"):
			reply(w, 404, map[string]string{"error": "not found: " + err.Error()})
		case err != nil:
			reply(w, 502, map[string]string{"error": err.Error()})
		default:
			reply(w, 200, map[string]string{"value": v})
		}
	})
	mux.HandleFunc("PUT /secret/{key}", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if err := xbin.SetSecret(r.PathValue("key"), string(b)); err != nil {
			reply(w, 502, map[string]string{"error": err.Error()})
			return
		}
		reply(w, 200, map[string]string{"ok": "stored"})
	})
	mux.HandleFunc("GET /env/{name}", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]string{"value": os.Getenv(r.PathValue("name"))})
	})
	mux.HandleFunc("/tick", func(w http.ResponseWriter, r *http.Request) {
		b, _ := json.Marshal(who(r))
		if err := xbin.KV(xbin.Resource("kv")).Put("tick", b); err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, map[string]string{"ok": "ticked"})
	})
`

// w2Source is the probe with the wave-2 routes.
var w2Source = strings.Replace(strings.Replace(psSource, "\t\"path/filepath\"\n", "\t\"path/filepath\"\n\t\"strings\"\n", 1),
	"\txbin.Serve(mux)\n", w2Routes+"\txbin.Serve(mux)\n", 1)

// w2Files is a wave-2 probe tile: manifest (the rest of its xbin.json after
// "runtime") and its scope's resources.
func w2Files(tile, manifest string) map[string]string {
	return map[string]string{
		"go.mod":          "module ps/" + strings.ReplaceAll(tile, "/", "_") + "\n\ngo 1.24\n\nrequire github.com/xbin-dev/xbin/sdk v0.0.0\n",
		"backend/main.go": w2Source,
		"scope.json":      `{"resources": {"kv": {"type": "kv"}, "beat": {"type": "cron"}}}` + "\n",
		"index.html":      "<!doctype html><title>probe</title><p>the probe's page</p>\n",
		"xbin.json":       `{"runtime": "go"` + manifest + "}\n",
	}
}

func w2Write(t *testing.T, d *xbindtest.Daemon, tile, manifest string) {
	t.Helper()
	files := w2Files(tile, manifest)
	m := files["xbin.json"]
	delete(files, "xbin.json")
	if err := d.WriteFiles(tile, files); err != nil {
		t.Fatal(err)
	}
	d.WriteTile(t, tile, map[string]string{"xbin.json": m})
}

func TestPartitionsSmokeW2(t *testing.T) {
	t.Parallel()
	e := psSetup(t)
	d := e.d
	uses := `, "uses": [{"target": "res:%s/kv", "role": "writer"}, {"target": "res:%s/beat", "role": "writer"}]`
	w2Write(t, d, w2Mcp, fmt.Sprintf(uses, w2Mcp, w2Mcp)+`, "provides": {"mcp": {"kind": "http", "service": "mcp"}}`)
	d.Must(t, "POST", "/api/xbin/owner", map[string]string{"tile": w2Mcp, "to": "user:alice"}, 200)
	w2Write(t, d, w2Tile, `, "partition": ["user", "global"]`+fmt.Sprintf(uses, w2Tile, w2Tile)+
		`, "interfaces": {"mcp": {"kind": "http", "service": "mcp", "multi": true}}`)
	e.waitState(t, w2Tile, "partitioned")

	api := "/api/" + w2Tile
	// everyone's data, named after them
	e.put(t, api+"/kv/kv/secret", "global-secret")
	for _, p := range []string{"alice", "bob"} {
		e.put(t, api+"/kv/kv/secret", p+"-secret", e.fr(t, w2Tile, p))
	}
	// alice's partition's cron job, registered by her frame (the partition
	// gate stamps it): it ticks in a minute, checked last
	d.Must(t, "PUT", "/api/xbin/cron/jobs", map[string]string{"name": "beat", "resource": "res:" + w2Tile + "/beat",
		"schedule": "@every 1m", "path": "/tick"}, 200, e.fr(t, w2Tile, "alice"))
	registered := time.Now()

	t.Run("vault", func(t *testing.T) {
		// F5 (03 §C, PD-07, S19): a partition's vault is its own; its values
		// are its backend's alone; the global instance's is today's
		e.put(t, api+"/secret/"+w2Alice, "alice-vault", e.fr(t, w2Tile, "alice"))
		d.Must(t, "PUT", "/api/xbin/vault/"+w2Tile+"/"+w2Glob, map[string]string{"value": "global-vault"}, 200)
		psExpect(t, "alice's backend reads her key", d.Call(t, "GET", api+"/secret/"+w2Alice, nil, e.fr(t, w2Tile, "alice")),
			[]string{"global-vault"}, psOK("alice-vault"))
		psExpect(t, "bob's backend reads alice's key", d.Call(t, "GET", api+"/secret/"+w2Alice, nil, e.fr(t, w2Tile, "bob")),
			[]string{"alice-vault"}, psWant{404, "not found", false})
		psExpect(t, "the global instance reads alice's key", d.Call(t, "GET", api+"/secret/"+w2Alice, nil),
			[]string{"alice-vault"}, psWant{404, "not found", false})
		psExpect(t, "the global instance reads its own key", d.Call(t, "GET", api+"/secret/"+w2Glob, nil), nil, psOK("global-vault"))
		psExpect(t, "alice's backend reads global's key", d.Call(t, "GET", api+"/secret/"+w2Glob, nil, e.fr(t, w2Tile, "alice")),
			[]string{"global-vault"}, psWant{404, "not found", false})
		// admins list global's keys and count people's vaults, never their names
		for name, hdrs := range map[string][]xbindtest.Header{"carol (admin)": e.as("carol"), "the owner token": nil} {
			for _, path := range []string{"/api/xbin/vaults", "/api/xbin/vault/" + w2Tile} {
				r := d.Call(t, "GET", path, nil, hdrs...)
				if r.Status != 200 || strings.Contains(string(r.Body), w2Alice) || strings.Contains(string(r.Body), "alice-vault") {
					t.Errorf("BUG: %s, GET %s: %d %s", name, path, r.Status, cut(r.String(), 300))
				}
			}
		}
	})

	t.Run("global-address", func(t *testing.T) {
		// F9 (05 §6): ?xbin-partition=global from a person's frame reaches
		// the global instance as that person; bob can't use it to reach
		// alice, whatever he sends
		alice, bob := e.fr(t, w2Tile, "alice"), e.fr(t, w2Tile, "bob")
		w := e.who(t, api+"/who?xbin-partition=global", alice)
		if w.Env != "global" || w.Caller.User != "alice" || w.Caller.Partition != "user:alice" || w.Caller.Role != "reader" ||
			w.Caller.UserLevel != "read" || w.Caller.From != w2Tile || strings.Contains(w.Query, "xbin-partition") {
			t.Errorf("alice's frame, ?xbin-partition=global: %+v", w)
		}
		psExpect(t, "alice's frame reads global's kv", d.Call(t, "GET", api+"/kv/kv/secret?xbin-partition=global", nil, alice),
			[]string{"alice-secret", "bob-secret"}, psOK("global-secret"))
		spoof := []xbindtest.Header{bob, xbindtest.H("X-XBin-User", "alice"), xbindtest.H("X-XBin-Partition", "user:alice")}
		w = e.who(t, api+"/who?xbin-partition=global", spoof...)
		if w.Env != "global" || w.Caller.User != "bob" || w.Caller.Partition != "user:bob" {
			t.Errorf("BUG: bob's frame with alice's headers, ?xbin-partition=global: %+v", w)
		}
		for _, q := range []string{"user:alice", "user:bob", "", "global&xbin-partition=global"} {
			psExpect(t, "bob's frame, ?xbin-partition="+q, d.Call(t, "GET", api+"/kv/kv/secret?xbin-partition="+q, nil, spoof...),
				[]string{"alice-secret"}, psBadPartitionParam)
		}
		psExpect(t, "bob's frame, ?xbin-partition=global", d.Call(t, "GET", api+"/kv/kv/secret?xbin-partition=global", nil, spoof...),
			[]string{"alice-secret"}, psOK("global-secret"))
		// a registration carrying it is refused up front (W2)
		psExpect(t, "alice's cron job with ?xbin-partition", d.Call(t, "PUT", "/api/xbin/cron/jobs", map[string]string{"name": "g",
			"resource": "res:" + w2Tile + "/beat", "schedule": "@every 1m", "path": "/tick?xbin-partition=global"}, alice),
			nil, psWant{400, "delivery acts in the partition it was registered for", false})
	})

	t.Run("personal-bind", func(t *testing.T) {
		// F15 (05 §3, PD-54): alice binds her own tile into her own
		// partition: only her partition's env and frames list it, only her
		// partition's calls pass; bob's and global get today's 403
		r := d.Call(t, "POST", "/api/xbin/partitions/binds", map[string]string{"requester": w2Tile, "slot": "mcp", "provider": w2Mcp}, e.as("alice")...)
		if r.Status != 200 {
			t.Fatalf("alice's personal bind: %d %s", r.Status, r)
		}
		psExpect(t, "carol (an admin) makes a personal bind",
			d.Call(t, "POST", "/api/xbin/partitions/binds", map[string]string{"requester": w2Tile, "slot": "mcp", "provider": w2Mcp}, e.as("carol")...),
			nil, psWant{403, "", false})
		env := func(hdrs ...xbindtest.Header) string {
			t.Helper()
			var v string
			xbindtest.Eventually(t, 4*time.Minute, "the probe's env answers", func() (bool, string) {
				val, st, body := e.value(t, api+"/env/XBIN_IFACE_MCP", hdrs...)
				v = val
				return st == 200, fmt.Sprint(st, " ", body)
			})
			return v
		}
		if v := env(e.fr(t, w2Tile, "alice")); !strings.Contains(v, `"provider":"`+w2Mcp+`"`) || !strings.Contains(v, `"personal":true`) {
			t.Errorf("alice's partition's XBIN_IFACE_MCP: %q", v)
		}
		for name, hdrs := range map[string][]xbindtest.Header{"bob's partition": {e.fr(t, w2Tile, "bob")}, "the global instance": nil} {
			if v := env(hdrs...); strings.Contains(v, w2Mcp) {
				t.Errorf("BUG: %s's XBIN_IFACE_MCP lists alice's personal bind: %q", name, v)
			}
		}
		call := api + "/call?path=/api/" + w2Mcp + "/who"
		w, st := relayWho(t, d.Call(t, "GET", call, nil, e.fr(t, w2Tile, "alice")))
		if st != 200 || w.Caller.From != w2Tile || w.Caller.Partition != "user:alice" {
			t.Errorf("alice's partition calls her personal tile: %d %+v", st, w)
		}
		for name, hdrs := range map[string][]xbindtest.Header{"bob's partition": {e.fr(t, w2Tile, "bob")}, "the global instance": nil} {
			psExpect(t, name+" calls alice's personal tile", d.Call(t, "GET", call, nil, hdrs...), nil,
				psWant{403, "not granted access to " + w2Mcp, false})
		}
		for p, want := range map[string]bool{"alice": true, "bob": false} {
			doc := d.Call(t, "GET", "/c/"+w2Tile+"/", nil, e.as(p)...)
			if doc.Status != 200 || strings.Contains(string(doc.Body), w2Mcp) != want {
				t.Errorf("%s's document lists alice's personal bind: %v, want %v (%d)", p, strings.Contains(string(doc.Body), w2Mcp), want, doc.Status)
			}
		}
	})

	t.Run("terminal", func(t *testing.T) {
		// F7a (06 §1-§3): a person's terminal on the partitioned tile acts
		// in their own partition: its env, $HOME, and every call it makes
		// with its token — curl and bx — reach only their data
		d.Must(t, "PUT", "/api/xbin/access", map[string]string{"tile": w2Tile, "kind": "user", "id": "alice", "level": "terminal"}, 200)
		// a user's terminal gets a token only with termApi, and is offline
		// without termNet (D17): the relay is how its bx and curl reach xbind
		d.Must(t, "PATCH", "/api/xbin/users/alice", map[string]bool{"termNet": true, "termApi": true}, 200)
		s := w2OpenTerm(t, d, w2Tile, e.as("alice")[0])
		if s.partition != "user:alice" {
			t.Errorf("alice's session frame names partition %q", s.partition)
		}
		run := func(cmd string) string {
			t.Helper()
			out, rc := s.run(t, cmd, time.Minute)
			if rc != 0 {
				t.Errorf("%s: exit %d: %s", cmd, rc, cut(out, 300))
			}
			return out
		}
		if out := run(`printf '%s|%s|%s' "$XBIN_PARTITION" "$PWD" "$HOME"`); !strings.HasPrefix(out, "user:alice|") || strings.Split(out, "|")[1] != strings.Split(out+"||", "|")[2] {
			t.Errorf("alice's terminal: XBIN_PARTITION|PWD|HOME = %q", out)
		}
		if out := run(`printf '%s' "${#XBIN_TOKEN}"`); out == "0" {
			t.Fatal("alice's terminal has no XBIN_TOKEN")
		}
		c := `curl -s -H "Authorization: Bearer $XBIN_TOKEN" `
		for _, x := range []struct {
			name, cmd, want string
			forbid          []string
		}{
			{"the kv API", c + `"$XBIN_URL/api/xbin/kv/res:` + w2Tile + `/kv/secret"`, "alice-secret", []string{"bob-", "global-"}},
			{"the tile's API", c + `"$XBIN_URL/api/` + w2Tile + `/kv/kv/secret"`, `{"value":"alice-secret"}`, []string{"bob-", "global-"}},
			{"the tile's API, bob's headers", c + `-H "X-XBin-Partition: user:bob" -H "X-XBin-User: bob" "$XBIN_URL/api/` + w2Tile + `/kv/kv/secret"`,
				`{"value":"alice-secret"}`, []string{"bob-", "global-"}},
			{"the tile's API, ?xbin-partition=user:bob", c + `-o /dev/null -w '%{http_code}' "$XBIN_URL/api/` + w2Tile + `/kv/kv/secret?xbin-partition=user:bob"`,
				"400", nil},
			{"the vault's keys", c + `"$XBIN_URL/api/xbin/vault/` + w2Tile + `"`, w2Alice, []string{w2Glob, "alice-vault"}},
			{"bx vault ls", `bx vault ls ` + w2Tile, w2Alice, []string{w2Glob, "alice-vault"}},
			// F7b (06 §5, §7): tile-status names her partition (what bx status
			// prints — the terminal's bx is the base rootfs's, so the smoke
			// asks the route), and bx logs reads her partition's own log
			{"tile-status", c + `"$XBIN_URL/api/xbin/tile-status?component=` + w2Tile + `"`, `"partition":"user:alice"`, []string{"bob-", "global-"}},
			{"bx logs", `bx logs ` + w2Tile, "alice-secret", []string{"bob-secret", "global-secret"}},
		} {
			out := run(x.cmd)
			if !strings.Contains(out, x.want) {
				t.Errorf("alice's terminal, %s: %q, want %q", x.name, cut(out, 300), x.want)
			}
			for _, f := range x.forbid {
				if strings.Contains(out, f) {
					t.Errorf("BUG: alice's terminal, %s: %q holds %q", x.name, cut(out, 300), f)
				}
			}
		}
	})

	t.Run("cron", func(t *testing.T) {
		// F5 (03 §D, PD-20): alice's job ticks her partition, as xbin/cron,
		// and nothing of it lands in bob's or global's
		var tick psWho
		xbindtest.Eventually(t, 3*time.Minute-time.Since(registered)+time.Minute, "alice's job ticks", func() (bool, string) {
			v, st, body := e.value(t, api+"/kv/kv/tick", e.fr(t, w2Tile, "alice"))
			return st == 200 && json.Unmarshal([]byte(v), &tick) == nil, fmt.Sprint(st, " ", cut(body, 200))
		})
		if tick.Env != "user:alice" || tick.Caller.From != "xbin/cron" || tick.Caller.Partition != "user:alice" {
			t.Errorf("alice's tick: %+v", tick)
		}
		for name, hdrs := range map[string][]xbindtest.Header{"bob's partition": {e.fr(t, w2Tile, "bob")}, "the global instance": nil} {
			psExpect(t, name+"'s tick", d.Call(t, "GET", api+"/kv/kv/tick", nil, hdrs...), nil, psWant{404, `"not found"`, false})
		}
		r := d.Call(t, "GET", "/api/xbin/cron/jobs", nil, e.fr(t, w2Tile, "bob"))
		if r.Status != 200 || strings.Contains(string(r.Body), `"beat"`) {
			t.Errorf("BUG: bob's partition lists alice's job: %d %s", r.Status, cut(r.String(), 300))
		}
	})
}

// w2Term is a terminal session over /ws/term, driven as a person drives a
// shell (flowc_test.go's termSession, for this package).
type w2Term struct {
	conn      *websocket.Conn
	partition string
	mu        sync.Mutex
	out       []byte
	more      chan struct{}
	read      int
	calls     int
}

var w2ANSI = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[()][0-9A-B]|\x1b[=>]`)

// w2OpenTerm opens a session on tile as cred and quiets its shell.
func w2OpenTerm(t *testing.T, d *xbindtest.Daemon, tile string, cred xbindtest.Header) *w2Term {
	t.Helper()
	conn, r, err := d.Dial(t, "/ws/term?cwd="+tile, cred)
	if err != nil {
		t.Fatalf("/ws/term on %s: %v (%d %s)", tile, err, r.Status, r)
	}
	s := &w2Term{conn: conn, more: make(chan struct{}, 1)}
	ready := make(chan string, 1)
	go func() {
		for {
			kind, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if kind == websocket.TextMessage {
				var c struct{ Op, ID, Partition string }
				if json.Unmarshal(msg, &c) == nil && c.Op == "session" {
					s.mu.Lock()
					s.partition = c.Partition
					s.mu.Unlock()
					select {
					case ready <- c.ID:
					default:
					}
				}
				continue
			}
			s.mu.Lock()
			s.out = append(s.out, msg...)
			s.mu.Unlock()
			select {
			case s.more <- struct{}{}:
			default:
			}
		}
	}()
	var id string
	select {
	case id = <-ready:
	case <-time.After(2 * time.Minute):
		t.Fatal("/ws/term sent no session frame")
	}
	t.Cleanup(func() {
		conn.Close()
		d.Call(t, "DELETE", "/ws/term?session="+id, nil)
	})
	_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"op":"resize","cols":400,"rows":50}`))
	if _, rc := s.run(t, "stty -echo; PS1=''; PS2=''; unset PROMPT_COMMAND", time.Minute); rc != 0 {
		t.Fatalf("quieting the session's shell: exit %d", rc)
	}
	return s
}

// run types cmd and returns its output (escapes stripped, trimmed) and exit
// status; the markers are spelled with an empty quote, so the typed line
// never matches them.
func (s *w2Term) run(t *testing.T, cmd string, timeout time.Duration) (string, int) {
	t.Helper()
	s.calls++
	n := s.calls
	begin, end := fmt.Sprintf("__XB%d__", n), fmt.Sprintf("__XE%d__", n)
	line := fmt.Sprintf("echo '__XB'%d__; %s; echo '__XE'%d__=$?\n", n, cmd, n)
	if err := s.conn.WriteMessage(websocket.BinaryMessage, []byte(line)); err != nil {
		t.Fatalf("typing into the session: %v", err)
	}
	done := regexp.MustCompile(regexp.QuoteMeta(end) + `=(\d+)`)
	deadline := time.After(timeout)
	for {
		s.mu.Lock()
		text := strings.ReplaceAll(w2ANSI.ReplaceAllString(string(s.out[s.read:]), ""), "\r", "")
		consumed := len(s.out)
		s.mu.Unlock()
		if m := done.FindStringSubmatchIndex(text); m != nil {
			start := strings.Index(text, begin+"\n")
			if start < 0 || start > m[0] {
				start = 0
			} else {
				start += len(begin) + 1
			}
			rc, _ := strconv.Atoi(text[m[2]:m[3]])
			s.mu.Lock()
			s.read = consumed
			s.mu.Unlock()
			return strings.TrimSpace(text[start:m[0]]), rc
		}
		select {
		case <-s.more:
		case <-deadline:
			t.Fatalf("the session never finished %q; it printed:\n%s", cmd, text)
		}
	}
}
