// Package clock provides a deterministic Clock implementation for evaluation.
//
// FakeClock satisfies engine.Clock. Unlike the real clock, it only advances
// when Advance is called, and AfterFunc timers fire synchronously when their
// deadline is reached. This makes time-based gating logic (typing speed,
// time-since-last-edit, idle timers) deterministic under replay.
package clock

import (
	"sort"
	"sync"
	"time"

	"cursortab/engine"
)

// FakeClock is a deterministic engine.Clock for tests and replay.
type FakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

// New returns a FakeClock anchored at t. If t is zero, uses a fixed epoch
// so cassettes behave identically regardless of when they run.
func New(t time.Time) *FakeClock {
	if t.IsZero() {
		t = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return &FakeClock{now: t}
}

// Now implements engine.Clock.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// AfterFunc implements engine.Clock. The returned Timer fires when Advance
// moves the clock past fireAt.
func (c *FakeClock) AfterFunc(d time.Duration, f func()) engine.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{
		fireAt: c.now.Add(d),
		f:      f,
	}
	c.timers = append(c.timers, t)
	return t
}

// Advance moves the clock forward by d and fires any timers whose deadline
// has passed, in order. Callbacks run on the caller's goroutine.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	target := c.now

	var due []*fakeTimer
	remaining := c.timers[:0]
	for _, t := range c.timers {
		t.mu.Lock()
		stopped := t.stopped
		fireAt := t.fireAt
		t.mu.Unlock()
		if stopped {
			continue
		}
		if !fireAt.After(target) {
			due = append(due, t)
		} else {
			remaining = append(remaining, t)
		}
	}
	c.timers = remaining
	c.mu.Unlock()

	sort.Slice(due, func(i, j int) bool {
		return due[i].fireAt.Before(due[j].fireAt)
	})
	for _, t := range due {
		t.fire()
	}
}

// Set advances the clock to the given absolute time.
func (c *FakeClock) Set(t time.Time) {
	c.mu.Lock()
	d := t.Sub(c.now)
	c.mu.Unlock()
	if d > 0 {
		c.Advance(d)
	}
}

type fakeTimer struct {
	mu      sync.Mutex
	fireAt  time.Time
	f       func()
	stopped bool
}

// Stop implements engine.Timer.
func (t *fakeTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	was := !t.stopped
	t.stopped = true
	return was
}

func (t *fakeTimer) fire() {
	t.mu.Lock()
	if t.stopped {
		t.mu.Unlock()
		return
	}
	t.stopped = true
	f := t.f
	t.mu.Unlock()
	if f != nil {
		f()
	}
}
