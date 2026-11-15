package main

import (
	"strings"
	"testing"
)

// admin_changes tells a conversation the change sets it proposed itself:
// xbind lists every set the person's agent proposed, and another
// session's lines are data from what that one read.
func TestWsAdminChangesAreTheConversations(t *testing.T) {
	fx := newWsFix(t)
	fx.x.mu.Lock()
	fx.x.state.CanAdminChanges, fx.x.state.AdminChanges = true, true
	fx.x.mu.Unlock()
	main := fx.main(t)
	books := fx.session(t, map[string]any{"name": "Books", "model": "local-llama"})
	propose := func(s *wsSession, title string) {
		t.Helper()
		if got := fx.tool(t, s, "admin_propose", map[string]any{"title": title, "changes": []any{map[string]any{"kind": "grant.add", "args": map[string]any{}, "why": "it needs it"}}}); !strings.Contains(got, "Proposed; nothing is applied") {
			t.Fatalf("proposing %q: %q", title, got)
		}
	}
	propose(books, "Give the ledger to Payroll")
	if got := fx.tool(t, main, "admin_changes", nil); strings.Contains(got, "ledger") || !strings.Contains(got, "This conversation has proposed no admin changes") {
		t.Fatalf("Main is told another session's change set: %q", got)
	}
	propose(main, "Share Orders with Sales")
	got := fx.tool(t, main, "admin_changes", nil)
	if !strings.Contains(got, "xbin's line for grant.add") || strings.Count(got, "1. [grant.add]") != 1 {
		t.Fatalf("Main's own change set: %q", got)
	}
	if got := fx.tool(t, books, "admin_changes", nil); strings.Count(got, "1. [grant.add]") != 1 {
		t.Fatalf("Books' own change set: %q", got)
	}
}
