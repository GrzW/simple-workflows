package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"workflow-engine/internal/models"
)

type CalculateConfig struct {
	A  json.RawMessage `json:"a"`
	B  json.RawMessage `json:"b"`
	Op json.RawMessage `json:"op"`
}

type PrintConfig struct {
	Template string `json:"template"`
}

var placeholderRe = regexp.MustCompile(`\{\{\s*(.*?)\s*\}\}`)

func resolveOperand(raw json.RawMessage, ec *ExecutionContext, name string) (float64, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return 0, fmt.Errorf("operand %q: empty input", name)
	}

	if bytes.Equal(trimmed, []byte("null")) {
		return 0, fmt.Errorf("operand %q: value cannot be null", name)
	}

	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return 0, fmt.Errorf("operand %q: cannot unmarshal as string: %w", name, err)
		}

		if s == "null" {
			return 0, fmt.Errorf("operand %q: string value \"null\" is not a valid number", name)
		}

		if strings.HasPrefix(s, "$.") {
			s = ec.Resolve(s)
		}

		if s == "" {
			return 0, fmt.Errorf("operand %q: reference resolved to an empty string", name)
		}

		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, fmt.Errorf("operand %q: %q cannot be parsed as a number: %w", name, s, err)
		}

		return f, nil
	}

	// Otherwise, treat it directly as a numeric value
	var f float64
	if err := json.Unmarshal(trimmed, &f); err != nil {
		return 0, fmt.Errorf("operand %q: cannot unmarshal as float64: %w", name, err)
	}

	return f, nil
}

func resolveOperator(raw json.RawMessage, ec *ExecutionContext) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("operator: cannot unmarshal as string: %w", err)
	}

	if strings.HasPrefix(s, "$.") {
		s = ec.Resolve(s)
	}

	return s, nil
}

func calculateHandler(task *models.Task, ec *ExecutionContext) (string, error) {
	var cfg CalculateConfig
	if err := json.Unmarshal(task.Config, &cfg); err != nil {
		return "", fmt.Errorf("calculate: parse config: %w", err)
	}

	a, err := resolveOperand(cfg.A, ec, "a")
	if err != nil {
		return "", fmt.Errorf("calculate: %w", err)
	}

	b, err := resolveOperand(cfg.B, ec, "b")
	if err != nil {
		return "", fmt.Errorf("calculate: %w", err)
	}

	op, err := resolveOperator(cfg.Op, ec)
	if err != nil {
		return "", fmt.Errorf("calculate: %w", err)
	}

	var result float64
	switch op {
	case "add", "plus", "+":
		result = a + b
	case "subtract", "minus", "-":
		result = a - b
	case "multiply", "times", "*":
		result = a * b
	case "divide", "/", ":":
		if b == 0 {
			return "", fmt.Errorf("calculate: division by zero")
		}
		result = a / b
	case "mod", "modulo", "%":
		if b == 0 {
			return "", fmt.Errorf("calculate: modulo by zero")
		}
		result = math.Mod(a, b)
	default:
		return "", fmt.Errorf("calculate: unsupported operator %q", op)
	}

	if math.IsNaN(result) || math.IsInf(result, 0) {
		return "", fmt.Errorf("calculate: result is not a finite number")
	}

	return strconv.FormatFloat(result, 'g', -1, 64), nil
}

func printHandler(task *models.Task, ec *ExecutionContext) (string, error) {
	var cfg PrintConfig
	if err := json.Unmarshal(task.Config, &cfg); err != nil {
		return "", fmt.Errorf("print: parse config: %w", err)
	}

	result := placeholderRe.ReplaceAllStringFunc(cfg.Template, func(match string) string {
		// match is guaranteed to start with "{{" and end with "}}" due to regexp boundaries.
		ref := match[2 : len(match)-2]
		ref = strings.TrimSpace(ref)

		return ec.Resolve(ref)
	})

	return result, nil
}
