package runner

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sbx"
	"github.com/xbin-dev/xbin/internal/util"
)

// A generation is listed with how it is isolated — a VM's size and VMM from
// its reservation — until its remove.
func TestSandboxEntries(t *testing.T) {
	reg := sbx.New()
	r := &Runner{Sandboxes: reg}
	goComp := &registry.Component{Path: "apps/x", Manifest: registry.Manifest{Runtime: "go"}}
	vmComp := &registry.Component{Path: "apps/v", Manifest: registry.Manifest{Runtime: "go", VM: &registry.VMOpt{On: true}}}
	static := &registry.Component{Path: "apps/s", Manifest: registry.Manifest{Runtime: "static"}}

	if r.intendedMode(goComp) != sbx.Host || r.intendedMode(vmComp) != sbx.Host {
		t.Fatal("without isolation everything runs on the host")
	}
	r.Isolate = true
	if r.intendedMode(goComp) != sbx.Namespace || r.intendedMode(vmComp) != sbx.VM || r.intendedMode(static) != sbx.Host {
		t.Fatal("intended modes")
	}

	rm := r.sbxAdd(goComp, 3, "/run/apps~x/g3.sock", 101)
	r.vms.res = map[string]vmRes{"/run/apps~v/g1.sock": {memMiB: 1024, vcpus: 2, emulated: true}}
	rmVM := r.sbxAdd(vmComp, 1, "/run/apps~v/g1.sock", 202)
	l := reg.List(sbx.Filter{})
	if len(l) != 2 {
		t.Fatalf("entries: %+v", l)
	}
	v, x := l[0], l[1]
	if v.ID != "backend:"+util.CompKey("apps/v")+":g1" || v.Mode != sbx.VM || v.Accel != sbx.Emulate || v.MemMiB != 1024 || v.VCPUs != 2 || v.PID != 202 || v.Kind != sbx.Backend {
		t.Fatalf("vm entry: %+v", v)
	}
	if x.Mode != sbx.Namespace || x.Gen != 3 || x.Accel != "" || x.Leaf != "" {
		t.Fatalf("namespace entry: %+v", x)
	}
	rm()
	rmVM()
	if n := len(reg.List(sbx.Filter{})); n != 0 {
		t.Fatalf("%d left", n)
	}

	// refusals and start failures are recorded under the mode it asked for
	r.sbxFail(vmComp, sbx.Start, sbx.Refuse(errors.New("budget spent")))
	r.sbxFail(goComp, sbx.Start, errors.New("no rootfs"))
	f := reg.Failures(sbx.Filter{})
	if len(f) != 2 || f[0].Stage != sbx.Start || f[0].Mode != sbx.Namespace || f[1].Stage != sbx.Refused || f[1].Mode != sbx.VM {
		t.Fatalf("failures: %+v", f)
	}
}

// Only the sandbox layer's own exits are failures: the VM shim's 125, the
// init's 127 at start — not a backend's crash, not a stop.
func TestSandboxExited(t *testing.T) {
	reg := sbx.New()
	r := &Runner{Sandboxes: reg, Isolate: true}
	c := &registry.Component{Path: "apps/x", Manifest: registry.Manifest{Runtime: "go"}}
	exit := func(code string) *os.ProcessState {
		cmd := exec.Command("sh", "-c", "exit "+code) // exec-ok: test
		_ = cmd.Run()
		return cmd.ProcessState
	}
	r.sbxExited(c, sbx.VM, exit("125"), time.Now().Add(-time.Hour))
	r.sbxExited(c, sbx.Namespace, exit("127"), time.Now())
	r.sbxExited(c, sbx.Namespace, exit("127"), time.Now().Add(-time.Minute)) // later: the backend's own
	r.sbxExited(c, sbx.Namespace, exit("1"), time.Now())
	r.sbxExited(c, sbx.Host, exit("125"), time.Now())
	f := reg.Failures(sbx.Filter{})
	if len(f) != 2 || f[0].Stage != sbx.Exit || !strings.Contains(f[0].Error, "(127)") || !strings.Contains(f[1].Error, "VM exited (125)") {
		t.Fatalf("failures: %+v", f)
	}
}

// A backend that exits before it listens fails at once.
func TestWaitHealthyExited(t *testing.T) {
	dir := t.TempDir()
	exited := make(chan struct{})
	close(exited)
	start := time.Now()
	if err := waitHealthy(filepath.Join(dir, "none.sock"), exited, time.Minute); !errors.Is(err, errExited) || time.Since(start) > 5*time.Second {
		t.Fatalf("err %v after %v", err, time.Since(start))
	}
	sock := filepath.Join(dir, "up.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if err := waitHealthy(sock, make(chan struct{}), time.Second); err != nil {
		t.Fatal(err)
	}
}

// A terminal or agent is sampled by its process tree when it has no leaf.
func TestSandboxStats(t *testing.T) {
	reg := sbx.New()
	r := &Runner{Sandboxes: reg}
	defer reg.Add(sbx.Entry{ID: "term1", Kind: sbx.Terminal, Tile: "apps/x", PID: os.Getpid()})()
	reg.Add(sbx.Entry{ID: "backend:apps~x:g1", Kind: sbx.Backend, Tile: "apps/x", PID: os.Getpid()})
	r.statsSample()
	r.stats.mu.Lock()
	ser := r.stats.series[sandboxKey("term1")]
	_, backendByID := r.stats.series[sandboxKey("backend:apps~x:g1")]
	r.stats.mu.Unlock()
	if len(ser) != 1 || ser[0].Mem < 1<<20 || ser[0].Pids < 1 {
		t.Fatalf("terminal series: %+v", ser)
	}
	if backendByID {
		t.Fatal("a backend is sampled by its tile, not its entry")
	}
	snap := r.StatsSnapshot()
	if tiles := snap["tiles"].([]TileStats); len(tiles) != 0 {
		t.Fatalf("a sandbox series leaked into the tiles: %+v", tiles)
	}
	by, _, _ := r.SandboxStats()
	if by["term1"].Mem == 0 {
		t.Fatalf("SandboxStats: %+v", by)
	}
}
