package runlock

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestTryLockReleaseReacquire(t *testing.T) {
	c := New()
	release, ok := c.TryLock("foo")
	if !ok {
		t.Fatal("first TryLock should succeed")
	}
	if _, ok := c.TryLock("foo"); ok {
		t.Fatal("second TryLock on held name should fail")
	}
	release()
	if r, ok := c.TryLock("foo"); !ok {
		t.Fatal("after release, TryLock should succeed")
	} else {
		r()
	}
}

func TestTryLockDifferentNamesIndependent(t *testing.T) {
	c := New()
	r1, ok1 := c.TryLock("a")
	r2, ok2 := c.TryLock("b")
	if !ok1 || !ok2 {
		t.Fatalf("different names should both lock: ok1=%v ok2=%v", ok1, ok2)
	}
	r1()
	r2()
}

func TestReleaseIdempotent(t *testing.T) {
	c := New()
	release, _ := c.TryLock("x")
	release()
	release() // must not panic or affect a fresh acquisition
	if r, ok := c.TryLock("x"); !ok {
		t.Fatal("after double-release, TryLock should still succeed")
	} else {
		r()
	}
}

// TestMutualExclusion: hammers TryLock from many goroutines and verifies that
// at most one holder is inside the critical section at a time.
func TestMutualExclusion(t *testing.T) {
	c := New()
	const goroutines = 100
	var inside atomic.Int32
	var peak atomic.Int32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for {
				release, ok := c.TryLock("m")
				if !ok {
					continue
				}
				cur := inside.Add(1)
				for {
					old := peak.Load()
					if cur > old {
						if peak.CompareAndSwap(old, cur) {
							break
						}
						continue
					}
					break
				}
				inside.Add(-1)
				release()
				return
			}
		}()
	}
	wg.Wait()
	if got := peak.Load(); got != 1 {
		t.Fatalf("peak holders inside lock = %d, want 1", got)
	}
}
