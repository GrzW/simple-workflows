package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"workflow-engine/internal/models"
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
