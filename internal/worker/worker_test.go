package worker

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/liaohonghui/github-pr-review-agent/internal/queue"
	"github.com/liaohonghui/github-pr-review-agent/internal/store"
)

type fakeTaskGetter struct {
	mu sync.Mutex

	task      *store.Task
	getErr    error
	updateErr error
	statuses  []string
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
	handler queue.Handler
}

func (c *captureConsumer) Consume(ctx context.Context, handler queue.Handler) error {
	c.handler = handler
	return nil
}

func newWorkerTestTask(status string) *store.Task {
	return &store.Task{
		ID:       1,
		Repo:     "owner/repo",
		PRNumber: 12,
		Status:   status,
	}
}

func TestProcessRunsQueuedTask(t *testing.T) {
	taskStore := &fakeTaskGetter{task: newWorkerTestTask("queued")}
	reviewer := &fakeReviewer{}
	w := New(taskStore, reviewer, &captureConsumer{}, 1)

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

func TestProcessMarksFailedTaskWhenReviewFails(t *testing.T) {
	taskStore := &fakeTaskGetter{task: newWorkerTestTask("queued")}
	reviewer := &fakeReviewer{err: errors.New("llm unavailable")}
	w := New(taskStore, reviewer, &captureConsumer{}, 1)

	action := w.process(queue.Message{TaskID: 1})
	if action != queue.Ack {
		t.Fatalf("action = %d, want %d", action, queue.Ack)
	}
	if got := taskStore.recordedStatuses(); len(got) != 2 || got[0] != "running" || got[1] != "failed" {
		t.Fatalf("statuses = %v, want [running failed]", got)
	}
}

func TestProcessMarksFailedTaskWhenReviewPanics(t *testing.T) {
	taskStore := &fakeTaskGetter{task: newWorkerTestTask("queued")}
	reviewer := &fakeReviewer{panic: true}
	w := New(taskStore, reviewer, &captureConsumer{}, 1)

	action := w.process(queue.Message{TaskID: 1})
	if action != queue.Ack {
		t.Fatalf("action = %d, want %d", action, queue.Ack)
	}
	if got := taskStore.recordedStatuses(); len(got) != 2 || got[0] != "running" || got[1] != "failed" {
		t.Fatalf("statuses = %v, want [running failed]", got)
	}
}

func TestProcessSkipsAlreadyDoneTask(t *testing.T) {
	taskStore := &fakeTaskGetter{task: newWorkerTestTask("done")}
	reviewer := &fakeReviewer{}
	w := New(taskStore, reviewer, &captureConsumer{}, 1)

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
	taskStore := &fakeTaskGetter{getErr: store.ErrTaskNotFound}
	reviewer := &fakeReviewer{}
	w := New(taskStore, reviewer, &captureConsumer{}, 1)

	action := w.process(queue.Message{TaskID: 99})
	if action != queue.NackDiscard {
		t.Fatalf("action = %d, want %d", action, queue.NackDiscard)
	}
	if calls := reviewer.recordedCalls(); len(calls) != 0 {
		t.Fatalf("missing task was reviewed: %+v", calls)
	}
}

func TestStartUsesConsumerHandler(t *testing.T) {
	taskStore := &fakeTaskGetter{task: newWorkerTestTask("queued")}
	reviewer := &fakeReviewer{}
	consumer := &captureConsumer{}
	w := New(taskStore, reviewer, consumer, 2)

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
