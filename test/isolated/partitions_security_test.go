//go:build linux && integration

package isolated

// partitions_security_test.go — the partitioned tiles' security suite
// (plans/partitions/10 §B.2, the threat model; work pack I1), on a real
// `xbind --isolate` with owner auth on, with S1's fixture and helpers
// (partitions_smoke_fx_test.go). Each case is one actor's explicit attempt
// on one path the table lists, with its exact expected answer: the status
// and the refusal's words, and that no other person's data comes back.
//
// The table's rows and where each is attempted — this file's subtests
// (TestPartitionsSecurity/<name>), or the suite that already makes the same
// attempt, named so that nothing is tried twice:
//
//	bob → alice's data through /api/<tile>/…        TestPartitionsSmoke/bob-never-sees-alice
//	bob forges X-XBin-Partition/-Id/-User           TestPartitionsSmoke/bob-never-sees-alice, TestPartitionsSmokeW2/global-address
//	bob holds alice's frame token                   tokens (a signed-out session's and a tampered token: 401)
//	bob's /ws/events: bus, session, status          events (and admins': no blanket pass)
//	bob's terminal: /tmp leftovers of alice's       terminal (her layer; sessions start in $HOME)
//	a writer changes code to exfiltrate             residual (PD-23): the trust panel, not an xbind refusal
//	admin: API, view-as, logs, vault, mail          TestPartitionsSmoke/admin, /view-as, /logs; TestPartitionsSmokeW2/vault; TestPartitionsSmokeMail/outsiders
//	admin: reattach, drive, rename (kill allowed)   terminal
//	admin: backups                                  TestPartitionsSmokeBackups (sealed: ciphertext at the archiver)
//	admin: a reset link / password for alice        credential-reset (credentialResetConfirm on: held)
//	admin or root token forges mail to alice        TestPartitionsSmokeMail/outsiders
//	admin: Z uses X, mail wakes Z                   TestPartitionsSmokeMail/never-run-restart (mail starts no partition); consent
//	admin: restore alice's archive into bob         TestPartitionsSmokeBackups (bob, alice's frame, bob's partition id: 403)
//	another (unpartitioned) tile → the tile         TestPartitionsSmoke/global
//	bob's partition of Z → X                        TestPartitionsSmoke/cross-tile; consent (policy on)
//	a partition's code: F5 to global                TestPartitionsSmokeW2/global-address (attributed)
//	a partition's code: the bot's replies anywhere  TestPartitionsAgentChannels/forged-outbox
//	a partition's code: shared routing rows         read-only ("read" resources refuse writes)
//	a partition's code: a private empty trigger     TestPartitionsAgentChannels/private-webhook-trigger; the agent template's trigger tests
//	members widen a hosted conversation             B2d (not built in this wave)
//	the global instance's code → a person           global-no-route
//	alice's compromised backend: mounts, loopback,  TestPartitionsSmoke/data-apart (mounts); host-net (loopback,
//	  abstract sockets, host network                  abstract sockets, host network under a net → host bind)
//	alice's partition spams people                  notify (clamped to alice); TestPartitionsSmokeMail/person-to-global (mail)
//	a code writer flips partition / rolls back      TestPartitionsSmoke/mode-keep, /mode-pending-unpartitioned; test/partitions_test.go
//	a code writer switches the mode                 mode-deciders (the tile's own credentials, a writer: 403)
//	a tile manager switches                         TestPartitionsSmoke/mode-switch (typed, recorded)
//	a builtin update / template merge               test/partitions_test.go; TestPartitionsTemplateMerge
//	no bind authority: a shared provider            bind-authority
//	bob's partition / global → alice's personal     personal-bind (making one: 403) and TestPartitionsSmokeW2/personal-bind (calls: 403)
//	an archiver reads backups                       TestPartitionsSmokeBackups (XBINSEAL, no plaintext)
//	an old backup of a deleted person               TestPartitionsSmokeBackups (erased key: unrestorable)
//	a live partition token after a mode change      TestPartitionsSmoke/token-revocation, /mode-switch
//	a lost users file                               test/downgrade_test.go (uid re-adopted)
//	deleted alice, recreated                        TestPartitionsSmoke/person-recreated
//	a person who lost read                          TestPartitionsSmoke/token-revocation, /cross-tile
//	a provider keyed on From                        TestCodingSandboxContract (user-partitions through xbind)
//	a crash in alice's partition                    events (partition-scoped status)
//
//	set -a; eval "$(sed -n 's/^export \([A-Z_]*\) := \(.*\)$/\1=\2/p' .dev.mk | grep -v ^PATH)"; set +a
//	go test -tags=integration -count=1 -v -run '^TestPartitionsSecurity$' ./test/isolated/
//
// Like the smoke, it needs user namespaces, a base rootfs and gocryptfs: run
// it with the Bash sandbox off on a dev box; CI skips it.

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

