package docscheck

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The realtime worked example (docs/partitions.md §Realtime between
// partitions) is quoted from the Go SDK's example, which compiles and which
// a test runs (sdk/example_realtime_test.go, sdk/realtime_example_test.go):
// every Go block of that section, split at its "// …" lines, appears in the
// example file in order, line for line. So the page can't drift from code
// that works. Its JavaScript blocks are run by
// hack/partitions-realtime.test.mjs.
const (
	realtimeDoc     = "docs/partitions.md"
	realtimeHeading = "## Realtime between partitions"
	realtimeExample = "sdk/example_realtime_test.go"
)

var goFence = regexp.MustCompile("(?s)```go\n(.*?)```")

func TestPartitionsRealtimeQuotes(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join(repo, realtimeDoc))
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join(repo, realtimeExample))
	if err != nil {
		t.Fatal(err)
	}
	section := sectionOf(string(doc), realtimeHeading)
	if section == "" {
		t.Fatalf("%s has no %q section", realtimeDoc, realtimeHeading)
	}
	file := trimLines(string(src))
	blocks := goFence.FindAllStringSubmatch(section, -1)
	if len(blocks) < 3 {
		t.Fatalf("%s §Realtime has %d Go blocks; the three patterns quote at least one each", realtimeDoc, len(blocks))
	}
	for _, b := range blocks {
		at := 0
		for _, frag := range fragments(b[1]) {
			i := indexLines(file, frag, at)
			if i < 0 {
				t.Errorf("%s §Realtime quotes Go that %s doesn't hold (in this order):\n%s",
					realtimeDoc, realtimeExample, strings.Join(frag, "\n"))
				break
			}
			at = i + len(frag)
		}
	}
}

// sectionOf is the text of the level-2 section headed heading, up to the
// next level-2 heading.
func sectionOf(doc, heading string) string {
	i := strings.Index(doc, "\n"+heading+"\n")
	if i < 0 {
		return ""
	}
	rest := doc[i+1+len(heading):]
	if j := strings.Index(rest, "\n## "); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// fragments splits a quoted block at its elision lines ("// …"), dropping
// empty fragments.
func fragments(block string) [][]string {
	var out [][]string
	var cur []string
	for _, l := range trimLines(block) {
		if strings.HasPrefix(strings.TrimSpace(l), "// …") {
			if len(cur) > 0 {
				out = append(out, cur)
			}
			cur = nil
			continue
		}
		cur = append(cur, l)
	}
	for len(cur) > 0 && cur[len(cur)-1] == "" {
		cur = cur[:len(cur)-1]
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

func trimLines(s string) []string {
	ls := strings.Split(s, "\n")
	for i, l := range ls {
		ls[i] = strings.TrimRight(l, " \t\r")
	}
	return ls
}

// indexLines is the first index ≥ from where file holds frag line for line,
// or -1.
func indexLines(file, frag []string, from int) int {
	for i := from; i+len(frag) <= len(file); i++ {
		ok := true
		for j, l := range frag {
			if file[i+j] != l {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}
