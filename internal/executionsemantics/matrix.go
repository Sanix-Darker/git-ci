package executionsemantics

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/sanix-darker/git-ci/pkg/types"
)

const (
	MaxMatrixDimensions = 8
	MaxMatrixVariants   = 64
)

type MatrixVariant struct {
	Index  int               `json:"index"`
	Total  int               `json:"total"`
	Values map[string]string `json:"values"`
	Label  string            `json:"label"`
}

func ExpandMatrix(job *types.Job) ([]MatrixVariant, error) {
	if job == nil {
		return nil, fmt.Errorf("matrix: job is nil")
	}
	var combinations []map[string]string
	var err error
	switch {
	case job.Strategy != nil && (len(job.Strategy.Matrix) > 0 || len(job.Strategy.Include) > 0):
		combinations, err = expandGitHubMatrix(job.Strategy)
	case job.Parallel != nil && len(job.Parallel.Matrix) > 0:
		combinations, err = expandGitLabMatrix(job.Parallel.Matrix)
	case job.Parallel != nil && job.Parallel.Total > 0:
		if job.Parallel.Total > MaxMatrixVariants {
			return nil, fmt.Errorf("matrix: parallel total %d exceeds limit %d", job.Parallel.Total, MaxMatrixVariants)
		}
		combinations = make([]map[string]string, job.Parallel.Total)
		for index := range combinations {
			combinations[index] = map[string]string{
				"CI_NODE_INDEX": strconv.Itoa(index + 1),
				"CI_NODE_TOTAL": strconv.Itoa(job.Parallel.Total),
			}
		}
	case len(job.Matrix) > 0:
		combinations, err = cartesian(job.Matrix)
	default:
		combinations = []map[string]string{{}}
	}
	if err != nil {
		return nil, err
	}
	if len(combinations) == 0 {
		return nil, fmt.Errorf("matrix: expansion produced no variants")
	}
	if len(combinations) > MaxMatrixVariants {
		return nil, fmt.Errorf("matrix: expansion produced %d variants; limit is %d", len(combinations), MaxMatrixVariants)
	}
	seen := make(map[string]struct{}, len(combinations))
	variants := make([]MatrixVariant, 0, len(combinations))
	for index, values := range combinations {
		canonical := canonicalCoordinates(values)
		if _, exists := seen[canonical]; exists {
			return nil, fmt.Errorf("matrix: duplicate variant %s", coordinateLabel(values))
		}
		seen[canonical] = struct{}{}
		variants = append(variants, MatrixVariant{
			Index: index + 1, Total: len(combinations), Values: copyCoordinates(values), Label: coordinateLabel(values),
		})
	}
	return variants, nil
}

func MatrixEnvironment(variant MatrixVariant, provider string) (map[string]string, error) {
	environment := map[string]string{
		"GCI_MATRIX_INDEX": strconv.Itoa(variant.Index),
		"GCI_MATRIX_TOTAL": strconv.Itoa(variant.Total),
	}
	encoded, err := json.Marshal(variant.Values)
	if err != nil {
		return nil, fmt.Errorf("matrix: encode coordinates: %w", err)
	}
	environment["GCI_MATRIX_JSON"] = string(encoded)
	for key, value := range variant.Values {
		environment["MATRIX_"+environmentKey(key)] = value
		if strings.EqualFold(provider, "gitlab") && validShellName(key) {
			environment[key] = value
		}
	}
	if strings.EqualFold(provider, "gitlab") {
		environment["CI_NODE_INDEX"] = strconv.Itoa(variant.Index)
		environment["CI_NODE_TOTAL"] = strconv.Itoa(variant.Total)
	}
	return environment, nil
}

func MatrixJobKey(base string, variant MatrixVariant) string {
	if variant.Total <= 1 && len(variant.Values) == 0 {
		return base
	}
	return fmt.Sprintf("%s[%02d]", base, variant.Index)
}

func MatrixJobName(base string, variant MatrixVariant) string {
	if variant.Label == "" {
		return base
	}
	return base + " / " + variant.Label
}

func ResolveStaticTemplate(value string, context map[string]string) (string, error) {
	var output strings.Builder
	for {
		start := strings.Index(value, "${{")
		if start < 0 {
			output.WriteString(value)
			return output.String(), nil
		}
		output.WriteString(value[:start])
		end := strings.Index(value[start+3:], "}}")
		if end < 0 {
			return "", fmt.Errorf("template: unclosed expression")
		}
		end += start + 3
		name := strings.TrimSpace(value[start+3 : end])
		replacement, exists := lookupString(context, name)
		if !exists {
			return "", fmt.Errorf("template: unsupported static context %q", name)
		}
		output.WriteString(replacement)
		value = value[end+2:]
	}
}

