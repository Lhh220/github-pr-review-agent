package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"

	amqp "github.com/rabbitmq/amqp091-go"
)

type RabbitBroker struct {
	connection *amqp.Connection
	publisher  *amqp.Channel
	consumer   *amqp.Channel
	queue      string

	publishMu sync.Mutex
}

func OpenRabbit(url, queue string, prefetch int) (*RabbitBroker, error) {
	if url == "" {
		return nil, errors.New("rabbitmq url is required")
	}
	if queue == "" {
		return nil, errors.New("rabbitmq queue name is required")
	}
	if prefetch <= 0 {
		prefetch = 1
	}

	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, fmt.Errorf("connect rabbitmq: %w", err)
	}

	declareCh, err := conn.Channel()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("open rabbitmq declare channel: %w", err)
	}
	if _, err := declareCh.QueueDeclare(
		queue,
		true,
		false,
		false,
		false,
		nil,
	); err != nil {
		declareCh.Close()
		conn.Close()
		return nil, fmt.Errorf("declare rabbitmq queue: %w", err)
	}
	if err := declareCh.Close(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("close rabbitmq declare channel: %w", err)
	}

	publisher, err := conn.Channel()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("open rabbitmq publisher channel: %w", err)
	}
	if err := publisher.Confirm(false); err != nil {
		publisher.Close()
		conn.Close()
		return nil, fmt.Errorf("enable rabbitmq publisher confirms: %w", err)
	}

	consumer, err := conn.Channel()
	if err != nil {
		publisher.Close()
		conn.Close()
		return nil, fmt.Errorf("open rabbitmq consumer channel: %w", err)
	}
	if err := consumer.Qos(prefetch, 0, false); err != nil {
		consumer.Close()
		publisher.Close()
		conn.Close()
		return nil, fmt.Errorf("set rabbitmq prefetch: %w", err)
	}

	return &RabbitBroker{
		connection: conn,
		publisher:  publisher,
		consumer:   consumer,
		queue:      queue,
	}, nil
}

func (b *RabbitBroker) Close() error {
	b.publishMu.Lock()
	publisher := b.publisher
	b.publisher = nil
	b.publishMu.Unlock()

	if publisher != nil {
		_ = publisher.Close()
	}
	if b.consumer != nil {
		_ = b.consumer.Close()
	}
	if b.connection != nil {
		return b.connection.Close()
	}
	return nil
}

func (b *RabbitBroker) Publish(ctx context.Context, taskID uint64) error {
	body, err := json.Marshal(Message{TaskID: taskID})
	if err != nil {
		return fmt.Errorf("marshal review task message: %w", err)
	}

	b.publishMu.Lock()
	defer b.publishMu.Unlock()
	if b.publisher == nil {
		return errors.New("rabbitmq publisher is closed")
	}

	confirmation, err := b.publisher.PublishWithDeferredConfirmWithContext(
		ctx,
		"",
		b.queue,
		false,
		false,
		amqp.Publishing{
			ContentType:  "application/json",
			Body:         body,
			DeliveryMode: amqp.Persistent,
		},
	)
	if err != nil {
		return fmt.Errorf("publish review task: %w", err)
	}
	if confirmation == nil {
		return errors.New("rabbitmq publisher confirmation was not enabled")
	}

	acked, err := confirmation.WaitContext(ctx)
	if err != nil {
		return fmt.Errorf("wait rabbitmq publisher confirmation: %w", err)
	}
	if !acked {
		return errors.New("rabbitmq publisher nack")
	}
	return nil
}

func (b *RabbitBroker) Consume(ctx context.Context, handler Handler) error {
	deliveries, err := b.consumer.ConsumeWithContext(
		ctx,
		b.queue,
		"github-pr-review-agent",
		false,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		return fmt.Errorf("consume rabbitmq queue: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case delivery, ok := <-deliveries:
			if !ok {
				return errors.New("rabbitmq delivery channel closed")
			}
			b.handleDelivery(ctx, delivery, handler)
		}
	}
}

func (b *RabbitBroker) handleDelivery(ctx context.Context, delivery amqp.Delivery, handler Handler) {
	var msg Message
	if err := json.Unmarshal(delivery.Body, &msg); err != nil || msg.TaskID == 0 {
		log.Printf("discard invalid rabbitmq message: delivery_tag=%d body=%q", delivery.DeliveryTag, delivery.Body)
		if err := delivery.Nack(false, false); err != nil {
			log.Printf("nack invalid rabbitmq message failed: error=%v", err)
		}
		return
	}

	action := handler(ctx, msg)
	switch action {
	case Ack:
		if err := delivery.Ack(false); err != nil {
			log.Printf("ack rabbitmq message failed: task_id=%d error=%v", msg.TaskID, err)
		}
	case NackDiscard:
		if err := delivery.Nack(false, false); err != nil {
			log.Printf("discard rabbitmq message failed: task_id=%d error=%v", msg.TaskID, err)
		}
	case NackRequeue:
		if err := delivery.Nack(false, true); err != nil {
			log.Printf("requeue rabbitmq message failed: task_id=%d error=%v", msg.TaskID, err)
		}
	default:
		log.Printf("unknown rabbitmq action: task_id=%d action=%d", msg.TaskID, action)
		if err := delivery.Nack(false, false); err != nil {
			log.Printf("discard rabbitmq message failed: task_id=%d error=%v", msg.TaskID, err)
		}
	}
}
