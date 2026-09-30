//go:build integration

package test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/util"
)

// Partitioned tiles on an xbind WITHOUT --isolate (work pack I1 of
// plans/partitions/95-work-packs.md; 10 §A.2, 01 §2, PD-19, PD-44): the
// mode rule end to end — auto, pending (the 409, the in-frame page, the
// /alerts line an old shell renders), keep (the tile's data byte for byte),
// switch (refused towards user partitions, which need isolation; towards
// unpartitioned it deletes), a rollback onto code that asks differently,
// and a template instance — plus what a non-isolated xbind still serves of
// a partitioned tile: its global instance, for the root token, cron, the
// bus and ingress, while every person's partition is refused (fail
// closed). People's partitions themselves run only under --isolate; their
// suites are test/isolated's partitions_*_test.go.

const (
	pnAuto  = "apps/pn-auto"  // a probe partitioned from the start (["user", "global"])
	pnPlain = "apps/pn-plain" // an unpartitioned probe that holds data, then asks for partitions
	pnRB    = "apps/pn-rb"    // a static tile: partitioned by a deploy, then rolled back
	pnTpl   = "apps/pn-tpl"   // a workspace template whose block asks for partitions
	pnHost  = "pn.test"       // pnAuto's published host
	pnNote  = "pn fixture: each person's notes"
)

// pnSource is the probe (probe_test.go) that also says which partition it
// runs as (GET /env's XBIN_PARTITION), which one a call or a delivery
// named (X-XBin-Partition), and mails (POST /send) and subscribes (POST
// /sub) through the SDK.
var pnSource = strings.NewReplacer(
	`strings.HasPrefix(k, "XBIN_RES_") || k == "XBIN_DEPLOYMENT" ||`,
	`strings.HasPrefix(k, "XBIN_RES_") || k == "XBIN_DEPLOYMENT" || k == "XBIN_PARTITION" ||`,
	"\tData         string `json:\"data,omitempty\"`\n",
	"\tData         string `json:\"data,omitempty\"`\n\tPartition    string `json:\"partition,omitempty\"`\n",
	`Deployment: r.Header.Get("X-XBin-Deployment")}`,
	`Deployment: r.Header.Get("X-XBin-Deployment"), Partition: r.Header.Get("X-XBin-Partition")}`,
	`"deployment": r.Header.Get("X-XBin-Deployment"),`+"\n\t\t})",
	`"deployment": r.Header.Get("X-XBin-Deployment"), "partition": c.Partition, "partitionId": c.PartitionID,`+"\n\t\t})",
	"\txbin.Serve(mux)\n",
	`	mux.HandleFunc("POST /send", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		id, err := xbin.Mail(r.URL.Query().Get("to"), "dm", string(b))
		if err != nil {
			xbin.WriteError(w, 502, err.Error())
			return
		}
		xbin.WriteJSON(w, 200, map[string]string{"id": id})
	})
	mux.HandleFunc("POST /sub", func(w http.ResponseWriter, r *http.Request) {
		if err := xbin.Subscribe("pn", "res:"+xbin.Self()+"/bus", "", "/on"); err != nil {
			xbin.WriteError(w, 502, err.Error())
			return
		}
		xbin.WriteJSON(w, 200, map[string]bool{"ok": true})
	})
	xbin.Serve(mux)
`).Replace(probeSource)

// pnManifest is a probe's xbin.json: its kv, bus and cron at writer, a
// published path, and partition (raw JSON; "" = none).
func pnManifest(tile, partition string) string {
	res := func(name string) string { return `{"target":"res:` + tile + `/` + name + `","role":"writer"}` }
	m := `{"runtime":"go"`
	if partition != "" {
		m += `,"partition":` + partition
	}
	return m + `,"partitionNote":"` + pnNote + `","uses":[` + res("kv") + `,` + res("bus") + `,` + res("cron") + `],` +
		`"exposes":{"web":{"kind":"http","paths":["/v"]}}}` + "\n"
}

