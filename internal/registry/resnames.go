package registry

import (
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
)

// Resource names (D118). A declared resource's name becomes a directory
// (data/resources-enc/<key>/<name>, .xbin/resenc/<key>/<name>, where xbind
// runs gocryptfs -init and mounts), a key-derivation label, a kv bucket
// suffix (res:<scope>/<name>) and an env var. scope.json sits in a tile's
// directory, which its terminals and coding agents write, so a name like
// "../../x" or "a/b" would steer xbind's mkdir, init and FUSE mounts outside
// data/ and .xbin/resenc, or share another scope's buckets. A name must be
// one plain path segment from a conservative set; any other is a manifest
// error and is never provisioned.

var resourceNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ResourceNameRule is the rule, as error messages say it.
const ResourceNameRule = "letters, digits, '.', '_' and '-', starting with a letter or digit, at most 64 characters"

// ValidResourceName reports whether name may name a resource.
func ValidResourceName(name string) bool { return resourceNameRe.MatchString(name) }

// invalidResourceNames lists res's invalid names, sorted.
func invalidResourceNames(res map[string]Resource) []string {
	var bad []string
	for name := range res {
		if !ValidResourceName(name) {
			bad = append(bad, name)
		}
	}
	sort.Strings(bad)
	return bad
}

// dropInvalidResources removes a scope's invalid resource names and
// records each as a refusal.
func (sm *ScopeManifest) dropInvalidResources(path string) {
	for _, name := range invalidResourceNames(sm.Resources) {
		delete(sm.Resources, name)
		sm.addErr(path, fmt.Sprintf("resource name %q is not allowed (%s) — it is not provisioned", name, ResourceNameRule))
	}
}

// validResources is res without invalid names: res itself when every name
// is valid, else a filtered copy. The workspace manifest keeps its own map
// as written, so a grants write never drops what an admin declared.
func validResources(res map[string]Resource) map[string]Resource {
	if len(invalidResourceNames(res)) == 0 {
		return res
	}
	out := make(map[string]Resource, len(res))
	for name, r := range res {
		if ValidResourceName(name) {
			out[name] = r
		}
	}
	return out
}

// warnWorkspaceResources logs the workspace manifest's invalid resource
// names when they change: there is no component to carry the error.
func (r *Registry) warnWorkspaceResources(ws WorkspaceManifest) {
	bad := strings.Join(invalidResourceNames(ws.Resources), ", ")
	if bad != "" && bad != r.wsBadRes {
		slog.Warn("workspace xbin.json: resource names not allowed, not provisioned", "names", bad, "rule", ResourceNameRule)
	}
	r.wsBadRes = bad
}
