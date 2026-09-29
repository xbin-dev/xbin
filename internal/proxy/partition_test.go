package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/ingress"
	"github.com/xbin-dev/xbin/internal/registry"
)

// covers PD-50 — a tile whose partition mode switch is pending, or whose
// request is invalid, answers 409 with why and its state, and never reaches
// the runner (nil here: reaching it would panic); a caller the policy
// refuses still gets 403, and public ingress today's 503.
func TestPartitionGate(t *testing.T) {
	root := t.TempDir()
	for rel, body := range map[string]string{
		"apps/pend/xbin.json": `{"runtime":"go","partition":["user"],"partitionNote":"<b>chats</b>"}`,
		"apps/bad/xbin.json":  `{"runtime":"go","partition":["org"]}`,
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg := &registry.Registry{Root: root, PartitionModes: func(a registry.PartitionAsk) registry.PartitionMode {
		switch {
		case a.Invalid != "":
			return registry.PartitionMode{State: registry.PartitionInvalid}
		case a.Requested != nil: // held: data, R unpartitioned
			return registry.PartitionMode{State: registry.PartitionPending, Request: &registry.PartitionRequest{Spec: a.Requested}}
		}
		return registry.PartitionMode{}
	}}
	if err := reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	allow := true
	px := &Proxy{Reg: reg, Policy: func(auth.Principal, *registry.Component) (string, bool) { return "admin", allow }}

	type answer struct {
		Error     string
		Partition struct {
			State    string
			From, To *registry.PartitionSpec
			Error    string
		}
	}
	call := func(target string) (int, answer) {
		rec := httptest.NewRecorder()
		px.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, target, strings.NewReader("{}")))
		var a answer
		_ = json.Unmarshal(rec.Body.Bytes(), &a)
		return rec.Code, a
	}
	code, a := call("/api/apps/pend/x")
	want := "apps/pend is paused: a partition mode switch is requested (unpartitioned → user); a manager of apps/pend must switch (deleting all its data) or keep the current mode"
	if code != http.StatusConflict || a.Error != want || a.Partition.State != "pending" ||
		a.Partition.From == nil || !a.Partition.From.IsZero() || a.Partition.To == nil || *a.Partition.To != (registry.PartitionSpec{User: true}) {
		t.Errorf("pending: %d %+v", code, a)
	}
	code, a = call("/api/apps/bad/")
	if code != http.StatusConflict || a.Partition.State != "invalid" || !strings.Contains(a.Error, `partition: unknown word "org"`) ||
		!strings.HasPrefix(a.Error, "apps/bad doesn't run") {
		t.Errorf("invalid: %d %+v", code, a)
	}
	allow = false
	if code, a = call("/api/apps/pend/x"); code != http.StatusForbidden || a.Partition.State != "" {
		t.Errorf("a refused caller: %d %+v, want 403 without the partition state", code, a)
	}
	for _, tile := range []string{"apps/pend", "apps/bad"} {
		rec := httptest.NewRecorder()
		px.ForwardIngress(rec, httptest.NewRequest(http.MethodGet, "/", nil), ingress.Route{Component: tile, Host: "x.example.com"}, true)
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "not being served right now") {
			t.Errorf("ingress to %s: %d %q", tile, rec.Code, rec.Body.String())
		}
	}
}
