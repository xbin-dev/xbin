//go:build linux && integration

// Run with: go test -tags=integration ./internal/runner/ -run PartitionHostNet
// Needs user namespaces, an unpacked rootfs (XBIN_TEST_ROOTFS, or the repo's
// .rootfs) and a host go; skips otherwise. TestMain is build_linux_test.go's.
package runner

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sandbox"
)

// netProbe builds a static probe: PROBE_MODE=dial connects to
// 127.0.0.1:$PROBE_PORT and prints "dial ok" or "dial refused"; listen
// listens there, checks it can reach itself ("self ok"), prints
// "listening" and serves until killed.
func netProbe(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go for the probe")
	}
	src := t.TempDir()
	ckWrite(t, filepath.Join(src, "go.mod"), "module probe"+ckGoMod)
	ckWrite(t, filepath.Join(src, "main.go"), `package main

import (
	"fmt"
	"net"
	"os"
	"time"
)

func main() {
	addr := "127.0.0.1:" + os.Getenv("PROBE_PORT")
	switch os.Getenv("PROBE_MODE") {
	case "dial":
		c, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			fmt.Println("dial refused:", err)
			return
		}
		c.Close()
		fmt.Println("dial ok")
	case "listen":
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			fmt.Println("listen failed:", err)
			os.Exit(1)
		}
		if c, err := net.DialTimeout("tcp", addr, 2*time.Second); err == nil {
			c.Close()
			fmt.Println("self ok")
		}
		fmt.Println("listening")
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}
}
`)
	bin := filepath.Join(src, "probe")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = src
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off", "GOFLAGS=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("probe: %v %s", err, out)
	}
	return bin
}

// syncBuf is an output buffer two goroutines share.
type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// covers PD-28 S5 — the isolated host-net test (plans/partitions/03 §Tests
// "isolated"): on a tile whose net is bound to the host, the global
// instance shares the host network (it reaches a host listener on
// 127.0.0.1), while each person's partition gets a network namespace of its
// own — it reaches neither the host's listener nor another person's
// partition's 127.0.0.1 listener.
func TestPartitionHostNetIsolated(t *testing.T) {
	fs := ckRootfs(t)
	probe := netProbe(t)
	ws, r, run := ckBackend(t, fs)
	x := filepath.Join(ws, "apps/x")
	ckWrite(t, filepath.Join(x, "xbin.json"), `{"runtime":"go"}`)
	r.NetHost = func(*registry.Component) bool { return true } // net → host
	r.PartitionEnv = func(*registry.Component, string, string) ([]string, map[string]ResBind) {
		return nil, map[string]ResBind{}
	}
	c := &registry.Component{Path: "apps/x", Dir: x, Manifest: registry.Manifest{Runtime: "go"}}
	alice := partitionView(c, "user:alice", fakePkey("user:alice"))
	bob := partitionView(c, "user:bob", fakePkey("user:bob"))

	hostLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hostLn.Close()
	go func() {
		for {
			c, err := hostLn.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	hostPort := strconv.Itoa(hostLn.Addr().(*net.TCPAddr).Port)

	launch := func(t *testing.T, v *registry.Component, env ...string) (*exec.Cmd, *sandbox.Handle) {
		t.Helper()
		dir := filepath.Join(run, partSockDir(v.Path, "main", v.PartitionID))
		if v.Partition == "" {
			dir = filepath.Join(run, sockDir(v.Path, "main"))
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		pol, _ := r.spawnEgress(v)
		spec := r.launchSpec(v, probe, dir, env, pol, "")
		if v.Partition != "" && spec.HostNet {
			t.Fatalf("%s's partition was given the host network", v.Partition)
		}
		cmd, h, err := r.sandboxCmd(v, probe, dir, filepath.Join(dir, "g1.sock"), append(env, "XBIN_COMPONENT=apps/x"), pol, "")
		if err != nil {
			t.Fatal(err)
		}
		return cmd, h
	}
	dial := func(t *testing.T, v *registry.Component, port string) string {
		t.Helper()
		cmd, h := launch(t, v, "PROBE_MODE=dial", "PROBE_PORT="+port)
		out, code := ckRun(t, cmd, h)
		if code != 0 {
			t.Fatalf("the dial probe exited %d:\n%s", code, out)
		}
		return out
	}

	if out := dial(t, c, hostPort); !strings.Contains(out, "dial ok") {
		t.Fatalf("the global instance of a host-net tile can't reach the host's 127.0.0.1 (the fixture is broken):\n%s", out)
	}
	if out := dial(t, alice, hostPort); !strings.Contains(out, "dial refused") {
		t.Errorf("alice's partition reached the host's 127.0.0.1 listener:\n%s", out)
	}

	// alice listens on her own 127.0.0.1; bob dials the same port.
	const port = "18471"
	cmd, h := launch(t, alice, "PROBE_MODE=listen", "PROBE_PORT="+port)
	defer h.Cleanup()
	var out syncBuf
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	if err := h.SetupUserns(); err != nil {
		t.Fatalf("userns: %v", err)
	}
	deadline := time.Now().Add(ckTimeout)
	for !strings.Contains(out.String(), "listening") {
		if time.Now().After(deadline) || strings.Contains(out.String(), "listen failed") {
			t.Fatalf("alice's listener never listened:\n%s", out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(out.String(), "self ok") {
		t.Fatalf("alice's partition can't reach its own listener (no loopback?):\n%s", out.String())
	}
	if got := dial(t, bob, port); !strings.Contains(got, "dial refused") {
		t.Errorf("bob's partition reached alice's 127.0.0.1 listener:\n%s", got)
	}
}
