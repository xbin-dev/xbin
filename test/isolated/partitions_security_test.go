//go:build linux && integration

package isolated

// partitions_security_test.go — the partitioned tiles' security suite
// (plans/partitions/10 §B.2, the threat model; work pack I1), on a real
// `xbind --isolate` with owner auth on, with S1's fixture and helpers
// (partitions_smoke_fx_test.go); its longer cases are in
// partitions_security_cases_test.go. Each case is one actor's explicit
// attempt on one path the table lists, with its exact expected answer: the
// status and the refusal's words, and that no other person's data comes
// back. A check that something did not reach someone has a positive control
// beside it — the same path reaching whom it should — so a dead socket or a
// fixture that never ran fails instead of passing.
//
// The table's rows and where each is attempted — this file's subtests
// (TestPartitionsSecurity/<name>), or the suite that already makes the same
// attempt, named so that nothing is tried twice. "In-process" names a
// package test that is the only coverage of that part of the row:
//
//	bob → alice's data through /api/<tile>/…        TestPartitionsSmoke/bob-never-sees-alice
//	bob forges X-XBin-Partition/-Id/-User           TestPartitionsSmoke/bob-never-sees-alice, TestPartitionsSmokeW2/global-address
//	bob holds alice's frame token                   tokens (a signed-out session's and a tampered token: 401)
//	bob's /ws/events: bus, session, status          events (her bus event, status and terminal's term events reach bob, an
//	                                                  admin and the root token nowhere); an agent session's own `session`
//	                                                  events: in-process (internal/server TestPartitionTermAdmin)
//	bob's terminal: /tmp leftovers of alice's       terminal (her /opt change is in her layer only; sessions start in $HOME)
//	a writer changes code to exfiltrate             residual (PD-23): the trust panel, not an xbind refusal
//	admin: API, view-as, logs, vault, mail          TestPartitionsSmoke/admin, /view-as, /logs; TestPartitionsSmokeW2/vault; TestPartitionsSmokeMail/outsiders
//	admin: reattach, drive, events, rename (kill)   terminal (reattach; get, events, log, prompt; rename; the listing's
//	                                                  name; kill allowed); cancel, restart, diff, answers: in-process
//	                                                  (TestPartitionTermAdmin)
//	admin: backups                                  TestPartitionsSmokeBackups (sealed: ciphertext at the archiver)
//	admin: a reset link / password / SSO email      credential-reset (credentialResetConfirm on: held; the link redeemed by
//	                                                  whoever holds it refused until alice allows it; alice told)
//	admin or root token forges mail to alice        TestPartitionsSmokeMail/outsiders
//	admin: Z uses X, mail wakes Z                   TestPartitionsSmokeMail/never-run-restart (mail starts no partition); consent
//	admin: restore alice's archive into bob         TestPartitionsSmokeBackups (bob, alice's frame, bob's partition id: 403)
//	another (unpartitioned) tile → the tile         TestPartitionsSmoke/global
//	bob's partition of Z → X                        TestPartitionsSmoke/cross-tile; consent (policy on)
//	a partition's code: F5 to global                TestPartitionsSmokeW2/global-address (attributed)
//	a partition's code: the bot's replies anywhere  TestPartitionsAgentChannels/forged-outbox
//	a partition's code: shared routing rows         read-only ("read" kv, blob and bus refuse writes, deletes, publishes)
//	a partition's code: a private empty trigger     TestPartitionsAgentChannels/private-webhook-trigger; the agent template's trigger tests
//	members widen a hosted conversation             B2d (not built in this wave)
//	the global instance's code → a person           global-no-route
//	alice's compromised backend: mounts, loopback,  TestPartitionsSmoke/data-apart (mounts); host-net (loopback,
//	  abstract sockets, host network                  abstract sockets, host network under a net → host bind)
//	alice's partition spams people                  notify (clamped to alice); TestPartitionsSmokeMail/person-to-global (mail)
//	a code writer flips partition / rolls back      TestPartitionsSmoke/mode-keep, /mode-pending-unpartitioned; test/partitions_test.go
//	                                                  (TestPartitionsNoIsolate/pending, /rollback)
//	a code writer switches the mode                 mode-deciders (a writer, the tile's frame, instance and terminal tokens: 403)
//	a tile manager switches                         TestPartitionsSmoke/mode-switch (typed, recorded)
//	a builtin update / template merge               TestPartitionsTemplateMerge (a template instance's merge, e2e); a
//	                                                  builtin update adding partition: in-process only
//	                                                  (internal/broker TestBuiltinUpdatePROnlyPartition,
//	                                                  TestUpdaterReadsRecordedPartition)
//	no bind authority: a shared provider            bind-authority
//	bob's partition / global → alice's personal     personal-bind (making one: 403; calling one: 403, and alice's own
//	                                                  reaches it — TestPartitionsSmokeW2/personal-bind's control)
//	an archiver reads backups                       TestPartitionsSmokeBackups (XBINSEAL, no plaintext)
//	an old backup of a deleted person               TestPartitionsSmokeBackups (erased key: unrestorable)
//	a live partition token after a mode change      TestPartitionsSmoke/token-revocation, /mode-switch
//	a lost users file / index                       test/downgrade_partitions_test.go (TestDowngradePartitions: the previous
//	                                                  release drops the uids — re-adopted; a person it deleted and made
//	                                                  again gets fresh partitions); a lost partition.json rebuilt: in-process
//	                                                  (internal/broker TestPartitionRecordsReadopt); a corrupted file: not tried
//	deleted alice, recreated                        TestPartitionsSmoke/person-recreated
//	a person who lost read                          lost-read (their partition's cron job is dormant); their bus
//	                                                  subscriptions: in-process (TestPartitionRegsLostRead); their
//	                                                  tokens: TestPartitionsSmoke/token-revocation, /cross-tile
//	a provider keyed on From                        TestCodingSandboxContract (user-partitions through xbind)
//	a crash in alice's partition                    crash (the breaker's error names her partition, no log path; her
//	                                                  sockets only)
//
//	set -a; eval "$(sed -n 's/^export \([A-Z_]*\) := \(.*\)$/\1=\2/p' .dev.mk | grep -v ^PATH)"; set +a
//	go test -tags=integration -count=1 -v -run '^TestPartitionsSecurity$' ./test/isolated/
//
// Like the smoke, it needs user namespaces, a base rootfs and gocryptfs: a
// dev box's suite, run with the Bash sandbox off. CI's integration job has
// no rootfs, so it SKIPs there (xbindtest.Require): the release checklist
// (plans/partitions/records/I1.md) runs it. The lost-read case waits for a
// cron job's first minute, so the suite takes about two minutes.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

