package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOpenAPISpec(t *testing.T) {
	doc := OpenAPI()

	// Serializes cleanly.
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"openapi":"3.1.0"`) {
		t.Error("missing openapi version")
	}

	paths, _ := doc["paths"].(oapi)
	if len(paths) < 30 {
		t.Fatalf("expected the full API surface, got %d paths", len(paths))
	}

	// A few representative endpoints must be present with their capability.
	want := map[string]struct{ method, cap string }{
		"/grants":                  {"post", "admin"},
		"/vault/{component}/{key}": {"put", "self or admin"},
		"/kv/{resource}/{key}":     {"put", "writer (resource grant)"},
		"/create":                  {"post", "xbin:writer"},
		"/runtime":                 {"get", "admin"},
		"/whoami":                  {"get", "authenticated"},
		"/users":                   {"post", "xbin:users"},
	}
	for p, w := range want {
		item, ok := paths[p].(oapi)
		if !ok {
			t.Errorf("missing path %s", p)
			continue
		}
		op, ok := item[w.method].(oapi)
		if !ok {
			t.Errorf("%s missing %s", p, w.method)
			continue
		}
		if op["x-xbin-capability"] != w.cap {
			t.Errorf("%s %s capability = %v, want %q", w.method, p, op["x-xbin-capability"], w.cap)
		}
		if op["responses"] == nil || op["operationId"] == "" {
			t.Errorf("%s %s missing responses/operationId", w.method, p)
		}
	}

	// Security schemes declared.
	comps, _ := doc["components"].(oapi)
	ss, _ := comps["securitySchemes"].(oapi)
	for _, s := range []string{"bearerAuth", "cookieAuth", "frameToken"} {
		if _, ok := ss[s]; !ok {
			t.Errorf("missing security scheme %s", s)
		}
	}
}
