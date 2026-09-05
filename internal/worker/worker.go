package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"strings"
	"time"

	"github.com/liaohonghui/github-pr-review-agent/internal/queue"
	"github.com/liaohonghui/github-pr-review-agent/internal/store"
)

const reviewTimeout = 5 * time.Minute
const recoveryScanInterval = 30 * time.Second
const runningTaskStaleAfter = reviewTimeout + time.Minute
const queuedTaskStaleAfter = time.Minute

type Reviewer interface {
	ReviewPR(ctx context.Context, owner, repo string, number int, taskID uint64) error
}

type TaskStore interface {
	GetTask(ctx context.Context, id uint64) (*store.Task, error)
	ClaimTask(ctx context.Context, id uint64, attempt int, now time.Time) (bool, error)
	UpdateTaskStatus(ctx context.Context, id uint64, status, taskError string) error
	MarkTaskRetry(ctx context.Context, id uint64, attempt, maxAttempts int, taskError string, nextRetryAt time.Time) error
	MarkTaskDeadLetter(ctx context.Context, id uint64, attempt, maxAttempts int, taskError string) error
	ListRecoverableTasks(ctx context.Context, staleBefore time.Time, limit int) ([]store.Task, error)
	ListStaleQueuedTasks(ctx context.Context, staleBefore time.Time, limit int) ([]store.Task, error)
	TouchQueuedTask(ctx context.Context, id uint64, updatedAt time.Time) error
}

type QueueClient interface {
	queue.Publisher
	queue.Consumer
	queue.RetryPublisher
}

type Options struct {
	MaxAttempts    int
	RetryBaseDelay time.Duration
	RetryMaxDelay  time.Duration
	RetryJitter    time.Duration
}

type Worker struct {
	store          TaskStore
	reviewer       Reviewer
	client         QueueClient
	workers        int
	maxAttempts    int
	retryBaseDelay time.Duration
	retryMaxDelay  time.Duration
	retryJitter    time.Duration
	jitter         func(max time.Duration) time.Duration
}

func New(taskStore TaskStore, reviewer Reviewer, client QueueClient, workers int, options Options) *Worker {
	if workers <= 0 {
		workers = 1
	}
	if options.MaxAttempts <= 0 {
		options.MaxAttempts = 3
	}
	if options.RetryBaseDelay <= 0 {
		options.RetryBaseDelay = 30 * time.Second
	}
	if options.RetryMaxDelay < options.RetryBaseDelay {
		options.RetryMaxDelay = 10 * time.Minute
	}
	if options.RetryJitter < 0 {
		options.RetryJitter = 5 * time.Second
	}
	return &Worker{
		store:          taskStore,
		reviewer:       reviewer,
		client:         client,
		workers:        workers,
		maxAttempts:    options.MaxAttempts,
		retryBaseDelay: options.RetryBaseDelay,
		retryMaxDelay:  options.RetryMaxDelay,
		retryJitter:    options.RetryJitter,
		jitter:         randomJitter,
	}
}

func (w *Worker) Start(ctx context.Context) error {
	go w.recoverStaleTasks(ctx)

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
	return w.client.Consume(ctx, handler)
}

func (w *Worker) recoverStaleTasks(ctx context.Context) {
	ticker := time.NewTicker(recoveryScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.recoverStaleRunningOnce(ctx)
			w.recoverStaleQueuedOnce(ctx)
		}
	}
}

func (w *Worker) recoverStaleRunningOnce(ctx context.Context) {
	listCtx, cancelList := context.WithTimeout(ctx, 5*time.Second)
	defer cancelList()
	staleBefore := time.Now().Add(-runningTaskStaleAfter)
	tasks, err := w.store.ListRecoverableTasks(listCtx, staleBefore, 100)
	if err != nil {
		log.Printf("list stale running review tasks failed: error=%v", err)
		return
	}

	for _, task := range tasks {
		reviewError := "review worker stopped before completion"
		msg := queue.Message{TaskID: task.ID, Attempt: task.AttemptCount}
		if nextAttempt(&task, msg) < w.maxAttempts {
			w.recoverRetry(ctx, &task, msg, reviewError)
			continue
		}
		w.deadLetter(&task, msg, reviewError)
	}
}

