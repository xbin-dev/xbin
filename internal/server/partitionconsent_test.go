package server

import (
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/users"
)

// personOnlyData stands in for the broker's consent events' data.
type personOnlyData map[string]any

func (personOnlyData) PersonOnly() bool { return true }

// covers PD-13 06§6.1 — a consent event (`partitions` op consent-needed or
// consent, whose data is PersonOnly) reaches the person's own sockets only:
// never the principals of the tile it names acting in their partition —
// its frames, terminals, its instance of them — so the callee's code never
// learns who tried to reach its people's data or who allowed it; nor
// anyone else, admins and view-as included. A partitions event that isn't
// PersonOnly filters as before.
func TestPartitionPersonOnlyEvents(t *testing.T) {
	s := partServer(t).s
	ana := auth.Principal{UserID: "ana", Via: "session"}
	anaFrame := auth.Principal{Component: "apps/a", UserID: "ana", Via: "frame"}
	anaTerm := auth.Principal{Component: "apps/a", UserID: "ana", Via: "terminal"}
	anaInstance := auth.Principal{Component: "apps/a", Via: "instance", Partition: "user:ana"}
	admin := auth.Principal{UserID: "root", User: &users.User{ID: "root", Role: users.RoleAdmin}, Via: "session"}
	consent := events.Event{Type: "partitions", Component: "apps/a", Partition: "user:ana",
		Data: personOnlyData{"op": "consent-needed", "from": "apps/z", "to": "apps/a"}}
	state := events.Event{Type: "partitions", Component: "apps/a", Partition: "user:ana", Data: map[string]any{"op": "state"}}
	for _, c := range []struct {
		name string
		p    auth.Principal
		e    events.Event
		want bool
	}{
		{"ana's shell", ana, consent, true},
		{"ana's frame of the tile", anaFrame, consent, false},
		{"ana's terminal on the tile", anaTerm, consent, false},
		{"the tile's instance of ana", anaInstance, consent, false},
		{"bob's shell", auth.Principal{UserID: "bob", Via: "session"}, consent, false},
		{"an admin's shell", admin, consent, false},
		{"the owner", auth.Principal{Owner: true, Via: "cookie"}, consent, false},
		{"view-as ana", auth.Principal{UserID: "ana", Via: "session", Impersonator: "owner"}, consent, false},
		{"ana's frame, a partitions event not PersonOnly", anaFrame, state, true},
	} {
		if got := s.eventFilter(c.p)(c.e); got != c.want {
			t.Errorf("%s: delivered %v, want %v", c.name, got, c.want)
		}
	}
}
