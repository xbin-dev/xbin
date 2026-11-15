package llm

import (
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
)

// A preview's render dials nowhere (D234): the model list, which asks every
// passthrough upstream, is refused to it before any upstream is asked.
func TestModelListRefusedInAPreview(t *testing.T) {
	r := newRig(t, "")
	up := newUpstream(t)
	r.addUpstream("open", up, nil)
	ada := auth.Principal{UserID: "ada", User: &users.User{ID: "ada", Role: users.RoleAdmin}, Via: "session"}
	preview := ada
	preview.Impersonator = auth.PreviewViewer
	if a := r.do(preview, "GET", "/llm/v1/models", nil); a.code != 403 || !strings.Contains(string(a.body), "a preview doesn't ask") {
		t.Fatalf("a preview's read: %d %s", a.code, a.body)
	}
	if up.modelsN != 0 {
		t.Fatalf("a preview's read asked the upstream %d time(s)", up.modelsN)
	}
	r.must(200, ada, "GET", "/llm/v1/models", nil)
	if up.modelsN != 1 {
		t.Fatalf("the person's own read: asked %d time(s)", up.modelsN)
	}
}
