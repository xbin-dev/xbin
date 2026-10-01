package manifestmerge

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func mustConflict(t *testing.T, base, ours, theirs string, o Options, want string) {
	t.Helper()
	r, err := Merge([]byte(base), []byte(ours), []byte(theirs), o)
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("merged (%v), want a conflict about %q:\n%s", err, want, r.Out)
	}
	if !strings.Contains(ce.Error(), want) {
		t.Fatalf("conflict %q, want one about %q", ce.Error(), want)
	}
}

// covers T1 (review) D177 — upstream's change to a top-level partition or
// template is a conflict, never taken and never dropped silently (a merge
// in the other direction — a builder's branch, a rebase — would otherwise
// lose the builder's partition); upstream's change to a block ours doesn't
// carry, and one ours already has, merge.
func TestKeptKeysConflict(t *testing.T) {
	base := "{\n  \"runtime\": \"go\"\n}\n"
	withPartition := "{\n  \"partition\": [\"user\"],\n  \"runtime\": \"go\"\n}\n"
	withBlock := "{\n  \"template\": { \"defaultName\": \"x\" },\n  \"runtime\": \"go\"\n}\n"
	// D177: upstream adding a mode where neither the base nor ours names one
	// is taken (a template whose update requests its instances' partition);
	// once ours names one, in any case, upstream's add is a conflict again
	if r := mustMerge(t, []byte(base), []byte(base), []byte(withPartition), Options{}); strings.Join(r.Took, " ") != "partition" {
		t.Errorf("an added partition: merged %q, took %q", r.Out, r.Took)
	} else if got, has := top(t, r.Out, "partition"); !has || got != `["user"]` {
		t.Errorf("an added partition: merged %q", r.Out)
	}
	mustConflict(t, base, strings.Replace(withPartition, `"partition": ["user"]`, `"Partition": ["global"]`, 1), withPartition, Options{}, "partition")
	// ours took the mode out after the base had it (the builder's escape
	// hatch): upstream asking for it unchanged is nothing to ours
	if r := mustMerge(t, []byte(withPartition), []byte(base), []byte(withPartition), Options{}); string(r.Out) != base {
		t.Errorf("a removed partition: merged %q", r.Out)
	}
	mustConflict(t, withPartition, withPartition, base, Options{}, "partition")                                                    // upstream drops it
	mustConflict(t, withPartition, withPartition, strings.Replace(withPartition, `"user"`, `"global"`, 1), Options{}, "partition") // upstream changes it
	mustConflict(t, base, base, withBlock, Options{}, "template")
	// the same change on both sides: nothing to take
	if r := mustMerge(t, []byte(base), []byte(withPartition), []byte(withPartition), Options{}); string(r.Out) != withPartition {
		t.Errorf("merged %q", r.Out)
	}
	// a block ours dropped (an instance): upstream's change to it is nothing to ours
	changed := strings.Replace(withBlock, `"x"`, `"y"`, 1)
	if r := mustMerge(t, []byte(withBlock), []byte(base), []byte(changed), Options{}); string(r.Out) != base {
		t.Errorf("merged %q", r.Out)
	}
}

