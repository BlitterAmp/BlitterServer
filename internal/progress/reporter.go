// Package progress provides a small processed-unit-driven logging cadence.
package progress

import "time"

// Reporter rate-limits progress after processed units advance.
type Reporter struct {
	now           func() time.Time
	interval      time.Duration
	started       time.Time
	last          time.Time
	lastProcessed int
}

func New(now func() time.Time, interval time.Duration) *Reporter {
	started := now()
	return &Reporter{now: now, interval: interval, started: started, last: started}
}

func (r *Reporter) Due(processed int) bool {
	if processed <= r.lastProcessed {
		return false
	}
	now := r.now()
	if now.Sub(r.last) < r.interval {
		return false
	}
	r.last, r.lastProcessed = now, processed
	return true
}

func (r *Reporter) DurationMS() int64 { return r.now().Sub(r.started).Milliseconds() }
