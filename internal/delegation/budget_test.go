package delegation

import (
	"errors"
	"testing"
)

// covers D246 — the context budget a conversation keeps: the setting
// (1 000 000 unless set), never past what the model's window allows — the
// larger of the rule before the setting (60 %, at least 32 000, at most
// 80 %) and the window less 64 000 — and today's 32 000 for an unknown
// window. The agent template's compact.go holds the same numbers
// (TestWsContextBudget there): these vectors are the two's contract.
func TestEffectiveBudget(t *testing.T) {
	for _, c := range []struct{ setting, window, want int }{
		{0, 1_048_576, 984_576},            // the default on a 1M-window model: up to the model's limit, less the margin
		{1_000_000, 1_048_576, 984_576},    // …the same, set
		{1_000_000, 262_144, 198_144},      // a 256k model: its window allows less than the setting
		{1_000_000, 131_072, 78_643},       // below a 160k window, today's 60 %: nothing changes there
		{1_000_000, 32_768, 26_214},        // a small model: today's rule (80 % of a window under the floor)
		{1_000_000, 0, 32_000},             // an unknown window: today's fallback
		{200_000, 1_048_576, 200_000},      // a smaller setting is what is kept
		{40_000, 0, 32_000},                // …never past what an unknown window allows
		{10_000_000, 2_000_000, 1_936_000}, // a larger setting: the window bounds it
	} {
		if got := EffectiveBudget(c.setting, c.window); got != c.want {
			t.Errorf("EffectiveBudget(%d, %d) = %d, want %d", c.setting, c.window, got, c.want)
		}
	}
}

// covers D246 — the setting in force: the default, the admin's, the host's
// preset, never past the host's ceiling; a write past it is refused by name
// and so is one out of range; null is the default again.
func TestContextBudgetSetting(t *testing.T) {
	s := OpenStore(t.TempDir())
	if got, byHost := s.ContextBudget(); got != DefaultContextBudget || byHost {
		t.Fatalf("never set: %d %v", got, byHost)
	}
	n := 400_000
	if _, err := s.SetContextBudget(&n, "user:ada"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ContextBudget(); got != n || s.NetPolicy().ContextBudget != n {
		t.Fatalf("set: %d %+v", got, s.NetPolicy())
	}
	for _, bad := range []int{0, 31_999, 10_000_001} {
		if _, err := s.SetContextBudget(&bad, "user:ada"); err == nil {
			t.Errorf("%d tokens was taken", bad)
		}
	}
	host := HostNet{ContextBudget: 300_000}
	s.SetHost(func() HostNet { return host })
	if got, byHost := s.ContextBudget(); got != 300_000 || !byHost {
		t.Fatalf("the admin's 400 000 past the host's 300 000: %d %v", got, byHost)
	}
	if l := host.Locks()["contextBudget"]; !l.Locked || l.Ceiling != "300000" || l.Reason != HostLockReason {
		t.Errorf("the host's lock: %+v", l)
	}
	var lk *ErrHostLocked
	if _, err := s.SetContextBudget(&n, "user:ada"); !errors.As(err, &lk) || lk.Settings[0] != "contextBudget" {
		t.Errorf("past the host's ceiling: %v", err)
	}
	less := 100_000
	if _, err := s.SetContextBudget(&less, "user:ada"); err != nil {
		t.Fatal(err)
	}
	if got, byHost := s.ContextBudget(); got != less || byHost {
		t.Errorf("under the ceiling, the admin's: %d %v", got, byHost)
	}
	if _, err := s.SetContextBudget(nil, "user:ada"); err != nil {
		t.Fatal(err)
	}
	if got, byHost := s.ContextBudget(); got != 300_000 || !byHost {
		t.Errorf("back to the default under a host preset: the host's, %d %v", got, byHost)
	}
	host = HostNet{}
	if got, _ := s.ContextBudget(); got != DefaultContextBudget {
		t.Errorf("no host, no setting: %d", got)
	}
}
