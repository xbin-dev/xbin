package runner

import (
	"slices"
	"strings"
	"testing"
)

func TestBackendEnvIsolatedAllowList(t *testing.T) {
	t.Setenv("XBIN_VAULT_PASSPHRASE", "hunter2")
	t.Setenv("SOME_PROVIDER_API_KEY", "sk-secret")
	t.Setenv("HOME", "/home/xbind")
	t.Setenv("LANG", "C.UTF-8")
	t.Setenv("LC_ALL", "C.UTF-8")
	t.Setenv("TZ", "UTC")
	t.Setenv("HTTPS_PROXY", "http://proxy:3128")

	env := backendEnv(true)
	for _, bad := range []string{"XBIN_VAULT_PASSPHRASE=hunter2", "SOME_PROVIDER_API_KEY=sk-secret", "HOME=/home/xbind"} {
		if slices.Contains(env, bad) {
			t.Errorf("isolated backend env leaks %q", bad)
		}
	}
	for _, want := range []string{envSetupPATH, "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "TZ=UTC", "HTTPS_PROXY=http://proxy:3128"} {
		if !slices.Contains(env, want) {
			t.Errorf("isolated backend env lacks %q: %v", want, env)
		}
	}
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") && e != envSetupPATH {
			t.Errorf("host PATH leaked into the sandbox: %q", e)
		}
	}
}

func TestBackendEnvHostDropsDaemonSettings(t *testing.T) {
	t.Setenv("XBIN_VAULT_PASSPHRASE", "hunter2")
	t.Setenv("XBIN_LIMIT_MEM", "4G")
	t.Setenv("HOME", "/home/xbind")

	env := backendEnv(false)
	for _, e := range env {
		if strings.HasPrefix(e, "XBIN_") {
			t.Errorf("unisolated backend env keeps daemon setting %q", e)
		}
	}
	if !slices.Contains(env, "HOME=/home/xbind") {
		t.Errorf("unisolated backend env should keep HOME: %v", env)
	}
}
