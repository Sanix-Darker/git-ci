package execution

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/sanix-darker/git-ci/internal/runnerinventory"
)

type UnavailableRunnerJob struct {
	Key     string   `json:"key"`
	Name    string   `json:"name"`
	Missing []string `json:"missing"`
	Reason  string   `json:"reason"`
}

type ErrRunnerUnavailable struct {
	Jobs []UnavailableRunnerJob `json:"jobs"`
}

func (err *ErrRunnerUnavailable) Error() string {
	if err == nil || len(err.Jobs) == 0 {
		return "execution: no eligible runner is available"
	}
	parts := make([]string, 0, len(err.Jobs))
	for _, job := range err.Jobs {
		parts = append(parts, fmt.Sprintf("%s (%s)", job.Key, strings.Join(job.Missing, ", ")))
	}
	return "execution: no eligible runner for " + strings.Join(parts, "; ")
}

func applyDefinitionsRunnerInventory(definitions []Definition, inventory runnerinventory.Inventory) {
	for index := range definitions {
		applyDefinitionRunnerInventory(&definitions[index], inventory)
	}
}

func applyDefinitionRunnerInventory(definition *Definition, inventory runnerinventory.Inventory) {
	if definition == nil {
		return
	}
	for index := range definition.Jobs {
		job := &definition.Jobs[index]
		if job.ChildPipeline != nil {
			job.RunnerMatch = runnerinventory.Match{}
			if job.ChildPipeline.Definition != nil {
				applyDefinitionRunnerInventory(job.ChildPipeline.Definition, inventory)
			}
			continue
		}
		job.RunnerMatch = inventory.Match(runnerinventory.Requirement{
			Provider: string(definition.Provider), Labels: job.RunnerRequirements, Group: job.RunnerGroup,
			RequiresDocker: job.Container != nil || len(job.Services) > 0,
		})
	}
}

func validateDefinitionRunnerAvailability(definition Definition) error {
	blocked := make([]UnavailableRunnerJob, 0)
	for _, job := range definition.Jobs {
		if job.ChildPipeline != nil {
			if job.ChildPipeline.Definition != nil {
				if err := validateDefinitionRunnerAvailability(*job.ChildPipeline.Definition); err != nil {
					var unavailable *ErrRunnerUnavailable
					if errors.As(err, &unavailable) {
						blocked = append(blocked, unavailable.Jobs...)
					}
				}
			}
			continue
		}
		if !job.RunnerMatch.Evaluated || job.RunnerMatch.Available {
			continue
		}
		blocked = append(blocked, UnavailableRunnerJob{
			Key: job.Key, Name: job.Name, Missing: append([]string(nil), job.RunnerMatch.Missing...), Reason: job.RunnerMatch.Reason,
		})
	}
	if len(blocked) == 0 {
		return nil
	}
	sort.Slice(blocked, func(i, j int) bool { return blocked[i].Key < blocked[j].Key })
	return &ErrRunnerUnavailable{Jobs: blocked}
}
