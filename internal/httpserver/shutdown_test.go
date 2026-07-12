package httpserver

import (
	"context"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/library"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

type shutdownEnricher struct {
	started  chan struct{}
	canceled chan struct{}
	release  chan struct{}
}

func (e *shutdownEnricher) Run(ctx context.Context) {
	close(e.started)
	<-ctx.Done()
	close(e.canceled)
	<-e.release
}

func TestShutdownCompletesApplicationBeforeStoreClose(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := New("127.0.0.1:0", st, library.NewManager(st, dir), dir, "test")
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("store close after synchronous shutdown: %v", err)
	}
}

func TestShutdownCancelsAndJoinsEnrichment(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	mgr := library.NewManager(st, dir)
	srv := New("127.0.0.1:0", st, mgr, dir, "test")
	e := &shutdownEnricher{started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{})}
	mgr.SetEnricher(e)
	mgr.TriggerEnrichment()
	<-e.started

	done := make(chan error, 1)
	go func() { done <- srv.Shutdown(context.Background()) }()
	select {
	case <-e.canceled:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel enrichment")
	}
	select {
	case <-done:
		t.Fatal("shutdown returned before enrichment exited")
	default:
	}
	close(e.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not join enrichment")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}
