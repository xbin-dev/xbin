package main

import (
	"slices"
	"testing"
)

// The container spec is the tile's security-relevant output: detached, on
// the tile's network when it has one, no inner cgroup management, and a
// keep-alive command unless the caller names one.
func TestCreateArgs(t *testing.T) {
	got := createArgs("", "box1", "ubuntu:24.04", nil)
	want := []string{"run", "-d", "--name", "box1", "--cgroups", "disabled", "ubuntu:24.04", "sleep", "infinity"}
	if !slices.Equal(got, want) {
		t.Errorf("default: %q", got)
	}
	got = createArgs("tilenet", "box2", "alpine", []string{"sh", "-c", "sleep 1"})
	want = []string{"run", "-d", "--name", "box2", "--network", "tilenet", "--cgroups", "disabled", "alpine", "sh", "-c", "sleep 1"}
	if !slices.Equal(got, want) {
		t.Errorf("with network + cmd: %q", got)
	}
}

func TestValidName(t *testing.T) {
	for s, ok := range map[string]bool{"box1": true, "dev-box_2": true, "": false, "../x": false, "a b": false} {
		if validName(s) != ok {
			t.Errorf("validName(%q) = %v", s, !ok)
		}
	}
}
