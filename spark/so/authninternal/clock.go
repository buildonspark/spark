package authninternal

import "time"

// Clock is an interface that provides time-related functions.
type Clock interface {
	Now() time.Time
}

// RealClock implements Clock using the actual system time.
type RealClock struct{}

// Now returns the current time.
func (RealClock) Now() time.Time {
	return time.Now()
}

// TestClock implements Clock for testing purposes.
type TestClock struct {
	current time.Time
}

// NewTestClock creates a new TestClock with the given time.
func NewTestClock(t time.Time) *TestClock {
	return &TestClock{current: t}
}

// Now returns the current time.
func (c *TestClock) Now() time.Time {
	return c.current
}

// Advance advances the clock by the given duration.
func (c *TestClock) Advance(d time.Duration) {
	c.current = c.current.Add(d)
}
