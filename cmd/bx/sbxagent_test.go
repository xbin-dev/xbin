package main

import "testing"

func TestSbxAgentArgs(t *testing.T) {
	for _, c := range []struct {
		args []string
		want sbxArgs
		ok   bool
	}{
		{[]string{"--fd", "5"}, sbxArgs{fd: 5}, true},
		{[]string{"--fd", "5", "--lock", "6"}, sbxArgs{fd: 5, lock: 6}, true},
		{[]string{"--lock", "6", "--fd", "5"}, sbxArgs{fd: 5, lock: 6}, true},
		{[]string{"--fd", "4", "--lock", "5", "--fuse-pid", "2"}, sbxArgs{fd: 4, lock: 5, fusePID: 2}, true},
		{[]string{"--fuse-pid", "9", "--fd", "4"}, sbxArgs{fd: 4, fusePID: 9}, true},
		{nil, sbxArgs{}, false},
		{[]string{"--lock", "6"}, sbxArgs{}, false},
		{[]string{"--fd"}, sbxArgs{}, false},
		{[]string{"--fd", "2"}, sbxArgs{}, false},
		{[]string{"--fd", "x"}, sbxArgs{}, false},
		{[]string{"--fd", "5", "--fd", "6"}, sbxArgs{}, false},
		{[]string{"--fd", "5", "--lock", "5"}, sbxArgs{}, false},
		{[]string{"--fd", "5", "--lock", "2"}, sbxArgs{}, false},
		{[]string{"--fd", "5", "--other", "6"}, sbxArgs{}, false},
		{[]string{"--fd", "5", "--fuse-pid", "1"}, sbxArgs{}, false}, // the agent itself
		{[]string{"--fd", "5", "--fuse-pid", "2", "--fuse-pid", "3"}, sbxArgs{}, false},
	} {
		got, err := sbxAgentArgs(c.args)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("%q: %+v err %v", c.args, got, err)
		}
	}
}
