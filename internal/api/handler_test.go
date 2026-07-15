package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"workflow-engine/internal/api"
	"workflow-engine/internal/engine"
	"workflow-engine/internal/models"
	"workflow-engine/internal/storage"
)

func newTestServer(t *testing.T) *http.ServeMux {
	t.Helper()

	store, err := storage.NewSQLiteStorage(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	eng := engine.NewEngine(store, 1)

	mux := http.NewServeMux()
	api.NewAPIHandler(store, eng, 10, 0).RegisterRoutes(mux)

	return mux
}

func validPayload() []byte {
	return []byte(`{
		"input": {"a": 10, "b": 5},
		"tasks": [
			{"type": "Calculate", "config": {"a": "$.input.a", "b": "$.input.b", "op": "add"}},
			{"type": "Print",     "config": {"template": "Result: {{ $.steps.0 }}"}}
		]
	}`)
}

func doRequest(t *testing.T, mux *http.ServeMux, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, path, bytes.NewReader(body))
	require.NoError(t, err)

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	return w
}

func TestPostWorkflows_ValidPayload_Returns201(t *testing.T) {
	t.Parallel()
	mux := newTestServer(t)

	w := doRequest(t, mux, http.MethodPost, "/workflows", validPayload())

	require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())

	// Content-Type must be application/json.
	ct := w.Header().Get("Content-Type")
	assert.True(t, strings.HasPrefix(ct, "application/json"), "Content-Type: got %q, want application/json", ct)

	// Body must decode to a valid response with a non-empty ID and Pending status.
	var resp struct {
		ID     string        `json:"id"`
		Status models.Status `json:"status"`
	}
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	assert.NotEmpty(t, resp.ID, "response ID is empty")
	assert.Equal(t, models.StatusPending, resp.Status)
}

func TestPostWorkflows_EmptyTaskList_Returns400(t *testing.T) {
	t.Parallel()
	mux := newTestServer(t)

	body := []byte(`{"input": {}, "tasks": []}`)
	w := doRequest(t, mux, http.MethodPost, "/workflows", body)

	require.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
	assertErrorField(t, w.Body.Bytes())
}

func TestPostWorkflows_MalformedJSON_Returns400(t *testing.T) {
	t.Parallel()
	mux := newTestServer(t)

	w := doRequest(t, mux, http.MethodPost, "/workflows", []byte(`{bad json`))

	require.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
	assertErrorField(t, w.Body.Bytes())
}

func TestPostWorkflows_UnknownField_Returns400(t *testing.T) {
	t.Parallel()
	mux := newTestServer(t)

	// DisallowUnknownFields is set in decodeJSON - extra field must 400.
	body := []byte(`{"input":{}, "tasks":[{"type":"Print","config":{}}], "surpriseField": true}`)
	w := doRequest(t, mux, http.MethodPost, "/workflows", body)

	require.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
}

func TestPostWorkflows_UnknownTaskType_Returns400(t *testing.T) {
	t.Parallel()
	mux := newTestServer(t)

	body := []byte(`{"input": {}, "tasks": [{"type": "Unknown", "config": {}}]}`)
	w := doRequest(t, mux, http.MethodPost, "/workflows", body)

	require.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
	assertErrorField(t, w.Body.Bytes())
}

func TestPostWorkflows_WorkflowPersistedInDB(t *testing.T) {
	t.Parallel()

	store, err := storage.NewSQLiteStorage(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	eng := engine.NewEngine(store, 1)
	mux := http.NewServeMux()
	api.NewAPIHandler(store, eng, 10, 0).RegisterRoutes(mux)

	w := doRequest(t, mux, http.MethodPost, "/workflows", validPayload())
	require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())

	var resp struct {
		ID string `json:"id"`
	}
	err = json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)

	wf, tasks, err := store.GetWorkflow(context.Background(), resp.ID)
	require.NoError(t, err)
	assert.Equal(t, resp.ID, wf.ID)
	require.Len(t, tasks, 2)

	// Positions must be 0-based and ascending.
	for i, task := range tasks {
		assert.Equal(t, i, task.Position, "task[%d].Position = %d, want %d", i, task.Position, i)
	}
}