// pnWrite writes the probe at ws/tile answering marker, its manifest last.
func pnWrite(t *testing.T, ws, tile, marker, partition string) {
	t.Helper()
	dir := filepath.Join(ws, filepath.FromSlash(tile))
	for _, f := range []struct{ rel, content string }{
		{"go.mod", "module pn/" + strings.ReplaceAll(tile, "/", "_") + "\n\ngo 1.24\n\nrequire github.com/xbin-dev/xbin/sdk v0.0.0\n"},
		{"scope.json", `{"resources":{"kv":{"type":"kv"},"bus":{"type":"bus"},"cron":{"type":"cron"}}}` + "\n"},
		{"backend/main.go", strings.NewReplacer("__MARKER__", fmt.Sprintf("%q", marker), "__FILE__", fmt.Sprintf("%q", probeFile)).Replace(pnSource)},
		{probeFile, marker},
		{"index.html", "<!doctype html><html><head></head><body>" + marker + "</body></html>\n"},
		{"xbin.json", pnManifest(tile, partition)},
	} {
		writeIfChanged(t, filepath.Join(dir, filepath.FromSlash(f.rel)), f.content)
	}
}

// pnEnv is the daemon (auth on, not isolated, an ingress listener), its
// workspace, and its people's sessions: alice and bob read apps/*, carol
// is a workspace admin.
type pnEnv struct {
	a       dlAPI
	ws      string
	ingress string
	people  map[string]userClient
}

func startPartitionsPlain(t *testing.T) *pnEnv {
	t.Helper()
	ws := filepath.Join(t.TempDir(), "ws")
	addr, ing := isoFreeAddr(t), isoFreeAddr(t)
	cmd := exec.Command(xbindBin, "--workspace", ws, "--listen", addr, "--insecure-vault", "--ingress-listen", ing)
	cmd.Env = append(os.Environ(), "XBIN_SDK_PATH="+filepath.Join(repo, "sdk"))
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	startDaemon(t, cmd, ws)
	e := &pnEnv{a: dlAPI{url: "http://" + addr}, ws: ws, ingress: ing, people: map[string]userClient{}}
	if !waitFor(func() bool { c, _ := e.a.do("GET", "/healthz", ""); return c == 200 }, 15*time.Second) {
		t.Fatalf("the auth-on xbind never became healthy:\n%s", out.String())
	}
	tok, err := os.ReadFile(filepath.Join(ws, ".xbin", "token"))
	if err != nil {
		t.Fatal(err)
	}
	e.a.token = strings.TrimSpace(string(tok))
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("xbind:\n%s", isoTailString(out.String(), 80))
		}
	})
	for id, role := range map[string]string{"alice": "user", "bob": "user", "carol": "admin"} {
		tiles := `{"apps/*":"read"}`
		if role == "admin" {
			tiles = `{}`
		}
		if c, b := e.a.do("POST", "/api/xbin/users", `{"id":"`+id+`","role":"`+role+`","tiles":`+tiles+`,"password":"pw-`+id+`-4d1"}`); c != 200 {
			t.Fatalf("adding %s: %d %s", id, c, b)
		}
		e.people[id] = loginAs(t, e.a, id, "pw-"+id+"-4d1")
	}
	return e
}

