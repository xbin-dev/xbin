package registry

import (
	"encoding/json"
	"strings"
	"testing"
)

// D194: a manifest's title and icon, as /components carries them.
func TestManifestDisplay(t *testing.T) {
	var m Manifest
	if err := json.Unmarshal([]byte(`{"runtime":"go","title":"  Team\n mail‮ (EU) ","icon":"mail"}`), &m); err != nil {
		t.Fatal(err)
	}
	if got := m.DisplayTitle(); got != "Team mail (EU)" {
		t.Fatalf("title %q", got)
	}
	if got := m.DisplayIcon(); got != "mail" {
		t.Fatalf("icon %q", got)
	}
	long := Manifest{Title: strings.Repeat("é", 200)}
	if got := []rune(long.DisplayTitle()); len(got) != MaxTitleRunes {
		t.Fatalf("title capped at %d runes, got %d", MaxTitleRunes, len(got))
	}
	for _, bad := range []string{"<svg>", "Mail", "mail icon", "-mail", strings.Repeat("a", MaxIconLen+1)} {
		if got := (Manifest{Icon: bad}).DisplayIcon(); got != "" {
			t.Fatalf("icon %q shown as %q", bad, got)
		}
	}
	if (Manifest{}).DisplayTitle() != "" || (Manifest{}).DisplayIcon() != "" {
		t.Fatal("none given: none shown")
	}
}