// covers T1 (review) — the own-path rename: applied for an instance path
// the shell must quote (~, a space), and a conflict — never upstream's
// entries verbatim — when ours doesn't name the path the driver renames to
// (a copy of an instance whose driver names its source's path), whether the
// driver named a rename or the template's own path.
func TestOwnPathRename(t *testing.T) {
	old, cur := agentManifests(t)
	for _, to := range []string{"apps/sales agent", "apps/a~b"} {
		ours := marshalled(t, old, "apps/agent", to)
		r := mustMerge(t, old, ours, served(t, cur, old), Options{From: "apps/agent", To: to})
		if want := marshalled(t, cur, "apps/agent", to); string(r.Out) != string(want) {
			t.Fatalf("%s merged:\n%s\nwant:\n%s", to, r.Out, want)
		}
	}
	// a copy at apps/agent2 of an instance at apps/agent, and of one at apps/my-agent
	copied := string(marshalled(t, old, "apps/agent", "apps/agent2"))
	for _, o := range []Options{{From: "apps/agent", To: "apps/agent"}, {From: "apps/agent", To: "apps/my-agent"}} {
		mustConflict(t, string(old), copied, string(served(t, cur, old)), o, "doesn't name")
	}
	// upstream adding no reference to its own path: nothing to rename
	edited := strings.Replace(string(old), `"runtime": "go",`, `"runtime": "go",`+"\n  \"title\": \"Agent\",", 1)
	r := mustMerge(t, old, []byte(copied), []byte(edited), Options{From: "apps/agent", To: "apps/agent"})
	if !strings.Contains(string(r.Out), `"title": "Agent"`) || strings.Contains(string(r.Out), "res:apps/agent/") {
		t.Errorf("merged:\n%s", r.Out)
	}
	if !namesPath([]byte(`"res:apps/x/db"`), "apps/x") || namesPath([]byte(`"res:apps/x2/db"`), "apps/x") || namesPath([]byte(`"apps/x-y"`), "apps/x") {
		t.Errorf("namesPath's word boundaries")
	}
}

// covers T1 (review) — the driver's command carries the rename as one shell
// word through git's placeholder expansion (%% is a %) and sh, whatever the
// path holds.
func TestDriverQuotesTheRename(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	for _, to := range []string{"apps/agent", "apps/sales agent", "apps/o'brien", "apps/100%O", "apps/a~b;$(x)`y`"} {
		cmd := strings.ReplaceAll(Driver("apps/agent", to), "%%", "%") // git's expansion of what isn't a placeholder
		out, err := exec.Command("sh", "-c", `bx() { for a; do printf '%s\n' "$a"; done; }; `+cmd).Output()
		if err != nil {
			t.Fatalf("%s: %v", to, err)
		}
		args := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
		if len(args) < 5 || args[4] != "--rename" || args[5] != "apps/agent="+to {
			t.Errorf("%s: bx got %q", to, args)
		}
	}
}

// covers T1 (review) — a manifest that carries comments takes upstream's
// change to them or conflicts: a reworded comment next to a value upstream
// changed is a conflict (not ours' old comment kept silently); a change of
// values only merges; a manifest without comments (the two-space form)
// takes a comment-only change as nothing — ours as it was, nothing taken.
func TestCommentChanges(t *testing.T) {
	base := "{\n  // the entry\n  \"entry\": \"./x\",\n  \"runtime\": \"go\"\n}\n"
	ours := strings.Replace(base, "{\n", "{\n  \"partition\": [\"user\"],\n", 1)
	reworded := strings.Replace(strings.Replace(base, "// the entry", "// where it starts", 1), `"go"`, `"static"`, 1)
	mustConflict(t, base, ours, reworded, Options{}, "comments")
	valuesOnly := strings.Replace(base, `"go"`, `"static"`, 1)
	if r := mustMerge(t, []byte(base), []byte(ours), []byte(valuesOnly), Options{}); !strings.Contains(string(r.Out), `"static"`) || !strings.Contains(string(r.Out), "// the entry") {
		t.Errorf("merged:\n%s", r.Out)
	}
	norm := "{\n  \"entry\": \"./x\",\n  \"runtime\": \"go\"\n}\n"
	r := mustMerge(t, []byte(base), []byte(norm), []byte(strings.Replace(base, "// the entry", "// where it starts", 1)), Options{})
	if string(r.Out) != norm || len(r.Took) != 0 {
		t.Errorf("merged %q, took %v", r.Out, r.Took)
	}
	// a member ours dropped with the comment above it (an instance's
	// template block): upstream rewording that comment is nothing to ours
	withBlock := strings.Replace(base, "{\n", "{\n  // A TEMPLATE\n  \"template\": { \"defaultName\": \"x\" },\n", 1)
	r = mustMerge(t, []byte(withBlock), []byte(ours), []byte(strings.Replace(withBlock, "// A TEMPLATE", "// A TEMPLATE, reworded", 1)), Options{})
	if string(r.Out) != ours {
		t.Errorf("merged %q", r.Out)
	}
}
