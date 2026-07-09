package proxy

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// countingListener counts distinct accepted connections.
type countingListener struct {
	net.Listener
	accepts atomic.Int64
}

func (l *countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.accepts.Add(1)
	}
	return c, err
}

// A proxied request must reuse pooled connections — one fresh Transport per
// request stranded a keep-alive conn (goroutine + buffers) on the backend per
// RPC, forever. This pins the fix: N sequential requests, 1 connection.
func TestBackendTransportReuse(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "g1.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	cl := &countingListener{Listener: ln}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	})}
	go srv.Serve(cl)
	defer srv.Close()

	px := &Proxy{}
	tr := px.transportFor(sock)
	if px.transportFor(sock) != tr {
		t.Fatal("transportFor must cache per socket")
	}
	client := &http.Client{Transport: tr}
	for i := 0; i < 5; i++ {
		resp, err := client.Get("http://xbin/x")
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	if n := cl.accepts.Load(); n != 1 {
		t.Fatalf("5 sequential requests used %d connections, want 1 (pooling broken)", n)
	}

	// A dead generation (socket file gone) is swept: entry evicted, idles closed.
	srv.Close()
	os.Remove(sock)
	px.sweepTransports()
	px.trMu.Lock()
	_, still := px.transports[sock]
	px.trMu.Unlock()
	if still {
		t.Fatal("sweep must evict transports for dead sockets")
	}
}
