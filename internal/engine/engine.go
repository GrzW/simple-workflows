package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"workflow-engine/internal/models"
	"workflow-engine/internal/storage"
)

const (
	defaultJobsBufferSize = 128
	stopTimeout           = 30 * time.Second
)

var ErrQueueFull = errors.New("engine: job queue is full")

type Engine struct {
	store       storage.Store
	concurrency int
	demoDelay   time.Duration
	jobs        chan string
	wg          sync.WaitGroup
	startOnce   sync.Once
	stopOnce    sync.Once
	ctx         context.Context    // operational context for DB operations - never cancelled by OS signals
	cancel      context.CancelFunc // cancels ctx as a last resort after stop timeout
	shutdownCtx context.Context    // parent context used only for shutdown detection
	logger      *slog.Logger
	startTime   time.Time
}

func NewEngine(store storage.Store, concurrency int) *Engine {
	if concurrency <= 0 {
		concurrency = 1
	}

	var demoDelay time.Duration
	if delayStr := os.Getenv("DEMO_TASK_DELAY"); delayStr != "" {
		if d, parseErr := time.ParseDuration(delayStr); parseErr == nil {
			demoDelay = d
		}
	}

	return &Engine{
		store:       store,
		concurrency: concurrency,
		demoDelay:   demoDelay,
		jobs:        make(chan string, defaultJobsBufferSize),
		logger:      slog.Default().With("component", "engine"),
		startTime:   time.Now().UTC(),
	}
}

// Start spawns the worker pool. It is idempotent; calling it more than once has no effect.
//
// The supplied ctx is used only for shutdown detection, database operations use a separate,
// Background-derived context so that in-flight writes can complete even after
// the parent context is cancelled.
func (e *Engine) Start(ctx context.Context) {
	e.startOnce.Do(func() {
		e.shutdownCtx = ctx
		e.ctx, e.cancel = context.WithCancel(context.Background())
		e.logger.InfoContext(ctx, "starting engine", "concurrency", e.concurrency)
		for i := range e.concurrency {
			e.wg.Add(1)

			go e.worker(i)
		}
	})
}

// Submit puts a workflow ID into the queue. Returns ErrQueueFull if the queue is full.
func (e *Engine) Submit(ctx context.Context, workflowID string) error {
	select {
	case e.jobs <- workflowID:
		e.logger.InfoContext(ctx, "submitted workflow", "workflow_id", workflowID)
		return nil
	default:
		e.logger.WarnContext(ctx, "jobs queue full, dropped workflow", "workflow_id", workflowID)
		return ErrQueueFull
	}
}

// Stop closes the job channel and waits for active workers, enforcing a timeout before cancelling the context.
func (e *Engine) Stop() {
	e.stopOnce.Do(func() {
		e.logger.Info("stopping engine, draining job queue")
		close(e.jobs)

		done := make(chan struct{})
		go func() {
			e.wg.Wait()
			close(done)
		}()

		select {
		case <-done:
			e.logger.Info("all workers finished cleanly")
		case <-time.After(stopTimeout):
			e.logger.Warn("stop timeout exceeded, canceling context", "timeout", stopTimeout)
		}

		if e.cancel != nil {
			e.cancel()
		}
	})
}

// RecoverOutstandingWork re-submits Pending or Running workflows from the database in batches.
func (e *Engine) RecoverOutstandingWork(ctx context.Context) error {
	const batchSize = 100
	var lastID string
	totalRecovered := 0

	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("engine: recover outstanding work: %w", err)
		}

		workflows, err := e.store.ListRecoverableWorkflows(ctx, lastID, e.startTime, batchSize)
		if err != nil {
			return fmt.Errorf("engine: recover outstanding work: %w", err)
		}

		if len(workflows) == 0 {
			break
		}

		for _, wf := range workflows {
			if err = e.submitBlocking(ctx, wf.ID); err != nil {
				return fmt.Errorf("submit recovered workflow %s: %w", wf.ID, err)
			}
			lastID = wf.ID
			totalRecovered++
		}
	}

	if totalRecovered > 0 {
		e.logger.InfoContext(ctx, "finished recovery", "total_recovered", totalRecovered)
	}

	return nil
}

