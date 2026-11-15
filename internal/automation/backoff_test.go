package automation

import (
	"testing"
	"time"
)

// All five assertions groups from tests/test_backoff.py.
func TestLoopBackoff(t *testing.T) {
	b := LoopBackoff{Max: 300 * time.Second}
	check := func(base, want time.Duration) {
		t.Helper()
		if got := b.NextSleep(base * time.Second); got != want*time.Second {
			t.Fatalf("sleep %s want %s", got, want*time.Second)
		}
	}
	check(60, 60)
	for _, want := range []time.Duration{120, 240, 300, 300} {
		b.RecordFailure()
		check(60, want)
	}
	b.Reset()
	check(60, 60)
	b.Max = 10000 * time.Second
	for i := 0; i < 1000; i++ {
		b.RecordFailure()
	}
	check(60, 10000)
	if b.failures != 8 {
		t.Fatal(b.failures)
	}
	b.Reset()
	check(30, 30)
	check(120, 120)
	b.RecordFailure()
	check(30, 60)
	check(120, 240)
}

func TestLoopBackoffFractionalCap(t *testing.T) {
	b := LoopBackoff{Max: 300 * time.Nanosecond}
	for i := 0; i < 8; i++ {
		b.RecordFailure()
	}
	if got := b.NextSleep(time.Nanosecond); got != 256*time.Nanosecond {
		t.Fatal(got)
	}
}
