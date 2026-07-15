package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"workflow-engine/internal/models"

	_ "modernc.org/sqlite" // register the "sqlite" driver
)

// schema contains every DDL statement needed to bootstrap the database.
const schema = `
CREATE TABLE IF NOT EXISTS workflows (
    id         TEXT PRIMARY KEY,
    status     TEXT NOT NULL DEFAULT 'Pending',
    input      TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS tasks (
    id          TEXT PRIMARY KEY,
    workflow_id TEXT NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    type        TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'Pending',
    position    INTEGER NOT NULL,
    config      TEXT NOT NULL DEFAULT '',
    output      TEXT NOT NULL DEFAULT '',
    error       TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_tasks_workflow_id ON tasks(workflow_id);
CREATE INDEX IF NOT EXISTS idx_workflows_created_at ON workflows(created_at);
`

// SQLiteStorage implements Store using a SQLite database.
// It must be constructed via NewSQLiteStorage; do not use the zero value.
type SQLiteStorage struct {
	db *sql.DB
}

var memDBCount uint64

// NewSQLiteStorage opens (or creates) the SQLite database at dbPath, applies concurrency-safe PRAGMA settings,
// and runs the schema initialisation script.
func NewSQLiteStorage(dbPath string) (*SQLiteStorage, error) {
	var dsn string
	if dbPath == ":memory:" {
		count := atomic.AddUint64(&memDBCount, 1)
		dsn = fmt.Sprintf("file:memdb-%d?mode=memory&cache=shared&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)", count)
	} else {
		dir := filepath.Dir(dbPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("storage: create database directory %s: %w", dir, err)
		}
		cleanedPath := filepath.ToSlash(dbPath)
		dsn = fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)", cleanedPath)
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("storage: open database: %w", err)
	}

	// With WAL mode a single writer and multiple readers can coexist. We
	// allow a larger pool size to enable concurrent reads while relying on
	// SQLite's busy_timeout and WAL to manage write serialization safely.
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(5 * time.Minute)

	s := &SQLiteStorage{db: db}

	if err = s.initSchema(); err != nil {
		_ = db.Close()

		return nil, fmt.Errorf("storage: initialise schema: %w", err)
	}

	return s, nil
}

// Close releases the underlying connection pool.
func (s *SQLiteStorage) Close() error {
	return s.db.Close()
}

func (s *SQLiteStorage) initSchema() error {
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("exec schema: %w", err)
	}

	return nil
}

func timeToString(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func stringToTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse time %q: %w", s, err)
	}

	return t, nil
}

func rawToString(r json.RawMessage) string {
	if len(r) == 0 {
		return ""
	}

	return string(r)
}

func stringToRaw(s string) json.RawMessage {
	if s == "" {
		return nil
	}

	return json.RawMessage(s)
}