const secTile = "apps/psec" // ["user", "global"]: kv, a cron, a bus, "read"-shared kv, blob and bus; net and a multi mcp slot

// secRoutes are the security probe's routes beside the wave-2 probe's: a
// POST relay through xbin.Client, its network namespace and the abstract
// unix sockets it sees, a TCP dial, a loopback listener and an abstract
// socket of its own, a bus publish, a status report, and an exit (a
// crash, logged first).
const secRoutes = `	mux.HandleFunc("POST /callp", func(w http.ResponseWriter, r *http.Request) {
		resp, err := xbin.Client().Post("http://xbin"+r.URL.Query().Get("path"), "application/json", r.Body)
		if err != nil {
			reply(w, 502, map[string]string{"error": err.Error()})
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		reply(w, 200, map[string]any{"status": resp.StatusCode, "body": string(b)})
	})
	mux.HandleFunc("GET /net", func(w http.ResponseWriter, r *http.Request) {
		ns, err := os.Readlink("/proc/self/ns/net")
		unix, _ := os.ReadFile("/proc/net/unix")
		reply(w, 200, map[string]any{"ns": ns, "err": fmt.Sprint(err), "unix": string(unix)})
	})
	mux.HandleFunc("GET /dial", func(w http.ResponseWriter, r *http.Request) {
		c, err := net.DialTimeout("tcp", r.URL.Query().Get("addr"), 3*time.Second)
		if err != nil {
			reply(w, 200, map[string]string{"error": err.Error()})
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		b, _ := io.ReadAll(io.LimitReader(c, 200))
		reply(w, 200, map[string]string{"read": string(b)})
	})
	mux.HandleFunc("POST /listen", func(w http.ResponseWriter, r *http.Request) {
		me := xbin.Partition() + " " + boot
		serve := func(ln net.Listener) {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				_, _ = io.WriteString(c, me)
				c.Close()
			}
		}
		tcp, err := net.Listen("tcp", "127.0.0.1:"+r.URL.Query().Get("port"))
		if err != nil {
			fail(w, err)
			return
		}
		go serve(tcp)
		abs, err := net.Listen("unix", "@"+r.URL.Query().Get("name"))
		if err != nil {
			fail(w, err)
			return
		}
		go serve(abs)
		reply(w, 200, map[string]string{"ok": me})
	})
	mux.HandleFunc("POST /publish", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if err := xbin.Publish("res:"+xbin.Self()+"/bus", r.URL.Query().Get("topic"), string(b)); err != nil {
			reply(w, 502, map[string]string{"error": err.Error()})
			return
		}
		reply(w, 200, map[string]string{"ok": "published"})
	})
	mux.HandleFunc("POST /status", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if err := xbin.Status("error", string(b)); err != nil {
			reply(w, 502, map[string]string{"error": err.Error()})
			return
		}
		reply(w, 200, map[string]string{"ok": "reported"})
	})
	mux.HandleFunc("POST /exit", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("crash-probe exiting in %s", xbin.Partition())
		reply(w, 200, map[string]string{"ok": "exiting"})
		go func() { time.Sleep(200 * time.Millisecond); os.Exit(1) }()
	})
`

// secSource is the wave-2 probe with the security routes.
var secSource = strings.Replace(strings.Replace(w2Source, "\t\"encoding/json\"\n", "\t\"encoding/json\"\n\t\"fmt\"\n\t\"net\"\n\t\"time\"\n", 1),
	"\txbin.Serve(mux)\n", secRoutes+"\txbin.Serve(mux)\n", 1)

