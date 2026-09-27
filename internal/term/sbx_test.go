package term

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/sbx"
)

// An agent session is listed while it lives; a restart swaps it for the new
// one (never both); the status list says what kind it is and whether it is a
// VM.
func TestSandboxRegistrySessions(t *testing.T) {
	r := newAgentRig(t)
	m := r.m
	reg := sbx.New()
	m.Sandboxes = reg
	owner := auth.Principal{Owner: true}
	info, code, err := m.OpenAgent(owner, "apps/x", "", "fake", "", "", "", nil)
	if err != nil || code != 200 {
		t.Fatalf("open: %d %v", code, err)
	}
	l := reg.List(sbx.Filter{})
	if len(l) != 1 || l[0].ID != info.ID || l[0].Kind != sbx.Agent || l[0].Tile != "apps/x" || l[0].User != "owner" ||
		l[0].Mode != sbx.Host || l[0].Label != "fake" || l[0].PID == 0 {
		t.Fatalf("entries: %+v", l)
	}
	if rows := m.List(); len(rows) != 1 || rows[0]["kind"] != KindAgent || rows[0]["vm"] != false {
		t.Fatalf("List: %+v", rows)
	}
	r.until(t, func(e SessionEvent) bool {
		return e.ID == info.ID && e.Type == agent.EvStatus && edata(e.Event)["status"] == agent.StatusIdle
	})
	fresh, _, _, err := m.RestartAgent(owner, info.ID, "none", "", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if l := reg.List(sbx.Filter{}); len(l) != 1 || l[0].ID != fresh.ID {
		t.Fatalf("after the restart: %+v", l)
	}
	m.Kill(fresh.ID)
	waitClose(t, r.change, "close:"+fresh.ID)
	if l := reg.List(sbx.Filter{}); len(l) != 0 {
		t.Fatalf("after the kill: %+v", l)
	}
	if f := reg.Failures(sbx.Filter{}); len(f) != 0 {
		t.Fatalf("a kill is not a failure: %+v", f)
	}
}

// A VM asked for where there is no isolation is refused — and the refusal
// is recorded for the admin.
func TestSandboxRegistryVMRefused(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "apps", "mine"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := NewManager(root, nil)
	reg := sbx.New()
	m.Sandboxes = reg
	r := httptest.NewRequest("GET", "/ws/term?cwd=apps/mine&vm=1", nil)
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true}))
	w := httptest.NewRecorder()
	m.ServeWS(w, r)
	if w.Code != 400 {
		t.Fatalf("vm=1 without isolation: %d", w.Code)
	}
	f := reg.Failures(sbx.Filter{})
	if len(f) != 1 || f[0].Stage != sbx.Refused || f[0].Kind != sbx.Terminal || f[0].Mode != sbx.VM || f[0].Tile != "apps/mine" ||
		!strings.Contains(f[0].Error, "isolation") {
		t.Fatalf("failures: %+v", f)
	}
	if len(reg.List(sbx.Filter{})) != 0 {
		t.Fatal("a refused session is listed")
	}
}
