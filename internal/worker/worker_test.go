package worker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/liaohonghui/github-pr-review-agent/internal/queue"
	"github.com/liaohonghui/github-pr-review-agent/internal/store"
)

type fakeTaskGetter struct {
	mu sync.Mutex

	task        *store.Task
	getErr      error
	claimErr    error
	updateErr   error
	claimResult bool
	statuses    []string
	retries     []retryRecord
	deadLetters []deadLetterRecord
	recoverable []store.Task
}

type retryRecord struct {
	TaskID      uint64
	Attempt     int
	MaxAttempts int
	Error       string
	NextRetryAt time.Time
}

type deadLetterRecord struct {
	TaskID      uint64
	Attempt     int
	MaxAttempts int
	Error       string
}

func (f *fakeTaskGetter) GetTask(ctx context.Context, id uint64) (*store.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.task, nil
}

func (f *fakeTaskGetter) UpdateTaskStatus(ctx context.Context, id uint64, status, taskError string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updateErr != nil {
		return f.updateErr
	}
	f.statuses = append(f.statuses, status)
	return nil
}

func (f *fakeTaskGetter) ClaimTask(ctx context.Context, id uint64, attempt int, now time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claimErr != nil {
		return false, f.claimErr
	}
	if f.claimResult {
		f.statuses = append(f.statuses, "running")
	}
	return f.claimResult, nil
}

func (f *fakeTaskGetter) MarkTaskRetry(ctx context.Context, id uint64, attempt, maxAttempts int, taskError string, nextRetryAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = append(f.statuses, "retrying")
	f.retries = append(f.retries, retryRecord{TaskID: id, Attempt: attempt, MaxAttempts: maxAttempts, Error: taskError, NextRetryAt: nextRetryAt})
	return nil
}

func (f *fakeTaskGetter) MarkTaskDeadLetter(ctx context.Context, id uint64, attempt, maxAttempts int, taskError string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = append(f.statuses, "dead_letter")
	f.deadLetters = append(f.deadLetters, deadLetterRecord{TaskID: id, Attempt: attempt, MaxAttempts: maxAttempts, Error: taskError})
	return nil
}

func (f *fakeTaskGetter) ListRecoverableTasks(ctx context.Context, staleBefore time.Time, limit int) ([]store.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.Task(nil), f.recoverable...), nil
}

func (f *fakeTaskGetter) recordedStatuses() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.statuses...)
}

type fakeReviewer struct {
	mu sync.Mutex

	err   error
	panic bool
	calls []reviewCall
}

type reviewCall struct {
	Owner  string
	Repo   string
	Number int
	TaskID uint64
}

func (f *fakeReviewer) ReviewPR(ctx context.Context, owner, repo string, number int, taskID uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, reviewCall{Owner: owner, Repo: repo, Number: number, TaskID: taskID})
	if f.panic {
		panic("review exploded")
	}
	return f.err
}

func (f *fakeReviewer) recordedCalls() []reviewCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]reviewCall(nil), f.calls...)
}

type captureConsumer struct {
	handler     queue.Handler
	retries     []retryPublish
	deadLetters []deadLetterPublish
}

type retryPublish struct {
	TaskID  uint64
	Attempt int
	Delay   time.Duration
}

type deadLetterPublish struct {
	TaskID  uint64
	Attempt int
}

func (c *captureConsumer) Consume(ctx context.Context, handler queue.Handler) error {
	c.handler = handler
	return nil
}

func (c *captureConsumer) PublishRetry(ctx context.Context, taskID uint64, attempt int, delay time.Duration) error {
	c.retries = append(c.retries, retryPublish{TaskID: taskID, Attempt: attempt, Delay: delay})
	return nil
}

func (c *captureConsumer) PublishDeadLetter(ctx context.Context, taskID uint64, attempt int) error {
	c.deadLetters = append(c.deadLetters, deadLetterPublish{TaskID: taskID, Attempt: attempt})
	return nil
}

func newWorkerTestTask(status string) *store.Task {
	return &store.Task{
		ID:          1,
		Repo:        "owner/repo",
		PRNumber:    12,
		Status:      status,
		MaxAttempts: 3,
	}
}

func TestProcessRunsQueuedTask(t *testing.T) {
	taskStore := &fakeTaskGetter{task: newWorkerTestTask("queued"), claimResult: true}
	reviewer := &fakeReviewer{}
	w := New(taskStore, reviewer, &captureConsumer{}, 1, Options{})

	action := w.process(queue.Message{TaskID: 1})
	if action != queue.Ack {
		t.Fatalf("action = %d, want %d", action, queue.Ack)
	}
	if got := taskStore.recordedStatuses(); len(got) != 2 || got[0] != "running" || got[1] != "done" {
		t.Fatalf("statuses = %v, want [running done]", got)
	}
	calls := reviewer.recordedCalls()
	if len(calls) != 1 || calls[0].Owner != "owner" || calls[0].Repo != "repo" || calls[0].Number != 12 || calls[0].TaskID != 1 {
		t.Fatalf("unexpected review calls: %+v", calls)
	}
}

