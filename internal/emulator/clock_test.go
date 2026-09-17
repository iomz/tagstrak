package emulator

import (
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers map[*fakeTimer]time.Time
}
type fakeTimer struct {
	clock *fakeClock
	ch    chan time.Time
}

func newClock() *fakeClock {
	return &fakeClock{now: time.Unix(1700000000, 0), timers: make(map[*fakeTimer]time.Time)}
}
func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) NewTimer(d time.Duration) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &fakeTimer{clock: c, ch: make(chan time.Time, 1)}
	c.timers[timer] = c.now.Add(d)
	return timer
}
func (t *fakeTimer) C() <-chan time.Time { return t.ch }
func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	_, ok := t.clock.timers[t]
	delete(t.clock.timers, t)
	return ok
}
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	for timer, end := range c.timers {
		if !end.After(c.now) {
			timer.ch <- c.now
			delete(c.timers, timer)
		}
	}
}
func (c *fakeClock) has(d time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, end := range c.timers {
		if end.Equal(c.now.Add(d)) {
			return true
		}
	}
	return false
}
func (c *fakeClock) count() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.timers) }
func eventually(t *testing.T, f func() bool) {
	t.Helper()
	end := time.Now().Add(2 * time.Second)
	for !f() {
		if time.Now().After(end) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}
func result(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not stop")
		return nil
	}
}
