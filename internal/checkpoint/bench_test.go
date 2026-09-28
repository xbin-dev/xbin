package checkpoint

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// covers D119d — 07-runtime §2.10's capture costs, measured on §13.2's
// reference tile (2 000 files, 20 MB): the first capture (estimate, init,
// run 1, run 2), an unchanged re-capture (run 1) and ten changed files (run
// 1, run 2). Direct mode here; BenchmarkCaptureConfined (integration) runs
// the same through the sandbox.
func BenchmarkCapture(b *testing.B) {
	if _, err := exec.LookPath("git"); err != nil {
		b.Skip("no git")
	}
	benchCapture(b)
}

func benchCapture(b *testing.B) {
	for _, phase := range []string{"first", "unchanged", "ten-changed"} {
		b.Run(phase, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				s := New(filepath.Join(b.TempDir(), "ws"))
				s.Caps.Burst, s.Caps.Every = 1<<30, time.Nanosecond
				src := referenceTile(b, s)
				req := CaptureRequest{Source: src, By: "user:bench", Create: true}
				if phase != "first" {
					if _, err := s.Capture(b.Context(), req); err != nil {
						b.Fatal(err)
					}
				}
				if phase == "ten-changed" {
					for f := 0; f < 10; f++ {
						p := filepath.Join(src.WorkTree, fmt.Sprintf("d%02d/f%03d.bin", f, f))
						if err := os.WriteFile(p, []byte(strings.Repeat("changed ", 1280)), 0o644); err != nil {
							b.Fatal(err)
						}
					}
				}
				b.StartTimer()
				if _, err := s.Capture(b.Context(), req); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// referenceTile is 2 000 files of 10 KiB in 20 directories.
func referenceTile(b *testing.B, s *Store) Source {
	b.Helper()
	dir := filepath.Join(s.Root, "apps", "ref")
	body := []byte(strings.Repeat("0123456789abcdef", 640))
	for d := 0; d < 20; d++ {
		if err := os.MkdirAll(filepath.Join(dir, fmt.Sprintf("d%02d", d)), 0o755); err != nil {
			b.Fatal(err)
		}
		for f := 0; f < 100; f++ {
			copy(body, fmt.Sprintf("%02d/%03d", d, f)) // distinct content per file
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("d%02d/f%03d.bin", d, f)), body, 0o644); err != nil {
				b.Fatal(err)
			}
		}
	}
	return Source{Tile: "apps/ref", WorkTree: dir}
}
