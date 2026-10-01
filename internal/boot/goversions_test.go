package boot

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// covers D166's upgrade check — wired by the boot: its state file is the
// done-marker (a workspace whose check is done never runs it again), its
// alert reaches the owner with the route that dismisses it, GET
// /go-build-versions reports it to admins only, a dismissal hides the
// tile's line and is kept, a dismissal of a tile it doesn't list is 404,
// and POST /go-build-versions/check starts a pass.
func TestGoBuildVersionsAPI(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	ws := filepath.Join(t.TempDir(), "ws")
	state := `{"since":"v0.3.65","done":true,"tiles":{"apps/zs":{"require":["example.com/dep v1.2.0"],"minimal":true,
		"changes":[{"module":"example.com/dep","had":"v1.2.0","now":"v1.0.0"}],"had":["example.com/dep v1.2.0"],"checkedAt":"2026-10-01T00:00:00Z"}}}`
	for rel, body := range map[string]string{
		"xbin.json":                   `{"schema":1}`,
		"apps/zs/xbin.json":           `{"title":"Zero"}`,
		"apps/zs/index.html":          "<!doctype html><html><head></head><body>zs</body></html>\n",
		"data/go-build-versions.json": state,
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
	post := func(path, body string) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest("POST", d.url+path, strings.NewReader(body))
		req.Header.Set("Authorization", owner)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	alert := func() map[string]any {
		t.Helper()
		code, b := d.do(t, "GET", "/api/xbin/alerts", owner)
		var out struct{ Alerts []map[string]any }
		if code != 200 || json.Unmarshal(b, &out) != nil {
			t.Fatalf("alerts: %d %s", code, b)
		}
		for _, a := range out.Alerts {
			if a["kind"] == "go-build-versions" {
				return a
			}
		}
		return nil
	}
	a := alert()
	if a == nil || a["level"] != "warn" || a["dismiss"] != "/go-build-versions/dismiss" ||
		a["message"] != "apps/zs builds with older dependency versions since v0.3.65 (each Go tile now builds with its own go.mod's versions): add `require example.com/dep v1.2.0` to apps/zs's go.mod to keep what it had" {
		t.Fatalf("the owner's alert: %+v", a)
	}
	if code, _ := d.do(t, "GET", "/api/xbin/go-build-versions", ""); code != http.StatusUnauthorized && code != http.StatusForbidden {
		t.Errorf("an anonymous report: %d", code)
	}
	code, b := d.do(t, "GET", "/api/xbin/go-build-versions", owner)
	var rep struct {
		Since, Running any
		Done           bool
		Tiles          []struct {
			Tile      string
			Require   []string
			Dismissed bool
		}
	}
	if code != 200 || json.Unmarshal(b, &rep) != nil || !rep.Done || rep.Since != "v0.3.65" || len(rep.Tiles) != 1 || rep.Tiles[0].Tile != "apps/zs" {
		t.Fatalf("report: %d %s", code, b)
	}
	if code, b := post("/api/xbin/go-build-versions/dismiss", `{"tile":"apps/nope"}`); code != http.StatusNotFound {
		t.Errorf("dismiss an unlisted tile: %d %s", code, b)
	}
	if code, b := post("/api/xbin/go-build-versions/dismiss", ""); code != 200 || !strings.Contains(string(b), `"dismissed":true`) {
		t.Fatalf("dismiss: %d %s", code, b)
	}
	if a := alert(); a != nil {
		t.Errorf("a dismissed alert: %+v", a)
	}
	if b, _ := os.ReadFile(filepath.Join(ws, "data", "go-build-versions.json")); !strings.Contains(string(b), `"dismissed": true`) {
		t.Errorf("the dismissal isn't kept:\n%s", b)
	}
	// a pass over a workspace with no Go tile: done, nothing listed
	if code, b := post("/api/xbin/go-build-versions/check", ""); code != http.StatusAccepted {
		t.Fatalf("check: %d %s", code, b)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, b := d.do(t, "GET", "/api/xbin/go-build-versions", owner)
		if strings.Contains(string(b), `"running":false`) && strings.Contains(string(b), `"tiles":[]`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the pass never ended: %s", b)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
