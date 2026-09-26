package runner

import (
	"os"
	"strings"
)

// backendEnv is the part of xbind's own environment a backend inherits, before
// the per-backend XBIN_* variables are appended.
//
// Under isolation it is an allow-list: locale, timezone and proxy settings,
// plus the rootfs toolchain PATH (the one terminals and setup scripts get).
// Nothing else of the daemon's environment crosses: XBIN_VAULT_PASSPHRASE
// (loaded from /etc/xbin/xbin.env), provider API keys and XBIN_* daemon
// settings stay out, because a sandboxed backend is assumed hostile
// (plans/isolation.md) and the vault key never enters a sandbox (VD-2).
//
// Without isolation the backend runs as xbind on the host and could read
// /proc/<xbind>/environ anyway, so only the daemon's own XBIN_* settings are
// dropped; PATH, HOME and toolchain variables pass through as before.
func backendEnv(isolated bool) []string {
	var env []string
	if isolated {
		env = append(env, envSetupPATH)
	}
	for _, e := range os.Environ() {
		name, _, _ := strings.Cut(e, "=")
		if isolated && !inheritable(name) || strings.HasPrefix(name, "XBIN_") {
			continue
		}
		env = append(env, e)
	}
	return env
}

// inheritable reports whether a daemon environment variable may reach a
// sandboxed backend.
func inheritable(name string) bool {
	switch name {
	case "LANG", "LANGUAGE", "TZ",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY",
		"http_proxy", "https_proxy", "no_proxy", "all_proxy":
		return true
	}
	return strings.HasPrefix(name, "LC_")
}
