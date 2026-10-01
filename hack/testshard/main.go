// Command testshard runs the integration suites (hack/integration.jsonc)
// and the unit packages split into shards, so CI runs them on parallel
// runners and `make integration` runs them concurrently; every test runs
// exactly once across the shards (docs/maintenance.md → "Integration
// shards").
//
//	testshard run [-shard i/N | -shard <job>]   # make integration [SHARD=…]: all shards and jobs at once without -shard
//	testshard unit -shard i/N                   # make test SHARD=i/N
//	testshard list [-shard …] [-unit]           # what a shard runs, without running it
//	testshard verify                            # CI's guard: the listings agree with go test -list, every test in exactly one shard
//	testshard timings [-profile p] log…         # refresh hack/integration-timings.json from shard logs
//
// Tests are balanced by hack/integration-timings.json's profile for this
// host — "ci" when $CI is set, else "local" (-profile overrides); a test it
// has no time for goes to the shard its name hashes to, so adding one moves
// nothing else.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	root, err := repoRoot()
	if err != nil {
		fatal(err)
	}
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "run":
		err = cmdRun(root, args)
	case "unit":
		err = cmdUnit(root, args)
	case "list":
		err = cmdList(root, args)
	case "verify":
		err = cmdVerify(root, args)
	case "timings":
		err = cmdTimings(root, args)
	default:
		usage()
	}
	if err != nil {
		fatal(err)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: testshard run|unit|list|verify|timings [flags] (see hack/testshard/main.go)")
	os.Exit(2)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "testshard:", err)
	os.Exit(1)
}

// repoRoot is the directory holding hack/integration.jsonc: the working
// directory or one above it.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, planFile)); err == nil {
			return d, nil
		}
		if d == filepath.Dir(d) {
			return "", fmt.Errorf("no %s in %s or above", planFile, dir)
		}
	}
}

func defaultProfile() string {
	if os.Getenv("CI") != "" {
		return "ci"
	}
	return "local"
}

// step is one go test of a shard.
type step struct {
	name   string   // the suite, or "unit"
	env    []string // added to the environment
	args   []string // go's arguments
	tests  int
	expect float64
}

