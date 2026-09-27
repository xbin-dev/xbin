//go:build linux && integration

// Live VM tile sandboxes (plans/tile-sandbox-runtime.md WP-16), driven
// through the /api/xbin routes as a manager tile, like live_linux_test.go:
// resident microVMs on the real shim and guest agent (bx and xbin-vmagent
// built from this tree), over the rootfs ($XBIN_TEST_ROOTFS or the repo's
// .rootfs) with the VM assets of the repo's bin/ (make vm-assets) or the
// XBIN_* variables. Under KVM; `make integration` runs it again with
// XBIN_VM_ACCEL=emulate (QEMU's emulation). Skips without them.
package tilesbx

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/sbx"
	"github.com/xbin-dev/xbin/internal/vm"
)

var (
	vmAgentOnce sync.Once
	vmAgent     string
	vmAgentErr  error
)

// liveVMAgent builds the guest agent from this tree (the initramfs packs it).
func liveVMAgent(t *testing.T, bin string) string {
	vmAgentOnce.Do(func() {
		vmAgent = filepath.Join(bin, "xbin-vmagent")
		cmd := exec.Command("go", "build", "-o", vmAgent, "../../cmd/xbin-vmagent")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if b, err := cmd.CombinedOutput(); err != nil {
			vmAgentErr = fmt.Errorf("build xbin-vmagent: %v\n%s", err, b)
		}
	})
	if vmAgentErr != nil {
		t.Fatal(vmAgentErr)
	}
	return vmAgent
}

// vmPolicyModes is boot's Modes adapter (tilesandboxes.go): TileVMs.
type vmPolicyModes struct{ m *vm.Manager }

func (s vmPolicyModes) VM() (string, string) {
	st, reason := s.m.TileVMs()
	switch {
	case reason != "":
		return "", reason
	case st.Emulated:
		return accelEmulate, ""
	}
	return accelKVM, ""
}

// hostIPv4 is one of the host's own non-loopback IPv4 addresses ("": none).
func hostIPv4() string {
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() && n.IP.IsGlobalUnicast() {
			return n.IP.String()
		}
	}
	return ""
}

