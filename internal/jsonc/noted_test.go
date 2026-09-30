package jsonc

import "testing"

// covers T1 — a member's own comment (the line comments right above it)
// goes with it; a comment a blank line away, one on the brace's line and
// one after another member's value stay.
func TestDeleteTopLevelNoted(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"{\n  // about the block\n  // more\n  \"template\": {\"a\": 1},\n\n  \"runtime\": \"go\"\n}\n",
			"{\n\n  \"runtime\": \"go\"\n}\n"},
		{"{ // the brace's\n  // header\n\n  // about it\n  \"template\": {},\n  \"x\": 1\n}\n",
			"{ // the brace's\n  // header\n\n  \"x\": 1\n}\n"},
		{"{\n  \"x\": 1, // about x\n  \"template\": {}\n}\n", "{\n  \"x\": 1 // about x\n}\n"},
		{"{\n  // the only one\n  \"template\": {}\n}\n", "{\n}\n"},
		{"{\"template\": {}, \"x\": 1}", "{\"x\": 1}"},
		{"{\n  \"x\": 1\n}\n", "{\n  \"x\": 1\n}\n"},
	} {
		got, err := DeleteTopLevelNoted([]byte(c.in), "template")
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if string(got) != c.want {
			t.Errorf("%q:\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}
