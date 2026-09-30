package boot

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/broker"
)

// covers PD-15 06§6 — the seam guard for partition-mail/1: once GET
// /api/xbin/partitions is served (F7b), its features must list
// broker.PartitionMailFeature, the word by which a client learns that this
// xbind keeps partition mail (the routes are served: TestMail* in broker).
// Until that route exists (404) the guard only logs; a merge that mounts it
// without the word fails here — register it from partition mail's side,
// `func init() { registerPartitionFeature(PartitionMailFeature) }` in
// internal/broker/partitionmail_drop.go (plans/partitions/records/F6.md).
func TestPartitionMailFeatureServed(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	ws := zsWorkspace(t)
	for rel, body := range map[string]string{
		"apps/pm/xbin.json":         `{"runtime":"node","partition":["user","global"],"partitionMail":"/mailbox"}`,
		"apps/pm/backend/server.js": zsNodeServer,
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
	req, err := http.NewRequest("GET", d.url+"/api/xbin/partitions?tile=apps/pm", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+d.owner)
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		t.Logf("GET /api/xbin/partitions isn't served yet (F7b): %s", raw)
		return
	}
	var body struct {
		Features []string `json:"features"`
	}
	if err := json.Unmarshal(raw, &body); resp.StatusCode != http.StatusOK || err != nil {
		t.Fatalf("GET /api/xbin/partitions: %d %s", resp.StatusCode, raw)
	}
	if !slices.Contains(body.Features, broker.PartitionMailFeature) {
		t.Errorf("GET /api/xbin/partitions lists features %q without %q: partition mail is served, so list it", body.Features, broker.PartitionMailFeature)
	}
}
