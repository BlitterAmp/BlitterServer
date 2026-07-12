package httpserver

import (
	"context"
	"testing"

	"github.com/BlitterAmp/BlitterServer/internal/library"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

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
