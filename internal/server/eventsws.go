package server

import (
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"github.com/magik6k/xbin/internal/events"
)

var eventsUpgrader = websocket.Upgrader{
	ReadBufferSize: 1024, WriteBufferSize: 4096,
	CheckOrigin: func(*http.Request) bool { return true }, // auth middleware ran
}

// serveEventsWS streams hub events (JSON text frames) to one subscriber.
// Client → server frames are ignored except pings keeping the socket alive.
func serveEventsWS(w http.ResponseWriter, r *http.Request, hub *events.Hub, filter events.Filter) {
	conn, err := eventsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	ch, cancel := hub.Subscribe(filter)
	defer cancel()

	done := make(chan struct{})
	go func() { // drain reads to detect close
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	defer conn.Close()
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return // evicted as slow client
			}
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteJSON(e); err != nil {
				return
			}
		case <-ping.C:
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-done:
			return
		}
	}
}
