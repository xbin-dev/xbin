package main

import "testing"

func TestNextArg(t *testing.T) {
	args := []string{"set", "devs", "--name", "Devs", "--sets"}
	i := 2
	v, err := nextArg(args, &i)
	if err != nil || v != "Devs" || i != 3 {
		t.Fatalf("got %q %v i=%d", v, err, i)
	}
	i = 4 // "--sets" is the last argument: no value
	if _, err := nextArg(args, &i); err == nil || err.Error() != "--sets needs a value" {
		t.Fatalf("missing value: got %v", err)
	}
	if i != 4 {
		t.Fatalf("i advanced on error: %d", i)
	}
}

func TestUnknownFlag(t *testing.T) {
	if err := unknownFlag("x", "--nope", false); err == nil || err.Error() != "unknown flag --nope" {
		t.Errorf("strict: %v", err)
	}
	if err := unknownFlag("x", "--nope", true); err != nil {
		t.Errorf("lenient must warn, not fail: %v", err)
	}
	for a, want := range map[string]bool{"--x": true, "-x": true, "-": false, "apps/x": false, "": false} {
		if got := isFlag(a); got != want {
			t.Errorf("isFlag(%q) = %v", a, got)
		}
	}
}
