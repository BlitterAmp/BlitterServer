package events

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func profile(t *testing.T, s *store.Store, name string) string {
	t.Helper()
	p, err := s.CreateProfileRecord(context.Background(), name, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return p.ProfileID
}

func recv(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
		return Event{}
	}
}

func TestPublishFansOutByScope(t *testing.T) {
	s := openStore(t)
	bus := NewBus(s)
	ctx := context.Background()
	p1 := profile(t, s, "A")
	p2 := profile(t, s, "B")

	sub1, cancel1 := bus.Subscribe(p1, 0)
	defer cancel1()
	sub2, cancel2 := bus.Subscribe(p2, 0)
	defer cancel2()

	if err := bus.Publish(ctx, "library.changed", "", map[string]any{"libraryId": "lib_local"}); err != nil {
		t.Fatal(err)
	}
	if err := bus.Publish(ctx, "love.updated", p1, map[string]any{"ref": "trk_x"}); err != nil {
		t.Fatal(err)
	}

	// Both see the instance event.
	e1 := recv(t, sub1)
	e2 := recv(t, sub2)
	if e1.Type != "library.changed" || e2.Type != "library.changed" || e1.Seq == 0 {
		t.Fatalf("instance fan-out: %+v %+v", e1, e2)
	}
	// Only p1 sees the profile event.
	e1 = recv(t, sub1)
	if e1.Type != "love.updated" {
		t.Fatalf("profile event: %+v", e1)
	}
	select {
	case leaked := <-sub2:
		t.Fatalf("p2 must not see p1's event: %+v", leaked)
	case <-time.After(100 * time.Millisecond):
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(e1.Data), &payload); err != nil || payload["ref"] != "trk_x" {
		t.Fatalf("payload: %v %+v", err, payload)
	}
}

func TestSubscribeReplaysFromSeq(t *testing.T) {
	s := openStore(t)
	bus := NewBus(s)
	ctx := context.Background()
	p1 := profile(t, s, "A")

	bus.Publish(ctx, "library.changed", "", map[string]any{"n": 1})
	bus.Publish(ctx, "taste.updated", p1, map[string]any{})

	// A late subscriber resuming from 0 replays both, in order.
	sub, cancel := bus.Subscribe(p1, 0)
	defer cancel()
	if e := recv(t, sub); e.Type != "library.changed" {
		t.Fatalf("replay 1: %+v", e)
	}
	second := recv(t, sub)
	if second.Type != "taste.updated" {
		t.Fatalf("replay 2: %+v", second)
	}

	// Resuming from the first seq skips it.
	sub2, cancel2 := bus.Subscribe(p1, second.Seq-1)
	defer cancel2()
	if e := recv(t, sub2); e.Seq != second.Seq {
		t.Fatalf("resume: %+v", e)
	}

	// And live events still arrive after replay.
	bus.Publish(ctx, "playlist.changed", p1, map[string]any{"playlistId": "pl_x"})
	if e := recv(t, sub); e.Type != "playlist.changed" {
		t.Fatalf("live after replay: %+v", e)
	}
}

func TestCancelStopsDelivery(t *testing.T) {
	s := openStore(t)
	bus := NewBus(s)
	p1 := profile(t, s, "A")
	sub, cancel := bus.Subscribe(p1, 0)
	cancel()
	bus.Publish(context.Background(), "library.changed", "", map[string]any{})
	select {
	case _, open := <-sub:
		if open {
			t.Fatal("canceled subscriber must not receive")
		}
	case <-time.After(100 * time.Millisecond):
	}
}
