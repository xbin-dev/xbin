//go:build linux && integration

package sandbox

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBareExe is the iroh sidecar's sandbox (plans/app-tunnel.md §5.6): no
// rootfs at all — a bare tmpfs root sealed read-only, the program passed
// open (Spec.Exe) and run through /proc/self/fd, the host network with the
// host's DNS files bound read-only, every capability dropped, and the
// factory reversed: the program sends xbind a connection (Factory.Accept).
func TestBareExe(t *testing.T) {
	if !Available() {
		t.Skip("unprivileged user namespaces unavailable")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "probe")
	buildBin(t, bin, bareProbeSrc)
	exe, err := os.Open(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer exe.Close()
	fac, child, err := NewFactory()
	if err != nil {
		t.Fatal(err)
	}
	defer fac.Close()

	spec := &Spec{
		Bare:         true,
		HostNet:      true,
		Unprivileged: true,
		Exe:          exe,
		Agent:        child,
		Entry:        "/nonexistent", // unused: Exe wins
		Argv:         []string{"xbin-probe", "serve"},
		Env:          []string{"PROBE=1"},
		Hostname:     "xbin-iroh",
		HostUID:      os.Getuid(),
		HostGID:      os.Getgid(),
	}
	cmd, h, err := Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Cleanup()
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		child.Close()
		t.Fatal(err)
	}
	h.Started()
	if err := h.SetupUserns(); err != nil {
		_ = cmd.Process.Kill()
		t.Fatal(err)
	}
	type res struct {
		c   io.ReadWriteCloser
		err error
	}
	acc := make(chan res, 1)
	go func() { c, err := fac.Accept(); acc <- res{c, err} }()
	var got []byte
	select {
	case r := <-acc:
		if r.err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			if strings.Contains(out.String(), "operation not permitted") {
				t.Skipf("sandbox creation denied by environment:\n%s", out.String())
			}
			t.Fatalf("accept: %v\n%s", r.err, out.String())
		}
		got, _ = io.ReadAll(r.c)
		r.c.Close()
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("no connection from the sandbox\n%s", out.String())
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("probe: %v\n%s", err, out.String())
	}
	var r struct {
		Argv       []string `json:"argv"`
		Pid        int      `json:"pid"`
		RootRO     bool     `json:"rootRO"`
		TmpRW      bool     `json:"tmpRW"`
		Resolv     string   `json:"resolv"`
		ResolvRO   bool     `json:"resolvRO"`
		HomeAbsent bool     `json:"homeAbsent"`
		Ifaces     int      `json:"ifaces"`
		CapEff     string   `json:"capEff"`
		Hostname   string   `json:"hostname"`
		Env        []string `json:"env"`
	}
	if err := json.Unmarshal(got, &r); err != nil {
		t.Fatalf("probe said %q: %v\n%s", got, err, out.String())
	}
	if len(r.Argv) != 4 || r.Argv[1] != "serve" || r.Argv[2] != "--fd" {
		t.Errorf("argv %q: want the given argv plus --fd N", r.Argv)
	}
	if r.Pid != 1 {
		t.Errorf("pid %d: want PID 1 of its own pid namespace", r.Pid)
	}
	if !r.RootRO {
		t.Error("the bare root is writable: want it sealed read-only")
	}
	if !r.TmpRW {
		t.Error("/tmp is not writable")
	}
	if !r.HomeAbsent {
		t.Error("the host's /home is visible: the root is not bare")
	}
	if host, err := os.ReadFile("/etc/resolv.conf"); err == nil {
		if r.Resolv != string(host) || !r.ResolvRO {
			t.Errorf("/etc/resolv.conf: %q ro=%v, want the host's %q, read-only", r.Resolv, r.ResolvRO, host)
		}
	}
	if r.Ifaces < 1 {
		t.Error("no interfaces: want the host network")
	}
	if strings.Trim(r.CapEff, "0") != "" {
		t.Errorf("CapEff %s: want every capability dropped", r.CapEff)
	}
	if r.Hostname != "xbin-iroh" {
		t.Errorf("hostname %q", r.Hostname)
	}
	if len(r.Env) != 1 || r.Env[0] != "PROBE=1" {
		t.Errorf("env %q: want exactly the spec's", r.Env)
	}
}

// bareProbeSrc reports what it sees, over a connection it sends xbind
// through its factory (--fd N), the way xbin-iroh hands over streams.
const bareProbeSrc = `package main

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func main() {
	r := map[string]any{"argv": os.Args, "pid": os.Getpid(), "env": os.Environ()}
	r["rootRO"] = os.WriteFile("/x", []byte("y"), 0o644) != nil
	r["tmpRW"] = os.WriteFile("/tmp/x", []byte("y"), 0o644) == nil
	_, e := os.Stat("/home")
	r["homeAbsent"] = os.IsNotExist(e)
	b, _ := os.ReadFile("/etc/resolv.conf")
	r["resolv"] = string(b)
	r["resolvRO"] = os.WriteFile("/etc/resolv.conf", []byte("x"), 0o644) != nil
	ifs, _ := net.Interfaces()
	r["ifaces"] = len(ifs)
	st, _ := os.ReadFile("/proc/self/status")
	for _, l := range strings.Split(string(st), "\n") {
		if v, ok := strings.CutPrefix(l, "CapEff:"); ok {
			r["capEff"] = strings.TrimSpace(v)
		}
	}
	h, _ := os.Hostname()
	r["hostname"] = h
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(r)

	fd := -1
	for i, a := range os.Args {
		if a == "--fd" && i+1 < len(os.Args) {
			fd, _ = strconv.Atoi(os.Args[i+1])
		}
	}
	p, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil || fd < 0 {
		os.Exit(2)
	}
	if err := syscall.Sendmsg(fd, []byte{'c'}, syscall.UnixRights(p[1]), nil, 0); err != nil {
		os.Stderr.WriteString("sendmsg: " + err.Error())
		os.Exit(3)
	}
	syscall.Close(p[1])
	syscall.Write(p[0], buf.Bytes())
	syscall.Close(p[0])
}
`
