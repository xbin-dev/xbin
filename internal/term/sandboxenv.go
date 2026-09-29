package term

// sandboxenv.go — a sandboxed terminal session's environment: the rootfs
// PATH, the user's HOME, the session's tile-scoped token, an XBIN_URL the
// session can reach from its own netns, and, while the tile has a
// deployment record, the checkpoint fetch remote; and XBIN_DEPLOYMENT when
// the session's target isn't the primary (sessionEnv).

import (
	"net"
	"net/url"
	"slices"
	"strconv"
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
		"PATH=" + sandbox.RootfsPATH,
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
		remote := m.deployRemote(rel)
		env = append(env,
			"GIT_CONFIG_COUNT="+strconv.Itoa(2+len(remote)/2),
			"GIT_CONFIG_KEY_0=url."+effURL+"/.insteadOf", "GIT_CONFIG_VALUE_0=http://xbin/",
			"GIT_CONFIG_KEY_1=http."+effURL+"/.extraHeader", "GIT_CONFIG_VALUE_1=Authorization: Bearer "+token,
		)
		env = append(env, remote...)
	}
	return env
}

// sessionEnv is a sandboxed session's env: sandboxEnv, with the session's
// XBIN_DEPLOYMENT right after XBIN_COMPONENT when its target isn't the
// primary at session start (11-contract §5); otherwise exactly sandboxEnv.
func (m *Manager) sessionEnv(rel string, relayNet bool, homeDir, termTok string, o openOpts) []string {
	env := m.sandboxEnv(rel, relayNet, homeDir, termTok)
	dep := o.deploymentEnv()
	if dep == nil {
		return env
	}
	at := slices.IndexFunc(env, func(e string) bool { return strings.HasPrefix(e, "XBIN_COMPONENT=") }) + 1
	return slices.Insert(env, at, dep...)
}

// deployRemote is the checkpoint fetch remote, `xbin-deploy`, as the two
// GIT_CONFIG_* pairs that follow today's two (D119g): only for a session on a
// tile that has a deployment record when the session spawns. The remote
// lives in the session's env and never in the tile's .git/config, so a clone
// of the tile, an opt-out or a downgrade leaves nothing behind. Its URL is on
// the SDK's gateway host, so the insteadOf rewrite and the session's bearer
// above carry the fetch; after `git fetch xbin-deploy`, `deploy/<name>`
// resolves in the tile's repository. Nil for a root session, for a tile
// without a record (a store kept after an opt-out included: only the record
// counts) and when no hook is wired, so a zero-state session keeps exactly
// today's two pairs (D119c).
func (m *Manager) deployRemote(tile string) []string {
	if tile == "" || m.HasDeploymentRecord == nil || !m.HasDeploymentRecord(tile) {
		return nil
	}
	return []string{
		"GIT_CONFIG_KEY_2=remote.xbin-deploy.url", "GIT_CONFIG_VALUE_2=" + checkpointRemoteURL(tile),
		"GIT_CONFIG_KEY_3=remote.xbin-deploy.fetch", "GIT_CONFIG_VALUE_3=+refs/heads/deploy/*:refs/deploy/*",
	}
}

// checkpointRemoteURL is the checkpoint remote of a tile on the gateway
// host: http://xbin/api/xbin/checkpoints/<tile>.git. Each path segment is
// escaped, so a tile path holding a space, '#', '?' or '%' still names that
// tile (the route decodes it back); an ordinary path is unchanged.
func checkpointRemoteURL(tile string) string {
	segs := strings.Split(tile, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return "http://xbin/api/xbin/checkpoints/" + strings.Join(segs, "/") + ".git"
}
