package acp

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsAuthRequired(t *testing.T) {
	auth := &Error{Code: CodeAuthRequired, Message: "Please run /login"}
	for _, x := range []struct {
		err  error
		want bool
	}{
		{auth, true},
		{fmt.Errorf("session/new: %w", auth), true},
		{&authError{text: "hint", err: auth}, true}, // the client's own wrapping (ClientOptions.AuthHint)
		{&Error{Code: CodeInvalidParams, Message: "Authentication required"}, false},
		{errors.New("Authentication required"), false}, // words alone are SaysSignedOut's
		{ErrEnded, false},
		{nil, false},
	} {
		if got := IsAuthRequired(x.err); got != x.want {
			t.Errorf("%v: %v, want %v", x.err, got, x.want)
		}
	}
}

func TestSaysSignedOut(t *testing.T) {
	claude, _ := Lookup("claude")
	for text, want := range map[string]bool{
		"Authentication required: OAuth token has been revoked": true,
		"Please run /login: " + claude.SignInHint(""):           true,
		claude.SignInHint("a terminal on apps/x"):               true,
		"the agent did not open a session within 30s":           false,
		"": false,
	} {
		if got := SaysSignedOut(text); got != want {
			t.Errorf("%q: %v, want %v", text, got, want)
		}
	}
}

func TestAuthStateSignedOut(t *testing.T) {
	for _, x := range []struct {
		a    AuthState
		want bool
	}{
		{AuthState{}, false},
		{AuthState{Needed: true}, true},
		{AuthState{Needed: true, Injected: true}, false}, // a status probe that calls an env token "none"
		{AuthState{Needed: true, Injected: true, Refused: true}, true},
		{AuthState{Needed: true, Refused: true}, true},
		{AuthState{Injected: true, Refused: true}, false}, // signed in since (a turn worked)
		{AuthState{Refused: true}, false},
	} {
		if got := x.a.SignedOut(); got != x.want {
			t.Errorf("%+v: %v, want %v", x.a, got, x.want)
		}
	}
}

func TestSignInHint(t *testing.T) {
	codex, _ := Lookup("codex")
	gemini, _ := Lookup("gemini")
	for _, x := range []struct {
		p     Provider
		where string
		want  string
	}{
		{codex, "", "the agent isn't signed in — run: codex login --device-auth"}, // the line that works where the agent runs
		{codex, "a terminal on apps/x", "the agent isn't signed in — in a terminal on apps/x, run: codex login --device-auth"},
		{gemini, "", "the agent isn't signed in — run: NO_BROWSER=true gemini"},
		{Provider{Login: "house login"}, "", "the agent isn't signed in — run: house login"},
		{Provider{}, "", "the agent isn't signed in — run: the CLI's own login"},
	} {
		if got := x.p.SignInHint(x.where); got != x.want {
			t.Errorf("%s %q: %q, want %q", x.p.ID, x.where, got, x.want)
		}
		if !SaysSignedOut(x.p.SignInHint(x.where)) {
			t.Errorf("%s: the hint must read as signed out (a status detail carrying it marks the session)", x.p.ID)
		}
	}
}
