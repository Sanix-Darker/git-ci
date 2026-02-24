package handlers

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/sanix-darker/git-ci/pkg/types"
)

// expandMatrixJobs expands jobs with matrix strategies into concrete job instances.
// A job with matrix {os: [ubuntu, macos], go: [1.21, 1.22]} becomes 4 jobs.
// Returns the expanded job map (non-matrix jobs pass through unchanged).
func expandMatrixJobs(jobs map[string]*types.Job) map[string]*types.Job {
	expanded := make(map[string]*types.Job)

	for name, job := range jobs {
		if job.Strategy == nil || len(job.Strategy.Matrix) == 0 {
			expanded[name] = job
			continue
		}

		combinations := computeCartesianProduct(job.Strategy.Matrix)
		combinations = applyIncludes(combinations, job.Strategy.Include)
		combinations = applyExcludes(combinations, job.Strategy.Exclude)

		if len(combinations) == 0 {
			// No valid combinations after filtering, keep original
			expanded[name] = job
			continue
		}

		for _, combo := range combinations {
			expandedName := formatMatrixJobName(name, combo)
			expandedJob := cloneJob(job)

			// Inject matrix values as environment variables
			if expandedJob.Environment == nil {
				expandedJob.Environment = make(map[string]string)
			}
			for k, v := range combo {
				valStr := fmt.Sprintf("%v", v)
				expandedJob.Environment[k] = valStr
				expandedJob.Environment["MATRIX_"+strings.ToUpper(k)] = valStr
			}

			// Update job name to include matrix values
			expandedJob.Name = fmt.Sprintf("%s (%s)", job.Name, formatCombination(combo))

			// Preserve the original job name in Needs references
			// so downstream jobs can still reference the pre-expansion name
			expanded[expandedName] = expandedJob
		}
	}

	// Fix Needs references: if a job references a pre-expansion name that was
	// expanded, update to reference all expanded variants
	expandedNames := make(map[string][]string)
	for origName, job := range jobs {
		if job.Strategy != nil && len(job.Strategy.Matrix) > 0 {
			for expName := range expanded {
				if strings.HasPrefix(expName, origName+" (") {
					expandedNames[origName] = append(expandedNames[origName], expName)
				}
			}
		}
	}

	for _, job := range expanded {
		var newNeeds []string
		for _, need := range job.Needs {
			if variants, ok := expandedNames[need]; ok {
				newNeeds = append(newNeeds, variants...)
			} else {
				newNeeds = append(newNeeds, need)
			}
		}
		job.Needs = newNeeds
	}

	return expanded
}

// computeCartesianProduct computes all combinations of matrix values
func computeCartesianProduct(matrix map[string][]interface{}) []map[string]interface{} {
	if len(matrix) == 0 {
		return nil
	}

	// Sort keys for deterministic ordering
	keys := make([]string, 0, len(matrix))
	for k := range matrix {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Start with a single empty combination
	combinations := []map[string]interface{}{{}}

	for _, key := range keys {
		values := matrix[key]
		if len(values) == 0 {
			continue
		}

		var newCombinations []map[string]interface{}
		for _, combo := range combinations {
			for _, val := range values {
				newCombo := make(map[string]interface{}, len(combo)+1)
				for k, v := range combo {
					newCombo[k] = v
				}
				newCombo[key] = val
				newCombinations = append(newCombinations, newCombo)
			}
		}
		combinations = newCombinations
	}

	return combinations
}

// applyIncludes adds extra combinations from the include list.
// Each include entry either extends an existing combination or adds a new one.
func applyIncludes(combinations []map[string]interface{}, includes []map[string]interface{}) []map[string]interface{} {
	for _, inc := range includes {
		matched := false
		for i, combo := range combinations {
			if includeMatchesCombo(inc, combo) {
				// Merge additional keys into existing combination
				for k, v := range inc {
					combinations[i][k] = v
				}
				matched = true
			}
		}
		if !matched {
			// Add as a new combination
			combinations = append(combinations, inc)
		}
	}
	return combinations
}

// includeMatchesCombo checks if an include entry matches an existing combination.
// It matches when all overlapping keys have the same values (the include may have extra keys to merge in).
func includeMatchesCombo(inc, combo map[string]interface{}) bool {
	hasOverlap := false
	for k, incVal := range inc {
		if comboVal, exists := combo[k]; exists {
			hasOverlap = true
			if fmt.Sprintf("%v", incVal) != fmt.Sprintf("%v", comboVal) {
				return false
			}
		}
	}
	return hasOverlap
}

// applyExcludes removes combinations that match any exclude entry
func applyExcludes(combinations []map[string]interface{}, excludes []map[string]interface{}) []map[string]interface{} {
	if len(excludes) == 0 {
		return combinations
	}

	var result []map[string]interface{}
	for _, combo := range combinations {
		excluded := false
		for _, exc := range excludes {
			if isSubset(exc, combo) {
				excluded = true
				break
			}
		}
		if !excluded {
			result = append(result, combo)
		}
	}
	return result
}

// isSubset checks if all key-value pairs in sub exist in super with matching values
func isSubset(sub, super map[string]interface{}) bool {
	for k, v := range sub {
		if superVal, exists := super[k]; !exists || fmt.Sprintf("%v", v) != fmt.Sprintf("%v", superVal) {
			return false
		}
	}
	return true
}

// cloneJob creates a deep copy of a job via JSON marshal/unmarshal
func cloneJob(job *types.Job) *types.Job {
	data, err := json.Marshal(job)
	if err != nil {
		// Fallback: return a shallow pointer (shouldn't happen with valid jobs)
		clone := *job
		return &clone
	}
	var clone types.Job
	if err := json.Unmarshal(data, &clone); err != nil {
		c := *job
		return &c
	}
	return &clone
}

// formatMatrixJobName creates a unique name for a matrix job combination
func formatMatrixJobName(baseName string, combo map[string]interface{}) string {
	return fmt.Sprintf("%s (%s)", baseName, formatCombination(combo))
}

// formatCombination formats a combination map as a human-readable string
func formatCombination(combo map[string]interface{}) string {
	keys := make([]string, 0, len(combo))
	for k := range combo {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%v", combo[k]))
	}
	return strings.Join(parts, ", ")
}
