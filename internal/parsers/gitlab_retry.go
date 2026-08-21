package parsers

import "github.com/sanix-darker/git-ci/pkg/types"

func parseGitLabRetryPolicy(raw interface{}) *types.RetryPolicy {
	if raw == nil {
		return nil
	}
	policy := &types.RetryPolicy{}
	switch value := raw.(type) {
	case int:
		policy.MaxAttempts = boundedGitLabRetries(value)
	case int64:
		policy.MaxAttempts = boundedGitLabRetries(int(value))
	case map[string]interface{}:
		if max, ok := retryInteger(value["max"]); ok {
			policy.MaxAttempts = boundedGitLabRetries(max)
		}
		policy.When = retryStrings(value["when"])
		policy.ExitCodes = retryIntegers(value["exit_codes"])
	case map[interface{}]interface{}:
		normalized := make(map[string]interface{}, len(value))
		for key, item := range value {
			if name, ok := key.(string); ok {
				normalized[name] = item
			}
		}
		return parseGitLabRetryPolicy(normalized)
	default:
		return nil
	}
	return policy
}

func boundedGitLabRetries(value int) int {
	if value < 0 {
		return 0
	}
	if value > 2 {
		return 2
	}
	return value
}

func retryStrings(raw interface{}) []string {
	switch value := raw.(type) {
	case string:
		if value != "" {
			return []string{value}
		}
	case []string:
		return append([]string(nil), value...)
	case []interface{}:
		items := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok && text != "" {
				items = append(items, text)
			}
		}
		return items
	}
	return nil
}

func retryIntegers(raw interface{}) []int {
	if item, ok := retryInteger(raw); ok {
		return []int{item}
	}
	items := []int{}
	switch value := raw.(type) {
	case []int:
		items = append(items, value...)
	case []interface{}:
		for _, rawItem := range value {
			if item, ok := retryInteger(rawItem); ok {
				items = append(items, item)
			}
		}
	}
	return items
}

func retryInteger(raw interface{}) (int, bool) {
	switch value := raw.(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case uint64:
		return int(value), true
	case float64:
		if value == float64(int(value)) {
			return int(value), true
		}
	}
	return 0, false
}