func (s step) String() string {
	q := make([]string, 0, len(s.env)+len(s.args)+1)
	q = append(q, s.env...)
	q = append(q, "go")
	for _, a := range s.args {
		if strings.ContainsAny(a, " |()[]^$*?\\'\"") {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		q = append(q, a)
	}
	return strings.Join(q, " ")
}

// suiteSteps are the go tests that run units (one shard's, or a job's), in
// plan order.
func suiteSteps(p *plan, units []unit, weight map[string]float64, execWrap string) []step {
	by := map[string][]string{}
	for _, u := range units {
		by[u.suite] = append(by[u.suite], u.test)
	}
	var steps []step
	for _, s := range p.Suites {
		tests := by[s.Name]
		if len(tests) == 0 {
			continue
		}
		args := []string{"test", "-tags=integration", "-count=1", "-v"}
		if s.Timeout != "" {
			args = append(args, "-timeout", s.Timeout)
		}
		if s.Delegated && execWrap != "" {
			args = append(args, "-exec", execWrap)
		}
		quoted := make([]string, len(tests))
		exp := s.Overhead
		for i, t := range tests {
			quoted[i] = regexp.QuoteMeta(t)
			exp += weight[s.Name+"/"+t]
		}
		args = append(args, "-run", "^("+strings.Join(quoted, "|")+")$")
		if s.Skip != "" {
			args = append(args, "-skip", s.Skip)
		}
		args = append(args, s.Pkg)
		steps = append(steps, step{name: s.Name, env: s.Env, args: args, tests: len(tests), expect: exp})
	}
	return steps
}

// shardFlags are run's and list's: which shard, the profile, the
// delegating -exec.
type shardFlags struct {
	shard, profile, execWrap, logs string
	unit                           bool
}

func parseShardFlags(name string, args []string) (shardFlags, error) {
	var f shardFlags
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.StringVar(&f.shard, "shard", "", "i/N, a job name, or empty for all")
	fs.StringVar(&f.profile, "profile", defaultProfile(), "timing profile")
	fs.StringVar(&f.execWrap, "exec", "", "go test -exec for the delegated suites (empty: none; they skip)")
	fs.BoolVar(&f.unit, "unit", false, "list: the unit shards")
	fs.StringVar(&f.logs, "logs", "", "run without -shard: the shards' logs go here and stay (default: a temp dir, removed when green)")
	if err := fs.Parse(args); err != nil {
		return f, err
	}
	if fs.NArg() > 0 {
		return f, fmt.Errorf("%s: unexpected arguments %q", name, fs.Args())
	}
	return f, nil
}

// planned is every shard's and job's steps: "1/N"…"N/N", then each job.
// Without -shard (every shard at once) the plan's alone tests leave their
// shards for after: the steps run once the shards are done.
func planned(root string, p *plan, t timings, f shardFlags) ([]string, map[string][]step, []step, error) {
	n := p.Shards
	if f.shard != "" && strings.Contains(f.shard, "/") {
		var err error
		if _, n, err = parseShard(f.shard); err != nil {
			return nil, nil, nil, err
		}
	}
	shards, _, err := integrationShards(root, p, t, f.profile, n)
	if err != nil {
		return nil, nil, nil, err
	}
	w := floor(t[f.profile])
	var (
		names []string
		alone []unit
	)
	out := map[string][]step{}
	for i, sh := range shards {
		k := fmt.Sprintf("%d/%d", i+1, n)
		names = append(names, k)
		if f.shard == "" {
			sh, alone = splitAlone(p, sh, alone)
		}
		out[k] = suiteSteps(p, sh, w, f.execWrap)
	}
	lists, err := listSuites(root, p)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, j := range p.jobs() {
		var units []unit
		for _, s := range p.Suites {
			if s.Job == j {
				for _, t := range lists[s.Name] {
					units = append(units, unit{s.Name, t})
				}
			}
		}
		names = append(names, j)
		out[j] = suiteSteps(p, units, w, f.execWrap)
	}
	if f.shard != "" {
		if _, ok := out[f.shard]; !ok {
			return nil, nil, nil, fmt.Errorf("no shard or job %q (have %s)", f.shard, strings.Join(names, ", "))
		}
		return []string{f.shard}, out, nil, nil
	}
	sort.Slice(alone, func(i, j int) bool { return alone[i].key() < alone[j].key() })
	return names, out, suiteSteps(p, alone, w, f.execWrap), nil
}

// splitAlone moves sh's alone units onto alone; returns the rest of sh.
func splitAlone(p *plan, sh, alone []unit) ([]unit, []unit) {
	var keep []unit
	for _, u := range sh {
		if p.alone(u.key()) {
			alone = append(alone, u)
		} else {
			keep = append(keep, u)
		}
	}
	return keep, alone
}

func cmdList(root string, args []string) error {
	f, err := parseShardFlags("list", args)
	if err != nil {
		return err
	}
	p, err := loadPlan(root)
	if err != nil {
		return err
	}
	t, err := loadTimings(root)
	if err != nil {
		return err
	}
	if f.unit {
		return listUnit(root, p, t, f)
	}
	names, steps, after, err := planned(root, p, t, f)
	if err != nil {
		return err
	}
	if len(after) > 0 {
		names, steps["after the shards, alone"] = append(names, "after the shards, alone"), after
	}
	for _, k := range names {
		total, tests := 0.0, 0
		for _, s := range steps[k] {
			total += s.expect
			tests += s.tests
		}
		fmt.Printf("# %s: %d tests, ~%.0fs (%s profile)\n", k, tests, total, f.profile)
		for _, s := range steps[k] {
			fmt.Printf("%s\n", s)
		}
	}
	return nil
}

func listUnit(root string, p *plan, t timings, f shardFlags) error {
	n := p.UnitShards
	if f.shard != "" {
		var err error
		if _, n, err = parseShard(f.shard); err != nil {
			return err
		}
	}
	shards, _, err := unitShards(root, t, f.profile, n)
	if err != nil {
		return err
	}
	w := floor(t[f.profile])
	for i, sh := range shards {
		k := fmt.Sprintf("%d/%d", i+1, n)
		if f.shard != "" && f.shard != k {
			continue
		}
		total := 0.0
		for _, pkg := range sh {
			total += w["unit/"+pkg]
		}
		fmt.Printf("# unit %s: %d packages, ~%.0fs of package time (%s profile)\n%s\n", k, len(sh), total, f.profile, strings.Join(sh, "\n"))
	}
	return nil
}

// result is one step's outcome.
type result struct {
	step
	err     error
	elapsed time.Duration
}

// runSteps runs steps one after another, all of them whatever fails, into
// out, with a header before each (what timings reads) and a summary after.
func runSteps(root, label string, steps []step, out io.Writer) []result {
	var res []result
	for _, s := range steps {
		fmt.Fprintf(out, "=== testshard: suite %s (%d tests, ~%.0fs): %s\n", s.name, s.tests, s.expect, s)
		cmd := exec.Command("go", s.args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), s.env...)
		cmd.Stdout, cmd.Stderr = out, out
		start := time.Now()
		err := cmd.Run()
		r := result{step: s, err: err, elapsed: time.Since(start)}
		res = append(res, r)
		verdict := "ok"
		if err != nil {
			verdict = "FAIL (" + err.Error() + ")"
		}
		fmt.Fprintf(out, "=== testshard: suite %s: %s in %.0fs\n", s.name, verdict, r.elapsed.Seconds())
	}
	fmt.Fprintf(out, "=== testshard: %s summary\n", label)
	for _, r := range res {
		v := "ok  "
		if r.err != nil {
			v = "FAIL"
		}
		fmt.Fprintf(out, "    %s %-17s %3d tests %5.0fs (expected ~%.0fs)\n", v, r.name, r.tests, r.elapsed.Seconds(), r.expect)
	}
	return res
}

