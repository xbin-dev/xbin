package xbin

import (
	"bytes"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// unsetEnv removes key for the rest of the test and restores it afterwards
// (t.Setenv first, so the original value comes back).
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
}

// Partition() is XBIN_PARTITION as xbind set it; PartitionUser() is the
// person of a user partition only. Without the variable — a tile that isn't
// partitioned, or an older xbind — both are "".
func TestPartitionFromEnv(t *testing.T) {
	for _, c := range []struct{ env, part, user string }{
		{"user:alice", "user:alice", "alice"},
		{"user:bob.smith_2", "user:bob.smith_2", "bob.smith_2"},
		{"global", "global", ""},
		{"", "", ""},
	} {
		t.Setenv("XBIN_PARTITION", c.env)
		if got := Partition(); got != c.part {
			t.Errorf("XBIN_PARTITION=%q: Partition() = %q, want %q", c.env, got, c.part)
		}
		if got := PartitionUser(); got != c.user {
			t.Errorf("XBIN_PARTITION=%q: PartitionUser() = %q, want %q", c.env, got, c.user)
		}
	}
	unsetEnv(t, "XBIN_PARTITION")
	if Partition() != "" || PartitionUser() != "" {
		t.Errorf("without XBIN_PARTITION: Partition() = %q, PartitionUser() = %q, want both \"\"", Partition(), PartitionUser())
	}
}

// CallerInfo.Partition and .PartitionID come from X-XBin-Partition and
// X-XBin-Partition-Id; without them every field reads exactly as before, and
// the headers change no other field.
func TestCallerPartition(t *testing.T) {
	r := httptest.NewRequest("GET", "/things", nil)
	r.Header.Set("X-XBin-From", "apps/agent")
	r.Header.Set("X-XBin-Role", "reader")

	today := Caller(r)
	if today != (CallerInfo{From: "apps/agent", Role: "reader"}) {
		t.Errorf("Caller without partition headers = %+v", today)
	}
	if today.Partition != "" || today.PartitionID != "" {
		t.Errorf("partition fields without the headers: %q, %q, want \"\"", today.Partition, today.PartitionID)
	}

	r.Header.Set("X-XBin-Partition", "user:alice")
	r.Header.Set("X-XBin-Partition-Id", "u-0123456789abcdef0123456789abcdef")
	c := Caller(r)
	if c.Partition != "user:alice" || c.PartitionID != "u-0123456789abcdef0123456789abcdef" {
		t.Errorf("Caller = %+v, want Partition user:alice and its id", c)
	}
	c.Partition, c.PartitionID = "", ""
	if c != today {
		t.Errorf("the headers changed another field: %+v, want %+v", c, today)
	}

	// A global instance calling out names its partition and has no id: a
	// provider keying on PartitionID treats it as the tile's one consumer.
	r.Header.Set("X-XBin-Partition", "global")
	r.Header.Del("X-XBin-Partition-Id")
	if g := Caller(r); g.Partition != "global" || g.PartitionID != "" {
		t.Errorf("global caller = %+v, want Partition global and no id", g)
	}
}

// The child half of TestRequirePartitionExits: runs RequirePartition under
// the environment the parent gave it and reports that it returned.
func TestRequirePartitionChild(t *testing.T) {
	if os.Getenv("XBIN_SDK_TEST_REQUIRE_CHILD") != "1" {
		t.Skip("run by TestRequirePartitionExits")
	}
	RequirePartition()
	os.Stdout.WriteString("returned\n")
	os.Exit(0)
}

// RequirePartition fails closed: it returns only for a partition xbind hands
// out ("global", "user:<id>") and otherwise exits 3 with a line on stderr —
// so on an older xbind, which ignores "partition", the backend doesn't run.
func TestRequirePartitionExits(t *testing.T) {
	if os.Getenv("XBIN_SDK_TEST_REQUIRE_CHILD") == "1" {
		t.Skip("the child runs TestRequirePartitionChild")
	}
	run := func(part string, set bool) (int, string, string) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestRequirePartitionChild$")
		var env []string
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "XBIN_PARTITION=") && !strings.HasPrefix(kv, "XBIN_COMPONENT=") {
				env = append(env, kv)
			}
		}
		env = append(env, "XBIN_SDK_TEST_REQUIRE_CHILD=1", "XBIN_COMPONENT=apps/agent")
		if set {
			env = append(env, "XBIN_PARTITION="+part)
		}
		cmd.Env = env
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return code, stdout.String(), stderr.String()
	}

	for _, ok := range []string{"global", "user:alice"} {
		code, out, errOut := run(ok, true)
		if code != 0 || !strings.Contains(out, "returned") {
			t.Errorf("XBIN_PARTITION=%q: exit %d, stdout %q, stderr %q — want RequirePartition to return", ok, code, out, errOut)
		}
	}

	cases := []struct {
		part string
		set  bool
	}{{"", false}, {"", true}, {"user:", true}, {"user", true}, {"org:acme", true}, {"GLOBAL", true}, {"user:a b", true}}
	for _, c := range cases {
		code, out, errOut := run(c.part, c.set)
		if code != 3 {
			t.Errorf("XBIN_PARTITION=%q (set %v): exit %d, stdout %q — want exit 3", c.part, c.set, code, out)
		}
		if strings.Contains(out, "returned") {
			t.Errorf("XBIN_PARTITION=%q (set %v): RequirePartition returned", c.part, c.set)
		}
		if !strings.Contains(errOut, "apps/agent must run as a partition") || !strings.Contains(errOut, "/docs/partitions.md") {
			t.Errorf("XBIN_PARTITION=%q (set %v): stderr %q, want the reason and the docs", c.part, c.set, errOut)
		}
	}
}

// GlobalURL adds ?xbin-partition=global only in a user partition; elsewhere
// it is the plain URL of the path on this tile.
func TestGlobalURL(t *testing.T) {
	t.Setenv("XBIN_COMPONENT", "apps/agent")

	t.Setenv("XBIN_PARTITION", "user:alice")
	for path, want := range map[string]string{
		"runs/42":      "http://xbin/api/apps/agent/runs/42?xbin-partition=global",
		"/runs/42":     "http://xbin/api/apps/agent/runs/42?xbin-partition=global",
		"runs?since=4": "http://xbin/api/apps/agent/runs?since=4&xbin-partition=global",
		"doc#top":      "http://xbin/api/apps/agent/doc?xbin-partition=global#top",
		"doc?a=1#x?y":  "http://xbin/api/apps/agent/doc?a=1&xbin-partition=global#x?y",
		"":             "http://xbin/api/apps/agent/?xbin-partition=global",
	} {
		if got := GlobalURL(path); got != want {
			t.Errorf("in user:alice, GlobalURL(%q) = %q, want %q", path, got, want)
		}
	}

	for _, part := range []string{"global", ""} {
		t.Setenv("XBIN_PARTITION", part)
		if got, want := GlobalURL("/runs?since=4"), "http://xbin/api/apps/agent/runs?since=4"; got != want {
			t.Errorf("XBIN_PARTITION=%q: GlobalURL = %q, want the plain URL %q", part, got, want)
		}
	}
	unsetEnv(t, "XBIN_PARTITION")
	if got, want := GlobalURL("runs"), "http://xbin/api/apps/agent/runs"; got != want {
		t.Errorf("without XBIN_PARTITION: GlobalURL = %q, want %q", got, want)
	}
}
