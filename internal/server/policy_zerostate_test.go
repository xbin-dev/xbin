package server

import (
	"errors"
	"reflect"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers D119c PO-1 SC-ZERO — TestNoopPolicyZeroState (15-test-plan §2.7): the
// server's deployment questions, answered by NoopPolicy (the policy of a
// server with none installed), reproduce today. Every tile serves its work
// tree (c.Dir, unpinned) as its one deployment, main; no other name exists,
// and main is the only deployment anyone addresses. The six questions the
// policy answered before keep their answers.
func TestNoopPolicyZeroState(t *testing.T) {
	var pol Policy = (&Server{}).policy()
	if _, ok := pol.(NoopPolicy); !ok {
		t.Fatalf("a server with no policy installed answers with %T, want NoopPolicy", pol)
	}

	principals := map[string]auth.Principal{
		"none":     {},
		"owner":    {Owner: true, Via: "bearer"},
		"user":     {UserID: "ana", User: &users.User{ID: "ana"}, Via: "session"},
		"frame":    {Component: "apps/crm", Via: "frame"},
		"instance": {Component: "apps/crm", Via: "instance"},
		"terminal": {Component: "apps/crm", UserID: "ana", Via: "terminal"},
	}

	t.Run("today's answers", func(t *testing.T) {
		for name, p := range principals {
			if pol.IsAdmin(p) {
				t.Errorf("IsAdmin(%s) = true, want false", name)
			}
			if pol.BusAllows(p, events.Event{Type: "bus", Topic: "res:apps/crm/events/x"}) {
				t.Errorf("BusAllows(%s) = true, want false", name)
			}
		}
		if got := pol.OwnerOf("apps/crm"); got != "" {
			t.Errorf("OwnerOf = %q, want \"\"", got)
		}
		if got := pol.Interfaces("apps/crm"); got != nil {
			t.Errorf("Interfaces = %v, want nil", got)
		}
		if got := pol.SandboxExtras("apps/crm"); got != nil {
			t.Errorf("SandboxExtras = %v, want nil", got)
		}
		if pol.CodeReadGrant("apps/a", "apps/crm") {
			t.Error("CodeReadGrant = true, want false")
		}
	})

	// The work tree of every kind of tile: plain, nested, a '+' in its own
	// last segment (a directory, never a deployment), and the workspace root's
	// shell.
	comps := []*registry.Component{
		{Path: "apps/crm", Dir: "/ws/apps/crm"},
		{Path: "apps/crm/widgets", Dir: "/ws/apps/crm/widgets"},
		{Path: "apps/crm+dev", Dir: "/ws/apps/crm+dev"},
		{Path: "apps/c++", Dir: "/ws/apps/c++"},
		{Path: "shell", Dir: "/ws/shell"},
	}

	t.Run("CodeRoot: the primary is main, following the work tree", func(t *testing.T) {
		for _, c := range comps {
			for _, dep := range []string{"", util.MainDeployment} {
				root, pinned, err := pol.CodeRoot(c, dep)
				if root != c.Dir || pinned || err != nil {
					t.Errorf("CodeRoot(%s, %q) = (%q, %v, %v), want (%q, false, nil)", c.Path, dep, root, pinned, err, c.Dir)
				}
			}
		}
	})

	t.Run("CodeRoot: no other deployment exists", func(t *testing.T) {
		c := comps[0]
		for _, dep := range []string{"dev", "Main", "main2", "+dev", "dev/x"} {
			root, pinned, err := pol.CodeRoot(c, dep)
			if root != "" || pinned || !errors.Is(err, util.ErrNoDeployment) {
				t.Errorf("CodeRoot(%s, %q) = (%q, %v, %v), want (\"\", false, ErrNoDeployment)", c.Path, dep, root, pinned, err)
			}
		}
		// 11-contract §1.14's "unknown deployment" text.
		if _, _, err := pol.CodeRoot(c, "dev"); err == nil || err.Error() != `apps/crm has no deployment "dev"` {
			t.Errorf("CodeRoot(apps/crm, dev) error = %v, want `apps/crm has no deployment \"dev\"`", err)
		}
	})

	t.Run("HasDeployment: main only", func(t *testing.T) {
		for _, c := range comps {
			if !pol.HasDeployment(c.Path, util.MainDeployment) {
				t.Errorf("HasDeployment(%s, main) = false", c.Path)
			}
			for _, name := range []string{"", "dev", "Main", "main "} {
				if pol.HasDeployment(c.Path, name) {
					t.Errorf("HasDeployment(%s, %q) = true", c.Path, name)
				}
			}
		}
	})

	t.Run("Addressable: main only, for everyone", func(t *testing.T) {
		for name, p := range principals {
			for _, c := range comps {
				got := pol.Addressable(p, c.Path)
				if !reflect.DeepEqual(got, []string{util.MainDeployment}) {
					t.Errorf("Addressable(%s, %s) = %q, want [main]", name, c.Path, got)
				}
				got[0] = "changed" // each answer is the caller's own
			}
		}
		if got := pol.Addressable(auth.Principal{}, "apps/crm"); got[0] != util.MainDeployment {
			t.Errorf("an earlier caller's edit leaked into Addressable: %q", got)
		}
	})
}
