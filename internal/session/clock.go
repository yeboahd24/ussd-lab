package session

import (
	"sync"
	"time"
)

// Clock supplies the current time.
//
// Session expiry is the one piece of core logic that depends on wall-clock
// time. Injecting it means a 120-second timeout can be tested in microseconds
// instead of with time.Sleep, which is the difference between a test suite
// people run and one they skip.
type Clock interface {
	Now() time.Time
}

// SystemClock is the production Clock.
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC() }

// FakeClock is a manually advanced Clock for tests.
//
// It lives in the production package, not a _test file, because the storage
// conformance suite and future packages need it too. It is safe for concurrent
// use so it can be shared by parallel tests.
type FakeClock struct {
	mu  sync.RWMutex
	now time.Time
}

// NewFakeClock returns a clock fixed at t.
func NewFakeClock(t time.Time) *FakeClock {
	return &FakeClock{now: t.UTC()}
}

func (c *FakeClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.now
}

// Advance moves the clock forward by d.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// Set moves the clock to t.
func (c *FakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t.UTC()
}
