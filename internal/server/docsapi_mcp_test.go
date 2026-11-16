package server

import (
	"encoding/json"
	"io/fs"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	xbin "github.com/xbin-dev/xbin"
	"github.com/xbin-dev/xbin/internal/auth"
)

// The workspace agent's docs_search is how it learns what it doesn't know:
// asked about giving an agent a tool, it must land on docs/mcp.md first —
// the page that carries an MCP server tile's interface, the mcp slot and the
// binding (the journey's "nobody knows how to build an MCP tile"). Over the docs
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

// docs/mcp.md carries a complete MCP server inline — the file a tile's
// coding agent starts from. It IS examples/mcp-server/backend/main.go,
// which is compiled, tested and run live (TestMCPExampleLive): the page
// can't drift from a server that works. The manifest lines it shows are the
// example's too.
func TestDocsMCPServerIsTheExample(t *testing.T) {
	page, err := fs.ReadFile(xbin.DocsFS(), "mcp.md")
	if err != nil {
		t.Fatal(err)
	}
	const mark = "<!-- examples/mcp-server/backend/main.go:"
	_, rest, ok := strings.Cut(string(page), mark)
	_, rest, ok2 := strings.Cut(rest, "-->\n```go\n")
	code, _, ok3 := strings.Cut(rest, "\n```\n")
	if !ok || !ok2 || !ok3 {
		t.Fatal("docs/mcp.md has no marked go block for the example's main.go")
	}
	want, err := os.ReadFile("../../examples/mcp-server/backend/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if code+"\n" != string(want) {
		t.Errorf("the server in docs/mcp.md is not examples/mcp-server/backend/main.go: copy the file into the page's block (they must be the same bytes)")
	}
	manifest, err := os.ReadFile("../../examples/mcp-server/xbin.json")
	if err != nil {
		t.Fatal(err)
	}
	const provide = `"provides": { "mcp": { "kind": "http", "service": "mcp", "role": "reader" } }`
	flat := strings.Join(strings.Fields(string(manifest)), " ")
	if !strings.Contains(strings.Join(strings.Fields(string(page)), " "), provide) || !strings.Contains(flat, strings.Join(strings.Fields(provide), " ")) {
		t.Errorf("docs/mcp.md and the example's manifest don't both declare %s", provide)
	}
}
