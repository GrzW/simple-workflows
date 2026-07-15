package engine

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

func newEC(input map[string]any, steps map[int]string) *ExecutionContext {
	if steps == nil {
		steps = make(map[int]string)
	}

	return &ExecutionContext{input: input, steps: steps}
}

func calcTask(config string) *models.Task {
	return &models.Task{
		ID:     "t1",
		Type:   models.TaskTypeCalculate,
		Config: json.RawMessage(config),
	}
}

func printTask(template string) *models.Task {
	raw, _ := json.Marshal(map[string]string{"template": template})
	return &models.Task{
		ID:     "t1",
		Type:   models.TaskTypePrint,
		Config: raw,
	}
}

func TestExecutionContext_Resolve(t *testing.T) {
	t.Parallel()

	ec := newEC(
		map[string]any{
			"name":   "Alice",
			"amount": float64(42),
			"ratio":  3.14,
			"active": true,
			"off":    false,
		},
		map[int]string{
			0: "30",
			2: "hello",
		},
	)

	tests := []struct {
		name string
		ref  string
		want string
	}{
		{
			name: "input string value",
			ref:  "$.input.name",
			want: "Alice",
		},
		{
			name: "input exact integer float",
			ref:  "$.input.amount",
			want: "42",
		},
		{
			name: "input fractional float",
			ref:  "$.input.ratio",
			want: "3.14",
		},
		{
			name: "input boolean true",
			ref:  "$.input.active",
			want: "true",
		},
		{
			name: "input boolean false",
			ref:  "$.input.off",
			want: "false",
		},
		{
			name: "missing input key returns empty string",
			ref:  "$.input.missing",
			want: "",
		},
		{
			name: "step position 0",
			ref:  "$.steps.0",
			want: "30",
		},
		{
			name: "step position 2",
			ref:  "$.steps.2",
			want: "hello",
		},
		{
			name: "missing step position returns empty string",
			ref:  "$.steps.99",
			want: "",
		},
		{
			name: "plain string literal",
			ref:  "hello world",
			want: "hello world",
		},
		{
			name: "numeric string literal",
			ref:  "123",
			want: "123",
		},
		{
			name: "empty string literal",
			ref:  "",
			want: "",
		},
		{
			name: "dollar without dot prefix is literal",
			ref:  "$input.key",
			want: "$input.key",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ec.Resolve(tc.ref)
			assert.Equal(t, tc.want, got, "Resolve(%q) mismatch", tc.ref)
		})
	}
}

