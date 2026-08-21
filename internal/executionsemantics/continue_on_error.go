package executionsemantics

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var bareMatrixBooleanExpression = regexp.MustCompile(`^matrix\.([A-Za-z_][A-Za-z0-9_-]*)$`)

// EvaluateContinueOnError evaluates the deterministic GitHub expression subset
// against one immutable matrix variant. Bare matrix values must be booleans;
// comparisons may use strings and numbers through the shared condition engine.
func EvaluateContinueOnError(expression string, matrix map[string]string) (bool, error) {
	normalized := normalizeCondition(expression)
	if normalized == "" {
		return false, fmt.Errorf("continue-on-error expression must not be empty")
	}
	if match := bareMatrixBooleanExpression.FindStringSubmatch(normalized); len(match) == 2 {
		raw, ok := matrix[match[1]]
		if !ok {
			return false, fmt.Errorf("continue-on-error: matrix.%s is not defined", match[1])
		}
		value, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return false, fmt.Errorf("continue-on-error: matrix.%s must be boolean", match[1])
		}
		return value, nil
	}

	values := make(map[string]interface{}, len(matrix))
	for key, raw := range matrix {
		values["matrix."+key] = continueOnErrorMatrixValue(raw)
	}
	value, err := EvaluateCondition(normalized, ConditionContext{
		Values:          values,
		Success:         true,
		CaseInsensitive: true,
	})
	if err != nil {
		return false, fmt.Errorf("continue-on-error: %w", err)
	}
	return value, nil
}

func continueOnErrorMatrixValue(raw string) interface{} {
	trimmed := strings.TrimSpace(raw)
	if value, err := strconv.ParseBool(trimmed); err == nil {
		return value
	}
	if value, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
		return value
	}
	if value, err := strconv.ParseFloat(trimmed, 64); err == nil {
		return value
	}
	return raw
}
