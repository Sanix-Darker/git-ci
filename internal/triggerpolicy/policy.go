package triggerpolicy

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Policy struct {
	Event          string   `json:"event"`
	Branches       []string `json:"branches,omitempty"`
	BranchesIgnore []string `json:"branchesIgnore,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	TagsIgnore     []string `json:"tagsIgnore,omitempty"`
	Paths          []string `json:"paths,omitempty"`
	PathsIgnore    []string `json:"pathsIgnore,omitempty"`
	Workflows      []string `json:"workflows,omitempty"`
	Actions        []string `json:"actions,omitempty"`
	Schedules      []string `json:"schedules,omitempty"`
	Inputs         []Input  `json:"inputs,omitempty"`
	Condition      string   `json:"condition,omitempty"`
	Evaluable      bool     `json:"evaluable"`
}

type Input struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Type        string   `json:"type"`
	Required    bool     `json:"required"`
	Default     string   `json:"default,omitempty"`
	Options     []string `json:"options,omitempty"`
}

type Event struct {
	Type         string
	Ref          string
	Action       string
	Workflow     string
	ChangedPaths []string
	PathsKnown   bool
}

func ParseFile(provider, path string, fallback []string) ([]Policy, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("trigger policy: read workflow: %w", err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(contents, &document); err != nil {
		return nil, fmt.Errorf("trigger policy: parse workflow: %w", err)
	}
	root := documentRoot(&document)
	if root == nil || root.Kind != yaml.MappingNode {
		return fallbackPolicies(fallback), nil
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "github_actions", "github":
		return parseGitHub(root, fallback), nil
	case "gitlab_ci", "gitlab":
		return parseGitLab(root, fallback), nil
	default:
		return fallbackPolicies(fallback), nil
	}
}

func Match(policies []Policy, fallback []string, event Event) bool {
	event.Type = normalizeEvent(event.Type)
	if len(policies) == 0 {
		policies = fallbackPolicies(fallback)
	}
	for _, policy := range policies {
		_, tag, accepted := matchesEventPolicy(policy, event)
		if !accepted {
			continue
		}
		if tag == "" && (len(policy.Paths) > 0 || len(policy.PathsIgnore) > 0) {
			if !event.PathsKnown || !matchesChangedPaths(policy.Paths, policy.PathsIgnore, event.ChangedPaths) {
				continue
			}
		}
		return true
	}
	return false
}

func NeedsChangedPaths(policies []Policy, fallback []string, event Event) bool {
	event.Type = normalizeEvent(event.Type)
	if len(policies) == 0 {
		policies = fallbackPolicies(fallback)
	}
	for _, policy := range policies {
		_, tag, accepted := matchesEventPolicy(policy, event)
		if accepted && tag == "" && (len(policy.Paths) > 0 || len(policy.PathsIgnore) > 0) {
			return true
		}
	}
	return false
}

func matchesEventPolicy(policy Policy, event Event) (string, string, bool) {
	if normalizeEvent(policy.Event) != event.Type || (!policy.Evaluable && policy.Condition != "") {
		return "", "", false
	}
	action := strings.ToLower(strings.TrimSpace(event.Action))
	if event.Type == "workflow_run" {
		if len(policy.Workflows) == 0 || !contains(policy.Workflows, strings.TrimSpace(event.Workflow)) {
			return "", "", false
		}
	}
	if len(policy.Actions) > 0 {
		if !matchesAny(policy.Actions, action) {
			return "", "", false
		}
	} else if event.Type == "pull_request" && !contains([]string{"opened", "synchronize", "reopened"}, action) {
		return "", "", false
	}
	branch, tag := refParts(event.Ref)
	if branch == "" && tag == "" && (len(policy.Branches) > 0 || len(policy.BranchesIgnore) > 0 || len(policy.Tags) > 0 || len(policy.TagsIgnore) > 0) {
		return "", "", false
	}
	if tag != "" {
		if !matchesFilter(policy.Tags, policy.TagsIgnore, tag) || len(policy.Branches) > 0 {
			return "", "", false
		}
	} else if branch != "" {
		if !matchesFilter(policy.Branches, policy.BranchesIgnore, branch) || len(policy.Tags) > 0 {
			return "", "", false
		}
	}
	return branch, tag, true
}

func ResolveManualInputs(policies []Policy, provided map[string]string) (map[string]string, error) {
	definitions := make(map[string]Input)
	for _, policy := range policies {
		if normalizeEvent(policy.Event) != "workflow_dispatch" {
			continue
		}
		for _, input := range policy.Inputs {
			definitions[input.Name] = input
		}
	}
	for name := range provided {
		if _, found := definitions[name]; !found {
			return nil, fmt.Errorf("workflow input %q is not declared", name)
		}
	}
	result := make(map[string]string, len(definitions))
	for name, input := range definitions {
		value, found := provided[name]
		value = strings.TrimSpace(value)
		if !found || value == "" {
			value = input.Default
		}
		if input.Required && value == "" {
			return nil, fmt.Errorf("workflow input %q is required", name)
		}
		if value == "" {
			continue
		}
		switch input.Type {
		case "boolean":
			if value != "true" && value != "false" {
				return nil, fmt.Errorf("workflow input %q must be true or false", name)
			}
		case "number":
			if _, err := strconv.ParseFloat(value, 64); err != nil {
				return nil, fmt.Errorf("workflow input %q must be a number", name)
			}
		case "choice":
			if !contains(input.Options, value) {
				return nil, fmt.Errorf("workflow input %q must be one of %s", name, strings.Join(input.Options, ", "))
			}
		}
		result[name] = value
	}
	return result, nil
}

func parseGitHub(root *yaml.Node, fallback []string) []Policy {
	on := mappingValue(root, "on")
	if on == nil {
		return fallbackPolicies(fallback)
	}
	policies := make([]Policy, 0)
	switch on.Kind {
	case yaml.ScalarNode:
		policies = append(policies, Policy{Event: normalizeEvent(on.Value), Evaluable: true})
	case yaml.SequenceNode:
		for _, event := range on.Content {
			policies = append(policies, Policy{Event: normalizeEvent(event.Value), Evaluable: true})
		}
	case yaml.MappingNode:
		for index := 0; index+1 < len(on.Content); index += 2 {
			event, config := on.Content[index].Value, on.Content[index+1]
			policy := Policy{Event: normalizeEvent(event), Evaluable: true}
			if config.Kind == yaml.MappingNode {
				policy.Branches = nodeStrings(mappingValue(config, "branches"))
				policy.BranchesIgnore = nodeStrings(mappingValue(config, "branches-ignore"))
				policy.Tags = nodeStrings(mappingValue(config, "tags"))
				policy.TagsIgnore = nodeStrings(mappingValue(config, "tags-ignore"))
				policy.Paths = nodeStrings(mappingValue(config, "paths"))
				policy.PathsIgnore = nodeStrings(mappingValue(config, "paths-ignore"))
				policy.Workflows = nodeStrings(mappingValue(config, "workflows"))
				policy.Actions = nodeStrings(mappingValue(config, "types"))
				if policy.Event == "workflow_dispatch" {
					policy.Inputs = parseInputs(mappingValue(config, "inputs"))
				}
			}
			if policy.Event == "schedule" {
				for _, item := range sequenceNodes(config) {
					if cron := mappingValue(item, "cron"); cron != nil {
						policy.Schedules = append(policy.Schedules, cron.Value)
					}
				}
			}
			policies = append(policies, policy)
		}
	}
	return policies
}

func parseGitLab(root *yaml.Node, fallback []string) []Policy {
	workflow := mappingValue(root, "workflow")
	rules := mappingValue(workflow, "rules")
	if rules == nil || rules.Kind != yaml.SequenceNode {
		policies := fallbackPolicies(fallback)
		if len(policies) == 0 {
			policies = []Policy{{Event: "push", Evaluable: true}}
		}
		return policies
	}
	policies := make([]Policy, 0, len(rules.Content))
	for _, rule := range rules.Content {
		condition := nodeValue(mappingValue(rule, "if"))
		event, evaluable := inferGitLabEvent(condition)
		policy := Policy{Event: event, Condition: condition, Evaluable: evaluable}
		changes := mappingValue(rule, "changes")
		if changes != nil && changes.Kind == yaml.MappingNode {
			changes = mappingValue(changes, "paths")
		}
		policy.Paths = nodeStrings(changes)
		policies = append(policies, policy)
	}
	return policies
}

func inferGitLabEvent(condition string) (string, bool) {
	condition = strings.TrimSpace(condition)
	if condition == "" {
		return "push", true
	}
	if strings.Contains(condition, "merge_request_event") {
		return "pull_request", condition == `$CI_PIPELINE_SOURCE == "merge_request_event"` || condition == `$CI_PIPELINE_SOURCE == 'merge_request_event'`
	}
	if strings.Contains(condition, `"push"`) || strings.Contains(condition, `'push'`) {
		return "push", condition == `$CI_PIPELINE_SOURCE == "push"` || condition == `$CI_PIPELINE_SOURCE == 'push'`
	}
	if condition == "$CI_COMMIT_BRANCH" || condition == "$CI_COMMIT_TAG" {
		return "push", true
	}
	return "push", false
}

func parseInputs(node *yaml.Node) []Input {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	inputs := make([]Input, 0, len(node.Content)/2)
	for index := 0; index+1 < len(node.Content); index += 2 {
		name, config := node.Content[index].Value, node.Content[index+1]
		input := Input{Name: name, Type: "string"}
		if config.Kind == yaml.MappingNode {
			input.Description = nodeValue(mappingValue(config, "description"))
			input.Type = strings.ToLower(nodeValue(mappingValue(config, "type")))
			if input.Type == "" {
				input.Type = "string"
			}
			input.Required = strings.EqualFold(nodeValue(mappingValue(config, "required")), "true")
			input.Default = nodeValue(mappingValue(config, "default"))
			input.Options = nodeStrings(mappingValue(config, "options"))
		}
		inputs = append(inputs, input)
	}
	return inputs
}

func fallbackPolicies(events []string) []Policy {
	policies := make([]Policy, 0, len(events))
	seen := make(map[string]struct{})
	for _, event := range events {
		event = normalizeEvent(event)
		if event == "" {
			continue
		}
		if _, found := seen[event]; found {
			continue
		}
		seen[event] = struct{}{}
		policies = append(policies, Policy{Event: event, Evaluable: true})
	}
	sort.Slice(policies, func(i, j int) bool { return policies[i].Event < policies[j].Event })
	return policies
}

func normalizeEvent(event string) string {
	event = strings.ToLower(strings.TrimSpace(event))
	switch event {
	case "manual":
		return "workflow_dispatch"
	case "merge_request", "merge_request_event", "merge request hook":
		return "pull_request"
	case "push hook":
		return "push"
	}
	return event
}

func refParts(ref string) (string, string) {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, "refs/heads/") {
		return strings.TrimPrefix(ref, "refs/heads/"), ""
	}
	if strings.HasPrefix(ref, "refs/tags/") {
		return "", strings.TrimPrefix(ref, "refs/tags/")
	}
	return strings.TrimPrefix(ref, "heads/"), ""
}

func matchesFilter(include, exclude []string, value string) bool {
	if len(include) > 0 && !matchesOrdered(include, value) {
		return false
	}
	return !matchesAny(exclude, value)
}

func matchesChangedPaths(include, exclude, paths []string) bool {
	for _, path := range paths {
		if (len(include) == 0 || matchesOrdered(include, path)) && !matchesAny(exclude, path) {
			return true
		}
	}
	return false
}

func matchesOrdered(patterns []string, value string) bool {
	matched := false
	for _, pattern := range patterns {
		negative := strings.HasPrefix(pattern, "!")
		pattern = strings.TrimPrefix(pattern, "!")
		if globMatch(pattern, value) {
			matched = !negative
		}
	}
	return matched
}

func matchesAny(patterns []string, value string) bool {
	for _, pattern := range patterns {
		if globMatch(pattern, value) {
			return true
		}
	}
	return false
}

func globMatch(pattern, value string) bool {
	var expression strings.Builder
	expression.WriteString("^")
	for index := 0; index < len(pattern); index++ {
		switch pattern[index] {
		case '*':
			if index+1 < len(pattern) && pattern[index+1] == '*' {
				expression.WriteString(".*")
				index++
			} else {
				expression.WriteString("[^/]*")
			}
		case '?':
			expression.WriteString("[^/]")
		default:
			expression.WriteString(regexp.QuoteMeta(string(pattern[index])))
		}
	}
	expression.WriteString("$")
	matched, _ := regexp.MatchString(expression.String(), value)
	return matched
}

func documentRoot(document *yaml.Node) *yaml.Node {
	if document == nil {
		return nil
	}
	if document.Kind == yaml.DocumentNode && len(document.Content) > 0 {
		return document.Content[0]
	}
	return document
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			return node.Content[index+1]
		}
	}
	return nil
}

func nodeStrings(node *yaml.Node) []string {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.SequenceNode {
		values := make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			values = append(values, nodeValue(item))
		}
		return values
	}
	if node.Kind == yaml.ScalarNode && node.Value != "" {
		return []string{node.Value}
	}
	return nil
}

func sequenceNodes(node *yaml.Node) []*yaml.Node {
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil
	}
	return node.Content
}

func nodeValue(node *yaml.Node) string {
	if node == nil {
		return ""
	}
	return strings.TrimSpace(node.Value)
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