func TestCalculateHandler(t *testing.T) {
	t.Parallel()

	ec := newEC(
		map[string]any{
			"x": float64(10),
			"y": float64(5),
		},
		map[int]string{
			0: "100",
		},
	)

	tests := []struct {
		name      string
		config    string
		wantOut   string
		wantError bool // true when an error is expected
	}{
		{name: "add word operator", config: `{"a":3,"b":7,"op":"add"}`, wantOut: "10"},
		{name: "add symbol operator", config: `{"a":3,"b":7,"op":"+"}`, wantOut: "10"},
		{name: "subtract", config: `{"a":10,"b":4,"op":"subtract"}`, wantOut: "6"},
		{name: "subtract symbol", config: `{"a":10,"b":4,"op":"-"}`, wantOut: "6"},
		{name: "multiply", config: `{"a":3,"b":4,"op":"multiply"}`, wantOut: "12"},
		{name: "multiply symbol", config: `{"a":3,"b":4,"op":"*"}`, wantOut: "12"},
		{name: "divide", config: `{"a":10,"b":4,"op":"divide"}`, wantOut: "2.5"},
		{name: "divide symbol", config: `{"a":10,"b":4,"op":"/"}`, wantOut: "2.5"},
		{
			name:    "integer result formats without decimal point",
			config:  `{"a":100,"b":50,"op":"add"}`,
			wantOut: "150",
		},
		{
			name:    "floating point result uses shortest representation",
			config:  `{"a":1,"b":3,"op":"divide"}`,
			wantOut: "0.3333333333333333",
		},
		{
			name:    "operands from input references",
			config:  `{"a":"$.input.x","b":"$.input.y","op":"add"}`,
			wantOut: "15",
		},
		{
			name:    "operand b from step reference",
			config:  `{"a":200,"b":"$.steps.0","op":"subtract"}`,
			wantOut: "100",
		},
		{
			name:      "division by zero returns error",
			config:    `{"a":10,"b":0,"op":"divide"}`,
			wantError: true,
		},
		{
			name:      "unsupported operator returns error",
			config:    `{"a":10,"b":2,"op":"power"}`,
			wantError: true,
		},
		{
			name:      "reference resolves to empty string returns error",
			config:    `{"a":"$.input.missing","b":1,"op":"add"}`,
			wantError: true,
		},
		{
			name:      "reference resolves to non-numeric string returns error",
			config:    `{"a":"$.steps.99","b":1,"op":"add"}`,
			wantError: true,
		},
		{
			name:      "malformed config JSON returns error",
			config:    `{"a":10,"b":`,
			wantError: true,
		},
		{name: "modulo word operator", config: `{"a":10,"b":3,"op":"mod"}`, wantOut: "1"},
		{name: "modulo symbol operator", config: `{"a":10,"b":3,"op":"%"}`, wantOut: "1"},
		{name: "modulo by zero returns error", config: `{"a":10,"b":0,"op":"mod"}`, wantError: true},
		{
			name:      "non-finite result returns error",
			config:    `{"a":1e308,"b":1e308,"op":"multiply"}`,
			wantError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			task := calcTask(tc.config)
			out, err := calculateHandler(task, ec)

			if tc.wantError {
				assert.Error(t, err, "calculateHandler(%s) expected error, got output=%q", tc.config, out)
				return
			}

			require.NoError(t, err, "calculateHandler(%s) unexpected error", tc.config)
			assert.Equal(t, tc.wantOut, out, "calculateHandler(%s) output mismatch", tc.config)
		})
	}
}

func TestPrintHandler(t *testing.T) {
	t.Parallel()

	ec := newEC(
		map[string]any{
			"name":  "Alice",
			"score": float64(99),
		},
		map[int]string{
			0: "42",
			1: "done",
		},
	)

	tests := []struct {
		name      string
		template  string
		wantOut   string
		wantError bool
	}{
		{
			name:     "single input reference",
			template: "Hello, {{$.input.name}}!",
			wantOut:  "Hello, Alice!",
		},
		{
			name:     "single step reference",
			template: "Result is {{$.steps.0}}",
			wantOut:  "Result is 42",
		},
		{
			name:     "multiple placeholders",
			template: "{{$.input.name}} scored {{$.input.score}} and step0=={{$.steps.0}}",
			wantOut:  "Alice scored 99 and step0==42",
		},
		{
			name:     "adjacent placeholders no separator",
			template: "{{$.steps.0}}{{$.steps.1}}",
			wantOut:  "42done",
		},
		{
			name:     "extra spaces inside braces",
			template: "{{  $.input.name  }}",
			wantOut:  "Alice",
		},
		{
			name:     "tab inside braces",
			template: "{{\t$.steps.0\t}}",
			wantOut:  "42",
		},
		{
			name:     "missing input key becomes empty string",
			template: "Value: {{$.input.missing}}",
			wantOut:  "Value: ",
		},
		{
			name:     "missing step position becomes empty string",
			template: "X={{$.steps.99}}",
			wantOut:  "X=",
		},
		{
			name:     "template with no placeholders returned verbatim",
			template: "no references here",
			wantOut:  "no references here",
		},
		{
			name:     "empty template",
			template: "",
			wantOut:  "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			task := printTask(tc.template)
			out, err := printHandler(task, ec)

			if tc.wantError {
				assert.Error(t, err, "printHandler(%q) expected error, got %q", tc.template, out)
				return
			}

			require.NoError(t, err, "printHandler(%q) unexpected error", tc.template)
			assert.Equal(t, tc.wantOut, out, "printHandler(%q) output mismatch", tc.template)
		})
	}
}

func TestAnyToString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input any
		want  string
	}{
		{"integer float", float64(10), "10"},
		{"negative integer float", float64(-5), "-5"},
		{"large integer float", float64(1000000), "1000000"},
		{"fractional float", 3.14, "3.14"},
		{"small fractional", 0.001, "0.001"},
		{"string passthrough", "hello", "hello"},
		{"empty string", "", ""},
		{"bool true", true, "true"},
		{"bool false", false, "false"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := anyToString(tc.input)
			assert.Equal(t, tc.want, got, "anyToString(%v) mismatch", tc.input)
		})
	}
}

