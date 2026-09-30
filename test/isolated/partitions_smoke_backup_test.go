//go:build linux && integration

package isolated

// partitions_smoke_backup_test.go — the partitions smoke's backup case
// (F17b; plans/partitions/11-backup-encryption.md §2-§4), on a real
// `xbind --isolate` with owner auth and a vault, beside TestPartitionsSmoke
// (whose fixture and helpers it shares): a backup of a partitioned tile
// writes each person's partition to an archive of its own, sealed; alice
// restores her own partition (her data comes back, bob's doesn't move);
// nobody else, and no tile credential, restores hers; erasing the tile's
// data keys makes her archive unrestorable and has the archiver drop it.
//
//	set -a; eval "$(sed -n 's/^export \([A-Z_]*\) := \(.*\)$/\1=\2/p' .dev.mk | grep -v ^PATH)"; set +a
//	go test -tags=integration -count=1 -v -run '^TestPartitionsSmokeBackups$' ./test/isolated/
//
// Like the main smoke, run it with the Bash sandbox off; CI skips it.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/util"
	"github.com/xbin-dev/xbin/test/xbindtest"
)

const (
	pbArch = "apps/pbk-arch" // an archiver that keeps what it is given, in memory
	pbTile = "apps/pbk"      // the partitioned probe: ["user", "global"]
)

// pbArchSource is the archiver: the archiver contract (docs/overview/
// 14-lifecycle.md) over an in-memory map, with the optional erase by key.
const pbArchSource = `package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"

	xbin "github.com/xbin-dev/xbin/sdk"
)

type ver struct {
	v, subkey string
	body      []byte
}

func main() {
	var mu sync.Mutex
	keys := map[string][]ver{} // newest first
	n := 0
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /archive/{key}", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		n++
		v := fmt.Sprintf("v%06d", n)
		k := r.PathValue("key")
		keys[k] = append([]ver{{v, r.Header.Get("X-XBin-Backup-Subkey"), body}}, keys[k]...)
		json.NewEncoder(w).Encode(map[string]any{"version": v, "size": len(body)})
	})
	mux.HandleFunc("GET /archive/{key}/versions", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		out := []map[string]any{}
		for _, x := range keys[r.PathValue("key")] {
			out = append(out, map[string]any{"version": x.v, "subkey": x.subkey, "size": len(x.body), "time": "2026-09-30T00:00:00Z"})
		}
		json.NewEncoder(w).Encode(map[string]any{"versions": out})
	})
	mux.HandleFunc("GET /archive/{key}/versions/{v}", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		vs := keys[r.PathValue("key")]
		for i, x := range vs {
			if x.v == r.PathValue("v") || r.PathValue("v") == "latest" && i == 0 {
				w.Write(x.body)
				return
			}
		}
		http.Error(w, "no such version", http.StatusNotFound)
	})
	mux.HandleFunc("DELETE /archive/{key}/versions/{v}", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		k := r.PathValue("key")
		keys[k] = slices.DeleteFunc(keys[k], func(x ver) bool { return x.v == r.PathValue("v") })
		io.WriteString(w, "{\"ok\":true}")
	})
	mux.HandleFunc("POST /archive/erase", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Subkeys []string }
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for k, vs := range keys {
			keys[k] = slices.DeleteFunc(vs, func(x ver) bool {
				dead := slices.Contains(req.Subkeys, x.subkey)
				if dead {
					n++
				}
				return dead
			})
		}
		json.NewEncoder(w).Encode(map[string]any{"deleted": n})
	})
	xbin.Serve(mux)
}
`

