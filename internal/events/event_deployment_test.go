package events

import (
	"encoding/json"
	"testing"
)

// covers D119c PO-5 Z3 — Event.Deployment is absent from the wire unless set:
// every event of today keeps its exact bytes, and a bus event of a non-main
// namespace names its deployment between topic and data (11-contract §3.2).
func TestEventDeploymentField(t *testing.T) {
	for _, tc := range []struct {
		e    Event
		want string
	}{
		{Event{Type: "reload", Component: "apps/crm"}, `{"type":"reload","component":"apps/crm"}`},
		{Event{Type: "build-error", Component: "apps/crm", Text: "x.go:1: nope"}, `{"type":"build-error","component":"apps/crm","text":"x.go:1: nope"}`},
		{Event{Type: "bus", Topic: "res:apps/crm/events/orders", Data: map[string]int{"n": 1}}, `{"type":"bus","topic":"res:apps/crm/events/orders","data":{"n":1}}`},
		{Event{Type: "bus", Topic: "res:apps/crm/events/orders", Deployment: "dev", Data: map[string]int{"n": 1}}, `{"type":"bus","topic":"res:apps/crm/events/orders","deployment":"dev","data":{"n":1}}`},
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