// call sends one request with exactly hdrs (no root token), a JSON or raw
// body, and returns the status, body and headers.
func (e *pnEnv) call(t *testing.T, method, path, body string, hdrs ...string) (int, string, http.Header) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	rq, err := http.NewRequest(method, e.a.url+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(hdrs); i += 2 {
		rq.Header.Set(hdrs[i], hdrs[i+1])
	}
	r, err := (&http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(rq)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, string(b), r.Header
}

// root is the root token's header pair; frame is person's frame token of
// tile as a header pair.
func (e *pnEnv) root() []string { return []string{"Authorization", "Bearer " + e.a.token} }

func (e *pnEnv) frame(t *testing.T, tile, person string) []string {
	t.Helper()
	c, b := e.people[person].get("/api/xbin/frame-token?component=" + tile)
	var ft struct{ Token string }
	if c != 200 || json.Unmarshal([]byte(b), &ft) != nil || ft.Token == "" {
		t.Fatalf("%s's frame token of %s: %d %s", person, tile, c, b)
	}
	return []string{"X-XBin-Frame-Token", ft.Token}
}

// pnRow is a components row's partition entry.
type pnRow struct {
	State   string `json:"state"`
	User    bool   `json:"user"`
	Global  bool   `json:"global"`
	Request *struct {
		User, Global, Declined bool
	} `json:"request"`
}

// waitState waits for tile's partition entry to be in state ("" = no
// entry, or unpartitioned asking nothing).
func (e *pnEnv) waitState(t *testing.T, tile, state string) *pnRow {
	t.Helper()
	var row *pnRow
	var last string
	if !waitFor(func() bool {
		c, b := e.a.do("GET", "/api/xbin/components/"+tile, "")
		var out struct{ Component struct{ Partition *pnRow } }
		if c != 200 || json.Unmarshal([]byte(b), &out) != nil {
			last = fmt.Sprint(c, " ", b)
			return false
		}
		row, last = out.Component.Partition, b
		switch {
		case row == nil:
			return state == ""
		case state == "":
			return row.State == "unpartitioned" && !row.User && !row.Global && row.Request == nil
		}
		return row.State == state
	}, 30*time.Second) {
		t.Fatalf("%s's partition state never became %q: %s", tile, state, firstN(last, 600))
	}
	return row
}

// modeOps is tile's mode.json history, oldest first ("" when it has none).
func (e *pnEnv) modeOps(t *testing.T, tile string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.ws, "data", "partitions", util.TileKey(tile), "mode.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var m struct{ History []struct{ Op string } }
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s's mode.json: %v", tile, err)
	}
	var ops []string
	for _, h := range m.History {
		ops = append(ops, h.Op)
	}
	return ops
}

// mode is POST /partitions/mode as person.
func (e *pnEnv) mode(t *testing.T, person string, body map[string]any) (int, string) {
	t.Helper()
	b, _ := json.Marshal(body)
	return e.people[person].do("POST", "/api/xbin/partitions/mode", string(b))
}

// dataOf is tile's data as the root token reads it, key by key and byte
// for byte: every key of its kv resource and its value, and every vault
// key and its value (main's kv buckets share data/kv.db with every other
// scope's, so the file itself isn't the tile's to compare).
func (e *pnEnv) dataOf(t *testing.T, tile string) map[string]string {
	t.Helper()
	out := map[string]string{}
	var kv struct{ Keys []string }
	c, b := e.a.do("GET", "/api/xbin/kv/res:"+tile+"/kv/?prefix=", "")
	if c != 200 || json.Unmarshal([]byte(b), &kv) != nil {
		t.Fatalf("%s's kv keys: %d %s", tile, c, b)
	}
	for _, k := range kv.Keys {
		c, b := e.a.do("GET", "/api/xbin/kv/res:"+tile+"/kv/"+k, "")
		out["kv/"+k] = fmt.Sprint(c, " ", b)
	}
	var vault struct{ Keys []string }
	c, b = e.a.do("GET", "/api/xbin/vault/"+tile, "")
	if c != 200 || json.Unmarshal([]byte(b), &vault) != nil {
		t.Fatalf("%s's vault keys: %d %s", tile, c, b)
	}
	for _, k := range vault.Keys {
		c, b := e.a.do("GET", "/api/xbin/vault/"+tile+"/"+k, "")
		out["vault/"+k] = fmt.Sprint(c, " ", b)
	}
	return out
}

