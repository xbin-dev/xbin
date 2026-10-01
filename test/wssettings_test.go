//go:build integration

package test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// covers D180 — an upgrade to the one set of workspace settings, on the
// real binary: a workspace whose data/workspace-settings.json is v0.3.65's
// (base auto-update off) and whose data/workspace-policies.json is
// v0.3.66's (partitionConsent on). The new xbind serves all three from GET
// /workspace-settings and the switches from the /workspace-policies alias;
// a switch set through /workspace-settings is written into the policies
// file too (what a downgrade to v0.3.66 reads); a second xbind on the same
// workspace reads the same and rewrites nothing.
func TestWorkspaceSettingsFromPolicies(t *testing.T) {
	w := filepath.Join(t.TempDir(), "ws")
	if out, err := exec.Command(xbindBin, "init", w).CombinedOutput(); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	data := filepath.Join(w, "data")
	must(t, os.MkdirAll(data, 0o755))
	settingsFile, policiesFile := filepath.Join(data, "workspace-settings.json"), filepath.Join(data, "workspace-policies.json")
	must(t, os.WriteFile(settingsFile, []byte("{\n  \"baseAutoUpdate\": false\n}\n"), 0o644))
	must(t, os.WriteFile(policiesFile, []byte("{\n  \"credentialResetConfirm\": false,\n  \"partitionConsent\": true,\n  \"schema\": 1\n}\n"), 0o644))

	call := func(base, method, path, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return r.StatusCode, strings.TrimSpace(string(b))
	}
	serveOnce(t, w, func(a dlAPI) {
		base := a.url
		if c, b := call(base, "GET", "/api/xbin/workspace-settings", ""); c != 200 || b != `{"baseAutoUpdate":false,"credentialResetConfirm":false,"partitionConsent":true}` {
			t.Fatalf("after the upgrade: %d %s", c, b)
		}
		if c, b := call(base, "GET", "/api/xbin/workspace-policies", ""); c != 200 || b != `{"credentialResetConfirm":false,"partitionConsent":true,"schema":1}` {
			t.Fatalf("the alias: %d %s", c, b)
		}
		if c, b := call(base, "PUT", "/api/xbin/workspace-settings", `{"credentialResetConfirm":true}`); c != 200 || !strings.Contains(b, `"credentialResetConfirm":true`) {
			t.Fatalf("PUT: %d %s", c, b)
		}
		var v0366 map[string]any
		raw, _ := os.ReadFile(policiesFile)
		if err := json.Unmarshal(raw, &v0366); err != nil || v0366["schema"] != float64(1) || v0366["partitionConsent"] != true || v0366["credentialResetConfirm"] != true {
			t.Fatalf("the policies file a downgrade reads: %s (%v)", raw, err)
		}
	})
	before, _ := os.ReadFile(settingsFile)
	if !bytes.Contains(before, []byte(`"policiesFileSha256"`)) || !bytes.Contains(before, []byte(`"partitionConsent": true`)) {
		t.Fatalf("the settings file after the upgrade: %s", before)
	}
	serveOnce(t, w, func(a dlAPI) {
		base := a.url
		if c, b := call(base, "GET", "/api/xbin/workspace-settings", ""); c != 200 || b != `{"baseAutoUpdate":false,"credentialResetConfirm":true,"partitionConsent":true}` {
			t.Fatalf("a second xbind: %d %s", c, b)
		}
	})
	if after, _ := os.ReadFile(settingsFile); !bytes.Equal(before, after) {
		t.Fatalf("a second xbind rewrote the settings file:\n%s\n→\n%s", before, after)
	}
}
