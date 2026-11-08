// projects_coord_research_git.go — a research subagent's read-only git
// (API.md §The coordinator, "Research"; D222). argv only, never a shell, and
// never git's own reading of the argv taken on trust: every argument is
// parsed here against an allowlist per subcommand, the way git parses it —
// an option is one of the subcommand's read-only options by its exact name
// (no abbreviation, no cluster of short flags), a value an option takes is
// attached or the next argument, and that argument may not look like an
// option itself, and a long option's value is always attached (--date=iso)
// — so git and this parser can never disagree about where an option or a
// path starts; everything else — revisions and paths, before and after
// `--` — must stay inside the code it reads: no absolute or ~ path, no ..
// out of the roots, no pathspec magic (:(…), :/). The forms that would
// read the working tree through git's filters are left out: no status, a
// diff compares commits or the index (--cached), blame names its revision.
//
// Git still reads the repository's own config, which may name filter,
// textconv or diff drivers: textconv and external diffs are switched off
// (--no-textconv, --no-ext-diff), so are fsmonitor, hooks, the attributes
// file, signature checks and the pager; what a sandbox writer could plant
// in .git/config beyond that is the residual risk D222 records (that writer
// could already run anything in the sandbox).
package main

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// gitSub is a subcommand research's git runs: its options (true: takes a
// value), the flags given first, and whether -<N> (a count) is taken.
type gitSub struct {
	opts   map[string]bool
	forced []string
	count  bool
}

// gitDiffOpts are the diff output options log, show and diff share.
var gitDiffOpts = map[string]bool{"--stat": false, "--shortstat": false, "--numstat": false, "--name-only": false, "--name-status": false,
	"--summary": false, "--compact-summary": false, "-p": false, "--patch": false, "-s": false, "--no-patch": false, "-U": true, "--unified": true,
	"-w": false, "--ignore-all-space": false, "-b": false, "--ignore-space-change": false, "--ignore-blank-lines": false, "--word-diff": false,
	"--diff-filter": true, "-M": false, "--find-renames": false, "--no-renames": false, "-R": false, "--minimal": false, "--patience": false,
	"--histogram": false, "--color": false, "--no-color": false}

