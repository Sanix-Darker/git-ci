package execution

import (
	"errors"
	"testing"

	"github.com/sanix-darker/git-ci/internal/runnerinventory"
	"github.com/sanix-darker/git-ci/pkg/types"
)

func TestDefinitionRunnerMatchingBlocksOnlyUnavailableJobs(t *testing.T) {
	docker := false
	inventory := runnerinventory.Local(runnerinventory.Config{
		Labels: []string{"cpu"}, Tags: []string{"deploy"}, Hostname: "fixture", OS: "linux", Architecture: "amd64", DockerAvailable: &docker,
	})
	definition := Definition{Provider: ProviderGitHubActions, Jobs: []JobDefinition{
		{Key: "build", Name: "Build", RunnerRequirements: []string{"ubuntu-latest"}},
		{Key: "gpu", Name: "GPU", RunnerRequirements: []string{"self-hosted", "gpu"}},
	}}
	applyDefinitionRunnerInventory(&definition, inventory)
	if !definition.Jobs[0].RunnerMatch.Available || definition.Jobs[1].RunnerMatch.Available {
		t.Fatalf("runner matches = %#v", definition.Jobs)
	}
	err := validateDefinitionRunnerAvailability(definition)
	var unavailable *ErrRunnerUnavailable
	if !errors.As(err, &unavailable) || len(unavailable.Jobs) != 1 || unavailable.Jobs[0].Key != "gpu" || unavailable.Jobs[0].Missing[0] != "gpu" {
		t.Fatalf("availability error = %#v, %v", unavailable, err)
	}
}

func TestDefinitionRunnerMatchingRequiresDockerForRuntimeJobs(t *testing.T) {
	docker := false
	inventory := runnerinventory.Local(runnerinventory.Config{Hostname: "fixture", OS: "linux", Architecture: "amd64", DockerAvailable: &docker})
	definition := Definition{Provider: ProviderGitLabCI, Jobs: []JobDefinition{{
		Key: "integration", Container: &types.Container{Image: "alpine:3.20"},
	}}}
	applyDefinitionRunnerInventory(&definition, inventory)
	if definition.Jobs[0].RunnerMatch.Available || len(definition.Jobs[0].RunnerMatch.Missing) != 1 || definition.Jobs[0].RunnerMatch.Missing[0] != "docker" {
		t.Fatalf("container runner match = %#v", definition.Jobs[0].RunnerMatch)
	}
}
