package handlers

import (
	"fmt"
	"strings"

	"github.com/sanix-darker/git-ci/pkg/types"
)

// topologicalSort returns job names in dependency order using Kahn's algorithm.
// Jobs with no dependencies come first. Returns an error if a cycle is detected.
func topologicalSort(jobs map[string]*types.Job) ([]string, error) {
	// Build adjacency and in-degree maps
	inDegree := make(map[string]int)
	dependents := make(map[string][]string) // job -> jobs that depend on it

	for name := range jobs {
		inDegree[name] = 0
	}

	for name, job := range jobs {
		for _, need := range job.Needs {
			// Only count dependencies that exist in the current job set
			if _, exists := jobs[need]; exists {
				inDegree[name]++
				dependents[need] = append(dependents[need], name)
			}
			// Filtered-out dependencies are treated as satisfied
		}
	}

	// Collect all jobs with no dependencies
	var queue []string
	for name, degree := range inDegree {
		if degree == 0 {
			queue = append(queue, name)
		}
	}

	var sorted []string
	for len(queue) > 0 {
		// Pop from front (stable BFS)
		current := queue[0]
		queue = queue[1:]
		sorted = append(sorted, current)

		// Reduce in-degree for dependents
		for _, dep := range dependents[current] {
			inDegree[dep]--
			if inDegree[dep] == 0 {
				queue = append(queue, dep)
			}
		}
	}

	if len(sorted) != len(jobs) {
		// Find the cycle participants for a useful error message
		var cycleJobs []string
		for name, degree := range inDegree {
			if degree > 0 {
				cycleJobs = append(cycleJobs, name)
			}
		}
		return nil, fmt.Errorf("circular dependency detected among jobs: %s", strings.Join(cycleJobs, ", "))
	}

	return sorted, nil
}

// groupByDependencyLevel groups jobs into levels where all jobs in a level
// can run in parallel (their dependencies are in earlier levels).
func groupByDependencyLevel(jobs map[string]*types.Job) ([][]string, error) {
	// Build in-degree map (only counting deps within our job set)
	inDegree := make(map[string]int)
	dependents := make(map[string][]string)

	for name := range jobs {
		inDegree[name] = 0
	}

	for name, job := range jobs {
		for _, need := range job.Needs {
			if _, exists := jobs[need]; exists {
				inDegree[name]++
				dependents[need] = append(dependents[need], name)
			}
		}
	}

	var levels [][]string
	remaining := len(jobs)

	for remaining > 0 {
		// Collect all jobs with in-degree 0
		var level []string
		for name, degree := range inDegree {
			if degree == 0 {
				level = append(level, name)
			}
		}

		if len(level) == 0 {
			var cycleJobs []string
			for name, degree := range inDegree {
				if degree > 0 {
					cycleJobs = append(cycleJobs, name)
				}
			}
			return nil, fmt.Errorf("circular dependency detected among jobs: %s", strings.Join(cycleJobs, ", "))
		}

		// Remove this level from consideration
		for _, name := range level {
			delete(inDegree, name)
			for _, dep := range dependents[name] {
				if _, exists := inDegree[dep]; exists {
					inDegree[dep]--
				}
			}
		}

		levels = append(levels, level)
		remaining -= len(level)
	}

	return levels, nil
}
