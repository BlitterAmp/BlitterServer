// Package events is the in-process event bus behind GET /v1/events: durable
// sequence numbers in SQLite (Last-Event-ID resume) plus live fan-out
// channels for connected SSE streams.
package events

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/store"
)

// Event is one delivered envelope. Data is the JSON payload (the contract's
// Event.data object).
type Event struct {
	Seq       int64
	Type      string
	ProfileID string // "" = instance-wide
	At        time.Time
	Data      string
}

type subscriber struct {
	profileID string
	ch        chan Event
}

// Bus persists events and fans them out. Slow subscribers never block
// publishers: on a full channel the event is dropped live and the client
// recovers via Last-Event-ID replay on reconnect.
type Bus struct {
	st *store.Store

	mu        sync.Mutex
	subs      map[*subscriber]struct{}
	published int64
}

const pruneKeep = 10000

func NewBus(st *store.Store) *Bus {
	return &Bus{st: st, subs: make(map[*subscriber]struct{})}
}

// Publish stores the event and delivers it to matching live subscribers.
// profileID "" targets every profile.
func (b *Bus) Publish(ctx context.Context, eventType, profileID string, data any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	seq, err := b.st.AppendEvent(ctx, eventType, profileID, string(payload))
	if err != nil {
		return err
	}
	ev := Event{Seq: seq, Type: eventType, ProfileID: profileID, At: time.Now().UTC(), Data: string(payload)}

	b.mu.Lock()
	for sub := range b.subs {
		if profileID == "" || sub.profileID == profileID {
			select {
			case sub.ch <- ev:
			default: // drop; replay covers it
			}
		}
	}
	b.published++
	prune := b.published%1000 == 0
	b.mu.Unlock()

	if prune {
		_ = b.st.PruneEvents(ctx, pruneKeep)
	}
	return nil
}

// Subscribe returns a channel of events visible to profileID, replaying
// everything after sinceSeq first. cancel closes the channel and detaches.
func (b *Bus) Subscribe(profileID string, sinceSeq int64) (<-chan Event, func()) {
	sub := &subscriber{profileID: profileID, ch: make(chan Event, 64)}

	// Replay before attaching would lose events published in between;
	// attach first, then replay and dedupe by seq on the reader side —
	// simpler: buffer live events while replaying, emitting replays first.
	out := make(chan Event, 64)
	b.mu.Lock()
	b.subs[sub] = struct{}{}
	b.mu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(out)
		lastSent := sinceSeq
		rows, err := b.st.EventsSince(context.Background(), profileID, sinceSeq, pruneKeep)
		if err == nil {
			for _, r := range rows {
				select {
				case <-done: // canceled while replaying — never deliver late
					return
				default:
				}
				select {
				case out <- Event{Seq: r.Seq, Type: r.Type, ProfileID: r.ProfileID, At: r.At, Data: r.Data}:
					lastSent = r.Seq
				case <-done:
					return
				}
			}
		}
		for {
			select {
			case ev := <-sub.ch:
				if ev.Seq <= lastSent {
					continue // already replayed
				}
				select {
				case out <- ev:
					lastSent = ev.Seq
				case <-done:
					return
				}
			case <-done:
				return
			}
		}
	}()

	cancel := func() {
		b.mu.Lock()
		delete(b.subs, sub)
		b.mu.Unlock()
		close(done)
	}
	return out, cancel
}
