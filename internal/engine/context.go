package engine

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
)

type ExecutionContext struct {
	input map[string]any
	steps map[int]string
}

var inputRe = regexp.MustCompile(`^\$\.input\.([a-zA-Z0-9_]+)$`)
var stepRe = regexp.MustCompile(`^\$\.steps\.([0-9]+)$`)

// Resolve evaluates a variable reference pattern. If unmatched, it is returned as a literal.
// Missing keys or steps fall back to empty string to allow graceful degradation in Print templates.
func (ec *ExecutionContext) Resolve(ref string) string {
	if m := inputRe.FindStringSubmatch(ref); len(m) == 2 {
		key := m[1]
		if val, ok := ec.input[key]; ok {
			return anyToString(val)
		}

		return ""
	}

	if m := stepRe.FindStringSubmatch(ref); len(m) == 2 {
		if pos, err := strconv.Atoi(m[1]); err == nil {
			return ec.steps[pos]
		}

		return ""
	}

	return ref
}

func anyToString(v any) string {
	switch t := v.(type) {
	case float64:
		// strip decimal points for exact integers within int64 bounds
		if t == math.Trunc(t) && !math.IsInf(t, 0) && t >= -(1<<53) && t <= (1<<53) {
			return strconv.FormatInt(int64(t), 10)
		}

		return strconv.FormatFloat(t, 'g', -1, 64)
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	default:
		return fmt.Sprint(v)
	}
}
