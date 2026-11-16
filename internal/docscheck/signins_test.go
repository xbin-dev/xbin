package docscheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// covers D230 (review 3c, 5) — the sign-in docs say what masking and the
// rule are and are not: a reader must not take redaction for a boundary,
// nor a partition for one.
func TestSigninDocsSayTheLimits(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join("..", "..", "docs", name))
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(strings.Fields(string(b)), " ")
	}
	guide, proto := read("agent-signins.md"), read(filepath.Join("protocol", "signins.md"))
	for what, want := range map[string]string{
		"the harness's model can read the credential": "it can run a command that prints the credential",
		"masking is not a guarantee":                  "**not guaranteed**",
		"…nor a boundary":                             "it is not a boundary",
		"the workspace agent can get it":              "and then **it can**",
		"the approval says who holds it":              "The tile's coding agent will hold that sign-in",
		"the harness's own transcripts are unmasked":  "~/.claude/projects/",
		"…and what a person does about it":            "revoke it at its vendor, and delete that transcript",
		"partitioned tiles are not exempt":            "A partitioned tile is no exception",
		"the rule is on the tile, not the directory":  "is judged on `apps/crm`",
		"write level, not terminal":                   "holds write level or more on the tile",
	} {
		if !strings.Contains(guide, want) {
			t.Errorf("docs/agent-signins.md doesn't say %s (%q)", what, want)
		}
	}
	if !strings.Contains(proto, "This lowers accidental exposure and is not a guarantee") {
		t.Error("docs/protocol/signins.md doesn't say masking is not a guarantee")
	}
	for _, stale := range []string{"wherever it appears whole", "no other person has terminal access", "can also open terminals on"} {
		if strings.Contains(guide, stale) || strings.Contains(proto, stale) {
			t.Errorf("a doc still says %q", stale)
		}
	}
}
