// Package storage defines the persistence interface used by the workflow
// engine and provides a production-ready SQLite implementation.
package storage

import (
	"context"

	"workflow-engine/internal/models"
)

// Store is the persistence contract the engine depends on.
// Every method accepts a context.Context so callers can propagate
// deadlines and cancellation signals down to the database layer.
type Store interface {
	// CreateWorkflow persists a new workflow together with all of its tasks
	// inside a single ACID transaction. Either all rows are written or none.
	CreateWorkflow(ctx context.Context, wf *models.Workflow, tasks []models.Task) error

	// GetWorkflow retrieves a workflow by its ID along with all associated
	// tasks, ordered ascending by their Position field.
	GetWorkflow(ctx context.Context, id string) (*models.Workflow, []models.Task, error)

	// ListWorkflows returns workflows ordered by creation time (oldest first) with support for pagination (limit, offset).
	ListWorkflows(ctx context.Context, limit, offset int) ([]models.Workflow, error)

	// ListRecoverableWorkflows returns recoverable workflows (Pending or Running) ordered by ID ascending, starting after the given afterID, up to the given batch size.
	ListRecoverableWorkflows(ctx context.Context, afterID string, batchSize int) ([]models.Workflow, error)

	// UpdateTask writes the Status, Output, and Error fields of the given
	// task back to the database. All other fields are left untouched.
	UpdateTask(ctx context.Context, task *models.Task) error

	// UpdateWorkflowStatus sets a workflow's Status and refreshes its
	// UpdatedAt timestamp to the current UTC time.
	UpdateWorkflowStatus(ctx context.Context, workflowID string, status models.Status) error

	// Close releases the underlying connection pool.
	Close() error
}
