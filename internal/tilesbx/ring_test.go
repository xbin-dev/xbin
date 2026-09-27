package tilesbx

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

// A ring addresses bytes by their offset in the stream: it keeps the last
// max of them across wraps, says where what it holds starts, and a read
// from before that start reports the gap (start > since).
func TestRingOffsets(t *testing.T) {
	b := newRingBudget(0)
	r := newRing(b, 10)
	r.Write([]byte("hello "))
	if c := r.Read(0, 100); string(c.data) != "hello " || c.start != 0 || c.end != 6 || c.total != 6 || c.ringStart != 0 {
		t.Fatalf("first read: %+v %q", c, c.data)
	}
	r.Write([]byte("world, again")) // 18 bytes in all: the first 8 are gone
	c := r.Read(0, 100)
	if c.total != 18 || c.ringStart != 8 || c.start != 8 || c.end != 18 || string(c.data) != "rld, again" {
		t.Fatalf("after a wrap: %+v %q", c, c.data)
	}
	if c := r.Read(12, 3); c.start != 12 || c.end != 15 || string(c.data) != " ag" {
		t.Fatalf("a range: %+v %q", c, c.data)
	}
	if c := r.Read(30, 5); c.start != 18 || c.end != 18 || len(c.data) != 0 {
		t.Fatalf("past the end: %+v", c)
	}
	// many small writes across the wrap point, checked byte for byte
	r2 := newRing(b, 1000)
	var all []byte
	for i := 0; i < 5000; i++ {
		p := []byte{byte(i), byte(i >> 8), byte(i * 7)}
		all = append(all, p...)
		r2.Write(p)
	}
	c = r2.Read(0, 1<<20)
	if c.total != int64(len(all)) || c.ringStart != int64(len(all)-1000) || !bytes.Equal(c.data, all[len(all)-1000:]) {
		t.Fatalf("the last 1000 of %d: ringStart %d, %d bytes, equal %v", len(all), c.ringStart, len(c.data), bytes.Equal(c.data, all[len(all)-1000:]))
	}
	if tail := r2.Tail(10); !bytes.Equal(tail, all[len(all)-10:]) {
		t.Fatalf("the tail: %v", tail)
	}
	// one write larger than the ring keeps its end
	r3 := newRing(b, 4)
	r3.Write([]byte("abcdefgh"))
	if c := r3.Read(0, 10); string(c.data) != "efgh" || c.ringStart != 4 || c.total != 8 {
		t.Fatalf("an oversized write: %+v %q", c, c.data)
	}
}

// A long-poll waits for bytes past since, answers as soon as the stream
// ends, and gives up at its bound.
func TestRingWait(t *testing.T) {
	r := newRing(newRingBudget(0), 100)
	start := time.Now()
	r.Wait(context.Background(), 0, 50*time.Millisecond)
	if d := time.Since(start); d < 40*time.Millisecond {
		t.Fatalf("an empty ring answered after %s", d)
	}
	go func() { time.Sleep(20 * time.Millisecond); r.Write([]byte("x")) }()
	start = time.Now()
	r.Wait(context.Background(), 0, 5*time.Second)
	if d := time.Since(start); d > 2*time.Second || r.Total() != 1 {
		t.Fatalf("a write answered after %s", d)
	}
	go func() { time.Sleep(20 * time.Millisecond); r.End() }()
	start = time.Now()
	r.Wait(context.Background(), 1, 5*time.Second)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("the end answered after %s", d)
	}
	if c := r.Read(1, 10); !c.ended || c.start != 1 || c.end != 1 {
		t.Fatalf("after the end: %+v", c)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r2 := newRing(newRingBudget(0), 100)
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	start = time.Now()
	r2.Wait(ctx, 0, 5*time.Second)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("a hang-up answered after %s", d)
	}
}

// A tile's rings share one budget: the oldest finished exec's bytes go
// first (its ringStart moves to its total), then the oldest bytes of the
// running ring holding the most; a dropped ring gives its bytes back.
func TestRingBudget(t *testing.T) {
	b := newRingBudget(100)
	old := newRing(b, 80)
	old.Write(bytes.Repeat([]byte("o"), 40))
	old.End()
	newer := newRing(b, 80)
	newer.Write(bytes.Repeat([]byte("n"), 30))
	newer.End()
	run := newRing(b, 80)
	run.Write(bytes.Repeat([]byte("r"), 20))
	if b.held() != 90 {
		t.Fatalf("held %d", b.held())
	}
	run.Write(bytes.Repeat([]byte("R"), 20)) // 110: the oldest finished goes, whole
	if c := old.Read(0, 100); c.ringStart != 40 || len(c.data) != 0 || c.total != 40 {
		t.Fatalf("the oldest finished: %+v", c)
	}
	if c := newer.Read(0, 100); len(c.data) != 30 {
		t.Fatalf("the newer finished lost bytes: %+v", c)
	}
	if b.held() != 70 {
		t.Fatalf("held %d", b.held())
	}
	run.Write(bytes.Repeat([]byte("s"), 40)) // 110 again: the other finished goes
	if c := newer.Read(0, 100); len(c.data) != 0 || c.ringStart != 30 {
		t.Fatalf("the newer finished: %+v", c)
	}
	// running rings only: the one holding the most loses its oldest bytes
	other := newRing(b, 80)
	other.Write(bytes.Repeat([]byte("x"), 30)) // run 80 + other 30 = 110
	if c := run.Read(0, 100); c.ringStart != 10 || len(c.data) != 70 || c.total != 80 {
		t.Fatalf("the biggest running ring: %+v", c)
	}
	if b.held() != 100 {
		t.Fatalf("held %d", b.held())
	}
	run.Drop()
	if b.held() != 30 {
		t.Fatalf("after a drop: held %d", b.held())
	}
	run.Write([]byte("ignored"))
	if b.held() != 30 {
		t.Fatalf("a dropped ring took bytes: %d", b.held())
	}
	// a lowered budget applies at once
	b.setMax(10)
	if b.held() != 10 {
		t.Fatalf("after a lowered budget: held %d", b.held())
	}
}

// A text read never splits a character: a UTF-8 sequence cut short at the
// end of a read is left for the next one (unless it is all the read has, or
// the stream ended there).
func TestTextCut(t *testing.T) {
	s := []byte("a€") // € is 3 bytes
	for n, want := range map[int]int{1: 1, 2: 1, 3: 1, 4: 4} {
		if got := textCut(s[:n], false); got != want {
			t.Errorf("textCut(%q) = %d, want %d", s[:n], got, want)
		}
	}
	if got := textCut(s[1:3], false); got != 2 {
		t.Errorf("a read that is only a partial character: %d", got)
	}
	if got := textCut(s[:2], true); got != 2 {
		t.Errorf("the final read: %d", got)
	}
	if got := utf8Text([]byte("ok\xff\xfe!")); got != "ok\uFFFD!" {
		t.Errorf("invalid bytes: %q", got)
	}
	if !strings.HasPrefix(utf8Text([]byte{0x82, 0xac, 'x'}), "\uFFFD") {
		t.Error("a read starting mid-character isn't replaced")
	}
}
