//go:build linux && integration

package vm

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/sandbox"
)

// VM backends (plans/vm-sandbox.md): the backend's program reaches the guest
// over the VM file server — a Go backend as the single-file export
// /run/backend (what the runner binds), a node/python script under the
// tile's directory export — and its process must come to listen, so the
// shim serves the host socket and the health check passes.
//
// The matrix: the program's language (a static Go binary, a static C one),
// the export it is served from (a single file, a directory), 1 and 2 vCPUs,
// and KVM or emulation. By default this runs every KVM case and two
// emulated ones; XBIN_VM_MATRIX=full runs every emulated case too (each boot
// takes seconds there), and so does XBIN_VM_ACCEL=emulate, which also skips
// the KVM ones (XBIN_VM_ACCEL=kvm: only those).
//
// Every 1-vCPU case used to hang: the agent's vfork for the backend held the
// only scheduler P while the exec waited on FUSE requests that the agent's
// own relay goroutines were to carry (plans/vm-sandbox.md §VM backends that
// never listened). The relay is its own process now.
func TestVMBackendListens(t *testing.T) {
	root := t.TempDir()    // one workspace: the rootfs image is built once
	harnessAt(t, root, "") // skips where VMs can't run at all
	bins := map[string]string{"go": buildGoBackend(t)}
	if c, err := buildCBackend(t); err != nil {
		t.Logf("no static C toolchain, skipping the C cases: %v", err)
	} else {
		bins["c"] = c
	}
	forced := os.Getenv("XBIN_VM_ACCEL")
	full := os.Getenv("XBIN_VM_MATRIX") == "full" || forced == "emulate"
	for _, accel := range []string{"kvm", "emulate"} {
		for _, lang := range []string{"go", "c"} {
			for _, export := range []string{"file", "dir"} {
				for _, vcpus := range []int{1, 2} {
					name := fmt.Sprintf("%s/%s/%s/%dcpu", accel, lang, export, vcpus)
					t.Run(name, func(t *testing.T) {
						if bins[lang] == "" {
							t.Skip("no " + lang + " backend")
						}
						if forced != "" && forced != accel {
							t.Skip("XBIN_VM_ACCEL=" + forced)
						}
						if accel == "emulate" && !full && name != "emulate/go/file/1cpu" && name != "emulate/c/dir/2cpu" {
							t.Skip("emulated: two cases unless XBIN_VM_MATRIX=full")
						}
						m := harnessAt(t, root, accel)
						runBackend(t, m, bins[lang], export, vcpus)
					})
				}
			}
		}
	}
}

// harnessAt is harness on a shared workspace with the accelerator forced
// ("": whatever this host has).
func harnessAt(t *testing.T, root, accel string) *Manager {
	t.Helper()
	if accel != "" {
		t.Setenv("XBIN_VM_ACCEL", accel)
	}
	m, _ := harness(t)
	m.Root = root
	m.Debug = os.Getenv("XBIN_SANDBOX_DEBUG") != "" // echoes the guest console
	st := m.Status()
	if !st.Available {
		t.Skipf("%s VMs unavailable here: %s", accel, st.Reason)
	}
	return m
}

// runBackend starts bin as a VM backend the way the runner does and waits
// for it to answer an HTTP request on its socket.
func runBackend(t *testing.T, m *Manager, bin, export string, vcpus int) {
	b := newVMBackend(t)
	entry := "/run/backend"
	if export == "dir" {
		dir := filepath.Join(b.ws, "build")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		entry = filepath.Join(dir, "backend")
		copyFile(t, bin, entry)
		b.binds = append(b.binds, sandbox.Bind{Src: dir, Dst: dir, RO: true})
	} else {
		b.binds = append(b.binds, sandbox.Bind{Src: bin, Dst: entry, RO: true})
	}
	b.start(t, m, vcpus, entry, []string{entry})

	start := time.Now()
	timeout := 30 * time.Second
	if m.Status().Emulated {
		timeout = 3 * time.Minute
	}
	body, err := getUntil(b.sock, b.exited, timeout)
	if err != nil {
		code := b.quit() // the shim dumps what the guest is doing (host/dump_linux.go)
		t.Fatalf("the VM backend never answered: %v (exit code %d)\n%s", err, code, b.out.String())
	}
	t.Logf("answered %q after %s", body, time.Since(start).Round(time.Millisecond))
	// SIGTERM drains it: the guest process gets it and the shim exits with it
	_ = b.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-b.exited:
	case <-time.After(20 * time.Second):
		t.Errorf("the VM backend didn't stop on SIGTERM\n%s", b.out.String())
	}
}

