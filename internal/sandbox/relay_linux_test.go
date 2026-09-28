//go:build linux && integration

package sandbox

import (
	"encoding/json"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/sandbox/relay"
)

// A sandbox behind a relay whose policy allows everything still has no route
// to the host once the relay carries HostDeny, and DNSRefuse fails a lookup at
// once (plans/tile-sandbox-runtime.md §4). The control run, without them,
// shows what they close: the host's own address and the gateway forward.
func TestRelayHostDenyEndToEnd(t *testing.T) {
	if !Available() {
		t.Skip("unprivileged user namespaces unavailable")
	}
	hostIP := hostIPv4(t)

	// A host service on every interface — what a sandbox must not reach.
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var accepted atomic.Int64
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			c.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	ps := strconv.Itoa(port)

	lower := filepath.Join(t.TempDir(), "lower")
	mkdir(t, lower)
	buildBin(t, filepath.Join(lower, "netprobe"), netProbeSrc)

	targets := []string{"tcp:" + hostIP.String() + ":" + ps, "tcp:" + GatewayIP + ":" + ps, "dns:example.com"}
	base := relay.Config{
		Allow:   func(netip.Addr, int) bool { return true },
		Gateway: netip.MustParseAddr(GatewayIP),
		HostFwd: map[int]string{port: "127.0.0.1:" + ps},
	}

	ctl := runNetProbe(t, lower, base, targets[:2]) // (DNS would wait out a timeout here)
	if !ctl["tcp:"+hostIP.String()+":"+ps].OK || !ctl["tcp:"+GatewayIP+":"+ps].OK {
		t.Fatalf("control: an allow-all relay reaches the host (that's the hole Deny closes): %+v", ctl)
	}

	before := accepted.Load()
	hard := base
	hard.Deny = relay.HostDeny(netip.AddrPortFrom(netip.IPv4Unspecified(), uint16(port)))
	hard.DNSRefuse = true
	hard.Resolver = HostResolver() // DNSRefuse wins
	res := runNetProbe(t, lower, hard, targets)
	for _, tg := range targets {
		r := res[tg]
		if r.OK {
			t.Errorf("%s: reached with HostDeny", tg)
		}
		if r.MS > 1000 {
			t.Errorf("%s: took %dms to fail, want an immediate refusal (%s)", tg, r.MS, r.Err)
		}
	}
	if !strings.Contains(res["tcp:"+hostIP.String()+":"+ps].Err, "refused") {
		t.Errorf("the host's address: want a reset, got %q", res["tcp:"+hostIP.String()+":"+ps].Err)
	}
	if n := accepted.Load() - before; n != 0 {
		t.Errorf("the host service accepted %d connections from a HostDeny sandbox", n)
	}
}

type probeResult struct {
	OK  bool   `json:"ok"`
	Err string `json:"err"`
	MS  int64  `json:"ms"`
}

func runNetProbe(t *testing.T, lower string, cfg relay.Config, targets []string) map[string]probeResult {
	t.Helper()
	spec := &Spec{
		Lower:   []string{lower},
		Entry:   "/netprobe",
		Argv:    append([]string{"/netprobe"}, targets...),
		Env:     []string{"PATH=/"},
		Cwd:     "/",
		Net:     "relay",
		HostUID: os.Getuid(),
		HostGID: os.Getgid(),
	}
	cmd, h, err := Launch(spec)
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
	fd, err := h.RecvTUN()
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if strings.Contains(out.String(), "operation not permitted") {
			t.Skipf("sandbox creation denied by environment:\n%s", out.String())
		}
		t.Fatalf("recv tun: %v\n%s", err, out.String())
	}
	cfg.TunFD, cfg.CloseTUN = fd, true // as the runner and terminals do
	rl, err := relay.Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("netprobe timed out\n%s", out.String())
	}
	rl.Close()
	if _, ferr := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); ferr != unix.EBADF {
		t.Errorf("the TUN fd is still open after Close with CloseTUN: %v", ferr)
	}
	if err != nil {
		t.Fatalf("netprobe: %v\n%s", err, out.String())
	}
	res := map[string]probeResult{}
	if err := json.Unmarshal(lastJSONLine([]byte(out.String())), &res); err != nil {
		t.Fatalf("netprobe output: %v\n%s", err, out.String())
	}
	t.Logf("netprobe %v: %+v", cfg.Deny != nil, res)
	return res
}

// hostIPv4 is a non-loopback IPv4 address of this host (skips without one).
func hostIPv4(t *testing.T) netip.Addr {
	as, _ := net.InterfaceAddrs()
	for _, a := range as {
		if n, ok := a.(*net.IPNet); ok {
			if ip, ok := netip.AddrFromSlice(n.IP.To4()); ok && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
				return ip
			}
		}
	}
	t.Skip("no non-loopback IPv4 address on this host")
	return netip.Addr{}
}

const netProbeSrc = `package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"strings"
	"time"
)

type result struct {
	OK  bool   ` + "`json:\"ok\"`" + `
	Err string ` + "`json:\"err\"`" + `
	MS  int64  ` + "`json:\"ms\"`" + `
}

func main() {
	out := map[string]result{}
	for _, tg := range os.Args[1:] {
		t0 := time.Now()
		var err error
		switch {
		case strings.HasPrefix(tg, "tcp:"):
			var c net.Conn
			c, err = net.DialTimeout("tcp", strings.TrimPrefix(tg, "tcp:"), 5*time.Second)
			if err == nil {
				c.Close()
			}
		case strings.HasPrefix(tg, "dns:"):
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			r := &net.Resolver{PreferGo: true}
			_, err = r.LookupHost(ctx, strings.TrimPrefix(tg, "dns:"))
			cancel()
		}
		res := result{OK: err == nil, MS: time.Since(t0).Milliseconds()}
		if err != nil {
			res.Err = err.Error()
		}
		out[tg] = res
	}
	b, _ := json.Marshal(out)
	os.Stdout.Write(append([]byte("\n"), append(b, '\n')...))
}
`
