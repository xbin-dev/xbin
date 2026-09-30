package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
)

// PD-44 — /components rows carry "partition" (the settled state, the
// recorded mode, an open or declined request, the partitionNote while the
// request is pending) and "partitionError" only for a tile that asks for a
// mode or records one; every other row keeps its keys.
func TestComponentsPartitionFields(t *testing.T) {
	w := newAssetWS(t, TileAssetsLegacy)
	for rel, body := range map[string]string{
		"apps/part/xbin.json": `{"runtime":"go","partition":["user","global"]}`,
		"apps/pend/xbin.json": `{"runtime":"go","partition":["user"],"partitionNote":" Your notes live here, yours alone. "}`,
		"apps/kept/xbin.json": `{"runtime":"go","partition":["user"],"partitionNote":"declined: not shown"}`,
		"apps/bad/xbin.json":  `{"runtime":"go","partition":["user","org"]}`,
		"apps/gone/xbin.json": `{"runtime":"go"}`,
	} {
		p := filepath.Join(w.root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	user := &registry.PartitionSpec{User: true}
	modes := map[string]registry.PartitionMode{
		"apps/part": {State: registry.PartitionPartitioned, Recorded: registry.PartitionSpec{User: true, Global: true}},
		"apps/pend": {State: registry.PartitionPending, Request: &registry.PartitionRequest{Spec: user}},
		"apps/kept": {State: registry.PartitionUnpartitioned, Request: &registry.PartitionRequest{Spec: user, Declined: true}},
		"apps/bad":  {State: registry.PartitionInvalid},
		// the code dropped the key on a partitioned tile with data
		"apps/gone": {State: registry.PartitionPending, Recorded: *user, Request: &registry.PartitionRequest{}},
	}
	w.s.Reg.PartitionModes = func(a registry.PartitionAsk) registry.PartitionMode { return modes[a.Tile] }
	if err := w.s.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/xbin/components", nil)
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true}))
	rec := httptest.NewRecorder()
	w.s.apiComponents(rec, r)
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, row := range rows {
		var path string
		_ = json.Unmarshal(row["path"], &path)
		got[path] = string(row["partition"])
		if e := row["partitionError"]; e != nil {
			got[path] += " " + string(e)
		}
	}
	for tile, want := range map[string]string{
		"apps/part": `{"state":"partitioned","user":true,"global":true}`,
		// the note (trimmed) only while the request waits for a manager
		"apps/pend": `{"state":"pending","user":false,"global":false,"request":{"user":true,"global":false,"declined":false},"note":"Your notes live here, yours alone."}`,
		"apps/kept": `{"state":"unpartitioned","user":false,"global":false,"request":{"user":true,"global":false,"declined":true}}`,
		"apps/bad":  `{"state":"invalid","user":false,"global":false} "partition: unknown word \"org\" (this xbind knows \"user\" and \"global\"; an unknown word runs no backend)"`,
		"apps/gone": `{"state":"pending","user":true,"global":false,"request":{"user":false,"global":false,"declined":false}}`,
		"apps/a":    ``, "apps/b": ``, "shell": ``,
	} {
		if got[tile] != want {
			t.Errorf("%s: %s, want %s", tile, got[tile], want)
		}
	}
	one := httptest.NewRecorder()
	r = httptest.NewRequest("GET", "/api/xbin/components/apps/pend", nil)
	r.SetPathValue("path", "apps/pend")
	w.s.apiComponent(one, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true})))
	if !strings.Contains(one.Body.String(), `"partition":{"state":"pending"`) {
		t.Errorf("/components/apps/pend: %s", one.Body.String())
	}
}
