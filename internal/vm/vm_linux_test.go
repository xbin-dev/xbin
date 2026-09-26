//go:build linux && integration

package vm

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/sandbox/relay"
)

// TestMain doubles as the re-exec init (sandbox.Launch re-execs this binary).
func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == sandbox.InitArg {
		sandbox.RunInit(os.Args[2]) // never returns
	}
	os.Exit(m.Run())
}

// harness finds a rootfs and the VM assets (the repo's bin/ unless the
// XBIN_* variables say otherwise), or skips.
func harness(t *testing.T) (*Manager, string) {
	t.Helper()
	_, here, _, _ := runtime.Caller(0)
	repo := filepath.Join(filepath.Dir(here), "..", "..")
	rootfs := os.Getenv("XBIN_ROOTFS")
	if rootfs == "" {
		rootfs = filepath.Join(repo, ".rootfs")
	}
	if _, err := os.Stat(filepath.Join(rootfs, "bin", "sh")); err != nil {
		t.Skipf("no rootfs at %s (make rootfs, or XBIN_ROOTFS)", rootfs)
	}
	bin := filepath.Join(repo, "bin")
	for env, name := range map[string]string{
		"XBIN_FIRECRACKER": "firecracker", "XBIN_VM_KERNEL": "vmlinux",
		"XBIN_VM_AGENT": "xbin-vmagent", "XBIN_MKFS_EROFS": "mkfs.erofs",
	} {
		if os.Getenv(env) == "" {
			t.Setenv(env, filepath.Join(bin, name))
		}
	}
	ws := t.TempDir()
	m := &Manager{Root: ws, Rootfs: rootfs, Bx: filepath.Join(bin, "bx")}
	if st := m.Status(); !st.Available {
		t.Skipf("VM sandboxes unavailable here: %s", st.Reason)
	}
	confine.Configure(rootfs)
	t.Cleanup(func() { confine.Configure("") })
	return m, ws
}

func launch(t *testing.T, spec *sandbox.Spec, withRelay func(fd int)) (string, int) {
	t.Helper()
	cmd, h, err := sandbox.Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Cleanup()
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := h.SetupUserns(); err != nil {
		t.Fatal(err)
	}
	if h.NeedsRelay() {
		fd, err := h.RecvTUN()
		if err != nil {
			_ = cmd.Process.Kill()
			t.Fatalf("recv tun: %v\n%s", err, out.String())
		}
		withRelay(fd)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		code := 0
		if ee, ok := err.(interface{ ExitCode() int }); ok && err != nil {
			code = ee.ExitCode()
		}
		return out.String(), code
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("VM sandbox timed out\n%s", out.String())
	}
	return "", -1
}

