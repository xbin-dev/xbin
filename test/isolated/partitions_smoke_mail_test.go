//go:build linux && integration

package isolated

// partitions_smoke_mail_test.go — the partitions smoke's partition mail
// case (F6, plans/partitions/04 §3), on a real `xbind --isolate` with owner
// auth on, with S1's fixture and helpers (partitions_smoke_fx_test.go): the
// global instance mails one person and the doorbell hands it to that
// person's partition alone; a person's partition mails global; a partition
// can't mail another person, admins and the root token can't mail or read;
// mail never starts a person's first instance, survives a restart of xbind,
// and rings at the partition's first start; GET /partitions lists
// partition-mail/1 and each inbox's counts, never a content. Records:
// plans/partitions/records/F6.md, W3a-wire.md.
//
//	set -a; eval "$(sed -n 's/^export \([A-Z_]*\) := \(.*\)$/\1=\2/p' .dev.mk | grep -v ^PATH)"; set +a
//	go test -tags=integration -count=1 -v -run '^TestPartitionsSmokeMail$' ./test/isolated/
//
// Run it with the Bash sandbox off, like the main smoke; CI skips it.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

const pmTile = "apps/pmail" // ["user", "global"], partitionMail /mailbox

// pmRoutes are the probe's mail routes: /send mails through the SDK as the
// instance it runs in; /mailbox is the doorbell's handler, which reads the
// inbox, keeps what it read (with the doorbell's identity) in its own
// partition's kv under "mailbox", and acknowledges it.
const pmRoutes = `	mux.HandleFunc("POST /send", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		id, err := xbin.Mail(r.URL.Query().Get("to"), r.URL.Query().Get("topic"), string(b))
		if err != nil {
			reply(w, 502, map[string]string{"error": err.Error()})
			return
		}
		reply(w, 200, map[string]string{"id": id})
	})
	mux.HandleFunc("POST /mailbox", func(w http.ResponseWriter, r *http.Request) {
		var bell xbin.MailBell
		_ = json.NewDecoder(r.Body).Decode(&bell)
		c := xbin.Caller(r)
		pg, err := xbin.InboxPage("", 100)
		if err != nil {
			fail(w, err)
			return
		}
		items := pg.Items
		kv := xbin.KV(xbin.Resource("kv"))
		var seen []map[string]any
		if prev, err := kv.Get("mailbox"); err == nil {
			_ = json.Unmarshal(prev, &seen)
		}
		for _, it := range items {
			var data string
			_ = json.Unmarshal(it.Data, &data)
			seen = append(seen, map[string]any{"id": it.ID, "from": it.From, "topic": it.Topic, "data": data,
				"bellFrom": c.From, "bellPartition": c.Partition, "bellRole": c.Role, "bell": bell.Partition, "self": xbin.Partition()})
		}
		b, _ := json.Marshal(seen)
		if err := kv.Put("mailbox", b); err != nil {
			fail(w, err)
			return
		}
		ids := make([]string, 0, len(items))
		for _, it := range items {
			ids = append(ids, it.ID)
		}
		if err := xbin.Ack(ids...); err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, map[string]int{"read": len(items)})
	})
`

var pmSource = strings.Replace(psSource, "\txbin.Serve(mux)\n", pmRoutes+"\txbin.Serve(mux)\n", 1)

// pmSeen is one item the doorbell's handler read.
type pmSeen struct {
	ID, From, Topic, Data             string
	BellFrom, BellPartition, BellRole string
	Bell, Self                        string
}

// mailbox is what the handler kept in the partition hdrs reach.
func (e *psEnv) mailbox(t *testing.T, hdrs ...xbindtest.Header) ([]pmSeen, int) {
	t.Helper()
	v, st, _ := e.value(t, "/api/"+pmTile+"/kv/kv/mailbox", hdrs...)
	var seen []pmSeen
	if st == 200 {
		if err := json.Unmarshal([]byte(v), &seen); err != nil {
			t.Errorf("the mailbox %q: %v", v, err)
		}
	}
	return seen, st
}

// waitMail waits until the partition hdrs reach has read an item of topic.
func (e *psEnv) waitMail(t *testing.T, what, topic string, hdrs ...xbindtest.Header) pmSeen {
	t.Helper()
	var got pmSeen
	xbindtest.Eventually(t, 4*time.Minute, what, func() (bool, string) {
		seen, st := e.mailbox(t, hdrs...)
		for _, s := range seen {
			if s.Topic == topic {
				got = s
				return true, ""
			}
		}
		return false, fmt.Sprintf("mailbox: %d %+v", st, seen)
	})
	return got
}

