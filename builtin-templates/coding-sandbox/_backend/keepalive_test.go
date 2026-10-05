package main

import (
	"context"
	"testing"
	"time"
)

// TestKeepAlive: while a stdio socket (a coding agent's ACP pipe) is open on
// a sandbox, the manager is activity on it every KeepAlive — it lists the
// sandbox's execs, which the substrate counts — and it stops once the
// socket closes, so the idle stop counts from there.
func TestKeepAlive(t *testing.T) {
	tm := newTestManager(t, "")
	tm.m.KeepAlive = 20 * time.Millisecond
	a := tm.tg.As(t, "apps/agent")
	sb := a.Create(map[string]any{"name": "alive"})
	x := a.Exec(sb.ID, map[string]any{"cmd": "sleep 30", "split": true})
	defer a.Call("DELETE", "/sandboxes/"+sb.ID+"/execs/"+x.ID, nil, 204, nil)

	time.Sleep(100 * time.Millisecond)
	if n := tm.fb.Calls("execs"); n != 0 {
		t.Fatalf("no socket open: %d keepalive calls", n)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := a.Dial(ctx, "/sandboxes/"+sb.ID+"/execs/"+x.ID+"/stdio")
	if err != nil {
		t.Fatal(err)
	}
	go func() { // drain the socket until it closes
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}()
	eventually(t, 5*time.Second, "keepalive calls while the socket is open", func() bool { return tm.fb.Calls("execs") >= 3 })

	_ = c.Close()
	var last int
	eventually(t, 5*time.Second, "the keepalive stopping once the socket closed", func() bool {
		n := tm.fb.Calls("execs")
		time.Sleep(100 * time.Millisecond) // five intervals
		last = tm.fb.Calls("execs")
		return last == n
	})
	time.Sleep(100 * time.Millisecond)
	if n := tm.fb.Calls("execs"); n != last {
		t.Fatalf("keepalive calls after the socket closed: %d → %d", last, n)
	}
}
