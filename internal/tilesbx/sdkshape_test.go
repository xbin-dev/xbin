package tilesbx

import (
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// The SDK (sdk/sandbox*.go) decodes what these routes answer and sends what
// they take. Every field the runtime answers must have a field in the SDK's
// type — a missing one is silently dropped, and a manager that re-encodes
// the SDK's value (examples/sandbox-go's GET /runtime) loses it — and every
// field the SDK sends must be one the runtime reads (it ignores unknown
// fields, so a misspelt one would do nothing). WP-21's live run found
// runtime.limits.flows missing from the SDK.
func TestSDKShapes(t *testing.T) {
	for _, c := range []struct {
		name          string
		runtime, sdk  any
		sdkExtra      []string // fields the SDK has that this answer lacks (another xbind's, or a request's own)
		runtimeExtra  []string // fields the runtime has that the SDK leaves out, and why that's fine
		answer, sends bool
	}{
		{name: "runtime", runtime: Runtime{}, sdk: xbin.SandboxRuntime{}, answer: true},
		{name: "sandbox", runtime: Info{}, sdk: xbin.SandboxInfo{}, answer: true},
		{name: "exec", runtime: Exec{}, sdk: xbin.ExecInfo{}, answer: true},
		{name: "output", runtime: OutputChunk{}, sdk: xbin.OutputChunk{}, answer: true},
		{name: "snapshot", runtime: SnapshotInfo{}, sdk: xbin.Snapshot{}, answer: true},
		{name: "run result", runtime: RunResult{}, sdk: xbin.RunResult{}, answer: true},
		{name: "create", runtime: CreateRequest{}, sdk: xbin.SandboxSpec{}, sends: true},
		{name: "patch", runtime: PatchRequest{}, sdk: xbin.SandboxPatch{}, sends: true},
		{name: "run", runtime: RunRequest{}, sdk: xbin.RunRequest{}, sends: true},
		{name: "exec request", runtime: ExecRequest{}, sdk: xbin.ExecRequest{}, sends: true},
	} {
		rt, sdk := jsonFields(reflect.TypeOf(c.runtime)), jsonFields(reflect.TypeOf(c.sdk))
		if c.answer {
			if miss := minus(rt, sdk, c.runtimeExtra); len(miss) > 0 {
				t.Errorf("%s: the runtime answers %v, which the SDK's %T drops", c.name, miss, c.sdk)
			}
		}
		if c.sends {
			if miss := minus(sdk, rt, c.sdkExtra); len(miss) > 0 {
				t.Errorf("%s: the SDK's %T sends %v, which the runtime doesn't read", c.name, c.sdk, miss)
			}
		}
		if extra := minus(sdk, rt, c.sdkExtra); c.answer && len(extra) > 0 {
			t.Errorf("%s: the SDK's %T has %v, which the runtime never answers", c.name, c.sdk, extra)
		}
	}
}

// jsonFields is every JSON field path of t ("limits.flows.tcp"), into
// structs, pointers, slices and maps' elements.
func jsonFields(t reflect.Type) map[string]bool {
	out := map[string]bool{}
	var walk func(t reflect.Type, prefix string, depth int)
	walk = func(t reflect.Type, prefix string, depth int) {
		for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Map {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct || depth > 6 {
			return
		}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			if f.Anonymous && name == "" {
				walk(f.Type, prefix, depth+1)
				continue
			}
			if name == "" {
				name = f.Name
			}
			out[prefix+name] = true
			walk(f.Type, prefix+name+".", depth+1)
		}
	}
	walk(t, "", 0)
	return out
}

// minus is a's fields not in b nor in except, sorted.
func minus(a, b map[string]bool, except []string) []string {
	var out []string
	for f := range a {
		if !b[f] && !slices.Contains(except, f) {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}
