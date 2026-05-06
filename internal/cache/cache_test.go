package cache

import (
	"context"
	"testing"
	"time"
)

func TestGetSet(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := New(ctx, 1*time.Minute)

	if _, ok := c.Get("alice"); ok {
		t.Fatal("expected cache miss for unknown user")
	}

	c.Set("alice", []string{"ns1", "ns2"})
	got, ok := c.Get("alice")
	if !ok {
		t.Fatal("expected cache hit after set")
	}
	if len(got) != 2 || got[0] != "ns1" || got[1] != "ns2" {
		t.Fatalf("unexpected namespaces: %v", got)
	}
}

func TestGetReturnsCopy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := New(ctx, 1*time.Minute)
	c.Set("alice", []string{"ns1"})

	got, _ := c.Get("alice")
	got[0] = "mutated"

	got2, _ := c.Get("alice")
	if got2[0] != "ns1" {
		t.Fatal("Get returned a reference to internal data instead of a copy")
	}
}

func TestSetStoresCopy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := New(ctx, 1*time.Minute)
	input := []string{"ns1"}
	c.Set("alice", input)
	input[0] = "mutated"

	got, _ := c.Get("alice")
	if got[0] != "ns1" {
		t.Fatal("Set did not copy input data")
	}
}

func TestExpiry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := New(ctx, 50*time.Millisecond)
	c.Set("alice", []string{"ns1"})

	if _, ok := c.Get("alice"); !ok {
		t.Fatal("expected cache hit immediately after set")
	}

	time.Sleep(100 * time.Millisecond)

	if _, ok := c.Get("alice"); ok {
		t.Fatal("expected cache miss after expiry")
	}
}

func TestInvalidate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := New(ctx, 1*time.Minute)
	c.Set("alice", []string{"ns1"})
	c.Invalidate("alice")

	if _, ok := c.Get("alice"); ok {
		t.Fatal("expected cache miss after invalidation")
	}
}

func TestCleanupStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c := New(ctx, 10*time.Millisecond)
	c.Set("alice", []string{"ns1"})
	cancel()
	// Give the goroutine time to exit; this test mainly verifies no panic/hang.
	time.Sleep(50 * time.Millisecond)
}
