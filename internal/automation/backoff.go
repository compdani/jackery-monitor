package automation

import "time"

// LoopBackoff paces one serialized worker. The caller supplies the current base
// interval on every tick so runtime setting changes take effect immediately.
type LoopBackoff struct {
	Max      time.Duration
	failures uint
}

func (b *LoopBackoff) Reset() { b.failures = 0 }
func (b *LoopBackoff) RecordFailure() {
	if b.failures < 8 {
		b.failures++
	}
}
func (b *LoopBackoff) NextSleep(base time.Duration) time.Duration {
	// Compare before multiplying to avoid duration overflow.
	factor := time.Duration(1 << b.failures)
	if base > b.Max/factor {
		return b.Max
	}
	return base * factor
}