func stepTests(steps []step) int {
	n := 0
	for _, s := range steps {
		n += s.tests
	}
	return n
}

func failed(res []result) []string {
	var out []string
	for _, r := range res {
		if r.err != nil {
			out = append(out, r.name)
		}
	}
	return out
}

func cmdRun(root string, args []string) error {
	f, err := parseShardFlags("run", args)
	if err != nil {
		return err
	}
	p, err := loadPlan(root)
	if err != nil {
		return err
	}
	t, err := loadTimings(root)
	if err != nil {
		return err
	}
	names, steps, after, err := planned(root, p, t, f)
	if err != nil {
		return err
	}
	if f.shard != "" {
		if bad := failed(runSteps(root, "shard "+f.shard, steps[f.shard], os.Stdout)); len(bad) > 0 {
			return fmt.Errorf("shard %s: failed: %s", f.shard, strings.Join(bad, ", "))
		}
		return nil
	}
	return runAll(root, names, steps, after, f.logs)
}

// runAll runs every shard and job at once, each into a log file of its own
// (in logs, kept; else a temp dir, removed when all pass), and reports each
// as it ends; then the after steps, by themselves, into after.log; the
// failures' FAIL lines and tails at the end.
func runAll(root string, names []string, steps map[string][]step, after []step, logs string) error {
	dir, keep := logs, logs != ""
	var err error
	if keep {
		err = os.MkdirAll(dir, 0o755)
	} else {
		dir, err = os.MkdirTemp("", "xbin-integration-")
	}
	if err != nil {
		return err
	}
	fmt.Printf("make integration: %d shards and jobs at once, logs in %s\n", len(names), dir)
	start := time.Now()
	var (
		mu  sync.Mutex
		bad []string
		wg  sync.WaitGroup
	)
	logOf := func(k string) string { return filepath.Join(dir, strings.ReplaceAll(k, "/", "-of-")+".log") }
	for _, k := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lf, err := os.Create(logOf(k))
			if err != nil {
				mu.Lock()
				bad = append(bad, k+": "+err.Error())
				mu.Unlock()
				return
			}
			res := runSteps(root, k, steps[k], lf)
			_ = lf.Close()
			mu.Lock()
			defer mu.Unlock()
			if f := failed(res); len(f) > 0 {
				bad = append(bad, k)
				fmt.Printf("  %-6s FAIL (%s) after %.0fs — %s\n", k, strings.Join(f, ", "), time.Since(start).Seconds(), logOf(k))
			} else {
				fmt.Printf("  %-6s ok after %.0fs\n", k, time.Since(start).Seconds())
			}
		}()
	}
	wg.Wait()
	if len(after) > 0 {
		k := "after"
		fmt.Printf("  then %d tests by themselves (the plan's alone: latency budgets)\n", stepTests(after))
		lf, err := os.Create(logOf(k))
		if err != nil {
			return err
		}
		res := runSteps(root, "after the shards, alone", after, lf)
		_ = lf.Close()
		if f := failed(res); len(f) > 0 {
			bad = append(bad, k)
			fmt.Printf("  %-6s FAIL (%s) after %.0fs — %s\n", k, strings.Join(f, ", "), time.Since(start).Seconds(), logOf(k))
		} else {
			fmt.Printf("  %-6s ok after %.0fs\n", k, time.Since(start).Seconds())
		}
	}
	if len(bad) == 0 {
		fmt.Printf("make integration: green in %.0fs\n", time.Since(start).Seconds())
		if !keep {
			_ = os.RemoveAll(dir)
		}
		return nil
	}
	sort.Strings(bad)
	for _, k := range bad {
		fmt.Printf("\n===== %s (%s): failures and tail\n", k, logOf(k))
		printFailures(logOf(k))
	}
	return fmt.Errorf("failed: %s (logs in %s)", strings.Join(bad, ", "), dir)
}

