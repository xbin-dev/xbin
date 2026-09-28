package term

import (
	"errors"
	"testing"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
)

// covers D127p — an agent restart takes a requested target (11-contract
// §7.4): onto dev it echoes dev; a refused target (404 unknown, 400 not a
// name, 403 a protected primary) answers before the session ends, which
// keeps running; a restart that names nothing takes the default and echoes
// nothing.
func TestRestartAgentOntoTarget(t *testing.T) {
	r := newAgentRig(t)
	m := r.m
	deps := &tileDeps{tile: "apps/x", d: TileDeployments{Record: true, Primary: "main", LiveReload: "dev", Names: []string{"main", "dev"}}}
	m.TileDeployments = deps.hook
	owner := auth.Principal{Owner: true}
	idle := func(id string) {
		t.Helper()
		r.until(t, func(e SessionEvent) bool {
			return e.ID == id && e.Type == agent.EvStatus && edata(e.Event)["status"] == agent.StatusIdle
		})
	}
	info, code, err := m.OpenAgentWith(owner, AgentOpen{Cwd: "apps/x", Provider: "fake"})
	if err != nil || code != 200 || info.Deployment != "" {
		t.Fatalf("open: %+v %d %v", info, code, err)
	}
	idle(info.ID)
	moved, _, code, err := m.RestartAgentOnto(owner, info.ID, "", "", true, nil, "dev")
	if err != nil || code != 200 || moved.Deployment != "dev" {
		t.Fatalf("a restart onto dev: %+v %d %v", moved, code, err)
	}
	idle(moved.ID)
	for dep, want := range map[string]int{"nope": 404, "Bad": 400} {
		if _, _, c, err := m.RestartAgentOnto(owner, moved.ID, "", "", true, nil, dep); c != want || err == nil {
			t.Errorf("a restart onto %q: %d %v, want %d", dep, c, err, want)
		}
		if _, ok := m.Info(moved.ID); !ok {
			t.Fatalf("a restart refused onto %q ended the session", dep)
		}
	}
	deps.set(TileDeployments{Record: true, Primary: "main", Protected: true, LiveReload: "dev", Names: []string{"main", "dev"}})
	if _, _, c, err := m.RestartAgentOnto(owner, moved.ID, "", "", true, nil, "main"); c != 403 || !errors.Is(err, ErrTargetProtected) {
		t.Errorf("a restart onto the protected primary: %d %v, want 403", c, err)
	}
	if _, ok := m.Info(moved.ID); !ok {
		t.Fatal("a restart refused onto the protected primary ended the session")
	}
	deps.set(TileDeployments{Record: true, Primary: "main", LiveReload: "dev", Names: []string{"main", "dev"}})
	back, _, code, err := m.RestartAgent(owner, moved.ID, "", "", true, nil)
	if err != nil || code != 200 || back.Deployment != "" {
		t.Fatalf("a restart onto the default: %+v %d %v", back, code, err)
	}
	m.Kill(back.ID)
	waitClose(t, r.change, "close:"+back.ID)
}