func (w *Worker) recoverStaleQueuedOnce(ctx context.Context) {
	listCtx, cancelList := context.WithTimeout(ctx, 5*time.Second)
	defer cancelList()
	staleBefore := time.Now().Add(-queuedTaskStaleAfter)
	tasks, err := w.store.ListStaleQueuedTasks(listCtx, staleBefore, 100)
	if err != nil {
		log.Printf("list stale queued review tasks failed: error=%v", err)
		return
	}

	for _, task := range tasks {
		publishCtx, cancelPublish := context.WithTimeout(ctx, 10*time.Second)
		if err := w.client.Publish(publishCtx, task.ID); err != nil {
			log.Printf("publish queued recovery failed: task_id=%d error=%v", task.ID, err)
			cancelPublish()
			continue
		}
		cancelPublish()

		touchCtx, cancelTouch := context.WithTimeout(ctx, 5*time.Second)
		if err := w.store.TouchQueuedTask(touchCtx, task.ID, time.Now()); err != nil {
			if errors.Is(err, store.ErrTaskTransitionFailed) {
				log.Printf(
					"queued recovery raced with worker: task_id=%d duplicate message will be ignored",
					task.ID,
				)
			} else {
				log.Printf("touch queued recovery failed: task_id=%d error=%v", task.ID, err)
			}
			cancelTouch()
			continue
		}
		cancelTouch()
		log.Printf("requeued stale queued review task: task_id=%d", task.ID)
	}
}

func (w *Worker) recoverRetry(ctx context.Context, task *store.Task, msg queue.Message, reviewError string) {
	attempt := nextAttempt(task, msg)
	delay := w.retryDelay(attempt)
	nextRetryAt := time.Now().Add(delay)

	publishCtx, cancelPublish := context.WithTimeout(ctx, 10*time.Second)
	defer cancelPublish()
	if err := w.client.PublishRetry(publishCtx, task.ID, attempt, delay); err != nil {
		log.Printf("publish recovery retry failed: task_id=%d attempt=%d error=%v", task.ID, attempt, err)
		return
	}

	markCtx, cancelMark := context.WithTimeout(ctx, 5*time.Second)
	defer cancelMark()
	if err := w.store.MarkTaskRetry(markCtx, task.ID, attempt, w.maxAttempts, reviewError, nextRetryAt); err != nil {
		log.Printf("mark recovered review task retrying failed: task_id=%d attempt=%d error=%v", task.ID, attempt, err)
		return
	}
	log.Printf(
		"recovered stale running review task: task_id=%d attempt=%d next_retry_at=%s",
		task.ID, attempt, nextRetryAt.Format(time.RFC3339),
	)
}

func (w *Worker) process(msg queue.Message) (action queue.Action) {
	taskID := msg.TaskID
	var task *store.Task
	defer func() {
		if recovered := recover(); recovered != nil {
			message := fmt.Sprintf("review worker panicked: %v", recovered)
			log.Printf("%s task_id=%d", message, taskID)
			if task != nil {
				action = w.fail(task, msg, message)
				return
			}
			w.updateStatus(taskID, "failed", message)
			action = queue.Ack
		}
	}()

	getCtx, cancelGet := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelGet()
	var err error
	task, err = w.store.GetTask(getCtx, taskID)
	if err != nil {
		if errors.Is(err, store.ErrTaskNotFound) {
			log.Printf("review task not found: task_id=%d", taskID)
			return queue.NackDiscard
		}
		log.Printf("get review task failed: task_id=%d error=%v", taskID, err)
		return queue.NackRequeue
	}

	now := time.Now()
	switch {
	case task.Status == "done", task.Status == "dead_letter", task.Status == "failed":
		return queue.Ack
	case task.Status == "retrying" && task.NextRetryAt != nil && task.NextRetryAt.After(now):
		return queue.Ack
	case task.Status == "running" && msg.Attempt <= task.AttemptCount:
		return queue.Ack
	}

	claimCtx, cancelClaim := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelClaim()
	claimed, err := w.store.ClaimTask(claimCtx, taskID, msg.Attempt, now)
	if err != nil {
		log.Printf("claim review task failed: task_id=%d error=%v", taskID, err)
		return queue.NackRequeue
	}
	if !claimed {
		return queue.Ack
	}

	owner, repo, validRepo := splitRepo(task.Repo)
	if !validRepo {
		return w.deadLetter(task, msg, "invalid repository name")
	}

	reviewCtx, cancelReview := context.WithTimeout(context.Background(), reviewTimeout)
	defer cancelReview()
	if err := w.reviewer.ReviewPR(reviewCtx, owner, repo, task.PRNumber, taskID); err != nil {
		log.Printf("review pr failed: owner=%s repo=%s number=%d task_id=%d error=%v", owner, repo, task.PRNumber, taskID, err)
		return w.fail(task, msg, err.Error())
	}

	if err := w.updateStatus(taskID, "done", ""); err != nil {
		log.Printf("mark review task done failed: task_id=%d error=%v", taskID, err)
	}
	return queue.Ack
}

