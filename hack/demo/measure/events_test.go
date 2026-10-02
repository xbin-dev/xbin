//go:build linux && xbinmeasure

package measure

// events_test.go — a tap on xbind's event stream (/ws/events, the owner's):
// every event with the time it arrived, so a save's phases (reload,
// build-start, build-ok) can be placed on the clock.

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

type event struct {
	Type      string          `json:"type"`
	Component string          `json:"component"`
	Text      string          `json:"text"`
	Data      json.RawMessage `json:"data"`
	at        time.Time
}

type tap struct {
	mu     sync.Mutex
	evs    []event
	notify chan struct{}
}

func tapEvents(t *testing.T, d *xbindtest.Daemon) *tap {
	t.Helper()
	c, r, err := d.Dial(t, "/ws/events")
	if err != nil {
		t.Fatalf("/ws/events: %v %d %s", err, r.Status, r)
	}
	e := &tap{notify: make(chan struct{}, 1)}
	go func() {
		for {
			_, msg, err := c.ReadMessage()
			at := time.Now()
			if err != nil {
				return
			}
			var ev event
			if json.Unmarshal(msg, &ev) != nil {
				continue
			}
			ev.at = at
			e.mu.Lock()
			e.evs = append(e.evs, ev)
			e.mu.Unlock()
			select {
			case e.notify <- struct{}{}:
			default:
			}
		}
	}()
	t.Cleanup(func() { c.Close() })
	return e
}

// mark is a cursor: what arrives after it.
func (e *tap) mark() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.evs)
}

// first is the first event after the cursor of the type for comp.
func (e *tap) first(from int, typ, comp string) (time.Time, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, ev := range e.evs[from:] {
		if ev.Type == typ && ev.Component == comp {
			return ev.at, true
		}
	}
	return time.Time{}, false
}

// next waits up to timeout for first to find one.
func (e *tap) next(from int, typ, comp string, timeout time.Duration) (time.Time, bool) {
	deadline := time.After(timeout)
	for {
		if at, ok := e.first(from, typ, comp); ok {
			return at, true
		}
		select {
		case <-e.notify:
		case <-deadline:
			return time.Time{}, false
		}
	}
}

// settle waits until comp has had no event for quiet: the last save's
// batch is over.
func (e *tap) settle(comp string, quiet time.Duration) {
	for {
		m := e.mark()
		time.Sleep(quiet)
		e.mu.Lock()
		busy := false
		for _, ev := range e.evs[m:] {
			if ev.Component == comp {
				busy = true
			}
		}
		e.mu.Unlock()
		if !busy {
			return
		}
	}
}

// describe lists comp's events after the cursor, relative to t0.
func (e *tap) describe(from int, comp string, t0 time.Time) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var b []string
	for _, ev := range e.evs[from:] {
		if ev.Component == comp {
			b = append(b, fmt.Sprintf("%s@%.1fms", ev.Type, ms(ev.at.Sub(t0))))
		}
	}
	return strings.Join(b, " ")
}
