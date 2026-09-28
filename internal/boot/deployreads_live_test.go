package boot

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/confine"
)

// covers P15 P16 P18 SC-AUDIT — the deployments API's reads wired to the
// plane in the running daemon (auth on, the owner's bearer): a backend tile
// without --isolate reads its pause refused (kind policy); after live reload of
// a static tile pauses, the state names the paused checkpoint and the last
// deploy, the deploy log and its one entry (held with wait) answer, the
// diff from main to the work tree names an edit (patch and stat, with the
// resolved checkpoints), and the checkpoint remote serves the view
// repository's HEAD and 404s off its allow-list.
func TestDeploymentReadsLive(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on this host")
	}
	if out, err := exec.Command("find", "--version").CombinedOutput(); err != nil || !strings.Contains(string(out), "GNU") {
		t.Skip("no GNU find on this host")
	}
	if confine.Isolated() {
		t.Skip("confinement is on: the store's tools run directly here")
	}
	ws := zsWorkspace(t)
	d := zsBoot(t, ws)
	req := func(method, path, body string) (int, http.Header, []byte) {
		t.Helper()
		r, err := http.NewRequest(method, d.url+path, bytes.NewReader([]byte(body)))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+d.owner)
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(r)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header, b
	}
	obj := func(what string, code int, b []byte) map[string]any {
		t.Helper()
		var m map[string]any
		if code != http.StatusOK || json.Unmarshal(b, &m) != nil {
			t.Fatalf("%s = %d %s", what, code, b)
		}
		return m
	}

	// Without --isolate a backend tile can't be paused: the caller's can
	// and the tile's allowed say so, as the request would be judged (P18).
	code, _, b := req("GET", "/api/xbin/deployments?tile=apps/zsnode", "")
	nodeState := obj("the node tile's state", code, b)
	caller, _ := nodeState["caller"].(map[string]any)
	cans, _ := caller["can"].(map[string]any)
	allowed, _ := nodeState["allowed"].(map[string]any)
	for what, c := range map[string]any{"caller.can.pause": cans["pause"], "allowed.pause": allowed["pause"]} {
		if m, _ := c.(map[string]any); m["ok"] != false || m["kind"] != "policy" || !strings.Contains(m["why"].(string), "--isolate") {
			t.Errorf("%s = %v, want the isolation refusal", what, c)
		}
	}

	code, _, b = req("POST", "/api/xbin/deployments/live-reload/pause", `{"tile":"apps/zs"}`)
	obj("pause", code, b)
	code, _, b = req("GET", "/api/xbin/deployments/log?tile=apps/zs&id=1&wait=5", "")
	entry, _ := obj("the pause's entry", code, b)["entry"].(map[string]any)
	if entry["how"] != "pause" || entry["result"] != "ok" {
		t.Fatalf("the pause's entry = %v", entry)
	}
	id, _ := entry["checkpoint"].(string)
	if !strings.HasPrefix(id, "c:") {
		t.Fatalf("the pause's checkpoint = %q", id)
	}
	code, _, b = req("GET", "/api/xbin/deployments/log?tile=apps/zs", "")
	if es, _ := obj("the log", code, b)["entries"].([]any); len(es) != 1 {
		t.Errorf("the log has %d entries, want 1", len(es))
	}

	code, _, b = req("GET", "/api/xbin/deployments?tile=apps/zs", "")
	st := obj("the state", code, b)
	rows, _ := st["deployments"].([]any)
	if len(rows) != 1 {
		t.Fatalf("the state's deployments = %v", st["deployments"])
	}
	row := rows[0].(map[string]any)
	cp, _ := row["checkpoint"].(map[string]any)
	last, _ := row["lastDeploy"].(map[string]any)
	if cp["id"] != id || cp["at"] == nil || cp["by"] != "owner" || last["how"] != "pause" {
		t.Errorf("main's checkpoint %v, last deploy %v", cp, last)
	}
	if wt, _ := st["workTree"].(map[string]any); wt["since"] != id || wt["changed"] != float64(0) {
		t.Errorf("workTree = %v", st["workTree"])
	}

	if err := os.WriteFile(filepath.Join(ws, "apps", "zs", "index.html"), []byte("<p>edited</p>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, h, b := req("GET", "/api/xbin/deployments/diff?tile=apps/zs", "")
	if code != http.StatusOK || !strings.HasPrefix(h.Get("Content-Type"), "text/x-diff") ||
		h.Get("X-XBin-Checkpoint-From") != id || !strings.HasPrefix(h.Get("X-XBin-Checkpoint-To"), "c:") ||
		!strings.Contains(string(b), "+<p>edited</p>") {
		t.Errorf("the diff = %d %v\n%s", code, h, b)
	}
	code, _, b = req("GET", "/api/xbin/deployments/diff?tile=apps/zs&stat=1", "")
	if files, _ := obj("the stat", code, b)["files"].([]any); len(files) != 1 || files[0].(map[string]any)["path"] != "index.html" {
		t.Errorf("the stat's files = %v", files)
	}

	code, _, b = req("GET", "/api/xbin/checkpoints/apps/zs.git/HEAD", "")
	if code != http.StatusOK || !strings.Contains(string(b), "refs/heads/deploy/main") {
		t.Errorf("the remote's HEAD = %d %q", code, b)
	}
	if code, _, _ = req("GET", "/api/xbin/checkpoints/apps/zs.git/config", ""); code != http.StatusNotFound {
		t.Errorf("the remote's config = %d, want 404", code)
	}
}
