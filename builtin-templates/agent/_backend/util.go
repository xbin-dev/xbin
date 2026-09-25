// util.go — small helpers shared across the backend.
package main

import (
	"fmt"
	"strconv"
	"strings"
)

func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case string:
		i, _ := strconv.Atoi(n)
		return i
	}
	return 0
}

// sliceLines returns a 1-based line window, with a marker for what was left
// out, so a text larger than the tool-result cap can be read in ranges.
func sliceLines(content string, offset, limit int) string {
	if offset <= 1 && limit <= 0 {
		return content
	}
	lines := strings.Split(content, "\n")
	start := offset - 1
	if start < 0 {
		start = 0
	}
	if start >= len(lines) {
		return fmt.Sprintf("(offset %d is past the end — the text has %d lines)", offset, len(lines))
	}
	end := len(lines)
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	var b strings.Builder
	if start > 0 {
		fmt.Fprintf(&b, "…[lines 1-%d not shown]\n", start)
	}
	b.WriteString(strings.Join(lines[start:end], "\n"))
	if end < len(lines) {
		fmt.Fprintf(&b, "\n…[lines %d-%d not shown]", end+1, len(lines))
	}
	return b.String()
}

func humanBytes(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f KB", float64(n)/1024)
}

// clip cuts s to n bytes (on a rune boundary) with an ellipsis.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "…"
}

// orStr is s, or def when s is empty.
func orStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