func ResolveMatrixTemplate(value string, coordinates map[string]string) (string, error) {
	context := make(map[string]string, len(coordinates))
	for key, coordinate := range coordinates {
		context["matrix."+key] = coordinate
	}
	var output strings.Builder
	for {
		start := strings.Index(value, "${{")
		if start < 0 {
			output.WriteString(value)
			return output.String(), nil
		}
		output.WriteString(value[:start])
		end := strings.Index(value[start+3:], "}}")
		if end < 0 {
			return "", fmt.Errorf("template: unclosed expression")
		}
		end += start + 3
		name := strings.TrimSpace(value[start+3 : end])
		if strings.HasPrefix(strings.ToLower(name), "matrix.") {
			replacement, exists := lookupString(context, name)
			if !exists {
				return "", fmt.Errorf("template: matrix coordinate %q is unavailable", name)
			}
			output.WriteString(replacement)
		} else {
			output.WriteString(value[start : end+2])
		}
		value = value[end+2:]
	}
}

func NormalizeConcurrencyGroup(group string) (string, error) {
	group = strings.TrimSpace(group)
	if group == "" {
		return "", nil
	}
	if len(group) > 256 {
		return "", fmt.Errorf("concurrency: group exceeds 256 bytes")
	}
	for _, character := range group {
		if unicode.IsControl(character) {
			return "", fmt.Errorf("concurrency: group contains a control character")
		}
	}
	return strings.ToLower(group), nil
}

func expandGitHubMatrix(strategy *types.Strategy) ([]map[string]string, error) {
	if len(strategy.Matrix) == 0 {
		includes, err := normalizeObjectList(strategy.Include)
		if err != nil {
			return nil, fmt.Errorf("matrix: include: %w", err)
		}
		return includes, nil
	}
	combinations, err := cartesian(strategy.Matrix)
	if err != nil {
		return nil, err
	}
	if len(combinations) == 0 {
		combinations = []map[string]string{{}}
	}
	excludes, err := normalizeObjectList(strategy.Exclude)
	if err != nil {
		return nil, fmt.Errorf("matrix: exclude: %w", err)
	}
	filtered := make([]map[string]string, 0, len(combinations))
	for _, combination := range combinations {
		excluded := false
		for _, exclude := range excludes {
			if partialMatch(combination, exclude) {
				excluded = true
				break
			}
		}
		if !excluded {
			filtered = append(filtered, combination)
		}
	}
	originals := make([]map[string]string, len(filtered))
	results := make([]map[string]string, len(filtered))
	for index, combination := range filtered {
		originals[index] = copyCoordinates(combination)
		results[index] = copyCoordinates(combination)
	}
	includes, err := normalizeObjectList(strategy.Include)
	if err != nil {
		return nil, fmt.Errorf("matrix: include: %w", err)
	}
	for _, include := range includes {
		applied := false
		for index, original := range originals {
			if !compatibleInclude(original, include) {
				continue
			}
			for key, value := range include {
				results[index][key] = value
			}
			applied = true
		}
		if !applied {
			results = append(results, copyCoordinates(include))
		}
		if len(results) > MaxMatrixVariants {
			return nil, fmt.Errorf("matrix: expansion exceeds limit %d", MaxMatrixVariants)
		}
	}
	return results, nil
}

func expandGitLabMatrix(groups []map[string]interface{}) ([]map[string]string, error) {
	var combinations []map[string]string
	for index, group := range groups {
		expanded, err := cartesianValues(group)
		if err != nil {
			return nil, fmt.Errorf("matrix: GitLab group %d: %w", index+1, err)
		}
		combinations = append(combinations, expanded...)
		if len(combinations) > MaxMatrixVariants {
			return nil, fmt.Errorf("matrix: expansion exceeds limit %d", MaxMatrixVariants)
		}
	}
	return combinations, nil
}

func cartesian(dimensions map[string][]interface{}) ([]map[string]string, error) {
	values := make(map[string]interface{}, len(dimensions))
	for key, dimension := range dimensions {
		values[key] = dimension
	}
	return cartesianValues(values)
}

