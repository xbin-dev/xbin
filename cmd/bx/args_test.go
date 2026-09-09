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
