//go:build integration

package test

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestStreaming verifies the gateway supports long-running component APIs:
// SSE responses flush progressively through the reverse proxy, and
// WebSocket upgrades pass through /api/<component>/… end to end.
func TestStreaming(t *testing.T) {
	writeStreamComponent(t)

	// --- SSE: events must arrive as they are produced, not buffered ---
	t.Run("sse", func(t *testing.T) {
		var resp *http.Response
		if !waitFor(func() bool {
			r, err := http.Get(baseURL + "/api/apps/stream/sse")
			if err != nil || r.StatusCode != 200 {
				if r != nil {
					r.Body.Close()
				}
				return false
			}
			resp = r
			return true
		}, 120*time.Second) {
			t.Fatal("stream backend never came up")
		}
		defer resp.Body.Close()

		sc := bufio.NewScanner(resp.Body)
		var stamps []time.Time
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "data: tick") {
				stamps = append(stamps, time.Now())
			}
		}
		if len(stamps) != 5 {
			t.Fatalf("got %d SSE events, want 5", len(stamps))
		}
		// Producer sleeps 150 ms between events; a buffering proxy would
		// deliver them all at once.
		if spread := stamps[len(stamps)-1].Sub(stamps[0]); spread < 300*time.Millisecond {
			t.Fatalf("SSE events arrived in %v — proxy is buffering, not streaming", spread)
		}
	})

	// --- WebSocket: upgrade through the proxy, echo, live for >1 round ---
	t.Run("websocket", func(t *testing.T) {
		wsURL := "ws" + strings.TrimPrefix(baseURL, "http") + "/api/apps/stream/ws"
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("ws dial through /api proxy: %v", err)
		}
		defer conn.Close()
		for i := 0; i < 3; i++ {
			msg := fmt.Sprintf("ping-%d", i)
			if err := conn.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
				t.Fatalf("write %d: %v", i, err)
			}
			conn.SetReadDeadline(time.Now().Add(10 * time.Second))
			_, got, err := conn.ReadMessage()
			if err != nil {
				t.Fatalf("read %d: %v", i, err)
			}
			if string(got) != "echo:"+msg {
				t.Fatalf("echo %d: got %q", i, got)
			}
			time.Sleep(200 * time.Millisecond) // hold the socket across writes
		}
	})
}

func writeStreamComponent(t *testing.T) {
	t.Helper()
	write(t, "apps/stream/buxon.json", `{"runtime":"go"}`)
	write(t, "apps/stream/go.mod", `module stream

go 1.24

require (
	github.com/gorilla/websocket v1.5.3
	github.com/magik6k/buxon/sdk v0.0.0
)
`)
	write(t, "apps/stream/backend/main.go", `package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	buxon "github.com/magik6k/buxon/sdk"
)

var up = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sse", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		fl := w.(http.Flusher)
		for i := 0; i < 5; i++ {
			fmt.Fprintf(w, "data: tick %d\n\n", i)
			fl.Flush()
			time.Sleep(150 * time.Millisecond)
		}
	})
	mux.HandleFunc("GET /ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			mt, msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			if err := c.WriteMessage(mt, append([]byte("echo:"), msg...)); err != nil {
				return
			}
		}
	})
	buxon.Serve(mux)
}
`)
	// The component's go.sum needs gorilla's hashes (the sdk is a use'd
	// go.work module and needs none). Lift them from the repo's own go.sum
	// so the pinned version stays in lockstep.
	repoSum, err := os.ReadFile(filepath.Join(repo, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	var sum strings.Builder
	for _, line := range strings.Split(string(repoSum), "\n") {
		if strings.HasPrefix(line, "github.com/gorilla/websocket ") {
			sum.WriteString(line + "\n")
		}
	}
	write(t, "apps/stream/go.sum", sum.String())
}