func gitOpts(base map[string]bool, more map[string]bool) map[string]bool {
	out := map[string]bool{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range more {
		out[k] = v
	}
	return out
}

var gitSubs = map[string]gitSub{
	"log": {opts: gitOpts(gitDiffOpts, map[string]bool{"--oneline": false, "--graph": false, "--decorate": false, "--no-decorate": false,
		"--all": false, "--first-parent": false, "--merges": false, "--no-merges": false, "--reverse": false, "--follow": false,
		"--abbrev-commit": false, "-n": true, "--max-count": true, "--skip": true, "--since": true, "--until": true, "--after": true,
		"--before": true, "--author": true, "--committer": true, "--grep": true, "-i": false, "--regexp-ignore-case": false, "-S": true,
		"-G": true, "--pickaxe-all": false, "--format": true, "--pretty": true, "--date": true, "-L": true}),
		forced: []string{"--no-ext-diff", "--no-textconv"}, count: true},
	"show": {opts: gitOpts(gitDiffOpts, map[string]bool{"--oneline": false, "--format": true, "--pretty": true, "--date": true,
		"--abbrev-commit": false, "--decorate": false, "--no-decorate": false}),
		forced: []string{"--no-ext-diff", "--no-textconv"}},
	"diff": {opts: gitOpts(gitDiffOpts, map[string]bool{"--cached": false, "--staged": false, "--merge-base": false}),
		forced: []string{"--no-ext-diff", "--no-textconv"}},
	"blame": {opts: map[string]bool{"-L": true, "-w": false, "-M": false, "-e": false, "--show-email": false, "-s": false, "-l": false,
		"-t": false, "--date": true, "--porcelain": false, "--line-porcelain": false, "-n": false, "--show-number": false, "-f": false,
		"--show-name": false}, forced: []string{"--no-textconv"}},
	"grep": {opts: map[string]bool{"-n": false, "--line-number": false, "-i": false, "--ignore-case": false, "-w": false, "--word-regexp": false,
		"-v": false, "--invert-match": false, "-l": false, "--files-with-matches": false, "-L": false, "--files-without-match": false,
		"--count": false, "-h": false, "-H": false, "--heading": false, "--break": false, "-E": false, "--extended-regexp": false,
		"-F": false, "--fixed-strings": false, "-G": false, "--basic-regexp": false, "-P": false, "--perl-regexp": false, "-e": true,
		"-A": true, "-B": true, "--context": true, "--max-depth": true, "--and": false, "--or": false, "--not": false, "--all-match": false,
		"-o": false, "--only-matching": false, "--column": false, "-m": true, "--max-count": true, "--cached": false, "--full-name": false,
		"-I": false, "-q": false, "--quiet": false}, forced: []string{"--no-textconv"}},
	"ls-files": {opts: map[string]bool{"--cached": false, "--others": false, "-o": false, "--directory": false, "--exclude-standard": false,
		"--full-name": false, "--stage": false, "-s": false, "--deduplicate": false, "--error-unmatch": false}},
}

// gitGlobals come before the subcommand: no pager, no optional locks, and
// what the repository's config could otherwise run switched off.
var gitGlobals = []string{"--no-pager", "--no-optional-locks", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null",
	"-c", "core.attributesFile=/dev/null", "-c", "log.showSignature=false", "-c", "core.pager=cat"}

var gitCountRe = regexp.MustCompile(`^-[0-9]{1,6}$`)

// gitArgv checks a research subagent's git argv (the subcommand first) for
// running in dir, inside roots, and answers the whole command line.
func gitArgv(args []string, dir string, roots []string) ([]string, error) {
	if len(args) == 0 {
		return nil, errors.New(`args: the subcommand first, e.g. ["log", "--oneline", "-20"]`)
	}
	sub, ok := gitSubs[args[0]]
	if !ok {
		return nil, fmt.Errorf("git %s isn't one of research's: log, show, diff, blame, grep or ls-files", clip(args[0], 40))
	}
	var revs, paths []string
	cached, dashes := false, false
	for i := 1; i < len(args); i++ {
		a := args[i]
		if strings.ContainsRune(a, 0) || len(a) > 4096 {
			return nil, errors.New("an argument is malformed")
		}
		if dashes {
			paths = append(paths, a)
			continue
		}
		switch {
		case a == "--":
			dashes = true
		case sub.count && gitCountRe.MatchString(a):
		case strings.HasPrefix(a, "--"):
			name, _, attached := strings.Cut(a, "=")
			takes, known := sub.opts[name]
			switch {
			case !known:
				return nil, fmt.Errorf("git %s %s isn't one of research's read-only options (exact names only)", args[0], clip(name, 60))
			case attached && !takes:
				return nil, fmt.Errorf("%s takes no value", name)
			case takes && !attached: // git takes some long values only attached: never let it read the next as a path
				return nil, fmt.Errorf("%s needs its value attached: %s=…", name, name)
			}
			cached = cached || name == "--cached" || name == "--staged" || name == "--merge-base"
		case strings.HasPrefix(a, "-") && len(a) > 1:
			name := a[:2]
			takes, known := sub.opts[name]
			switch {
			case !known:
				return nil, fmt.Errorf("git %s %s isn't one of research's read-only options (one flag per argument)", args[0], clip(a, 60))
			case len(a) > 2 && !takes:
				return nil, fmt.Errorf("%s: one flag per argument (%s takes no value)", clip(a, 60), name)
			case takes && len(a) == 2:
				if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
					return nil, fmt.Errorf("%s needs a value (one starting with - goes attached: %s-…)", name, name)
				}
				i++
			}
		default:
			revs = append(revs, a)
		}
	}
	for _, p := range append(append([]string(nil), revs...), paths...) {
		if why := gitPathRefused(p, dir, roots); why != "" {
			return nil, errors.New(why)
		}
	}
	switch args[0] {
	case "diff":
		if !cached && len(revs) < 2 && !(len(revs) == 1 && strings.Contains(revs[0], "..")) {
			return nil, errors.New(`research's git diff compares commits (["diff", "A", "B"], ["diff", "A..B"]) or the index (--cached): the working tree's files are read with read and grep`)
		}
	case "blame":
		if !dashes || len(paths) != 1 || len(revs) != 1 {
			return nil, errors.New(`research's git blame names its revision and one file: ["blame", "-L", "10,20", "HEAD", "--", "path/to/file"]`)
		}
	}
	argv := append([]string{"git"}, gitGlobals...)
	argv = append(append(argv, args[0]), sub.forced...)
	return append(argv, args[1:]...), nil
}

