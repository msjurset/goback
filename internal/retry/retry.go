// Package retry implements exponential backoff with jitter for transient
// operations (network calls, scheduled tasks). Permanent errors short-circuit
// the loop via retry.Permanent.
package retry

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"
)

// Policy controls a retry loop. Zero-valued fields fall back to Default.
type Policy struct {
	MaxAttempts int           // total attempts including the first
	Initial     time.Duration // first sleep before the second attempt
	Max         time.Duration // ceiling on a single backoff window
	Multiplier  float64       // each delay = previous * Multiplier
	Jitter      float64       // 0..1, fraction of the delay to randomize ±
}

// Default is the policy applied when fields on a Policy are zero.
var Default = Policy{
	MaxAttempts: 5,
	Initial:     30 * time.Second,
	Max:         15 * time.Minute,
	Multiplier:  2.0,
	Jitter:      0.2,
}

type permanentErr struct{ err error }

func (e *permanentErr) Error() string { return e.err.Error() }
func (e *permanentErr) Unwrap() error { return e.err }

// Permanent wraps err so retry.Do returns immediately without further
// attempts. Useful for config-shaped failures (bad token, missing remote
// path, validation error) where retrying can't help.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentErr{err: err}
}

// IsPermanent reports whether err was wrapped by Permanent.
func IsPermanent(err error) bool {
	var p *permanentErr
	return errors.As(err, &p)
}

// Hook fires once per failed attempt with the error and the delay before the
// next attempt (zero on the final attempt). Useful for logging or recording
// each failure to a status log.
type Hook func(attempt int, err error, next time.Duration)

// Do calls fn up to p.MaxAttempts times with exponential backoff + jitter
// between attempts. fn receives the 1-indexed attempt number. ctx
// cancellation interrupts the wait between attempts immediately. A nil
// returned from fn ends the loop with success.
//
// If fn returns an error wrapped in Permanent, Do calls hook and returns
// the underlying error without further attempts.
func Do(ctx context.Context, p Policy, hook Hook, fn func(attempt int) error) error {
	p = applyDefaults(p)

	delay := p.Initial
	for attempt := 1; attempt <= p.MaxAttempts; attempt++ {
		err := fn(attempt)
		if err == nil {
			return nil
		}

		if IsPermanent(err) {
			if hook != nil {
				hook(attempt, err, 0)
			}
			return errors.Unwrap(err)
		}

		if attempt == p.MaxAttempts {
			if hook != nil {
				hook(attempt, err, 0)
			}
			return err
		}

		next := withJitter(delay, p.Jitter)
		if hook != nil {
			hook(attempt, err, next)
		}

		timer := time.NewTimer(next)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}

		delay = time.Duration(float64(delay) * p.Multiplier)
		if delay > p.Max {
			delay = p.Max
		}
	}
	// unreachable — the loop always returns
	return nil
}

func applyDefaults(p Policy) Policy {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = Default.MaxAttempts
	}
	if p.Initial <= 0 {
		p.Initial = Default.Initial
	}
	if p.Max <= 0 {
		p.Max = Default.Max
	}
	if p.Multiplier <= 0 {
		p.Multiplier = Default.Multiplier
	}
	if p.Jitter < 0 {
		p.Jitter = 0
	}
	if p.Jitter > 1 {
		p.Jitter = 1
	}
	return p
}

func withJitter(base time.Duration, fraction float64) time.Duration {
	if fraction <= 0 {
		return base
	}
	factor := 1 + (rand.Float64()*2-1)*fraction
	d := time.Duration(float64(base) * factor)
	if d < 0 {
		d = 0
	}
	return d
}
