//go:build linux && integration

// Run with: go test -tags=integration ./internal/sandbox/
// Needs unprivileged user namespaces (skips if unavailable).
package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMain doubles as the re-exec init: when started as `… __sandbox-init
// <spec>`, become the in-namespace init instead of running tests.
func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == InitArg {
		RunInit(os.Args[2]) // never returns
	}
	os.Exit(m.Run())
}

func TestSandboxIsolation(t *testing.T) {
	if !Available() {
		t.Skip("unprivileged user namespaces unavailable")
	}
	dir := t.TempDir()

	// A minimal base rootfs (lower) with just a static probe binary.
	lower := filepath.Join(dir, "lower")
	mkdir(t, lower)
	buildProbe(t, filepath.Join(lower, "probe"))

	// A component dir (ro bind) with a marker, a rw data dir, and a host unix
	// socket to bind in as the "gateway".
	comp := filepath.Join(dir, "comp")
	mkdir(t, comp)
	if err := os.WriteFile(filepath.Join(comp, "marker"), []byte("hello-comp"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A secret beneath the ro bind that a mask must hide (mirrors .xbin/token
	// under the workspace ro bind in a tile terminal — Gap 0).
	mkdir(t, filepath.Join(comp, "hidden"))
	if err := os.WriteFile(filepath.Join(comp, "hidden", "token"), []byte("OWNER-TOKEN"), 0o644); err != nil {
		t.Fatal(err)
	}
	rw := filepath.Join(dir, "rw")
	mkdir(t, rw)
	sockPath := filepath.Join(dir, "gw.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	spec := &Spec{
		Lower: []string{lower},
		Binds: []Bind{
			{Src: comp, Dst: "/component", RO: true},
			{Dst: "/component/hidden", Mask: true, RO: true}, // cover the secret beneath the ro bind
			{Src: rw, Dst: "/data"},
			{Src: sockPath, Dst: "/run/gw.sock"},
		},
		Entry:   "/probe",
		Argv:    []string{"/probe"},
		Env:     []string{"PATH=/"},
		Cwd:     "/",
		HostUID: os.Getuid(),
		HostGID: os.Getgid(),
	}

	cmd, h, err := Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Cleanup()
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(err.Error(), "operation not permitted") {
			t.Skipf("sandbox creation denied by environment: %v\n%s", err, out)
		}
		t.Fatalf("sandbox run: %v\n%s", err, out)
	}

	var res struct {
		HomeAbsent   bool     `json:"homeAbsent"`
		PasswdAbsent bool     `json:"passwdAbsent"`
		Marker       string   `json:"marker"`
		MaskedAbsent bool     `json:"maskedAbsent"`
		RwOK         bool     `json:"rwOK"`
		Ifaces       []string `json:"ifaces"`
		GwOK         bool     `json:"gwOK"`
		GwErr        string   `json:"gwErr"`
	}
	if err := json.Unmarshal(lastJSONLine(out), &res); err != nil {
		t.Fatalf("probe output not JSON: %v\n%s", err, out)
	}

	if !res.HomeAbsent {
		t.Error("host /home is visible inside the sandbox — fs not isolated")
	}
	if !res.PasswdAbsent {
		t.Error("host /etc/passwd is visible inside the sandbox")
	}
	if res.Marker != "hello-comp" {
		t.Errorf("component ro bind not visible: marker=%q", res.Marker)
	}
	if !res.MaskedAbsent {
		t.Error("masked path is still readable beneath the ro bind — mask ineffective (Gap 0)")
	}
	if !res.RwOK {
		t.Error("rw resource bind not writable")
	}
	if got := strings.Join(res.Ifaces, ","); got != "lo" {
		t.Errorf("netns should have only lo, got %q (egress not default-denied)", got)
	}
	if !res.GwOK {
		t.Errorf("bind-mounted gateway socket not reachable: %s", res.GwErr)
	}

	// The rw write must have landed on the host side (real bind, not tmpfs).
	if _, err := os.Stat(filepath.Join(rw, "x")); err != nil {
		t.Errorf("rw write did not reach the host bind: %v", err)
	}
}

// TestSandboxServesUnixSocket proves the runner's core dependency: a backend
// serving on a unix socket *inside* the sandbox is reachable from the host via
// the bound run dir — while the backend itself is FS/network-isolated.
func TestSandboxServesUnixSocket(t *testing.T) {
	if !Available() {
		t.Skip("unprivileged user namespaces unavailable")
	}
	dir := t.TempDir()
	lower := filepath.Join(dir, "lower")
	mkdir(t, lower)
	buildBin(t, filepath.Join(lower, "server"), serverSrc)

	run := filepath.Join(dir, "run") // host side of the backend's run dir
	mkdir(t, run)

	spec := &Spec{
		Lower:   []string{lower},
		Binds:   []Bind{{Src: run, Dst: "/run"}}, // socket the backend creates appears here on the host
		Entry:   "/server",
		Argv:    []string{"/server"},
		Env:     []string{"PATH=/"},
		HostUID: os.Getuid(),
		HostGID: os.Getgid(),
	}
	cmd, h, err := Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Cleanup()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	hostSock := filepath.Join(run, "g.sock")
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", hostSock)
		},
	}}
	var body string
	ok := false
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(hostSock); err == nil {
			if resp, err := client.Get("http://backend/"); err == nil {
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				body = string(b)
				ok = true
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !ok || body != "ok" {
		t.Fatalf("backend not reachable through bound socket (body=%q)", body)
	}
}

func buildBin(t *testing.T, out, src string) {
	t.Helper()
	f := filepath.Join(t.TempDir(), "main.go")
	if err := os.WriteFile(f, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", out, f)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0") // fully static → minimal rootfs
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", out, err, b)
	}
}

func buildProbe(t *testing.T, out string) { buildBin(t, out, probeSrc) }

const serverSrc = `package main

import (
	"net"
	"net/http"
	"os"
)

func main() {
	os.Remove("/run/g.sock")
	l, err := net.Listen("unix", "/run/g.sock")
	if err != nil {
		os.Stderr.WriteString(err.Error())
		os.Exit(1)
	}
	http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
}
`

func mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func lastJSONLine(b []byte) []byte {
	lines := bytes.Split(bytes.TrimSpace(b), []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		if bytes.HasPrefix(bytes.TrimSpace(lines[i]), []byte("{")) {
			return lines[i]
		}
	}
	return b
}

const probeSrc = `package main

import (
	"encoding/json"
	"net"
	"os"
)

func main() {
	r := map[string]any{}
	_, e := os.Stat("/home")
	r["homeAbsent"] = os.IsNotExist(e)
	_, e = os.Stat("/etc/passwd")
	r["passwdAbsent"] = os.IsNotExist(e)
	b, _ := os.ReadFile("/component/marker")
	r["marker"] = string(b)
	_, me := os.ReadFile("/component/hidden/token")
	r["maskedAbsent"] = os.IsNotExist(me)
	r["rwOK"] = os.WriteFile("/data/x", []byte("y"), 0o644) == nil
	ifs, _ := net.Interfaces()
	names := []string{}
	for _, i := range ifs {
		names = append(names, i.Name)
	}
	r["ifaces"] = names
	if c, err := net.Dial("unix", "/run/gw.sock"); err == nil {
		r["gwOK"] = true
		c.Close()
	} else {
		r["gwOK"] = false
		r["gwErr"] = err.Error()
	}
	json.NewEncoder(os.Stdout).Encode(r)
}
`
