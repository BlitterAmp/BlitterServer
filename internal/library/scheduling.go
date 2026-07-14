package library

import (
	"context"
	"math/rand/v2"
	"time"
)

func waitForEnrichment(ctx context.Context) bool {
	// Frequent staggered passes let newly overdue rows flow through while the
	// store remains the authority on the 24-hour per-entity eligibility window.
	delay := 4*time.Hour + time.Duration(rand.Int64N(int64(4*time.Hour)))
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func waitForResetRetry(ctx context.Context, attempt int) bool {
	if attempt > 8 {
		attempt = 8
	}
	delay := 100 * time.Millisecond * time.Duration(1<<(attempt-1))
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
