package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An xbind older than partitioned tiles refuses the "partition" field with
// its old 400 ("need {source, path?, owner?}"): bx says why and what to do.
func TestBxTemplateNewOlderXbind(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"need {source, path?, owner?}"}`))
	}))
	defer srv.Close()
	t.Setenv("XBIN_URL", srv.URL)
	t.Setenv("XBIN_TOKEN", "t")
	err := cmdTemplate([]string{"new", "agent", "--no-partition"})
	if err == nil || !strings.Contains(err.Error(), "predates partitioned tiles") || !strings.Contains(err.Error(), "without --no-partition") {
		t.Fatalf("err = %v", err)
	}
	// Without the flag the error is xbind's own.
	if err := cmdTemplate([]string{"new", "agent"}); err == nil || strings.Contains(err.Error(), "predates") {
		t.Fatalf("err = %v", err)
	}
}
