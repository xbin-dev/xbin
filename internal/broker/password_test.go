package broker

import "testing"

func TestWeakPassword(t *testing.T) {
	for _, pw := range []string{"", "short", "1234567"} { // < 8
		if weakPassword(pw) == "" {
			t.Errorf("password %q should be rejected", pw)
		}
	}
	for _, pw := range []string{"password", "correct horse", "12345678"} { // >= 8
		if msg := weakPassword(pw); msg != "" {
			t.Errorf("password %q should pass: %s", pw, msg)
		}
	}
}
