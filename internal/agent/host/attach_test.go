package host

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/agent/acp"
)

// A prompt's files land in a private dir under the sandbox's tmp — a second
// file of the same name beside the first, never through an existing path;
// past the budget the oldest go; a name with a path in it is refused; the
// dir goes with the host.
func TestAttachDrops(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	h := newHost(t, t.TempDir())
	put := func(name string, data []byte) string {
		t.Helper()
		res, rerr := h.attach(acp.AttachParams{Name: name, Data: data})
		if rerr != nil {
			t.Fatalf("attach %s: %v", name, rerr)
		}
		return res.(acp.AttachResult).Path
	}
	p1 := put("shot.png", []byte("one"))
	p2 := put("shot.png", []byte("two"))
	if filepath.Dir(p1) != filepath.Dir(p2) || !strings.HasPrefix(p1, tmp) || filepath.Base(p1) != "shot.png" || filepath.Base(p2) != "shot-2.png" {
		t.Fatalf("paths: %s %s", p1, p2)
	}
	if b, _ := os.ReadFile(p2); string(b) != "two" {
		t.Fatalf("content: %q", b)
	}
	if fi, _ := os.Stat(filepath.Dir(p1)); fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", fi.Mode().Perm())
	}
	for _, bad := range []string{"", ".", "..", "a/b", `a\b`, "a\x00b"} {
		if _, rerr := h.attach(acp.AttachParams{Name: bad, Data: []byte("x")}); rerr == nil || rerr.Code != acp.ErrInvalidParam {
			t.Fatalf("name %q: %v", bad, rerr)
		}
	}
	// the budget: a file that doesn't fit beside the others pushes the oldest out
	big := bytes.Repeat([]byte{'x'}, attachBudget-2)
	p3 := put("big.bin", big)
	if _, err := os.Stat(p1); err == nil {
		t.Fatal("the oldest file stayed past the budget")
	}
	if _, err := os.Stat(p3); err != nil {
		t.Fatal(err)
	}
	if _, rerr := h.attach(acp.AttachParams{Name: "huge.bin", Data: make([]byte, attachBudget+1)}); rerr == nil {
		t.Fatal("a file over the whole budget was taken")
	}
	h.dropAttachments()
	if _, err := os.Stat(filepath.Dir(p3)); !os.IsNotExist(err) {
		t.Fatalf("the attachment dir outlived the host: %v", err)
	}
}
