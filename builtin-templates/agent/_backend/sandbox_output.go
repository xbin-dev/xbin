// sandbox_output.go — shaping a command's output for the model: its head and
// its tail (the end of a build or a test run is where the verdict is),
// terminal noise taken out, and a footer that says how it ended.
package main

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// A bash result stays under bashMax: a short head, a long tail.
const (
	bashMax  = 12 << 10
	bashHead = 2 << 10
	bashTail = bashMax - bashHead - 512 // the elision marker and the footer fit in the rest
)

// shaper keeps the first headMax bytes of a stream and its last tailMax.
type shaper struct {
	headMax, tailMax int
	head, tail       []byte
	elided           int64 // bytes between head and tail: pushed out, skipped, or dropped by the ring
	lead             int64 // bytes gone before anything was kept (a ring that dropped the start)
}

func newShaper() *shaper { return &shaper{headMax: bashHead, tailMax: bashTail} }

func (s *shaper) write(p []byte) {
	if n := s.headMax - len(s.head); n > 0 {
		k := min(n, len(p))
		s.head = append(s.head, p[:k]...)
		p = p[k:]
	}
	if len(p) == 0 {
		return
	}
	s.tail = append(s.tail, p...)
	if over := len(s.tail) - s.tailMax; over > 0 {
		s.elided += int64(over)
		s.tail = append(s.tail[:0:0], s.tail[over:]...)
	}
}

// headFull: from here on only the tail keeps anything, so a reader may skip
// what the tail couldn't hold anyway.
func (s *shaper) headFull() bool { return len(s.head) >= s.headMax }

// skip accounts for n bytes never read — a reader jumping ahead, or a ring
// that dropped them: the head ends where they start, and the tail starts
// after them.
func (s *shaper) skip(n int64) {
	if n <= 0 {
		return
	}
	if s.empty() {
		s.lead += n
		return
	}
	s.headMax = len(s.head)
	s.elided += n + int64(len(s.tail))
	s.tail = s.tail[:0]
}

func (s *shaper) empty() bool { return len(s.head) == 0 && len(s.tail) == 0 && s.elided == 0 }

func (s *shaper) String() string {
	lead := ""
	if s.lead > 0 {
		lead = fmt.Sprintf("… %d earlier bytes are gone (the sandbox keeps only the latest output) …\n", s.lead)
	}
	if s.elided == 0 {
		return lead + cleanOutput(append(append([]byte(nil), s.head...), s.tail...))
	}
	head := s.head
	for len(head) > 0 && !utf8.FullRune(head[lastRuneStart(head):]) {
		head = head[:lastRuneStart(head)]
	}
	tail := s.tail
	for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
		tail = tail[1:]
	}
	return lead + strings.TrimRight(cleanOutput(head), "\n") +
		fmt.Sprintf("\n… %d bytes elided …\n", s.elided) + cleanOutput(tail)
}

func lastRuneStart(b []byte) int {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			return i
		}
	}
	return len(b)
}

// ansiRE: terminal escapes (colors, cursor moves, titles) a no-TTY command
// may still print.
var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]`)

// cleanOutput is text for a transcript: valid UTF-8, no escapes, a line
// redrawn with \r (a progress bar) kept as its last state, no stray control
// bytes.
func cleanOutput(b []byte) string {
	s := strings.ToValidUTF8(string(b), "\uFFFD")
	s = ansiRE.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if strings.ContainsRune(s, '\r') {
		lines := strings.Split(s, "\n")
		for i, l := range lines {
			if !strings.ContainsRune(l, '\r') {
				continue
			}
			parts := strings.Split(l, "\r")
			last := ""
			for _, p := range parts {
				if p != "" {
					last = p
				}
			}
			lines[i] = last
		}
		s = strings.Join(lines, "\n")
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\t' || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// fmtDur is a short duration: 14s, 2m05s, 1h02m.
func fmtDur(d time.Duration) string {
	s := int((d + time.Second/2) / time.Second)
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm%02ds", s/60, s%60)
	}
	return fmt.Sprintf("%dh%02dm", s/3600, s%3600/60)
}

// endWords says how a command ended.
func endWords(state string, code *int, signal string) string {
	switch {
	case signal != "":
		return "killed by " + signal
	case code != nil:
		return fmt.Sprintf("exit %d", *code)
	case state == "killed":
		return "killed"
	case state == "lost":
		return "lost (its sandbox restarted, or went away)"
	}
	return state
}

// withFooter is a result: the output (or that there was none) and a footer.
func withFooter(out, footer string) string {
	out = strings.TrimRight(out, "\n")
	if out == "" {
		out = "(no output)"
	}
	return out + "\n[" + footer + "]"
}
