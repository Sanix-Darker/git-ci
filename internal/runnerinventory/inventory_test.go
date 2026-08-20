package runnerinventory

import "testing"

func TestInventoryMatchesProviderSemanticsAndCapabilities(t *testing.T) {
	docker := false
	inventory := Local(Config{
		Labels: []string{"GPU"}, Tags: []string{"linux", "Deploy"}, Group: "builders",
		Hostname: "fixture", OS: "linux", Architecture: "amd64", DockerAvailable: &docker,
	})

	github := inventory.Match(Requirement{Provider: ProviderGitHub, Labels: []string{"SELF-HOSTED", "Linux", "gpu", "ubuntu-latest"}, Group: "BUILDERS"})
	if !github.Available || github.RunnerID != "local" {
		t.Fatalf("GitHub cumulative case-insensitive match = %#v", github)
	}
	gitlab := inventory.Match(Requirement{Provider: ProviderGitLab, Labels: []string{"linux", "Deploy"}})
	if !gitlab.Available {
		t.Fatalf("GitLab exact tag match = %#v", gitlab)
	}
	caseMismatch := inventory.Match(Requirement{Provider: ProviderGitLab, Labels: []string{"deploy"}})
	if caseMismatch.Available || len(caseMismatch.Missing) != 1 || caseMismatch.Missing[0] != "deploy" {
		t.Fatalf("GitLab tags must be case-sensitive: %#v", caseMismatch)
	}
	container := inventory.Match(Requirement{Provider: ProviderGitHub, Labels: []string{"ubuntu-latest"}, RequiresDocker: true})
	if container.Available || len(container.Missing) != 1 || container.Missing[0] != "docker" {
		t.Fatalf("Docker capability match = %#v", container)
	}
}

func TestInventoryReportsMissingGroupAndLabelsDeterministically(t *testing.T) {
	docker := true
	inventory := Local(Config{Hostname: "fixture", OS: "linux", Architecture: "arm64", Group: "default", DockerAvailable: &docker})
	match := inventory.Match(Requirement{Provider: ProviderGitHub, Labels: []string{"x64", "gpu"}, Group: "special"})
	if match.Available {
		t.Fatalf("match = %#v, want unavailable", match)
	}
	want := []string{"group:special", "gpu", "x64"}
	if len(match.Missing) != len(want) {
		t.Fatalf("missing = %#v, want %#v", match.Missing, want)
	}
	for index := range want {
		if match.Missing[index] != want[index] {
			t.Fatalf("missing = %#v, want %#v", match.Missing, want)
		}
	}
}
