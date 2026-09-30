package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestShellSwitchWords keeps the shell's words for a switch in step with
// xbind's: the scaffold shell's card overlay and typed confirmation
// (workspace-template/shell/partition-mode.js switchDeletes, modeName) say
// what SwitchDeletes and PartitionSpec.String say, before the dry run's
// answer (which carries xbind's own) arrives.
func TestShellSwitchWords(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "workspace-template", "shell", "partition-mode.js"))
	if err != nil {
		t.Fatal(err)
	}
	js := strings.ReplaceAll(string(b), `\'`, `'`)
	U, UG, N := PartitionSpec{User: true}, PartitionSpec{User: true, Global: true}, PartitionSpec{}
	for _, want := range []string{
		SwitchDeletes(N, U), SwitchDeletes(UG, U), SwitchDeletes(U, UG),
		`'` + U.String() + `'`, `'` + UG.String() + `'`, `'` + N.String() + `'`, `'` + (PartitionSpec{Global: true}).String() + `'`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("partition-mode.js doesn't say %q, as xbind does", want)
		}
	}
}
