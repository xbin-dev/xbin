package broker

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/magik6k/xbin/internal/auth"
	"github.com/magik6k/xbin/internal/server"
	"github.com/magik6k/xbin/internal/util"
)

// Backend log serving — the HTTP twin of `bx logs [-f]`, and what the
// terminal window's read-only "logs" tab streams. The runner appends every
// backend generation's stdout/stderr to .xbin/log/<compkey>.log with
// generation markers (internal/runner); this serves a tail of that file and,
// with ?follow=1, keeps streaming appended bytes as they land.
//
// Gate (canReadLogs): workspace admins; the tile itself (self-logs, mirroring
// /tile-status); or a human with TERMINAL-level access on the tile — logs are
// the stdout/stderr of code that user could root-shell into anyway, while
// read/write-level users don't get them (output can carry secrets).

const (
	logTailDefault = int64(64 << 10)
	logTailMax     = int64(1 << 20)
)

// logPoll is how often a follow checks for appended bytes (var: tests
// shrink it). 300ms matches `bx logs -f`.
var logPoll = 300 * time.Millisecond

func (b *Broker) registerLogs(srv *server.Server) {
	srv.RegisterAPI("GET /logs", b.apiLogs)
}

func (b *Broker) canReadLogs(p auth.Principal, comp string) bool {
	// CanTerminalTile: admins everywhere; session humans by their resolved
	// (org/team-aware) level; element principals always false there — only
	// the explicit self case admits them.
	return p.Component == comp || p.CanTerminalTile(comp)
}

// apiLogs serves GET /logs?component=<path>[&tail=<bytes>][&follow=1].
// Plain text; follow streams chunked until the client goes away.
func (b *Broker) apiLogs(w http.ResponseWriter, r *http.Request) {
	comp := strings.Trim(r.URL.Query().Get("component"), "/")
	if _, ok := b.Reg.Component(comp); !ok {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such component: " + comp})
		return
	}
	if !b.canReadLogs(auth.PrincipalOf(r), comp) {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{
			"error": "backend logs need admin, the tile itself, or terminal-level access on it", "docs": "/docs/auth.md",
		})
		return
	}
	follow := r.URL.Query().Get("follow") == "1"
	tail := logTailDefault
	if t, err := strconv.ParseInt(r.URL.Query().Get("tail"), 10, 64); err == nil && t >= 0 {
		tail = min(t, logTailMax)
	}

	path := filepath.Join(b.Reg.Root, ".xbin", "log", util.CompKey(comp)+".log")
	f, err := os.Open(path)
	if err != nil && !follow {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no logs yet — the backend hasn't started"})
		return
	}
	defer func() {
		if f != nil {
			f.Close()
		}
	}()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fl, _ := w.(http.Flusher)
	flush := func() {
		if fl != nil {
			fl.Flush()
		}
	}

	// The tail: the last `tail` bytes (partial first line and all — same as
	// `bx logs`), tracking our read position for the follow loop.
	var pos int64
	if f != nil {
		if fi, err := f.Stat(); err == nil {
			start := int64(0)
			if fi.Size() > tail {
				start = fi.Size() - tail
			}
			if _, err := f.Seek(start, io.SeekStart); err == nil {
				n, _ := io.Copy(w, f)
				pos = start + n
			}
		}
	}
	if !follow {
		return
	}
	if f == nil {
		fmt.Fprintf(w, "\x1b[90m[no logs yet — waiting for the backend to start]\x1b[0m\n")
	}
	flush()

	// Follow: poll for growth (the writer holds the file O_APPEND; today it
	// only ever grows — a shrink means someone truncated it, so restart from
	// the top rather than silently stalling).
	tick := time.NewTicker(logPoll)
	defer tick.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if f == nil {
			nf, err := os.Open(path)
			if err != nil {
				continue // still no backend
			}
			f = nf
		}
		fi, err := f.Stat()
		if err != nil {
			return
		}
		if fi.Size() < pos {
			pos = 0
			fmt.Fprintf(w, "\x1b[90m[log truncated — restarting from the top]\x1b[0m\n")
		}
		if fi.Size() == pos {
			continue
		}
		if _, err := f.Seek(pos, io.SeekStart); err != nil {
			return
		}
		n, err := io.Copy(w, io.LimitReader(f, fi.Size()-pos))
		pos += n
		flush()
		if err != nil {
			return // client went away
		}
	}
}