// A backend that never listens: xbind's SIGQUIT on the health timeout makes
// the shim write what the guest is doing to the log, then quit.
func TestVMBackendDump(t *testing.T) {
	m := harnessAt(t, t.TempDir(), "")
	b := newVMBackend(t)
	mark := filepath.Join(b.ws, "mark")
	if err := os.MkdirAll(mark, 0o755); err != nil {
		t.Fatal(err)
	}
	b.binds = append(b.binds, sandbox.Bind{Src: mark, Dst: mark})
	b.start(t, m, 1, "/bin/sh", []string{"sh", "-c", "touch " + mark + "/ran; exec sleep 600"})
	deadline := time.Now().Add(vmTimeout)
	for {
		if _, err := os.Stat(filepath.Join(mark, "ran")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the backend never ran\n%s", b.out.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // the exec after the touch
	code := b.quit()
	out := b.out.String()
	t.Logf("dump:\n%s", out)
	if code != 128+int(syscall.SIGQUIT) {
		t.Errorf("the shim exited %d, want %d", code, 128+int(syscall.SIGQUIT))
	}
	for _, want := range []string{
		"--- VM dump (SIGQUIT) ---",
		"session 1 started; its socket isn't listening yet",
		"file server " + b.tile + ": ",
		"none in flight",
		"session 1: pid ",
		b.sock + ": not accepting",
		"(FUSE relay)",
		"sleep",
		"agent goroutines:",
		"--- end of VM dump ---",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the dump lacks %q", want)
		}
	}
}

// vmBackend is one VM backend generation, laid out as the runner lays it
// out: the tile's directory read-only, a guest-local run dir holding the
// listen socket, xbind's gateway socket.
type vmBackend struct {
	ws, tile, run, sock, gw string
	binds                   []sandbox.Bind

	cmd    *exec.Cmd
	out    lockedBuf
	exited chan struct{}
}

func newVMBackend(t *testing.T) *vmBackend {
	b := &vmBackend{ws: t.TempDir()}
	b.tile = filepath.Join(b.ws, "tiles", "b")
	b.run = filepath.Join(b.ws, "run", "b")
	for _, d := range []string{b.tile, b.run} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	b.sock = filepath.Join(b.run, "backend.sock")
	b.gw = filepath.Join(b.ws, "gateway.sock")
	gl, err := net.Listen("unix", b.gw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gl.Close() })
	b.binds = []sandbox.Bind{
		{Src: b.tile, Dst: b.tile, RO: true},
		{Src: b.run, Dst: b.run},
		{Src: b.gw, Dst: b.gw},
	}
	return b
}

// start launches the VM; the test's cleanup kills it.
func (b *vmBackend) start(t *testing.T, m *Manager, vcpus int, entry string, argv []string) {
	spec := &sandbox.Spec{
		Lower: []string{m.Rootfs},
		Binds: b.binds,
		Entry: entry,
		Argv:  argv,
		Env: []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
			"XBIN_SOCKET=" + b.sock, "XBIN_GATEWAY=" + b.gw},
		Cwd:          b.tile,
		HostUID:      os.Getuid(),
		HostGID:      os.Getgid(),
		Unprivileged: true,
	}
	if err := m.Apply(context.Background(), spec, Options{
		VCPUs: vcpus, MemMiB: 512, Hostname: "b",
		Local: []string{b.run}, Listen: b.sock, Gateway: b.gw,
	}); err != nil {
		t.Fatal(err)
	}
	cmd, h, err := sandbox.Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Cleanup)
	cmd.Stdout, cmd.Stderr = &b.out, &b.out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	b.cmd, b.exited = cmd, make(chan struct{})
	go func() { _ = cmd.Wait(); close(b.exited) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-b.exited
	})
	if err := h.SetupUserns(); err != nil {
		t.Fatal(err)
	}
}

