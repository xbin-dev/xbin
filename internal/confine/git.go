package confine

import (
	"context"
	"strings"

	"github.com/xbin-dev/xbin/internal/sandbox"
)

// gitFlags pin the settings a repo's own config could otherwise turn into a
// command. Inside the sandbox that command could only hurt the repo it came
// from; the flags keep a direct run (isolation off) and the common paths
// quiet and deterministic too.
var gitFlags = []string{
	"-c", "core.fsmonitor=false",
	"-c", "core.hooksPath=/dev/null",
	"-c", "core.untrackedCache=false",
	"-c", "safe.directory=*", // the sandbox maps xbind to root: ownership checks would refuse every repo
	"-c", "protocol.ext.allow=never",
	"-c", "commit.gpgsign=false",
	"-c", "tag.gpgsign=false",
	"-c", "core.pager=cat",
}

// gitEnv: no system or global config (the daemon's own ~/.gitconfig must not
// reach a tile), never prompt, never take optional locks, fixed locale so
// callers can parse output.
var gitEnv = []string{
	"GIT_CONFIG_NOSYSTEM=1",
	"GIT_CONFIG_GLOBAL=/dev/null",
	"GIT_TERMINAL_PROMPT=0",
	"GIT_OPTIONAL_LOCKS=0",
	"LC_ALL=C",
}

// Git runs git in dir (a repo's work tree, bound read-write) confined, with
// the hardened flags and environment. binds adds other paths git needs (a
// repo to fetch from, read-only). Returns stdout; a failure's error text is
// git's stderr.
func Git(ctx context.Context, dir string, binds []sandbox.Bind, args ...string) (string, error) {
	return GitCmd(ctx, Cmd{Dir: dir, Binds: binds}, args...)
}

// GitRead is Git with dir bound read-only (log, show, diff, rev-parse, …).
func GitRead(ctx context.Context, dir string, args ...string) (string, error) {
	return GitCmd(ctx, Cmd{Dir: dir, ReadOnlyDir: true}, args...)
}

// GitCmd runs git with c's binds, network, timeout (Argv is set from args).
func GitCmd(ctx context.Context, c Cmd, args ...string) (string, error) {
	c.Argv = append(append([]string{"git"}, gitFlags...), args...)
	c.Env = append(append([]string(nil), gitEnv...), c.Env...)
	res, err := Run(ctx, c)
	if err != nil {
		if ee, ok := err.(*ExitError); ok && ee.Stderr == "" {
			ee.Stderr = strings.TrimSpace(string(res.Stdout))
		}
		return string(res.Stdout), err
	}
	return string(res.Stdout), nil
}