// secWrite writes the security probe at secTile, its manifest last.
func secWrite(t *testing.T, d *xbindtest.Daemon) {
	t.Helper()
	files := w2Files(secTile, `, "partition": ["user", "global"], "uses": [`+
		`{"target": "res:`+secTile+`/kv", "role": "writer"}, {"target": "res:`+secTile+`/beat", "role": "writer"}, `+
		`{"target": "res:`+secTile+`/bus", "role": "writer"}, {"target": "res:`+secTile+`/pub", "role": "writer"}, `+
		`{"target": "res:`+secTile+`/pubblob", "role": "writer"}, {"target": "res:`+secTile+`/pubbus", "role": "writer"}], `+
		`"interfaces": {"net": {"kind": "net"}, "mcp": {"kind": "http", "service": "mcp", "multi": true}}`)
	files["backend/main.go"] = secSource
	files["scope.json"] = `{"resources": {"kv": {"type": "kv"}, "beat": {"type": "cron"}, "bus": {"type": "bus"}, "pub": {"type": "kv", "shared": "read"}, ` +
		`"pubblob": {"type": "blob", "shared": "read"}, "pubbus": {"type": "bus", "shared": "read"}}}` + "\n"
	m := files["xbin.json"]
	delete(files, "xbin.json")
	if err := d.WriteFiles(secTile, files); err != nil {
		t.Fatal(err)
	}
	d.WriteTile(t, secTile, map[string]string{"xbin.json": m})
}

// secRelay is what the probe's /call or /callp relay reached: the relayed
// call's status and body.
func secRelay(t *testing.T, r xbindtest.Resp) (int, string) {
	t.Helper()
	var out struct {
		Status int
		Body   string
	}
	if r.Status != 200 || json.Unmarshal(r.Body, &out) != nil || out.Status == 0 {
		t.Fatalf("the relay itself: %d %s", r.Status, r)
	}
	return out.Status, out.Body
}

// secNet is the probe's /net: its network namespace and /proc/net/unix.
type secNet struct {
	NS, Err, Unix string
}

func (e *psEnv) secNet(t *testing.T, hdrs ...xbindtest.Header) secNet {
	t.Helper()
	e.who(t, "/api/"+secTile+"/who", hdrs...) // started (a cold start builds)
	var n secNet
	e.d.Must(t, "GET", "/api/"+secTile+"/net", nil, 200, hdrs...).Decode(t, &n)
	if !strings.HasPrefix(n.NS, "net:[") {
		t.Fatalf("the probe's network namespace: %+v", n)
	}
	return n
}

// secEvents is one /ws/events socket, recording every frame it gets.
type secEvents struct {
	name string
	mu   sync.Mutex
	got  []string
}

func (e *psEnv) secEvents(t *testing.T, name, query string, hdrs ...xbindtest.Header) *secEvents {
	t.Helper()
	c, r, err := e.d.Dial(t, "/ws/events"+query, hdrs...)
	if err != nil {
		t.Fatalf("%s's /ws/events: %v (%d %s)", name, err, r.Status, r)
	}
	s := &secEvents{name: name}
	t.Cleanup(func() { c.Close() })
	go func() {
		for {
			kind, msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			if kind == websocket.TextMessage {
				s.mu.Lock()
				s.got = append(s.got, string(msg))
				s.mu.Unlock()
			}
		}
	}()
	return s
}

// with reports the frames holding every one of frags.
func (s *secEvents) with(frags ...string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, m := range s.got {
		all := true
		for _, f := range frags {
			all = all && strings.Contains(m, f)
		}
		if all {
			out = append(out, m)
		}
	}
	return out
}

