package docscheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// covers D230 (review 3c, 5; the rule of 2026-11-17) — the sign-in docs
// say what masking and the rule are and are not: a reader must not take
// redaction for a boundary, nor a partition for one; and what the default
// and trusting a tile expose.
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
		"the workspace agent can get it":              "**it can**",
		"the approval says who holds it":              "The tile's coding agent will hold that sign-in",
		"the harness's own transcripts are unmasked":  "~/.claude/projects/",
		"…and what a person does about it":            "revoke it at its vendor, and delete that transcript",
		"the rule is on the tile, not the directory":  "is judged on `apps/crm`",
		// the rule of 2026-11-17: used by default, trust asked where the source isn't only the person's
		"the default is on":                            "**The default is on.**",
		"what the default exposes":                     "**only code you put there**",
		"what trusting exposes":                        "**the people the card listed**",
		"condition 1, apart":                           "**it is owned by someone else**",
		"condition 2, apart; write level":              "**someone else who isn't a workspace admin can change its code**",
		"partitioned tiles are not exempt":             "on a partitioned tile too",
		"trust is re-asked when its basis changes":     "**asks again** when someone new can change it",
		"approving a start is not trusting":            "approving a start never implies trust",
		"unasked starts never reach an untrusted tile": "**never** on a tile you haven't trusted",
		"nothing but the person answers":               "nothing but you answers it",
		"the migration":                                "get the question instead",
		"the owner accepted the risk":                  "accepted the risk",
	} {
		if !strings.Contains(guide, want) {
			t.Errorf("docs/agent-signins.md doesn't say %s (%q)", what, want)
		}
	}
	if !strings.Contains(proto, "This lowers accidental exposure and is not a guarantee") {
		t.Error("docs/protocol/signins.md doesn't say masking is not a guarantee")
	}
	for _, stale := range []string{"wherever it appears whole", "no other person has terminal access", "can also open terminals on", "never on a start your"} {
		if strings.Contains(guide, stale) || strings.Contains(proto, stale) {
			t.Errorf("a doc still says %q", stale)
		}
	}
}
