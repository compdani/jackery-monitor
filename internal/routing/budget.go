package routing

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// DefaultDailyCalls caps provider calls per UTC day. Apple's documented
// free allowance is 25,000 calls/day per team, shared with MapKit JS; the
// default keeps this app to a fifth of it.
const DefaultDailyCalls = 5000

// MaxCallsPerRun caps one evaluate/optimize request.
const MaxCallsPerRun = 1500

// ErrBudget means the daily call cap is used up.
var ErrBudget = errors.New("daily routing call limit reached")

// BudgetStore persists the day's call count across restarts.
type BudgetStore interface {
	LoadUsage(day string) (int, error)
	SaveUsage(day string, used int) error
}

// Budget counts provider calls per UTC day. With a Store the count is
// loaded on first use each day and written by Flush, so a restart loses at
// most the calls since the last Flush (one request's worth); Apple's own
// 429 quota still bounds the team.
type Budget struct {
	Limit int
	Now   func() time.Time
	Store BudgetStore

	mu     sync.Mutex
	day    string
	used   int
	loaded bool
	dirty  bool
}

func (b *Budget) today() string {
	now := time.Now
	if b.Now != nil {
		now = b.Now
	}
	return now().UTC().Format("2006-01-02")
}

func (b *Budget) limit() int {
	if b.Limit > 0 {
		return b.Limit
	}
	return DefaultDailyCalls
}

func (b *Budget) roll() {
	if d := b.today(); d != b.day {
		b.day, b.used, b.loaded, b.dirty = d, 0, false, false
	}
	if !b.loaded && b.Store != nil {
		b.loaded = true
		if n, err := b.Store.LoadUsage(b.day); err == nil && n > b.used {
			b.used = n
		}
	}
}

// Flush saves the count when it changed since the last Flush.
func (b *Budget) Flush() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.dirty || b.Store == nil {
		return nil
	}
	if err := b.Store.SaveUsage(b.day, b.used); err != nil {
		return err
	}
	b.dirty = false
	return nil
}

// Take records n calls, or fails without recording when over the cap.
func (b *Budget) Take(n int) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.roll()
	if b.used+n > b.limit() {
		return ErrBudget
	}
	b.used += n
	b.dirty = true
	return nil
}

// Check fails when n more calls would exceed the cap or the per-run cap.
func (b *Budget) Check(n int) error {
	if n > MaxCallsPerRun {
		return fmt.Errorf("this trip needs about %d routing calls; the limit per request is %d", n, MaxCallsPerRun)
	}
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.roll()
	if b.used+n > b.limit() {
		return fmt.Errorf("%w (%d of %d used today; this request needs about %d)", ErrBudget, b.used, b.limit(), n)
	}
	return nil
}

// Usage reports calls used today and the cap.
func (b *Budget) Usage() (used, limit int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.roll()
	return b.used, b.limit()
}
