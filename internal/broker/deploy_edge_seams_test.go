package broker

import (
	"reflect"
	"testing"

	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers P3 P23 T4 E10 — the spawn-time hooks apply the edge policy to a
// non-primary deployment's view (09-fabric §5.8–§5.9): EgressFor gives it
// the tile's relay policy while the net edge inherits, and no egress once
// the edge is block (a manager's block, or a net that shares the host's);
// NetAdminFor and ContainersFor withhold a capability grant whose edge is
// block. The primary's answers never change.
func TestSpawnHooksApplyEdgePolicy(t *testing.T) {
	b := newEdgeBroker(t)
	none := sandbox.EgressPolicy{}
	edgePlane(t, b, edgeRec{tile: efWeb, primary: util.MainDeployment}, edgeRec{tile: efHostNet, primary: util.MainDeployment})
	primary := b.EgressFor(mustComp(t, b, efWeb))
	if reflect.DeepEqual(primary, none) {
		t.Fatalf("%s's primary has no egress: the fixture proves nothing", efWeb)
	}
	if got := b.EgressFor(edgeDevView(t, b, efWeb)); !reflect.DeepEqual(got, primary) {
		t.Errorf("dev under inherit: %+v, want the tile's %+v", got, primary)
	}
	if got := b.EgressFor(edgeDevView(t, b, efHostNet)); !reflect.DeepEqual(got, none) {
		t.Errorf("dev of a host-sharing tile: %+v, want none", got)
	}
	edgePlane(t, b, edgeRec{tile: efWeb, primary: util.MainDeployment, edges: map[string]string{"slot:net": "block"}})
	if got := b.EgressFor(edgeDevView(t, b, efWeb)); !reflect.DeepEqual(got, none) {
		t.Errorf("dev under a manager's block: %+v, want none", got)
	}
	if got := b.EgressFor(mustComp(t, b, efWeb)); !reflect.DeepEqual(got, primary) {
		t.Errorf("the primary's egress moved with dev's edge: %+v", got)
	}

	for _, c := range []struct {
		edges map[string]string
		want  bool
	}{
		{nil, true},
		{map[string]string{"grant:cap:net-admin": "block", "grant:cap:containers": "block"}, false},
	} {
		edgePlane(t, b, edgeRec{tile: efGPU, primary: util.MainDeployment, edges: c.edges})
		dev, main := edgeDevView(t, b, efGPU), mustComp(t, b, efGPU)
		if b.NetAdminFor(dev) != c.want || b.ContainersFor(dev) != c.want {
			t.Errorf("edges %v: dev holds cap:net-admin %v, cap:containers %v; want %v", c.edges, b.NetAdminFor(dev), b.ContainersFor(dev), c.want)
		}
		if !b.NetAdminFor(main) || !b.ContainersFor(main) {
			t.Errorf("edges %v: the primary lost a capability", c.edges)
		}
	}
}
