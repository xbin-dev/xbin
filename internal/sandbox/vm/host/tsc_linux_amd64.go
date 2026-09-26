package host

import (
	"time"

	"golang.org/x/sys/unix"
)

func rdtsc() uint64

// tscKHz measures the host's TSC rate. Under QEMU's emulation the guest's
// TSC is the host's (rdtsc passes through), and a guest that has to
// calibrate it against the emulated PIT sometimes fails — then waits forever
// for a timer tick that never comes. Told the rate (tsc_early_khz=), it
// doesn't calibrate. 0 = couldn't measure.
func tscKHz() int {
	var t0, t1 unix.Timespec
	if unix.ClockGettime(unix.CLOCK_MONOTONIC_RAW, &t0) != nil {
		return 0
	}
	c0 := rdtsc()
	time.Sleep(50 * time.Millisecond)
	c1 := rdtsc()
	if unix.ClockGettime(unix.CLOCK_MONOTONIC_RAW, &t1) != nil {
		return 0
	}
	ns := t1.Nano() - t0.Nano()
	if ns <= 0 || c1 <= c0 {
		return 0
	}
	return int((c1 - c0) * 1e6 / uint64(ns))
}
