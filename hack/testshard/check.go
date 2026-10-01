package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ciFile is the workflow that runs the shards.
const ciFile = ".github/workflows/ci.yml"

// checkPartition: with n shards and the profile's timings, every test of
// every suite runs exactly once — in one shard, or in its suite's job —
// and every suite has a test (a run filter that matches nothing is a
// mistake, not an empty suite). It recomputes the shards from scratch, as
// a CI shard does.
func checkPartition(root string, p *plan, t timings, profile string, n int) error {
	shards, lists, err := integrationShards(root, p, t, profile, n)
	if err != nil {
		return err
	}
	var problems []string
	seen := map[string]int{}
	for i, sh := range shards {
		for _, u := range sh {
			if prev, ok := seen[u.key()]; ok {
				problems = append(problems, fmt.Sprintf("%s is in shards %d and %d", u.key(), prev+1, i+1))
			}
			seen[u.key()] = i
		}
	}
	for _, s := range p.Suites {
		if len(lists[s.Name]) == 0 {
			problems = append(problems, fmt.Sprintf("suite %s (%s, run %q) has no tests", s.Name, s.Pkg, s.Run))
		}
		for _, name := range lists[s.Name] {
			k := s.Name + "/" + name
			_, sharded := seen[k]
			switch {
			case s.Job == "" && !sharded:
				problems = append(problems, k+" is in no shard")
			case s.Job != "" && sharded:
				problems = append(problems, k+" is in a shard and in job "+s.Job)
			}
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("the %d shards (%s profile) don't run every test exactly once:\n  %s", n, profile, strings.Join(problems, "\n  "))
	}
	return nil
}

// checkAlone: each of the plan's alone patterns names a sharded suite's
// test, and a local run of every shard at once (which moves those tests
// after the shards) still runs every sharded test exactly once.
func checkAlone(root string, p *plan, t timings, profile string) error {
	shards, _, err := integrationShards(root, p, t, profile, p.Shards)
	if err != nil {
		return err
	}
	var problems []string
	seen := map[string]int{}
	var alone []unit
	matched := make([]bool, len(p.Alone))
	for _, sh := range shards {
		var keep []unit
		keep, alone = splitAlone(p, sh, alone)
		for _, u := range keep {
			seen[u.key()]++
		}
	}
	for _, u := range alone {
		seen[u.key()]++
		for i, a := range p.Alone {
			if regexp.MustCompile(a).MatchString(u.key()) {
				matched[i] = true
			}
		}
	}
	for _, sh := range shards {
		for _, u := range sh {
			if seen[u.key()] != 1 {
				problems = append(problems, fmt.Sprintf("%s runs %d times", u.key(), seen[u.key()]))
			}
		}
	}
	for i, a := range p.Alone {
		if !matched[i] {
			problems = append(problems, fmt.Sprintf("alone %q matches no sharded test", a))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("every shard at once, the alone tests after (%s profile):\n  %s", profile, strings.Join(problems, "\n  "))
	}
	return nil
}

// checkUnitPartition: every unit package in exactly one of n shards.
func checkUnitPartition(root string, t timings, profile string, n int) error {
	shards, pkgs, err := unitShards(root, t, profile, n)
	if err != nil {
		return err
	}
	count := map[string]int{}
	for _, sh := range shards {
		for _, pkg := range sh {
			count[pkg]++
		}
	}
	var problems []string
	for _, pkg := range pkgs {
		if count[pkg] != 1 {
			problems = append(problems, fmt.Sprintf("%s runs in %d unit shards", pkg, count[pkg]))
		}
	}
	if len(count) != len(pkgs) {
		problems = append(problems, fmt.Sprintf("the unit shards name %d packages, go list %d", len(count), len(pkgs)))
	}
	if len(pkgs) == 0 {
		problems = append(problems, "go list found no unit packages")
	}
	if len(problems) > 0 {
		return fmt.Errorf("the %d unit shards (%s profile):\n  %s", n, profile, strings.Join(problems, "\n  "))
	}
	return nil
}

var (
	ciIntegration = regexp.MustCompile(`make integration( SHARD=[^\s"'},]+)?`)
	ciUnit        = regexp.MustCompile(`make (test|check)( SHARD=[^\s"'},]+)?`)
)

// checkCI: ci.yml runs `make integration SHARD=s` for every shard 1/N…N/N
// and every job — each once — and `make test SHARD=i/M` for every unit
// shard, and never an unsharded `make integration`, `make test` or `make
// check` (which would run tests twice, or miss the split's guarantee).
func checkCI(root string, p *plan) error {
	b, err := os.ReadFile(filepath.Join(root, ciFile))
	if err != nil {
		return err
	}
	var lines []string
	for _, l := range strings.Split(string(b), "\n") {
		if t := strings.TrimSpace(l); !strings.HasPrefix(t, "#") {
			lines = append(lines, l)
		}
	}
	text := strings.Join(lines, "\n")
	want := map[string]int{}
	for i := 1; i <= p.Shards; i++ {
		want[fmt.Sprintf("integration SHARD=%d/%d", i, p.Shards)] = 0
	}
	for _, j := range p.jobs() {
		want["integration SHARD="+j] = 0
	}
	for i := 1; i <= p.UnitShards; i++ {
		want[fmt.Sprintf("test SHARD=%d/%d", i, p.UnitShards)] = 0
	}
	var problems []string
	for _, m := range ciIntegration.FindAllStringSubmatch(text, -1) {
		if m[1] == "" {
			problems = append(problems, "an unsharded `make integration`")
			continue
		}
		k := "integration" + m[1]
		if _, ok := want[k]; !ok {
			problems = append(problems, "`make "+k+"`, which the plan doesn't have")
			continue
		}
		want[k]++
	}
	for _, m := range ciUnit.FindAllStringSubmatch(text, -1) {
		if m[1] == "check" || m[2] == "" {
			problems = append(problems, "an unsharded `make "+m[1]+"`")
			continue
		}
		k := m[1] + m[2]
		if _, ok := want[k]; !ok {
			problems = append(problems, "`make "+k+"`, which the plan doesn't have")
			continue
		}
		want[k]++
	}
	for k, n := range want {
		if n != 1 {
			problems = append(problems, fmt.Sprintf("`make %s` appears %d times, want once", k, n))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("%s doesn't run %s's shards exactly once each:\n  %s", ciFile, planFile, strings.Join(problems, "\n  "))
	}
	return nil
}

// checkGoTestList: for every suite package, the tests testNames finds are
// the ones `go test -list` prints (Benchmarks aside). This compiles each
// test binary and runs its TestMain, so it is CI's guard job's, not a unit
// test's.
func checkGoTestList(root string, p *plan) error {
	listed, err := goList(root, "integration", suitePkgs(p)...)
	if err != nil {
		return err
	}
	var problems []string
	for _, lp := range listed {
		mine, err := testNames(lp.Dir, append(append([]string(nil), lp.TestGoFiles...), lp.XTestGoFiles...))
		if err != nil {
			return err
		}
		cmd := exec.Command("go", "test", "-tags=integration", "-count=1", "-list", ".", lp.ImportPath)
		cmd.Dir = root
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("go test -list %s: %v\n%s%s", lp.ImportPath, err, out, stderr.String())
		}
		var theirs []string
		for _, l := range strings.Split(string(out), "\n") {
			l = strings.TrimSpace(l)
			if l == "" || strings.HasPrefix(l, "ok ") || strings.HasPrefix(l, "ok\t") || strings.HasPrefix(l, "Benchmark") || strings.ContainsAny(l, " \t") {
				continue
			}
			theirs = append(theirs, l)
		}
		sort.Strings(theirs)
		if strings.Join(mine, " ") != strings.Join(theirs, " ") {
			problems = append(problems, fmt.Sprintf("%s: testshard lists %v,\n    go test -list %v", lp.ImportPath, mine, theirs))
		} else {
			fmt.Printf("ok   %-48s %3d tests, as go test -list\n", lp.ImportPath, len(mine))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("the source listing disagrees with go test -list:\n  %s", strings.Join(problems, "\n  "))
	}
	return nil
}

func suitePkgs(p *plan) []string {
	var pkgs []string
	seen := map[string]bool{}
	for _, s := range p.Suites {
		if !seen[s.Pkg] {
			seen[s.Pkg] = true
			pkgs = append(pkgs, s.Pkg)
		}
	}
	return pkgs
}

// cmdVerify is CI's guard job: the partition and ci.yml checks the unit
// tests make, for both profiles, and the listings against go test -list.
func cmdVerify(root string, _ []string) error {
	p, err := loadPlan(root)
	if err != nil {
		return err
	}
	t, err := loadTimings(root)
	if err != nil {
		return err
	}
	for _, profile := range []string{"ci", "local"} {
		if err := checkPartition(root, p, t, profile, p.Shards); err != nil {
			return err
		}
		if err := checkUnitPartition(root, t, profile, p.UnitShards); err != nil {
			return err
		}
		if err := checkAlone(root, p, t, profile); err != nil {
			return err
		}
	}
	if err := checkCI(root, p); err != nil {
		return err
	}
	fmt.Printf("ok   %d integration shards + %v, %d unit shards: every test and package exactly once; ci.yml runs each\n",
		p.Shards, p.jobs(), p.UnitShards)
	return checkGoTestList(root, p)
}
