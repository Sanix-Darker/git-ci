// Package runnerinventory models the execution capacity exposed by the
// single-process git-ci service and performs provider-correct label matching.
package runnerinventory

import (
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
)

const (
	ProviderGitHub = "github"
	ProviderGitLab = "gitlab"
)

type Config struct {
	Labels          []string
	Tags            []string
	Group           string
	Hostname        string
	OS              string
	Architecture    string
	DockerAvailable *bool
}

type Runner struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Status          string   `json:"status"`
	Mode            string   `json:"mode"`
	OS              string   `json:"os"`
	Architecture    string   `json:"architecture"`
	Group           string   `json:"group"`
	Labels          []string `json:"labels"`
	Tags            []string `json:"tags"`
	DockerAvailable bool     `json:"dockerAvailable"`
	RunUntagged     bool     `json:"runUntagged"`
	MaxParallel     int      `json:"maxParallel"`
}

type Inventory struct {
	Runners []Runner `json:"items"`
}

type Requirement struct {
	Provider       string
	Labels         []string
	Group          string
	RequiresDocker bool
}

type Match struct {
	Evaluated bool     `json:"evaluated"`
	Available bool     `json:"available"`
	RunnerID  string   `json:"runnerId,omitempty"`
	Runner    string   `json:"runner,omitempty"`
	Required  []string `json:"required,omitempty"`
	Missing   []string `json:"missing,omitempty"`
	Group     string   `json:"group,omitempty"`
	Reason    string   `json:"reason"`
}

func Local(config Config) Inventory {
	hostOS := strings.ToLower(strings.TrimSpace(config.OS))
	if hostOS == "" {
		hostOS = runtime.GOOS
	}
	architecture := normalizeArchitecture(config.Architecture)
	if architecture == "" {
		architecture = normalizeArchitecture(runtime.GOARCH)
	}
	hostname := strings.TrimSpace(config.Hostname)
	if hostname == "" {
		hostname, _ = os.Hostname()
	}
	if hostname == "" {
		hostname = "local"
	}
	group := strings.TrimSpace(config.Group)
	if group == "" {
		group = "default"
	}
	dockerAvailable := false
	if config.DockerAvailable != nil {
		dockerAvailable = *config.DockerAvailable
	} else if _, err := exec.LookPath("docker"); err == nil {
		dockerAvailable = true
	}
	labels := uniqueFolded(append([]string{"gci", "local", "self-hosted", hostOS, architecture}, config.Labels...))
	if dockerAvailable {
		labels = uniqueFolded(append(labels, "docker"))
	}
	return Inventory{Runners: []Runner{{
		ID: "local", Name: hostname, Status: "online", Mode: "serial", OS: hostOS,
		Architecture: architecture, Group: group, Labels: labels, Tags: uniqueExact(config.Tags),
		DockerAvailable: dockerAvailable, RunUntagged: true, MaxParallel: 1,
	}}}
}

func (inventory Inventory) Snapshot() Inventory {
	copy := Inventory{Runners: make([]Runner, len(inventory.Runners))}
	for index, runner := range inventory.Runners {
		runner.Labels = append([]string(nil), runner.Labels...)
		runner.Tags = append([]string(nil), runner.Tags...)
		copy.Runners[index] = runner
	}
	return copy
}

func (inventory Inventory) Match(requirement Requirement) Match {
	required := append([]string(nil), requirement.Labels...)
	if requirement.RequiresDocker {
		required = append(required, "docker")
	}
	match := Match{Evaluated: true, Required: required, Group: strings.TrimSpace(requirement.Group)}
	for _, runner := range inventory.Runners {
		if !strings.EqualFold(runner.Status, "online") {
			continue
		}
		missing := missingRequirements(runner, requirement)
		if len(missing) == 0 {
			match.Available = true
			match.RunnerID = runner.ID
			match.Runner = runner.Name
			match.Reason = "online local runner matches all requirements"
			return match
		}
		if len(match.Missing) == 0 || len(missing) < len(match.Missing) {
			match.Missing = missing
		}
	}
	if len(inventory.Runners) == 0 {
		match.Reason = "no runners are registered"
	} else if len(match.Missing) > 0 {
		match.Reason = "missing runner requirements: " + strings.Join(match.Missing, ", ")
	} else {
		match.Reason = "no online runner is available"
	}
	return match
}

func missingRequirements(runner Runner, requirement Requirement) []string {
	missing := make([]string, 0)
	if requirement.Group != "" && !strings.EqualFold(runner.Group, requirement.Group) {
		missing = append(missing, "group:"+requirement.Group)
	}
	switch strings.ToLower(strings.TrimSpace(requirement.Provider)) {
	case ProviderGitHub:
		for _, label := range uniqueFolded(requirement.Labels) {
			if !matchesGitHubLabel(runner, label) {
				missing = append(missing, label)
			}
		}
	case ProviderGitLab:
		if len(requirement.Labels) == 0 && !runner.RunUntagged {
			missing = append(missing, "untagged")
		}
		for _, tag := range uniqueExact(requirement.Labels) {
			if !containsExact(runner.Tags, tag) {
				missing = append(missing, tag)
			}
		}
	}
	if requirement.RequiresDocker && !runner.DockerAvailable {
		missing = append(missing, "docker")
	}
	return missing
}

func matchesGitHubLabel(runner Runner, label string) bool {
	if containsFolded(runner.Labels, label) {
		return true
	}
	lower := strings.ToLower(strings.TrimSpace(label))
	switch {
	case strings.HasPrefix(lower, "ubuntu-"):
		return runner.OS == "linux"
	case strings.HasPrefix(lower, "windows-"):
		return runner.OS == "windows"
	case strings.HasPrefix(lower, "macos-"):
		return runner.OS == "darwin"
	default:
		return false
	}
}

func normalizeArchitecture(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "amd64", "x86_64", "x64":
		return "x64"
	case "arm64", "aarch64":
		return "arm64"
	case "386", "x86":
		return "x86"
	case "arm", "arm32":
		return "arm"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func uniqueFolded(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func uniqueExact(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func containsFolded(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}

func containsExact(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
