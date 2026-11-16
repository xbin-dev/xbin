package server

import (
	"net/http"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	sdkllm "github.com/xbin-dev/xbin/sdk/llm"
)

// llmkey.go — the gate for LLM keys (D226, docs/auth.md "LLM keys"). An LLM
// key is a narrow credential the LLM connector (internal/llm) hands a
// terminal, an agent session or a sandbox: it is good for the connector's
// OpenAI-compatible routes and for nothing else, so a coding CLI that
// keeps or leaks its key gives away no access to the workspace.
//
// The key rides Authorization: Bearer, where OpenAI clients put theirs. It
// is recognised by its prefix before any other credential is looked at:
//
//   - on /api/xbin/llm/v1/…, a live key runs the request as a marker
//     principal (Via "llm") — the connector reads who the key stands for
//     from the key itself — and a dead one is a 401 in OpenAI's error shape;
//   - anywhere else it is a 401, whatever other credential the request
//     carries: Via "llm" exists on no other route.

// llmPlane is the path prefix LLM keys are good for.
const llmPlane = "/api/xbin/llm/v1/"

// llmKeyPrefix starts every LLM key (internal/llm's KeyPrefix).
const llmKeyPrefix = "xbl_"

// llmBearer is the LLM key r presents, if any.
func llmBearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if len(h) < 7 || !strings.EqualFold(h[:7], "Bearer ") {
		return "", false
	}
	tok := strings.TrimSpace(h[7:])
	return tok, strings.HasPrefix(tok, llmKeyPrefix)
}

// onLLMPlane reports a request for the connector's OpenAI-compatible
// routes, by its decoded and its escaped path alike.
func onLLMPlane(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, llmPlane) && strings.HasPrefix(r.URL.EscapedPath(), llmPlane)
}

// llmKeyGate answers a request that presents an LLM key (handled true), or
// leaves any other request alone.
func (s *Server) llmKeyGate(w http.ResponseWriter, r *http.Request, next http.Handler) (handled bool) {
	tok, ok := llmBearer(r)
	if !ok {
		return false
	}
	switch {
	case !onLLMPlane(r):
		WriteError(w, http.StatusUnauthorized, "an LLM key is good for the LLM connector's routes ("+llmPlane+"…) only", "/docs/auth.md")
	case auth.TransportOf(r).Tunnel:
		writeLLMUnauthorized(w, "an LLM key is not used over the app tunnel")
	case s.LLMKey == nil:
		writeLLMUnauthorized(w, "this LLM key is not valid")
	default:
		p, live := s.LLMKey(tok)
		if !live {
			writeLLMUnauthorized(w, "this LLM key is not valid (it expired, was revoked, or its session ended)")
			break
		}
		p.Via, p.Component, p.Owner, p.User, p.Access = "llm", "", false, nil, nil // a marker, never an identity another route could act on
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
	}
	return true
}

// writeLLMUnauthorized is a 401 in OpenAI's error shape, which OpenAI SDKs
// and coding CLIs show.
func writeLLMUnauthorized(w http.ResponseWriter, msg string) {
	sdkllm.WriteError(w, http.StatusUnauthorized, sdkllm.Error{Message: msg, Code: "invalid_api_key"})
}