func TestVMSandboxFilesAndExit(t *testing.T) {
	m, ws := harness(t)
	tile := filepath.Join(ws, "tiles", "t1")
	other := filepath.Join(ws, "tiles", "t2")
	secret := filepath.Join(ws, ".xbin")
	for _, d := range []string{tile, other, secret} {
		os.MkdirAll(d, 0o755)
	}
	os.WriteFile(filepath.Join(tile, "in.txt"), []byte("from host"), 0o644)
	os.WriteFile(filepath.Join(secret, "token"), []byte("s3cret"), 0o600)

	script := fmt.Sprintf(`set -u
cat %[1]s/in.txt; echo
echo from-guest > %[1]s/out.txt
echo nope > %[2]s/x 2>/dev/null && echo WROTE-RO
ls -A %[3]s | wc -l | sed 's/^/secret-entries=/'
uname -r; id -u
exit 3`, tile, other, secret)
	spec := &sandbox.Spec{
		Lower: []string{m.Rootfs},
		Binds: []sandbox.Bind{
			{Src: ws, Dst: ws, RO: true},
			{Dst: secret, Mask: true, RO: true},
			{Src: tile, Dst: tile},
		},
		Entry:   "/bin/bash",
		Argv:    []string{"bash", "-c", script},
		Env:     []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		Cwd:     tile,
		HostUID: os.Getuid(),
		HostGID: os.Getgid(),
	}
	if err := m.Apply(context.Background(), spec, Options{MemMiB: 512}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	out, code := launch(t, spec, nil)
	t.Logf("VM sandbox ran in %s:\n%s", time.Since(start).Round(time.Millisecond), out)
	if code != 3 {
		t.Errorf("exit code %d, want 3", code)
	}
	if !strings.Contains(out, "from host") {
		t.Errorf("guest didn't read the tile file")
	}
	if b, _ := os.ReadFile(filepath.Join(tile, "out.txt")); string(b) != "from-guest\n" {
		t.Errorf("host sees %q from the guest's write", b)
	}
	if strings.Contains(out, "WROTE-RO") {
		t.Errorf("guest wrote under a read-only bind")
	}
	if !strings.Contains(out, "secret-entries=0") {
		t.Errorf("masked dir is not empty in the guest")
	}
	if !strings.Contains(out, "\n0\n") {
		t.Errorf("guest workload is not root")
	}
}

func TestVMSandboxRelay(t *testing.T) {
	m, ws := harness(t)
	// a host service reachable only through the relay's host-forward
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "hello-from-host") }))
	port := ln.Addr().(*net.TCPAddr).Port

	script := fmt.Sprintf(`curl -s --max-time 5 http://10.0.2.2:%d/; echo
curl -s --max-time 3 -o /dev/null http://1.1.1.1/ && echo EGRESS-LEAK
ip -4 -o addr show eth0`, port)
	spec := &sandbox.Spec{
		Lower:   []string{m.Rootfs},
		Binds:   []sandbox.Bind{{Src: ws, Dst: ws}},
		Entry:   "/bin/bash",
		Argv:    []string{"bash", "-c", script},
		Env:     []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		Cwd:     ws,
		Net:     "relay",
		HostUID: os.Getuid(),
		HostGID: os.Getgid(),
	}
	if err := m.Apply(context.Background(), spec, Options{MemMiB: 512}); err != nil {
		t.Fatal(err)
	}
	var rl *relay.Relay
	out, _ := launch(t, spec, func(fd int) {
		var err error
		rl, err = relay.Start(relay.Config{
			TunFD:   fd,
			Allow:   func(netip.Addr, int) bool { return false }, // deny-all egress
			Gateway: netip.MustParseAddr(sandbox.GatewayIP),
			HostFwd: map[int]string{port: fmt.Sprintf("127.0.0.1:%d", port)},
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	if rl != nil {
		rl.Close()
	}
	t.Logf("guest output:\n%s", out)
	if !strings.Contains(out, "hello-from-host") {
		t.Errorf("guest can't reach the host-forward through the relay")
	}
	if strings.Contains(out, "EGRESS-LEAK") {
		t.Errorf("guest reached the internet past a deny-all relay policy")
	}
	if !strings.Contains(out, "10.0.2.15/32") {
		t.Errorf("guest eth0 lacks 10.0.2.15/32")
	}
}

func TestVMSandboxEgressAllowed(t *testing.T) {
	m, ws := harness(t)
	spec := &sandbox.Spec{
		Lower:   []string{m.Rootfs},
		Binds:   []sandbox.Bind{{Src: ws, Dst: ws}},
		Entry:   "/bin/bash",
		Argv:    []string{"bash", "-c", `curl -sS --max-time 10 -o /dev/null -w 'status=%{http_code}\n' https://example.com/ 2>&1`},
		Env:     []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		Cwd:     ws,
		Net:     "relay",
		HostUID: os.Getuid(),
		HostGID: os.Getgid(),
	}
	if err := m.Apply(context.Background(), spec, Options{MemMiB: 512}); err != nil {
		t.Fatal(err)
	}
	var rl *relay.Relay
	out, _ := launch(t, spec, func(fd int) {
		var err error
		rl, err = relay.Start(relay.Config{
			TunFD:    fd,
			Allow:    func(netip.Addr, int) bool { return true },
			Resolver: sandbox.HostResolver(),
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	if rl != nil {
		rl.Close()
	}
	t.Logf("guest output:\n%s", out)
	if !strings.Contains(out, "status=200") {
		if strings.Contains(out, "Could not resolve") || strings.Contains(out, "timed out") {
			t.Skipf("no internet from this host? %s", out)
		}
		t.Errorf("guest couldn't reach the internet through an allow-all relay")
	}
}
