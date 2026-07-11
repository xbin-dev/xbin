package sandbox

import "testing"

// Replays the prod failure: the SDK ExtraBind degenerated to the install
// prefix (/opt/xbin) and, appended last, shadowed the rw $HOME/component
// binds beneath it. sortBinds must mount ancestors first so the deep rw
// binds stay on top, while equal-depth binds keep their caller order.
func TestSortBindsAncestorsFirst(t *testing.T) {
	in := []Bind{
		{Src: "/opt/xbin/workspace", Dst: "/opt/xbin/workspace", RO: true},
		{Src: "/opt/xbin/workspace/home", Dst: "/opt/xbin/workspace/home"},
		{Src: "/opt/xbin/workspace/apps/x", Dst: "/opt/xbin/workspace/apps/x"},
		{Src: "/opt/xbin", Dst: "/opt/xbin", RO: true}, // the broad late bind
	}
	got := sortBinds(in)

	pos := map[string]int{}
	for i, b := range got {
		pos[b.Dst] = i
	}
	for _, pair := range [][2]string{
		{"/opt/xbin", "/opt/xbin/workspace"},
		{"/opt/xbin/workspace", "/opt/xbin/workspace/home"},
		{"/opt/xbin/workspace", "/opt/xbin/workspace/apps/x"},
	} {
		if pos[pair[0]] >= pos[pair[1]] {
			t.Errorf("ancestor %s must mount before %s (got order %v)", pair[0], pair[1], got)
		}
	}

	// Stable within a depth: home (declared first) stays before... they differ
	// in depth; use two same-depth entries to check stability instead.
	same := []Bind{{Dst: "/a/b/c"}, {Dst: "/a/b/d"}}
	s := sortBinds(same)
	if s[0].Dst != "/a/b/c" || s[1].Dst != "/a/b/d" {
		t.Errorf("equal-depth binds must keep caller order, got %v", s)
	}

	// Input must not be mutated (init may retry / log it).
	if in[3].Dst != "/opt/xbin" || in[0].Dst != "/opt/xbin/workspace" {
		t.Error("sortBinds mutated its input")
	}
}
