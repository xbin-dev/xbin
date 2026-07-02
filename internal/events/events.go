// Package events is the fan-out hub behind /ws/events: live-reload
// notifications, build status, component logs, and (phase 4) bus messages.
// Wire format is documented in docs/protocol.md.
package events

import (
	"sync"
)

// Event is one message on the hub. JSON-encoded on the wire.
type Event struct {
	Type      string `json:"type"`                // reload|build-start|build-error|build-ok|backend|log|bus|grants
	Component string `json:"component,omitempty"` // workspace-relative path
	Text      string `json:"text,omitempty"`      // human text (compiler output, log line)
	Topic     string `json:"topic,omitempty"`     // bus: resource-qualified topic "res:scope/name/topic"
	Data      any    `json:"data,omitempty"`      // bus payload / structured extras
}

// Filter decides whether a subscriber receives an event. Non-bus events are
// visible to every authenticated subscriber; bus events are grant-checked by
// the broker-installed filter.
type Filter func(Event) bool

type sub struct {
	ch     chan Event
	filter Filter
}

type Hub struct {
	mu   sync.Mutex
	subs map[*sub]struct{}
}

func NewHub() *Hub {
	return &Hub{subs: map[*sub]struct{}{}}
}

// Subscribe registers a subscriber. The returned channel is buffered; a
// subscriber that falls more than the buffer behind is dropped (its channel
// is closed) — slow clients must reconnect rather than stall the hub.
func (h *Hub) Subscribe(filter Filter) (<-chan Event, func()) {
	s := &sub{ch: make(chan Event, 64), filter: filter}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	cancel := func() {
		h.mu.Lock()
		if _, ok := h.subs[s]; ok {
			delete(h.subs, s)
			close(s.ch)
		}
		h.mu.Unlock()
	}
	return s.ch, cancel
}

func (h *Hub) Publish(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		if s.filter != nil && !s.filter(e) {
			continue
		}
		select {
		case s.ch <- e:
		default: // evict slow client
			delete(h.subs, s)
			close(s.ch)
		}
	}
}