// covers PD-44 PD-19 PD-52 10§A.2 01§2 — the mode rule and a partitioned
// tile's global instance on an xbind without --isolate.
func TestPartitionsNoIsolate(t *testing.T) {
	t.Parallel()
	for _, s := range []string{`k == "XBIN_PARTITION"`, "Partition    string `json:", `Partition: r.Header.Get("X-XBin-Partition")`,
		`"partitionId": c.PartitionID`, `"POST /send"`} {
		if !strings.Contains(pnSource, s) {
			t.Fatalf("pnSource lacks %s: probeSource changed under its replacements", s)
		}
	}
	e := startPartitionsPlain(t)
	pnWrite(t, e.ws, pnAuto, "auto-v1", `["user","global"]`)
	pnWrite(t, e.ws, pnPlain, "plain-v1", "")
	// the published host, bound before any instance starts
	e.a.waitTile(t, pnAuto)
	if c, b := e.a.do("POST", "/api/xbin/bindings", `{"component":"`+pnAuto+`","slot":"web","provider":"runtime","host":"`+pnHost+`"}`); c != 200 {
		t.Fatalf("binding %s's host: %d %s", pnAuto, c, b)
	}

	t.Run("auto", func(t *testing.T) {
		// 01 §2.3: a tile that holds no data takes its manifest's mode at
		// once, isolation or not (the record decides routing)
		row := e.waitState(t, pnAuto, "partitioned")
		if !row.User || !row.Global || row.Request != nil {
			t.Errorf("the recorded mode: %+v", *row)
		}
		if ops := e.modeOps(t, pnAuto); len(ops) != 1 || ops[0] != "auto" {
			t.Errorf("mode.json's history: %q", ops)
		}
		waitProbeA(t, e.a, pnAuto, "auto-v1")
	})

	t.Run("global-only", func(t *testing.T) {
		// G3/PD-19: without --isolate no person's partition runs — a
		// person's frame (a reader's, an admin's) is refused, never served
		// by the global instance; the root token reaches global, which
		// runs as today's instance
		c, b, _ := e.call(t, "GET", "/api/"+pnAuto+"/env", "", e.root()...)
		if c != 200 || !strings.Contains(b, `"XBIN_PARTITION":"global"`) {
			t.Errorf("the root token's call: %d %s (want the global instance)", c, b)
		}
		if c, b := e.a.do("PUT", "/api/"+pnAuto+"/kv/k", "global-only-7a"); c != 200 {
			t.Fatalf("global's kv write: %d %s", c, b)
		}
		for _, p := range []string{"alice", "carol"} {
			c, b, _ := e.call(t, "GET", "/api/"+pnAuto+"/kv/k", "", e.frame(t, pnAuto, p)...)
			t.Logf("%s's frame without --isolate: %d %s", p, c, firstN(b, 300))
			if c == 200 || strings.Contains(b, "global-only-7a") || !strings.Contains(b, "runs only in a sandbox (--isolate)") {
				t.Errorf("%s's frame without --isolate: %d %s (want a refusal naming --isolate)", p, c, firstN(b, 300))
			}
		}
		// the document still names the person's partition; its API refuses
		if c, b := e.people["alice"].get("/c/" + pnAuto + "/"); c != 200 || !strings.Contains(b, `<meta name="xbin-partition" content="user:alice">`) {
			t.Errorf("alice's document: %d %s", c, firstN(b, 400))
		}
	})

	t.Run("global-sources", func(t *testing.T) {
		// 02 §3, 05 §5: what doesn't act for a person — the root token's
		// cron job, the bus, a published host — reaches the global instance;
		// global mails a person, which waits: it starts nothing
		if c, b, _ := e.call(t, "POST", "/api/"+pnAuto+"/sub", "", e.root()...); c != 200 {
			t.Fatalf("global subscribes: %d %s", c, b)
		}
		if c, b := e.a.do("POST", "/api/xbin/bus/publish", `{"resource":"res:`+pnAuto+`/bus","topic":"t","data":"from-root-3c"}`); c != 200 {
			t.Fatalf("publishing: %d %s", c, b)
		}
		if c, b := e.a.do("PUT", "/api/xbin/cron/jobs", `{"name":"tick","resource":"res:`+pnAuto+`/cron","schedule":"@every 1s","path":"/tick","role":"writer","component":"`+pnAuto+`"}`); c != 200 {
			t.Fatalf("the root token's cron job: %d %s", c, b)
		}
		var seen []struct{ Path, From, Topic, Data, Partition string }
		if !waitFor(func() bool {
			c, b := e.a.do("GET", "/api/"+pnAuto+"/seen", "")
			seen = nil
			if c != 200 || json.Unmarshal([]byte(b), &seen) != nil {
				return false
			}
			bus, tick := false, false
			for _, s := range seen {
				bus = bus || s.Path == "/on"
				tick = tick || s.Path == "/tick"
			}
			return bus && tick
		}, 150*time.Second) {
			t.Fatalf("the bus event and the cron tick never reached global: %+v", seen)
		}
		for _, s := range seen {
			if s.Partition != "" && s.Partition != "global" {
				t.Errorf("a delivery to the global instance named partition %q: %+v", s.Partition, s)
			}
			if s.Path == "/on" && (s.From != "xbin/bus" || !strings.Contains(s.Data, "from-root-3c")) || s.Path == "/tick" && s.From != "xbin/cron" {
				t.Errorf("global's delivery: %+v", s)
			}
		}
		t.Logf("global's deliveries: %+v", seen)
		// ingress
		var pub string
		if !waitFor(func() bool {
			rq, _ := http.NewRequest("GET", "http://"+e.ingress+"/v", nil)
			rq.Host = pnHost
			r, err := http.DefaultClient.Do(rq)
			if err != nil {
				return false
			}
			defer r.Body.Close()
			b, _ := io.ReadAll(r.Body)
			pub = fmt.Sprint(r.StatusCode, " ", string(b))
			return r.StatusCode == 200
		}, time.Minute) || pub != "200 auto-v1" {
			t.Errorf("ingress to %s: %s", pnHost, pub)
		}
		// mail: global → alice is kept; her partition can't start here
		if c, b, _ := e.call(t, "POST", "/api/"+pnAuto+"/send?to=user:alice", "for-alice-9e", e.root()...); c != 200 {
			t.Errorf("global mails alice: %d %s", c, b)
		}
		time.Sleep(2 * time.Second)
		if c, b := e.a.do("GET", "/api/xbin/sandboxes", ""); c != 200 || strings.Contains(b, "user:alice") {
			t.Errorf("the sandbox list after the mail: %d %s", c, firstN(b, 400))
		}
	})

	t.Run("pending", func(t *testing.T) {
		// 01 §2.4: an unpartitioned tile that holds data asks for
		// partitions: a request, not a change — nothing of its primary runs
		waitProbeA(t, e.a, pnPlain, "plain-v1")
		for _, w := range [][2]string{{"/api/" + pnPlain + "/kv/k", "plain-data-5b"}, {"/api/" + pnPlain + "/kv/k2", "plain-data-\u00e9\n"}} {
			if c, b := e.a.do("PUT", w[0], w[1]); c != 200 {
				t.Fatalf("the tile's data: %d %s", c, b)
			}
		}
		if c, b := e.a.do("PUT", "/api/xbin/vault/"+pnPlain+"/token", `{"value":"plain-vault-8c"}`); c/100 != 2 {
			t.Fatalf("the tile's vault key: %d %s", c, b)
		}
		before := e.dataOf(t, pnPlain)
		if len(before) != 3 {
			t.Fatalf("%s's data: %v (want two kv keys and a vault key)", pnPlain, before)
		}
		pnWrite(t, e.ws, pnPlain, "plain-v1", `["user","global"]`)
		row := e.waitState(t, pnPlain, "pending")
		if row.User || row.Global || row.Request == nil || !row.Request.User || !row.Request.Global || row.Request.Declined {
			t.Errorf("the pending row: %+v %+v", *row, row.Request)
		}
		if ops := e.modeOps(t, pnPlain); len(ops) != 1 || ops[0] != "request" {
			t.Errorf("mode.json's history: %q", ops)
		}
		c, b, _ := e.call(t, "GET", "/api/"+pnPlain+"/v", "", e.root()...)
		if c != 409 || !strings.Contains(b, pnPlain+" is paused: a partition mode switch is requested (unpartitioned → user + global)") {
			t.Errorf("the root token's call while pending: %d %s", c, b)
		}
		c, b, _ = e.call(t, "GET", "/api/"+pnPlain+"/v", "", e.frame(t, pnPlain, "alice")...)
		if c != 409 || !strings.Contains(b, "partition mode switch is requested") {
			t.Errorf("alice's frame while pending: %d %s", c, b)
		}
		rq, _ := http.NewRequest("GET", e.a.url+"/c/"+pnPlain+"/", nil)
		rq.Header.Set("Sec-Fetch-Dest", "iframe")
		rq.AddCookie(e.people["alice"].cookie)
		if r, err := http.DefaultClient.Do(rq); err != nil {
			t.Fatal(err)
		} else {
			page, _ := io.ReadAll(r.Body)
			r.Body.Close()
			if r.StatusCode != 409 || !bytes.Contains(page, []byte("All data in this tile will be deleted")) || !bytes.Contains(page, []byte(pnNote)) {
				t.Errorf("the in-frame switch page: %d %s", r.StatusCode, firstN(string(page), 600))
			}
		}
		// the /alerts line every shell renders (an old one shows level and
		// message: workspace-template/shell/bx-shell.js of v0.3.64)
		c, b = e.people["alice"].get("/api/xbin/alerts")
		var alerts struct {
			Alerts []struct{ Kind, Level, Message, Tile string }
		}
		_ = json.Unmarshal([]byte(b), &alerts)
		found := false
		for _, al := range alerts.Alerts {
			if al.Kind == "partition-switch" && al.Tile == pnPlain {
				found = al.Level != "" && strings.Contains(al.Message, pnPlain)
			}
		}
		if c != 200 || !found {
			t.Errorf("alice's alerts while pending: %d %s", c, b)
		}
		// a switch towards user partitions needs isolation: refused, and
		// nothing is deleted
		sw := map[string]any{"tile": pnPlain, "act": "switch", "from": nil, "to": map[string]bool{"user": true, "global": true}, "confirm": pnPlain}
		if c, b := e.mode(t, "carol", sw); c != 409 || !strings.Contains(b, "user partitions need isolation: run xbind with --isolate before switching "+pnPlain) {
			t.Errorf("carol switches without --isolate: %d %s", c, b)
		}
		keep := map[string]any{"tile": pnPlain, "act": "keep", "from": nil, "to": map[string]bool{"user": true, "global": true}}
		if c, b := e.mode(t, "bob", keep); c != 403 || !strings.Contains(b, "tile manager's act") {
			t.Errorf("bob (a reader) keeps: %d %s", c, b)
		}
		// the tile's own credentials never decide (the root token, the
		// owner's, does: owner is admin)
		if c, b, _ := e.call(t, "POST", "/api/xbin/partitions/mode", mustJSON(keep), append(e.frame(t, pnPlain, "carol"), "Content-Type", "application/json")...); c != 403 ||
			!strings.Contains(b, "no tile's backend, frame, terminal or agent decides") {
			t.Errorf("carol's (admin) frame of the tile keeps: %d %s", c, b)
		}
		// keep: the recorded mode runs again on its data, byte for byte
		if c, b := e.mode(t, "carol", keep); c != 200 {
			t.Fatalf("carol keeps: %d %s", c, b)
		}
		waitProbeA(t, e.a, pnPlain, "plain-v1")
		if c, b := e.a.do("GET", "/api/"+pnPlain+"/kv/k", ""); c != 200 || b != "plain-data-5b" {
			t.Errorf("the kept tile's data: %d %s", c, b)
		}
		if after := e.dataOf(t, pnPlain); fmt.Sprint(after) != fmt.Sprint(before) {
			t.Errorf("keep changed %s's data:\n%v\nwas\n%v", pnPlain, after, before)
		}
		c, b, _ = e.call(t, "GET", "/api/"+pnPlain+"/env", "", e.frame(t, pnPlain, "alice")...)
		if c != 200 || strings.Contains(b, "XBIN_PARTITION") {
			t.Errorf("alice after keep: %d %s (want the unpartitioned instance)", c, b)
		}
		if ops := e.modeOps(t, pnPlain); len(ops) != 2 || ops[1] != "keep" {
			t.Errorf("mode.json's history after keep: %q", ops)
		}
	})

	t.Run("switch", func(t *testing.T) {
		// 01 §2.5: pnAuto (partitioned, global's data) drops partition: a
		// request; the switch deletes global's data after the typed path
		pnWrite(t, e.ws, pnAuto, "auto-v1", "")
		e.waitState(t, pnAuto, "pending")
		sw := map[string]any{"tile": pnAuto, "act": "switch", "from": map[string]bool{"user": true, "global": true}, "to": nil}
		if c, b := e.mode(t, "carol", sw); c != 400 || !strings.Contains(b, pnAuto) {
			t.Errorf("a switch without the typed path: %d %s", c, b)
		}
		sw["confirm"] = pnAuto
		if c, b := e.mode(t, "carol", sw); c != 200 {
			t.Fatalf("carol switches: %d %s", c, b)
		}
		e.waitState(t, pnAuto, "")
		waitProbeA(t, e.a, pnAuto, "auto-v1")
		if c, b := e.a.do("GET", "/api/"+pnAuto+"/kv/k", ""); c != 404 {
			t.Errorf("global's data after the switch: %d %s", c, b)
		}
		if ops := e.modeOps(t, pnAuto); len(ops) < 3 || ops[len(ops)-1] != "switch" {
			t.Errorf("mode.json's history after the switch: %q", ops)
		}
	})

	t.Run("rollback", func(t *testing.T) {
		// 01 §2.4 (PD-44): a rollback onto code that asks differently is the
		// same request. A static tile (pins need no isolation): c1 asks
		// nothing; c2, deployed while it held nothing, recorded partitions;
		// with data, a rollback to c1 is pending until a manager decides.
		dir := filepath.Join(e.ws, filepath.FromSlash(pnRB))
		for _, f := range [][2]string{
			{"scope.json", `{"resources":{"kv":{"type":"kv"}}}` + "\n"},
			{"index.html", "<!doctype html><title>rb</title><p>rb-v1</p>\n"},
			{probeFile, "rb-v1"},
			{"xbin.json", `{"title":"rb","uses":[{"target":"res:` + pnRB + `/kv","role":"writer"}]}` + "\n"},
		} {
			writeIfChanged(t, filepath.Join(dir, f[0]), f[1])
		}
		e.a.waitServed(t, pnRB, "static", "rb-v1", 30*time.Second)
		if _, en := e.a.op(t, "live-reload/pause", pnRB); en.Result != "ok" {
			t.Fatalf("the pause: %+v", en)
		}
		c1 := e.a.state(t, pnRB).pinned("main")
		writeIfChanged(t, filepath.Join(dir, probeFile), "rb-v2")
		writeIfChanged(t, filepath.Join(dir, "xbin.json"), `{"title":"rb","partition":["user","global"],"uses":[{"target":"res:`+pnRB+`/kv","role":"writer"}]}`+"\n")
		time.Sleep(2 * time.Second) // the watcher's batch
		if _, en := e.a.op(t, "live-reload/now", pnRB); en.Result != "ok" {
			t.Fatalf("reload now: %+v", en)
		}
		e.waitState(t, pnRB, "partitioned")
		if c, b := e.a.do("PUT", "/api/xbin/kv/res:"+pnRB+"/kv/k", "rb-data-1f"); c/100 != 2 {
			t.Fatalf("global's data: %d %s", c, b)
		}
		if _, en := e.a.op(t, "rollback", pnRB, "deployment", "main", "checkpoint", c1); en.Result != "ok" {
			t.Fatalf("the rollback to c1: %+v", en)
		}
		row := e.waitState(t, pnRB, "pending")
		if !row.User || !row.Global || row.Request == nil || row.Request.User || row.Request.Global {
			t.Errorf("the rolled-back tile's row: %+v %+v", *row, row.Request)
		}
		keep := map[string]any{"tile": pnRB, "act": "keep", "from": map[string]bool{"user": true, "global": true}, "to": nil}
		if c, b := e.mode(t, "carol", keep); c != 200 {
			t.Fatalf("carol keeps: %d %s", c, b)
		}
		row = e.waitState(t, pnRB, "partitioned")
		if row.Request == nil || !row.Request.Declined {
			t.Errorf("the kept row: %+v %+v", *row, row.Request)
		}
		if c, b := e.a.do("GET", "/api/xbin/kv/res:"+pnRB+"/kv/k", ""); c != 200 || !strings.Contains(b, "rb-data-1f") {
			t.Errorf("the tile's data after keep: %d %s", c, b)
		}
	})

	t.Run("template", func(t *testing.T) {
		// 01 §2.7 (PD-19, PD-35): a template's default mode is written into
		// a new instance only under --isolate; here the answer says why not,
		// and the instance asks for nothing (no mode record)
		dir := filepath.Join(e.ws, filepath.FromSlash(pnTpl))
		writeIfChanged(t, filepath.Join(dir, "index.html"), "<!doctype html><title>tpl</title>\n")
		writeIfChanged(t, filepath.Join(dir, "xbin.json"), `{"title":"pn template","template":{"title":"pn","partition":["user","global"]}}`+"\n")
		e.a.waitTile(t, pnTpl)
		for _, c := range []struct{ path, body, why string }{
			{"apps/pn-inst", `{"source":"` + pnTpl + `","path":"apps/pn-inst"}`, "needs --isolate"},
			{"apps/pn-out", `{"source":"` + pnTpl + `","path":"apps/pn-out","partition":false}`, "opted out"},
		} {
			code, b := e.a.do("POST", "/api/xbin/templates/new", c.body)
			var ans struct {
				Path             string
				Partition        any
				PartitionSkipped string
			}
			if code != 200 || json.Unmarshal([]byte(b), &ans) != nil || ans.Path != c.path || ans.Partition != nil || ans.PartitionSkipped != c.why {
				t.Errorf("instantiating %s: %d %s (want partitionSkipped %q)", c.path, code, b, c.why)
			}
			m, err := os.ReadFile(filepath.Join(e.ws, filepath.FromSlash(c.path), "xbin.json"))
			if err != nil || bytes.Contains(m, []byte(`"partition"`)) || bytes.Contains(m, []byte(`"template"`)) {
				t.Errorf("%s's manifest: %v %s", c.path, err, m)
			}
			e.a.waitTile(t, c.path)
			e.waitState(t, c.path, "")
			if ops := e.modeOps(t, c.path); ops != nil {
				t.Errorf("%s has a mode record: %q", c.path, ops)
			}
		}
	})
}

// waitProbeA is waitProbe for a dlAPI: tile answers marker on GET /v.
func waitProbeA(t *testing.T, a dlAPI, tile, marker string) {
	t.Helper()
	var code int
	var body string
	waitFor(func() bool {
		code, body = a.do("GET", "/api/"+tile+"/v", "")
		return code == 200 && body == marker || strings.Contains(body, "build failed")
	}, 3*time.Minute)
	if code != 200 || body != marker {
		t.Fatalf("probe %s never served %q: %d %s", tile, marker, code, body)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
