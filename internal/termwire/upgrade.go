package termwire

import (
	"net/http"

	"github.com/gorilla/websocket"
)

// ReadLimit bounds one inbound message on a tile-facing socket (the tile
// TTY, plans/tile-sandbox-runtime.md §3.7). A paste arrives as one frame, so
// it is generous; a message past it closes the socket (1009) and the peer
// may reattach.
const ReadLimit = 1 << 20

var upgrader = websocket.Upgrader{
	ReadBufferSize: 4096, WriteBufferSize: 4096,
	// The caller has authenticated the request already: /ws/term is the
	// same-origin app behind the auth middleware, a tile socket answers only
	// a bearer token (which a cross-site page cannot attach).
	CheckOrigin: func(*http.Request) bool { return true },
}

// Upgrade answers an authenticated request's WebSocket handshake for the
// /ws/term wire; hdr rides on the 101 (browsers can't read it, so the session
// frame repeats whatever matters). readLimit > 0 bounds one inbound message —
// tile-facing sockets pass ReadLimit; the browser's own /ws/term passes 0 (no
// limit, as it always had). On error the handshake has already been answered.
func Upgrade(w http.ResponseWriter, r *http.Request, hdr http.Header, readLimit int64) (*websocket.Conn, error) {
	conn, err := upgrader.Upgrade(w, r, hdr)
	if err != nil {
		return nil, err
	}
	if readLimit > 0 {
		conn.SetReadLimit(readLimit)
	}
	return conn, nil
}