// gitPathRefused says why a revision or path argument may not go to git
// ("" = it may): absolute, ~, pathspec magic, or .. out of the roots.
func gitPathRefused(p, dir string, roots []string) string {
	switch {
	case strings.HasPrefix(p, "/"):
		return fmt.Sprintf("%s: an absolute path — research's git reads inside the repository (paths relative to it)", clip(p, 80))
	case strings.HasPrefix(p, "~"):
		return fmt.Sprintf("%s: a home path — research's git reads inside the repository", clip(p, 80))
	case strings.HasPrefix(p, ":"):
		return fmt.Sprintf("%s: pathspec magic and :/ searches aren't taken — name paths plainly", clip(p, 80))
	case !inRoots(path.Join(dir, p), roots):
		return fmt.Sprintf("%s leaves the project's code (..)", clip(p, 80))
	}
	return ""
}

// codeReadGit runs one read-only git command in dir.
func codeReadGit(ctx context.Context, use *sbxUse, dir string, roots []string, raw any) (string, error) {
	var args []string
	list, ok := raw.([]any)
	if !ok {
		return "", errors.New(`args: a list of strings, the subcommand first`)
	}
	for _, x := range list {
		s, ok := x.(string)
		if !ok {
			return "", errors.New("args: strings only")
		}
		args = append(args, s)
	}
	argv, err := gitArgv(args, dir, roots)
	if err != nil {
		return "", err
	}
	res, err := use.Conn.Run(ctx, use.ID, sbxRunReq{Argv: argv, Cwd: dir, Merge: true, TimeoutMs: runTimeout, MaxOutput: 256 << 10,
		Env: map[string]string{"GIT_PAGER": "cat", "PAGER": "cat", "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": "/dev/null",
			"GIT_ATTR_NOSYSTEM": "1", "GIT_TERMINAL_PROMPT": "0", "GIT_NO_REPLACE_OBJECTS": "1"}})
	if err != nil {
		return "", err
	}
	out := ""
	if res.Output != nil {
		out = res.Output.Head
		if res.Output.Elided > 0 || res.Output.Tail != "" {
			out = clip(out, 10<<10) + fmt.Sprintf("\n… (%d bytes left out) …\n", res.Output.Elided) + res.Output.Tail
		}
	}
	out = clip(out, 14<<10)
	code := -1
	if res.ExitCode != nil {
		code = *res.ExitCode
	}
	switch {
	case res.TimedOut:
		return out + "\n[timed out]", nil
	case code != 0:
		return fmt.Sprintf("%s\n[exit %d]", strings.TrimRight(out, "\n"), code), nil
	case strings.TrimSpace(out) == "":
		return "(no output)", nil
	}
	return out, nil
}

// codeReadPattern says why a glob pattern may not be used ("" = it may):
// one that is absolute, starts at a home or climbs out with .. would look
// outside the directory it is given.
func codeReadPattern(what, p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") {
		return fmt.Sprintf("%s: an absolute or home pattern — give path for where to look, and a pattern relative to it", what)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return fmt.Sprintf("%s: .. isn't taken in a pattern — give path for where to look", what)
		}
	}
	return ""
}
