// Package limits provides rate limiting and host-side confirmation
// for brokered secrets.
package limits

import (
	"sync"
	"time"
)

// rateWindow is the rolling window length for rate limits.
const rateWindow = time.Minute

// RateLimiter counts uses per key in a rolling one-minute window.
type RateLimiter struct {
	mutex           sync.Mutex
	now             func() time.Time
	timestampsByKey map[string][]time.Time
}

// NewRateLimiter returns a new limiter. Takes a clock function; nil
// defaults to time.Now.
func NewRateLimiter(now func() time.Time) *RateLimiter {
	if now == nil {
		now = time.Now
	}
	return &RateLimiter{now: now, timestampsByKey: map[string][]time.Time{}}
}

// Allow returns true if key has room under the perMinute limit and
// records the use. Zero or negative perMinute means unlimited. Safe
// for concurrent use.
func (limiter *RateLimiter) Allow(key string, perMinute int) bool {
	if perMinute <= 0 {
		return true
	}
	moment := limiter.now()
	limiter.mutex.Lock()
	defer limiter.mutex.Unlock()
	recent := limiter.timestampsByKey[key][:0:0]
	for _, stamp := range limiter.timestampsByKey[key] {
		if moment.Sub(stamp) < rateWindow {
			recent = append(recent, stamp)
		}
	}
	if len(recent) >= perMinute {
		limiter.timestampsByKey[key] = recent
		return false
	}
	limiter.timestampsByKey[key] = append(recent, moment)
	return true
}

// Confirmer decides whether a secret use is approved.
type Confirmer interface {
	Confirm(project, secret, detail string) bool
}

// AlwaysAllow is a Confirmer that approves every request.
type AlwaysAllow struct{}

// Confirm always returns true.
func (AlwaysAllow) Confirm(project, secret, detail string) bool {
	return true
}

// AlwaysDeny is a Confirmer that refuses every request.
type AlwaysDeny struct{}

// Confirm always returns false.
func (AlwaysDeny) Confirm(project, secret, detail string) bool {
	return false
}
