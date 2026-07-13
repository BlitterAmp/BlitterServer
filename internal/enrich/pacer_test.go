package enrich

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestProviderPacerFIFOAndCancellation(t *testing.T) {
	p := newProviderPacer(time.Hour)
	if err := p.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var order []int
	done := make(chan struct{}, 2)
	for _, n := range []int{1, 2} {
		go func() {
			if err := p.Wait(context.Background()); err == nil {
				mu.Lock()
				order = append(order, n)
				mu.Unlock()
			}
			done <- struct{}{}
		}()
		<-p.Queued()
	}
	p.AdvanceForTest()
	<-done
	p.AdvanceForTest()
	<-done
	if len(order) != 2 || order[0] != 1 || order[1] != 2 {
		t.Fatalf("wake order=%v", order)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled wait error=%v", err)
	}
}
