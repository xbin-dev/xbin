package runner

import (
	"testing"
	"time"
)

func TestAlwaysOnBackoff(t *testing.T) {
	cases := []struct {
		prev, upFor, want time.Duration
	}{
		{0, 0, time.Second},
		{time.Second, time.Second, 2 * time.Second},
		{4 * time.Minute, time.Minute, 5 * time.Minute}, // capped
		{5 * time.Minute, time.Minute, 5 * time.Minute},
		{2 * time.Minute, 11 * time.Minute, time.Second}, // healthy for a while: start over
	}
	for _, c := range cases {
		if got := nextBackoff(c.prev, c.upFor); got != c.want {
			t.Errorf("nextBackoff(%v, up %v) = %v, want %v", c.prev, c.upFor, got, c.want)
		}
	}
}