// printFailures prints a log's --- FAIL lines and its last 40 lines.
func printFailures(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Println(err)
		return
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	for _, l := range lines {
		if strings.Contains(l, "--- FAIL") || strings.HasPrefix(l, "FAIL") || strings.HasPrefix(l, "panic:") {
			fmt.Println(l)
		}
	}
	fmt.Println("…")
	for _, l := range lines[max(0, len(lines)-40):] {
		fmt.Println(l)
	}
}

func cmdUnit(root string, args []string) error {
	fs := flag.NewFlagSet("unit", flag.ContinueOnError)
	shard := fs.String("shard", "", "i/N")
	profile := fs.String("profile", defaultProfile(), "timing profile")
	if err := fs.Parse(args); err != nil {
		return err
	}
	i, n, err := parseShard(*shard)
	if err != nil {
		return err
	}
	t, err := loadTimings(root)
	if err != nil {
		return err
	}
	shards, _, err := unitShards(root, t, *profile, n)
	if err != nil {
		return err
	}
	pkgs := shards[i-1]
	if len(pkgs) == 0 {
		fmt.Printf("unit %s: no packages\n", *shard)
		return nil
	}
	s := step{name: "unit", args: append([]string{"test"}, pkgs...), tests: len(pkgs)}
	if bad := failed(runSteps(root, "unit "+*shard, []step{s}, os.Stdout)); len(bad) > 0 {
		return fmt.Errorf("unit %s failed", *shard)
	}
	return nil
}

