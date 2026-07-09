package xbin

import (
	"net/http"
	"testing"
	"time"
)

// The backend server must reap idle keep-alive connections: without
// IdleTimeout, every conn xbind's pooled proxy parks holds a goroutine +
// buffers forever (one per RPC before pooling — measured in the wild). The
// idle timeout must exceed the proxy's 90s IdleConnTimeout so the client
// closes first.
func TestServerTimeouts(t *testing.T) {
	h := http.NewServeMux()
	srv := newServer(h)
	if srv.Handler == nil {
		t.Fatal("handler not set")
	}
	if srv.IdleTimeout != 120*time.Second {
		t.Fatalf("IdleTimeout = %v, want 120s", srv.IdleTimeout)
	}
	if srv.IdleTimeout <= 90*time.Second {
		t.Fatal("IdleTimeout must exceed the proxy's 90s IdleConnTimeout")
	}
	if srv.ReadHeaderTimeout != 30*time.Second {
		t.Fatalf("ReadHeaderTimeout = %v, want 30s", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout != 0 || srv.WriteTimeout != 0 {
		t.Fatal("no blanket read/write timeouts — SSE/streams run indefinitely")
	}
}
