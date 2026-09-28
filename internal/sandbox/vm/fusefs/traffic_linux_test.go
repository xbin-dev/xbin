//go:build linux

package fusefs

import (
	"encoding/binary"
	"strings"
	"testing"
	"time"
)

func inHeader(op uint32, unique, node uint64, pid uint32) []byte {
	b := make([]byte, 40)
	binary.LittleEndian.PutUint32(b[0:4], 40)
	binary.LittleEndian.PutUint32(b[4:8], op)
	binary.LittleEndian.PutUint64(b[8:16], unique)
	binary.LittleEndian.PutUint64(b[16:24], node)
	binary.LittleEndian.PutUint32(b[32:36], pid)
	return b
}

func outHeader(unique uint64) []byte {
	b := make([]byte, 16)
	binary.LittleEndian.PutUint32(b[0:4], 16)
	binary.LittleEndian.PutUint64(b[8:16], unique)
	return b
}

// The dump's view of an export: what the guest still waits on, by whom and
// for how long; answered requests and ones that get no answer drop out.
func TestTrafficReport(t *testing.T) {
	var tr traffic
	t0 := time.Unix(1000, 0)
	names := func(id uint64) string { return map[uint64]string{1: "/run/backend", 7: "/t/a"}[id] }
	if got := tr.report(t0, names); got != "no requests yet" {
		t.Fatalf("empty: %q", got)
	}
	tr.request(inHeader(26, 1, 0, 0), t0)                    // INIT
	tr.request(inHeader(15, 2, 1, 57), t0.Add(time.Second))  // READ /run/backend by pid 57
	tr.request(inHeader(1, 3, 7, 60), t0.Add(2*time.Second)) // LOOKUP under /t/a
	tr.request(inHeader(2, 4, 7, 0), t0.Add(3*time.Second))  // FORGET: no reply
	tr.reply(outHeader(1))                                   // INIT answered
	tr.reply(outHeader(3))                                   // LOOKUP answered
	tr.reply(outHeader(0))                                   // a notification
	got := tr.report(t0.Add(11*time.Second), names)
	for _, want := range []string{"4 requests, the last 8s ago, 1 in flight:", "READ /run/backend for guest pid 57, 10s"} {
		if !strings.Contains(got, want) {
			t.Errorf("report lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "LOOKUP") || strings.Contains(got, "FORGET") || strings.Contains(got, "INIT") {
		t.Errorf("answered or unanswerable requests reported in flight:\n%s", got)
	}
	if d := tr.oldest(t0.Add(11 * time.Second)); d != 10*time.Second {
		t.Errorf("oldest %s, want 10s", d)
	}
	tr.reply(outHeader(2))
	if got := tr.report(t0.Add(12*time.Second), names); !strings.HasSuffix(got, "none in flight") || tr.oldest(t0) != 0 {
		t.Errorf("after the last answer: %q", got)
	}
}