func cartesianValues(dimensions map[string]interface{}) ([]map[string]string, error) {
	if len(dimensions) == 0 {
		return nil, nil
	}
	if len(dimensions) > MaxMatrixDimensions {
		return nil, fmt.Errorf("%d dimensions exceed limit %d", len(dimensions), MaxMatrixDimensions)
	}
	keys := make([]string, 0, len(dimensions))
	for key := range dimensions {
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("dimension name is empty")
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	combinations := []map[string]string{{}}
	for _, key := range keys {
		values, err := dimensionValues(dimensions[key])
		if err != nil {
			return nil, fmt.Errorf("dimension %q: %w", key, err)
		}
		if len(values) == 0 {
			return nil, fmt.Errorf("dimension %q has no values", key)
		}
		if len(combinations) > MaxMatrixVariants/len(values) {
			return nil, fmt.Errorf("expansion exceeds limit %d", MaxMatrixVariants)
		}
		next := make([]map[string]string, 0, len(combinations)*len(values))
		for _, combination := range combinations {
			for _, value := range values {
				item := copyCoordinates(combination)
				item[key] = value
				next = append(next, item)
			}
		}
		combinations = next
	}
	return combinations, nil
}

func dimensionValues(value interface{}) ([]string, error) {
	switch typed := value.(type) {
	case []interface{}:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			normalized, err := scalarString(item)
			if err != nil {
				return nil, err
			}
			result = append(result, normalized)
		}
		return result, nil
	case []string:
		return append([]string(nil), typed...), nil
	default:
		normalized, err := scalarString(value)
		if err != nil {
			return nil, err
		}
		return []string{normalized}, nil
	}
}

func normalizeObjectList(items []map[string]interface{}) ([]map[string]string, error) {
	result := make([]map[string]string, 0, len(items))
	for _, item := range items {
		normalized := make(map[string]string, len(item))
		for key, value := range item {
			scalar, err := scalarString(value)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			normalized[key] = scalar
		}
		result = append(result, normalized)
	}
	return result, nil
}

func scalarString(value interface{}) (string, error) {
	switch typed := value.(type) {
	case nil:
		return "", nil
	case string:
		return typed, nil
	case bool:
		return strconv.FormatBool(typed), nil
	case int:
		return strconv.Itoa(typed), nil
	case int64:
		return strconv.FormatInt(typed, 10), nil
	case uint64:
		return strconv.FormatUint(typed, 10), nil
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64), nil
	case float32:
		return strconv.FormatFloat(float64(typed), 'g', -1, 32), nil
	case json.Number:
		return typed.String(), nil
	default:
		return "", fmt.Errorf("value of type %T is not a scalar", value)
	}
}

func compatibleInclude(original, include map[string]string) bool {
	for key, value := range include {
		if existing, found := original[key]; found && existing != value {
			return false
		}
	}
	return true
}

func partialMatch(values, filter map[string]string) bool {
	for key, expected := range filter {
		if values[key] != expected {
			return false
		}
	}
	return true
}

func canonicalCoordinates(values map[string]string) string {
	keys := sortedCoordinateKeys(values)
	var output strings.Builder
	for _, key := range keys {
		output.WriteString(strconv.Quote(key))
		output.WriteByte('=')
		output.WriteString(strconv.Quote(values[key]))
		output.WriteByte(';')
	}
	return output.String()
}

func coordinateLabel(values map[string]string) string {
	keys := sortedCoordinateKeys(values)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+values[key])
	}
	return strings.Join(parts, ", ")
}

func sortedCoordinateKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func copyCoordinates(values map[string]string) map[string]string {
	copy := make(map[string]string, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}

func environmentKey(value string) string {
	var output strings.Builder
	for _, character := range strings.ToUpper(value) {
		if character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' {
			output.WriteRune(character)
		} else {
			output.WriteByte('_')
		}
	}
	return strings.Trim(output.String(), "_")
}

func validShellName(value string) bool {
	if value == "" {
		return false
	}
	for index, character := range value {
		if index == 0 && !unicode.IsLetter(character) && character != '_' {
			return false
		}
		if index > 0 && !unicode.IsLetter(character) && !unicode.IsDigit(character) && character != '_' {
			return false
		}
	}
	return true
}

func lookupString(values map[string]string, name string) (string, bool) {
	if value, exists := values[name]; exists {
		return value, true
	}
	value, exists := values[strings.ToLower(name)]
	return value, exists
}
