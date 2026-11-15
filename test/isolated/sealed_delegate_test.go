//go:build linux && integration

package isolated

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

// sdNote is a tile with a backend that holds one note: GET reads it, PUT
// writes it and says who the write came from.
const sdNote = `package main

import (
	"fmt"
	"io"
	"net/http"
	"sync"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func main() {
	var mu sync.Mutex
	note := "first"
	mux := http.NewServeMux()
	mux.HandleFunc("GET /note", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(w, "note=%s reader=%s", note, xbin.Caller(r).User)
	})
	mux.HandleFunc("PUT /note", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		note = string(b)
		fmt.Fprintf(w, "written by=%s", xbin.Caller(r).User)
	})
	xbin.Serve(mux)
}
`

// Delegation with a real holder (D228 × D227): the fixture's sealed
// component xbin/people holds delegation by its table row, one instance per
// person. Alice's partition of it reads a tile as Alice, is refused a write
// she didn't approve, and performs it with her ticket; Bob's partition is
// Bob; nothing of it is an admin; and the table's Env reached the backend.
// Its calls run under an agent session it opened; xbind's notices reach the
// path its row names, in its person's partition only; and its page reads
// that session's connection view through the one extra read the row allows.
// A production xbind, sandboxes on, people signed in.
func TestSealedHolderActsForItsPerson(t *testing.T) {
	a := xbindtest.Require(t)
	if a.Gocryptfs == "" {
		xbindtest.Skip(t, "no gocryptfs: people's partitions keep their data in encrypted volumes")
	}
	a = a.WithSealedFixture(t)
	d := xbindtest.Start(t, a, xbindtest.Options{Auth: true, Production: true,
		Env: []string{"XBIN_VAULT_PASSPHRASE=xbindtest-vault"}})
	const tile = "apps/sdnote"
	if err := d.WriteFiles(tile, map[string]string{
		"go.mod":          "module sd/note\n\ngo 1.24\n\nrequire github.com/xbin-dev/xbin/sdk v0.0.0\n",
		"backend/main.go": sdNote,
		"index.html":      "<!doctype html><html><head></head><body>note</body></html>\n",
	}); err != nil {
		t.Fatal(err)
	}
	d.WriteTile(t, tile, map[string]string{"xbin.json": `{"runtime":"go","expose":{"roles":{"reader":"read","writer":"write"}}}` + "\n"})

	e := &sealedEnv{d: d, sess: map[string]string{}, frames: map[string]xbindtest.Header{}}
	d.AddUser(t, "alice", "pw-alice-5c2e81", "user", map[string]string{sealedPeople: "read", sealedProbe: "read", tile: "write"})
	d.AddUser(t, "bob", "pw-bob-5c2e81", "user", map[string]string{sealedPeople: "read"}) // not the tile
	for _, id := range []string{"alice", "bob"} {
		e.sess[id] = d.Login(t, id, "pw-"+id+"-5c2e81")
	}
	// the workspace lets people's agents act (off until an admin says so)
	if r := d.Call(t, "PUT", "/api/xbin/delegate/workspace", map[string]any{"enabled": true}); r.Status/100 != 2 {
		t.Logf("PUT /delegate/workspace: %d %s (the switch may already be on)", r.Status, r)
	}

	// asPerson has person's partition of the holder make one call as them
	type relay struct {
		Status int
		Body   string
	}
	// each person's partition opens the agent session its calls run under
	handles := map[string]string{}
	handle := func(person string) string {
		t.Helper()
		if handles[person] == "" {
			var s struct{ ID string }
			d.Must(t, "POST", "/api/"+sealedPeople+"/session?model=fixture/model", nil, 200, e.frame(t, sealedPeople, person)).Decode(t, &s)
			if s.ID == "" {
				t.Fatalf("%s's agent session has no id", person)
			}
			handles[person] = s.ID
		}
		return handles[person]
	}
	asPerson := func(person, method, path, body, ticket string) relay {
		t.Helper()
		q := url.Values{"method": {method}, "path": {path}, "session": {handle(person)}}
		if ticket != "" {
			q.Set("ticket", ticket)
		}
		var payload any
		if body != "" {
			payload = json.RawMessage(body)
		}
		var out relay
		d.Must(t, "POST", "/api/"+sealedPeople+"/as-person?"+q.Encode(), payload, 200, e.frame(t, sealedPeople, person)).Decode(t, &out)
		return out
	}

	// the table's fixed environment reached the backend; xbind's own wins
	var env struct{ Mode, Component string }
	d.Must(t, "GET", "/api/"+sealedPeople+"/env", nil, 200, e.frame(t, sealedPeople, "alice")).Decode(t, &env)
	if env.Mode != "workspace" || env.Component != sealedPeople {
		t.Errorf("its environment: %+v, want FIXTURE_MODE=workspace from the table", env)
	}

	// it reads the tile as its person
	if r := asPerson("alice", "GET", "/api/"+tile+"/note", "", ""); r.Status != 200 || !strings.Contains(r.Body, "note=first reader=alice") {
		t.Fatalf("alice's agent reads the tile: %d %s", r.Status, r.Body)
	}
	if r := asPerson("alice", "GET", "/api/xbin/whoami", "", ""); r.Status != 200 || !strings.Contains(r.Body, `"alice"`) || strings.Contains(r.Body, `"admin":true`) {
		t.Errorf("who alice's agent is: %d %s", r.Status, r.Body)
	}
	// …and only as its person: bob can't read the tile, so neither can his
	if r := asPerson("bob", "GET", "/api/"+tile+"/note", "", ""); r.Status != 403 {
		t.Errorf("bob's agent reads a tile bob can't: %d %s", r.Status, r.Body)
	}
	// never an admin, never into a system component — its own included
	for _, path := range []string{"/api/xbin/users", "/api/xbin/sealed", "/api/" + sealedProbe + "/who", "/api/" + sealedPeople + "/who"} {
		if r := asPerson("alice", "GET", path, "", ""); r.Status < 400 {
			t.Errorf("alice's agent GET %s: %d %s", path, r.Status, r.Body)
		}
	}

	// a write she didn't approve: refused, and the tile is as it was
	if r := asPerson("alice", "PUT", "/api/"+tile+"/note", `"by the agent"`, ""); r.Status != 403 || !strings.Contains(r.Body, "needs-approval") {
		t.Fatalf("an unapproved write: %d %s", r.Status, r.Body)
	}
	if r := asPerson("alice", "GET", "/api/"+tile+"/note", "", ""); !strings.Contains(r.Body, "note=first") {
		t.Fatalf("the refused write changed the tile: %s", r.Body)
	}
	// the agent asks, alice approves in her own session, the agent performs
	// exactly that call with the ticket
	var asked struct{ ID, State string }
	d.Must(t, "POST", "/api/"+sealedPeople+"/ask?"+url.Values{"method": {"PUT"}, "path": {"/api/" + tile + "/note"}, "session": {handle("alice")}}.Encode(),
		json.RawMessage(`"by the agent"`), 200, e.frame(t, sealedPeople, "alice")).Decode(t, &asked)
	if asked.ID == "" {
		t.Fatalf("the agent's request: %+v", asked)
	}
	// bob can't approve what alice's agent asked
	if r := d.Call(t, "POST", "/api/xbin/delegate/requests/"+asked.ID+"/approve", nil, e.as("bob")...); r.Status < 400 {
		t.Errorf("bob approved alice's agent's request: %d %s", r.Status, r)
	}
	d.Must(t, "POST", "/api/xbin/delegate/requests/"+asked.ID+"/approve", nil, 200, e.as("alice")...)
	// xbind tells the holder's backend — on the path its row names, in
	// alice's partition, naming the request and nothing of its content
	var got struct {
		Count int
		Last  string
	}
	for end := time.Now().Add(30 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		d.Must(t, "GET", "/api/"+sealedPeople+"/notices", nil, 200, e.frame(t, sealedPeople, "alice")).Decode(t, &got)
		if strings.Contains(got.Last, asked.ID) {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("no notice of the decided request reached alice's partition on the row's Notify path: %+v", got)
		}
	}
	if !strings.Contains(got.Last, `"partition":"user:`) || strings.Contains(got.Last, "by the agent") {
		t.Errorf("the notice: %s, want a partition and ids only", got.Last)
	}
	var bobs struct{ Count int }
	d.Must(t, "GET", "/api/"+sealedPeople+"/notices", nil, 200, e.frame(t, sealedPeople, "bob")).Decode(t, &bobs)
	if bobs.Count != 0 {
		t.Errorf("bob's partition was told of alice's request: %d notices", bobs.Count)
	}
	if r := asPerson("alice", "PUT", "/api/"+tile+"/note", `"by the agent"`, asked.ID); r.Status != 200 || !strings.Contains(r.Body, "written by=alice") {
		t.Fatalf("the approved write, with its ticket: %d %s", r.Status, r.Body)
	}
	if r := asPerson("alice", "GET", "/api/"+tile+"/note", "", ""); !strings.Contains(r.Body, `by the agent`) {
		t.Errorf("the tile after the approved write: %s", r.Body)
	}
	// the ticket was for that call, once: another body, or a second use, is refused
	if r := asPerson("alice", "PUT", "/api/"+tile+"/note", `"something else"`, asked.ID); r.Status < 400 {
		t.Errorf("the ticket used for another write: %d %s", r.Status, r.Body)
	}
	// its page is a viewer, with the row's one extra read and the
	// delegation API — nothing else of xbind's
	fr := e.frame(t, sealedPeople, "alice")
	if r := d.Call(t, "GET", "/api/xbin/agents/connection", nil, fr); r.Status == 403 && strings.Contains(r.String(), "its page reaches its own backend only") {
		t.Errorf("the read its row names: %d %s", r.Status, r)
	}
	// …which answers the page of the holder a session belongs to, for the
	// person driving it, and nobody else
	conn := "/api/xbin/agents/connection?session=" + url.QueryEscape(handle("alice"))
	var view struct{ Session string }
	d.Must(t, "GET", conn, nil, 200, fr).Decode(t, &view)
	if view.Session != handle("alice") {
		t.Errorf("the connection view of alice's session: %+v", view)
	}
	if r := d.Call(t, "GET", conn, nil, e.frame(t, sealedPeople, "bob")); r.Status != 404 {
		t.Errorf("bob's page reads alice's session: %d %s", r.Status, r)
	}
	if r := d.Call(t, "GET", conn, nil, e.frame(t, sealedProbe, "alice")); r.Status != 403 {
		t.Errorf("another system component's page reads the session (its row allows no such read): %d %s", r.Status, r)
	}
	if r := d.Call(t, "GET", conn, nil, e.as("alice")...); r.Status != 403 {
		t.Errorf("alice's own session reads the holder page's view: %d %s", r.Status, r)
	}
	if r := d.Call(t, "GET", "/api/xbin/agents/connection?session=nope", nil, fr); r.Status != 404 {
		t.Errorf("an unknown session: %d %s", r.Status, r)
	}
	if r := d.Call(t, "GET", "/api/xbin/delegate/state", nil, fr); r.Status != 200 {
		t.Errorf("its page reads its person's delegation state: %d %s", r.Status, r)
	}
	for _, c := range [][2]string{{"GET", "/api/xbin/components"}, {"PUT", "/api/xbin/agents/connection"}, {"GET", "/api/" + tile + "/note"}} {
		if r := d.Call(t, c[0], c[1], nil, fr); r.Status != 403 {
			t.Errorf("its page's %s %s: %d %s", c[0], c[1], r.Status, r)
		}
	}
}
