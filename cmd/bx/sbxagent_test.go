package main

import "testing"

func TestSbxAgentArgs(t *testing.T) {
	for _, c := range []struct {
		args     []string
		fd, lock int
		ok       bool
	}{
		{[]string{"--fd", "5"}, 5, 0, true},
		{[]string{"--fd", "5", "--lock", "6"}, 5, 6, true},
		{[]string{"--lock", "6", "--fd", "5"}, 5, 6, true},
		{nil, 0, 0, false},
		{[]string{"--lock", "6"}, 0, 0, false},
		{[]string{"--fd"}, 0, 0, false},
		{[]string{"--fd", "2"}, 0, 0, false},
		{[]string{"--fd", "x"}, 0, 0, false},
		{[]string{"--fd", "5", "--fd", "6"}, 0, 0, false},
		{[]string{"--fd", "5", "--lock", "5"}, 0, 0, false},
		{[]string{"--fd", "5", "--other", "6"}, 0, 0, false},
	} {
		fd, lock, err := sbxAgentArgs(c.args)
		if (err == nil) != c.ok || fd != c.fd || lock != c.lock {
			t.Errorf("%q: fd %d lock %d err %v", c.args, fd, lock, err)
		}
	}
}
