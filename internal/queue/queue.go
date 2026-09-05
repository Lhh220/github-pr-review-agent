package queue

import (
	"context"
	"time"
)

type Message struct {
	TaskID  uint64 `json:"task_id"`
	Attempt int    `json:"attempt,omitempty"`
}

type Action int

const (
	Ack Action = iota
	NackDiscard
	NackRequeue
)

type Handler func(ctx context.Context, msg Message) Action

type Publisher interface {
	Publish(ctx context.Context, taskID uint64) error
}

type RetryPublisher interface {
	PublishRetry(ctx context.Context, taskID uint64, attempt int, delay time.Duration) error
	PublishDeadLetter(ctx context.Context, taskID uint64, attempt int) error
}

type Consumer interface {
	Consume(ctx context.Context, handler Handler) error
}
