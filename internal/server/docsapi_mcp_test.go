package server

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"

	xbin "github.com/xbin-dev/xbin"
	"github.com/xbin-dev/xbin/internal/auth"
)

// The workspace agent's docs_search is how it learns what it doesn't know:
// asked about giving an agent a tool, it must land on docs/mcp.md first —
// the page that says the mcp-server template, the mcp slot and the binding
// (the journey's "nobody knows how to build an MCP tile"). Over the docs
// this xbind embeds, ranked as the route ranks them.
func TestDocsSearchFindsMCP(t *testing.T) {
	s := &Server{DocsFS: xbin.DocsFS()}
	for _, q := range []string{"MCP", "tool", "agent tool", "give the assistant a tool", "MCP server tile", "build an MCP server tile for an agent", "roll dice tool", "mcp-server template"} {
		r := httptest.NewRequest("GET", "/docs/search?q="+url.QueryEscape(q)+"&limit=3", nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{UserID: "alice"}))
		w := httptest.NewRecorder()
		s.apiDocsSearch(w, r)
		var out struct {
			Results []struct{ Path, Heading string }
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 {
			t.Fatalf("%q: %d %s", q, w.Code, w.Body.String())
		}
		if len(out.Results) == 0 || out.Results[0].Path != "mcp.md" {
			t.Errorf("docs_search %q: first %+v, want mcp.md", q, out.Results)
		}
	}
}