// quit sends the shim SIGQUIT, as xbind does on a health timeout, and
// returns its exit code (-1: still running after a while).
func (b *vmBackend) quit() int {
	_ = b.cmd.Process.Signal(syscall.SIGQUIT)
	select {
	case <-b.exited:
		return b.cmd.ProcessState.ExitCode()
	case <-time.After(30 * time.Second):
		return -1
	}
}

// getUntil GETs / over the unix socket until it answers.
func getUntil(sock string, exited <-chan struct{}, timeout time.Duration) (string, error) {
	cl := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}
	deadline := time.Now().Add(timeout)
	var err error
	for time.Now().Before(deadline) {
		var resp *http.Response
		if resp, err = cl.Get("http://backend/"); err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return strings.TrimSpace(string(b)), nil
		}
		select {
		case <-exited:
			return "", fmt.Errorf("the VM exited: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
	}
	return "", fmt.Errorf("no answer within %s: %v", timeout, err)
}

const goBackend = `package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ln, err := net.Listen("unix", os.Getenv("XBIN_SOCKET"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	go func() {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGTERM)
		<-sigs
		os.Exit(0)
	}()
	http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "go-backend") }))
}
`

const cBackend = `#include <signal.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <unistd.h>

static void term(int s) { (void)s; _exit(0); }

int main(void) {
	signal(SIGTERM, term);
	signal(SIGPIPE, SIG_IGN); /* health probes connect and hang up */
	struct sockaddr_un a = {.sun_family = AF_UNIX};
	strncpy(a.sun_path, getenv("XBIN_SOCKET"), sizeof a.sun_path - 1);
	int l = socket(AF_UNIX, SOCK_STREAM, 0);
	if (l < 0 || bind(l, (struct sockaddr *)&a, sizeof a) || listen(l, 16))
		return 1;
	const char *r = "HTTP/1.1 200 OK\r\nContent-Length: 10\r\nConnection: close\r\n\r\nc-backend\n";
	for (;;) {
		int c = accept(l, 0, 0);
		if (c < 0)
			continue;
		char buf[4096];
		(void)read(c, buf, sizeof buf);
		(void)write(c, r, strlen(r));
		close(c);
	}
}
`

func buildGoBackend(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte(goBackend), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "backend")
	cmd := exec.Command("go", "build", "-o", out, src)
	cmd.Dir = dir
	cmd.Env = append(cleanEnv("GOFLAGS", "GOWORK"), "CGO_ENABLED=0", "GOWORK=off")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build the Go backend: %v\n%s", err, b)
	}
	return out
}

func buildCBackend(t *testing.T) (string, error) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "backend.c")
	if err := os.WriteFile(src, []byte(cBackend), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "backend")
	var errs []string
	for _, cc := range []string{"musl-gcc", "cc", "gcc"} {
		b, err := exec.Command(cc, "-static", "-O2", "-o", out, src).CombinedOutput()
		if err == nil {
			return out, nil
		}
		errs = append(errs, fmt.Sprintf("%s: %v %s", cc, err, strings.TrimSpace(string(b))))
	}
	return "", fmt.Errorf("%s", strings.Join(errs, "; "))
}

func cleanEnv(drop ...string) []string {
	var env []string
next:
	for _, e := range os.Environ() {
		for _, d := range drop {
			if strings.HasPrefix(e, d+"=") {
				continue next
			}
		}
		env = append(env, e)
	}
	return env
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, 0o755); err != nil {
		t.Fatal(err)
	}
}

// lockedBuf collects a sandbox's output while the test reads it.
type lockedBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
