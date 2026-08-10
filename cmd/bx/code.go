package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// bx code — cross-tile change proposals ("code PRs", plans/code-prs.md).
// An agent that can READ a sibling tile proposes changes to it as a git
// format-patch series; the target's own terminal/agent reviews and applies
// with `git am`. xbind stores proposals (data/prs/), never applies them.

type prMetaJSON struct {
	Number int    `json:"number"`
	Target string `json:"target"`
	From   struct {
		Component string `json:"component"`
		User      string `json:"user"`
		Via       string `json:"via"`
	} `json:"from"`
	Title   string    `json:"title"`
	Message string    `json:"message"`
	Base    string    `json:"base"`
	State   string    `json:"state"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	Events  []struct {
		TS    time.Time `json:"ts"`
		Who   string    `json:"who"`
		Type  string    `json:"type"`
		Body  string    `json:"body"`
		State string    `json:"state"`
	} `json:"events"`
}

func cmdCode(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: bx code prs|pr … (see bx code pr)")
	}
	switch args[0] {
	case "prs":
		return cmdCodePRs(args[1:])
	case "pr":
		return cmdCodePR(args[1:])
	}
	return fmt.Errorf("unknown: bx code %s (want prs|pr)", args[0])
}

// bx code prs [<component>] [--all|--state=S] [--from]
func cmdCodePRs(args []string) error {
	comp, state, mine := "", "open", false
	for _, a := range args {
		switch {
		case a == "--from":
			mine = true
		case a == "--all":
			state = ""
		case strings.HasPrefix(a, "--state="):
			state = strings.TrimPrefix(a, "--state=")
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %s", a)
		default:
			comp = a
		}
	}
	q := ""
	if state != "" {
		q = "&state=" + state
	}
	var out struct {
		PRs []prMetaJSON `json:"prs"`
	}
	if mine {
		if err := apiJSON("GET", "/api/xbin/code/prs?from=1"+q, nil, &out); err != nil {
			return err
		}
	} else {
		if comp == "" {
			comp = os.Getenv("XBIN_COMPONENT")
		}
		if comp == "" {
			return fmt.Errorf("usage: bx code prs <component> (or run inside a tile terminal, or --from for your outgoing PRs)")
		}
		if err := apiJSON("GET", "/api/xbin/code/prs?target="+comp+q, nil, &out); err != nil {
			return err
		}
	}
	if len(out.PRs) == 0 {
		if state == "open" {
			fmt.Println("no open PRs (bx code prs --all shows closed ones)")
		} else {
			fmt.Println("no PRs")
		}
		return nil
	}
	for _, m := range out.PRs {
		loc := ""
		if mine {
			loc = " → " + m.Target
		}
		from := m.From.Component
		if from == "" {
			from = "owner"
			if m.From.User != "" {
				from = "user:" + m.From.User
			}
		}
		comments := 0
		for _, e := range m.Events {
			if e.Type == "comment" {
				comments++
			}
		}
		extra := ""
		if comments > 0 {
			extra = fmt.Sprintf("  💬%d", comments)
		}
		fmt.Printf("#%-3d %-9s %s%s  (from %s, %s)%s\n",
			m.Number, m.State, m.Title, loc, from, prAge(m.Updated), extra)
	}
	if !mine && state == "open" {
		fmt.Println("\nreview one: bx code pr show <n> · apply: bx code pr fetch <n> | git am --3way")
	}
	return nil
}

// bx code pr <target> --title <t> [-m <msg>] [--base <rev>] <patch>…  (open)
// bx code pr show|fetch|comment|close <n> [<component>] [flags]
func cmdCodePR(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf(`usage:
  bx code pr <target> --title <t> [-m <msg>] [--base <rev>] <patch.mbox>…
  bx code pr show <n> [<component>]
  bx code pr fetch <n> [<component>]        series to stdout (| git am --3way)
  bx code pr comment <n> [<component>] -m <text>
  bx code pr close <n> [<component>] --merged|--rejected|--withdrawn [-m <text>]`)
	}
	switch args[0] {
	case "show", "fetch", "comment", "close":
		return cmdCodePRVerb(args[0], args[1:])
	}
	return cmdCodePROpen(args)
}

// prLocate parses "<n> [<component>]" with the component defaulting to the
// terminal's tile.
func prLocate(args []string) (comp string, n int, rest []string, err error) {
	if len(args) < 1 {
		return "", 0, nil, fmt.Errorf("pr number required")
	}
	n, err = strconv.Atoi(args[0])
	if err != nil || n < 1 {
		return "", 0, nil, fmt.Errorf("bad pr number %q", args[0])
	}
	comp = os.Getenv("XBIN_COMPONENT")
	rest = args[1:]
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		comp, rest = rest[0], rest[1:]
	}
	if comp == "" {
		return "", 0, nil, fmt.Errorf("component required (bx code pr %d <component>) outside a tile terminal", n)
	}
	return comp, n, rest, nil
}

func cmdCodePRVerb(verb string, args []string) error {
	comp, n, rest, err := prLocate(args)
	if err != nil {
		return err
	}
	qs := fmt.Sprintf("target=%s&n=%d", comp, n)
	switch verb {
	case "show":
		var m prMetaJSON
		if err := apiJSON("GET", "/api/xbin/code/pr?"+qs, nil, &m); err != nil {
			return err
		}
		from := m.From.Component
		if from == "" {
			from = "owner"
			if m.From.User != "" {
				from = "user:" + m.From.User
			}
		} else if m.From.User != "" {
			from += " (" + m.From.User + ")"
		}
		fmt.Printf("PR %s#%d — %s\nstate: %s · from: %s · opened %s\n",
			m.Target, m.Number, m.Title, m.State, from, prAge(m.Created))
		if m.Base != "" {
			fmt.Printf("base:  %s (the target's HEAD when formatted — git am --3way rides drift)\n", m.Base)
		}
		if m.Message != "" {
			fmt.Printf("\n%s\n", m.Message)
		}
		for _, e := range m.Events {
			if e.Type == "state" {
				fmt.Printf("\n[%s] %s → %s", prAge(e.TS), e.Who, e.State)
				if e.Body != "" {
					fmt.Printf(": %s", e.Body)
				}
				fmt.Println()
			} else {
				fmt.Printf("\n[%s] %s:\n%s\n", prAge(e.TS), e.Who, e.Body)
			}
		}
		fmt.Printf("\nseries: bx code pr fetch %d %s | git am --3way   (review the diff FIRST)\n", n, comp)
		return nil

	case "fetch":
		resp, err := api("GET", "/api/xbin/code/pr/series?"+qs, nil)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			b, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
		}
		_, err = io.Copy(os.Stdout, resp.Body)
		return err

	case "comment":
		text := flagText(rest)
		if text == "" {
			return fmt.Errorf("usage: bx code pr comment <n> [<component>] -m <text>")
		}
		if err := apiJSON("POST", "/api/xbin/code/pr/comment",
			map[string]any{"target": comp, "n": n, "body": text}, nil); err != nil {
			return err
		}
		fmt.Printf("commented on %s#%d\n", comp, n)
		return nil

	case "close":
		state, text := "", flagText(rest)
		for _, a := range rest {
			switch a {
			case "--merged":
				state = "merged"
			case "--rejected":
				state = "rejected"
			case "--withdrawn":
				state = "withdrawn"
			}
		}
		if state == "" {
			return fmt.Errorf("usage: bx code pr close <n> [<component>] --merged|--rejected|--withdrawn [-m <text>]")
		}
		if err := apiJSON("POST", "/api/xbin/code/pr/state",
			map[string]any{"target": comp, "n": n, "state": state, "comment": text}, nil); err != nil {
			return err
		}
		fmt.Printf("%s#%d %s\n", comp, n, state)
		return nil
	}
	return nil
}

func cmdCodePROpen(args []string) error {
	target, title, msg, base := "", "", "", ""
	var patches []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--title" && i+1 < len(args):
			i++
			title = args[i]
		case a == "-m" && i+1 < len(args):
			i++
			msg = args[i]
		case a == "--base" && i+1 < len(args):
			i++
			base = args[i]
		case strings.HasPrefix(a, "-") && a != "-":
			return fmt.Errorf("unknown flag %s", a)
		case target == "":
			target = a
		default:
			patches = append(patches, a)
		}
	}
	if target == "" || title == "" || len(patches) == 0 {
		return fmt.Errorf("usage: bx code pr <target> --title <t> [-m <why + how it was tested>] [--base <rev>] <patch.mbox>… ('-' = stdin)")
	}
	var series strings.Builder
	for _, p := range patches {
		var data []byte
		var err error
		if p == "-" {
			data, err = io.ReadAll(os.Stdin)
		} else {
			data, err = os.ReadFile(p)
		}
		if err != nil {
			return err
		}
		series.Write(data)
		if len(data) > 0 && data[len(data)-1] != '\n' {
			series.WriteByte('\n')
		}
	}
	// format-patch --base records the base commit in a trailer; lift it so
	// the reviewer can judge staleness without opening the mbox.
	if base == "" {
		for _, line := range strings.Split(series.String(), "\n") {
			if s, ok := strings.CutPrefix(strings.TrimSpace(line), "base-commit: "); ok {
				base = s
				break
			}
		}
	}
	var m prMetaJSON
	if err := apiJSON("POST", "/api/xbin/code/prs",
		map[string]any{"target": target, "title": title, "message": msg, "base": base, "series": series.String()}, &m); err != nil {
		return err
	}
	fmt.Printf("opened %s#%d — %s\nthe %s side reviews it with: bx code prs (in its terminal) · your side: bx code prs --from\n",
		m.Target, m.Number, m.Title, m.Target)
	return nil
}

func flagText(args []string) string {
	for i, a := range args {
		if a == "-m" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func prAge(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
