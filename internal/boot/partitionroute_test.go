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

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers PD-10 PD-29 S13 — identity and routing wired in a booted xbind
// (plans/partitions/02): on a partitioned tile (user + global), the owner
// token reaches the global instance, whose backend hears X-XBin-Partition:
// global; alice's document names her partition, her frame is refused the
// routes whose handlers don't act per partition yet and reaches the neutral
// ones, and her call to the tile's API reaches her partition, which this
// xbind's runner can't start yet (503); an instance token of her partition
// authenticates only while she lives with the same uid (401 once disabled).
func TestPartitionWiring(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	ws := zsWorkspace(t)
	for rel, body := range map[string]string{
		"apps/pa/xbin.json":         `{"runtime":"node","partition":["user","global"]}`,
		"apps/pa/backend/server.js": zsNodeServer,
		"apps/pa/index.html":        "<!doctype html><html><head></head><body>pa</body></html>\n",
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
	if _, err := d.st.Users.Upsert(users.User{ID: "alice", Role: users.RoleUser, Tiles: map[string]string{"apps/pa": users.LevelRead}}, "password1"); err != nil {
		t.Fatal(err)
	}
	do := func(method, path string, hdr map[string]string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(method, d.url+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	owner := map[string]string{"Authorization": "Bearer " + d.owner}
	code, body := do("GET", "/api/apps/pa/hello", owner)
	var echo struct{ Xbin map[string]string }
	_ = json.Unmarshal([]byte(body), &echo)
	if code != 200 || echo.Xbin["x-xbin-partition"] != "global" || echo.Xbin["x-xbin-partition-id"] != "" {
		t.Errorf("the owner token on apps/pa: %d %s", code, body)
	}
	aliceCookie := map[string]string{"Cookie": auth.CookieName + "=" + d.st.Auth.NewSession("alice", "127.0.0.1")}
	if code, body := do("GET", "/c/apps/pa/", aliceCookie); code != 200 || !strings.Contains(body, `<meta name="xbin-partition" content="user:alice">`) {
		t.Errorf("alice's document: %d %s", code, body)
	}
	frame := map[string]string{auth.FrameTokenHeader: d.st.Auth.MintFrameToken("apps/pa", "alice", time.Hour)}
	if code, body := do("GET", "/api/xbin/kv/res:apps/pa/db/k", frame); code != 403 || !strings.Contains(body, "isn't available to a partition's credentials yet") {
		t.Errorf("alice's frame on kv: %d %s", code, body)
	}
	if code, body := do("GET", "/api/xbin/whoami", frame); code != 200 {
		t.Errorf("alice's frame on whoami: %d %s", code, body)
	}
	if code, body := do("GET", "/api/apps/pa/hello", frame); code != 503 || !strings.Contains(body, "no partition runner") {
		t.Errorf("alice's frame on the tile's API: %d %s", code, body)
	}
	if u, _ := d.st.Users.Get("alice"); !users.UIDOK(u.UID) {
		t.Errorf("alice's first partition minted no uid: %q", u.UID)
	}

	u, _ := d.st.Users.Get("alice")
	d.st.Auth.RegisterInstancePartition("tok-alice", "apps/pa", "", util.UserPartition("alice"), u.UID)
	inst := map[string]string{"Authorization": "Bearer tok-alice"}
	if code, body := do("GET", "/api/xbin/whoami", inst); code != 200 {
		t.Errorf("alice's partition's token: %d %s", code, body)
	}
	u.Disabled = true
	if _, err := d.st.Users.Upsert(*u, ""); err != nil {
		t.Fatal(err)
	}
	if code, body := do("GET", "/api/xbin/whoami", inst); code != 401 {
		t.Errorf("alice disabled, her partition's token: %d %s", code, body)
	}
}
