package lock

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRedisLockerAllowsOnlyOneHolder(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	locker := NewRedisLocker(client)

	first, acquired, err := locker.Acquire(context.Background(), "review:pr:owner/repo:12", time.Minute)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if !acquired {
		t.Fatal("first Acquire() was not acquired")
	}

	if _, acquired, err := locker.Acquire(context.Background(), "review:pr:owner/repo:12", time.Minute); err != nil || acquired {
		t.Fatalf("second Acquire() = acquired=%v error=%v, want false and nil", acquired, err)
	}

	if err := first.Release(context.Background()); err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	third, acquired, err := locker.Acquire(context.Background(), "review:pr:owner/repo:12", time.Minute)
	if err != nil {
		t.Fatalf("Acquire() after release error = %v", err)
	}
	if !acquired {
		t.Fatal("Acquire() after release was not acquired")
	}
	if err := third.Release(context.Background()); err != nil {
		t.Fatalf("Release() after reacquire error = %v", err)
	}
}

func TestRedisLockReleaseRejectsForeignToken(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	locker := NewRedisLocker(client)

	first, acquired, err := locker.Acquire(context.Background(), "review:pr:owner/repo:12", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("Acquire() = acquired=%v error=%v, want true and nil", acquired, err)
	}

	key := "lock:review:pr:owner/repo:12"
	server.Set(key, "foreign-token")
	if err := first.Release(context.Background()); !errors.Is(err, ErrNotHeld) {
		t.Fatalf("Release() error = %v, want ErrNotHeld", err)
	}
	if got, err := server.Get(key); err != nil || got != "foreign-token" {
		t.Fatalf("foreign lock was deleted: value=%q", got)
	}
}
