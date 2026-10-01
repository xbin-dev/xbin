package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func testRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// The guard: with the committed plan and timings, CI's integration shards
// and its jobs run every integration test exactly once, its unit shards
// every package exactly once — under either profile, so a local
// `make integration` covers the same — and ci.yml runs each shard and job
// once, never an unsharded run beside them.
func TestShardsRunEveryTestOnce(t *testing.T) {
	root := testRoot(t)
	p, err := loadPlan(root)
	if err != nil {
		t.Fatal(err)
	}
	tm, err := loadTimings(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range []string{"ci", "local"} {
		if err := checkPartition(root, p, tm, profile, p.Shards); err != nil {
			t.Error(err)
		}
		if err := checkUnitPartition(root, tm, profile, p.UnitShards); err != nil {
			t.Error(err)
		}
		if err := checkAlone(root, p, tm, profile); err != nil {
			t.Error(err)
		}
	}
	if err := checkCI(root, p); err != nil {
		t.Error(err)
	}
}

// Every suite's tests come from its package (the plan lists real ones),
// and a suite's run filter keeps only what it matches.
func TestSuitesList(t *testing.T) {
	root := testRoot(t)
	p, err := loadPlan(root)
	if err != nil {
		t.Fatal(err)
	}
	lists, err := listSuites(root, p)
	if err != nil {
		t.Fatal(err)
	}
	if got := lists["term"]; len(got) == 0 {
		t.Fatal("suite term lists nothing")
	} else {
		for _, n := range got {
			if !strings.HasPrefix(n, "TestConfined") && !strings.HasPrefix(n, "TestTermMountPoints") {
				t.Errorf("suite term holds %s, which its run filter excludes", n)
			}
		}
	}
	if got := lists["isolated-emulate"]; !reflect.DeepEqual(got, []string{"TestVM"}) {
		t.Errorf("isolated-emulate = %v, want [TestVM]", got)
	}
}

// What go test runs and lists: Test/Fuzz with the testing signature (also
// under a renamed import), examples with output; not TestMain, Testing…, a
// wrong signature, a method, or an example without output.
func TestTestNames(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a_test.go", `package a
import "testing"
func TestMain(m *testing.M) {}
func TestA(t *testing.T) {}
func Test(t *testing.T) {}
func Test_b(t *testing.T) {}
func Testing(t *testing.T) {}
func TestWrong(b *testing.B) {}
func TestTwo(t *testing.T, x int) {}
func FuzzF(f *testing.F) {}
func BenchmarkX(b *testing.B) {}
type s struct{}
func (s) TestMethod(t *testing.T) {}
func ExampleRuns() {
	// Output: x
}
func ExampleSilent() {}
`)
	write("b_test.go", `package a_test
import tt "testing"
func TestRenamed(t *tt.T) {}
func ExampleEmpty() {
	// Output:
}
`)
	got, err := testNames(dir, []string{"a_test.go", "b_test.go"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ExampleEmpty", "ExampleRuns", "FuzzF", "Test", "TestA", "TestRenamed", "Test_b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("testNames = %v, want %v", got, want)
	}
}

// The split is deterministic, balances the measured, and a test without a
// time lands by its hash without moving any other.
func TestAssign(t *testing.T) {
	var units []unit
	w := map[string]float64{}
	for i := 0; i < 20; i++ {
		u := unit{"s", fmt.Sprintf("Test%02d", i)}
		units = append(units, u)
		w[u.key()] = float64(i + 1)
	}
	a := assign(units, 4, w, map[string]float64{"s": 5})
	if b := assign(units, 4, w, map[string]float64{"s": 5}); !reflect.DeepEqual(a, b) {
		t.Fatal("two splits of the same input differ")
	}
	loads := make([]float64, 4)
	for i, sh := range a {
		for _, u := range sh {
			loads[i] += w[u.key()]
		}
	}
	lo, hi := loads[0], loads[0]
	for _, l := range loads {
		lo, hi = min(lo, l), max(hi, l)
	}
	if hi-lo > 20 { // the largest single test
		t.Errorf("unbalanced: %v", loads)
	}
	more := append(append([]unit(nil), units...), unit{"s", "TestNew"}, unit{"t", "TestOther"})
	c := assign(more, 4, w, map[string]float64{"s": 5})
	for i := range a {
		var kept []unit
		for _, u := range c[i] {
			if _, ok := w[u.key()]; ok {
				kept = append(kept, u)
			}
		}
		if !reflect.DeepEqual(kept, a[i]) {
			t.Errorf("shard %d changed when unmeasured tests were added: %v → %v", i+1, a[i], kept)
		}
	}
}

// Timings read the shard headers and top-level results, raw or in
// `gh run view --log`'s form, and unit packages' ok lines.
func TestReadTimings(t *testing.T) {
	log := strings.Join([]string{
		"integration 1/4\tRun make integration SHARD=1/4\t\ufeff2026-10-01T09:24:36.1602455Z === testshard: suite test (3 tests, ~40s): go test -tags=integration ./test/",
		"integration 1/4\tRun make integration SHARD=1/4\t2026-10-01T09:24:40.0000000Z --- PASS: TestA (12.50s)",
		"integration 1/4\tRun make integration SHARD=1/4\t2026-10-01T09:24:40.0000000Z     --- PASS: TestA/sub (2.00s)",
		"2026-10-01T09:24:41.0000000Z --- SKIP: TestB (0.00s)",
		"=== testshard: suite broker (1 tests, ~5s): go test ./internal/broker/",
		"--- FAIL: TestC (3.25s)",
		"=== testshard: suite unit (2 tests, ~0s): go test a b",
		"ok  \tgithub.com/xbin-dev/xbin/internal/auth\t12.188s",
		"--- PASS: TestIgnoredInUnit (1.00s)",
	}, "\n")
	got := map[string]float64{}
	if err := readTimings(strings.NewReader(log), got); err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{"test/TestA": 12.5, "test/TestB": 0, "broker/TestC": 3.25, "unit/github.com/xbin-dev/xbin/internal/auth": 12.188}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("readTimings = %v, want %v", got, want)
	}
}

func TestSplitTop(t *testing.T) {
	for _, c := range []struct{ in, top, rest string }{
		{"^(A|B)$/^(x|y)$", "^(A|B)$", "^(x|y)$"},
		{"^A$", "^A$", ""},
		{"[/]x/y", "[/]x", "y"},
		{`a\/b/c`, `a\/b`, "c"},
	} {
		top, rest, _ := splitTop(c.in)
		if top != c.top || rest != c.rest {
			t.Errorf("splitTop(%q) = %q, %q; want %q, %q", c.in, top, rest, c.top, c.rest)
		}
	}
}
