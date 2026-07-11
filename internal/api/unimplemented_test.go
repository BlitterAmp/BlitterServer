package api

import (
	"context"
	"errors"
	"testing"
)

// Compile-time proof the base satisfies the full contract.
var _ StrictServerInterface = Unimplemented{}

func TestUnimplementedReturnsErrNotImplemented(t *testing.T) {
	_, err := Unimplemented{}.GetStatus(context.Background(), GetStatusRequestObject{})
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("want ErrNotImplemented, got %v", err)
	}
}
