package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/liaohonghui/github-pr-review-agent/internal/queue"
	"github.com/liaohonghui/github-pr-review-agent/internal/store"
)

const reviewTimeout = 5 * time.Minute

type Reviewer interface {
	ReviewPR(ctx context.Context, owner, repo string, number int, taskID uint64) error
}

type TaskStore interface {
	GetTask(ctx context.Context, id uint64) (*store.Task, error)
	UpdateTaskStatus(ctx context.Context, id uint64, status, taskError string) error
}

type Worker struct {
	store    TaskStore
	reviewer Reviewer
	consumer queue.Consumer
	workers  int
}

func New(taskStore TaskStore, reviewer Reviewer, consumer queue.Consumer, workers int) *Worker {
	if workers <= 0 {
		workers = 1
	}
	return &Worker{
		store:    taskStore,
		reviewer: reviewer,
		consumer: consumer,
		workers:  workers,
	}
}

func (w *Worker) Start(ctx context.Context) error {
	slots := make(chan struct{}, w.workers)
	handler := func(ctx context.Context, msg queue.Message) queue.Action {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return queue.NackRequeue
		}

		action := w.process(msg)
		<-slots
		return action
	}
	return w.consumer.Consume(ctx, handler)
}

func (w *Worker) process(msg queue.Message) (action queue.Action) {
	taskID := msg.TaskID
	defer func() {
		if recovered := recover(); recovered != nil {
			message := fmt.Sprintf("review worker panicked: %v", recovered)
			log.Printf("%s task_id=%d", message, taskID)
			w.updateStatus(taskID, "failed", message)
			action = queue.Ack
		}
	}()

	getCtx, cancelGet := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelGet()
	task, err := w.store.GetTask(getCtx, taskID)
	if err != nil {
		if errors.Is(err, store.ErrTaskNotFound) {
			log.Printf("review task not found: task_id=%d", taskID)
			return queue.NackDiscard
		}
		log.Printf("get review task failed: task_id=%d error=%v", taskID, err)
		return queue.NackDiscard
	}
	if task.Status == "done" {
		return queue.Ack
	}

	owner, repo, validRepo := splitRepo(task.Repo)
	if !validRepo {
		w.updateStatus(taskID, "failed", "invalid repository name")
		return queue.Ack
	}

	if err := w.updateStatus(taskID, "running", ""); err != nil {
		log.Printf("mark review task running failed: task_id=%d error=%v", taskID, err)
		return queue.Ack
	}

	reviewCtx, cancelReview := context.WithTimeout(context.Background(), reviewTimeout)
	defer cancelReview()
	if err := w.reviewer.ReviewPR(reviewCtx, owner, repo, task.PRNumber, taskID); err != nil {
		log.Printf("review pr failed: owner=%s repo=%s number=%d task_id=%d error=%v", owner, repo, task.PRNumber, taskID, err)
		w.updateStatus(taskID, "failed", err.Error())
		return queue.Ack
	}

	if err := w.updateStatus(taskID, "done", ""); err != nil {
		log.Printf("mark review task done failed: task_id=%d error=%v", taskID, err)
	}
	return queue.Ack
}

func (w *Worker) updateStatus(taskID uint64, status, taskError string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.store.UpdateTaskStatus(ctx, taskID, status, taskError); err != nil {
		log.Printf("update review task status failed: task_id=%d status=%s error=%v", taskID, status, err)
		return err
	}
	return nil
}

func splitRepo(repo string) (string, string, bool) {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}
