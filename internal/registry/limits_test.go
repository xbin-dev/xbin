package registry

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/limits"
)

// covers D203 — the manifest's "limits" key is read leniently: values that
// parse ask (memory rounded up to MiB, disk to GiB), every problem — a bare
// number, an unknown unit or key, a size below the minimum, a malformed
// sandboxes object, a key that isn't an object at all — is a ManifestErr
// warning and ignored; the manifest still parses, its other keys intact, and
// "vm" parses as before (its sizes now read as the VM kinds' ask).
func TestLimitsAskLenient(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"apps/ok/xbin.json": `{"runtime":"go","limits":{"memory":"1.5g","disk":"100GiB",
			"sandboxes":{"memory":"16G","vcpus":8,"disk":"200G"}}}`,
		"apps/bad/xbin.json": `{"runtime":"go","uses":[{"target":"apps/ok","role":"reader"}],
			"limits":{"memory":4096,"disk":"1X","pids":100,"sandboxes":{"vcpus":0,"memory":"64M","gpu":1}}}`,
		"apps/notobj/xbin.json": `{"runtime":"go","limits":"lots"}`,
		"apps/sbx/xbin.json":    `{"runtime":"go","limits":{"sandboxes":[1]}}`,
		"apps/vm/xbin.json":     `{"runtime":"go","vm":{"memory":4096,"vcpus":4}}`,
		"apps/none/xbin.json":   `{"runtime":"go","limits":null}`,
	})
	r, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := r.Component("apps/ok")
	want := map[limits.Kind]int64{limits.MemMiB: 1536, limits.DiskGiB: 100, limits.SbxMemMiB: 16 << 10, limits.SbxVCPUs: 8, limits.SbxDiskGiB: 200}
	if c.ManifestErr != "" || c.Manifest.Limits == nil || !reflect.DeepEqual(c.Manifest.Limits.Values, want) {
		t.Fatalf("apps/ok: %q %+v", c.ManifestErr, c.Manifest.Limits)
	}
	for k, v := range want {
		if got := c.Manifest.LimitAsk(k); got != v {
			t.Errorf("LimitAsk(%s) = %d, want %d", k, got, v)
		}
	}

	c, _ = r.Component("apps/bad")
	if !c.HasBackend() || len(c.Manifest.Uses) != 1 {
		t.Fatalf("apps/bad: the rest of the manifest is lost: %+v", c.Manifest)
	}
	if c.Manifest.Limits == nil || len(c.Manifest.Limits.Values) != 0 {
		t.Errorf("apps/bad: no value should ask: %+v", c.Manifest.Limits)
	}
	for _, frag := range []string{`"limits".memory: use a size like "4G"`, `"limits".disk: "1X"`, `"limits".pids: not a limit`,
		`"limits".sandboxes.vcpus: a whole number`, `"limits".sandboxes.memory: at least 128 MiB`, `"limits".sandboxes.gpu: not a limit`} {
		if !strings.Contains(c.ManifestErr, frag) {
			t.Errorf("apps/bad: ManifestErr %q lacks %q", c.ManifestErr, frag)
		}
	}
	for _, p := range []string{"apps/notobj", "apps/sbx"} {
		c, _ := r.Component(p)
		if !c.HasBackend() || !strings.Contains(c.ManifestErr, "must be an object") {
			t.Errorf("%s: %q", p, c.ManifestErr)
		}
	}

	c, _ = r.Component("apps/vm")
	if c.ManifestErr != "" || c.Manifest.LimitAsk(limits.VMMemMiB) != 4096 || c.Manifest.LimitAsk(limits.VMVCPUs) != 4 || c.Manifest.LimitAsk(limits.MemMiB) != 0 {
		t.Errorf("apps/vm: %q %+v", c.ManifestErr, c.Manifest.VM)
	}
	c, _ = r.Component("apps/none")
	if c.ManifestErr != "" || c.Manifest.Limits != nil || c.Manifest.LimitAsk(limits.MemMiB) != 0 {
		t.Errorf("apps/none: %q %+v", c.ManifestErr, c.Manifest.Limits)
	}

	// The key round-trips in the manifest's own form.
	var m Manifest
	if err := json.Unmarshal([]byte(`{"limits":{"memory":"2G","sandboxes":{"vcpus":2}}}`), &m); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(m)
	var back Manifest
	if err := json.Unmarshal(b, &back); err != nil || !reflect.DeepEqual(back.Limits.Values, m.Limits.Values) {
		t.Errorf("round trip: %s → %+v (%v)", b, back.Limits, err)
	}
}
