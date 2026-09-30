package server

import (
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/util"
)

// ifacePartPolicy is partPolicy with one multi http slot on every tile and
// a person's view that adds their personal bind (PartitionInterfaces).
type ifacePartPolicy struct{ partPolicy }

func (ifacePartPolicy) Interfaces(comp string) map[string]any {
	return map[string]any{"mcp": map[string]any{"service": "mcp", "multi": true,
		"endpoints": []map[string]any{{"provider": "apps/shared", "url": "/api/apps/shared"}}}}
}

func (p ifacePartPolicy) PartitionInterfaces(comp string, part util.Partition) map[string]any {
	out := p.Interfaces(comp)
	id, _ := part.User()
	m := out["mcp"].(map[string]any)
	m["endpoints"] = append(m["endpoints"].([]map[string]any),
		map[string]any{"provider": "users/" + id + "/mcp", "url": "/api/users/" + id + "/mcp", "personal": true})
	return out
}

// covers PD-54 — the xbin-interfaces meta of a partitioned tile's document
// (plans/partitions/05 §3): a person's own view lists their personal bind;
// the root token's (global), view-as and a tile that isn't partitioned get
// Interfaces exactly.
func TestPartitionInterfacesMeta(t *testing.T) {
	w := partServer(t)
	w.s.InstallPolicy(ifacePartPolicy{})
	personal := `users/ana/mcp`
	for _, c := range []struct {
		name, url string
		opts      []reqOpt
		personal  bool
	}{
		{"ana", "/c/apps/a/", []reqOpt{w.session("ana")}, true},
		{"the root token", "/c/apps/a/", []reqOpt{cookie(auth.CookieName, w.a.OwnerTokenValue())}, false},
		{"ana on an unpartitioned tile", "/c/apps/b/", []reqOpt{w.session("ana")}, false},
	} {
		rec := w.do(c.url, c.opts...)
		body := rec.Body.String()
		if rec.Code != 200 || !strings.Contains(body, `<meta name="xbin-interfaces"`) || !strings.Contains(body, "apps/shared") {
			t.Fatalf("%s: %d, no interfaces meta with the global bind in\n%s", c.name, rec.Code, body)
		}
		if got := strings.Contains(body, personal); got != c.personal {
			t.Errorf("%s: personal bind in the meta %v, want %v\n%s", c.name, got, c.personal, body)
		}
	}
	tk, err := w.a.NewImpersonationTicket(auth.Principal{Owner: true, Via: "cookie"}, "ana")
	if err != nil {
		t.Fatal(err)
	}
	sid, err := w.a.RedeemImpersonation(tk, auth.Principal{Owner: true, Via: "cookie"}, "", "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	if body := w.do("/c/apps/a/", cookie(auth.CookieName, sid)).Body.String(); strings.Contains(body, personal) {
		t.Errorf("view-as ana: her personal bind in\n%s", body)
	}
}
