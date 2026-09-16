package queue

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestPublishFailsFastOnClosedBroker(t *testing.T) {
	broker := &RabbitBroker{queue: "review.tasks"}
	if err := broker.Close(); err != nil {
		t.Fatalf("close broker: %v", err)
	}
	err := broker.Publish(context.Background(), 1)
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("publish on closed broker: err=%v, want closed error", err)
	}
}

func TestPublishReturnsUnavailableWhenReconnectFails(t *testing.T) {
	// Port 1 refuses immediately, so the reconnect attempt fails fast
	// without needing a real broker.
	broker := &RabbitBroker{url: "amqp://guest:guest@127.0.0.1:1/", queue: "review.tasks"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := broker.Publish(ctx, 1)
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("publish with dead broker: err=%v, want unavailable error", err)
	}
}
