package acp

// Is the agent signed in? An adapter says it is not in three ways — an
// "auth required" error (-32000) to a session or a prompt, a status update
// (_auth/status_update: none), or a failure in its own words — and a host
// has to tell a person what to do about it. The detection and the one
// sentence are here, the same for every host:
//
//   - IsAuthRequired: the error is the agent's auth-required answer;
//   - SaysSignedOut: a status detail or a turn's error reads as "sign in";
//   - AuthState.SignedOut: whether to treat the session as signed out,
//     for a host that may have put a credential of its own in the agent's
//     environment (an adapter's status probe can call such a sign-in
//     "none": only its own refusal counts then);
//   - Provider.SignInHint: "the agent isn't signed in — run: ‹command›".

import (
	"errors"
	"strings"
)

// IsAuthRequired reports whether err is (or wraps) the agent's
// auth-required answer (CodeAuthRequired, -32000): it refused a session or
// a prompt because it is signed out.
func IsAuthRequired(err error) bool {
	var re *Error
	return errors.As(err, &re) && re.Code == CodeAuthRequired
}

// SaysSignedOut reports whether a status detail or a turn's error text
// says the agent is signed out: an adapter that fails in its own words
// ("Authentication required …") rather than with -32000, or a text that
// already carries SignInHint.
func SaysSignedOut(text string) bool {
	return strings.Contains(text, "Authentication required") || strings.Contains(text, "isn't signed in")
}

// AuthState is what a host knows about the agent's sign-in.
type AuthState struct {
	// Needed: the session says it is signed out (SessionState.AuthNeeded,
	// a status event's login).
	Needed bool
	// Injected: the host put a credential of its own in the agent's
	// environment (a saved token or key).
	Injected bool
	// Refused: the agent itself refused a session or a prompt as signed
	// out (IsAuthRequired; a ClientOptions.AuthHint call) since it started.
	Refused bool
}

// SignedOut reports whether the session is to be treated as signed out:
// it says so — and, while a credential of the host's is in its
// environment, the agent itself refused (a status update alone doesn't
// count then: claude-agent-acp 0.81 reports an environment token's
// sign-in as "none").
func (a AuthState) SignedOut() bool { return a.Needed && (!a.Injected || a.Refused) }

// SignInHint is the one sentence that tells a person the agent is signed
// out and how to sign it in: "the agent isn't signed in — run: ‹command›",
// the command being the provider's LoginCmd (the line that works in a
// terminal where the agent runs: it prints a URL or a code), else its
// Login, else "the CLI's own login". where names the place to run it when
// the reader may be somewhere else ("a terminal on apps/x"): "… — in
// ‹where›, run: ‹command›". The default text of an auth-required error
// (ClientOptions.AuthHint) is the agent's message, ": ", and this.
func (p Provider) SignInHint(where string) string {
	how := p.LoginCmd
	if how == "" {
		how = p.Login
	}
	if how == "" {
		how = "the CLI's own login"
	}
	if where != "" {
		return "the agent isn't signed in — in " + where + ", run: " + how
	}
	return "the agent isn't signed in — run: " + how
}
