package term

// sandboxenv.go — a sandboxed terminal session's environment: the rootfs
// PATH, the user's HOME, the session's tile-scoped token, and an XBIN_URL
// the session can reach from its own netns.

import (
	"net"
	"strings"

	"github.com/xbin-dev/xbin/internal/sandbox"
)

// hostForward maps the xbind listen port on the relay gateway IP to xbind on
// host loopback, so an internet-scope terminal (in its own netns) can still
// reach the workspace controller via XBIN_URL (bx/curl) without any host
// interface being exposed. Nil if the listen addr can't be parsed.
func (m *Manager) hostForward() map[int]string {
	_, portStr, err := net.SplitHostPort(m.Listen)
	if err != nil {
		return nil
	}
	port, err := net.LookupPort("tcp", portStr)
	if err != nil {
		return nil
	}
	return map[int]string{port: "127.0.0.1:" + portStr}
}

// sandboxEnv is the terminal env inside the rootfs: PATH points at the rootfs
// toolchains (not the host's), the session user's $HOME (homes/<user>), the
// session's tile-scoped XBIN_TOKEN (plans/terminal-tokens.md), plus
// XBIN_URL/WORKSPACE from m.Env(). In internet scope the netns can't reach
// xbind's 127.0.0.1 listener, so XBIN_URL is rewritten to the relay gateway
// host-forward.
func (m *Manager) sandboxEnv(rel string, relayNet bool, homeDir, termTok string) []string {
	env := []string{
		"TERM=xterm-256color", "COLORTERM=truecolor",
		"XBIN_COMPONENT=" + rel,
		"HOME=" + homeDir,
		"IN_SANDBOX=1", // scripts/agents can tell they're in the terminal sandbox
		"IS_SANDBOX=1", // the spelling agent CLIs (Claude Code) actually check
		"LANG=C.UTF-8",
		"PATH=/usr/local/go/bin:/usr/local/node/bin:/usr/local/bun/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}
	var xbinURL string
	if relayNet {
		if _, port, err := net.SplitHostPort(m.Listen); err == nil {
			xbinURL = "http://" + net.JoinHostPort(sandbox.GatewayIP, port)
		}
	}
	token := termTok // the session's tile-scoped credential, for env + git
	var effURL string
	if m.Env != nil {
		for _, e := range m.Env() {
			if strings.HasPrefix(e, "PATH=") {
				continue // the rootfs PATH above wins
			}
			if strings.HasPrefix(e, "HOME=") {
				continue // the per-user HOME above wins (getenv is first-match)
			}
			if strings.HasPrefix(e, "XBIN_TOKEN=") {
				continue // only the per-session terminal token goes in
			}
			if v, ok := strings.CutPrefix(e, "XBIN_URL="); ok {
				if xbinURL != "" {
					continue // rewritten below to the relay gateway
				}
				effURL = v
			}
			env = append(env, e)
		}
	}
	if token != "" {
		env = append(env, "XBIN_TOKEN="+token)
	}
	if xbinURL != "" {
		env = append(env, "XBIN_URL="+xbinURL)
		effURL = xbinURL
	}
	// Make the SDK's gateway host `http://xbin/…` work for raw git/curl in the
	// terminal too: git's env-config rewrites it to the reachable XBIN_URL and
	// attaches the session's tile-scoped bearer, pinned to that URL so the
	// token never goes anywhere else. This is what lets a template instance's
	// `template` remote fetch (plans/agent-v2.md); `curl http://xbin/…` works too.
	if effURL != "" && token != "" {
		env = append(env,
			"GIT_CONFIG_COUNT=2",
			"GIT_CONFIG_KEY_0=url."+effURL+"/.insteadOf", "GIT_CONFIG_VALUE_0=http://xbin/",
			"GIT_CONFIG_KEY_1=http."+effURL+"/.extraHeader", "GIT_CONFIG_VALUE_1=Authorization: Bearer "+token,
		)
	}
	return env
}
