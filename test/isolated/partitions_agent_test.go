//go:build linux && integration

package isolated

// partitions_agent_test.go — the agent template partitioned (work pack B2a of
// the partitioned-tiles plan; the template's API.md "Partitioned
// instances"), end to end on a real `xbind --isolate` with owner auth — the
// partitions smoke's daemon and people (psSetup: alice, bob, dave read
// apps/*; carol and erin are admins).
//
//   - A new instance of the builtin template is partitioned by default
//     ("template": {"partition": [...]} written at instantiation, the mode
//     recorded at once); one made with the opt-out is not, and keeps running
//     unpartitioned — every instance made before the default is that one.
//   - Its model comes through llm-gw from hack/fakeopenai. The owner token
//     reaches the global instance, which keeps the settings and mirrors them
//     into `conf`; alice's and bob's partitions read the model from there.
//   - alice and bob each chat privately: each sees only their own
//     conversations and session files; admins (carol's own partition, the
//     owner token at global) see neither's; a person's conversation ids
//     start at 2^40, global's at 1.
//   - alice's partition never has the global instance's db (or files)
//     mounted: its mount table (read from /proc on the host) shows her
//     partition's volumes and the shared `team`, nothing of global's own
//     data and nothing of bob's.
//   - Partition mail through the agent's /mailbox (W3b): with a builder's
//     two topics compiled in (paMailTopics), alice's frame mails global a
//     ping, the global instance mails her partition a note, and her
//     partition handles it once; both inboxes end acked. The owner token's
//     frame reads (and acks) global's inbox but sends nothing; a topic the
//     code doesn't know waits there while it is young.
//   - Sandboxes: two copies of hack/fakesandbox are bound — one offering the
//     `partitions` capability, one from before it. alice's partition uses
//     only the first (the old one is refused with refusal "partitions",
//     naming it); a sandbox she makes is homed in her partition, labelled
//     xbin.agent/home with her partition id, and bob's partition and the
//     global instance never list it; the global instance keeps using the
//     old manager.
//
// It runs on a dev box only (user namespaces, a rootfs, gocryptfs; CI builds
// no rootfs, so xbindtest.Require skips it):
//
//	set -a; eval "$(sed -n 's/^export \([A-Z_]*\) := \(.*\)$/\1=\2/p' .dev.mk | grep -v ^PATH)"; set +a
//	go test -tags=integration -count=1 -v -run '^TestPartitionsAgent$' ./test/isolated/

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/jsonc"
	"github.com/xbin-dev/xbin/internal/util"
	"github.com/xbin-dev/xbin/test/xbindtest"
)

const (
	paAgent = "apps/agent"       // the template's new instance: partitioned by default
	paPlain = "apps/agent-plain" // made with the opt-out: an unpartitioned instance, as every older one
	paGW    = "apps/llm-gw"
	paNew   = "apps/sbx-new" // hack/fakesandbox, offering `partitions`
	paOld   = "apps/sbx-old" // …and a copy from before it
	pa2to40 = int64(1) << 40
)

