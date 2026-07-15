package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"workflow-engine/internal/engine"
	"workflow-engine/internal/models"
	"workflow-engine/internal/storage"
)

// APIHandler wires the HTTP layer to the storage and execution engine.
// It must be constructed via NewAPIHandler; do not use the zero value.
type APIHandler struct {
	store         storage.Store
	eng           *engine.Engine
	defaultLimit  int
	defaultOffset int
	logger        *slog.Logger
}

// NewAPIHandler constructs an APIHandler with the provided dependencies.
func NewAPIHandler(store storage.Store, eng *engine.Engine, defaultLimit, defaultOffset int) *APIHandler {
	return &APIHandler{
		store:         store,
		eng:           eng,
		defaultLimit:  defaultLimit,
		defaultOffset: defaultOffset,
		logger:        slog.Default().With("component", "api"),
	}
}

// RegisterRoutes registers all API endpoints on mux.
func (h *APIHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /workflows", h.createWorkflow)
	mux.HandleFunc("GET /workflows", h.listWorkflows)
	mux.HandleFunc("GET /workflows/{id}", h.getWorkflow)
}

type createWorkflowRequest struct {
	Input json.RawMessage  `json:"input"`
	Tasks []taskDefinition `json:"tasks"`
}

type taskDefinition struct {
	Type   models.TaskType `json:"type"`
	Config json.RawMessage `json:"config"`
}

type createWorkflowResponse struct {
	ID     string        `json:"id"`
	Status models.Status `json:"status"`
}

type workflowResponse struct {
	ID        string          `json:"id"`
	Status    models.Status   `json:"status"`
	Input     json.RawMessage `json:"input,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	Tasks     []taskResponse  `json:"tasks"`
}

type taskResponse struct {
	ID       string          `json:"id"`
	Type     models.TaskType `json:"type"`
	Status   models.Status   `json:"status"`
	Position int             `json:"position"`
	Config   json.RawMessage `json:"config,omitempty"`
	Output   string          `json:"output,omitempty"`
	Error    string          `json:"error,omitempty"`
}

type workflowSummary struct {
	ID        string        `json:"id"`
	Status    models.Status `json:"status"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func (h *APIHandler) createWorkflow(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB request body limit

	var req createWorkflowRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())

		return
	}

	if len(req.Tasks) == 0 {
		writeError(w, http.StatusBadRequest, "tasks must not be empty")

		return
	}

	for _, def := range req.Tasks {
		switch def.Type {
		case models.TaskTypeCalculate, models.TaskTypePrint:
		default:
			writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown task type %q", def.Type))

			return
		}
	}

	now := time.Now().UTC()
	wf := &models.Workflow{
		ID:        uuid.NewString(),
		Status:    models.StatusPending,
		Input:     req.Input,
		CreatedAt: now,
		UpdatedAt: now,
	}

	tasks := make([]models.Task, len(req.Tasks))
	for i, def := range req.Tasks {
		tasks[i] = models.Task{
			ID:         uuid.NewString(),
			WorkflowID: wf.ID,
			Type:       def.Type,
			Status:     models.StatusPending,
			Position:   i,
			Config:     def.Config,
		}
	}

	if err := h.store.CreateWorkflow(r.Context(), wf, tasks); err != nil {
		h.logger.ErrorContext(r.Context(), "createWorkflow: storage error", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to persist workflow")

		return
	}

	if err := h.eng.Submit(r.Context(), wf.ID); err != nil {
		h.logger.ErrorContext(r.Context(), "createWorkflow: engine submit failed", "error", err)
		// Fail the workflow in storage so it is not left in a ghost Pending state.
		if updateErr := h.store.UpdateWorkflowStatus(r.Context(), wf.ID, models.StatusFailed); updateErr != nil {
			h.logger.ErrorContext(r.Context(), "createWorkflow: failed to update status to Failed in storage", "error", updateErr)
		}
		if errors.Is(err, engine.ErrQueueFull) {
			writeError(w, http.StatusServiceUnavailable, "engine job queue is full")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to submit workflow")
		}
		return
	}

	h.logger.InfoContext(r.Context(), "created workflow", "workflow_id", wf.ID, "tasks_count", len(tasks))

	writeJSON(w, http.StatusCreated, createWorkflowResponse{
		ID:     wf.ID,
		Status: wf.Status,
	})
}

func (h *APIHandler) getWorkflow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	wf, tasks, err := h.store.GetWorkflow(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "workflow not found")

			return
		}
		h.logger.ErrorContext(r.Context(), "getWorkflow: storage error", "workflow_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to fetch workflow")

		return
	}

	resp := workflowResponse{
		ID:        wf.ID,
		Status:    wf.Status,
		Input:     wf.Input,
		CreatedAt: wf.CreatedAt,
		UpdatedAt: wf.UpdatedAt,
		Tasks:     make([]taskResponse, len(tasks)),
	}

	for i, t := range tasks {
		resp.Tasks[i] = taskResponse{
			ID:       t.ID,
			Type:     t.Type,
			Status:   t.Status,
			Position: t.Position,
			Config:   t.Config,
			Output:   t.Output,
			Error:    t.Error,
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *APIHandler) listWorkflows(w http.ResponseWriter, r *http.Request) {
	limitVal := r.URL.Query().Get("limit")
	offsetVal := r.URL.Query().Get("offset")

	limit := h.defaultLimit
	if limitVal != "" {
		parsedLimit, err := strconv.Atoi(limitVal)
		if err != nil || parsedLimit < 0 {
			writeError(w, http.StatusBadRequest, "invalid limit query parameter")

			return
		}
		limit = parsedLimit
	}

	offset := h.defaultOffset
	if offsetVal != "" {
		parsedOffset, err := strconv.Atoi(offsetVal)
		if err != nil || parsedOffset < 0 {
			writeError(w, http.StatusBadRequest, "invalid offset query parameter")

			return
		}
		offset = parsedOffset
	}

	workflows, err := h.store.ListWorkflows(r.Context(), limit, offset)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "listWorkflows: storage error", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list workflows")

		return
	}

	summaries := make([]workflowSummary, len(workflows))
	for i, wf := range workflows {
		summaries[i] = workflowSummary{
			ID:        wf.ID,
			Status:    wf.Status,
			CreatedAt: wf.CreatedAt,
			UpdatedAt: wf.UpdatedAt,
		}
	}

	writeJSON(w, http.StatusOK, summaries)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	buf := &bytes.Buffer{}
	if err := json.NewEncoder(buf).Encode(v); err != nil {
		slog.Error("writeJSON: encode error", "error", err, "component", "api")
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}

func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		var syntaxErr *json.SyntaxError
		var unmarshalErr *json.UnmarshalTypeError

		switch {
		case errors.As(err, &syntaxErr):
			return fmt.Errorf("malformed JSON: syntax error near offset %d", syntaxErr.Offset)
		case errors.As(err, &unmarshalErr):
			return fmt.Errorf("invalid value for field %q", unmarshalErr.Field)
		default:
			return err
		}
	}

	return nil
}