func TestEngine_Submit_QueueFull(t *testing.T) {
	t.Parallel()
	eng := NewEngine(nil, 1)

	// Fill the queue
	for i := 0; i < defaultJobsBufferSize; i++ {
		err := eng.Submit(context.Background(), "test-job")
		assert.NoError(t, err)
	}

	// 129th submit should return ErrQueueFull
	err := eng.Submit(context.Background(), "overflow-job")
	assert.ErrorIs(t, err, ErrQueueFull)
}

func TestEngine_Execution_Success(t *testing.T) {
	t.Parallel()

	// 1. Setup in-memory store
	store, err := storage.NewSQLiteStorage(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	// 2. Setup engine with the store and concurrency 1
	eng := NewEngine(store, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	eng.Start(ctx)
	t.Cleanup(func() { eng.Stop() })

	// 3. Seed a workflow
	wfID := "wf-success-test"
	wf := &models.Workflow{
		ID:        wfID,
		Status:    models.StatusPending,
		Input:     json.RawMessage(`{"a":10,"b":5}`),
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	tasks := []models.Task{
		{
			ID:         "task-1",
			WorkflowID: wfID,
			Type:       models.TaskTypeCalculate,
			Status:     models.StatusPending,
			Position:   0,
			Config:     json.RawMessage(`{"a":"$.input.a","b":"$.input.b","op":"add"}`),
		},
		{
			ID:         "task-2",
			WorkflowID: wfID,
			Type:       models.TaskTypePrint,
			Status:     models.StatusPending,
			Position:   1,
			Config:     json.RawMessage(`{"template":"Result is {{$.steps.0}}"}`),
		},
	}
	err = store.CreateWorkflow(ctx, wf, tasks)
	require.NoError(t, err)

	// 4. Submit workflow
	err = eng.Submit(ctx, wfID)
	require.NoError(t, err)

	// 5. Poll database until workflow is completed or timeout
	var finalWF *models.Workflow
	var finalTasks []models.Task
	require.Eventually(t, func() bool {
		var getErr error
		finalWF, finalTasks, getErr = store.GetWorkflow(context.Background(), wfID)
		if getErr != nil {
			return false
		}
		return finalWF.Status == models.StatusCompleted || finalWF.Status == models.StatusFailed
	}, 5*time.Second, 50*time.Millisecond)

	// 6. Verify assertions
	assert.Equal(t, models.StatusCompleted, finalWF.Status)
	require.Len(t, finalTasks, 2)
	assert.Equal(t, models.StatusCompleted, finalTasks[0].Status)
	assert.Equal(t, "15", finalTasks[0].Output)
	assert.Equal(t, models.StatusCompleted, finalTasks[1].Status)
	assert.Equal(t, "Result is 15", finalTasks[1].Output)
}

func TestEngine_Execution_Failure(t *testing.T) {
	t.Parallel()

	// 1. Setup in-memory store
	store, err := storage.NewSQLiteStorage(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	// 2. Setup engine
	eng := NewEngine(store, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	eng.Start(ctx)
	t.Cleanup(func() { eng.Stop() })

	// 3. Seed workflow with a division by zero error
	wfID := "wf-failure-test"
	wf := &models.Workflow{
		ID:        wfID,
		Status:    models.StatusPending,
		Input:     json.RawMessage(`{"x":10}`),
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	tasks := []models.Task{
		{
			ID:         "task-1",
			WorkflowID: wfID,
			Type:       models.TaskTypeCalculate,
			Status:     models.StatusPending,
			Position:   0,
			Config:     json.RawMessage(`{"a":"$.input.x","b":0,"op":"/"}`),
		},
	}
	err = store.CreateWorkflow(ctx, wf, tasks)
	require.NoError(t, err)

	// 4. Submit
	err = eng.Submit(ctx, wfID)
	require.NoError(t, err)

	// 5. Poll
	var finalWF *models.Workflow
	var finalTasks []models.Task
	require.Eventually(t, func() bool {
		var getErr error
		finalWF, finalTasks, getErr = store.GetWorkflow(context.Background(), wfID)
		if getErr != nil {
			return false
		}
		return finalWF.Status == models.StatusFailed
	}, 5*time.Second, 50*time.Millisecond)

	// 6. Verify assertions
	assert.Equal(t, models.StatusFailed, finalWF.Status)
	require.Len(t, finalTasks, 1)
	assert.Equal(t, models.StatusFailed, finalTasks[0].Status)
	assert.Contains(t, finalTasks[0].Error, "division by zero")
}

func TestEngine_Recovery(t *testing.T) {
	t.Parallel()

	// 1. Setup store
	store, err := storage.NewSQLiteStorage(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	// 2. Seed a Pending workflow and a Running workflow
	now := time.Now().UTC()
	wf1ID := "wf-pending-recovery"
	wf1 := &models.Workflow{
		ID:        wf1ID,
		Status:    models.StatusPending,
		CreatedAt: now.Add(-10 * time.Second),
		UpdatedAt: now,
	}
	tasks1 := []models.Task{
		{
			ID:         "task-w1-1",
			WorkflowID: wf1ID,
			Type:       models.TaskTypePrint,
			Status:     models.StatusPending,
			Position:   0,
			Config:     json.RawMessage(`{"template":"wf1 task"}`),
		},
	}
	err = store.CreateWorkflow(context.Background(), wf1, tasks1)
	require.NoError(t, err)

	wf2ID := "wf-running-recovery"
	wf2 := &models.Workflow{
		ID:        wf2ID,
		Status:    models.StatusRunning,
		CreatedAt: now.Add(-5 * time.Second),
		UpdatedAt: now,
	}
	tasks2 := []models.Task{
		{
			ID:         "task-w2-1",
			WorkflowID: wf2ID,
			Type:       models.TaskTypePrint,
			Status:     models.StatusCompleted,
			Position:   0,
			Config:     json.RawMessage(`{"template":"skipped task"}`),
			Output:     "already-done",
		},
		{
			ID:         "task-w2-2",
			WorkflowID: wf2ID,
			Type:       models.TaskTypePrint,
			Status:     models.StatusPending,
			Position:   1,
			Config:     json.RawMessage(`{"template":"run me"}`),
		},
	}
	err = store.CreateWorkflow(context.Background(), wf2, tasks2)
	require.NoError(t, err)

	// 3. Setup engine
	eng := NewEngine(store, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	eng.Start(ctx)
	t.Cleanup(func() { eng.Stop() })

	// 4. Run recovery
	err = eng.RecoverOutstandingWork(ctx)
	require.NoError(t, err)

	// 5. Poll database for completion of both workflows
	require.Eventually(t, func() bool {
		w1, _, err1 := store.GetWorkflow(context.Background(), wf1ID)
		w2, _, err2 := store.GetWorkflow(context.Background(), wf2ID)
		if err1 != nil || err2 != nil {
			return false
		}
		return w1.Status == models.StatusCompleted && w2.Status == models.StatusCompleted
	}, 5*time.Second, 50*time.Millisecond)

	// 6. Verify that tasks were handled correctly
	_, tasks1Final, err := store.GetWorkflow(context.Background(), wf1ID)
	require.NoError(t, err)
	assert.Equal(t, models.StatusCompleted, tasks1Final[0].Status)
	assert.Equal(t, "wf1 task", tasks1Final[0].Output)

	_, tasks2Final, err := store.GetWorkflow(context.Background(), wf2ID)
	require.NoError(t, err)
	assert.Equal(t, models.StatusCompleted, tasks2Final[0].Status)
	assert.Equal(t, "already-done", tasks2Final[0].Output) // skipped
	assert.Equal(t, models.StatusCompleted, tasks2Final[1].Status)
	assert.Equal(t, "run me", tasks2Final[1].Output) // executed
}

func TestEngine_GracefulShutdown_CancelDelay(t *testing.T) {
	t.Parallel()

	// 1. Setup store
	store, err := storage.NewSQLiteStorage(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	// 2. Setup engine with 1 worker and set a demo delay directly (parallel-safe)
	eng := NewEngine(store, 1)
	const delay = 500 * time.Millisecond
	eng.demoDelay = delay

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	eng.Start(ctx)

	// 3. Seed workflow
	wfID := "wf-shutdown-test"
	wf := &models.Workflow{
		ID:        wfID,
		Status:    models.StatusPending,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	tasks := []models.Task{
		{
			ID:         "task-1",
			WorkflowID: wfID,
			Type:       models.TaskTypePrint,
			Status:     models.StatusPending,
			Position:   0,
			Config:     json.RawMessage(`{"template":"will be interrupted"}`),
		},
	}
	err = store.CreateWorkflow(context.Background(), wf, tasks)
	require.NoError(t, err)

	// 4. Submit workflow
	err = eng.Submit(context.Background(), wfID)
	require.NoError(t, err)

	// 5. Wait a tiny bit to make sure worker picked it up and is in time.After
	time.Sleep(50 * time.Millisecond)

	// 6. Stop the engine (which triggers graceful stop logic and cancels the worker context)
	// Measure the duration to prove we didn't block for the full 500ms delay.
	start := time.Now()
	cancel()   // Cancel the signal context passed to Start
	eng.Stop() // Drain workers
	duration := time.Since(start)

	// Shutdown should interrupt early and take much less time than the 500ms delay.
	assert.Less(t, duration, delay)

	// The workflow execution should have aborted and not updated to Completed
	finalWF, finalTasks, err := store.GetWorkflow(context.Background(), wfID)
	require.NoError(t, err)
	assert.NotEqual(t, models.StatusCompleted, finalWF.Status)
	assert.NotEqual(t, models.StatusCompleted, finalTasks[0].Status)
}

func TestEngine_Recovery_BeforeTimeGuard(t *testing.T) {
	t.Parallel()

	// 1. Setup store
	store, err := storage.NewSQLiteStorage(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	// 2. Setup engine
	eng := NewEngine(store, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	eng.Start(ctx)
	t.Cleanup(func() { eng.Stop() })

	// 3. Seed workflow BEFORE engine start time
	wfBeforeID := "wf-before-recovery"
	wfBefore := &models.Workflow{
		ID:        wfBeforeID,
		Status:    models.StatusPending,
		CreatedAt: eng.startTime.Add(-5 * time.Second),
		UpdatedAt: eng.startTime.Add(-5 * time.Second),
	}
	tasksBefore := []models.Task{
		{
			ID:         "task-before-1",
			WorkflowID: wfBeforeID,
			Type:       models.TaskTypePrint,
			Status:     models.StatusPending,
			Position:   0,
			Config:     json.RawMessage(`{"template":"run me before"}`),
		},
	}
	err = store.CreateWorkflow(context.Background(), wfBefore, tasksBefore)
	require.NoError(t, err)

	// 4. Seed workflow AFTER engine start time
	wfAfterID := "wf-after-recovery"
	wfAfter := &models.Workflow{
		ID:        wfAfterID,
		Status:    models.StatusPending,
		CreatedAt: eng.startTime.Add(5 * time.Second),
		UpdatedAt: eng.startTime.Add(5 * time.Second),
	}
	tasksAfter := []models.Task{
		{
			ID:         "task-after-1",
			WorkflowID: wfAfterID,
			Type:       models.TaskTypePrint,
			Status:     models.StatusPending,
			Position:   0,
			Config:     json.RawMessage(`{"template":"do not run me"}`),
		},
	}
	err = store.CreateWorkflow(context.Background(), wfAfter, tasksAfter)
	require.NoError(t, err)

	// 5. Run recovery
	err = eng.RecoverOutstandingWork(ctx)
	require.NoError(t, err)

	// 6. Wait for wfBefore to complete
	require.Eventually(t, func() bool {
		w, _, err := store.GetWorkflow(context.Background(), wfBeforeID)
		if err != nil {
			return false
		}
		return w.Status == models.StatusCompleted
	}, 2*time.Second, 50*time.Millisecond)

	// 7. Verify wfAfter remains Pending
	time.Sleep(100 * time.Millisecond)
	wAfter, _, err := store.GetWorkflow(context.Background(), wfAfterID)
	require.NoError(t, err)
	assert.Equal(t, models.StatusPending, wAfter.Status)
}
