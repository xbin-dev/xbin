package signins

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/sandbox"
)

const flowURL = "https://claude.com/cai/oauth/authorize?code=true&client_id=test-client&response_type=code" +
	"&redirect_uri=https%3A%2F%2Fplatform.claude.com%2Foauth%2Fcode%2Fcallback&scope=user%3Ainference&state=test-state"

// scriptedClaude is a scripted `claude setup-token` (Claude Code 2.1.280's
// shape, sdk/acp/testdata/signin): on a terminal only, the page as Ink
// draws it (an OSC 8 link), the prompt, then a code per line — one without
// '#' is "Invalid code" (asked again); good-XXXX#… prints a fake one-year
// token ending in XXXX; newformat… prints a credential in a format the scan
// doesn't know; anything else is its OAuth error. It refuses to run with a
// HOME that isn't empty, with anything but PATH, HOME, TERM and LANG in its
// environment, or (SCRIPTED_WANT_SANDBOX=… is never passed: the check is on
// what it can see) where the marker file of the test's "tile" is visible.
const scriptedClaude = `#!/bin/sh
URL='` + flowURL + `'
[ "$1" = setup-token ] || { printf 'error: unknown command %s\n' "$1"; exit 2; }
[ -t 0 ] || { printf 'Error: Raw mode is not supported on the current process.stdin\n'; exit 2; }
[ -d "$HOME" ] && [ -z "$(ls -A "$HOME")" ] || { printf 'OAuth error: HOME is not an empty throwaway (%s)\n' "$HOME"; exit 1; }
extra=$(env | cut -d= -f1 | grep -v -x -e PATH -e HOME -e TERM -e LANG -e PWD -e SHLVL -e _ -e OLDPWD | tr '\n' ' ')
[ -z "$extra" ] || { printf 'OAuth error: the environment is not clean (%s)\n' "$extra"; exit 1; }
[ "$(stty size | cut -d' ' -f2)" = 1000 ] || { printf 'OAuth error: the terminal is %s wide\n' "$(stty size)"; exit 1; }
[ ! -e "@MARKER@" ] || { printf 'OAuth error: the workspace is visible from the sign-in\n'; exit 1; }
printf '\033[2G\033[1mThis\033[7Gwill\033[12Gguide\033[18Gyou\033[22Gthrough\033[30Glong-lived\033[41G(1-year)\033[50Gauth\033[55Gtoken\033[61Gsetup\033[22m\r\n'
printf "\033]8;id=t;%s\007\033[38;5;246m%s\033[39m\033]8;;\007\r\n" "$URL" "$URL"
printf '\r\n\033[2GPaste\033[8Gcode\033[13Ghere\033[18Gif\033[21Gprompted\033[30G>\r\n'
while IFS= read -r c; do
  c=$(printf '%s' "$c" | tr -d '\r')
  case "$c" in *'#'*) ;; *) printf 'Invalid code. Please make sure the full code was copied.\r\n'; continue ;; esac
  case "$c" in
  good-*) s=${c%%#*}; s=${s#good-}
    printf '\033[32m✓\033[39m Long-lived authentication token created successfully!\r\n\r\nYour OAuth token (valid for 1 year):\r\n\r\n'
    printf '\033[38;5;220msk-ant-oat01-FAKEFAKEFAKEFAKEFAKEFAKE-%s\033[39m\r\n\r\nStore this token securely.\r\n' "$s"; exit 0 ;;
  newformat*) printf 'Your OAuth token (valid for 1 year):\r\n\r\nnewformat-FAKE-FAKE-FAKE-FAKE-zz\r\n'; exit 0 ;;
  esac
  printf 'OAuth error: Request failed with status code 400\r\n'; exit 1
done
exit 1
`

// scriptedToken is the fake token the scripted CLI prints for good-<s>#….
func scriptedToken(s string) string { return "sk-ant-oat01-FAKEFAKEFAKEFAKEFAKEFAKE-" + s }

