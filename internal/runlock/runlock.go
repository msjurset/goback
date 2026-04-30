// Package runlock provides per-name run locks so a single backup name is never
// executed concurrently by multiple goroutines (e.g. the cron scheduler and
// the catchup loop both deciding the same job is due).
package runlock

import "sync"

// Coordinator hands out per-name run locks. TryLock returns (release, true) on
// acquisition; the caller must invoke release exactly once when done. If a job
// with that name is already held, TryLock returns (nil, false) immediately.
type Coordinator struct {
	mu   sync.Mutex
	held map[string]bool
}

// New returns an empty Coordinator.
func New() *Coordinator {
	return &Coordinator{held: make(map[string]bool)}
}

// TryLock attempts to acquire the lock for name. The release function is safe
// to call multiple times; only the first call has effect.
func (c *Coordinator) TryLock(name string) (release func(), ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.held[name] {
		return nil, false
	}
	c.held[name] = true
	released := false
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if released {
			return
		}
		released = true
		delete(c.held, name)
	}, true
}
