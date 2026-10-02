package limits

import (
	"sync"
	"time"
)

const rateWindow = time.Minute

type RateLimiter struct {
	mutex           sync.Mutex
	now             func() time.Time
	timestampsByKey map[string][]time.Time
}

func NewRateLimiter(now func() time.Time) *RateLimiter {
	if now == nil {
		now = time.Now
	}
	return &RateLimiter{now: now, timestampsByKey: map[string][]time.Time{}}
}

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
