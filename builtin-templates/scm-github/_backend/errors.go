// errors.go — the scm contract's refusals (docs/scm.md §Errors) and
// secretString, the one type a token or key is held in.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
)

// Refusals: the sandbox-manager contract's, plus the scm contract's own.
const (
	refProtocol     = "protocol"      // 400 (protocols)
	refInvalid      = "invalid"       // 400
	refNotFound     = "not-found"     // 404
	refNotAllowed   = "not-allowed"   // 403 (sso)
	refExists       = "exists"        // 409
	refPrecondition = "precondition"  // 412
	refLimit        = "limit"         // 429 (retryAfterMs)
	refUnsupported  = "unsupported"   // 501
	refUnavailable  = "unavailable"   // 503 (retryAfterMs)
	refSignin       = "signin"        // 409 (signin)
	refNotInstalled = "not-installed" // 409 (install)
	refIdentity     = "identity"      // 403 (identities)
	refSetup        = "setup"         // 503
	refUpstream     = "upstream"      // 502 (upstream)
	refInProgress   = "in-progress"   // 409 (url)
)

// refusalStatus is each refusal's HTTP status.
var refusalStatus = map[string]int{
	refProtocol: 400, refInvalid: 400, refNotFound: 404, refNotAllowed: 403, refExists: 409,
	refPrecondition: 412, refLimit: 429, refUnsupported: 501, refUnavailable: 503, refSignin: 409,
	refNotInstalled: 409, refIdentity: 403, refSetup: 503, refUpstream: 502, refInProgress: 409,
}

// scmErr is a refusal: {error, refusal, …} with its status. Its message is
// for people and never carries a secret or a body GitHub's users wrote.
type scmErr struct {
	Status       int          `json:"-"`
	Message      string       `json:"error"`
	Refusal      string       `json:"refusal"`
	RetryAfterMs int64        `json:"retryAfterMs,omitempty"`
	Protocols    []int        `json:"protocols,omitempty"`
	Signin       *signinInfo  `json:"signin,omitempty"`
	Install      *installInfo `json:"install,omitempty"`
	Identities   []string     `json:"identities,omitempty"`
	Upstream     *upstreamErr `json:"upstream,omitempty"`
	SSO          *ssoInfo     `json:"sso,omitempty"`
	URL          string       `json:"url,omitempty"`
}

type installInfo struct {
	URL   string `json:"url"`
	Owner string `json:"owner"`
}

type upstreamErr struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
}

type ssoInfo struct {
	URL string `json:"url"`
}

func (e *scmErr) Error() string { return e.Refusal + ": " + e.Message }

// refuse makes a refusal with its contract status.
func refuse(refusal, format string, a ...any) *scmErr {
	st := refusalStatus[refusal]
	if st == 0 {
		st = http.StatusInternalServerError
	}
	return &scmErr{Status: st, Refusal: refusal, Message: fmt.Sprintf(format, a...)}
}

// asRefusal turns any error into a refusal: an *scmErr as it is, anything
// else 503 unavailable (a store or network failure inside the provider).
func asRefusal(err error) *scmErr {
	var e *scmErr
	if errors.As(err, &e) {
		return e
	}
	return refuse(refUnavailable, "the provider couldn't finish: %s", clip(err.Error(), 300))
}

// isRefusal says whether err is a refusal of that kind.
func isRefusal(err error, refusal string) bool {
	var e *scmErr
	return errors.As(err, &e) && e.Refusal == refusal
}

// fail writes err as the contract's error body.
func fail(w http.ResponseWriter, err error) {
	e := asRefusal(err)
	if e.RetryAfterMs > 0 {
		w.Header().Set("Retry-After", strconv.FormatInt((e.RetryAfterMs+999)/1000, 10))
	}
	writeJSON(w, e.Status, e)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// secretString holds a token, key or secret in memory. It prints and
// marshals as "[secret]" under %v, %+v, %#v, %s and JSON, so a struct
// holding one can be logged or encoded without leaking it; Reveal is the
// one way to the value (an upstream call, the vault, the one response that
// hands out a token — built explicitly).
type secretString struct{ v string }

func newSecret(v string) secretString                { return secretString{v} }
func (s secretString) Reveal() string                { return s.v }
func (s secretString) Empty() bool                   { return s.v == "" }
func (s secretString) String() string                { return "[secret]" }
func (s secretString) GoString() string              { return `"[secret]"` }
func (s secretString) MarshalJSON() ([]byte, error)  { return []byte(`"[secret]"`), nil }
func (s *secretString) UnmarshalJSON(b []byte) error { return json.Unmarshal(b, &s.v) }
func (s secretString) Format(f fmt.State, _ rune)    { _, _ = f.Write([]byte("[secret]")) }

// clip cuts s to at most n bytes on a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}