func TestGetWorkflowByID_ExistingID_Returns200(t *testing.T) {
	t.Parallel()

	store, err := storage.NewSQLiteStorage(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now().UTC()
	testID := "test-wf-id"
	seedWF := &models.Workflow{
		ID:        testID,
		Status:    models.StatusPending,
		Input:     json.RawMessage(`{"key":"val"}`),
		CreatedAt: now,
		UpdatedAt: now,
	}
	seedTasks := []models.Task{
		{
			ID:         "task-2",
			WorkflowID: testID,
			Type:       models.TaskTypePrint,
			Status:     models.StatusPending,
			Position:   2,
			Config:     json.RawMessage(`{"template":"hi 2"}`),
		},
		{
			ID:         "task-0",
			WorkflowID: testID,
			Type:       models.TaskTypePrint,
			Status:     models.StatusPending,
			Position:   0,
			Config:     json.RawMessage(`{"template":"hi 0"}`),
		},
		{
			ID:         "task-1",
			WorkflowID: testID,
			Type:       models.TaskTypePrint,
			Status:     models.StatusPending,
			Position:   1,
			Config:     json.RawMessage(`{"template":"hi 1"}`),
		},
	}
	err = store.CreateWorkflow(context.Background(), seedWF, seedTasks)
	require.NoError(t, err)

	eng := engine.NewEngine(store, 1)
	mux := http.NewServeMux()
	api.NewAPIHandler(store, eng, 10, 0).RegisterRoutes(mux)

	w := doRequest(t, mux, http.MethodGet, fmt.Sprintf("/workflows/%s", testID), nil)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	var resp struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Tasks  []struct {
			Position int    `json:"position"`
			Type     string `json:"type"`
		} `json:"tasks"`
	}
	err = json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Equal(t, testID, resp.ID)
	require.Len(t, resp.Tasks, 3)
	assert.Equal(t, 0, resp.Tasks[0].Position)
	assert.Equal(t, 1, resp.Tasks[1].Position)
	assert.Equal(t, 2, resp.Tasks[2].Position)
}

func TestGetWorkflowByID_NonExistentID_Returns404(t *testing.T) {
	t.Parallel()
	mux := newTestServer(t)

	w := doRequest(t, mux, http.MethodGet, "/workflows/does-not-exist", nil)

	require.Equal(t, http.StatusNotFound, w.Code, "body: %s", w.Body.String())
	assertErrorField(t, w.Body.Bytes())
}