func (e *Engine) submitBlocking(ctx context.Context, workflowID string) error {
	select {
	case e.jobs <- workflowID:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *Engine) worker(id int) {
	defer e.wg.Done()
	e.logger.InfoContext(e.ctx, "worker started", "worker_id", id)

	for workflowID := range e.jobs {
		e.logger.InfoContext(e.ctx, "picked up workflow", "worker_id", id, "workflow_id", workflowID)
		if err := e.processWorkflow(e.ctx, workflowID); err != nil {
			e.logger.ErrorContext(e.ctx, "workflow failed", "worker_id", id, "workflow_id", workflowID, "error", err)
		}
	}

	e.logger.InfoContext(e.ctx, "worker exiting", "worker_id", id)
}

// processWorkflow runs the workflow tasks. It is idempotent: completed tasks are skipped.
func (e *Engine) processWorkflow(ctx context.Context, workflowID string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			panicErr := fmt.Errorf("panic recovered during execution: %v", r)
			e.logger.ErrorContext(ctx, "CRITICAL: panic in workflow execution",
				"workflow_id", workflowID,
				"error", panicErr,
			)

			_ = e.failWorkflow(ctx, workflowID, panicErr)
			err = panicErr
		}
	}()

	wf, tasks, err := e.store.GetWorkflow(ctx, workflowID)
	if err != nil {
		return fmt.Errorf("get workflow: %w", err)
	}

	e.logger.InfoContext(ctx, "starting execution", "workflow_id", wf.ID, "status", wf.Status, "tasks_count", len(tasks))

	if wf.Status == models.StatusPending {
		if err = e.store.UpdateWorkflowStatus(ctx, wf.ID, models.StatusRunning); err != nil {
			return fmt.Errorf("set workflow running: %w", err)
		}
		wf.Status = models.StatusRunning
	}

	ec, err := e.prepareExecutionContext(wf, tasks)
	if err != nil {
		return e.failWorkflow(ctx, wf.ID, err)
	}

	for i := range tasks {
		if err = e.runTask(ctx, wf.ID, &tasks[i], ec); err != nil {
			return err
		}
	}

	if err = e.store.UpdateWorkflowStatus(ctx, wf.ID, models.StatusCompleted); err != nil {
		return fmt.Errorf("mark workflow completed: %w", err)
	}

	e.logger.InfoContext(ctx, "workflow completed successfully", "workflow_id", wf.ID)

	return nil
}

func (e *Engine) prepareExecutionContext(wf *models.Workflow, tasks []models.Task) (*ExecutionContext, error) {
	ec := &ExecutionContext{
		input: make(map[string]any),
		steps: make(map[int]string),
	}

	if len(wf.Input) > 0 {
		if err := json.Unmarshal(wf.Input, &ec.input); err != nil {
			return nil, fmt.Errorf("parse workflow input: %w", err)
		}
	}

	for i := range tasks {
		if tasks[i].Status == models.StatusCompleted {
			ec.steps[tasks[i].Position] = tasks[i].Output
		}
	}

	return ec, nil
}

func (e *Engine) runTask(ctx context.Context, workflowID string, task *models.Task, ec *ExecutionContext) error {
	if err := e.shutdownCtx.Err(); err != nil {
		return fmt.Errorf("workflow aborted due to engine shutdown: %w", err)
	}

	logger := e.logger.With(
		"workflow_id", workflowID,
		"task_id", task.ID,
		"status", task.Status,
		"task_type", task.Type,
		"position", task.Position,
	)

	if task.Status == models.StatusCompleted {
		logger.InfoContext(ctx, "task already completed, skipping")

		return nil
	}

	task.Status = models.StatusRunning
	if err := e.store.UpdateTask(ctx, task); err != nil {
		return fmt.Errorf("mark task running (id=%s): %w", task.ID, err)
	}

	logger.InfoContext(ctx, "executing task")

	if e.demoDelay > 0 {
		select {
		case <-time.After(e.demoDelay):
		case <-e.shutdownCtx.Done():
			return fmt.Errorf("workflow aborted during demo delay: %w", e.shutdownCtx.Err())
		}
	}

	output, execErr := e.executeTask(task, ec)
	if execErr != nil {
		logger.ErrorContext(ctx, "task failed", "error", execErr)
		task.Status = models.StatusFailed
		task.Error = execErr.Error()
		if updateErr := e.store.UpdateTask(ctx, task); updateErr != nil {
			logger.WarnContext(ctx, "could not persist task failure", "error", updateErr)
		}

		return e.failWorkflow(ctx, workflowID, fmt.Errorf("task %s (pos=%d): %w", task.ID, task.Position, execErr))
	}

	task.Status = models.StatusCompleted
	task.Output = output
	ec.steps[task.Position] = output

	if err := e.store.UpdateTask(ctx, task); err != nil {
		return fmt.Errorf("persist task success (id=%s): %w", task.ID, err)
	}

	logger.InfoContext(ctx, "task completed", "output", output)

	return nil
}

func (e *Engine) executeTask(task *models.Task, ec *ExecutionContext) (string, error) {
	switch task.Type {
	case models.TaskTypeCalculate:
		return calculateHandler(task, ec)
	case models.TaskTypePrint:
		return printHandler(task, ec)
	default:
		return "", fmt.Errorf("unknown task type %q", task.Type)
	}
}

func (e *Engine) failWorkflow(ctx context.Context, workflowID string, cause error) error {
	if updateErr := e.store.UpdateWorkflowStatus(ctx, workflowID, models.StatusFailed); updateErr != nil {
		e.logger.WarnContext(ctx, "could not persist workflow failed status", "workflow_id", workflowID, "error", updateErr)
	}
	e.logger.InfoContext(ctx, "workflow marked as failed", "workflow_id", workflowID)

	return cause
}
