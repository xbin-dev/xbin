package events

import (
	"encoding/json"
	"testing"
)

// covers SC-ZERO PD-29 — Event.Partition is absent from the wire unless set
// (every event of today keeps its bytes); a user partition's event names it
// before its data (plans/partitions/02 §9).
func TestEventPartitionField(t *testing.T) {
	for _, tc := range []struct {
		e    Event
		want string
	}{
		{Event{Type: "status", Component: "apps/a", Data: map[string]string{"level": "ok"}}, `{"type":"status","component":"apps/a","data":{"level":"ok"}}`},
		{Event{Type: "status", Component: "apps/a", Partition: "user:ana", Data: map[string]string{"level": "ok"}},
			`{"type":"status","component":"apps/a","partition":"user:ana","data":{"level":"ok"}}`},
		{Event{Type: "bus", Topic: "res:apps/a/feed/x", Deployment: "dev", Partition: "user:ana"},
			`{"type":"bus","topic":"res:apps/a/feed/x","deployment":"dev","partition":"user:ana"}`},
	} {
		b, err := json.Marshal(tc.e)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != tc.want {
			t.Errorf("marshal %+v:\n got %s\nwant %s", tc.e, b, tc.want)
		}
	}
}