func TestPartitionsSmokeBackups(t *testing.T) {
	t.Parallel()
	e := psSetup(t)
	d := e.d
	if err := d.WriteFiles(pbArch, map[string]string{
		"go.mod":          "module ps/pbk_arch\n\ngo 1.24\n\nrequire github.com/xbin-dev/xbin/sdk v0.0.0\n",
		"backend/main.go": pbArchSource,
	}); err != nil {
		t.Fatal(err)
	}
	d.WriteTile(t, pbArch, map[string]string{"xbin.json": `{"runtime":"go","provides":{"store":{"kind":"archive"}}}` + "\n"})
	versions := func(key string) []struct{ Version, Subkey string } {
		t.Helper()
		var out struct {
			Versions []struct{ Version, Subkey string }
		}
		r := d.Call(t, "GET", "/api/"+pbArch+"/archive/"+key+"/versions", nil)
		if r.Status != 200 {
			return nil
		}
		r.Decode(t, &out)
		return out.Versions
	}
	for deadline := time.Now().Add(3 * time.Minute); ; time.Sleep(time.Second) { // built and serving
		if r := d.Call(t, "GET", "/api/"+pbArch+"/archive/none/versions", nil); r.Status == 200 {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("the archiver never served: %d %s", r.Status, r)
		}
	}
	d.Bind(t, "*", "@archive", pbArch)
	uses := `, "uses": [{"target": "res:%s/kv", "role": "writer"}, {"target": "res:%s/beat", "role": "writer"}]`
	w2Write(t, d, pbTile, `, "partition": ["user", "global"]`+strings.ReplaceAll(uses, "%s", pbTile))
	e.waitState(t, pbTile, "partitioned")

	api := "/api/" + pbTile
	e.put(t, api+"/kv/kv/secret", "global-secret")
	for _, p := range []string{"alice", "bob"} {
		e.put(t, api+"/kv/kv/secret", p+"-secret-4b1d", e.fr(t, pbTile, p))
	}
	e.put(t, api+"/secret/tok", "alice-vault-4b1d", e.fr(t, pbTile, "alice"))

	// the backup: each person's partition in an archive of its own, sealed
	var out struct {
		Partitions struct {
			Archived int
			Failed   []string
		}
	}
	d.Must(t, "POST", "/api/xbin/backup", map[string]string{"component": pbTile}, 200).Decode(t, &out)
	if out.Partitions.Archived != 2 || len(out.Partitions.Failed) > 0 {
		t.Fatalf("the backup's partitions: %+v", out.Partitions)
	}
	alicePK := util.PartitionKey("alice", e.uid(t, "alice"))
	aliceKey := ".partitions." + util.TileKey(pbTile) + ".main." + alicePK
	vs := versions(aliceKey)
	if len(vs) != 1 || !strings.HasPrefix(vs[0].Subkey, "bk-") {
		t.Fatalf("alice's archive at the archiver: %+v", vs)
	}
	body := d.Must(t, "GET", "/api/"+pbArch+"/archive/"+aliceKey+"/versions/latest", nil, 200).Body
	if !bytes.HasPrefix(body, []byte("XBINSEAL")) || bytes.Contains(body, []byte("4b1d")) {
		t.Fatalf("alice's archive isn't sealed, or holds plaintext: %.40q", body)
	}

	// alice changes her data, then restores her own partition
	e.put(t, api+"/kv/kv/secret", "alice-changed", e.fr(t, pbTile, "alice"))
	e.put(t, api+"/kv/kv/secret", "bob-changed", e.fr(t, pbTile, "bob"))
	restore := map[string]any{"tile": pbTile, "confirm": pbTile + " user:alice"}
	for _, c := range []struct {
		who  string
		hdrs []xbindtest.Header
		body map[string]any
		want int
	}{
		{"bob, for alice", e.as("bob"), map[string]any{"tile": pbTile, "user": "alice", "confirm": pbTile + " user:alice"}, 403},
		{"alice's frame", []xbindtest.Header{e.fr(t, pbTile, "alice")}, restore, 403},
		{"alice without the typed confirmation", e.as("alice"), map[string]any{"tile": pbTile}, 400},
	} {
		if r := d.Call(t, "POST", "/api/xbin/partitions/restore", c.body, c.hdrs...); r.Status != c.want {
			t.Errorf("%s: %d %s, want %d", c.who, r.Status, r, c.want)
		}
	}
	var done struct {
		OK   bool
		Data bool
	}
	d.Must(t, "POST", "/api/xbin/partitions/restore", restore, 200, e.as("alice")...).Decode(t, &done)
	if !done.OK || !done.Data {
		t.Errorf("alice's restore: %+v", done)
	}
	if v, code, raw := e.value(t, api+"/kv/kv/secret", e.fr(t, pbTile, "alice")); v != "alice-secret-4b1d" {
		t.Errorf("alice's data after her restore: %q %d %s", v, code, raw)
	}
	if v, code, raw := e.value(t, api+"/secret/tok", e.fr(t, pbTile, "alice")); v != "alice-vault-4b1d" {
		t.Errorf("alice's vault after her restore: %q %d %s", v, code, raw)
	}
	if v, _, _ := e.value(t, api+"/kv/kv/secret", e.fr(t, pbTile, "bob")); v != "bob-changed" {
		t.Errorf("bob's data moved with alice's restore: %q", v)
	}
	if v, _, _ := e.value(t, api+"/kv/kv/secret"); v != "global-secret" {
		t.Errorf("global's data moved with alice's restore: %q", v)
	}

	// erasing the tile's data keys: alice's archive is gone at the archiver,
	// and a restore of it is refused with why
	d.Must(t, "POST", "/api/xbin/backup/erase", map[string]string{"component": pbTile, "what": "data"}, 200)
	if vs := versions(aliceKey); len(vs) != 0 {
		t.Errorf("alice's archive after the erase: %+v", vs)
	}
	r := d.Call(t, "POST", "/api/xbin/partitions/restore", restore, e.as("alice")...)
	if r.Status != 404 || !strings.Contains(r.String(), "no backup of this partition") {
		t.Errorf("alice's restore after the erase: %d %s", r.Status, r)
	}
	rec, _ := e.modeRecord(t, pbTile)
	var ops []string
	for _, h := range rec.History {
		ops = append(ops, h.Op)
	}
	if got := strings.Join(ops, ","); !strings.Contains(got, "partition-restore") || !strings.Contains(got, "backup-erase") {
		t.Errorf("the tile's history: %s", got)
	}
	var listing map[string]any
	d.Must(t, "GET", "/api/xbin/partitions/backups?tile="+pbTile, nil, 200, e.as("alice")...).Decode(t, &listing)
	if b, _ := json.Marshal(listing["versions"]); string(b) != "[]" {
		t.Errorf("alice's listing after the erase: %s", b)
	}
}