func TestPartitionsSecurity(t *testing.T) {
	t.Parallel()
	e := psSetup(t)
	d := e.d

	// alice's personal tile (a D88 personal plane tile providing mcp), the
	// security probe, and S1's pair for the consent case: apps/pt2 (user)
	// granted writer on apps/pt (user + global)
	w2Write(t, d, w2Mcp, fmt.Sprintf(`, "uses": [{"target": "res:%s/kv", "role": "writer"}, {"target": "res:%s/beat", "role": "writer"}]`, w2Mcp, w2Mcp)+
		`, "provides": {"mcp": {"kind": "http", "service": "mcp"}}`)
	d.Must(t, "POST", "/api/xbin/owner", map[string]string{"tile": w2Mcp, "to": "user:alice"}, 200)
	secWrite(t, d)
	psWrite(t, d, psTile, `["user", "global"]`)
	psWrite(t, d, psPeer, `["user"]`)
	d.Grant(t, psPeer, psTile, "writer")
	for _, tile := range []string{secTile, psTile, psPeer} {
		e.waitState(t, tile, "partitioned")
	}
	// the host network for the tile's net slot, bound before any instance
	// starts (a binding restarts every live instance): the global instance
	// gets it, people's partitions never do
	d.Bind(t, secTile, "net", "host")

	api := "/api/" + secTile
	e.addPerson(t, "frank", "user") // the lost-read case's: he loses read on the tile
	for _, p := range []string{"alice", "bob", "carol", "frank"} {
		e.put(t, api+"/kv/kv/secret", p+"-secret", e.fr(t, secTile, p))
	}
	e.put(t, api+"/kv/kv/secret", "global-secret")
	for _, p := range []string{"alice", "bob"} { // the events and terminal cases' sessions
		d.Must(t, "PUT", "/api/xbin/access", map[string]string{"tile": secTile, "kind": "user", "id": p, "level": "terminal"}, 200)
	}
	lostRead := secLostReadSetup(t, e) // checked a minute on, in lost-read

	t.Run("tokens", func(t *testing.T) {
		// bob holding alice's frame token: it is hers, bound to the session
		// that minted it — signing that session out ends it — and xbind's
		// MAC: a token bob alters authenticates nothing
		sess := d.Login(t, "alice", psPassword("alice"))
		var ft struct{ Token string }
		d.Must(t, "GET", "/api/xbin/frame-token?component="+secTile, nil, 200, xbindtest.H("Authorization", "Bearer "+sess)).Decode(t, &ft)
		fr := xbindtest.FrameHeader(ft.Token)
		secExpect(t, "alice's frame token, her session live", d.Call(t, "GET", api+"/kv/kv/secret", nil, fr), psForbid("alice", true), psOK("alice-secret"))
		n := len(ft.Token)
		tampered := ft.Token[:n-3] + map[bool]string{true: "A", false: "B"}[ft.Token[n-3] != 'A'] + ft.Token[n-2:]
		secExpect(t, "a tampered copy of alice's frame token", d.Call(t, "GET", api+"/kv/kv/secret", nil, xbindtest.FrameHeader(tampered)),
			psForbid("", false), psWant{401, "unauthorized", false})
		st, body, _ := psRaw(t, "POST", d.URL+"/logout", xbindtest.H("Authorization", "Bearer "+sess))
		t.Logf("alice signs the session out: %d %s", st, cut(body, 120))
		secExpect(t, "alice's frame token once its session is signed out", d.Call(t, "GET", api+"/kv/kv/secret", nil, fr),
			psForbid("", false), psWant{401, "unauthorized", false})
		secExpect(t, "alice's other session's frame meanwhile", d.Call(t, "GET", api+"/kv/kv/secret", nil, e.fr(t, secTile, "alice")),
			psForbid("alice", true), psOK("alice-secret"))
	})

	t.Run("read-only", func(t *testing.T) {
		// a "shared": "read" resource is where shared routing rows live:
		// global writes it, a person's partition only reads it — through
		// its backend's kv client and through the resource APIs with its
		// own token: a kv's put and delete, a blob's put and delete, a
		// bus's publish
		e.put(t, api+"/kv/pub/route", "from-global")
		d.Must(t, "PUT", "/api/xbin/blob/res:"+secTile+"/pubblob/route", "blob-from-global", 200)
		for _, p := range []string{"alice", "bob"} {
			secExpect(t, p+" reads the read-shared kv", d.Call(t, "GET", api+"/kv/pub/route", nil, e.fr(t, secTile, p)), nil, psOK("from-global"))
		}
		secExpect(t, "alice's backend writes the read-shared kv", d.Call(t, "PUT", api+"/kv/pub/route", "alice-route", e.fr(t, secTile, "alice")),
			nil, psWant{502, "is read-only for people's partitions", false})
		tok := xbindtest.H("Authorization", "Bearer "+e.who(t, api+"/who", e.fr(t, secTile, "alice")).Token)
		readOnly := func(res string) psWant {
			return psWant{403, "res:" + secTile + "/" + res + " is read-only for people's partitions", false}
		}
		if r := d.Call(t, "GET", "/api/xbin/blob/res:"+secTile+"/pubblob/route", nil, tok); r.Status != 200 || string(r.Body) != "blob-from-global" {
			t.Errorf("alice's instance token reads the read-shared blob (so the refusals below are the write's): %d %s", r.Status, r)
		}
		for _, a := range []struct {
			name, method, path string
			body               any
			res                string
		}{
			{"puts the read-shared kv", "PUT", "/api/xbin/kv/res:" + secTile + "/pub/route", "alice-route", "pub"},
			{"deletes the read-shared kv's key", "DELETE", "/api/xbin/kv/res:" + secTile + "/pub/route", nil, "pub"},
			{"puts the read-shared blob", "PUT", "/api/xbin/blob/res:" + secTile + "/pubblob/route", "alice-blob", "pubblob"},
			{"deletes the read-shared blob", "DELETE", "/api/xbin/blob/res:" + secTile + "/pubblob/route", nil, "pubblob"},
			{"publishes on the read-shared bus", "POST", "/api/xbin/bus/publish", map[string]string{"resource": "res:" + secTile + "/pubbus", "topic": "t", "data": "alice-route"}, "pubbus"},
		} {
			secExpect(t, "alice's instance token "+a.name, d.Call(t, a.method, a.path, a.body, tok), nil, readOnly(a.res))
		}
		secExpect(t, "global's kv value after the attempts", d.Call(t, "GET", api+"/kv/pub/route", nil), []string{"alice-route"}, psOK("from-global"))
		if r := d.Call(t, "GET", "/api/xbin/blob/res:"+secTile+"/pubblob/route", nil); r.Status != 200 || string(r.Body) != "blob-from-global" {
			t.Errorf("global's blob after the attempts: %d %s", r.Status, r)
		}
		// global publishes on it (the bus accepts a writer's publish)
		d.Must(t, "POST", "/api/xbin/bus/publish", map[string]string{"resource": "res:" + secTile + "/pubbus", "topic": "t", "data": "global-route"}, 200)
	})

	t.Run("events", func(t *testing.T) { secEventsCase(t, e) })     // partitions_security_cases_test.go
	t.Run("terminal", func(t *testing.T) { secTerminalCase(t, e) }) // partitions_security_cases_test.go

	t.Run("host-net", func(t *testing.T) {
		// 03 §A (the non-primary network rule), 10 §B.2: the tile's net slot
		// is bound to the host network; its global instance gets it, and
		// alice's partition — her backend compromised, say — gets a network
		// namespace of her own: not the host's, not bob's. The host's
		// loopback listener, bob's instance's and the host's abstract unix
		// socket are all out of her reach.
		self, err := os.Readlink("/proc/self/ns/net")
		if err != nil {
			t.Fatal(err)
		}
		glob := e.secNet(t)
		alice := e.secNet(t, e.fr(t, secTile, "alice"))
		bob := e.secNet(t, e.fr(t, secTile, "bob"))
		t.Logf("namespaces: the test %s, global %s, alice %s, bob %s", self, glob.NS, alice.NS, bob.NS)
		if glob.NS != self {
			t.Fatalf("the global instance under net → host runs in %s, not the host's %s: the checks below would pass vacuously", glob.NS, self)
		}
		if alice.NS == self || bob.NS == self {
			t.Errorf("BUG: a person's partition shares the host network: alice %s, bob %s (host %s)", alice.NS, bob.NS, self)
		}
		if alice.NS == bob.NS {
			t.Errorf("BUG: alice's and bob's partitions share a network namespace (%s)", alice.NS)
		}
		// the host's loopback listener and abstract socket
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				_, _ = c.Write([]byte("host-loopback-3f0a"))
				c.Close()
			}
		}()
		abs := fmt.Sprintf("xbin-i1-host-%d", os.Getpid())
		un, err := net.Listen("unix", "@"+abs)
		if err != nil {
			t.Fatal(err)
		}
		defer un.Close()
		dial := func(hdrs []xbindtest.Header, addr string) (string, string) {
			var out struct{ Read, Error string }
			d.Must(t, "GET", api+"/dial?addr="+addr, nil, 200, hdrs...).Decode(t, &out)
			return out.Read, out.Error
		}
		host := ln.Addr().String()
		if read, errs := dial(nil, host); read != "host-loopback-3f0a" {
			t.Errorf("the global instance (host network) dials the host's loopback: %q %q", read, errs)
		}
		if read, errs := dial([]xbindtest.Header{e.fr(t, secTile, "alice")}, host); read != "" || errs == "" {
			t.Errorf("BUG: alice's partition reaches the host's loopback listener: %q %q", read, errs)
		}
		if n := e.secNet(t); !strings.Contains(n.Unix, "@"+abs) {
			t.Errorf("the global instance doesn't see the host's abstract socket @%s (host network): the check below would pass vacuously", abs)
		}
		if n := e.secNet(t, e.fr(t, secTile, "alice")); strings.Contains(n.Unix, "@"+abs) {
			t.Errorf("BUG: alice's partition sees the host's abstract socket @%s", abs)
		}
		// bob's instance listens on its loopback and an abstract socket
		port := fmt.Sprint(40000 + os.Getpid()%20000)
		d.Must(t, "POST", api+"/listen?port="+port+"&name=xbin-i1-bob", nil, 200, e.fr(t, secTile, "bob"))
		if read, _ := dial([]xbindtest.Header{e.fr(t, secTile, "bob")}, "127.0.0.1:"+port); !strings.HasPrefix(read, "user:bob ") {
			t.Errorf("bob's own dial of his listener: %q (the check below would pass vacuously)", read)
		}
		if read, errs := dial([]xbindtest.Header{e.fr(t, secTile, "alice")}, "127.0.0.1:"+port); read != "" || errs == "" {
			t.Errorf("BUG: alice's partition reaches bob's instance's loopback listener: %q %q", read, errs)
		}
		if n := e.secNet(t, e.fr(t, secTile, "alice")); strings.Contains(n.Unix, "@xbin-i1-bob") {
			t.Errorf("BUG: alice's partition sees bob's abstract socket")
		}
		if n := e.secNet(t, e.fr(t, secTile, "bob")); !strings.Contains(n.Unix, "@xbin-i1-bob") {
			t.Errorf("bob's partition doesn't list its own abstract socket (the check above proves nothing): %s", cut(n.Unix, 400))
		}
	})

	t.Run("global-no-route", func(t *testing.T) {
		// the global instance's code reaches no person's partition: its
		// relay naming one is refused, and its calls to its own tile stay
		// global's
		call := func(path string) (int, string) {
			return secRelay(t, d.Call(t, "GET", api+"/call?path="+path, nil))
		}
		for _, q := range []string{"xbin-partition=user:alice", "xbin-partition=" + "user:bob"} {
			st, body := call(api + "/kv/kv/secret?" + q)
			if st != 400 || !strings.Contains(body, "its one value is global") || strings.Contains(body, "-secret") {
				t.Errorf("global's relay with ?%s: %d %s", q, st, cut(body, 200))
			}
		}
		// the kv API has no partition parameter: global's own value
		st, body := call("/api/xbin/kv/res:" + secTile + "/kv/secret%3Fpartition=user:alice")
		if st != 200 || body != "global-secret" {
			t.Errorf("global's kv API naming alice's partition: %d %s (want its own value)", st, cut(body, 200))
		}
		// the vault's, cron's and the logs' ?partition= are refused on a
		// partitioned tile (C22), whoever asks
		for _, p := range []string{"/api/xbin/vault/" + secTile, "/api/xbin/cron/jobs", "/api/xbin/logs?component=" + secTile} {
			sep := "%3F"
			if strings.Contains(p, "?") {
				p, sep = strings.Replace(p, "?", "%3F", 1), "%26"
			}
			st, body = call(p + sep + "partition=user:alice")
			t.Logf("global's %s ?partition=user:alice: %d %s", p, st, cut(body, 200))
			if st != 400 || strings.Contains(body, "alice-") {
				t.Errorf("global's %s with ?partition=user:alice: %d %s (want 400)", p, st, cut(body, 200))
			}
		}
		st, body = call(api + "/kv/kv/secret")
		if st != 200 || !strings.Contains(body, "global-secret") {
			t.Errorf("global's self-call: %d %s", st, cut(body, 200))
		}
	})

	t.Run("notify", func(t *testing.T) {
		// alice's partition can't push to anyone but alice (POST /notify);
		// the global instance notifies any reader
		push := func(hdrs []xbindtest.Header, to string) (int, string) {
			body := map[string]string{"user": to, "title": "hello from " + secTile}
			return secRelay(t, d.Call(t, "POST", api+"/callp?path=/api/xbin/notify", body, hdrs...))
		}
		alice := []xbindtest.Header{e.fr(t, secTile, "alice")}
		if st, body := push(alice, "bob"); st != 403 || !strings.Contains(body, "a person's partition notifies only its person (alice)") {
			t.Errorf("alice's partition notifies bob: %d %s", st, cut(body, 300))
		}
		if st, body := push(alice, "user:carol"); st != 403 {
			t.Errorf("alice's partition notifies carol: %d %s", st, cut(body, 300))
		}
		if st, body := push(alice, "alice"); st != 202 {
			t.Errorf("alice's partition notifies alice: %d %s", st, cut(body, 300))
		}
		if st, body := push(nil, "bob"); st != 202 {
			t.Errorf("the global instance notifies bob (a reader): %d %s", st, cut(body, 300))
		}
	})

	t.Run("mode-deciders", func(t *testing.T) {
		// a code writer — or the tile's own code — can't decide a mode: the
		// act is a tile manager's, in their own session (a dry run asks
		// nothing of anyone's data, so it is what each attempts)
		sw := map[string]any{"tile": secTile, "act": "switch", "from": psBoth, "to": nil, "dryRun": true}
		d.Must(t, "PUT", "/api/xbin/access", map[string]string{"tile": secTile, "kind": "user", "id": "dave", "level": "write"}, 200)
		tok := xbindtest.H("Authorization", "Bearer "+e.who(t, api+"/who", e.fr(t, secTile, "alice")).Token)
		globTok := xbindtest.H("Authorization", "Bearer "+e.who(t, api+"/who").Token)
		manager := "deciding " + secTile + "'s partition mode is a tile manager's act"
		for _, a := range []secAttempt{
			{"dave (a writer of the tile)", e.as("dave"), manager},
			{"alice's session (a reader)", e.as("alice"), manager},
			{"alice's instance token", []xbindtest.Header{tok}, secGlobalOnly("alice")},
			{"alice's frame", []xbindtest.Header{e.fr(t, secTile, "alice")}, secGlobalOnly("alice")},
			{"carol's (admin) frame", []xbindtest.Header{e.fr(t, secTile, "carol")}, secGlobalOnly("carol")},
			{"the global instance's token", []xbindtest.Header{globTok}, "no tile's backend, frame, terminal or agent decides"},
		} {
			secExpect(t, a.name+" decides the mode", d.Call(t, "POST", "/api/xbin/partitions/mode", sw, a.hdrs...), nil, psWant{403, a.refusal, false})
		}
		// alice's terminal on the tile (terminal level, with its API and
		// network): its token is the tile's credential acting for her
		d.Must(t, "PATCH", "/api/xbin/users/alice", map[string]bool{"termNet": true, "termApi": true}, 200)
		term := w2OpenTerm(t, d, secTile, e.as("alice")[0])
		body, _ := json.Marshal(sw)
		out, rc := term.run(t, `curl -s -X POST -H "Authorization: Bearer $XBIN_TOKEN" -H 'Content-Type: application/json' -d '`+string(body)+
			`' -w ' HTTP%{http_code}' "$XBIN_URL/api/xbin/partitions/mode"`, time.Minute)
		t.Logf("alice's terminal token decides the mode: exit %d %s", rc, cut(out, 300))
		if !strings.HasSuffix(out, " HTTP403") || !strings.Contains(out, secGlobalOnly("alice")) {
			t.Errorf("alice's terminal token decides the mode: exit %d %q, want 403 %q", rc, out, secGlobalOnly("alice"))
		}
		// the request doesn't exist here: a switch needs one pending. What
		// is checked is who is refused before that — a manager gets past it
		// to the tile's own answer
		secExpect(t, "carol's (admin) dry run with no request pending", e.mode(t, "carol", sw), nil,
			psWant{409, secTile + " has no partition mode switch request (it runs user + global)", false})
	})

	t.Run("bind-authority", func(t *testing.T) {
		// widening everyone's trust base: binding a shared provider into the
		// partitioned tile keeps today's bind authority — not a reader, not
		// a writer of the tile alone, not the tile's code
		body := map[string]string{"component": secTile, "slot": "mcp", "provider": w2Mcp}
		tok := xbindtest.H("Authorization", "Bearer "+e.who(t, api+"/who", e.fr(t, secTile, "alice")).Token)
		approver := "not approvable by you — bindings are wired by a workspace admin"
		for _, a := range []secAttempt{
			{"alice (a reader; the provider's owner)", e.as("alice"), approver},
			{"dave (a writer of the tile)", e.as("dave"), approver},
			{"alice's instance token", []xbindtest.Header{tok}, secGlobalOnly("alice")},
			{"alice's frame", []xbindtest.Header{e.fr(t, secTile, "alice")}, secGlobalOnly("alice")},
		} {
			secExpect(t, a.name+" binds a provider globally", d.Call(t, "POST", "/api/xbin/bindings", body, a.hdrs...), nil, psWant{403, a.refusal, false})
		}
		secExpect(t, "dave rebinds the tile's network", d.Call(t, "POST", "/api/xbin/bindings",
			map[string]string{"component": secTile, "slot": "net", "provider": "internet"}, e.as("dave")...), nil, psWant{403, approver, false})
	})

	t.Run("personal-bind", func(t *testing.T) {
		// a personal bind is made by the provider's owner for their own
		// partition only: bob, an admin and the tile's code can't bind
		// alice's tile, not even into their own partition
		body := map[string]string{"requester": secTile, "slot": "mcp", "provider": w2Mcp}
		tok := xbindtest.H("Authorization", "Bearer "+e.who(t, api+"/who", e.fr(t, secTile, "alice")).Token)
		for _, a := range []secAttempt{
			{"bob", e.as("bob"), w2Mcp + " isn't yours: a personal bind wires a tile you own personally into your own partition"},
			{"carol (admin)", e.as("carol"), "an admin's bind is always a global bind"},
			{"the root token", nil, secPersonOnly},
			{"alice's frame", []xbindtest.Header{e.fr(t, secTile, "alice")}, secPersonOnly},
			{"alice's instance token", []xbindtest.Header{tok}, secPersonOnly},
		} {
			secExpect(t, a.name+" binds alice's tile personally", d.Call(t, "POST", "/api/xbin/partitions/binds", body, a.hdrs...), nil, psWant{403, a.refusal, false})
		}
		// alice's own bind reaches her partition only
		d.Must(t, "POST", "/api/xbin/partitions/binds", body, 200, e.as("alice")...)
		call := api + "/call?path=/api/" + w2Mcp + "/who"
		xbindtest.Eventually(t, time.Minute, "alice's partition calls her personal tile (the refusals' control)", func() (bool, string) {
			w, st := relayWho(t, d.Call(t, "GET", call, nil, e.fr(t, secTile, "alice")))
			return st == 200 && w.Caller.From == secTile && w.Caller.Partition == "user:alice", fmt.Sprintf("%d %+v", st, w)
		})
		for name, hdrs := range map[string][]xbindtest.Header{"bob's partition": {e.fr(t, secTile, "bob")}, "the global instance": nil} {
			st, b := secRelay(t, d.Call(t, "GET", call, nil, hdrs...))
			if st != 403 || !strings.Contains(b, "not granted access to "+w2Mcp) {
				t.Errorf("%s calls alice's personal tile: %d %s", name, st, cut(b, 200))
			}
		}
		if r := d.Must(t, "GET", "/api/xbin/partitions/binds", nil, 200, e.as("bob")...); strings.Contains(r.String(), w2Mcp) {
			t.Errorf("BUG: bob's personal binds list alice's: %s", r)
		}
		// an admin lists the record — person, slot, provider — for hygiene
		// (PD-54, owner 2026-09-30), never what it reaches
		if r := d.Must(t, "GET", "/api/xbin/partitions/binds", nil, 200, e.as("carol")...); !strings.Contains(r.String(), `"user":"alice"`) ||
			!strings.Contains(r.String(), `"provider":"`+w2Mcp+`"`) || strings.Contains(r.String(), "-secret") {
			t.Errorf("carol's (admin) listing of personal binds: %s", r)
		}
	})

	t.Run("consent", func(t *testing.T) {
		// PD-13: with the workspace policy partitionConsent off (the
		// default) apps/pt2's grant on apps/pt carries each reader's data;
		// on, bob's is refused until he allows the edge in his own session,
		// and only there (not from the tiles' code); off again, it flows
		call := func(p string) xbindtest.Resp {
			return d.Call(t, "GET", "/api/"+psPeer+"/call?path=/api/"+psTile+"/kv/kv/secret", nil, e.fr(t, psPeer, p))
		}
		for _, p := range []string{"alice", "bob"} {
			e.put(t, "/api/"+psTile+"/kv/kv/secret", p+"-secret", e.fr(t, psTile, p))
			secExpect(t, p+"'s pt2 → pt, policy off", call(p), psForbid(p, true), psOK(p+"-secret"))
		}
		secExpect(t, "bob sets the policy", d.Call(t, "PUT", "/api/xbin/workspace-policies", map[string]bool{"partitionConsent": true}, e.as("bob")...),
			nil, psWant{403, "admin only", false})
		d.Must(t, "PUT", "/api/xbin/workspace-policies", map[string]bool{"partitionConsent": true}, 200, e.as("carol")...)
		refused := psWant{403, "bob hasn't let " + psPeer + " use their " + psTile + " data (they allow it at /xbin/partitions)", false}
		secExpect(t, "bob's pt2 → pt, policy on", call("bob"), psForbid("bob", false), refused)
		edge := map[string]string{"from": psPeer, "to": psTile}
		// the tiles' own credentials and the root token can't consent for
		// anyone; a person's consent is their own — an admin's (carol) and
		// alice's open their own edge, never bob's
		for name, hdrs := range map[string][]xbindtest.Header{
			"bob's pt2 frame": {e.fr(t, psPeer, "bob")},
			"bob's pt frame":  {e.fr(t, psTile, "bob")},
			"the root token":  nil,
		} {
			r := d.Call(t, "POST", "/api/xbin/partitions/consents", edge, hdrs...)
			t.Logf("%s allows the edge: %d %s", name, r.Status, cut(r.String(), 200))
			secExpect(t, name+" allows bob's edge", r, nil, psWant{403, secPersonOnly, false})
		}
		for _, p := range []string{"carol", "alice"} {
			if r := d.Call(t, "POST", "/api/xbin/partitions/consents", edge, e.as(p)...); r.Status != 200 || strings.Contains(r.String(), "bob") {
				t.Errorf("%s allows the edge for herself: %d %s", p, r.Status, r)
			}
		}
		secExpect(t, "bob's pt2 → pt after the others' attempts", call("bob"), psForbid("bob", false), refused)
		secExpect(t, "alice's pt2 → pt, she allowed it", call("alice"), psForbid("alice", true), psOK("alice-secret"))
		d.Must(t, "POST", "/api/xbin/partitions/consents", edge, 200, e.as("bob")...)
		secExpect(t, "bob's pt2 → pt, allowed", call("bob"), psForbid("bob", true), psOK("bob-secret"))
		d.Must(t, "DELETE", "/api/xbin/partitions/consents", edge, 200, e.as("bob")...)
		secExpect(t, "bob's pt2 → pt, taken back", call("bob"), psForbid("bob", false), refused)
		d.Must(t, "PUT", "/api/xbin/workspace-policies", map[string]bool{"partitionConsent": false}, 200, e.as("carol")...)
		secExpect(t, "bob's pt2 → pt, policy off again", call("bob"), psForbid("bob", true), psOK("bob-secret"))
	})

	t.Run("credential-reset", func(t *testing.T) { secCredentialCase(t, e) }) // partitions_security_cases_test.go
	t.Run("lost-read", func(t *testing.T) { secLostReadCase(t, e, lostRead) })
	// last: it leaves alice's instance of the tile crash-looping
	t.Run("crash", func(t *testing.T) { secCrashCase(t, e) })
}

// secAttempt is one actor's attempt: who, with what credential, and the
// words of the refusal they get.
type secAttempt struct {
	name    string
	hdrs    []xbindtest.Header
	refusal string
}

// secPersonOnly refuses a tile's credentials (and the root token) a
// person's own act.
const secPersonOnly = "this is a person's own act: sign in and do it yourself — a tile's credentials can't"

// secGlobalOnly refuses a route to a person's partition's credentials.
func secGlobalOnly(person string) string {
	return "this route is the global instance's alone: a person's partition (user:" + person + ") can't use it"
}

// secExpect is psExpect, logging the answer: the run's -v output is the
// record of each attempt and its refusal.
func secExpect(t *testing.T, name string, r xbindtest.Resp, forbid []string, wants ...psWant) {
	t.Helper()
	t.Logf("%s: %d %s", name, r.Status, cut(r.String(), 240))
	psExpect(t, name, r, forbid, wants...)
}

// secLogin is a sign-in attempt (POST /api/xbin/login, no credential).
func secLogin(t *testing.T, d *xbindtest.Daemon, body []byte) (int, string) {
	t.Helper()
	resp, err := http.Post(d.URL+"/api/xbin/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}