func (w *Worker) fail(task *store.Task, msg queue.Message, reviewError string) queue.Action {
	attempt := nextAttempt(task, msg)
	if attempt < w.maxAttempts {
		return w.retry(task, msg, attempt, reviewError)
	}
	return w.deadLetter(task, msg, reviewError)
}

func (w *Worker) retry(task *store.Task, msg queue.Message, attempt int, reviewError string) queue.Action {
	delay := w.retryDelay(attempt)
	nextRetryAt := time.Now().Add(delay)

	publishCtx, cancelPublish := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelPublish()
	if err := w.client.PublishRetry(publishCtx, task.ID, attempt, delay); err != nil {
		log.Printf("publish retry task failed: task_id=%d attempt=%d error=%v", task.ID, attempt, err)
		w.updateStatus(task.ID, "failed", reviewError)
		return queue.Ack
	}

	markCtx, cancelMark := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelMark()
	if err := w.store.MarkTaskRetry(markCtx, task.ID, attempt, w.maxAttempts, reviewError, nextRetryAt); err != nil {
		log.Printf("mark review task retrying failed: task_id=%d attempt=%d error=%v", task.ID, attempt, err)
	}
	log.Printf(
		"review task scheduled for retry: task_id=%d attempt=%d max_attempts=%d next_retry_at=%s",
		task.ID, attempt, w.maxAttempts, nextRetryAt.Format(time.RFC3339),
	)
	return queue.Ack
}

func (w *Worker) deadLetter(task *store.Task, msg queue.Message, reviewError string) queue.Action {
	attempt := nextAttempt(task, msg)

	publishCtx, cancelPublish := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelPublish()
	if err := w.client.PublishDeadLetter(publishCtx, task.ID, attempt); err != nil {
		log.Printf("publish dead letter task failed: task_id=%d attempt=%d error=%v", task.ID, attempt, err)
		w.updateStatus(task.ID, "failed", reviewError)
		return queue.Ack
	}

	markCtx, cancelMark := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelMark()
	if err := w.store.MarkTaskDeadLetter(markCtx, task.ID, attempt, w.maxAttempts, reviewError); err != nil {
		log.Printf("mark review task dead letter failed: task_id=%d attempt=%d error=%v", task.ID, attempt, err)
	}
	log.Printf(
		"review task moved to dead letter: task_id=%d attempt=%d max_attempts=%d",
		task.ID, attempt, w.maxAttempts,
	)
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

func nextAttempt(task *store.Task, msg queue.Message) int {
	attempt := task.AttemptCount
	if msg.Attempt > attempt {
		attempt = msg.Attempt
	}
	return attempt + 1
}

func (w *Worker) retryDelay(attempt int) time.Duration {
	delay := w.retryBaseDelay
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= w.retryMaxDelay {
			delay = w.retryMaxDelay
			break
		}
	}
	if delay > w.retryMaxDelay {
		delay = w.retryMaxDelay
	}
	if w.retryJitter <= 0 {
		return delay
	}

	jitterRoom := w.retryMaxDelay - delay
	if w.retryJitter < jitterRoom {
		jitterRoom = w.retryJitter
	}
	return delay + w.jitter(jitterRoom)
}

func randomJitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(max)))
}

func splitRepo(repo string) (string, string, bool) {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}
