package xbin

import (
	"net/http/httptest"
	"testing"
)

func TestWriteJSONAndError(t *testing.T) {
	w := httptest.NewRecorder()
	WriteJSON(w, 201, map[string]any{"n": 1})
	if w.Code != 201 || w.Header().Get("Content-Type") != "application/json" || w.Body.String() != "{\"n\":1}\n" {
		t.Errorf("WriteJSON: %d %q %q", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
	w = httptest.NewRecorder()
	WriteError(w, 403, "nope")
	if w.Code != 403 || w.Body.String() != "{\"error\":\"nope\"}\n" {
		t.Errorf("WriteError: %d %q", w.Code, w.Body.String())
	}
}
