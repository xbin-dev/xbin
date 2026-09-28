package tilesbx

import (
	"io"
	"net"
	"testing"

	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// A copy's destination gets the terminator — what makes it commit — only
// once the source's last line says the data was whole, so a source that
// fails part-way leaves a file copy uncommitted.
func TestSpliceHoldsTheTerminator(t *testing.T) {
	for _, tc := range []struct {
		name   string
		last   proto.FileResult
		commit bool
	}{
		{"whole", proto.FileResult{OK: true}, true},
		{"failed part-way", proto.FileResult{Error: "read: input/output error"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srcX, srcA := net.Pipe() // xbind's end, the source agent's
			dstX, dstA := net.Pipe()
			defer srcX.Close()
			defer dstX.Close()
			go func() { // the source agent: data, the terminator, its last line
				defer srcA.Close()
				fw := proto.NewFrameWriter(srcA)
				_, _ = fw.Write([]byte("part of the file"))
				_ = fw.Close()
				_ = proto.NewConn(srcA, nil).Send(tc.last)
			}()
			committed := make(chan bool, 1)
			go func() { // the destination agent: commits at the terminator
				defer dstA.Close()
				b, err := io.ReadAll(proto.NewFrameReader(dstA))
				committed <- err == nil && string(b) == "part of the file"
				if err == nil {
					_ = proto.NewConn(dstA, nil).Send(proto.FileResult{OK: true})
				}
			}()
			call := func(c net.Conn) *fileCall {
				return &fileCall{m: &Manager{}, r: &run{b: &box{}, exited: make(chan struct{})}, name: "sb-1", c: proto.NewConn(c, nil)}
			}
			err := splice(call(srcX), call(dstX))
			if got := <-committed; got != tc.commit || (err == nil) != tc.commit {
				t.Fatalf("committed %v, splice: %v", got, err)
			}
		})
	}
}
