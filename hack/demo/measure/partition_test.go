//go:build linux && xbinmeasure

package measure

// partition_test.go — a person's own instance of a partitioned tile
// (docs/partitions.md), on an isolated xbind with owner auth: apps/notes
// declares "partition": ["user"], a Go backend over a per-person kv
// resource (encrypted at rest, envelope keys: no FUSE volume). Each person
// signs in (POST /api/xbin/login), mints the tile's frame token as their
// page would, and calls GET /notes:
//
//   - the first person ever: the tile's first build, then their start;
//   - every other person's first request: their partition's cold start
//     (admission, a namespace sandbox, the backend's start and health check),
//     the build shared;
//   - a restart: POST /api/xbin/partitions/stop, then their next request;
//   - warm requests: GET /notes over and over.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

const notesTile = "apps/notes"

var (
	partPeople = []string{"ana", "ben", "chloe", "dev", "elena", "farid", "grace", "hiro", "ines", "jonas", "kofi", "lena", "marta", "nikhil"}
	warmN      = quick(300, 20) // warm requests per person (for warmPeople of them)
	warmPeople = quick(5, 2)
	restartN   = quick(12, 2) // restarts measured
)

const notesSource = `package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

type note struct {
	ID   string    ` + "`json:\"id\"`" + `
	Text string    ` + "`json:\"text\"`" + `
	At   time.Time ` + "`json:\"at\"`" + `
}

func main() {
	kv := xbin.KV(xbin.Resource("notes"))
	mux := http.NewServeMux()
	// the person's notes: each person's partition holds only their own
	mux.HandleFunc("GET /notes", func(w http.ResponseWriter, r *http.Request) {
		keys, err := kv.List("")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		sort.Strings(keys)
		out := []note{}
		for _, k := range keys {
			b, err := kv.Get(k)
			if err != nil {
				continue
			}
			var n note
			if json.Unmarshal(b, &n) == nil {
				out = append(out, n)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"owner": xbin.PartitionUser(), "notes": out})
	})
	mux.HandleFunc("POST /notes", func(w http.ResponseWriter, r *http.Request) {
		var n note
		if err := json.NewDecoder(r.Body).Decode(&n); err != nil || n.Text == "" {
			http.Error(w, "a note needs text", http.StatusBadRequest)
			return
		}
		n.ID, n.At = strconv.FormatInt(time.Now().UnixNano(), 36), time.Now()
		b, _ := json.Marshal(n)
		if err := kv.Put(n.ID, b); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(n)
	})
	xbin.Serve(mux)
}
`

