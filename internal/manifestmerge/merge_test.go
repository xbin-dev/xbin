package manifestmerge

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/jsonc"
)

// The agent template's manifest before B2a (master's builtin-templates/agent
// at bf5a56b1 — the same file at 7b54e7fb), and as it ships now.
func agentManifests(t *testing.T) (old, cur []byte) {
	t.Helper()
	old, err := os.ReadFile(filepath.Join("testdata", "agent-bf5a56b1.json"))
	if err != nil {
		t.Fatal(err)
	}
	cur, err = os.ReadFile(filepath.Join("..", "..", "builtin-templates", "agent", "xbin.json"))
	if err != nil {
		t.Fatal(err)
	}
	return old, cur
}

// marshalled is a template manifest as xbinds before T1 instantiated it
// (builtins.stripTemplateBlock at bf5a56b1, byte for byte): the block
// deleted and the rest re-marshalled — no comments, keys sorted.
func marshalled(t *testing.T, tpl []byte, rename ...string) []byte {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(jsonc.Strip(tpl), &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "template")
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	out = append(out, '\n')
	if len(rename) == 2 {
		out = []byte(strings.ReplaceAll(string(out), rename[0], rename[1]))
	}
	return out
}

// served is cur as the served template repo carries it when an older xbind
// wrote the repo: its "template" block the one it had (templaterepo_block.go).
func served(t *testing.T, cur, old []byte) []byte {
	t.Helper()
	blk, _, err := jsonc.TopLevel(old, "template")
	if err != nil {
		t.Fatal(err)
	}
	out, err := jsonc.SetTopLevel(cur, "template", blk)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func mustMerge(t *testing.T, base, ours, theirs []byte, o Options) Result {
	t.Helper()
	r, err := Merge(base, ours, theirs, o)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !json.Valid(jsonc.Strip(r.Out)) {
		t.Fatalf("the merged manifest doesn't parse:\n%s", r.Out)
	}
	return r
}

func top(t *testing.T, doc []byte, key string) (string, bool) {
	t.Helper()
	raw, has, err := jsonc.TopLevel(doc, key)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	_ = json.Unmarshal(jsonc.Strip(raw), &v)
	b, _ := json.Marshal(v)
	return string(b), has
}

// covers T1 — an instance an older xbind made from the pre-B2a agent
// template (its xbin.json re-marshalled) takes today's manifest by keys: an
// unedited one merges to exactly what instantiation would write today the
// old way — B2a's partitionMail, partitionNote and the conf/team uses in,
// no partition, no template block — whether the served repo kept the old
// block (an older xbind's repo) or upstream's block is new; a renamed one
// (apps/my-agent) the same with its own path.
func TestOldInstanceTakesTemplateManifest(t *testing.T) {
	old, cur := agentManifests(t)
	ours := marshalled(t, old)
	want := marshalled(t, cur)
	for name, theirs := range map[string][]byte{"repo keeps the old block": served(t, cur, old), "upstream's block": cur} {
		t.Run(name, func(t *testing.T) {
			r := mustMerge(t, old, ours, theirs, Options{From: "apps/agent", To: "apps/agent"})
			if string(r.Out) != string(want) {
				t.Fatalf("merged:\n%s\nwant (today's template instantiated the old way):\n%s", r.Out, want)
			}
			if _, has := top(t, r.Out, "partition"); has {
				t.Errorf("the merge made the instance partitioned")
			}
			if got := strings.Join(r.Took, " "); got != "uses partitionMail partitionNote" {
				t.Errorf("took %q", got)
			}
		})
	}
	t.Run("renamed", func(t *testing.T) {
		ours := marshalled(t, old, "apps/agent", "apps/my-agent")
		r := mustMerge(t, old, ours, served(t, cur, old), Options{From: "apps/agent", To: "apps/my-agent"})
		if want := marshalled(t, cur, "apps/agent", "apps/my-agent"); string(r.Out) != string(want) {
			t.Fatalf("merged:\n%s\nwant:\n%s", r.Out, want)
		}
		if strings.Contains(string(r.Out), "res:apps/agent/") {
			t.Errorf("upstream's own path reached the instance:\n%s", r.Out)
		}
	})
}

// covers T1 — an old instance its builder built up: their grants, their
// interface and their key stay, upstream's new uses land after the entry
// they follow upstream, in the instance's form; a value both changed is a
// conflict naming it.
func TestOldInstanceEditedByBuilder(t *testing.T) {
	old, cur := agentManifests(t)
	ours := string(marshalled(t, old))
	edit := func(doc, from, to string) string {
		t.Helper()
		if !strings.Contains(doc, from) {
			t.Fatalf("no %q in:\n%s", from, doc)
		}
		return strings.Replace(doc, from, to, 1)
	}
	// a grant of their own at the end of uses, an interface of their own, a title
	ours = edit(ours, "      \"target\": \"cap:open-links\",\n      \"role\": \"writer\"\n    }\n",
		"      \"target\": \"cap:open-links\",\n      \"role\": \"writer\"\n    },\n    {\n      \"target\": \"res:apps/crm/db\",\n      \"role\": \"reader\"\n    }\n")
	ours = edit(ours, "    \"net\": {\n      \"kind\": \"net\"\n    }\n", "    \"net\": {\n      \"kind\": \"net\"\n    },\n    \"crm\": {\n      \"kind\": \"http\",\n      \"service\": \"crm\"\n    }\n")
	ours = edit(ours, "  \"runtime\": \"go\",\n", "  \"runtime\": \"go\",\n  \"title\": \"Sales bot\",\n")
	r := mustMerge(t, old, []byte(ours), served(t, cur, old), Options{})
	out := string(r.Out)
	for _, s := range []string{`"res:apps/crm/db"`, `"service": "crm"`, `"title": "Sales bot"`, `"partitionMail": "/mailbox"`} {
		if !strings.Contains(out, s) {
			t.Errorf("merged lacks %s:\n%s", s, out)
		}
	}
	files, conf, team, crm := strings.Index(out, "apps/agent/files"), strings.Index(out, "apps/agent/conf"), strings.Index(out, "apps/agent/team"), strings.Index(out, "apps/crm/db")
	if !(files < conf && conf < team && team < crm) {
		t.Errorf("uses order: files %d conf %d team %d crm %d:\n%s", files, conf, team, crm, out)
	}
	if !strings.Contains(out, "    {\n      \"target\": \"res:apps/agent/conf\",\n      \"role\": \"writer\"\n    },\n") {
		t.Errorf("the new uses entries aren't in the instance's form:\n%s", out)
	}
	if !normalized(r.Out) {
		t.Errorf("the merge left the two-space form:\n%s", out)
	}

	// both change the entry: a conflict, named
	both := edit(ours, `"entry": "./_backend"`, `"entry": "./backend"`)
	theirs := edit(string(cur), `"entry": "./_backend"`, `"entry": "./cmd/agent"`)
	_, err := Merge(old, []byte(both), []byte(theirs), Options{})
	var ce *ConflictError
	if !errors.As(err, &ce) || strings.Join(ce.Paths, ",") != "entry" {
		t.Fatalf("both changed entry: %v", err)
	}
}

// covers T1 — an instance written the way instantiation writes now (the
// template's JSONC, the block gone, its own partition on top): upstream's
// new key comes with its comment, after the key it follows upstream, an
// entry upstream dropped goes with the comment above it, commas stay right,
// the instance's partition stays — and a partition or template block
// upstream changes never arrives.
func TestKeepsCommentsAndPartition(t *testing.T) {
	base := `{
  // header
  "runtime": "go",
  "uses": [
    { "target": "res:apps/agent/db", "role": "writer" },
    // the last one
    { "target": "cap:open-links", "role": "writer" }
  ],
  "template": { "partition": ["user"] },
  "interfaces": {
    "llm": { "kind": "http", "service": "openai" }
  }
}
`
	ours := strings.Replace(strings.Replace(base, "{\n  // header", "{\n  \"partition\": [\"user\", \"global\"],\n  // header", 1),
		"  \"template\": { \"partition\": [\"user\"] },\n", "", 1)
	ours = strings.Replace(ours, `"service": "openai" }`, `"service": "openai", "multi": true }`, 1) // theirs: the builder's own edit
	theirs := strings.Replace(base, "    // the last one\n    { \"target\": \"cap:open-links\", \"role\": \"writer\" }\n", "", 1)
	theirs = strings.Replace(theirs, `"role": "writer" },`, `"role": "writer" }`, 1)
	theirs = strings.Replace(theirs, `"template": { "partition": ["user"] },`, `"template": { "partition": ["global"] },
  // Where the doorbell rings.
  "partitionMail": "/mailbox",`, 1)
	r := mustMerge(t, []byte(base), []byte(ours), []byte(theirs), Options{})
	want := `{
  "partition": ["user", "global"],
  // header
  "runtime": "go",
  "uses": [
    { "target": "res:apps/agent/db", "role": "writer" }
  ],
  // Where the doorbell rings.
  "partitionMail": "/mailbox",
  "interfaces": {
    "llm": { "kind": "http", "service": "openai", "multi": true }
  }
}
`
	if string(r.Out) != want {
		t.Fatalf("merged:\n%s\nwant:\n%s", r.Out, want)
	}
	if _, has := top(t, r.Out, "template"); has {
		t.Errorf("the template block arrived")
	}
}

// covers T1 — trailing commas stay as the instance writes them, a key both
// sides added the same is no conflict, and a manifest that doesn't parse is
// an error (the driver then leaves git's own merge).
func TestMergeEdges(t *testing.T) {
	base := "{\n  \"a\": 1,\n  \"list\": [\n    \"x\",\n  ],\n}\n"
	ours := "{\n  \"a\": 1,\n  \"b\": 2,\n  \"list\": [\n    \"x\",\n    \"mine\",\n  ],\n}\n"
	theirs := "{\n  \"a\": 1,\n  \"b\": 2,\n  \"list\": [\n    \"x\",\n    \"up\",\n  ],\n  \"c\": 3,\n}\n"
	r := mustMerge(t, []byte(base), []byte(ours), []byte(theirs), Options{})
	want := "{\n  \"a\": 1,\n  \"b\": 2,\n  \"list\": [\n    \"x\",\n    \"up\",\n    \"mine\",\n  ],\n  \"c\": 3,\n}\n"
	if string(r.Out) != want {
		t.Fatalf("merged:\n%s\nwant:\n%s", r.Out, want)
	}
	if _, err := Merge([]byte(""), []byte(ours), []byte(theirs), Options{}); err == nil {
		t.Errorf("an empty base (an add/add merge) merged")
	}
	if _, err := Merge([]byte(base), []byte("<<<<<<< ours\n{}"), []byte(theirs), Options{}); err == nil {
		t.Errorf("a manifest with conflict markers merged")
	}
	// an upstream change the instance already has: nothing to do
	r = mustMerge(t, []byte(base), []byte(theirs), []byte(theirs), Options{})
	if string(r.Out) != theirs || len(r.Took) != 0 {
		t.Errorf("merging what ours has changed it: %q took %v", r.Out, r.Took)
	}
	// an element of a one-line list both sides changed: a conflict, not a guess
	_, err := Merge([]byte(`{"l": [1, 2]}`), []byte(`{"l": [1, 2, 3]}`), []byte(`{"l": [1]}`), Options{})
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Errorf("a one-line list both changed: %v", err)
	}
}
