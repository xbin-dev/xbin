package auth

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/users"
)

// The reserved viewer of preview sessions (D234) is not a person: no account
// can take the id, and it never issues or redeems a view-as.
func TestPreviewViewerIsNobody(t *testing.T) {
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{PreviewViewer, "preview~", "~"} {
		if _, err := st.Upsert(users.User{ID: id, Role: users.RoleAdmin}, "a-password-1"); err == nil {
			t.Errorf("an account took the id %q", id)
		}
	}
	a, err := Load(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Upsert(users.User{ID: "alice", Role: users.RoleAdmin}, "a-password-1"); err != nil {
		t.Fatal(err)
	}
	a.SetUsers(st)
	// even a principal that somehow carried the id as its own (a users file
	// edited by hand: ids on disk are not re-validated) mints nothing
	fake := Principal{UserID: PreviewViewer, User: &users.User{ID: PreviewViewer, Role: users.RoleAdmin}, Via: "session"}
	if _, err := a.NewImpersonationTicket(fake, "alice"); err == nil {
		t.Error("the reserved viewer minted a view-as ticket")
	}
	p := Principal{UserID: "alice", Impersonator: PreviewViewer}
	if !p.ReadOnly() || !p.Preview() || p.ViewAs() {
		t.Errorf("a preview principal: readOnly %v preview %v viewAs %v", p.ReadOnly(), p.Preview(), p.ViewAs())
	}
	p.Impersonator = "bob"
	if !p.ReadOnly() || p.Preview() || !p.ViewAs() {
		t.Errorf("a view-as principal: readOnly %v preview %v viewAs %v", p.ReadOnly(), p.Preview(), p.ViewAs())
	}
}

// impersonatorUses is every file outside this package that reads
// Principal.Impersonator directly, with how many times and what the reads
// are for. Since D234 the field has two kinds of value — an admin's id (a
// view-as) and the reserved PreviewViewer (the person's own read-only
// render) — so a direct read must be one of:
//
//	act     "is this the person, acting?" (== "" / != ""): a preview is not
//	        — it performs no person-only act, so the comparison stays as is
//	carry   copied into another credential or an audit line, value and all
//	name    shown as WHO is viewing: only under ViewAs() (or after Preview()
//	        was ruled out), never the reserved viewer as a person's name
//
// Everything else asks the question it means: ReadOnly() for "may it
// write", ViewAs() for "is someone else looking" (the privacy gates),
// Preview() for "is this xbind's own render". A new direct read fails this
// test until it is classified here.
var impersonatorUses = map[string]struct {
	n    int
	kind string
}{
	"internal/broker/agentlevel.go":               {1, "act: the person's own approval"},
	"internal/broker/agentpolicy.go":              {1, "act"},
	"internal/broker/changeset.go":                {1, "act: an admin approving a change set"},
	"internal/broker/delegation.go":               {1, "act: a delegate is never a view of someone"},
	"internal/broker/backup_partition_restore.go": {1, "act"},
	"internal/broker/partitionconsent.go":         {1, "act"},
	"internal/broker/partitioncreds.go":           {1, "act"},
	"internal/broker/partitionlist.go":            {2, "act: the person's own row actions"},
	"internal/broker/partitionlogshare.go":        {3, "act"},
	"internal/broker/partitionmail_api.go":        {1, "act: the person's mail is theirs to open"},
	"internal/broker/partitionops.go":             {1, "act"},
	"internal/broker/partitionpeople.go":          {1, "act: person-only events"},
	"internal/broker/personalbind_api.go":         {1, "act"},
	"internal/broker/tilelimits_facts.go":         {1, "act"},
	"internal/broker/usersapi.go":                 {2, "name: the sessions list, which never holds a preview session (Sessions skips them)"},
	"internal/broker/whoami.go":                   {2, "name: after the Preview() case"},
	"internal/proxy/proxy.go":                     {1, "name: X-XBin-Viewed-By, under ViewAs()"},
	"internal/server/agentprefsapi.go":            {1, "act"},
	"internal/server/changeset.go":                {1, "act: who may approve"},
	"internal/server/delegateapi.go":              {2, "act: the person's own grants and pauses"},
	"internal/server/partitionclass.go":           {1, "act: PersonOnly routes"},
	"internal/server/server.go":                   {2, "carry: the audit line"},
	"internal/server/tileorigin.go":               {2, "carry: the tile cookie's grant"},
}

func TestImpersonatorReadsAreClassified(t *testing.T) {
	root := filepath.Join("..", "..")
	use := regexp.MustCompile(`\.Impersonator\b|\bImpersonator:`)
	got := map[string]int{}
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "internal/auth/") {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if n := len(use.FindAll(b, -1)); n > 0 {
				got[rel] = n
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for file, n := range got {
		if want, ok := impersonatorUses[file]; !ok || want.n != n {
			t.Errorf("%s reads Principal.Impersonator %d time(s), classified: %d — a preview session's is the reserved %q, never a person: ask ReadOnly(), ViewAs() or Preview(), or classify the read in impersonatorUses (act, carry, name)", file, n, want.n, PreviewViewer)
		}
	}
	for file := range impersonatorUses {
		if got[file] == 0 {
			t.Errorf("%s no longer reads Principal.Impersonator: drop its row from impersonatorUses", file)
		}
	}
}