// scriptedBin writes the scripted claude into a directory and answers it;
// marker is a path the CLI must not be able to see ("" none).
func scriptedBin(t *testing.T, marker string) string {
	t.Helper()
	dir := t.TempDir()
	if marker == "" {
		marker = "/nonexistent/marker"
	}
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(strings.ReplaceAll(scriptedClaude, "@MARKER@", marker)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newFlows(t *testing.T, w *world, bin string) *Flows {
	t.Helper()
	f := NewFlows(w.s, Confined{testBin: bin})
	f.StartWait, f.CodeWait = 20*time.Second, 20*time.Second
	t.Cleanup(f.Close)
	return f
}

func runGuided(t *testing.T, w *world, f *Flows) {
	t.Helper()
	ctx := context.Background()
	fl, err := f.Start(ctx, "alice", "claude", "Work", true)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if fl.URL != flowURL || !fl.Paste || fl.Harness != "claude" || fl.ExpiresAt <= w.now.UnixMilli() {
		t.Fatalf("flow: %+v", fl)
	}
	// the same person again: the same flow; someone else: none of it
	if again, err := f.Start(ctx, "alice", "claude", "", false); err != nil || again.ID != fl.ID {
		t.Fatalf("a second start: %+v %v", again, err)
	}
	if mine := f.Of("alice"); len(mine) != 1 || mine[0].ID != fl.ID {
		t.Fatalf("Of: %+v", mine)
	}
	if len(f.Of("bob")) != 0 {
		t.Fatal("bob sees alice's flow")
	}
	if _, _, err := f.Code("bob", fl.ID, "good-9f3e#state"); status(err) != 404 {
		t.Fatalf("bob types into alice's flow: %v", err)
	}
	if f.Cancel("bob", fl.ID) {
		t.Fatal("bob cancelled alice's flow")
	}
	// a malformed code: asked again, still open
	if _, _, err := f.Code("alice", fl.ID, "halfcode"); status(err) != 409 {
		t.Fatalf("a malformed code: %v", err)
	}
	if _, _, err := f.Code("alice", fl.ID, "  "); status(err) != 400 {
		t.Fatalf("an empty code: %v", err)
	}
	sg, replaced, err := f.Code("alice", fl.ID, " good-9f3e#state\n")
	if err != nil || replaced || sg.Name != "Work" || sg.Kind != KindSetupToken || sg.Env != "CLAUDE_CODE_OAUTH_TOKEN" || !sg.Default ||
		sg.ExpiresAt-sg.CreatedAt != SetupTokenLife.Milliseconds() {
		t.Fatalf("saved: %+v %v %v", sg, replaced, err)
	}
	if len(f.Of("alice")) != 0 {
		t.Fatal("the flow outlived its sign-in")
	}
	if _, _, err := f.Code("alice", fl.ID, "good-0000#state"); status(err) != 404 {
		t.Fatalf("a code after the end: %v", err)
	}
	c, err := w.s.Use("alice", "claude", "", UseFor{})
	if err != nil || c.Secret != scriptedToken("9f3e") {
		t.Fatalf("the kept credential: %v", err)
	}
	if diskHolds(t, w.dir, "FAKEFAKE") {
		t.Fatal("the token is on disk in the clear")
	}
}

// The guided flow end to end against the scripted CLI, as a host process
// (isolation off): the page, a malformed code, the token kept.
func TestGuidedFlow(t *testing.T) {
	w := newWorld(t)
	runGuided(t, w, newFlows(t, w, scriptedBin(t, "")))
}

// The same in a real sandbox: the CLI runs over the base image with
// nothing of the workspace in sight.
func TestGuidedFlowSandboxed(t *testing.T) {
	rootfs := os.Getenv("XBIN_TEST_ROOTFS")
	if rootfs == "" || !sandbox.Available() {
		t.Skip("needs XBIN_TEST_ROOTFS and user namespaces (hack/dev-setup.sh)")
	}
	confine.Configure(rootfs)
	t.Cleanup(func() { confine.Configure("") })
	w := newWorld(t)
	marker := filepath.Join(w.dir, "..", "marker") // a file of the "workspace": must not be visible
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGuided(t, w, newFlows(t, w, scriptedBin(t, marker)))
}

func TestGuidedFlowEnds(t *testing.T) {
	w := newWorld(t)
	f := newFlows(t, w, scriptedBin(t, ""))
	ctx := context.Background()

	// a refusal: the CLI's reason, the flow over
	fl, err := f.Start(ctx, "alice", "claude", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.Code("alice", fl.ID, "refuse#state"); status(err) != 502 || !strings.Contains(err.Error(), "OAuth error") {
		t.Fatalf("a refusal: %v", err)
	}
	if len(f.Of("alice")) != 0 {
		t.Fatal("a refused flow stayed open")
	}
	// a credential in a format the scan doesn't know: never shown
	fl, _ = f.Start(ctx, "alice", "claude", "", false)
	if _, _, err := f.Code("alice", fl.ID, "newformat#state"); status(err) != 502 || strings.Contains(err.Error(), "newformat-") {
		t.Fatalf("an unknown token format: %v", err)
	}
	if all, _, _ := w.s.List("alice"); len(all) != 0 {
		t.Fatalf("something was saved: %+v", all)
	}
	// cancel; the expiry
	fl, _ = f.Start(ctx, "alice", "claude", "", false)
	if !f.Cancel("alice", fl.ID) || f.Cancel("alice", fl.ID) {
		t.Fatal("Cancel")
	}
	f.Life = 150 * time.Millisecond
	fl, err = f.Start(ctx, "alice", "claude", "", false)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(f.Of("alice")) != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if len(f.Of("alice")) != 0 {
		t.Fatal("the flow outlived its bound")
	}
	if _, _, err := f.Code("alice", fl.ID, "good-1#s"); status(err) != 404 {
		t.Fatalf("a code after the bound: %v", err)
	}
}

func TestGuidedFlowRefusals(t *testing.T) {
	w := newWorld(t)
	f := newFlows(t, w, scriptedBin(t, ""))
	ctx := context.Background()
	for _, c := range []struct {
		user, harness, name string
		want                int
	}{
		{"carol", "claude", "", 403},
		{"alice", "nope", "", 400},
		{"alice", "codex", "", 409}, // no mint: paste a key
		{"alice", "claude", strings.Repeat("x", NameMax+1), 400},
	} {
		if _, err := f.Start(ctx, c.user, c.harness, c.name, false); status(err) != c.want {
			t.Errorf("Start(%s, %s): %v, want %d", c.user, c.harness, err, c.want)
		}
	}
	w.vault.sealed = true
	if _, err := f.Start(ctx, "alice", "claude", "", false); !errors.Is(err, ErrSealed) {
		t.Fatalf("sealed: %v", err)
	}
	w.vault.sealed = false
	_, _ = w.s.SetPolicy(false)
	if _, err := f.Start(ctx, "alice", "claude", "", false); !errors.Is(err, ErrOff) {
		t.Fatalf("switched off: %v", err)
	}
	_, _ = w.s.SetPolicy(true)
	// no CLI at all: said, nothing left running. The PATH holds the
	// wrapper's own tools and nothing else — never this machine's CLIs.
	tools := t.TempDir()
	for _, tool := range []string{"sh", "mktemp", "env", "rm"} {
		at, err := exec.LookPath(tool)
		if err != nil {
			t.Skip(tool + " not found")
		}
		if err := os.Symlink(at, filepath.Join(tools, tool)); err != nil {
			t.Fatal(err)
		}
	}
	g := NewFlows(w.s, Confined{testBin: t.TempDir()})
	g.StartWait = 10 * time.Second
	t.Cleanup(g.Close)
	t.Setenv("PATH", tools)
	if _, err := g.Start(ctx, "alice", "claude", "", false); status(err) != 502 {
		t.Fatalf("no CLI: %v", err)
	}
	if len(g.Of("alice")) != 0 {
		t.Fatal("a failed start left a flow")
	}
}
