package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const maxReconnectDelay = 30 * time.Second

type RabbitBroker struct {
	url      string
	queue    string
	prefetch int

	mu         sync.RWMutex
	connection *amqp.Connection
	publisher  *amqp.Channel
	consumer   *amqp.Channel
	closed     bool

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

	broker := &RabbitBroker{
		url:      url,
		queue:    queue,
		prefetch: prefetch,
	}
	if err := broker.connect(); err != nil {
		return nil, err
	}
	return broker, nil
}

func (b *RabbitBroker) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	b.closeLocked()
	return nil
}

func (b *RabbitBroker) connect() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return errors.New("rabbitmq broker is closed")
	}
	return b.connectLocked()
}

func (b *RabbitBroker) reconnect() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return errors.New("rabbitmq broker is closed")
	}
	if b.healthyLocked() {
		return nil
	}
	if err := b.connectLocked(); err != nil {
		return err
	}
	log.Printf("rabbitmq connection re-established: queue=%s", b.queue)
	return nil
}

func (b *RabbitBroker) connectLocked() error {
	b.closeLocked()

	conn, err := amqp.Dial(b.url)
	if err != nil {
		return fmt.Errorf("connect rabbitmq: %w", err)
	}

	declareCh, err := conn.Channel()
	if err != nil {
		conn.Close()
		return fmt.Errorf("open rabbitmq declare channel: %w", err)
	}
	if _, err := declareCh.QueueDeclare(
		b.queue,
		true,
		false,
		false,
		false,
		nil,
	); err != nil {
		declareCh.Close()
		conn.Close()
		return fmt.Errorf("declare rabbitmq queue: %w", err)
	}
	if err := declareCh.Close(); err != nil {
		conn.Close()
		return fmt.Errorf("close rabbitmq declare channel: %w", err)
	}

	publisher, err := conn.Channel()
	if err != nil {
		conn.Close()
		return fmt.Errorf("open rabbitmq publisher channel: %w", err)
	}
	if err := publisher.Confirm(false); err != nil {
		publisher.Close()
		conn.Close()
		return fmt.Errorf("enable rabbitmq publisher confirms: %w", err)
	}

	consumer, err := conn.Channel()
	if err != nil {
		publisher.Close()
		conn.Close()
		return fmt.Errorf("open rabbitmq consumer channel: %w", err)
	}
	if err := consumer.Qos(b.prefetch, 0, false); err != nil {
		consumer.Close()
		publisher.Close()
		conn.Close()
		return fmt.Errorf("set rabbitmq prefetch: %w", err)
	}

	b.connection = conn
	b.publisher = publisher
	b.consumer = consumer
	return nil
}

func (b *RabbitBroker) healthyLocked() bool {
	return b.connection != nil &&
		!b.connection.IsClosed() &&
		b.publisher != nil &&
		!b.publisher.IsClosed() &&
		b.consumer != nil &&
		!b.consumer.IsClosed()
}

func (b *RabbitBroker) closeLocked() {
	if b.consumer != nil {
		_ = b.consumer.Close()
		b.consumer = nil
	}
	if b.publisher != nil {
		_ = b.publisher.Close()
		b.publisher = nil
	}
	if b.connection != nil {
		_ = b.connection.Close()
		b.connection = nil
	}
}

func (b *RabbitBroker) currentPublisher() (*amqp.Channel, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return nil, errors.New("rabbitmq broker is closed")
	}
	if b.publisher == nil {
		return nil, errors.New("rabbitmq publisher is unavailable")
	}
	return b.publisher, nil
}

func (b *RabbitBroker) Publish(ctx context.Context, taskID uint64) error {
	body, err := json.Marshal(Message{TaskID: taskID})
	if err != nil {
		return fmt.Errorf("marshal review task message: %w", err)
	}

	b.publishMu.Lock()
	defer b.publishMu.Unlock()

	publisher, err := b.currentPublisher()
	if err != nil {
		if reconnectErr := b.reconnect(); reconnectErr != nil {
			log.Printf("reconnect rabbitmq publisher failed: error=%v", reconnectErr)
		}
		return fmt.Errorf("get rabbitmq publisher: %w", err)
	}

	confirmation, err := publisher.PublishWithDeferredConfirmWithContext(
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
		if reconnectErr := b.reconnect(); reconnectErr != nil {
			log.Printf("reconnect rabbitmq publisher failed: error=%v", reconnectErr)
		}
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
	attempt := 0
	for {
		b.mu.RLock()
		consumer := b.consumer
		closed := b.closed
		b.mu.RUnlock()
		if closed {
			return errors.New("rabbitmq broker is closed")
		}
		if consumer == nil {
			if err := b.reconnect(); err != nil {
				log.Printf("reconnect rabbitmq consumer failed: error=%v", err)
				if !waitRetry(ctx, attempt) {
					return ctx.Err()
				}
				attempt++
			}
			continue
		}

		deliveries, err := consumer.ConsumeWithContext(
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
			log.Printf("consume rabbitmq queue failed: error=%v", err)
			if reconnectErr := b.reconnect(); reconnectErr != nil {
				log.Printf("reconnect rabbitmq consumer failed: error=%v", reconnectErr)
			}
			if !waitRetry(ctx, attempt) {
				return ctx.Err()
			}
			attempt++
			continue
		}
		attempt = 0

		channelClosed := false
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case delivery, ok := <-deliveries:
				if !ok {
					log.Printf("rabbitmq delivery channel closed, reconnecting")
					channelClosed = true
				}
				if ok {
					b.handleDelivery(ctx, delivery, handler)
				}
			}
			if channelClosed {
				if err := b.reconnect(); err != nil {
					log.Printf("reconnect rabbitmq consumer failed: error=%v", err)
					if !waitRetry(ctx, attempt) {
						return ctx.Err()
					}
					attempt++
				}
				break
			}
		}
	}
}

func waitRetry(ctx context.Context, attempt int) bool {
	delay := 2 * time.Second * time.Duration(attempt+1)
	if delay > maxReconnectDelay {
		delay = maxReconnectDelay
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
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