func TestLiveVM(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("unprivileged user namespaces unavailable")
	}
	rootfs := os.Getenv("XBIN_TEST_ROOTFS")
	if rootfs == "" {
		rootfs, _ = filepath.Abs("../../.rootfs")
	}
	if _, err := os.Stat(filepath.Join(rootfs, "bin", "sh")); err != nil {
		t.Skipf("no rootfs at %s ($XBIN_TEST_ROOTFS, or make rootfs)", rootfs)
	}
	bin := liveBinaries(t)
	repoBin, _ := filepath.Abs("../../bin")
	for env, name := range map[string]string{
		"XBIN_FIRECRACKER": "firecracker", "XBIN_VM_KERNEL": "vmlinux", "XBIN_MKFS_EROFS": "mkfs.erofs",
		"XBIN_QEMU": "qemu-system-x86_64", "XBIN_VHOST_VSOCK": "vhost-device-vsock",
	} {
		if os.Getenv(env) == "" {
			t.Setenv(env, filepath.Join(repoBin, name))
		}
	}
	t.Setenv("XBIN_VM_AGENT", liveVMAgent(t, bin))
	vmm := &vm.Manager{Root: t.TempDir(), Rootfs: rootfs, Bx: filepath.Join(bin, "bx")}
	st := vmm.Status()
	if !st.Available {
		t.Skipf("VM sandboxes unavailable here: %s", st.Reason)
	}
	accel, slow := accelKVM, time.Duration(1)
	if st.Emulated {
		accel, slow = accelEmulate, emulateSlow
		t.Logf("emulated VMs: %s", st.Note)
	}
	confine.Configure(rootfs) // the image build and the state removal run confined, as under --isolate
	t.Cleanup(func() { confine.Configure("") })
	policy := vm.Policy{Tiles: true, TilesEmulated: st.Emulated, BudgetMiB: 8192, TilesBudgetMiB: 4096}
	if err := vmm.SetPolicy(policy); err != nil {
		t.Fatal(err)
	}
	hostIP := hostIPv4()
	classes := []EgressClass{{Class: "class:internet", Slot: "internet", Ref: "internet", Reach: "internet", Rules: []string{"net:internet"}}}
	if hostIP != "" {
		classes = append(classes, EgressClass{Class: "class:lan", Slot: "lan", Ref: "lan:" + hostIP + "/32", Reach: "open", Rules: []string{"lan:" + hostIP + "/32"}})
	}
	le := newLiveEnv(t, bin, rootfs, func(o *Options) {
		o.Deps.VM, o.Deps.Modes = vmm, vmPolicyModes{vmm}
		o.Deps.Net = fakeNet{"apps/mgr": classes}
	})
	vmUsed := func() vm.Usage { return vmm.UsedTiles() }
	le.create(map[string]any{"name": "vm-1", "mode": "vm", "memMiB": 512, "vcpus": 1, "diskGiB": 1, "mounts": []any{probeMount,
		map[string]any{"res": "res:apps/mgr/work", "at": "/mnt/work"},
		map[string]any{"res": "res:apps/mgr/ro", "at": "/mnt/ro"}}})
	sh := func(name, script string) string {
		t.Helper()
		out, ex := execRun(le.t, le.runOf(name), []string{"sh", "-c", script})
		if ex.Code != 0 || ex.Error != "" {
			le.t.Fatalf("%q in %s: %q %+v", script, name, out, ex)
		}
		return out
	}

	t.Run("start, exec, stop, start: the disk persists", func(t *testing.T) {
		le.t = t
		start := time.Now()
		in := le.start("vm-1")
		t.Logf("booted in %s", time.Since(start).Round(time.Millisecond))
		if in.Accel != accel || in.Mode != ModeVM {
			t.Fatalf("started: %+v", in)
		}
		rows := le.sbx.List(sbx.Filter{Kind: sbx.Tile})
		if len(rows) != 1 || rows[0].Mode != sbx.VM || string(rows[0].Accel) != accel || rows[0].MemMiB != 512 {
			t.Fatalf("the registry row: %+v", rows)
		}
		if u := vmUsed(); u.VMs != 1 || u.MemMiB != 512 {
			t.Fatalf("the tile sub-budget: %+v", u)
		}
		if out := sh("vm-1", "hostname; id -u"); out != "vm-1\n0\n" {
			t.Fatalf("hostname, uid: %q", out)
		}
		le.probeOK("vm-1", "write", "/var/kept", "on the disk")
		le.probeOK("vm-1", "write", "/mnt/work/x", "shared")
		if b, _ := os.ReadFile(filepath.Join(le.work, "x")); string(b) != "shared" {
			t.Fatalf("the writer mount: %q", b)
		}
		if out, ex := le.probe("vm-1", "write", "/mnt/ro/x", "no"); ex.Code == 0 {
			t.Fatalf("a reader's mount took a write: %q", out)
		}
		if _, err := os.Stat(filepath.Join(le.ro, "x")); err == nil {
			t.Fatal("the write reached the resource")
		}
		le.stop("vm-1")
		if u := vmUsed(); u.VMs != 0 {
			t.Fatalf("the VM reservation after the stop: %+v", u)
		}
		d, _ := le.m.defs.get(le.k, "vm-1")
		cur, _ := le.m.CurDir(le.k, d)
		var st syscall.Stat_t
		if err := syscall.Stat(filepath.Join(cur, "vm", "disk.img"), &st); err != nil || st.Size != 1<<30 || st.Blocks == 0 {
			t.Fatalf("the disk: %+v %v", st, err)
		}
		t.Logf("the disk: %d KiB allocated", st.Blocks/2)
		if _, err := os.Lstat(filepath.Join(cur, "upper")); !os.IsNotExist(err) {
			t.Fatalf("a VM sandbox has an upper: %v", err)
		}
		le.start("vm-1")
		if out := le.probeOK("vm-1", "cat", "/var/kept"); out != "on the disk" {
			t.Fatalf("after a restart: %q", out)
		}
	})

	t.Run("egress none: reset and REFUSED at once; the relay's tile config", func(t *testing.T) {
		le.t = t
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "hello-from-host") }))
		bound := 100 * time.Millisecond // at once, as in a namespace sandbox
		if accel == accelEmulate {
			bound = time.Second // through an emulated guest's own stack
		}
		out := le.probeOK("vm-1", "tcp", "1.1.1.1:80")
		if f := strings.Fields(out); len(f) < 2 || f[0] != "reset" || time.Duration(atoi(f[1]))*time.Millisecond >= bound {
			t.Fatalf("tcp: %q", out)
		}
		out = le.probeOK("vm-1", "dns", "example.com", "10.0.2.3:53")
		if f := strings.Fields(out); len(f) < 3 || f[0] != "rcode" || f[1] != "5" || time.Duration(atoi(f[2]))*time.Millisecond >= bound {
			t.Fatalf("dns: %q", out)
		}
		// no host forward: the gateway is a dead end (TestVMSandboxRelay's
		// forward, refused under the tile config)
		port := ln.Addr().(*net.TCPAddr).Port
		if out := le.probeOK("vm-1", "tcp", fmt.Sprintf("10.0.2.2:%d", port)); !strings.HasPrefix(out, "reset") {
			t.Fatalf("the gateway: %q", out)
		}
		if out := sh("vm-1", "ip -4 -o addr show eth0"); !strings.Contains(out, "10.0.2.15/32") {
			t.Fatalf("the guest's address: %q", out)
		}
	})

	t.Run("the cgroup leaf", func(t *testing.T) {
		le.t = t
		if le.m.cg == nil {
			t.Skip("no delegated cgroup (see live_linux_test.go's header)")
		}
		r := le.runOf("vm-1")
		b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", r.proc.Pid()))
		path := strings.TrimSpace(strings.TrimPrefix(string(b), "0::"))
		over := vm.VMOverheadMiB
		if accel == accelEmulate {
			over = vm.EmulatedOverheadMiB
		}
		for f, want := range map[string]string{"memory.max": strconv.Itoa((512 + over) << 20),
			"cpu.max": "200000 100000", "pids.max": "512", "memory.high": "max"} {
			if got, _ := os.ReadFile(filepath.Join("/sys/fs/cgroup", path, f)); strings.TrimSpace(string(got)) != want {
				t.Errorf("its %s: %q, want %q", f, got, want)
			}
		}
	})

	t.Run("a diskGiB grow applies at the next start", func(t *testing.T) {
		le.t = t
		size := func() int {
			out := sh("vm-1", "df -BM --output=size / | tail -1")
			mb, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(out), "M"))
			return mb
		}
		before := size()
		w := le.do(mgr, "PATCH", "/sandboxes/vm-1", map[string]any{"diskGiB": 2})
		le.want(w, http.StatusOK, "")
		if in := le.info(w); !in.RestartNeeded {
			t.Fatalf("patched: %+v", in)
		}
		le.stop("vm-1")
		le.start("vm-1")
		after := size()
		t.Logf("the root filesystem: %d MiB → %d MiB", before, after)
		if after < 1500 || after <= before {
			t.Fatalf("the filesystem didn't grow with the disk: %d → %d MiB", before, after)
		}
		if out := le.probeOK("vm-1", "cat", "/var/kept"); out != "on the disk" {
			t.Fatalf("data lost across the grow: %q", out)
		}
	})

	t.Run("the tile sub-budget", func(t *testing.T) {
		le.t = t
		le.create(map[string]any{"name": "vm-big", "mode": "vm", "memMiB": 4096, "vcpus": 1, "diskGiB": 1})
		w := le.do(mgr, "POST", "/sandboxes/vm-big/start", nil)
		le.want(w, http.StatusTooManyRequests, RefLimit)
		if !strings.Contains(w.Body.String(), "tile sandboxes (4096 MiB)") {
			t.Fatalf("the refusal: %s", w.Body.String())
		}
		f := le.sbx.Failures(sbx.Filter{Kind: sbx.Tile})
		if len(f) == 0 || f[0].Stage != sbx.Refused || !strings.Contains(f[0].Error, "vm-big") { // newest first
			t.Fatalf("failures: %+v", f)
		}
		if u := vmUsed(); u.VMs != 1 || u.MemMiB != 512 {
			t.Fatalf("the sub-budget after the refusal: %+v", u)
		}
		le.want(le.do(mgr, "DELETE", "/sandboxes/vm-big", nil), http.StatusNoContent, "")
	})

	t.Run("the VM policy's switches", func(t *testing.T) {
		le.t = t
		flip := func(p vm.Policy) {
			t.Helper()
			old := vmm.StoredPolicy()
			if err := vmm.SetPolicy(p); err != nil {
				t.Fatal(err)
			}
			le.m.OnVMPolicy(old, p) // boot's putVMPolicy
		}
		off := policy
		off.Tiles = false
		flip(off)
		in := le.waitState("vm-1", StateStopped, 30*slow*time.Second)
		if !strings.Contains(in.StateDetail, "vm policy: tiles") {
			t.Fatalf("stateDetail %q", in.StateDetail)
		}
		w := le.do(mgr, "POST", "/sandboxes/vm-1/start", nil)
		le.want(w, http.StatusServiceUnavailable, RefUnavailable)
		if !strings.Contains(w.Body.String(), "vm policy: tiles") {
			t.Fatalf("a start with tiles off: %s", w.Body.String())
		}
		le.want(le.do(mgr, "POST", "/sandboxes", map[string]any{"name": "vm-2", "mode": "vm"}), http.StatusBadRequest, RefInvalid)
		flip(policy)
		le.start("vm-1")
		noEmu := policy
		noEmu.TilesEmulated = false
		old := policy
		old.TilesEmulated = true
		if err := vmm.SetPolicy(noEmu); err != nil {
			t.Fatal(err)
		}
		le.m.OnVMPolicy(old, noEmu)
		if accel == accelKVM {
			time.Sleep(200 * time.Millisecond)
			if in, _ := le.m.infoOf(le.k, "vm-1"); in.State != StateRunning {
				t.Fatalf("tilesEmulated off stopped a VM on KVM: %+v", in)
			}
			if err := vmm.SetPolicy(policy); err != nil {
				t.Fatal(err)
			}
			return
		}
		in = le.waitState("vm-1", StateStopped, 30*slow*time.Second)
		if !strings.Contains(in.StateDetail, "tilesEmulated") {
			t.Fatalf("stateDetail %q", in.StateDetail)
		}
		w = le.do(mgr, "POST", "/sandboxes/vm-1/start", nil)
		le.want(w, http.StatusServiceUnavailable, RefUnavailable)
		if !strings.Contains(w.Body.String(), "tilesEmulated") {
			t.Fatalf("emulation without tilesEmulated: %s", w.Body.String())
		}
		flip(policy)
		le.start("vm-1")
	})

	t.Run("start/stop cycles leave no descriptor behind", func(t *testing.T) {
		le.t = t
		le.stop("vm-1")
		n := 10
		if accel == accelEmulate {
			n = 3
		}
		before := openFDs(t)
		for i := 0; i < n; i++ {
			le.start("vm-1")
			le.stop("vm-1")
		}
		runtime.GC()
		time.Sleep(100 * time.Millisecond)
		if after := openFDs(t); after > before {
			t.Fatalf("open fds %d → %d over %d cycles", before, after, n)
		}
		if u := vmUsed(); u.VMs != 0 {
			t.Fatalf("the VM reservations: %+v", u)
		}
		le.start("vm-1")
	})

	t.Run("egress through a class: the host is denied, the internet reached", func(t *testing.T) {
		le.t = t
		le.stop("vm-1")
		if hostIP != "" {
			ln, err := net.Listen("tcp", hostIP+":0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			le.want(le.do(mgr, "PATCH", "/sandboxes/vm-1", map[string]any{"net": map[string]any{"egress": "class:lan"}}), http.StatusOK, "")
			le.start("vm-1")
			// the class allows the host's address; the relay's Deny refuses it
			if out := le.probeOK("vm-1", "tcp", ln.Addr().String()); !strings.HasPrefix(out, "reset") {
				t.Fatalf("the host's own address through a lan class: %q", out)
			}
			t.Logf("%s (the host's own address) through lan:%s/32: reset", ln.Addr(), hostIP)
			le.stop("vm-1")
		} else {
			t.Log("no non-loopback host address: the Deny check skipped")
		}
		le.want(le.do(mgr, "PATCH", "/sandboxes/vm-1", map[string]any{"net": map[string]any{"egress": "class:internet"}}), http.StatusOK, "")
		le.start("vm-1")
		out, _ := execRun(t, le.runOf("vm-1"), []string{"sh", "-c", `curl -sS --max-time 20 -o /dev/null -w 'status=%{http_code}\n' https://example.com/ 2>&1`})
		t.Logf("curl: %s", out)
		if !strings.Contains(out, "status=200") {
			if strings.Contains(out, "Could not resolve") || strings.Contains(out, "timed out") {
				t.Logf("no internet from this host? %s", out)
			} else {
				t.Fatalf("the guest couldn't reach the internet through its class: %q", out)
			}
		}
		if out := le.probeOK("vm-1", "tcp", "10.0.2.2:80"); !strings.HasPrefix(out, "reset") {
			t.Fatalf("the gateway under a class: %q", out)
		}
	})

	t.Run("the VMM dying ends it with its console", func(t *testing.T) {
		le.t = t
		r := le.runOf("vm-1")
		pid := vmmPid(r.proc.Pid())
		if pid == 0 {
			t.Fatalf("no VMM under the shim %d", r.proc.Pid())
		}
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		in := le.waitState("vm-1", StateStopped, 15*slow*time.Second)
		t.Logf("stateDetail: %q", in.StateDetail)
		if !strings.HasPrefix(in.StateDetail, "the VM exited: ") || len(in.StateDetail) < len("the VM exited: ")+10 {
			t.Fatalf("stateDetail %q", in.StateDetail)
		}
		le.assertGone(r)
		if u := vmUsed(); u.VMs != 0 {
			t.Fatalf("the VM reservation: %+v", u)
		}
		f := le.sbx.Failures(sbx.Filter{Kind: sbx.Tile})
		if len(f) == 0 || f[0].Stage != sbx.Exit || !strings.Contains(f[0].Error, "the VM exited") {
			t.Fatalf("failures: %+v", f)
		}
	})

	t.Run("delete", func(t *testing.T) {
		le.t = t
		d, _ := le.m.defs.get(le.k, "vm-1")
		dir, _ := le.m.StateDir(le.k, d)
		le.want(le.do(mgr, "DELETE", "/sandboxes/vm-1", nil), http.StatusNoContent, "")
		le.m.trash.wait()
		if _, err := os.Lstat(dir); !os.IsNotExist(err) {
			t.Fatalf("%s is still there: %v", dir, err)
		}
	})
}

// vmmPid is the VMM (Firecracker, or QEMU emulating) among the shim's
// descendants (0: none).
func vmmPid(shim int) int {
	parents := map[int]int{}
	comm := map[int]string{}
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		p, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		stat, _ := os.ReadFile("/proc/" + e.Name() + "/stat")
		i := strings.LastIndexByte(string(stat), ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(string(stat[i+1:]))
		if len(f) > 1 {
			parents[p] = atoi(f[1])
		}
		c, _ := os.ReadFile("/proc/" + e.Name() + "/comm")
		comm[p] = strings.TrimSpace(string(c))
	}
	for p, c := range comm {
		if c != "firecracker" && c != "qemu" { // their names in the jail (vm.Apply's inFC, inQEMU)
			continue
		}
		for q := parents[p]; q > 1; q = parents[q] {
			if q == shim {
				return p
			}
		}
	}
	return 0
}
