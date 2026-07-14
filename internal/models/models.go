package models

import (
	"encoding/json"
	"time"
)

// Status represents the lifecycle state of a Workflow or Task.
type Status string

const (
	StatusPending   Status = "Pending"
	StatusRunning   Status = "Running"
	StatusCompleted Status = "Completed"
	StatusFailed    Status = "Failed"
)

// TaskType identifies the kind of operation a Task performs.
type TaskType string

const (
	TaskTypeCalculate TaskType = "Calculate"
	TaskTypePrint     TaskType = "Print"
)

// Workflow represents a single execution unit composed of ordered Tasks.
type Workflow struct {
	ID        string          `json:"id"`
	Status    Status          `json:"status"`
	Input     json.RawMessage `json:"input,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// Task is a single unit of work within a Workflow.
// Tasks are executed in ascending Position order.
type Task struct {
	ID         string          `json:"id"`
	WorkflowID string          `json:"workflow_id"`
	Type       TaskType        `json:"type"`
	Status     Status          `json:"status"`
	Position   int             `json:"position"`
	Config     json.RawMessage `json:"config,omitempty"`
	Output     string          `json:"output,omitempty"`
	Error      string          `json:"error,omitempty"`
}
