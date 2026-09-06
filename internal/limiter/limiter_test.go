package limiter

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRedisLimiterAllowsConfiguredCallsThenWaits(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	limiter := NewRedisLimiter(client, 2, 100*time.Millisecond)

	for i := 0; i < 2; i++ {
		if err := limiter.Wait(context.Background(), "test:scope"); err != nil {
			t.Fatalf("Wait() call %d error = %v", i+1, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := limiter.Wait(ctx, "test:scope"); err == nil {
		t.Fatal("third Wait() was allowed before the window reset")
	}

	server.FastForward(110 * time.Millisecond)
	if err := limiter.Wait(context.Background(), "test:scope"); err != nil {
		t.Fatalf("Wait() after window reset error = %v", err)
	}
}