const secTile = "apps/psec" // ["user", "global"]: kv, a cron, a bus; net and a multi mcp slot

// secRoutes are the security probe's routes beside the wave-2 probe's: a
// POST relay through xbin.Client, its network namespace and the abstract
// unix sockets it sees, a TCP dial, a loopback listener and an abstract
// socket of its own, a bus publish and a status report.
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
`

// secSource is the wave-2 probe with the security routes.
var secSource = strings.Replace(strings.Replace(w2Source, "\t\"encoding/json\"\n", "\t\"encoding/json\"\n\t\"fmt\"\n\t\"net\"\n\t\"time\"\n", 1),
	"\txbin.Serve(mux)\n", secRoutes+"\txbin.Serve(mux)\n", 1)

// secWrite writes the security probe at secTile, its manifest last.
func secWrite(t *testing.T, d *xbindtest.Daemon) {
	t.Helper()
	files := w2Files(secTile, `, "partition": ["user", "global"], "uses": [`+
		`{"target": "res:`+secTile+`/kv", "role": "writer"}, {"target": "res:`+secTile+`/beat", "role": "writer"}, `+
		`{"target": "res:`+secTile+`/bus", "role": "writer"}, {"target": "res:`+secTile+`/pub", "role": "writer"}], `+
		`"interfaces": {"net": {"kind": "net"}, "mcp": {"kind": "http", "service": "mcp", "multi": true}}`)
	files["backend/main.go"] = secSource
	files["scope.json"] = `{"resources": {"kv": {"type": "kv"}, "beat": {"type": "cron"}, "bus": {"type": "bus"}, "pub": {"type": "kv", "shared": "read"}}}` + "\n"
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
	for _, p := range []string{"alice", "bob", "carol"} {
		e.put(t, api+"/kv/kv/secret", p+"-secret", e.fr(t, secTile, p))
	}
	e.put(t, api+"/kv/kv/secret", "global-secret")

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
		// its backend's kv client and through the kv API with its own token
		e.put(t, api+"/kv/pub/route", "from-global")
		for _, p := range []string{"alice", "bob"} {
			secExpect(t, p+" reads the read-shared kv", d.Call(t, "GET", api+"/kv/pub/route", nil, e.fr(t, secTile, p)), nil, psOK("from-global"))
		}
		secExpect(t, "alice's backend writes the read-shared kv", d.Call(t, "PUT", api+"/kv/pub/route", "alice-route", e.fr(t, secTile, "alice")),
			nil, psWant{502, "is read-only for people's partitions", false})
		tok := xbindtest.H("Authorization", "Bearer "+e.who(t, api+"/who", e.fr(t, secTile, "alice")).Token)
		secExpect(t, "alice's instance token writes the read-shared kv through the API",
			d.Call(t, "PUT", "/api/xbin/kv/res:"+secTile+"/pub/route", "alice-route", tok), nil, psWant{403, "res:" + secTile + "/pub is read-only for people's partitions", false})
		secExpect(t, "global's value after the attempts", d.Call(t, "GET", api+"/kv/pub/route", nil), []string{"alice-route"}, psOK("from-global"))
	})

	t.Run("events", func(t *testing.T) {
		// 10 §B.2 (bob's /ws/events; admins lose the blanket pass): an
		// event of alice's partition — a bus message in its data, its
		// status, her terminal's term events — reaches her own sockets and
		// the tile's credentials acting in her partition, never bob's, an
		// admin's (carol) or the root token's
		aliceFrame := e.secEvents(t, "alice's frame", "?frame="+e.fr(t, secTile, "alice").V)
		aliceSess := e.secEvents(t, "alice's session", "", e.as("alice")...)
		others := []*secEvents{
			e.secEvents(t, "bob's frame", "?frame="+e.fr(t, secTile, "bob").V),
			e.secEvents(t, "bob's session", "", e.as("bob")...),
			e.secEvents(t, "carol's (admin) session", "", e.as("carol")...),
			e.secEvents(t, "carol's frame", "?frame="+e.fr(t, secTile, "carol").V),
			e.secEvents(t, "the root token", ""),
		}
		time.Sleep(500 * time.Millisecond) // the sockets are subscribed
		d.Must(t, "POST", api+"/publish?topic=t", "alice-evt-2c9d", 200, e.fr(t, secTile, "alice"))
		d.Must(t, "POST", api+"/publish?topic=t", "bob-evt-7e11", 200, e.fr(t, secTile, "bob"))
		d.Must(t, "POST", api+"/status", "alice-status-51fa", 200, e.fr(t, secTile, "alice"))
		xbindtest.Eventually(t, 20*time.Second, "alice's frame gets her partition's bus event", func() (bool, string) {
			got := aliceFrame.with("alice-evt-2c9d")
			return len(got) == 1 && strings.Contains(got[0], `"partition":"user:alice"`), fmt.Sprint(got)
		})
		xbindtest.Eventually(t, 20*time.Second, "alice's session gets her partition's status", func() (bool, string) {
			got := aliceSess.with("alice-status-51fa")
			return len(got) == 1 && strings.Contains(got[0], `"partition":"user:alice"`), fmt.Sprint(got)
		})
		if got := aliceFrame.with("bob-evt-7e11"); len(got) != 0 {
			t.Errorf("BUG: alice's frame got bob's partition's bus event: %q", got)
		}
		time.Sleep(2 * time.Second) // what reaches alice's reaches the others' by now
		for _, s := range others {
			for _, f := range []string{"alice-evt-2c9d", "alice-status-51fa", `"partition":"user:alice"`} {
				if got := s.with(f); len(got) != 0 {
					t.Errorf("BUG: %s got alice's partition's event (%s): %q", s.name, f, got)
				}
			}
		}
		if got := others[0].with("bob-evt-7e11"); len(got) != 1 {
			t.Errorf("bob's frame didn't get his own partition's bus event (so the check above proves nothing): %q", got)
		}
	})

	t.Run("terminal", func(t *testing.T) {
		// PD-22, PD-09: alice's shell leaves a file in /tmp; bob's shell on
		// the same tile has a layer of his own. carol, an admin, may end
		// alice's session but neither reattach to it nor rename it.
		for _, p := range []string{"alice", "bob"} {
			d.Must(t, "PUT", "/api/xbin/access", map[string]string{"tile": secTile, "kind": "user", "id": p, "level": "terminal"}, 200)
		}
		a := w2OpenTerm(t, d, secTile, e.as("alice")[0])
		if out, rc := a.run(t, `echo alice-left > /tmp/alice-left-9b2e && cd && pwd && ls /tmp`, time.Minute); rc != 0 || !strings.Contains(out, "alice-left-9b2e") {
			t.Fatalf("alice's shell: exit %d %q", rc, out)
		}
		b := w2OpenTerm(t, d, secTile, e.as("bob")[0])
		out, rc := b.run(t, `ls -a /tmp; cat /tmp/alice-left-9b2e 2>&1; printf 'pwd=%s' "$PWD"`, time.Minute)
		if strings.Contains(out, "alice-left") && !strings.Contains(out, "No such file") {
			t.Errorf("BUG: bob's shell on %s sees alice's /tmp leftovers: exit %d %q", secTile, rc, out)
		}
		if !strings.Contains(out, "No such file") {
			t.Errorf("bob's shell: %q (want cat's No such file)", out)
		}
		if strings.Contains(out, "pwd=/"+secTile) || strings.HasSuffix(out, "pwd=") {
			t.Errorf("bob's shell starts in %q, want his $HOME (the tile directory is shared code)", out)
		}
		var rows []struct{ ID, Cwd string }
		d.Must(t, "GET", "/api/xbin/term/sessions", nil, 200, e.as("alice")...).Decode(t, &rows)
		id := ""
		for _, r := range rows { // her own listing: her sessions
			if r.Cwd == secTile {
				id = r.ID
			}
		}
		if id == "" {
			t.Fatalf("alice's session isn't listed to her: %+v", rows)
		}
		refusal := "session belongs to another user: " + secTile + " keeps each person's data apart"
		for name, cred := range map[string]xbindtest.Header{"carol (admin)": e.as("carol")[0], "bob": e.as("bob")[0]} {
			c, r, err := d.Dial(t, "/ws/term?session="+id, cred)
			if err == nil {
				c.Close()
				t.Errorf("BUG: %s reattached to alice's session", name)
			} else if r.Status != 403 {
				t.Errorf("%s reattaching to alice's session: %d %s (%v)", name, r.Status, r, err)
			}
			t.Logf("%s reattaching: %d %s", name, r.Status, cut(r.String(), 200))
		}
		secExpect(t, "carol renames alice's session", d.Call(t, "PATCH", "/api/xbin/term/sessions/"+id, map[string]string{"name": "carol's"}, e.as("carol")...),
			nil, psWant{403, "session belongs to another user", false})
		var listed []struct{ ID, Name string }
		d.Must(t, "GET", "/api/xbin/term/sessions?all=1", nil, 200, e.as("carol")...).Decode(t, &listed)
		t.Logf("carol's listing: %+v (refusal words: %q)", listed, refusal)
		secExpect(t, "bob ends alice's session", d.Call(t, "DELETE", "/api/xbin/term/sessions/"+id, nil, e.as("bob")...), nil, psWant{403, "session belongs to another user", false})
		if r := d.Call(t, "DELETE", "/api/xbin/term/sessions/"+id, nil, e.as("carol")...); r.Status/100 != 2 {
			t.Errorf("carol (admin) ends alice's session (governance keeps kill): %d %s", r.Status, r)
		}
	})

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
		// the request doesn't exist here: a switch needs one pending. What
		// is checked is who is refused before that — a manager gets past it
		r := e.mode(t, "carol", sw)
		t.Logf("carol's (admin) dry run with no request pending: %d %s", r.Status, cut(r.String(), 300))
		if r.Status == 403 {
			t.Errorf("carol (a tile manager) is refused like the others: %d %s", r.Status, r)
		}
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

	t.Run("credential-reset", func(t *testing.T) {
		// G2 remaining path 2: an admin can take over a person's account by
		// resetting its credentials; with credentialResetConfirm on, a reset
		// link or a new password for someone who holds partitions is held
		// (the old password keeps working) until they allow it
		d.Must(t, "PUT", "/api/xbin/workspace-policies", map[string]bool{"credentialResetConfirm": true}, 200, e.as("carol")...)
		defer d.Must(t, "PUT", "/api/xbin/workspace-policies", map[string]bool{"credentialResetConfirm": false}, 200, e.as("carol")...)
		var inv struct {
			Held      bool   `json:"held"`
			HeldUntil string `json:"heldUntil"`
			InviteURL string `json:"inviteUrl"`
		}
		d.Must(t, "POST", "/api/xbin/users/alice/invite", nil, 200, e.as("carol")...).Decode(t, &inv)
		if !inv.Held || inv.HeldUntil == "" {
			t.Errorf("carol's reset link for alice isn't held: %+v", inv)
		}
		r := d.Call(t, "PATCH", "/api/xbin/users/alice", map[string]string{"password": "carol-knows-it-1"}, e.as("carol")...)
		if r.Status != 200 || r.Header.Get("X-XBin-Credential-Held") != "password" {
			t.Errorf("carol sets alice's password: %d held %q %s", r.Status, r.Header.Get("X-XBin-Credential-Held"), r)
		}
		b, _ := json.Marshal(map[string]string{"username": "alice", "password": "carol-knows-it-1"})
		if st, body := secLogin(t, d, b); st == 200 {
			t.Errorf("BUG: carol's held password signs in as alice: %d %s", st, cut(body, 200))
		}
		e.sess["alice"] = d.Login(t, "alice", psPassword("alice")) // her own still works
		e.forget("alice")
		// bob, a user, mints nothing for alice
		secExpect(t, "bob mints alice a reset link", d.Call(t, "POST", "/api/xbin/users/alice/invite", nil, e.as("bob")...), nil, psWant{403, "invites are minted by admins", false})
	})
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
