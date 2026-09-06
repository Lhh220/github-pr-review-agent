package limiter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type Limiter interface {
	Wait(ctx context.Context, scope string) error
}

type NoopLimiter struct{}

func (NoopLimiter) Wait(ctx context.Context, scope string) error { return nil }

type RedisLimiter struct {
	client     *redis.Client
	limit      int
	window     time.Duration
	retryDelay time.Duration
}

func NewRedisLimiter(client *redis.Client, limit int, window time.Duration) *RedisLimiter {
	if limit <= 0 {
		limit = 1
	}
	if window <= 0 {
		window = time.Minute
	}
	retryDelay := window
	if retryDelay > 500*time.Millisecond {
		retryDelay = 500 * time.Millisecond
	}
	return &RedisLimiter{
		client:     client,
		limit:      limit,
		window:     window,
		retryDelay: retryDelay,
	}
}

var allowScript = redis.NewScript(`
local count = redis.call("INCR", KEYS[1])
if count == 1 then
	redis.call("PEXPIRE", KEYS[1], ARGV[1])
end
if count <= tonumber(ARGV[2]) then
	return 1
end
return 0
`)

func (l *RedisLimiter) Wait(ctx context.Context, scope string) error {
	scope = strings.ToLower(strings.TrimSpace(scope))
	if scope == "" {
		return errors.New("rate limit scope is required")
	}
	key := "ratelimit:" + scope

	for {
		allowed, err := allowScript.Run(
			ctx,
			l.client,
			[]string{key},
			l.window.Milliseconds(),
			l.limit,
		).Int()
		if err != nil {
			return fmt.Errorf("check redis rate limit: %w", err)
		}
		if allowed == 1 {
			return nil
		}

		delay := l.retryDelay
		ttl, ttlErr := l.client.TTL(ctx, key).Result()
		if ttlErr == nil && ttl > 0 && ttl < delay {
			delay = ttl
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