// cmdTimings merges the top-level test times (and unit package times) in
// shard logs — raw, or as `gh run view --log` prints them — into the
// timings file's profile.
func cmdTimings(root string, args []string) error {
	fs := flag.NewFlagSet("timings", flag.ContinueOnError)
	profile := fs.String("profile", "ci", "timing profile to update")
	if err := fs.Parse(args); err != nil {
		return err
	}
	t, err := loadTimings(root)
	if err != nil {
		return err
	}
	if t[*profile] == nil {
		t[*profile] = map[string]float64{}
	}
	got := map[string]float64{}
	for _, path := range fs.Args() {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		err = readTimings(f, got)
		_ = f.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	for k, v := range got {
		t[*profile][k] = v
	}
	// drop what no longer exists: a removed test or suite, a gone package
	p, err := loadPlan(root)
	if err != nil {
		return err
	}
	lists, err := listSuites(root, p)
	if err != nil {
		return err
	}
	pkgs, err := unitPackages(root)
	if err != nil {
		return err
	}
	live := map[string]bool{}
	for s, names := range lists {
		for _, n := range names {
			live[s+"/"+n] = true
		}
	}
	for _, pkg := range pkgs {
		live["unit/"+pkg] = true
	}
	pruned := 0
	for _, prof := range t {
		for k := range prof {
			if !live[k] {
				delete(prof, k)
				pruned++
			}
		}
	}
	if err := writeTimings(root, t); err != nil {
		return err
	}
	fmt.Printf("%s: %d times into the %q profile (%d entries); %d gone tests or packages dropped\n", timingsFile, len(got), *profile, len(t[*profile]), pruned)
	return nil
}

var (
	ghPrefix   = regexp.MustCompile(`^[^\t]*\t[^\t]*\t\x{feff}?(\d{4}-\d\d-\d\dT[0-9:.]+Z) ?`)
	tsPrefix   = regexp.MustCompile(`^\x{feff}?\d{4}-\d\d-\d\dT[0-9:.]+Z ?`)
	headerLine = regexp.MustCompile(`^=== testshard: suite (\S+) \(`)
	resultLine = regexp.MustCompile(`^--- (?:PASS|FAIL|SKIP): (\S+) \(([0-9.]+)s\)`)
	pkgLine    = regexp.MustCompile(`^(?:ok|FAIL)\s+(\S+)\s+([0-9.]+)s`)
)

// readTimings reads one log into got: suite/Test → seconds, and
// unit/<package> → seconds.
func readTimings(r io.Reader, got map[string]float64) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	cur := ""
	for sc.Scan() {
		l := sc.Text()
		if m := ghPrefix.FindStringIndex(l); m != nil {
			l = l[m[1]:]
		} else if m := tsPrefix.FindStringIndex(l); m != nil {
			l = l[m[1]:]
		}
		if m := headerLine.FindStringSubmatch(l); m != nil {
			cur = m[1]
			continue
		}
		if cur == "" {
			continue
		}
		var secs float64
		if m := resultLine.FindStringSubmatch(l); m != nil && cur != "unit" {
			if _, err := fmt.Sscan(m[2], &secs); err == nil {
				got[cur+"/"+m[1]] = secs
			}
		} else if m := pkgLine.FindStringSubmatch(l); m != nil && cur == "unit" {
			if _, err := fmt.Sscan(m[2], &secs); err == nil {
				got["unit/"+m[1]] = secs
			}
		}
	}
	return sc.Err()
}

// writeTimings writes the file sorted, two decimals, one entry per line.
func writeTimings(root string, t timings) error {
	var b strings.Builder
	b.WriteString("{\n")
	profiles := make([]string, 0, len(t))
	for p := range t {
		profiles = append(profiles, p)
	}
	sort.Strings(profiles)
	for i, p := range profiles {
		fmt.Fprintf(&b, "  %q: {\n", p)
		keys := make([]string, 0, len(t[p]))
		for k := range t[p] {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for j, k := range keys {
			sep := ","
			if j == len(keys)-1 {
				sep = ""
			}
			fmt.Fprintf(&b, "    %q: %.2f%s\n", k, t[p][k], sep)
		}
		sep := ","
		if i == len(profiles)-1 {
			sep = ""
		}
		fmt.Fprintf(&b, "  }%s\n", sep)
	}
	b.WriteString("}\n")
	return os.WriteFile(filepath.Join(root, timingsFile), []byte(b.String()), 0o644)
}
