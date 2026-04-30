package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDoSucceedsFirstAttempt(t *testing.T) {
	calls := 0
	err := Do(context.Background(), Policy{Initial: 1 * time.Millisecond}, nil, func(attempt int) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

func TestDoSucceedsAfterTransientFailures(t *testing.T) {
	calls := 0
	hookCalls := 0
	policy := Policy{MaxAttempts: 5, Initial: 1 * time.Millisecond, Max: 5 * time.Millisecond, Multiplier: 2, Jitter: 0}
	err := Do(context.Background(), policy, func(attempt int, e error, next time.Duration) {
		hookCalls++
	}, func(attempt int) error {
		calls++
		if calls < 3 {
			return errors.New("transient")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
	if hookCalls != 2 { // hook fires for the 2 failed attempts before success
		t.Errorf("hookCalls = %d, want 2", hookCalls)
	}
}

func TestDoExhaustsAttempts(t *testing.T) {
	calls := 0
	hookCalls := 0
	wantErr := errors.New("nope")
	policy := Policy{MaxAttempts: 3, Initial: 1 * time.Millisecond, Max: 5 * time.Millisecond, Multiplier: 2, Jitter: 0}
	err := Do(context.Background(), policy, func(attempt int, e error, next time.Duration) {
		hookCalls++
	}, func(attempt int) error {
		calls++
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
	if hookCalls != 3 { // every attempt fires the hook (the last with next=0)
		t.Errorf("hookCalls = %d, want 3", hookCalls)
	}
}

func TestDoPermanentShortCircuits(t *testing.T) {
	calls := 0
	wantErr := errors.New("bad token")
	policy := Policy{MaxAttempts: 5, Initial: 1 * time.Millisecond}
	err := Do(context.Background(), policy, nil, func(attempt int) error {
		calls++
		return Permanent(wantErr)
	})
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (permanent should not retry)", calls)
	}
}

func TestDoCtxCancelDuringBackoff(t *testing.T) {
	calls := 0
	policy := Policy{MaxAttempts: 5, Initial: 50 * time.Millisecond, Max: 50 * time.Millisecond, Multiplier: 1, Jitter: 0}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	err := Do(ctx, policy, nil, func(attempt int) error {
		calls++
		return errors.New("transient")
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (cancelled during first backoff)", calls)
	}
}

func TestApplyDefaults(t *testing.T) {
	p := applyDefaults(Policy{})
	if p.MaxAttempts != Default.MaxAttempts {
		t.Errorf("MaxAttempts = %d, want %d", p.MaxAttempts, Default.MaxAttempts)
	}
	if p.Initial != Default.Initial {
		t.Errorf("Initial = %v, want %v", p.Initial, Default.Initial)
	}
	if p.Max != Default.Max {
		t.Errorf("Max = %v, want %v", p.Max, Default.Max)
	}
}

func TestApplyDefaultsClampsJitter(t *testing.T) {
	if got := applyDefaults(Policy{Jitter: -0.5}).Jitter; got != 0 {
		t.Errorf("negative jitter = %v, want 0", got)
	}
	if got := applyDefaults(Policy{Jitter: 1.5}).Jitter; got != 1 {
		t.Errorf("over-1 jitter = %v, want 1", got)
	}
}

func TestWithJitterRange(t *testing.T) {
	base := 100 * time.Millisecond
	for range 1000 {
		d := withJitter(base, 0.2)
		if d < 80*time.Millisecond || d > 120*time.Millisecond {
			t.Fatalf("withJitter out of [80,120]ms: %v", d)
		}
	}
}

func TestWithJitterZero(t *testing.T) {
	base := 100 * time.Millisecond
	for range 100 {
		if d := withJitter(base, 0); d != base {
			t.Fatalf("zero-jitter = %v, want %v", d, base)
		}
	}
}
