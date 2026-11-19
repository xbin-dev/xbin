package term

// sandboxenv.go — a sandboxed terminal session's environment: the rootfs
// PATH, the user's HOME, the session's tile-scoped token, an XBIN_URL the
// session can reach from its own netns, and, while the tile has a
// deployment record, the checkpoint fetch remote; and XBIN_DEPLOYMENT when
// the session's target isn't the primary (sessionEnv).

import (
	"fmt"
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

// sessionEnv is a sandboxed session's env: sandboxEnv, then the person's
// fallback EMAIL (gitIdentityEnv), with the session's XBIN_DEPLOYMENT right
// after XBIN_COMPONENT when its target isn't the primary at session start
// (11-contract §5), then XBIN_PARTITION on a partitioned tile
// (partition.go), and a plain terminal's XBIN_LLM_* at the end while the
// workspace has an LLM connector.
func (m *Manager) sessionEnv(rel string, relayNet bool, homeDir, termTok string, o openOpts) []string {
	env := m.sandboxEnv(rel, relayNet, homeDir, termTok)
	env = append(env, gitIdentityEnv(o.homeKey, env)...) // git commits work in a sandbox: the person's fallback address
	env = append(env, o.llmShellEnv(env)...)             // a terminal's XBIN_LLM_* with a connector configured (llmhook.go)
	dep := append(o.deploymentEnv(), o.partitionEnv()...)
	if len(dep) == 0 {
		return env
	}
	at := slices.IndexFunc(env, func(e string) bool { return strings.HasPrefix(e, "XBIN_COMPONENT=") }) + 1
	return slices.Insert(env, at, dep...)
}

// gitIdentityEnv is a sandboxed session's fallback address for git:
// EMAIL=<person>@xbin.invalid. In the sandbox git can't make one up (the
// host name has no domain: "unable to auto-detect email address"), so a
// commit — a merge of a tile's template, a tile agent's `git am` (D248) —
// failed in every session whose person set no identity. EMAIL is git's
// last resort, below user.email in any config file and GIT_*_EMAIL (which
// would beat the person's own config: not used), so an identity the
// person set wins; and it is set only when env has no EMAIL of its own.
// The name stays git's own (the sandbox's user) unless they set one.
// .invalid never delivers (RFC 2606) — but commits carry it, and so do
// pushes of them to an external remote (docs/getting-started.md). The
// person's id is escaped, never stripped (gitLocalPart): two ids never
// share an address. Nothing for a session with no person.
func gitIdentityEnv(person string, env []string) []string {
	if person == "" || slices.ContainsFunc(env, func(e string) bool { return strings.HasPrefix(e, "EMAIL=") }) {
		return nil
	}
	return []string{"EMAIL=" + gitLocalPart(person) + "@xbin.invalid"}
}

// gitLocalPart is id as an address's local part, one to one: letters,
// digits, '-' and '_' as they are, every other byte (a '.', an '@', a '='
// itself) as '=' and two hex digits — so distinct ids never collide.
func gitLocalPart(id string) string {
	var b strings.Builder
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "=%02X", c)
		}
	}
	return b.String()
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
