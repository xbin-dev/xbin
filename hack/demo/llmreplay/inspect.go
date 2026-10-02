package main

import (
	"flag"
	"fmt"
	"io"
	"strings"
)

// inspectCmd prints what a cassette holds, lane by lane, numbered as replay
// numbers it (thread and step): enough to check a recording before a shoot
// without reading the JSON. -v adds each call's newest input (prompt text:
// mind where the output goes).
func inspectCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	verbose := fs.Bool("v", false, "show each call's newest input (an excerpt)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: llmreplay inspect [-v] FILE")
		return 2
	}
	c, err := loadCassette(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, "llmreplay:", err)
		return 1
	}
	lanes, lookups := laneThreads(c)
	fmt.Fprintf(stdout, "%s: cassette v%d recorded %s, %d exchanges", fs.Arg(0), c.Header.LLMReplay,
		c.Header.Created.Local().Format("2006-01-02 15:04"), len(c.Exchanges))
	if c.Skipped > 0 {
		fmt.Fprintf(stdout, " (%d unreadable lines)", c.Skipped)
	}
	fmt.Fprintln(stdout)
	for _, name := range sortedKeys(lanes) {
		es := lanes[name]
		threads := map[int]bool{}
		for _, e := range es {
			threads[e.thread] = true
		}
		fmt.Fprintf(stdout, "\nlane %s: %d model calls in %d threads\n", name, len(es), len(threads))
		for _, e := range es {
			fmt.Fprintf(stdout, "  seq %-4d t%d·%-3d %-18s %-28s %-6s %s%s\n", e.ex.Seq, e.thread, e.step, e.d.api, clip(e.d.model, 28),
				map[bool]string{true: "stream"}[e.d.stream], describeAnswer(e.ex), flags(e.ex))
			if *verbose {
				_, tail := e.d.texts()
				fmt.Fprintf(stdout, "             > %s\n", clip(strings.Join(tail, " | "), 160))
			}
		}
	}
	if len(lookups) > 0 {
		fmt.Fprintf(stdout, "\nlookups (answered by content, any number of times):\n")
		for _, e := range lookups {
			fmt.Fprintf(stdout, "  %-10s %s → %s\n", e.ex.Lane, e.d.describeCall(), describeAnswer(e.ex))
		}
	}
	return 0
}

func flags(ex *Exchange) string {
	if ex.transient() {
		return "  [left out of replay: transient failure; -keep-errors keeps it]"
	}
	return ""
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
