package term

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
)

// covers P24 — a session whose tile API is off though it asked for a target
// (a user without the terminal tile-API grant, D17) has no target to echo,
// and its session frame says api:false, so a client never reads the missing
// echo as an xbind that can't target deployments (11-contract §7.4). A
// session that asked for nothing keeps today's frame.
func TestClampedAPIEchoesAPIOff(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "apps", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	deps := &tileDeps{tile: "apps/x", d: TileDeployments{Record: true, Primary: "main", LiveReload: "main", Names: []string{"main", "dev"}}}
	m := &Manager{Root: root, Listen: "127.0.0.1:8642", Tokens: &targetTokens{}, TileDeployments: deps.hook,
		sessions: map[string]*Session{}, envHeld: map[string]bool{}}
	owner := auth.Principal{Owner: true}
	frame := func(requested string) map[string]any {
		t.Helper()
		o := m.openOptsFor(owner, "apps/x", "apps/x", "", "", false) // the API clamped off
		if code, err := m.pickTarget(owner, &o, "apps/x", requested); err != nil {
			t.Fatalf("pick %q: %d %v", requested, code, err)
		}
		if o.api || o.target.env != "" || o.target.Deployment != "" {
			t.Errorf("asked %q with the API off: api=%v target %+v", requested, o.api, o.target)
		}
		var h map[string]any
		if err := json.Unmarshal((&Session{ID: "s", target: o.target}).hello(""), &h); err != nil {
			t.Fatal(err)
		}
		return h
	}
	if h := frame("dev"); h["api"] != false || h["deployment"] != nil || h["targetNote"] != nil {
		t.Errorf("asked dev with the API off: frame %v, want api:false and no echo", h)
	}
	if h := frame(""); h["api"] != nil {
		t.Errorf("asked nothing: frame %v, want today's (no api field)", h)
	}
}
