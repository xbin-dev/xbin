package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The workspace agent's map of xbin is generated from the docs: the
// checked-in file is exactly what the docs give now (a doc passage that
// changed, or went, without the file being regenerated fails here), and it
// is within its budget.
func TestModelIsCurrent(t *testing.T) {
	root := filepath.Join("..", "..")
	got, err := generate(filepath.Join(root, "docs"))
	if err != nil {
		t.Fatalf("generating the map: %v", err)
	}
	have, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(out)))
	if err != nil {
		t.Fatalf("%v — run hack/wsagent-model.sh", err)
	}
	if !bytes.Equal(have, got) {
		t.Fatalf("%s isn't what the docs give now: run hack/wsagent-model.sh and commit the result", out)
	}
	if len(got) > budget {
		t.Fatalf("the map is %d bytes, over %d", len(got), budget)
	}
}

func TestPassageCutsAtASentence(t *testing.T) {
	if got := cut("One. Two is longer than the rest. Three.", 12); got != "One." {
		t.Fatalf("cut: %q", got)
	}
	if got := cut("Each one has its own:", 80); got != "Each one has its own." {
		t.Fatalf("cut at a colon: %q", got)
	}
	if got := plain("- **Bold** and a [link](x.md) (D19) with `code/*` (\"x\", D16)"); got != "Bold and a link with code/* (\"x\")" {
		t.Fatalf("plain: %q", got)
	}
}
