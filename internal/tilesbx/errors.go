package tilesbx

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// The refusals of the sandbox-manager contract (docs/sandbox-manager.md
// "Conventions"): the runtime answers the contract's error enum and statuses,
// so a manager forwards an answer unchanged.
const (
	RefInvalid      = "invalid"      // 400
	RefNotAllowed   = "not-allowed"  // 403
	RefNotFound     = "not-found"    // 404
	RefState        = "state"        // 409
	RefExists       = "exists"       // 409
	RefLost         = "lost"         // 410
	RefPrecondition = "precondition" // 412
	RefTooLarge     = "too-large"    // 413
	RefLimit        = "limit"        // 429
	RefUnsupported  = "unsupported"  // 501
	RefUnavailable  = "unavailable"  // 503
)

var refusalStatus = map[string]int{
	RefInvalid:      http.StatusBadRequest,
	RefNotAllowed:   http.StatusForbidden,
	RefNotFound:     http.StatusNotFound,
	RefState:        http.StatusConflict,
	RefExists:       http.StatusConflict,
	RefLost:         http.StatusGone,
	RefPrecondition: http.StatusPreconditionFailed,
	RefTooLarge:     http.StatusRequestEntityTooLarge,
	RefLimit:        http.StatusTooManyRequests,
	RefUnsupported:  http.StatusNotImplemented,
	RefUnavailable:  http.StatusServiceUnavailable,
}

// Error is a refusal: what the caller asked can't be done, and why.
type Error struct {
	Refusal    string        // one of the Ref* values
	Msg        string        // for people
	State      string        // RefState: the sandbox's current state
	ETag       string        // RefPrecondition: the current etag
	RetryAfter time.Duration // RefUnavailable: when to try again
}

func (e *Error) Error() string { return e.Msg }

// Status is the HTTP status the refusal answers with.
func (e *Error) Status() int {
	if s, ok := refusalStatus[e.Refusal]; ok {
		return s
	}
	return http.StatusInternalServerError
}

// refuse builds a refusal.
func refuse(refusal, format string, a ...any) *Error {
	return &Error{Refusal: refusal, Msg: fmt.Sprintf(format, a...)}
}

// errorBody is the wire shape: {error, refusal, state?, etag?, retryAfterMs?}.
type errorBody struct {
	Error        string `json:"error"`
	Refusal      string `json:"refusal,omitempty"`
	State        string `json:"state,omitempty"`
	ETag         string `json:"etag,omitempty"`
	RetryAfterMs int64  `json:"retryAfterMs,omitempty"`
}

// writeErr answers err: a refusal with its status and shape; anything else
// (a disk that wouldn't take the definitions file) as a 500 with the message.
func writeErr(w http.ResponseWriter, err error) {
	var e *Error
	if !errors.As(err, &e) {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: err.Error()})
		return
	}
	body := errorBody{Error: e.Msg, Refusal: e.Refusal, State: e.State, ETag: e.ETag}
	if e.RetryAfter > 0 {
		body.RetryAfterMs = e.RetryAfter.Milliseconds()
	}
	writeJSON(w, e.Status(), body)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
