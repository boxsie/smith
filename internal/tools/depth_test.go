package tools

import (
	"context"
	"testing"
)

func TestDepth_FreshContext(t *testing.T) {
	if got := Depth(context.Background()); got != 0 {
		t.Errorf("Depth(background) = %d, want 0", got)
	}
}

func TestDepth_RoundTrip(t *testing.T) {
	ctx := WithDepth(context.Background(), 2)
	if got := Depth(ctx); got != 2 {
		t.Errorf("Depth = %d, want 2", got)
	}
}

func TestDepth_Nested(t *testing.T) {
	ctx := WithDepth(context.Background(), 1)
	ctx = WithDepth(ctx, 3)
	if got := Depth(ctx); got != 3 {
		t.Errorf("Depth = %d, want 3 (latest value)", got)
	}
}
