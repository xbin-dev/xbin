package boot

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// npGolden is the /components row of apps/np, a scoped tile with data whose
// manifest carries partitionNote and partitionMail without "partition" and
// whose scope.json marks resources shared: keys nobody partitions, which
// change nothing. Master's answer, byte for byte (normalized as zsNorm does).
const npGolden = `[
  {
    "hasIndex": true,
    "path": "apps/np",
    "runtime": "static",
    "scope": "apps/np",
    "uses": [
      {
        "role": "writer",
        "target": "res:apps/np/db"
      }
    ]
  }
]`

// covers PD-44 SC-ZERO — the components part of TestNoPartitionGolden
// (plans/partitions/10 §A.1): on a workspace where no tile asks for a
// partition, /components answers as master does — the zero-state fixture's
// rows, and a scoped tile with kv data and a vault key whose manifest and
// scope.json carry the new keys without "partition" — for the owner and for
// a reader, no row has a partition member, and no mode record (not even
// data/partitions) is ever written, across rescans.
func TestNoPartitionGolden(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	ws := zsWorkspace(t)
	for rel, body := range map[string]string{
		"apps/np/scope.json": `{"resources":{"db":{"type":"kv","shared":true},"feed":{"type":"kv","shared":"read"}}}`,
		"apps/np/xbin.json":  `{"runtime":"static","partitionNote":"ignored without partition","partitionMail":"/mailbox","uses":[{"target":"res:apps/np/db","role":"writer"}]}`,
		"apps/np/index.html": "<!doctype html><html><head></head><body>np</body></html>\n",
	} {
		p := filepath.Join(ws, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	d := zsBoot(t, ws)
	owner := "Bearer " + d.owner
	if _, err := d.st.Users.Upsert(users.User{ID: "ana", Role: users.RoleUser,
		Tiles: map[string]string{"apps/zs": users.LevelRead, "notes+ideas": users.LevelWrite, "apps/np": users.LevelRead}}, "password1"); err != nil {
		t.Fatal(err)
	}
	ana := "Cookie: xbin_session=" + d.st.Auth.NewSession("ana", "127.0.0.1")
	if code, body := d.do(t, "PUT", "/api/xbin/kv/res:apps/np/db/k", owner); code/100 != 2 {
		t.Fatalf("a kv write: %d %s", code, body)
	}
	if err := d.st.Reg.Rescan(); err != nil { // what the watcher does after an edit
		t.Fatal(err)
	}

	get := func(cred string) []byte {
		t.Helper()
		code, body := d.do(t, "GET", "/api/xbin/components", cred)
		if code != 200 {
			t.Fatalf("GET /components: %d %s", code, body)
		}
		return body
	}
	for who, cred := range map[string]string{"the owner": owner, "ana": ana} {
		comps := get(cred)
		for _, k := range strings.Split(zsKeys(t, comps), ",") {
			if strings.HasPrefix(k, "partition") {
				t.Errorf("%s: /components rows carry %q in a workspace without partitions", who, k)
			}
		}
		var rows []map[string]any
		if err := json.Unmarshal(comps, &rows); err != nil {
			t.Fatal(err)
		}
		var np, rest []any
		for _, r := range rows {
			if r["path"] == "apps/np" {
				np = append(np, r)
			} else {
				rest = append(rest, r)
			}
		}
		b, _ := json.Marshal(rest)
		for _, g := range zsListingGoldens {
			if g.path != "/api/xbin/components" || g.as != who {
				continue
			}
			if got := zsNorm(t, zsPick(t, b, "path"), nil, nil); got != g.want {
				t.Errorf("%s: the zero-state fixture's rows:\n%s\nwant\n%s", who, got, g.want)
			}
		}
		b, _ = json.Marshal(np)
		if got := zsNorm(t, b, nil, nil); got != npGolden {
			t.Errorf("%s: apps/np's row:\n%s\nwant\n%s", who, got, npGolden)
		}
		if code, body := d.do(t, "GET", "/api/xbin/components/apps/np", cred); code != 200 || strings.Contains(string(body), `"partition`) {
			t.Errorf("%s: GET /components/apps/np: %d %s", who, code, body)
		}
	}
	if _, err := os.Stat(filepath.Join(ws, "data", "partitions")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("data/partitions exists on a workspace without partitions: %v", err)
	}
	// Nor a partition namespace level, a key in the main kv store's place,
	// or a partition member on a resource row (plans/partitions/03 §B.1, §B.7).
	for _, rel := range []string{"data/resources-enc/.partitions", ".xbin/resenc/.partitions"} {
		if _, err := os.Stat(filepath.Join(ws, filepath.FromSlash(rel))); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s exists on a workspace without partitions: %v", rel, err)
		}
	}
	if code, body := d.do(t, "GET", "/api/xbin/kv/res:apps/np/db/k", owner); code != 200 {
		t.Errorf("the kv key back from today's store: %d %s", code, body)
	}
	for _, r := range d.st.Broker.ResourceUsage() {
		if r.Partition != "" {
			t.Errorf("a resource row names a partition: %+v", r)
		}
	}
}

// covers PD-44 PD-50 — the wiring of a booted xbind: the broker installs the
// mode store, so a tile that asks for user partitions and holds no data is
// recorded at boot (auto), and one that holds data (a vault key) waits:
// /components says pending with the request, its API answers 409 with the
// pending body, and the runner refuses to start its primary, whatever asks.
func TestPartitionModeBoot(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	ws := zsWorkspace(t)
	for rel, body := range map[string]string{
		"apps/pa/xbin.json":         `{"runtime":"node","partition":["user","global"]}`,
		"apps/pa/backend/server.js": zsNodeServer,
		"apps/pt/xbin.json":         `{"runtime":"node","partition":["user"]}`,
		"apps/pt/backend/server.js": zsNodeServer,
		// a legacy plaintext vault: a key the tile holds before any partition
		"data/vault/" + util.CompKey("apps/pt") + ".json": `{"token":"x"}`,
	} {
		p := filepath.Join(ws, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	d := zsBoot(t, ws)
	owner := "Bearer " + d.owner

	code, body := d.do(t, "GET", "/api/xbin/components", owner)
	if code != 200 {
		t.Fatalf("GET /components: %d %s", code, body)
	}
	var rows []struct {
		Path      string          `json:"path"`
		Partition json.RawMessage `json:"partition"`
	}
	_ = json.Unmarshal(body, &rows)
	got := map[string]string{}
	for _, r := range rows {
		got[r.Path] = string(r.Partition)
	}
	for tile, want := range map[string]string{
		"apps/pa": `{"state":"partitioned","user":true,"global":true}`,
		"apps/pt": `{"state":"pending","user":false,"global":false,"request":{"user":true,"global":false,"declined":false}}`,
		"apps/zs": ``,
	} {
		if got[tile] != want {
			t.Errorf("%s's partition: %s, want %s", tile, got[tile], want)
		}
	}
	for _, tile := range []string{"apps/pa", "apps/pt"} {
		if _, err := os.Stat(filepath.Join(ws, "data", "partitions", util.TileKey(tile), "mode.json")); err != nil {
			t.Errorf("%s has no mode record: %v", tile, err)
		}
	}

	code, body = d.do(t, "GET", "/api/apps/pt/x", owner)
	var ans struct {
		Error     string
		Partition struct{ State string }
	}
	_ = json.Unmarshal(body, &ans)
	if code != 409 || ans.Partition.State != "pending" ||
		!strings.HasPrefix(ans.Error, "apps/pt is paused: a partition mode switch is requested (unpartitioned → user)") {
		t.Errorf("the pending tile's API: %d %s", code, body)
	}
	c, _ := d.st.Reg.Component("apps/pt")
	if _, err := d.st.Run.Ensure(context.Background(), c); err == nil || !strings.Contains(err.Error(), "is paused") {
		t.Errorf("the runner started a pending primary: %v", err)
	}
	// The hold is the primary's alone: a deployment beyond it keeps
	// D127's behaviour (PD-17), and a tile that isn't held runs as today.
	for _, c := range []struct {
		tile, dep string
		want      bool
	}{{"apps/pt", "main", false}, {"apps/pt", "dev", true}, {"apps/pa", "main", true}} {
		if got := d.st.Run.ShouldRunDeployment(c.tile, c.dep); got != c.want {
			t.Errorf("ShouldRunDeployment(%s, %s) = %v, want %v", c.tile, c.dep, got, c.want)
		}
	}
}
