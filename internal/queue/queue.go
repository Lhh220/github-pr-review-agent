package queue

import "context"

type Message struct {
	TaskID uint64 `json:"task_id"`
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

type Consumer interface {
	Consume(ctx context.Context, handler Handler) error
}
