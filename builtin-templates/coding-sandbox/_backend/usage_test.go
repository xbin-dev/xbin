package main

// usage_test.go — the runtime's CPU and memory (SandboxInfo.usage, D215)
// passed on to consumers as the contract's optional usage: a GET of one
// sandbox with its series, the list and every other answer without it,
// nothing when the runtime has none.

import (
	"reflect"
	"testing"

	xbin "github.com/xbin-dev/xbin/sdk"
	"github.com/xbin-dev/xbin/sdk/sandboxcontract"
)

func TestXbinBackendUsage(t *testing.T) {
	rt := newRuntime("namespace", "vm")
	m, _, tg := xbinManager(t, rt, nil)
	a := tg.As(t, "apps/agent").Verified("alice")
	sb := a.Create(map[string]any{"name": "api"})
	if u := a.Get(sb.ID).Usage; u != nil {
		t.Fatalf("usage the runtime never gave: %+v", u)
	}

	series := []xbin.SandboxUsagePoint{{T: 1790000100000, CPU: 41.5, Mem: 2600000000}, {T: 1790000160000, CPU: 63.5, Mem: 2791728742}}
	runtime := m.recCopy(sb.ID).Runtime
	rt.mu.Lock()
	rt.boxes[runtime].Usage = &xbin.SandboxLiveUsage{At: 1790000160000, CPUPct: 63.5, MemBytes: 2791728742,
		MemMaxBytes: 4294967296, Pids: 41, Series: series} // the double's list sends it with its series: the manager drops them
	rt.mu.Unlock()

	u := a.Get(sb.ID).Usage
	if u == nil || u.At != 1790000160000 || u.CPUPct != 63.5 || u.MemBytes != 2791728742 || u.MemMaxBytes != 4294967296 || u.Pids != 41 || len(u.Series) != 2 {
		t.Fatalf("GET's usage: %+v", u)
	}
	for i, p := range u.Series {
		if p.T != series[i].T || p.CPU != series[i].CPU || p.Mem != series[i].Mem {
			t.Fatalf("GET's series[%d] = %+v, want %+v", i, p, series[i])
		}
	}

	var list struct{ Sandboxes []map[string]any }
	a.Call("GET", "/sandboxes", nil, 200, &list)
	if len(list.Sandboxes) != 1 {
		t.Fatalf("the list: %+v", list)
	}
	lu, _ := list.Sandboxes[0]["usage"].(map[string]any)
	if _, has := lu["series"]; lu == nil || has || lu["cpuPct"] != 63.5 || lu["memMaxBytes"] != 4294967296.0 || lu["pids"] != 41.0 {
		t.Fatalf("the list's usage (no series): %v", lu)
	}

	// another answer carrying the sandbox (a PATCH) has no series either
	var patched sandboxcontract.Sandbox
	a.Call("PATCH", "/sandboxes/"+sb.ID, map[string]any{"name": "api2"}, 200, &patched)
	if patched.Usage == nil || patched.Usage.CPUPct != 63.5 || len(patched.Usage.Series) != 0 {
		t.Fatalf("PATCH's usage: %+v", patched.Usage)
	}

	// stopped: the runtime has none, nor does the manager
	a.Call("POST", "/sandboxes/"+sb.ID+"/stop", nil, 200, nil)
	if u := a.Get(sb.ID).Usage; u != nil {
		t.Fatalf("a stopped sandbox's usage: %+v", u)
	}
}

// The view never invents usage, and passes the substrate's on unchanged
// but for the series, which only withSeries (a GET of one) adds.
func TestViewUsage(t *testing.T) {
	m := &Manager{}
	in := &xbin.SandboxInfo{State: "running", Usage: &xbin.SandboxLiveUsage{At: 5, CPUPct: 1.5, MemBytes: 7, Pids: 2,
		Series: []xbin.SandboxUsagePoint{{T: 5, CPU: 1.5, Mem: 7}}}}
	v := m.view(record{ID: "sb-1"}, in, caller{}, nil)
	want := xbin.SandboxLiveUsage{At: 5, CPUPct: 1.5, MemBytes: 7, Pids: 2}
	if v.Usage == nil || !reflect.DeepEqual(*v.Usage, want) {
		t.Fatalf("view: %+v", v.Usage)
	}
	if g := withSeries(v, in); len(g.Usage.Series) != 1 || len(in.Usage.Series) != 1 {
		t.Fatalf("withSeries: %+v (the substrate's: %+v)", g.Usage, in.Usage)
	}
	if v := m.view(record{ID: "sb-1"}, &xbin.SandboxInfo{State: "running"}, caller{}, nil); v.Usage != nil {
		t.Fatalf("usage without the substrate's: %+v", v.Usage)
	}
	if v := withSeries(m.view(record{ID: "sb-1"}, nil, caller{}, nil), nil); v.Usage != nil {
		t.Fatalf("usage without info: %+v", v.Usage)
	}
}
