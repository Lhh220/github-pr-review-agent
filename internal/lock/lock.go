package lock

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

var ErrNotHeld = errors.New("lock is not held")

type Lock interface {
	Release(ctx context.Context) error
}

type Locker interface {
	Acquire(ctx context.Context, key string, ttl time.Duration) (Lock, bool, error)
}

type RedisLocker struct {
	client *redis.Client
}

func NewRedisLocker(client *redis.Client) *RedisLocker {
	return &RedisLocker{client: client}
}

func (l *RedisLocker) Acquire(ctx context.Context, key string, ttl time.Duration) (Lock, bool, error) {
	if key == "" {
		return nil, false, errors.New("lock key is required")
	}
	if ttl <= 0 {
		return nil, false, errors.New("lock ttl must be positive")
	}

	token, err := randomToken()
	if err != nil {
		return nil, false, fmt.Errorf("generate lock token: %w", err)
	}
	acquired, err := l.client.SetNX(ctx, "lock:"+key, token, ttl).Result()
	if err != nil {
		return nil, false, fmt.Errorf("acquire redis lock: %w", err)
	}
	if !acquired {
		return nil, false, nil
	}
	return &redisLock{client: l.client, key: "lock:" + key, token: token}, true, nil
}

type redisLock struct {
	client *redis.Client
	key    string
	token  string
}

var releaseScript = redis.NewScript(`
local token = redis.call("GET", KEYS[1])
if token == false then
	return 1
end
if token ~= ARGV[1] then
	return 0
end
redis.call("DEL", KEYS[1])
return 1
`)

func (l *redisLock) Release(ctx context.Context) error {
	result, err := releaseScript.Run(ctx, l.client, []string{l.key}, l.token).Int()
	if err != nil {
		return fmt.Errorf("release redis lock: %w", err)
	}
	if result != 1 {
		return ErrNotHeld
	}
	return nil
}

type noopLock struct{}

func (noopLock) Release(ctx context.Context) error { return nil }

type noopLocker struct{}

func NewNoopLocker() Locker { return noopLocker{} }

func (noopLocker) Acquire(ctx context.Context, key string, ttl time.Duration) (Lock, bool, error) {
	return noopLock{}, true, nil
}

func randomToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
