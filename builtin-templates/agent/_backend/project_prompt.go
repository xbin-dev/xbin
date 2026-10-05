// project_prompt.go — a task's brief (API.md §The workspace, "What a task
// is told"): the # Project section of a built-in task's system prompt,
// after the sandbox's (context.go), and the same words at the start of a
// coding agent's first prompt (it has no system prompt of ours). Built from
// the database only and stable from turn to turn, so the prompt cache stays
// warm. Text from outside — the setup's output — is clipped, redacted and
// framed as untrusted.
package main

import (
	"fmt"
	"strings"
)

// projectPrompt is task k's # Project section (sub: for a subagent of it).
func projectPrompt(d *DB, p *Project, k *ProjectTask, sub bool) string {
	pol := policyOf(p.Policy)
	var b strings.Builder
	b.WriteString("# Project\n")
	// a task started from an issue may carry the issue's title (createTask's
	// default): text from outside, so it is left out of this line and
	// framed with the issue below
	title := ""
	if k.Issue == nil {
		title = fmt.Sprintf(": %q", k.Title)
	}
	if sub {
		fmt.Fprintf(&b, "You work for task #%d of the project %q%s.\n", k.N, p.Name, title)
	} else {
		fmt.Fprintf(&b, "This conversation is task #%d of the project %q%s (%s).\n", k.N, p.Name, title, orStr(k.Size, sizeSmall))
	}
	fmt.Fprintf(&b, "Branch: %s — commit to it and push it (`git push`); never push the default branch, never merge: a person reviews and merges.\n", k.Branch)
	repos, _ := d.projectRepos(p.ID)
	cos := map[string]ProjectCheckout{}
	for _, c := range d.checkouts(k.ID) {
		cos[c.Repo] = c
	}
	if k.Dir != "" {
		fmt.Fprintf(&b, "Workspace: %s", k.Dir)
		if cwd := k.cwd(); cwd != k.Dir {
			fmt.Fprintf(&b, " (you start in %s)", cwd)
		}
		b.WriteString("\n")
	}
	for _, slug := range k.Repos {
		for _, r := range repos {
			if r.Slug != slug {
				continue
			}
			path := cos[slug].Path
			if path == "" && k.Dir != "" {
				path = k.Dir + "/" + slug
			}
			fmt.Fprintf(&b, "- %s: %s (its default branch: %s)\n", r.Repo, orStr(path, "being prepared"), orStr(r.DefaultBranch, "unknown"))
		}
	}
	b.WriteString("Each repo's AGENTS.md / CLAUDE.md governs how to work in it — read it first.\n")
	if k.PortsBase > 0 {
		fmt.Fprintf(&b, "Ports: listen only on %d–%d ($PORT is %d); other tasks use the others.\n", k.PortsBase, k.PortsBase+pol.Ports.Span-1, k.PortsBase)
	}
	if k.Issue != nil {
		fmt.Fprintf(&b, "It starts from the issue %s#%d. The task's title (it may be the issue's):\n%s\n", k.Issue.Repo, k.Issue.Number,
			untrusted(p.Host, "the task's title", oneLineTitle(k.Title)))
	}
	if s := strings.TrimSpace(pol.Instructions); s != "" {
		fmt.Fprintf(&b, "\nThe project's instructions:\n%s\n", s)
	}
	if len(pol.Checks) > 0 {
		b.WriteString("\nBefore you push, run:\n")
		for _, c := range pol.Checks {
			fmt.Fprintf(&b, "- `%s`\n", c)
		}
	}
	if s := strings.TrimSpace(pol.PRConventions); s != "" {
		fmt.Fprintf(&b, "\nPull request conventions:\n%s\n", s)
	}
	if setup := setupOutcome(d, k); setup != "" {
		b.WriteString("\n" + setup)
	}
	return strings.TrimRight(b.String(), "\n")
}

// setupOutcome is what the task is told about its repos' setup scripts:
// nothing when none ran or all succeeded; a failure's tail, framed (a
// failed setup doesn't stop the task — it may fix it).
func setupOutcome(d *DB, k *ProjectTask) string {
	var failed []string
	for _, c := range d.checkouts(k.ID) {
		if c.SetupExit != nil && *c.SetupExit != 0 {
			failed = append(failed, fmt.Sprintf("%s (exit %d)", c.Repo, *c.SetupExit))
		}
	}
	if len(failed) == 0 {
		return ""
	}
	s := "The setup script failed for " + strings.Join(failed, ", ") + "; the workspace is there, but may be incomplete."
	if k.SetupTail != "" {
		s += "\n" + untrusted("the setup script's output", "the end of what it printed", k.SetupTail)
	}
	return s + "\n"
}

// harnessBrief is a coding agent's first prompt: the same brief, then the
// task's own text last.
func harnessBrief(d *DB, p *Project, k *ProjectTask, text string) string {
	return projectPrompt(d, p, k, false) + "\n\n# Your task\n" + text
}

// oneLineTitle is a title on one line.
func oneLineTitle(s string) string { return strings.Join(strings.Fields(s), " ") }