func TestPartitionsSmokeMail(t *testing.T) {
	t.Parallel()
	e := psSetup(t)
	d := e.d
	files := psFiles(pmTile, "")
	files["backend/main.go"] = pmSource
	manifest := `{"runtime": "go", "partition": ["user", "global"], "partitionMail": "/mailbox", ` +
		`"uses": [{"target": "res:` + pmTile + `/kv", "role": "writer"}]}` + "\n"
	delete(files, "xbin.json")
	if err := d.WriteFiles(pmTile, files); err != nil {
		t.Fatal(err)
	}
	d.WriteTile(t, pmTile, map[string]string{"xbin.json": manifest})
	e.waitState(t, pmTile, "partitioned")
	// the first build must see the tile's module in go.work: a build that
	// raced its regeneration fails until the code changes
	xbindtest.Eventually(t, 30*time.Second, "go.work lists "+pmTile, func() (bool, string) {
		b, err := os.ReadFile(filepath.Join(d.WS, "go.work"))
		return err == nil && strings.Contains(string(b), "./"+pmTile+"\n"), string(b)
	})
	api := "/api/" + pmTile
	// alice and bob have used the tile (their partitions ran); dave hasn't
	for _, p := range []string{"alice", "bob"} {
		if w := e.who(t, api+"/who", e.fr(t, pmTile, p)); w.Partition != "user:"+p {
			t.Fatalf("%s's partition answers as %q", p, w.Partition)
		}
	}
	if w := e.who(t, api+"/who"); w.Partition != "global" {
		t.Fatalf("the global instance answers as %q", w.Partition)
	}

	t.Run("global-to-person", func(t *testing.T) {
		// 04 §3: the global instance mails alice; the doorbell rings her
		// partition as xbin/mail, and her handler reads it from her inbox
		d.Must(t, "POST", api+"/send?to=user:alice&topic=dm", "for-alice-7c1e", 200)
		got := e.waitMail(t, "alice's partition reads global's mail", "dm", e.fr(t, pmTile, "alice"))
		if got.From != "global" || got.Data != "for-alice-7c1e" || got.BellFrom != "xbin/mail" || got.BellRole != "writer" ||
			got.BellPartition != "user:alice" || got.Bell != "user:alice" || got.Self != "user:alice" {
			t.Errorf("alice's partition read %+v", got)
		}
		// nobody else got it
		if seen, _ := e.mailbox(t, e.fr(t, pmTile, "bob")); len(seen) != 0 {
			t.Errorf("BUG: bob's partition read %+v", seen)
		}
		if seen, _ := e.mailbox(t); len(seen) != 0 {
			t.Errorf("BUG: the global instance read %+v", seen)
		}
	})

	t.Run("person-to-global", func(t *testing.T) {
		// a person's partition mails global only; from is xbind's
		d.Must(t, "POST", api+"/send?to=global&topic=reply", "alice-says-hi", 200, e.fr(t, pmTile, "alice"))
		got := e.waitMail(t, "the global instance reads alice's mail", "reply")
		if got.From != "user:alice" || got.Data != "alice-says-hi" || got.BellPartition != "global" || got.Self != "global" {
			t.Errorf("global read %+v", got)
		}
		r := d.Call(t, "POST", api+"/send?to=user:bob&topic=dm", "alice-to-bob", e.fr(t, pmTile, "alice"))
		if r.Status != 502 || !strings.Contains(string(r.Body), "403") || !strings.Contains(string(r.Body), "mails only") {
			t.Errorf("alice's partition mails bob: %d %s", r.Status, r)
		}
	})

	t.Run("outsiders", func(t *testing.T) {
		// admins and the root token neither send nor read: 403
		for name, hdrs := range map[string][]xbindtest.Header{"the owner token": nil, "carol (admin)": e.as("carol"), "alice's session": e.as("alice")} {
			r := d.Call(t, "POST", "/api/xbin/partitions/mail", map[string]string{"to": "user:alice", "topic": "forged"}, hdrs...)
			if r.Status != 403 {
				t.Errorf("%s mails alice: %d %s", name, r.Status, r)
			}
			if r := d.Call(t, "GET", "/api/xbin/partitions/mail", nil, hdrs...); r.Status != 403 {
				t.Errorf("%s reads mail: %d %s", name, r.Status, r)
			}
		}
		// alice's own frame reads her inbox — empty: her handler acked it
		var pg struct{ Items []json.RawMessage }
		d.Must(t, "GET", "/api/xbin/partitions/mail", nil, 200, e.fr(t, pmTile, "alice")).Decode(t, &pg)
		if len(pg.Items) != 0 {
			t.Errorf("alice's inbox after her handler acked: %s", pg.Items)
		}
	})

	t.Run("listing", func(t *testing.T) {
		// W3a: GET /partitions says mail is served and carries each
		// inbox's counts — alice's own row, and every row for an admin —
		// never a content
		type row struct {
			User string `json:"user"`
			Mail *struct {
				Pending int   `json:"pending"`
				Bytes   int64 `json:"bytes"`
			} `json:"mail"`
		}
		type listing struct {
			Features   []string `json:"features"`
			Partitions []row    `json:"partitions"`
		}
		for name, hdrs := range map[string][]xbindtest.Header{"alice": e.as("alice"), "carol (admin)": e.as("carol")} {
			r := d.Must(t, "GET", "/api/xbin/partitions?tile="+pmTile, nil, 200, hdrs...)
			var out listing
			r.Decode(t, &out)
			if !slices.Contains(out.Features, "partition-mail/1") {
				t.Errorf("%s: features %q without partition-mail/1", name, out.Features)
			}
			i := slices.IndexFunc(out.Partitions, func(p row) bool { return p.User == "alice" })
			if i < 0 || out.Partitions[i].Mail == nil || out.Partitions[i].Mail.Pending != 0 {
				t.Errorf("%s: alice's row's mail counts (her handler acked her item): %s", name, cut(string(r.Body), 600))
			}
			if s := string(r.Body); strings.Contains(s, "for-alice-7c1e") || strings.Contains(s, "alice-says-hi") {
				t.Errorf("BUG: %s's listing carries mail content: %s", name, cut(s, 600))
			}
		}
	})

	t.Run("never-run-restart", func(t *testing.T) {
		// dave never used the tile: mail waits and starts nothing (S1,
		// S20), survives a restart of xbind, and rings at his first start
		d.Must(t, "POST", api+"/send?to=user:dave&topic=dm", "for-dave-41b9", 200)
		noDave := func(what string, d0 time.Duration) {
			for end := time.Now().Add(d0); time.Now().Before(end); time.Sleep(500 * time.Millisecond) {
				if r := d.Must(t, "GET", "/api/xbin/sandboxes", nil, 200); strings.Contains(string(r.Body), `"user:dave"`) {
					t.Errorf("BUG: %s started dave's never-run partition: %s", what, cut(string(r.Body), 400))
					return
				}
			}
		}
		noDave("mail", 5*time.Second)
		// boot's sweep rings every inbox holding items, bootDelay (5 s)
		// after the delivery path is up: wait for its line, then watch
		const sweepLine = "partition mail: the sweep rings the inboxes holding items"
		sweeps := func() int { return strings.Count(d.LogTail(1<<20), sweepLine) }
		before := sweeps()
		d.Restart(t)
		e.frames = map[string]xbindtest.Header{}
		for id := range psPeople {
			e.sess[id] = d.Login(t, id, psPassword(id))
		}
		xbindtest.Eventually(t, time.Minute, "boot's mail sweep", func() (bool, string) {
			return sweeps() > before, d.LogTail(20)
		})
		noDave("boot's doorbell", 8*time.Second)
		if w := e.who(t, api+"/who", e.fr(t, pmTile, "dave")); w.Partition != "user:dave" {
			t.Fatalf("dave's partition answers as %q", w.Partition)
		}
		if r := d.Must(t, "GET", "/api/xbin/sandboxes", nil, 200); !strings.Contains(string(r.Body), `"user:dave"`) {
			t.Errorf("the sandbox list doesn't name dave's running partition, so the checks above prove nothing: %s", cut(string(r.Body), 400))
		}
		got := e.waitMail(t, "dave's first start rings the mail that waited", "dm", e.fr(t, pmTile, "dave"))
		if got.From != "global" || got.Data != "for-dave-41b9" {
			t.Errorf("dave's partition read %+v", got)
		}
		if seen, _ := e.mailbox(t, e.fr(t, pmTile, "bob")); len(seen) != 0 {
			t.Errorf("BUG: bob's partition read %+v", seen)
		}
	})
}
