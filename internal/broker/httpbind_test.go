package broker

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
)

// Binding a consumer's http slot restarts the consumer (its XBIN_IFACE_*
// env changed) and nothing else: not the provider — whose other consumers'
// calls in flight a restart would cut (a sandbox manager's relayed
// terminals, its long polls) — and not the consumer's net provider, whose
// roster didn't change. A net slot's binding still restarts its provider.
func TestHTTPBindingRestartsOnlyTheConsumer(t *testing.T) {
	root := t.TempDir()
	for rel, content := range map[string]string{
		"xbin.json":           `{"schema":1}`,
		"apps/mgr/xbin.json":  `{"runtime":"go","provides":{"sandboxes":{"kind":"http","service":"sandbox-manager","role":"consumer"}},"expose":{"roles":{"consumer":"use it"}}}`,
		"apps/cons/xbin.json": `{"runtime":"go","interfaces":{"net":{"kind":"net"},"sandboxes":{"kind":"http","service":"sandbox-manager","multi":true}}}`,
		"apps/vpn/xbin.json":  `{"runtime":"go","provides":{"egress":{"kind":"net"}}}`,
	} {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(reg, events.NewHub(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	var restarted []string
	b.OnGrantChange = func(comp string) { restarted = append(restarted, comp) }
	admin := auth.Principal{Owner: true}
	bind := func(method, body string, want []string) {
		t.Helper()
		restarted = nil
		if w := call(t, b.apiBindingSet, admin, method, "/bindings", body, nil); w.Code != 200 {
			t.Fatalf("%s %s: %d %s", method, body, w.Code, w.Body)
		}
		slices.Sort(restarted)
		if !slices.Equal(restarted, want) {
			t.Errorf("%s %s restarted %v, want %v", method, body, restarted, want)
		}
	}
	bind("POST", `{"component":"apps/cons","slot":"net","provider":"apps/vpn"}`, []string{"apps/cons", "apps/vpn"})
	bind("POST", `{"component":"apps/cons","slot":"sandboxes","provider":"apps/mgr"}`, []string{"apps/cons"})
	bind("DELETE", `{"component":"apps/cons","slot":"sandboxes"}`, []string{"apps/cons"})
	bind("DELETE", `{"component":"apps/cons","slot":"net"}`, []string{"apps/cons", "apps/vpn"})
}
