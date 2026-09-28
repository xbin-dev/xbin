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

// helloFields are the session frame's own fields (termwire's SessionFrame
// adds "op" and "echoAck"; docs/protocol.md §/ws/term). deployment is the
// session's echoed target (Manager.echoOf). It, the targetNote, and
// api:false when the target choice's last fallback took the tile API away
// (D127p) or a session that asked for a target has no tile API (a D17
// clamp), are present only for a session that has a target to state: every
// other session's frame is today's.
func (s *Session) helloFields(deployment string) map[string]any {
	h := map[string]any{
		"id": s.ID, "net": s.Net, "baseOutdated": s.baseOld,
		"label": s.Label, "scopes": s.Scopes, "netNote": s.NetNote,
		"vm": s.vm,
	}
	if deployment != "" {
		h["deployment"] = deployment
	}
	if s.target.apiOff {
		h["api"] = false
	}
	if s.target.note != "" {
		h["targetNote"] = s.target.note
	}
	return h
}

// hello is the session frame as the hub sends it, the first message on
// every attach.
func (s *Session) hello(deployment string) []byte {
	return termwire.SessionFrame(s.helloFields(deployment))
}

// attach serves one WebSocket client of the session; deployment is what
// its session frame echoes (helloFields).
func (s *Session) attach(conn *websocket.Conn, deployment string) {
	s.hub.Attach(conn, s.helloFields(deployment), termwire.Terminal{
		Write: func(b []byte) error { _, err := s.pty.Write(b); return err },
		Resize: func(cols, rows uint16) {
			_ = pty.Setsize(s.pty, &pty.Winsize{Cols: cols, Rows: rows})
		},
	})
}
