package storage_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"workflow-engine/internal/models"
	"workflow-engine/internal/storage"
)

func newMemoryStore(t *testing.T) *storage.SQLiteStorage {
	t.Helper()
	s, err := storage.NewSQLiteStorage(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		err := s.Close()
		assert.NoError(t, err)
	})
	return s
}

func makeWorkflow(id string) *models.Workflow {
	now := time.Now().UTC().Truncate(time.Second) // SQLite TEXT has nanosecond precision but Truncate avoids flakiness in equality checks
	return &models.Workflow{
		ID:        id,
		Status:    models.StatusPending,
		Input:     json.RawMessage(`{"x":1}`),
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func makeTasks(workflowID string, n int) []models.Task {
	tasks := make([]models.Task, n)
	for i := range n {
		tasks[i] = models.Task{
			ID:         workflowID + "-task-" + string(rune('A'+i)),
			WorkflowID: workflowID,
			Type:       models.TaskTypePrint,
			Status:     models.StatusPending,
			Position:   i,
			Config:     json.RawMessage(`{"template":"hello"}`),
		}
	}
	return tasks
}

func TestNewSQLiteStorage_SchemaApplied(t *testing.T) {
	t.Parallel()
	s := newMemoryStore(t)
	ctx := context.Background()

	wfs, err := s.ListWorkflows(ctx, -1, 0)
	require.NoError(t, err)
	assert.Empty(t, wfs)
}

func TestCreateWorkflow_Roundtrip(t *testing.T) {
	t.Parallel()
	s := newMemoryStore(t)
	ctx := context.Background()

	wf := makeWorkflow("wf-1")
	tasks := makeTasks("wf-1", 3)

	err := s.CreateWorkflow(ctx, wf, tasks)
	require.NoError(t, err)

	gotWF, gotTasks, err := s.GetWorkflow(ctx, "wf-1")
	require.NoError(t, err)

	assert.Equal(t, wf.ID, gotWF.ID)
	assert.Equal(t, models.StatusPending, gotWF.Status)

	require.Len(t, gotTasks, 3)

	for i, task := range gotTasks {
		assert.Equal(t, i, task.Position)
		assert.Equal(t, models.StatusPending, task.Status)
		assert.Equal(t, "wf-1", task.WorkflowID)
	}
}

func TestCreateWorkflow_EmptyTasksAllowed(t *testing.T) {
	t.Parallel()
	s := newMemoryStore(t)
	ctx := context.Background()

	wf := makeWorkflow("wf-empty")
	err := s.CreateWorkflow(ctx, wf, nil)
	require.NoError(t, err)

	_, tasks, err := s.GetWorkflow(ctx, "wf-empty")
	require.NoError(t, err)
	assert.Empty(t, tasks)
}

func TestCreateWorkflow_DuplicateIDFails(t *testing.T) {
	t.Parallel()
	s := newMemoryStore(t)
	ctx := context.Background()

	wf := makeWorkflow("wf-dup")
	err := s.CreateWorkflow(ctx, wf, nil)
	require.NoError(t, err)

	// Second insert with the same primary key must fail (ACID / uniqueness).
	err = s.CreateWorkflow(ctx, wf, nil)
	assert.Error(t, err, "expected error on duplicate workflow ID")
}

func TestGetWorkflow_NotFound(t *testing.T) {
	t.Parallel()
	s := newMemoryStore(t)
	ctx := context.Background()

	_, _, err := s.GetWorkflow(ctx, "does-not-exist")
	assert.Error(t, err, "expected error for missing workflow")
}

func TestGetWorkflow_TasksAscendingByPosition(t *testing.T) {
	t.Parallel()
	s := newMemoryStore(t)
	ctx := context.Background()

	wf := makeWorkflow("wf-order")

	tasks := []models.Task{
		{ID: "t3", WorkflowID: "wf-order", Type: models.TaskTypePrint, Status: models.StatusPending, Position: 2, Config: json.RawMessage(`{}`)},
		{ID: "t1", WorkflowID: "wf-order", Type: models.TaskTypePrint, Status: models.StatusPending, Position: 0, Config: json.RawMessage(`{}`)},
		{ID: "t2", WorkflowID: "wf-order", Type: models.TaskTypePrint, Status: models.StatusPending, Position: 1, Config: json.RawMessage(`{}`)},
	}
	err := s.CreateWorkflow(ctx, wf, tasks)
	require.NoError(t, err)

	_, gotTasks, err := s.GetWorkflow(ctx, "wf-order")
	require.NoError(t, err)
	require.Len(t, gotTasks, 3)

	for i, task := range gotTasks {
		assert.Equal(t, i, task.Position)
	}
}

func TestUpdateTask(t *testing.T) {
	t.Parallel()
	s := newMemoryStore(t)
	ctx := context.Background()

	wf := makeWorkflow("wf-ut")
	tasks := makeTasks("wf-ut", 1)
	err := s.CreateWorkflow(ctx, wf, tasks)
	require.NoError(t, err)

	// Mutate and persist.
	tasks[0].Status = models.StatusCompleted
	tasks[0].Output = "the-result"
	tasks[0].Error = ""
	err = s.UpdateTask(ctx, &tasks[0])
	require.NoError(t, err)

	// Reload and verify.
	_, gotTasks, err := s.GetWorkflow(ctx, "wf-ut")
	require.NoError(t, err)
	require.Len(t, gotTasks, 1)
	got := gotTasks[0]
	assert.Equal(t, models.StatusCompleted, got.Status)
	assert.Equal(t, "the-result", got.Output)
}

func TestUpdateTask_NotFound(t *testing.T) {
	t.Parallel()
	s := newMemoryStore(t)
	ctx := context.Background()

	ghost := &models.Task{ID: "ghost-id", Status: models.StatusFailed}
	err := s.UpdateTask(ctx, ghost)
	assert.Error(t, err, "expected error for updating non-existent task")
}

func TestUpdateWorkflowStatus(t *testing.T) {
	t.Parallel()
	s := newMemoryStore(t)
	ctx := context.Background()

	wf := makeWorkflow("wf-uws")
	err := s.CreateWorkflow(ctx, wf, nil)
	require.NoError(t, err)

	err = s.UpdateWorkflowStatus(ctx, "wf-uws", models.StatusRunning)
	require.NoError(t, err)

	gotWF, _, err := s.GetWorkflow(ctx, "wf-uws")
	require.NoError(t, err)
	assert.Equal(t, models.StatusRunning, gotWF.Status)
}

func TestUpdateWorkflowStatus_NotFound(t *testing.T) {
	t.Parallel()
	s := newMemoryStore(t)
	ctx := context.Background()

	err := s.UpdateWorkflowStatus(ctx, "ghost", models.StatusFailed)
	assert.Error(t, err, "expected error for non-existent workflow")
}

func TestListWorkflows(t *testing.T) {
	t.Parallel()
	s := newMemoryStore(t)
	ctx := context.Background()

	// Insert three workflows with intentionally different creation times.
	ids := []string{"wf-a", "wf-b", "wf-c"}
	for i, id := range ids {
		wf := &models.Workflow{
			ID:        id,
			Status:    models.StatusPending,
			CreatedAt: time.Now().UTC().Add(time.Duration(i) * time.Second),
			UpdatedAt: time.Now().UTC(),
		}
		err := s.CreateWorkflow(ctx, wf, nil)
		require.NoError(t, err)
	}

	wfs, err := s.ListWorkflows(ctx, -1, 0)
	require.NoError(t, err)
	require.Len(t, wfs, 3)

	// Verify ascending created_at order.
	for i, wf := range wfs {
		assert.Equal(t, ids[i], wf.ID)
	}
}

func TestListWorkflows_Pagination(t *testing.T) {
	t.Parallel()
	s := newMemoryStore(t)
	ctx := context.Background()

	// Insert three workflows with intentionally different creation times.
	ids := []string{"wf-a", "wf-b", "wf-c"}
	for i, id := range ids {
		wf := &models.Workflow{
			ID:        id,
			Status:    models.StatusPending,
			CreatedAt: time.Now().UTC().Add(time.Duration(i) * time.Second),
			UpdatedAt: time.Now().UTC(),
		}
		err := s.CreateWorkflow(ctx, wf, nil)
		require.NoError(t, err)
	}

	// limit = 2, offset = 0 -> "wf-a", "wf-b"
	wfs, err := s.ListWorkflows(ctx, 2, 0)
	require.NoError(t, err)
	require.Len(t, wfs, 2)
	assert.Equal(t, "wf-a", wfs[0].ID)
	assert.Equal(t, "wf-b", wfs[1].ID)

	// limit = 2, offset = 1 -> "wf-b", "wf-c"
	wfs, err = s.ListWorkflows(ctx, 2, 1)
	require.NoError(t, err)
	require.Len(t, wfs, 2)
	assert.Equal(t, "wf-b", wfs[0].ID)
	assert.Equal(t, "wf-c", wfs[1].ID)

	// limit = 1, offset = 2 -> "wf-c"
	wfs, err = s.ListWorkflows(ctx, 1, 2)
	require.NoError(t, err)
	require.Len(t, wfs, 1)
	assert.Equal(t, "wf-c", wfs[0].ID)

	// limit = 5, offset = 5 -> empty
	wfs, err = s.ListWorkflows(ctx, 5, 5)
	require.NoError(t, err)
	assert.Empty(t, wfs)
}

func TestListWorkflows_EmptyDatabase(t *testing.T) {
	t.Parallel()
	s := newMemoryStore(t)
	ctx := context.Background()

	wfs, err := s.ListWorkflows(ctx, -1, 0)
	require.NoError(t, err)
	assert.Empty(t, wfs)
}

// TestCreateWorkflow_TransactionRollbackOnBadTask verifies that if any task insert would fail (duplicate task ID),
// the entire CreateWorkflow transaction is rolled back - the workflow row must not exist afterwards.
func TestCreateWorkflow_TransactionRollbackOnBadTask(t *testing.T) {
	t.Parallel()
	s := newMemoryStore(t)
	ctx := context.Background()

	wf := makeWorkflow("wf-atomic")
	tasks := []models.Task{
		{ID: "dup-task", WorkflowID: "wf-atomic", Type: models.TaskTypePrint, Status: models.StatusPending, Position: 0, Config: json.RawMessage(`{}`)},
		{ID: "dup-task", WorkflowID: "wf-atomic", Type: models.TaskTypePrint, Status: models.StatusPending, Position: 1, Config: json.RawMessage(`{}`)}, // duplicate ID → constraint violation
	}

	err := s.CreateWorkflow(ctx, wf, tasks)
	assert.Error(t, err, "expected error due to duplicate task ID")

	_, _, getErr := s.GetWorkflow(ctx, "wf-atomic")
	assert.Error(t, getErr, "workflow was persisted despite task insert failure")
}

func TestListRecoverableWorkflows(t *testing.T) {
	t.Parallel()
	s := newMemoryStore(t)
	ctx := context.Background()

	// Create workflows with different statuses and creation times
	now := time.Now().UTC()
	workflows := []*models.Workflow{
		{ID: "wf-pending-1", Status: models.StatusPending, CreatedAt: now.Add(-10 * time.Second), UpdatedAt: now},
		{ID: "wf-completed", Status: models.StatusCompleted, CreatedAt: now.Add(-5 * time.Second), UpdatedAt: now},
		{ID: "wf-running", Status: models.StatusRunning, CreatedAt: now.Add(-2 * time.Second), UpdatedAt: now},
		{ID: "wf-failed", Status: models.StatusFailed, CreatedAt: now.Add(-1 * time.Second), UpdatedAt: now},
		{ID: "wf-pending-2", Status: models.StatusPending, CreatedAt: now, UpdatedAt: now},
	}

	for _, wf := range workflows {
		err := s.CreateWorkflow(ctx, wf, nil)
		require.NoError(t, err)
	}

	recoverable, err := s.ListRecoverableWorkflows(ctx, "", now.Add(time.Minute), 10)
	require.NoError(t, err)
	require.Len(t, recoverable, 3)

	// Verify order is sorted by ID ascending and only Pending/Running statuses
	assert.Equal(t, "wf-pending-1", recoverable[0].ID)
	assert.Equal(t, models.StatusPending, recoverable[0].Status)

	assert.Equal(t, "wf-pending-2", recoverable[1].ID)
	assert.Equal(t, models.StatusPending, recoverable[1].Status)

	assert.Equal(t, "wf-running", recoverable[2].ID)
	assert.Equal(t, models.StatusRunning, recoverable[2].Status)

	// Test batch size / limit
	firstBatch, err := s.ListRecoverableWorkflows(ctx, "", now.Add(time.Minute), 2)
	require.NoError(t, err)
	require.Len(t, firstBatch, 2)
	assert.Equal(t, "wf-pending-1", firstBatch[0].ID)
	assert.Equal(t, "wf-pending-2", firstBatch[1].ID)

	// Test keyset pagination with afterID
	secondBatch, err := s.ListRecoverableWorkflows(ctx, "wf-pending-2", now.Add(time.Minute), 2)
	require.NoError(t, err)
	require.Len(t, secondBatch, 1)
	assert.Equal(t, "wf-running", secondBatch[0].ID)
}

func TestListRecoverableWorkflowsBeforeTimeGuard(t *testing.T) {
	t.Parallel()
	s := newMemoryStore(t)
	ctx := context.Background()

	baseTime := time.Now().UTC()

	// 1. Seed a workflow created before baseTime
	wfBefore := &models.Workflow{
		ID:        "wf-before",
		Status:    models.StatusPending,
		CreatedAt: baseTime.Add(-10 * time.Second),
		UpdatedAt: baseTime,
	}
	err := s.CreateWorkflow(ctx, wfBefore, nil)
	require.NoError(t, err)

	// 2. Seed a workflow created after baseTime
	wfAfter := &models.Workflow{
		ID:        "wf-after",
		Status:    models.StatusPending,
		CreatedAt: baseTime.Add(10 * time.Second),
		UpdatedAt: baseTime,
	}
	err = s.CreateWorkflow(ctx, wfAfter, nil)
	require.NoError(t, err)

	// 3. Query using baseTime as boundary
	recoverable, err := s.ListRecoverableWorkflows(ctx, "", baseTime, 10)
	require.NoError(t, err)

	// 4. Assert only wfBefore is returned
	require.Len(t, recoverable, 1)
	assert.Equal(t, "wf-before", recoverable[0].ID)
}
