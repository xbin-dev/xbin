//go:build unix

package portlease

import (
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// A leased port is nobody else's — in this process or another — until its
// holder lets go; then it is anyone's again.
func TestLeaseExcludes(t *testing.T) {
	addr, release, err := Reserve()
	if err != nil {
		t.Fatal(err)
	}
	_, p, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(p)
	if f, err := lease(port); err != nil || f != nil {
		t.Fatalf("a held port was leased again in this process: %v %v", f, err)
	}
	// another process: this test binary, asked to try the same port
	other := func() string {
		cmd := exec.Command(os.Args[0], "-test.run=^TestLeaseHelper$")
		cmd.Env = append(os.Environ(), "PORTLEASE_TRY="+p)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("the helper: %v\n%s", err, out)
		}
		return string(out)
	}
	if out := other(); !contains(out, "lease: held") {
		t.Fatalf("another process leased a held port: %s", out)
	}
	release()
	release() // twice is fine
	if out := other(); !contains(out, "lease: taken") {
		t.Fatalf("a released port stayed held: %s", out)
	}
	f, err := lease(port)
	if err != nil || f == nil {
		t.Fatalf("a released port can't be leased: %v", err)
	}
	f.Close()
}

// TestLeaseHelper is TestLeaseExcludes' other process.
func TestLeaseHelper(t *testing.T) {
	p := os.Getenv("PORTLEASE_TRY")
	if p == "" {
		t.Skip("TestLeaseExcludes' helper")
	}
	port, _ := strconv.Atoi(p)
	f, err := lease(port)
	switch {
	case err != nil:
		t.Fatal(err)
	case f == nil:
		os.Stdout.WriteString("lease: held\n")
	default:
		os.Stdout.WriteString("lease: taken\n")
		f.Close()
	}
}

// Many reservations held at once are all different, and each is a port the
// holder can bind.
func TestReserveDistinct(t *testing.T) {
	seen := map[string]bool{}
	for range 64 {
		addr := Addr(t)
		if seen[addr] {
			t.Fatalf("%s was reserved twice", addr)
		}
		seen[addr] = true
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatalf("a reserved port can't be bound: %v", err)
		}
		ln.Close()
	}
	if n := Port(t); n <= 0 || seen["127.0.0.1:"+strconv.Itoa(n)] {
		t.Fatalf("Port: %d", n)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