func TestListWorkflows_Empty_ReturnsEmptyArray(t *testing.T) {
	t.Parallel()
	mux := newTestServer(t)

	w := doRequest(t, mux, http.MethodGet, "/workflows", nil)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	body := strings.TrimSpace(w.Body.String())
	assert.True(t, strings.HasPrefix(body, "["), "body is not a JSON array: %s", body)

	var list []json.RawMessage
	err := json.Unmarshal([]byte(body), &list)
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestListWorkflows_AfterCreate_ReturnsItems(t *testing.T) {
	t.Parallel()

	store, err := storage.NewSQLiteStorage(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	eng := engine.NewEngine(store, 1)
	mux := http.NewServeMux()
	api.NewAPIHandler(store, eng, 10, 0).RegisterRoutes(mux)

	for range 2 {
		w := doRequest(t, mux, http.MethodPost, "/workflows", validPayload())
		require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
	}

	w := doRequest(t, mux, http.MethodGet, "/workflows", nil)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	var list []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	err = json.NewDecoder(w.Body).Decode(&list)
	require.NoError(t, err)
	require.Len(t, list, 2)
	for i, item := range list {
		assert.NotEmpty(t, item.ID, "list[%d].id is empty", i)
		assert.Equal(t, string(models.StatusPending), item.Status, "list[%d].status = %q, want Pending", i, item.Status)
	}
}

func TestListWorkflows_Pagination_LimitAndOffset(t *testing.T) {
	t.Parallel()

	store, err := storage.NewSQLiteStorage(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	eng := engine.NewEngine(store, 1)
	mux := http.NewServeMux()
	api.NewAPIHandler(store, eng, 1, 0).RegisterRoutes(mux)

	var createdIDs []string
	for range 3 {
		w := doRequest(t, mux, http.MethodPost, "/workflows", validPayload())
		require.Equal(t, http.StatusCreated, w.Code)
		var resp struct{ ID string }
		err = json.NewDecoder(w.Body).Decode(&resp)
		require.NoError(t, err)
		createdIDs = append(createdIDs, resp.ID)
	}

	w := doRequest(t, mux, http.MethodGet, "/workflows", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var list []struct{ ID string }
	err = json.NewDecoder(w.Body).Decode(&list)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, createdIDs[0], list[0].ID)

	w = doRequest(t, mux, http.MethodGet, "/workflows?limit=2&offset=1", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var list2 []struct{ ID string }
	err = json.NewDecoder(w.Body).Decode(&list2)
	require.NoError(t, err)
	require.Len(t, list2, 2)
	assert.Equal(t, createdIDs[1], list2[0].ID)
	assert.Equal(t, createdIDs[2], list2[1].ID)
}

func TestListWorkflows_Pagination_InvalidParams(t *testing.T) {
	t.Parallel()
	mux := newTestServer(t)

	// Invalid limit
	w := doRequest(t, mux, http.MethodGet, "/workflows?limit=abc", nil)
	require.Equal(t, http.StatusBadRequest, w.Code)
	assertErrorField(t, w.Body.Bytes())

	// Negative limit
	w = doRequest(t, mux, http.MethodGet, "/workflows?limit=-5", nil)
	require.Equal(t, http.StatusBadRequest, w.Code)
	assertErrorField(t, w.Body.Bytes())

	// Invalid offset
	w = doRequest(t, mux, http.MethodGet, "/workflows?offset=xyz", nil)
	require.Equal(t, http.StatusBadRequest, w.Code)
	assertErrorField(t, w.Body.Bytes())

	// Negative offset
	w = doRequest(t, mux, http.MethodGet, "/workflows?offset=-1", nil)
	require.Equal(t, http.StatusBadRequest, w.Code)
	assertErrorField(t, w.Body.Bytes())
}

func assertErrorField(t *testing.T, body []byte) {
	t.Helper()
	var envelope struct {
		Error string `json:"error"`
	}
	err := json.Unmarshal(body, &envelope)
	require.NoError(t, err, "response is not valid JSON: %v\nbody: %s", err, body)
	assert.NotEmpty(t, envelope.Error, "expected non-empty 'error' field in response, body: %s", body)
}

func TestPostWorkflows_QueueFull_Returns503(t *testing.T) {
	t.Parallel()

	store, err := storage.NewSQLiteStorage(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	eng := engine.NewEngine(store, 1)
	mux := http.NewServeMux()
	api.NewAPIHandler(store, eng, 10, 0).RegisterRoutes(mux)

	// Fill the engine queue without starting it.
	// Since buffer size is 128, submitting 128 times directly to engine fills it.
	for i := 0; i < 128; i++ {
		err := eng.Submit(context.Background(), fmt.Sprintf("wf-%d", i))
		require.NoError(t, err)
	}

	// Now try to post a new workflow via API. It should fail to submit and return 503.
	w := doRequest(t, mux, http.MethodPost, "/workflows", validPayload())
	require.Equal(t, http.StatusServiceUnavailable, w.Code, "body: %s", w.Body.String())
	assertErrorField(t, w.Body.Bytes())
}
