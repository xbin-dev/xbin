package broker

import (
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/util"
)

// covers D127i D127t PO-2 — the data plane honours a namespace's hold and write
// gate (08-data §8.2, §8.5): while an act holds apps/calendar's dev
// namespace, dev's kv reads and writes, blob requests and bus publishes
// answer 503 with Retry-After, and dev may not start, while main's
// namespace answers as before; an API write waits on the write gate an act
// holds, and past the wait answers 503; a namespace left partial keeps its
// deployment from starting; once released, everything passes again, and
// nothing of dev's mounts while it is held. A tile
// without deployments never reads namespace metadata to start.
func TestDataPlaneHonoursNamespaceHold(t *testing.T) {
	b, f := newNSBroker(t)
	if b.nsHeld(fxCalendar, "apps/calendar", "dev") {
		t.Fatal("a tile without deployments is held")
	}
	runs := nsFakeVolumes(t, b)
	f.set(t, b, fxCalendar, util.MainDeployment, map[string]string{util.MainDeployment: "", "dev": nsCheckpoint(t, calDevScope)})
	id := nsOf("apps/calendar", "dev")
	release, err := b.holdNS(id, nsResetting)
	if err != nil {
		t.Fatal(err)
	}
	for what, code := range map[string]int{
		"dev get":     codeOf(nsKV(t, b, "GET", calDev, "res:apps/calendar/events/k", "")),
		"dev put":     codeOf(nsKV(t, b, "PUT", calDev, "res:apps/calendar/events/k", "x")),
		"dev delete":  codeOf(nsKV(t, b, "DELETE", calDev, "res:apps/calendar/events/k", "")),
		"dev blob":    zeroDataCall(t, b.apiBlobGet, "GET", "res:apps/calendar/pics/a", "", calDev).Code,
		"dev publish": zeroDataCall(t, b.apiBusPublish, "POST", "", `{"resource":"res:apps/calendar/bus","topic":"t"}`, calDev).Code,
	} {
		if code != 503 {
			t.Errorf("%s while dev's data is reset: %d, want 503", what, code)
		}
	}
	w := zeroDataCall(t, b.apiKVGet, "GET", "res:apps/calendar/events/k", "", calDev)
	if w.Header().Get("Retry-After") == "" || !strings.Contains(w.Body.String(), "dev's data is being reset") {
		t.Errorf("the hold's 503 is %q, Retry-After %q", w.Body.String(), w.Header().Get("Retry-After"))
	}
	if c, body := nsKV(t, b, "PUT", calMain, "res:apps/calendar/events/k", "main"); c != 200 {
		t.Errorf("main's put while dev is held: %d %s", c, body)
	}
	if !b.DeploymentEncryptionHold(fxCalendar, "dev") {
		t.Error("dev may start while its namespace is held")
	}
	if log := runs(); strings.Contains(log, ".deployments") {
		t.Errorf("dev's volumes were touched while held:\n%s", log)
	}
	release()
	if b.DeploymentEncryptionHold(fxCalendar, "dev") {
		t.Error("dev may not start once released")
	}
	if c, body := nsKV(t, b, "PUT", calDev, "res:apps/calendar/events/k", "dev"); c != 200 {
		t.Fatalf("dev's put once released: %d %s", c, body)
	}

	gate := b.nsTab().gate(id)
	gate.Lock() // an act reading or erasing dev's data
	done := make(chan int, 1)
	go func() { done <- codeOf(nsKV(t, b, "PUT", calDev, "res:apps/calendar/events/k", "later")) }()
	select {
	case c := <-done:
		t.Fatalf("a write passed the closed gate: %d", c)
	case <-time.After(100 * time.Millisecond):
	}
	if c, _ := nsKV(t, b, "GET", calDev, "res:apps/calendar/events/k", ""); c != 200 {
		t.Errorf("a read behind the closed gate: %d", c)
	}
	gate.Unlock()
	if c := <-done; c != 200 {
		t.Errorf("the waiting write, once the gate opened: %d", c)
	}
	prev := nsWriteWait
	nsWriteWait = 50 * time.Millisecond
	t.Cleanup(func() { nsWriteWait = prev })
	gate.Lock()
	c := codeOf(nsKV(t, b, "PUT", calDev, "res:apps/calendar/events/k", "late"))
	gate.Unlock()
	if c != 503 {
		t.Errorf("a write past the gate's wait: %d, want 503", c)
	}

	if err := b.updateNS(id, true, func(m *nsMeta) { m.State, m.Failed, m.Step = nsPartial, "seed", "kv" }); err != nil {
		t.Fatal(err)
	}
	if !b.DeploymentEncryptionHold(fxCalendar, "dev") {
		t.Error("dev may start on partial data")
	}
}

func codeOf(code int, _ string) int { return code }