func TestPartition(t *testing.T) {
	d := daemon(t)
	rec := record(t, "partition")
	files := map[string]string{
		"go.mod":          "module notes\n\ngo 1.24\n\nrequire github.com/xbin-dev/xbin/sdk v0.0.0\n",
		"backend/main.go": notesSource,
		"scope.json":      `{"resources": {"notes": {"type": "kv"}}}` + "\n",
		"index.html":      "<!doctype html><title>Notes</title><h1>My notes</h1>\n",
	}
	if err := d.WriteFiles(notesTile, files); err != nil {
		t.Fatal(err)
	}
	d.WriteTile(t, notesTile, map[string]string{"xbin.json": `{"runtime": "go", "partition": ["user"], ` +
		`"partitionNote": "Your notes are yours: each person's live in their own instance.", ` +
		`"uses": [{"target": "res:` + notesTile + `/notes", "role": "writer"}]}` + "\n"})
	// the tile holds no data yet: the manifest's mode applies at once
	xbindtest.Eventually(t, 30*time.Second, "apps/notes partitioned", func() (bool, string) {
		st, b, err := call("GET", d.URL+"/api/xbin/partitions?tile="+notesTile, nil, owner(d))
		return err == nil && st == 200 && strings.Contains(string(b), `"state":"partitioned"`), fmt.Sprint(st, " ", cut(b), err)
	})

	people := partPeople
	if len(people) > restartN+2 {
		people = people[:restartN+2]
	}
	frames := map[string]map[string]string{}
	for _, p := range people {
		pw := "pw-" + p + "-91c2e7"
		d.AddUser(t, p, pw, "user", map[string]string{"apps/*": "read"})
		sess := d.Login(t, p, pw)
		frames[p] = frame(t, d, notesTile, map[string]string{"Authorization": "Bearer " + sess})
	}
	notes := d.URL + "/api/" + notesTile + "/notes"
	get := func(p string) (int, []byte, error) { return call("GET", notes, nil, frames[p]) }

	// the first person ever: the tile's first build, then their start
	t0 := time.Now()
	for {
		st, b, err := get(people[0])
		if err == nil && st == 200 {
			break
		}
		if time.Since(t0) > 5*time.Minute {
			t.Fatalf("%s's first request: %d %s %v\n%s", people[0], st, cut(b), err, d.LogTail(40))
		}
		time.Sleep(20 * time.Millisecond)
	}
	rec.add(map[string]any{"phase": "first-ever", "person": people[0]},
		map[string]float64{"first person ever (tile's first build + start)": ms(time.Since(t0))})

	// every other person's first request: a cold start of their partition
	for _, p := range people[1:] {
		t0 := time.Now()
		st, b, err := get(p)
		took := ms(time.Since(t0))
		var body struct{ Owner string }
		if err != nil || st != 200 || json.Unmarshal(b, &body) != nil || body.Owner != p {
			rec.add(map[string]any{"phase": "cold", "person": p, "err": fmt.Sprintf("%d %s %v", st, cut(b), err)}, nil)
			t.Errorf("%s's first request: %d %s %v", p, st, cut(b), err)
			continue
		}
		rec.add(map[string]any{"phase": "cold", "person": p}, map[string]float64{"person's first request (cold start)": took})
		// one note each, so the warm reads list something
		if st, b, err := call("POST", notes, map[string]any{"text": "Call the bakery about Friday's order"}, frames[p]); err != nil || st != 201 {
			t.Errorf("%s's note: %d %s %v", p, st, cut(b), err)
		}
	}

	// warm: the same person, again and again
	for _, p := range people[1 : 1+warmPeople] {
		for i := 0; i < warmN; i++ {
			t0 := time.Now()
			st, b, err := get(p)
			took := ms(time.Since(t0))
			if err != nil || st != 200 || !strings.Contains(string(b), `"owner":"`+p+`"`) || !strings.Contains(string(b), "bakery") {
				rec.add(map[string]any{"phase": "warm", "person": p, "i": i, "err": fmt.Sprintf("%d %s %v", st, cut(b), err)}, nil)
				t.Errorf("%s warm %d: %d %s %v", p, i, st, cut(b), err)
				continue
			}
			rec.add(map[string]any{"phase": "warm", "person": p, "i": i}, map[string]float64{"warm request (GET /notes)": took})
		}
	}

	// a restart: stopped (as the idle stop would), then their next request
	for _, p := range people[1 : 1+restartN] {
		st, b, err := call("POST", d.URL+"/api/xbin/partitions/stop", map[string]string{"tile": notesTile, "partition": "user:" + p}, owner(d))
		if err != nil || st != 200 {
			t.Errorf("stopping %s's partition: %d %s %v", p, st, cut(b), err)
			continue
		}
		time.Sleep(500 * time.Millisecond) // the old process is gone
		t0 := time.Now()
		st, b, err = get(p)
		took := ms(time.Since(t0))
		if err != nil || st != 200 {
			rec.add(map[string]any{"phase": "restart", "person": p, "err": fmt.Sprintf("%d %s %v", st, cut(b), err)}, nil)
			t.Errorf("%s after a stop: %d %s %v", p, st, cut(b), err)
			continue
		}
		rec.add(map[string]any{"phase": "restart", "person": p}, map[string]float64{"restart after a stop (next request)": took})
	}
	rec.done(map[string]any{"tile": notesTile, "people": len(people), "resource": "kv (encrypted at rest with envelope keys; no FUSE volume)"})
}