// paSandboxTile writes a copy of hack/fakesandbox as tile (every sandbox a
// directory in its `boxes` resource), the old one without `partitions`.
func paSandboxTile(t *testing.T, d *xbindtest.Daemon, tile string, partitions bool) {
	t.Helper()
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(d.A.Repo, "hack", "fakesandbox", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	main := read("main.go")
	if !partitions {
		const at = `m := &fsbManager{Root: *root, DefaultFrom: *from}`
		if !strings.Contains(main, at) {
			t.Fatalf("hack/fakesandbox/main.go no longer makes its manager with %q", at)
		}
		main = strings.Replace(main, at, `m := &fsbManager{Root: *root, DefaultFrom: *from, Caps: []string{"exec", "files", "tar", "snapshots", "clone", "archive", "ports"}}`, 1)
	}
	files := map[string]string{
		"go.mod":          "module fsb_" + strings.ReplaceAll(tile, "/", "_") + "\n\ngo 1.24\n\nrequire github.com/xbin-dev/xbin/sdk v0.0.0\n",
		"backend/fsb.go":  read("fsb.go"),
		"backend/main.go": main,
		"scope.json":      `{"resources": {"boxes": {"type": "filesystem"}}}` + "\n",
		"index.html":      "<!doctype html><title>fake sandboxes</title>\n",
	}
	if err := d.WriteFiles(tile, files); err != nil {
		t.Fatal(err)
	}
	d.WriteTile(t, tile, map[string]string{"xbin.json": `{"runtime": "go",
  "uses": [{"target": "res:` + tile + `/boxes", "role": "writer"}],
  "provides": {"sandboxes": {"kind": "http", "service": "sandbox-manager", "role": "consumer"}},
  "expose": {"roles": {"consumer": "Use this tile's sandboxes"}}}` + "\n"})
}

// paMailTopics is what a builder adds to the instance's mailbox
// (_backend/mailbox.go's mailHandlers): at the global instance a person's
// e2e/ping is answered by mailing them an e2e/note; in a person's partition
// an e2e/note becomes a conversation of theirs titled with the item's id and
// data — so each handled item shows, once, in their list.
const paMailTopics = `package main

import (
	"context"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func init() {
	mailHandlers["e2e/ping"] = func(_ context.Context, _ *DB, it mailItem) error {
		_, err := xbin.Mail(it.From, "e2e/note", it.Data)
		return err
	}
	mailHandlers["e2e/note"] = func(_ context.Context, t *DB, it mailItem) error {
		_, err := t.createRunStamped("mail "+it.ID+" "+string(it.Data), "{}", 0, statusIdle,
			runStamp{Owner: runUser, Visibility: visPrivate, TitleSrc: "user"})
		return err
	}
}
`

// paRun is GET /runs/{id} as far as the test reads it.
type paRun struct {
	Run struct {
		ID            int64
		Status, Owner string
	}
	Messages []struct{ Role, Content string }
}

func TestPartitionsAgent(t *testing.T) {
	t.Parallel()
	e := psSetup(t)
	d := e.d
	fake := startFakeOpenAI(t, d)
	ok := func(method, path string, body any, hdrs ...xbindtest.Header) xbindtest.Resp {
		t.Helper()
		r := d.Call(t, method, path, body, hdrs...)
		if r.Status/100 != 2 {
			t.Fatalf("%s %s: %d %s", method, path, r.Status, r)
		}
		return r
	}
	paSandboxTile(t, d, paNew, true)
	paSandboxTile(t, d, paOld, false)

	ok("POST", "/api/xbin/builtins/import", map[string]string{"name": "llm-gw", "path": paGW})
	d.WaitComponent(t, paGW)
	d.Bind(t, paGW, "net", "host")
	ok("PUT", "/api/xbin/vault/"+paGW+"/api-token-fake", map[string]string{"value": "sk-fake"})

	// a new instance of the template: partitioned by default
	var inst struct {
		Partition        []string
		PartitionSkipped string
	}
	ok("POST", "/api/xbin/templates/new", map[string]string{"source": "agent", "path": paAgent}).Decode(t, &inst)
	if strings.Join(inst.Partition, ",") != "user,global" || inst.PartitionSkipped != "" {
		t.Fatalf("instantiating the agent: partition %v, skipped %q", inst.Partition, inst.PartitionSkipped)
	}
	// the builder's mail topics, before anything builds it (the mailbox case)
	if err := d.WriteFiles(paAgent, map[string]string{"_backend/zz_e2e_mail.go": paMailTopics}); err != nil {
		t.Fatal(err)
	}
	// …and an opted-out one: unpartitioned, like every instance made before
	inst.Partition, inst.PartitionSkipped = nil, ""
	ok("POST", "/api/xbin/templates/new", map[string]any{"source": "agent", "path": paPlain, "partition": false}).Decode(t, &inst)
	if len(inst.Partition) != 0 || inst.PartitionSkipped == "" {
		t.Fatalf("the opt-out: partition %v, skipped %q", inst.Partition, inst.PartitionSkipped)
	}
	for _, tile := range []string{paAgent, paPlain} {
		d.WaitComponent(t, tile)
		ok("POST", "/api/xbin/bindings", map[string]any{"component": tile, "slot": "llm", "providers": []string{paGW}})
		d.Bind(t, tile, "net", "none")
	}
	ok("POST", "/api/xbin/bindings", map[string]any{"component": paAgent, "slot": "sandboxes", "providers": []string{paNew, paOld}})

	t.Run("modes", func(t *testing.T) {
		row := e.waitState(t, paAgent, "partitioned")
		if !row.User || !row.Global || row.Request != nil {
			t.Errorf("the new instance's recorded mode: %+v", *row)
		}
		m, has := e.modeRecord(t, paAgent)
		if !has || len(m.History) == 0 || m.History[0].Op != "auto" {
			t.Errorf("the new instance's mode record: %v %+v", has, m)
		}
		b, _ := os.ReadFile(filepath.Join(d.WS, paAgent, "xbin.json"))
		if _, block, _ := jsonc.TopLevel(b, "template"); !strings.Contains(string(b), `"partition"`) || block { // the template's comments stay (T1)
			t.Errorf("the new instance's manifest carries its own partition and no template block: %s", cut(string(b), 400))
		}
		e.waitState(t, paPlain, "")
		b, _ = os.ReadFile(filepath.Join(d.WS, paPlain, "xbin.json"))
		if strings.Contains(string(b), `"partition"`) {
			t.Errorf("the opted-out instance asks for a partition: %s", cut(string(b), 400))
		}
	})

	xbindtest.Eventually(t, 5*time.Minute, "llm-gw's backend answers", func() (bool, string) {
		r := d.Call(t, "GET", "/api/"+paGW+"/config", nil)
		return r.Status == 200 && strings.Contains(string(r.Body), "backends"), fmt.Sprint(r.Status, " ", r)
	})
	ok("PUT", "/api/"+paGW+"/config/backend", map[string]string{"name": "fake", "baseURL": "http://" + fake})
	// the owner token reaches the global instance, which keeps the settings
	// (and mirrors them into conf for people's partitions)
	for _, tile := range []string{paAgent, paPlain} {
		var cfg map[string]any
		xbindtest.Eventually(t, 8*time.Minute, tile+"'s backend answers", func() (bool, string) {
			r := d.Call(t, "GET", "/api/"+tile+"/config", nil)
			cfg = nil
			if r.Status == 200 {
				_ = json.Unmarshal(r.Body, &cfg)
			}
			return cfg["system"] != nil, fmt.Sprint(r.Status, " ", r)
		})
		cfg["model"] = "fake/fake-chat"
		ok("PUT", "/api/"+tile+"/config", cfg)
	}

	// ag calls the agent (tile) as person through their frame ("" = the owner token).
	ag := func(tile, person, method, path string, body any) xbindtest.Resp {
		t.Helper()
		var hdrs []xbindtest.Header
		if person != "" {
			hdrs = append(hdrs, e.fr(t, tile, person))
		}
		return d.Call(t, method, "/api/"+tile+path, body, hdrs...)
	}
	answer := func(tile, person string, id int64, want string) paRun {
		t.Helper()
		var r paRun
		xbindtest.Eventually(t, 3*time.Minute, fmt.Sprintf("%s's run %d on %s answers %q", person, id, tile, want), func() (bool, string) {
			resp := ag(tile, person, "GET", fmt.Sprintf("/runs/%d", id), nil)
			r = paRun{}
			if resp.Status == 200 {
				_ = json.Unmarshal(resp.Body, &r)
			}
			for _, m := range r.Messages {
				if m.Role == "assistant" && strings.Contains(m.Content, want) && r.Run.Status != "running" {
					return true, ""
				}
			}
			return false, fmt.Sprint(resp.Status, " ", r.Run.Status, " ", cut(string(resp.Body), 300))
		})
		return r
	}
	ask := func(tile, person, text string) int64 {
		t.Helper()
		var run struct{ ID int64 }
		r := ag(tile, person, "POST", "/ask", map[string]string{"text": text})
		if r.Status/100 != 2 {
			t.Fatalf("%s asks %s: %d %s", person, tile, r.Status, r)
		}
		r.Decode(t, &run)
		answer(tile, person, run.ID, "Hello from the fake model.")
		return run.ID
	}
	convs := func(tile, person, scope string) (ids []int64, body string) {
		t.Helper()
		r := ag(tile, person, "GET", "/conversations?scope="+scope, nil)
		if r.Status != 200 {
			t.Fatalf("%s's conversations on %s: %d %s", person, tile, r.Status, r)
		}
		var out struct{ Pinned, Items []struct{ ID int64 } }
		r.Decode(t, &out)
		for _, it := range append(out.Pinned, out.Items...) {
			ids = append(ids, it.ID)
		}
		slices.Sort(ids)
		return ids, string(r.Body)
	}
	ids := map[string]int64{}
	pkeys := map[string]string{}

	t.Run("private-chats", func(t *testing.T) {
		ids["global"] = ask(paAgent, "", "hello from global-secret")
		ids["alice"] = ask(paAgent, "alice", "hello from alice-secret")
		ids["bob"] = ask(paAgent, "bob", "hello from bob-secret")
		if ids["global"] >= pa2to40 || ids["alice"] < pa2to40 || ids["bob"] < pa2to40 {
			t.Errorf("ids: global %d (want < 2^40), alice %d and bob %d (want ≥ 2^40)", ids["global"], ids["alice"], ids["bob"])
		}
		for _, p := range []string{"alice", "bob", "carol"} {
			pkeys[p] = util.PartitionKey(p, e.uid(t, p))
			var me struct{ Kind, User, Partition string }
			ag(paAgent, p, "GET", "/me", nil).Decode(t, &me)
			if me.Kind != "user" || me.User != p || me.Partition != "user:"+p {
				t.Errorf("%s's /me: %+v", p, me)
			}
		}
		var me struct{ Partition string }
		ag(paAgent, "", "GET", "/me", nil).Decode(t, &me)
		if me.Partition != "global" {
			t.Errorf("the owner token reached %q, want the global instance", me.Partition)
		}
		// each sees their own conversation only; admins see neither person's
		for _, c := range []struct {
			person string
			want   []int64
		}{
			{"alice", []int64{ids["alice"]}},
			{"bob", []int64{ids["bob"]}},
			{"carol", nil},               // an admin reaches their own partition
			{"", []int64{ids["global"]}}, // the owner token: the global instance
		} {
			got, body := convs(paAgent, c.person, "mine")
			if !slices.Equal(got, c.want) {
				t.Errorf("%q's conversations: %v, want %v", c.person, got, c.want)
			}
			own := c.person
			if own == "" {
				own = "global"
			}
			for _, p := range []string{"alice", "bob", "global"} {
				if p != own && strings.Contains(body, p+"-secret") {
					t.Errorf("BUG: %q's list holds %s's conversation: %s", c.person, p, cut(body, 400))
				}
			}
		}
		// ids collide across homes by design: each partition's own is itself
		var r paRun
		ag(paAgent, "alice", "GET", fmt.Sprintf("/runs/%d", pa2to40), nil).Decode(t, &r)
		if msgs := fmt.Sprint(r.Messages); !strings.Contains(msgs, "alice-secret") || strings.Contains(msgs, "bob-secret") {
			t.Errorf("alice's run %d: %v", pa2to40, r.Messages)
		}
		for _, c := range []struct {
			person string
			id     int64
		}{{"alice", ids["global"]}, {"carol", pa2to40}, {"", pa2to40}} {
			if st := ag(paAgent, c.person, "GET", fmt.Sprintf("/runs/%d", c.id), nil).Status; st != 404 {
				t.Errorf("%q reading run %d: %d, want 404 (not in their db)", c.person, c.id, st)
			}
		}
		// session files stay in their partition
		w := ag(paAgent, "alice", "PUT", fmt.Sprintf("/runs/%d/file", ids["alice"]), map[string]string{"path": "notes.md", "content": "alice-secret-file"})
		if w.Status != 200 {
			t.Fatalf("alice writes a file: %d %s", w.Status, w)
		}
		if r := ag(paAgent, "alice", "GET", fmt.Sprintf("/runs/%d/file?path=notes.md", ids["alice"]), nil); r.Status != 200 || !strings.Contains(string(r.Body), "alice-secret-file") {
			t.Errorf("alice reads her file: %d %s", r.Status, r)
		}
		for _, p := range []string{"bob", "carol", ""} {
			r := ag(paAgent, p, "GET", fmt.Sprintf("/runs/%d/file?path=notes.md", pa2to40), nil)
			if strings.Contains(string(r.Body), "alice-secret-file") {
				t.Errorf("BUG: %q reads alice's file: %d %s", p, r.Status, r)
			}
		}
		// the tile-wide settings are global's: a reader's own partition may
		// not change them, and the model came from conf (the answers above)
		if st := ag(paAgent, "alice", "PUT", "/config", map[string]string{"model": "x"}).Status; st != 403 {
			t.Errorf("alice (read) changing the settings from her partition: %d", st)
		}
		if st := ag(paAgent, "alice", "POST", fmt.Sprintf("/runs/%d/members", ids["alice"]), map[string]string{"user": "bob", "role": "viewer"}).Status; st != 409 {
			t.Errorf("sharing from a person's partition: %d, want 409", st)
		}
	})

	t.Run("global-db-not-mounted", func(t *testing.T) {
		// 08 §2: no person's partition mounts global's db. From the host:
		// the mount table of alice's backend — found by its mounts, not its
		// environment (mountinfo is world-readable, environ isn't under a
		// multi-uid user namespace) — classifies every mount of the tile's
		// data: hers, the shared team, and nothing else. This is an
		// acceptance check: it fails, never skips, when it can't look.
		const scope = "apps~agent"
		canonDB := filepath.Join(d.WS, ".xbin", "resenc", scope, "db")
		var pid string
		var mounts []paMount
		xbindtest.Eventually(t, 2*time.Minute, "alice's partition backend's mount table", func() (bool, string) {
			ag(paAgent, "alice", "GET", "/me", nil) // running, whatever the idle stop did meanwhile
			pid, mounts = paPartitionMounts(canonDB, pkeys["alice"])
			return pid != "", "no process has alice's volume at " + canonDB
		})
		var mine, shared []string
		for _, m := range mounts {
			where := m.root + " " + m.point + " " + m.source
			if !strings.Contains(where, d.WS+"/data") && !strings.Contains(where, d.WS+"/.xbin/res") {
				continue // not the workspace's data (the rootfs, the tile's source, /proc…)
			}
			switch {
			case strings.Contains(where, "/.partitions/"+scope+"/") && strings.Contains(where, "/"+pkeys["alice"]+"/"):
				mine = append(mine, m.line)
			case strings.Contains(where, "/.partitions/"):
				t.Errorf("BUG: alice's partition mounts a partition volume that isn't hers: %s", m.line)
			case strings.HasSuffix(m.point, "/"+scope+"/team") && strings.Contains(m.source, "/"+scope+"/team"):
				shared = append(shared, m.line)
			default:
				t.Errorf("BUG: alice's partition mounts data that isn't hers or the shared team (global's own?): %s", m.line)
			}
		}
		t.Logf("alice's backend %s — her volumes: %q; shared: %q", pid, mine, shared)
		if len(mine) == 0 || len(shared) == 0 {
			t.Errorf("alice's mount table lacks her db volume (%d) or the shared team (%d)", len(mine), len(shared))
		}
		// …and, where the host may look inside her namespace (a single-uid
		// user namespace), the file at the canonical path is her db
		if b, err := os.ReadFile("/proc/" + pid + "/root" + canonDB + "/db.sqlite"); err == nil {
			if !strings.Contains(string(b), "alice-secret") || strings.Contains(string(b), "global-secret") || strings.Contains(string(b), "bob-secret") {
				t.Errorf("BUG: the db alice's backend sees at %s isn't hers alone", canonDB)
			}
		} else {
			t.Logf("alice's db file from the host: %v (the mount table above is the check)", err)
		}
	})

	t.Run("mailbox", func(t *testing.T) {
		// Partition mail through the agent's own /mailbox (W3b): alice's
		// frame mails global an e2e/ping; xbind rings the global instance,
		// whose handler mails her partition an e2e/note; her doorbell rings
		// and her handler makes one conversation of it — once, whatever
		// rings or starts come after — and both inboxes end empty (acked).
		const mailAPI = "/api/xbin/partitions/mail"
		nonce := fmt.Sprintf("ping-%d", time.Now().UnixNano())
		d.Must(t, "POST", mailAPI, map[string]any{"to": "global", "topic": "e2e/ping", "data": nonce}, 200, e.fr(t, paAgent, "alice"))
		notes := func() []string {
			_, body := convs(paAgent, "alice", "mine")
			var list struct{ Pinned, Items []struct{ Title string } }
			_ = json.Unmarshal([]byte(body), &list)
			var hits []string
			for _, it := range append(list.Pinned, list.Items...) {
				if strings.HasPrefix(it.Title, "mail ") && strings.Contains(it.Title, nonce) {
					hits = append(hits, it.Title)
				}
			}
			return hits
		}
		xbindtest.Eventually(t, 4*time.Minute, "global's note reaches alice's partition", func() (bool, string) {
			n := notes()
			return len(n) > 0, fmt.Sprint(n)
		})
		inbox := func(hdr xbindtest.Header) []struct{ ID, From, Topic string } {
			var pg struct {
				Items []struct{ ID, From, Topic string }
			}
			d.Must(t, "GET", mailAPI, nil, 200, hdr).Decode(t, &pg)
			return pg.Items
		}
		// the owner token's frame acts as global: it reads the global
		// instance's inbox (the owner's answer of 2026-09-30), sends nothing
		owner := e.fr(t, paAgent, "")
		xbindtest.Eventually(t, time.Minute, "both inboxes are acked", func() (bool, string) {
			a, g := inbox(e.fr(t, paAgent, "alice")), inbox(owner)
			return len(a) == 0 && len(g) == 0, fmt.Sprintf("alice's %v, global's %v", a, g)
		})
		time.Sleep(3 * time.Second) // a late ring or start pulls again: nothing new
		if n := notes(); len(n) != 1 {
			t.Errorf("alice's partition handled global's note %d times: %q", len(n), n)
		}
		if r := d.Call(t, "POST", mailAPI, map[string]any{"to": "user:alice", "topic": "forged"}, owner); r.Status != 403 ||
			!strings.Contains(string(r.Body), "only the global instance's backend") {
			t.Errorf("the owner token's frame sends as global: %d %s", r.Status, r)
		}
		// a topic this version doesn't know stays in global's inbox (young:
		// a newer version may read it) — where the owner's frame sees and
		// acknowledges it
		d.Must(t, "POST", mailAPI, map[string]any{"to": "global", "topic": "e2e/later", "data": nonce}, 200, e.fr(t, paAgent, "alice"))
		var later []struct{ ID, From, Topic string }
		xbindtest.Eventually(t, time.Minute, "the unknown topic waits in global's inbox", func() (bool, string) {
			later = inbox(owner)
			return len(later) == 1, fmt.Sprint(later)
		})
		time.Sleep(3 * time.Second) // the doorbell rang and global's mailbox left it
		if later = inbox(owner); len(later) != 1 || later[0].From != "user:alice" || later[0].Topic != "e2e/later" {
			t.Fatalf("global's inbox after its doorbell: %v", later)
		}
		d.Must(t, "POST", mailAPI+"/ack", map[string]any{"ids": []string{later[0].ID}}, 200, owner)
		if g := inbox(owner); len(g) != 0 {
			t.Errorf("global's inbox after the owner's frame acked: %v", g)
		}
	})

	t.Run("sandboxes", func(t *testing.T) {
		type mgr struct {
			Provider, Refusal, Error string
			OK                       bool
		}
		type box struct {
			Ref, Name string
			Labels    map[string]string
		}
		list := func(person string) (boxes []box, mgrs map[string]mgr) {
			t.Helper()
			var out struct {
				Sandboxes []box
				Managers  []mgr
			}
			r := ag(paAgent, person, "GET", "/sandboxes?fresh=1", nil)
			if r.Status != 200 {
				t.Fatalf("%q's sandboxes: %d %s", person, r.Status, r)
			}
			r.Decode(t, &out)
			mgrs = map[string]mgr{}
			for _, m := range out.Managers {
				mgrs[m.Provider] = m
			}
			return out.Sandboxes, mgrs
		}
		var mgrs map[string]mgr
		xbindtest.Eventually(t, 5*time.Minute, "the sandbox managers answer alice's partition", func() (bool, string) {
			_, mgrs = list("alice")
			return mgrs[paNew].OK && mgrs[paOld].Refusal != "", fmt.Sprintf("%+v", mgrs)
		})
		if o := mgrs[paOld]; o.OK || o.Refusal != "partitions" || !strings.Contains(o.Error, paOld) {
			t.Errorf("the old manager in alice's partition: %+v", o)
		}
		var made box
		r := ag(paAgent, "alice", "POST", "/sandboxes", map[string]any{"provider": paNew, "name": "alice-box", "conversation": ids["alice"], "bind": false})
		if r.Status != 201 {
			t.Fatalf("alice makes a sandbox: %d %s", r.Status, r)
		}
		r.Decode(t, &made)
		if made.Labels["xbin.agent/home"] != pkeys["alice"] || made.Labels["xbin.agent/conversation"] != strconv.FormatInt(ids["alice"], 10) {
			t.Errorf("alice's sandbox labels: %v (home should be her partition id %s)", made.Labels, pkeys["alice"])
		}
		if st := ag(paAgent, "alice", "POST", "/sandboxes", map[string]any{"provider": paOld, "name": "old-box"}).Status; st != 409 {
			t.Errorf("alice making a sandbox at the old manager: %d, want 409", st)
		}
		// her conversation works in her own sandbox (the manager's owner
		// says it is homed in her partition), never in the team's
		var coding struct{ ID int64 }
		if r := ag(paAgent, "alice", "POST", "/ask", map[string]string{"text": "hello in code", "class": "coding"}); r.Status/100 != 2 {
			t.Fatalf("alice starts a coding conversation: %d %s", r.Status, r)
		} else {
			r.Decode(t, &coding)
		}
		answer(paAgent, "alice", coding.ID, "Hello from the fake model.")
		conv := fmt.Sprintf("/runs/%d", coding.ID)
		if r := ag(paAgent, "alice", "PATCH", conv, map[string]any{"sandbox": map[string]string{"ref": made.Ref}}); r.Status != 200 {
			t.Errorf("alice binds her own sandbox: %d %s", r.Status, r)
		}
		var team box
		if r := ag(paAgent, "", "POST", "/sandboxes", map[string]any{"provider": paNew, "name": "team-box", "visibility": "team"}); r.Status != 201 {
			t.Fatalf("the global instance makes a team sandbox: %d %s", r.Status, r)
		} else {
			r.Decode(t, &team)
		}
		if team.Labels["xbin.agent/home"] != "global" {
			t.Errorf("the global instance's sandbox (no conversation) labels: %v", team.Labels)
		}
		if r := ag(paAgent, "alice", "PATCH", conv, map[string]any{"sandbox": map[string]string{"ref": team.Ref}}); r.Status != 403 {
			t.Errorf("BUG: alice binds the team's sandbox into her private conversation: %d %s", r.Status, r)
		}
		var loose box
		if r := ag(paAgent, "alice", "POST", "/sandboxes", map[string]any{"provider": paNew, "name": "loose-box"}); r.Status != 201 {
			t.Errorf("alice makes a sandbox without a conversation: %d %s", r.Status, r)
		} else if r.Decode(t, &loose); loose.Labels["xbin.agent/home"] != pkeys["alice"] {
			t.Errorf("her sandbox made without a conversation: labels %v", loose.Labels)
		}
		names := func(bs []box) (out []string) {
			for _, b := range bs {
				out = append(out, b.Name)
			}
			return out
		}
		if bs, _ := list("alice"); !slices.Contains(names(bs), "alice-box") {
			t.Errorf("alice doesn't list her sandbox: %v", names(bs))
		}
		if bs, _ := list("bob"); slices.Contains(names(bs), "alice-box") {
			t.Errorf("BUG: bob's partition lists alice's sandbox: %v", names(bs))
		}
		bs, gm := list("")
		if slices.Contains(names(bs), "alice-box") {
			t.Errorf("BUG: the global instance lists alice's sandbox: %v", names(bs))
		}
		if !gm[paOld].OK || !gm[paNew].OK {
			t.Errorf("the global instance keeps using both managers: %+v", gm)
		}
	})

	t.Run("existing-instance", func(t *testing.T) {
		// an unpartitioned instance — every instance made before the default,
		// and one made with the opt-out — runs as it always has: one
		// instance, privacy in its own code (D83)
		var me map[string]any
		ag(paPlain, "alice", "GET", "/me", nil).Decode(t, &me)
		if _, has := me["partition"]; has || me["user"] != "alice" {
			t.Errorf("alice on the unpartitioned instance: %v", me)
		}
		a := ask(paPlain, "alice", "hello from alice-plain")
		b := ask(paPlain, "bob", "hello from bob-plain")
		if a >= pa2to40 || b >= pa2to40 || a == b {
			t.Errorf("one db, ids from 1: alice %d, bob %d", a, b)
		}
		if got, body := convs(paPlain, "bob", "mine"); !slices.Equal(got, []int64{b}) || strings.Contains(body, "alice-plain") {
			t.Errorf("bob's conversations on the unpartitioned instance: %v", got)
		}
		if st := ag(paPlain, "bob", "GET", fmt.Sprintf("/runs/%d", a), nil).Status; st != 404 {
			t.Errorf("bob reading alice's private run on the unpartitioned instance: %d", st)
		}
		if row := e.row(t, paPlain); row != nil && (row.User || row.Global) {
			t.Errorf("the unpartitioned instance's row: %+v", *row)
		}
	})
}

// paMount is one line of a mount table.
type paMount struct{ line, root, point, source string }

// paPartitionMounts finds a process whose mount namespace has the person's
// partition volume (pkey) mounted at canonDB — the partition's backend (or
// a helper inside its sandbox: one namespace) — and returns its pid and
// mount table. The host's namespace has the global instance's volume there,
// never a partition's.
func paPartitionMounts(canonDB, pkey string) (string, []paMount) {
	ents, _ := os.ReadDir("/proc")
	for _, ent := range ents {
		if _, err := strconv.Atoi(ent.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", ent.Name(), "mountinfo"))
		if err != nil {
			continue
		}
		var ms []paMount
		hit := false
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			sep := slices.Index(f, "-")
			if len(f) < 5 || sep < 0 || sep+2 >= len(f) {
				continue
			}
			m := paMount{line: line, root: f[3], point: f[4], source: f[sep+2]}
			ms = append(ms, m)
			if m.point == canonDB && strings.Contains(m.source, "/"+pkey+"/") {
				hit = true
			}
		}
		if hit {
			return ent.Name(), ms
		}
	}
	return "", nil
}
