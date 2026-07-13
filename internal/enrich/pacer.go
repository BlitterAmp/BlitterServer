package enrich

import (
	"context"
	"sync"
	"time"
)

type providerPacer struct {
	mu        sync.Mutex
	interval  time.Duration
	last      time.Time
	queue     []chan struct{}
	queued    chan struct{}
	waitCount int
}

func newProviderPacer(interval time.Duration) *providerPacer {
	return &providerPacer{interval: interval, queued: make(chan struct{}, 32)}
}

func (p *providerPacer) Wait(ctx context.Context) error {
	p.mu.Lock()
	p.waitCount++
	if p.last.IsZero() || time.Since(p.last) >= p.interval && len(p.queue) == 0 {
		p.last = time.Now()
		p.mu.Unlock()
		return nil
	}
	wake := make(chan struct{})
	p.queue = append(p.queue, wake)
	select {
	case p.queued <- struct{}{}:
	default:
	}
	if len(p.queue) == 1 {
		delay := time.Until(p.last.Add(p.interval))
		if delay < 0 {
			delay = 0
		}
		time.AfterFunc(delay, p.advance)
	}
	p.mu.Unlock()
	select {
	case <-wake:
		return nil
	case <-ctx.Done():
		p.mu.Lock()
		for i, candidate := range p.queue {
			if candidate == wake {
				p.queue = append(p.queue[:i], p.queue[i+1:]...)
				break
			}
		}
		p.mu.Unlock()
		return ctx.Err()
	}
}

func (p *providerPacer) advance() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.queue) == 0 {
		return
	}
	wake := p.queue[0]
	p.queue = p.queue[1:]
	p.last = time.Now()
	close(wake)
	if len(p.queue) > 0 {
		time.AfterFunc(p.interval, p.advance)
	}
}

func (p *providerPacer) Queued() <-chan struct{} { return p.queued }
func (p *providerPacer) AdvanceForTest()         { p.advance() }
func (p *providerPacer) WaitCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waitCount
}
