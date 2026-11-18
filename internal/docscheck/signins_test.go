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
		"the approval's line says why it is warned":   "what it tells the coding agent could expose your sign-in",
		"the harness's own transcripts are unmasked":  "~/.claude/projects/",
		"…and what a person does about it":            "revoke it at its vendor, and delete that transcript",
		"the rule is on the tile, not the directory":  "is judged on `apps/crm`",
		// the rule of 2026-11-17: used by default, trust asked where the source isn't only the person's
		"the default is on":                             "**The default is on.**",
		"what the default exposes":                      "**only code you put there**",
		"what trusting exposes":                         "**the people the card listed**",
		"condition 1, apart":                            "**it is owned by someone else**",
		"condition 2, apart; write level":               "**someone else who isn't a workspace admin can change its code**",
		"partitioned tiles are not exempt":              "on a partitioned tile too",
		"a changed basis voids the trust, for good":     "A void answer **stays void**",
		"…when it is seen, with no session running":     "with no session of yours running",
		"the answer is for what was shown":              "**You answer what you were shown.**",
		"people are shown by account id":                "by **account id**",
		"everyone is shown before it can be trusted":    "you see every one of them before the button can be used",
		"a workspace tile is an admin's own":            "**for a workspace admin**",
		"the agent never gets it by itself":             "**Your workspace agent never gets your sign-in by itself.**",
		"never under start without asking, on any tile": "**never has your sign-in**, on any tile",
		"the line is unticked":                          "a line of its own, **unticked**",
		"approving is neither using nor trusting":       "approving a start never implies using your sign-in, and never implies trust",
		"what a ticked start exposes":                   "your agent can steer that session",
		"a file in the tile is not masked":              "a file written in the tile is not masked at all",
		// D245: the home is a copy on a tile the person has not trusted — closed for writes, open for reads
		"the home: closed for writes, open for reads":  "**closed for writes, open for reads.**",
		"…reads stay open, said plainly":               "It can still **read** your home",
		"…what the copy is":                            "**Writes stay on this tile.**",
		"what trusting means now: two things":          "means two things: your saved sign-in is used there, and",
		"trusting merges nothing":                      "there is no \"copy it over\"",
		"a copy that can't be mounted fails closed":    "session **fails** and says why",
		"no sandboxes, no protection":                  "nothing here protects it",
		"an admin can't type into a real-home session": "admin can't attach to, or drive, your session when it has your own home",
		"…with the exact attack":                       "`~/.claude/settings.json` hooks, `~/.gitconfig`",
		// the review of D245
		"an admin's copy session is a member's":    "**confined like a member's until you trust that tile**",
		"who could change a tile before":           "**someone else could change its code before, and can't now**",
		"…and what xbind can't know":               "**What xbind can't know:**",
		"an old answer is void":                    "**what trusting meant when you gave it**",
		"a terminal's token: that terminal's home": "**only when that terminal has it**",
		"nothing is deleted on a doubt":            "xbind never deletes a copy on a doubt",
		"a restore from a backup":                  "**restored from a backup**",
		"the fallback's limit":                     "**only root in the session can use the home**",
		"the trust button is a deliberate click":   "not Enter, not a script",
		"nothing but the person answers":           "nothing but you answers it",
		"the migration":                            "get the question instead",
		"the owner accepted the risk":              "accepted the risk",
	} {
		if !strings.Contains(guide, want) {
			t.Errorf("docs/agent-signins.md doesn't say %s (%q)", what, want)
		}
	}
	if !strings.Contains(proto, "This lowers accidental exposure and is not a guarantee") {
		t.Error("docs/protocol/signins.md doesn't say masking is not a guarantee")
	}
	for _, stale := range []string{"not closed in this version", "mounted read-write into every session of yours", "wherever it appears whole", "no other person has terminal access", "can also open terminals on", "never on a start your", "gets your default sign-in on a tile", "asks again** when someone new"} {
		if strings.Contains(guide, stale) || strings.Contains(proto, stale) {
			t.Errorf("a doc still says %q", stale)
		}
	}
}
