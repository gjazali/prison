package limits

import (
	"testing"
	"time"
)

func TestRateLimiterWindow(t *testing.T) {
	clock := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	limiter := NewRateLimiter(func() time.Time { return clock })
	for attempt := 1; attempt <= 3; attempt++ {
		if !limiter.Allow("alpha/deploy", 3) {
			t.Fatalf("use %d refused, want allowed", attempt)
		}
	}
	if limiter.Allow("alpha/deploy", 3) {
		t.Fatal("the fourth use in the same minute was allowed")
	}
	clock = clock.Add(59 * time.Second)
	if limiter.Allow("alpha/deploy", 3) {
		t.Fatal("a use 59 seconds later was allowed, want the window to hold")
	}
	clock = clock.Add(2 * time.Second)
	for attempt := 1; attempt <= 3; attempt++ {
		if !limiter.Allow("alpha/deploy", 3) {
			t.Fatalf("use %d past the window was refused", attempt)
		}
	}
	if limiter.Allow("alpha/deploy", 3) {
		t.Fatal("the refilled window allowed a fourth use")
	}
}

func TestRateLimiterSeparatesKeys(t *testing.T) {
	clock := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	limiter := NewRateLimiter(func() time.Time { return clock })
	if !limiter.Allow("alpha/deploy", 1) || !limiter.Allow("beta/deploy", 1) {
		t.Fatal("the first use of each key was refused")
	}
	if limiter.Allow("alpha/deploy", 1) {
		t.Fatal("alpha exceeded its limit")
	}
	if limiter.Allow("beta/deploy", 1) {
		t.Fatal("beta exceeded its limit")
	}
}

func TestRateLimiterUnlimited(t *testing.T) {
	clock := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	limiter := NewRateLimiter(func() time.Time { return clock })
	for attempt := 0; attempt < 100; attempt++ {
		if !limiter.Allow("alpha/deploy", 0) {
			t.Fatal("an unlimited use was refused")
		}
	}
	if !limiter.Allow("alpha/deploy", 1) {
		t.Fatal("unlimited uses were counted against a later limit")
	}
}

func TestNewRateLimiterDefaultsToWallClock(t *testing.T) {
	limiter := NewRateLimiter(nil)
	if !limiter.Allow("alpha/deploy", 1) {
		t.Fatal("the first use was refused")
	}
	if limiter.Allow("alpha/deploy", 1) {
		t.Fatal("the second use was allowed")
	}
}
