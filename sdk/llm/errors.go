package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Error types, as OpenAI's API names them. Clients key on the HTTP status
// first; the type and code say which refusal it was.
const (
	ErrTypeInvalidRequest = "invalid_request_error"
	ErrTypeAuthentication = "authentication_error"
	ErrTypePermission     = "permission_error"
	ErrTypeNotFound       = "not_found_error"
	ErrTypeRateLimit      = "rate_limit_error"
	ErrTypeQuota          = "insufficient_quota"
	ErrTypeAPI            = "api_error"
	ErrTypeUnavailable    = "service_unavailable_error"
)

// Error is the error object of an OpenAI-shaped error body,
// {"error": {message, type, param, code}}. Extra carries members beyond
// those four, written beside them (a quota refusal's "resets").
type Error struct {
	Message string         `json:"message"`
	Type    string         `json:"type"`
	Param   *string        `json:"param"`
	Code    any            `json:"code"` // a string, a number or null
	Extra   map[string]any `json:"-"`
}

func (e *Error) Error() string {
	if e.Code != nil && e.Code != "" {
		return fmt.Sprintf("%s (%v)", e.Message, e.Code)
	}
	return e.Message
}

// MarshalJSON writes the four standard members, then Extra's.
func (e Error) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, 4+len(e.Extra))
	for k, v := range e.Extra {
		m[k] = v
	}
	m["message"], m["type"], m["param"], m["code"] = e.Message, e.Type, e.Param, e.Code
	return json.Marshal(m)
}

// UnmarshalJSON reads an error as providers send it: the object, or — from
// servers that are only roughly compatible — a bare string.
func (e *Error) UnmarshalJSON(b []byte) error {
	*e = Error{}
	if len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, &e.Message)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	for k, v := range m {
		switch k {
		case "message":
			e.Message, _ = v.(string)
		case "type":
			e.Type, _ = v.(string)
		case "param":
			if s, ok := v.(string); ok {
				e.Param = &s
			}
		case "code":
			e.Code = v
		default:
			if e.Extra == nil {
				e.Extra = map[string]any{}
			}
			e.Extra[k] = v
		}
	}
	return nil
}

// ErrorBody is a whole error answer.
type ErrorBody struct {
	Error Error `json:"error"`
}

// WriteError answers status with e as an OpenAI-shaped error body, so
// OpenAI SDKs and CLIs show its message.
func WriteError(w http.ResponseWriter, status int, e Error) {
	if e.Type == "" {
		e.Type = TypeForStatus(status)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorBody{Error: e})
}

// TypeForStatus is the error type OpenAI uses for an HTTP status.
func TypeForStatus(status int) string {
	switch {
	case status == http.StatusUnauthorized:
		return ErrTypeAuthentication
	case status == http.StatusForbidden:
		return ErrTypePermission
	case status == http.StatusNotFound:
		return ErrTypeNotFound
	case status == http.StatusTooManyRequests:
		return ErrTypeRateLimit
	case status == http.StatusServiceUnavailable:
		return ErrTypeUnavailable
	case status >= 500:
		return ErrTypeAPI
	}
	return ErrTypeInvalidRequest
}

// ParseError reads the error out of an error body; ok is false when the
// body has none (a proxy's HTML page, an empty answer).
func ParseError(body []byte) (e Error, ok bool) {
	var v struct {
		Error *Error `json:"error"`
	}
	if json.Unmarshal(body, &v) != nil || v.Error == nil || (v.Error.Message == "" && v.Error.Code == nil) {
		return Error{}, false
	}
	return *v.Error, true
}