func TestProcessSchedulesRetryWhenReviewFails(t *testing.T) {
	taskStore := &fakeTaskGetter{task: newWorkerTestTask("queued"), claimResult: true}
	reviewer := &fakeReviewer{err: errors.New("llm unavailable")}
	client := &captureConsumer{}
	w := New(taskStore, reviewer, client, 1, Options{})

	action := w.process(queue.Message{TaskID: 1})
	if action != queue.Ack {
		t.Fatalf("action = %d, want %d", action, queue.Ack)
	}
	if got := taskStore.recordedStatuses(); len(got) != 2 || got[0] != "running" || got[1] != "retrying" {
		t.Fatalf("statuses = %v, want [running retrying]", got)
	}
	if len(client.retries) != 1 || client.retries[0].TaskID != 1 || client.retries[0].Attempt != 1 {
		t.Fatalf("unexpected retry publishes: %+v", client.retries)
	}
}

func TestProcessMovesTaskToDeadLetterAfterMaxAttempts(t *testing.T) {
	task := newWorkerTestTask("queued")
	task.AttemptCount = 2
	taskStore := &fakeTaskGetter{task: task, claimResult: true}
	reviewer := &fakeReviewer{panic: true}
	client := &captureConsumer{}
	w := New(taskStore, reviewer, client, 1, Options{})

	action := w.process(queue.Message{TaskID: 1})
	if action != queue.Ack {
		t.Fatalf("action = %d, want %d", action, queue.Ack)
	}
	if got := taskStore.recordedStatuses(); len(got) != 2 || got[0] != "running" || got[1] != "dead_letter" {
		t.Fatalf("statuses = %v, want [running dead_letter]", got)
	}
	if len(client.deadLetters) != 1 || client.deadLetters[0].TaskID != 1 || client.deadLetters[0].Attempt != 3 {
		t.Fatalf("unexpected dead letters: %+v", client.deadLetters)
	}
}

func TestRecoverOnceSchedulesStaleRunningTask(t *testing.T) {
	task := newWorkerTestTask("running")
	task.AttemptCount = 0
	task.UpdatedAt = time.Now().Add(-10 * time.Minute)
	taskStore := &fakeTaskGetter{recoverable: []store.Task{*task}}
	client := &captureConsumer{}
	w := New(taskStore, &fakeReviewer{}, client, 1, Options{})

	w.recoverOnce(context.Background())

	if len(client.retries) != 1 || client.retries[0].TaskID != task.ID || client.retries[0].Attempt != 1 {
		t.Fatalf("unexpected recovery retries: %+v", client.retries)
	}
	if got := taskStore.recordedStatuses(); len(got) != 1 || got[0] != "retrying" {
		t.Fatalf("statuses = %v, want [retrying]", got)
	}
}

func TestProcessSkipsAlreadyDoneTask(t *testing.T) {
	taskStore := &fakeTaskGetter{task: newWorkerTestTask("done"), claimResult: true}
	reviewer := &fakeReviewer{}
	w := New(taskStore, reviewer, &captureConsumer{}, 1, Options{})

	action := w.process(queue.Message{TaskID: 1})
	if action != queue.Ack {
		t.Fatalf("action = %d, want %d", action, queue.Ack)
	}
	if got := taskStore.recordedStatuses(); len(got) != 0 {
		t.Fatalf("done task was processed again: statuses=%v", got)
	}
	if calls := reviewer.recordedCalls(); len(calls) != 0 {
		t.Fatalf("done task was reviewed again: %+v", calls)
	}
}

func TestProcessDiscardsMissingTask(t *testing.T) {
	taskStore := &fakeTaskGetter{getErr: store.ErrTaskNotFound, claimResult: true}
	reviewer := &fakeReviewer{}
	w := New(taskStore, reviewer, &captureConsumer{}, 1, Options{})

	action := w.process(queue.Message{TaskID: 99})
	if action != queue.NackDiscard {
		t.Fatalf("action = %d, want %d", action, queue.NackDiscard)
	}
	if calls := reviewer.recordedCalls(); len(calls) != 0 {
		t.Fatalf("missing task was reviewed: %+v", calls)
	}
}

func TestStartUsesConsumerHandler(t *testing.T) {
	taskStore := &fakeTaskGetter{task: newWorkerTestTask("queued"), claimResult: true}
	reviewer := &fakeReviewer{}
	consumer := &captureConsumer{}
	w := New(taskStore, reviewer, consumer, 2, Options{})

	if err := w.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if consumer.handler == nil {
		t.Fatal("worker did not register a consumer handler")
	}
	if action := consumer.handler(context.Background(), queue.Message{TaskID: 1}); action != queue.Ack {
		t.Fatalf("handler action = %d, want %d", action, queue.Ack)
	}
	if got := taskStore.recordedStatuses(); len(got) != 2 || got[0] != "running" || got[1] != "done" {
		t.Fatalf("statuses = %v, want [running done]", got)
	}
}
