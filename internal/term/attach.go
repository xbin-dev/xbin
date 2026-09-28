package term

// The per-client half of a session is the /ws/term wire (internal/termwire,
// docs/protocol.md §/ws/term): the hub replays the scrollback, fans the PTY
// out, acks input for the predictive echo (D70) and sends the exit. This
// adapter gives it the session frame and the PTY.

import (
	"github.com/creack/pty"
	"github.com/gorilla/websocket"

	"github.com/xbin-dev/xbin/internal/termwire"
)

func (s *Session) attach(conn *websocket.Conn) {
	s.hub.Attach(conn, map[string]any{
		"id": s.ID, "net": s.Net, "baseOutdated": s.baseOld,
		"label": s.Label, "scopes": s.Scopes, "netNote": s.NetNote,
		"vm": s.vm,
	}, termwire.Terminal{
		Write: func(b []byte) error { _, err := s.pty.Write(b); return err },
		Resize: func(cols, rows uint16) {
			_ = pty.Setsize(s.pty, &pty.Winsize{Cols: cols, Rows: rows})
		},
	})
}