// CreateWorkflow persists a new workflow and all of its tasks in a single ACID transaction. If any insert fails
// the transaction is rolled back and neither the workflow nor any task is written.
func (s *SQLiteStorage) CreateWorkflow(ctx context.Context, wf *models.Workflow, tasks []models.Task) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin transaction: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	const insertWorkflow = `
		INSERT INTO workflows (id, status, input, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)`

	if _, err = tx.ExecContext(ctx, insertWorkflow,
		wf.ID, string(wf.Status), rawToString(wf.Input), timeToString(wf.CreatedAt), timeToString(wf.UpdatedAt),
	); err != nil {
		return fmt.Errorf("storage: insert workflow %s: %w", wf.ID, err)
	}

	const insertTask = `
		INSERT INTO tasks (id, workflow_id, type, status, position, config, output, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

	stmt, err := tx.PrepareContext(ctx, insertTask)
	if err != nil {
		return fmt.Errorf("storage: prepare task insert: %w", err)
	}
	defer stmt.Close()

	for i := range tasks {
		t := &tasks[i]
		if _, err = stmt.ExecContext(ctx,
			t.ID, t.WorkflowID, string(t.Type), string(t.Status), t.Position, rawToString(t.Config), t.Output, t.Error,
		); err != nil {
			return fmt.Errorf("storage: insert task %s (pos %d): %w", t.ID, t.Position, err)
		}
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit transaction: %w", err)
	}

	return nil
}

// GetWorkflow retrieves a workflow by ID along with all its tasks ordered by
// Position ascending. Returns an error wrapping sql.ErrNoRows if not found.
func (s *SQLiteStorage) GetWorkflow(ctx context.Context, id string) (*models.Workflow, []models.Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("storage: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	const queryWorkflow = `SELECT id, status, input, created_at, updated_at FROM workflows WHERE id = ?`

	row := tx.QueryRowContext(ctx, queryWorkflow, id)

	wf, err := scanWorkflow(row)
	if err != nil {
		return nil, nil, fmt.Errorf("storage: get workflow %s: %w", id, err)
	}

	const queryTasks = `SELECT id, workflow_id, type, status, position, config, output, error
		FROM tasks WHERE workflow_id = ? ORDER BY position ASC`

	rows, err := tx.QueryContext(ctx, queryTasks, id)
	if err != nil {
		return nil, nil, fmt.Errorf("storage: query tasks for workflow %s: %w", id, err)
	}
	defer rows.Close()

	var (
		t     models.Task
		tasks []models.Task
	)
	for rows.Next() {
		t, err = scanTask(rows)
		if err != nil {
			return nil, nil, fmt.Errorf("storage: scan task row: %w", err)
		}
		tasks = append(tasks, t)
	}

	if err = rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("storage: iterate task rows: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("storage: commit transaction: %w", err)
	}

	return wf, tasks, nil
}

// ListWorkflows returns workflows ordered by created_at ascending.
// If limit >= 0, it applies LIMIT limit OFFSET offset.
// If limit < 0, it retrieves all workflows.
func (s *SQLiteStorage) ListWorkflows(ctx context.Context, limit, offset int) ([]models.Workflow, error) {
	var query string
	var args []any

	if limit >= 0 {
		query = `SELECT id, status, input, created_at, updated_at
			FROM workflows ORDER BY created_at ASC LIMIT ? OFFSET ?`
		args = []any{limit, offset}
	} else {
		query = `SELECT id, status, input, created_at, updated_at FROM workflows ORDER BY created_at ASC`
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: list workflows: %w", err)
	}
	defer rows.Close()

	var workflows []models.Workflow

	for rows.Next() {
		wf, err := scanWorkflow(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan workflow row: %w", err)
		}

		workflows = append(workflows, *wf)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: iterate workflow rows: %w", err)
	}

	return workflows, nil
}

// ListRecoverableWorkflows returns recoverable workflows (Pending or Running) ordered by ID ascending, starting after
// the given afterID, up to the given batch size, but only including those created before beforeTime.
func (s *SQLiteStorage) ListRecoverableWorkflows(ctx context.Context, afterID string, beforeTime time.Time, batchSize int) ([]models.Workflow, error) {
	const query = `
		SELECT id, status, input, created_at, updated_at
		FROM workflows
		WHERE (status = ? OR status = ?) AND id > ? AND created_at < ?
		ORDER BY id ASC
		LIMIT ?`

	rows, err := s.db.QueryContext(ctx, query, string(models.StatusPending), string(models.StatusRunning), afterID, timeToString(beforeTime), batchSize)
	if err != nil {
		return nil, fmt.Errorf("storage: list recoverable workflows: %w", err)
	}
	defer rows.Close()

	var workflows []models.Workflow
	for rows.Next() {
		wf, err := scanWorkflow(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan workflow row: %w", err)
		}
		workflows = append(workflows, *wf)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: iterate workflow rows: %w", err)
	}

	return workflows, nil
}

// UpdateTask writes the mutable fields of a task (Status, Output, Error)
// back to the database.
func (s *SQLiteStorage) UpdateTask(ctx context.Context, task *models.Task) error {
	const query = `
		UPDATE tasks
		SET    status = ?, output = ?, error = ?
		WHERE  id = ?`

	result, err := s.db.ExecContext(ctx, query,
		string(task.Status),
		task.Output,
		task.Error,
		task.ID,
	)
	if err != nil {
		return fmt.Errorf("storage: update task %s: %w", task.ID, err)
	}

	if err = expectOneRow(result, "task", task.ID); err != nil {
		return err
	}

	return nil
}

// UpdateWorkflowStatus sets Status and refreshes UpdatedAt for the workflow.
func (s *SQLiteStorage) UpdateWorkflowStatus(
	ctx context.Context,
	workflowID string,
	status models.Status,
) error {
	const query = `
		UPDATE workflows
		SET    status = ?, updated_at = ?
		WHERE  id = ?`

	result, err := s.db.ExecContext(ctx, query,
		string(status),
		timeToString(time.Now().UTC()),
		workflowID,
	)
	if err != nil {
		return fmt.Errorf("storage: update workflow status %s: %w", workflowID, err)
	}

	if err = expectOneRow(result, "workflow", workflowID); err != nil {
		return err
	}

	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanWorkflow(s scanner) (*models.Workflow, error) {
	var (
		wf         models.Workflow
		statusStr  string
		inputStr   string
		createdStr string
		updatedStr string
	)

	if err := s.Scan(&wf.ID, &statusStr, &inputStr, &createdStr, &updatedStr); err != nil {
		return nil, err
	}

	wf.Status = models.Status(statusStr)
	wf.Input = stringToRaw(inputStr)

	var err error
	if wf.CreatedAt, err = stringToTime(createdStr); err != nil {
		return nil, fmt.Errorf("created_at: %w", err)
	}
	if wf.UpdatedAt, err = stringToTime(updatedStr); err != nil {
		return nil, fmt.Errorf("updated_at: %w", err)
	}

	return &wf, nil
}

// scanTask reads one task row from s.
func scanTask(s scanner) (models.Task, error) {
	var (
		t         models.Task
		typeStr   string
		statusStr string
		configStr string
	)

	if err := s.Scan(
		&t.ID,
		&t.WorkflowID,
		&typeStr,
		&statusStr,
		&t.Position,
		&configStr,
		&t.Output,
		&t.Error,
	); err != nil {
		return models.Task{}, err
	}

	t.Type = models.TaskType(typeStr)
	t.Status = models.Status(statusStr)
	t.Config = stringToRaw(configStr)

	return t, nil
}

func expectOneRow(result sql.Result, kind, id string) error {
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("storage: rows affected for %s %s: %w", kind, id, err)
	}
	if n == 0 {
		return fmt.Errorf("storage: %s %s not found", kind, id)
	}
	return nil
}
