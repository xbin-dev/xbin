package registry

import (
	"encoding/json"
	"path"
	"strings"
)

// RedactedManifestJSON renders the workspace manifest a RESTRICTED terminal
// may see (D40): schema + importMap, plus only the grants and binding slots
// whose every referenced component is readable to the principal. The full
// xbin.json is the workspace's entire topology — grant edges, binding wiring,
// public hostnames — which a user who can read one tile has no business
// holding (the leak that motivated the allow-list terminal view).
//
// Reference extraction is deliberately conservative: `res:<scope>/<name>`
// scopes use the path's parent dir (the broker's longest-declared-prefix
// resolution isn't available here; over-hiding a row is safe, leaking one is
// not), capability classes (xbin, gpu:*, cap:*, net:*…) name no component,
// and builtin binding refs (internet[:…], host, lan:…, runtime) pass as-is.
func RedactedManifestJSON(ws WorkspaceManifest, readable func(path string) bool) []byte {
	refOK := func(t string) bool {
		switch {
		case t == "":
			return true
		case strings.HasPrefix(t, "res:"):
			scope := path.Dir(strings.TrimPrefix(t, "res:"))
			if scope == "workspace" || scope == "." {
				return true // workspace-plane resource: no component named
			}
			return readable(scope)
		case strings.HasPrefix(t, "code:"):
			return readable(strings.TrimPrefix(t, "code:"))
		case strings.Contains(t, ":"):
			return true // capability class — no component path in the target
		default:
			return readable(t)
		}
	}
	providerOK := func(ref string) bool {
		if i := strings.IndexByte(ref, '#'); i >= 0 {
			ref = ref[:i]
		}
		switch {
		case ref == "" || ref == "runtime" || ref == "internet" || ref == "host":
			return true
		case strings.HasPrefix(ref, "lan:") || strings.HasPrefix(ref, "internet:"):
			return true
		default:
			return readable(ref)
		}
	}

	out := struct {
		Schema    int                           `json:"schema"`
		ImportMap map[string]string             `json:"importMap,omitempty"`
		Grants    []Grant                       `json:"grants,omitempty"`
		Bindings  map[string]map[string]Binding `json:"bindings,omitempty"`
	}{Schema: ws.Schema, ImportMap: ws.ImportMap}

	for _, g := range ws.Grants {
		if readable(g.From) && refOK(g.Target) {
			out.Grants = append(out.Grants, g)
		}
	}
	for comp, slots := range ws.Bindings {
		if !readable(comp) {
			continue
		}
		kept := map[string]Binding{}
		for slot, binding := range slots {
			ok := true
			for _, ref := range binding {
				if !providerOK(ref.Ref) {
					ok = false
					break
				}
			}
			if ok {
				kept[slot] = binding
			}
		}
		if len(kept) > 0 {
			if out.Bindings == nil {
				out.Bindings = map[string]map[string]Binding{}
			}
			out.Bindings[comp] = kept
		}
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return []byte(`{"schema":1}`)
	}
	return append(b, '\n')
}
