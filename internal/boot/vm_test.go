package boot

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/vm"
)

// PUT /vm/policy merges the body onto the stored policy: a field the body
// leaves out keeps its value, so an admin console from before tile
// sandboxes can't zero tiles (D120). A refused body changes nothing.
func TestVMPolicyPutMerges(t *testing.T) {
	quiet(t)
	st := &State{VM: &vm.Manager{Root: t.TempDir()}}
	owner := auth.Principal{Owner: true}
	put := func(p auth.Principal, body string) (int, map[string]any) {
		t.Helper()
		r := httptest.NewRequest("PUT", "/api/xbin/vm/policy", strings.NewReader(body))
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		st.putVMPolicy(w, r)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	if code, _ := put(owner, `{"terminals":true,"tiles":true,"tilesBudgetMiB":4096,"tilesEmulated":true}`); code != 200 {
		t.Fatalf("set tiles: %d", code)
	}
	// what the admin console sent before tile sandboxes: no tiles fields
	code, out := put(owner, `{"terminals":true,"backends":true,"memMiB":1024,"vcpus":0,"maxVMs":0,"budgetMiB":8192,"diskGiB":0}`)
	if code != 200 {
		t.Fatalf("older console: %d", code)
	}
	want := vm.Policy{Terminals: true, Backends: true, MemMiB: 1024, BudgetMiB: 8192, Tiles: true, TilesBudgetMiB: 4096, TilesEmulated: true}
	if got := st.VM.StoredPolicy(); got != want {
		t.Fatalf("a body without tiles zeroed them: %+v", got)
	}
	if s, _ := out["stored"].(map[string]any); s["tiles"] != true || s["memMiB"] != float64(1024) {
		t.Fatalf("the response's stored policy: %v", out["stored"])
	}
	if p, _ := out["policy"].(map[string]any); p["tilesBudgetMiB"] != float64(4096) || p["vcpus"] != float64(vm.DefaultVCPUs) {
		t.Fatalf("the response's effective policy: %v", out["policy"])
	}
	// one field turns one thing off; zero sizes still mean the default
	if code, _ := put(owner, `{"tiles":false,"tilesBudgetMiB":0}`); code != 200 {
		t.Fatalf("tiles off: %d", code)
	}
	want.Tiles, want.TilesBudgetMiB = false, 0
	if got := st.VM.StoredPolicy(); got != want {
		t.Fatalf("tiles off: %+v", got)
	}
	if got := st.VM.Policy(); got.TilesBudgetMiB != 4096 {
		t.Fatalf("the sub-budget's default is half the budget: %+v", got)
	}

	// refused: nothing changes
	for _, c := range []struct {
		p    auth.Principal
		body string
		code int
	}{
		{auth.Principal{}, `{"tiles":true}`, 403},
		{owner, `{"tiles":true,"nope":1}`, 400},
		{owner, `{"tiles":true,"tilesBudgetMiB":9000}`, 400}, // over budgetMiB
		{owner, `{"tiles":true,"budgetMiB":1024,"tilesBudgetMiB":2048}`, 400},
		{owner, `{"tiles":true,"memMiB":1}`, 400},
		{owner, `not json`, 400},
	} {
		if code, _ := put(c.p, c.body); code != c.code {
			t.Errorf("%s: %d, want %d", c.body, code, c.code)
		}
		if got := st.VM.StoredPolicy(); got != want {
			t.Fatalf("%s changed the policy: %+v", c.body, got)
		}
	}

	// GET /vm: admins see what tile sandboxes hold next to what all VMs hold
	get := func(p auth.Principal) map[string]any {
		r := httptest.NewRequest("GET", "/api/xbin/vm", nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		st.getVM(w, r)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	if out := get(owner); out["used"] == nil || out["usedTiles"] == nil {
		t.Fatalf("admin GET /vm: %v", out)
	}
	if out := get(auth.Principal{}); out["used"] != nil || out["usedTiles"] != nil || out["policy"] == nil {
		t.Fatalf("non-admin GET /vm: %v", out)
	}

	// without isolation there is no manager
	noVM := &State{}
	r := httptest.NewRequest("PUT", "/api/xbin/vm/policy", strings.NewReader(`{}`))
	r = r.WithContext(auth.WithPrincipal(r.Context(), owner))
	w := httptest.NewRecorder()
	noVM.putVMPolicy(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("no isolation: %d", w.Code)
	}
}
