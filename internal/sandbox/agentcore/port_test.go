//go:build linux

package agentcore

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// A "port" connection (D135) reaches a TCP server on the loopback: one
// PortReply line, then the stream both ways; a port nothing listens on is
// refused in the reply, and so is one out of range.
func TestPortBridge(t *testing.T) {
	h := newHarness(t, nil)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
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
			go func() { // echo, upper-cased
				defer c.Close()
				b, _ := io.ReadAll(c)
				_, _ = c.Write([]byte(strings.ToUpper(string(b))))
			}()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	reply := func(c net.Conn) (proto.PortReply, *bufio.Reader) {
		t.Helper()
		_ = c.SetReadDeadline(time.Now().Add(hangGuard))
		br := bufio.NewReader(c)
		line, err := br.ReadBytes('\n')
		if err != nil {
			t.Fatalf("no reply: %v", err)
		}
		var r proto.PortReply
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatalf("reply %q: %v", line, err)
		}
		return r, br
	}

	c := h.dial(proto.Hello{Kind: "port", Port: port})
	r, br := reply(c)
	if !r.OK {
		t.Fatalf("reply %+v", r)
	}
	_, _ = c.Write([]byte("hello, sandbox"))
	_ = c.CloseWrite()
	if got, _ := io.ReadAll(br); string(got) != "HELLO, SANDBOX" {
		t.Fatalf("got %q", got)
	}

	ln2, _ := net.Listen("tcp", "127.0.0.1:0") // a port that was free a moment ago
	free := ln2.Addr().(*net.TCPAddr).Port
	ln2.Close()
	if r, _ := reply(h.dial(proto.Hello{Kind: "port", Port: free})); r.OK || !r.Refused || r.Error == "" {
		t.Fatalf("nothing listening: %+v", r)
	}
	for _, p := range []int{0, -1, 65536} {
		if r, _ := reply(h.dial(proto.Hello{Kind: "port", Port: p})); r.OK || r.Error == "" {
			t.Fatalf("port %d: %+v", p, r)
		}
	}
}
