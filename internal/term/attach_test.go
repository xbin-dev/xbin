package term

import (
	"encoding/json"
	"testing"
	"time"
)

// A tracker on a fake clock whose timer the test fires by hand.
type fakeTimer struct{ stopped bool }

func (f *fakeTimer) Stop() bool { f.stopped = true; return true }

func rigTracker(t *testing.T) (*echoTracker, *time.Time, *[]uint64, *func()) {
	t.Helper()
	now := time.Unix(1000, 0)
	var acks []uint64
	var pending func()
	e := newEchoTracker(func(n uint64) { acks = append(acks, n) })
	e.now = func() time.Time { return now }
	e.after = func(d time.Duration, f func()) stopper {
		if d < 0 || d > echoTimeout {
			t.Fatalf("armed for %v, want within [0, %v]", d, echoTimeout)
		}
		pending = f
		return &fakeTimer{}
	}
	return e, &now, &acks, &pending
}

func TestEchoTrackerAcksOnceOldEnough(t *testing.T) {
	e, now, acks, fire := rigTracker(t)
	if n := e.input(); n != 1 {
		t.Fatalf("first frame numbered %d", n)
	}
	if *fire == nil {
		t.Fatal("no timer armed for the first frame")
	}
	// the timer fires early (a coarse clock): nothing is old enough yet, re-armed
	*now = now.Add(30 * time.Millisecond)
	f := *fire
	*fire = nil
	f()
	if len(*acks) != 0 {
		t.Fatalf("acked %v before %v elapsed", *acks, echoTimeout)
	}
	if *fire == nil {
		t.Fatal("not re-armed for the still-pending frame")
	}
	*now = now.Add(20 * time.Millisecond)
	(*fire)()
	if len(*acks) != 1 || (*acks)[0] != 1 {
		t.Fatalf("acks = %v, want [1]", *acks)
	}
}

func TestEchoTrackerCoalescesABurstAndReArms(t *testing.T) {
	e, now, acks, fire := rigTracker(t)
	e.input() // 1 at t=0
	*now = now.Add(10 * time.Millisecond)
	e.input() // 2 at t=10
	*now = now.Add(10 * time.Millisecond)
	e.input() // 3 at t=20
	*now = now.Add(40 * time.Millisecond)
	e.input() // 4 at t=60
	// t=60: frames 1 (60 old) and 2 (50 old) qualify → one ack, the newest; 3 and 4 wait
	f := *fire
	*fire = nil
	f()
	if len(*acks) != 1 || (*acks)[0] != 2 {
		t.Fatalf("acks = %v, want [2] (the newest frame at least %v old)", *acks, echoTimeout)
	}
	if *fire == nil {
		t.Fatal("not re-armed for frames 3 and 4")
	}
	*now = now.Add(50 * time.Millisecond) // t=110: 3 (90 old) and 4 (50 old)
	f = *fire
	*fire = nil
	f()
	if len(*acks) != 2 || (*acks)[1] != 4 {
		t.Fatalf("acks = %v, want [2 4]", *acks)
	}
	if *fire != nil {
		// nothing pending: no timer
		t.Fatal("re-armed with nothing pending")
	}
}

func TestEchoTrackerCloseStopsEverything(t *testing.T) {
	e, now, acks, fire := rigTracker(t)
	e.input()
	e.close()
	*now = now.Add(time.Second)
	(*fire)() // a timer that raced the close
	if len(*acks) != 0 {
		t.Fatalf("acked %v after close", *acks)
	}
	e.input() // input after close arms nothing and emits nothing
	if e.timer != nil {
		t.Fatal("armed after close")
	}
}

func TestControlFramesRoundTrip(t *testing.T) {
	var ctl control
	if err := json.Unmarshal([]byte(`{"op":"ping","t":1234.5}`), &ctl); err != nil {
		t.Fatal(err)
	}
	pong := textFrame(map[string]any{"op": "pong", "t": ctl.T})
	if !pong.text || string(pong.b) != `{"op":"pong","t":1234.5}` {
		t.Fatalf("pong = %q (text=%v), want the ping's t echoed verbatim", pong.b, pong.text)
	}
	ack := textFrame(map[string]any{"op": "ack", "n": uint64(7)})
	if string(ack.b) != `{"n":7,"op":"ack"}` {
		t.Fatalf("ack = %q", ack.b)
	}
	if (frame{b: []byte("x")}).text {
		t.Fatal("PTY bytes must be binary frames")
	}
	if err := json.Unmarshal([]byte(`{"op":"resize","cols":120,"rows":32}`), &ctl); err != nil || ctl.Cols != 120 || ctl.Rows != 32 {
		t.Fatalf("resize parse: %v %+v", err, ctl)
	}
}
