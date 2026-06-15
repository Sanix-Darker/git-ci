package parsers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sanix-darker/git-ci/pkg/types"
	yaml "gopkg.in/yaml.v3"
)

// TravisParser parses Travis CI configuration files (.travis.yml)
type TravisParser struct {
	baseDir string
}

// NewTravisParser creates a new Travis CI parser
func NewTravisParser() *TravisParser {
	return &TravisParser{}
}

// Travis CI top-level structures
type TravisConfig struct {
	Language      string                 `yaml:"language,omitempty"`
	OS            interface{}            `yaml:"os,omitempty"`
	Dist          string                 `yaml:"dist,omitempty"`
	Arch          string                 `yaml:"arch,omitempty"`
	Env           *TravisEnv             `yaml:"env,omitempty"`
	Services      []string               `yaml:"services,omitempty"`
	Cache         map[string]interface{} `yaml:"cache,omitempty"`
	BeforeInstall []interface{}          `yaml:"before_install,omitempty"`
	Install       []interface{}          `yaml:"install,omitempty"`
	BeforeScript  []interface{}          `yaml:"before_script,omitempty"`
	Script        []interface{}          `yaml:"script,omitempty"`
	AfterSuccess  []interface{}          `yaml:"after_success,omitempty"`
	AfterFailure  []interface{}          `yaml:"after_failure,omitempty"`
	BeforeDeploy  []interface{}          `yaml:"before_deploy,omitempty"`
	Deploy        interface{}            `yaml:"deploy,omitempty"`
	AfterDeploy   []interface{}          `yaml:"after_deploy,omitempty"`
	Jobs          *TravisJobs            `yaml:"jobs,omitempty"`
	Stages        []interface{}          `yaml:"stages,omitempty"`
	Branches      *TravisBranches        `yaml:"branches,omitempty"`
	Notifications interface{}            `yaml:"notifications,omitempty"`
	Git           *TravisGit             `yaml:"git,omitempty"`
	If            string                 `yaml:"if,omitempty"`
}

type TravisEnv struct {
	Global []interface{} `yaml:"global,omitempty"`
	Matrix []interface{} `yaml:"matrix,omitempty"`
}

type TravisJobs struct {
	Include       []TravisJobInclude `yaml:"include,omitempty"`
	Exclude       []interface{}      `yaml:"exclude,omitempty"`
	AllowFailures []interface{}      `yaml:"allow_failures,omitempty"`
	FastFinish    bool               `yaml:"fast_finish,omitempty"`
}

type TravisJobInclude struct {
	Stage        string                 `yaml:"stage,omitempty"`
	Name         string                 `yaml:"name,omitempty"`
	Lang         string                 `yaml:"language,omitempty"`
	OS           string                 `yaml:"os,omitempty"`
	Dist         string                 `yaml:"dist,omitempty"`
	Arch         string                 `yaml:"arch,omitempty"`
	If           string                 `yaml:"if,omitempty"`
	Env          interface{}            `yaml:"env,omitempty"`
	Script       interface{}            `yaml:"script,omitempty"`
	BeforeScript interface{}            `yaml:"before_script,omitempty"`
	AfterSuccess interface{}            `yaml:"after_success,omitempty"`
	AfterFailure interface{}            `yaml:"after_failure,omitempty"`
	BeforeDeploy interface{}            `yaml:"before_deploy,omitempty"`
	Deploy       interface{}            `yaml:"deploy,omitempty"`
	AfterDeploy  interface{}            `yaml:"after_deploy,omitempty"`
	Addons       interface{}            `yaml:"addons,omitempty"`
	Services     []string               `yaml:"services,omitempty"`
	Cache        map[string]interface{} `yaml:"cache,omitempty"`
	Workspace    *TravisWorkspace       `yaml:"workspace,omitempty"`
}

type TravisStage struct {
	Name string `yaml:"name,omitempty"`
	If   string `yaml:"if,omitempty"`
}

type TravisBranches struct {
	Only   []string `yaml:"only,omitempty"`
	Except []string `yaml:"except,omitempty"`
}

type TravisGit struct {
	Depth      int    `yaml:"depth,omitempty"`
	Submodules bool   `yaml:"submodules,omitempty"`
	Strategy   string `yaml:"strategy,omitempty"`
	Quiet      bool   `yaml:"quiet,omitempty"`
	LfsSkip    bool   `yaml:"lfs_skip,omitempty"`
}

type TravisWorkspace struct {
	Size string `yaml:"size,omitempty"`
}

// languageVersionField maps language to the YAML field that holds version list
var languageVersionField = map[string]string{
	"go":          "go",
	"node_js":     "node_js",
	"python":      "python",
	"ruby":        "rvm",
	"java":        "jdk",
	"php":         "php",
	"rust":        "rust",
	"scala":       "scala",
	"c":           "compiler",
	"cpp":         "compiler",
	"objective-c": "xcode_scheme",
	"generic":     "",
}

// Parse parses a Travis CI configuration file
func (p *TravisParser) Parse(ciFilePath string) (*types.Pipeline, error) {
	p.baseDir = filepath.Dir(ciFilePath)

	if _, err := os.Stat(ciFilePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("Travis CI file not found: %s", ciFilePath)
	}

	data, err := os.ReadFile(ciFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read Travis CI file: %w", err)
	}

	if len(data) == 0 {
		return nil, fmt.Errorf("Travis CI file is empty: %s", ciFilePath)
	}

	// Parse YAML into raw map first to extract language-specific version lists
	var rawData map[string]interface{}
	if err := yaml.Unmarshal(data, &rawData); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	var config TravisConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse Travis config: %w", err)
	}

	pipeline := p.convertToPipeline(&config, rawData)
	if err := p.Validate(pipeline); err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	return pipeline, nil
}

// convertToPipeline converts Travis config to generic Pipeline
func (p *TravisParser) convertToPipeline(config *TravisConfig, rawData map[string]interface{}) *types.Pipeline {
	pipeline := &types.Pipeline{
		Name:     "Travis CI Pipeline",
		Provider: "travis",
		Jobs:     make(map[string]*types.Job),
		Stages:   p.extractStages(config),
	}

	// Get language-specific version list for matrix
	versionList := p.getVersionList(rawData, config.Language)

	// Extract global environment variables
	globalEnv := make(map[string]string)
	if config.Env != nil {
		globalEnv = p.parseEnvList(config.Env.Global, "")
	}

	// Build the primary job script from lifecycle hooks
	mainScript := p.getLifecycleScript(config)

	// --- STEP 1: Matrix expansion from version list (before include processing) ---
	if len(versionList) > 0 && len(mainScript) > 0 {
		excludes := p.parseExcludes(config)
		for _, version := range versionList {
			// Apply excludes to filter out unwanted combinations
			os := p.firstOS(config.OS)
			if os == "" {
				os = "linux"
			}
			if p.isExcluded(excludes, version, os, config.Language) {
				continue
			}

			jobName := fmt.Sprintf("test (%s)", version)
			env := make(map[string]string)
			for k, v := range globalEnv {
				env[k] = v
			}
			env[strings.ToUpper(config.Language)+"_VERSION"] = version

			job := &types.Job{
				Name:        jobName,
				RunsOn:      os,
				Stage:       "test",
				Image:       fmt.Sprintf("%s:%s", p.imageForLanguage(config.Language), version),
				Environment: env,
				Steps: []types.Step{
					{
						Name:   "Test",
						Run:    strings.Join(mainScript, "\n"),
						Script: mainScript,
					},
				},
			}
			job.Strategy = &types.Strategy{
				Matrix: map[string][]interface{}{
					config.Language: {version},
				},
			}
			pipeline.Jobs[jobName] = job
		}
	} else if len(mainScript) > 0 {
		// No version list — create a single test job from lifecycle scripts
		primaryJob := &types.Job{
			Name:        "test",
			RunsOn:      fmt.Sprintf("%s-%s", config.OS, config.Dist),
			Stage:       "test",
			Image:       p.imageForLanguage(config.Language),
			Environment: globalEnv,
			Steps: []types.Step{
				{
					Name:   "Test",
					Run:    strings.Join(mainScript, "\n"),
					Script: mainScript,
				},
			},
		}
		pipeline.Jobs["test"] = primaryJob
	}

	// --- STEP 2: Process jobs.include entries (with unique names) ---
	if config.Jobs != nil && len(config.Jobs.Include) > 0 {
		for _, include := range config.Jobs.Include {
			stage := include.Stage
			if stage == "" {
				stage = "test"
			}

			includeScript := p.getIncludeScript(&include)
			jobName := p.uniqueIncludeName(&include, stage, config, pipeline.Jobs)

			// Merge env vars
			jobEnv := make(map[string]string)
			for k, v := range globalEnv {
				jobEnv[k] = v
			}
			if envStr, ok := include.Env.(string); ok {
				parsed := p.parseEnvString(envStr)
				for k, v := range parsed {
					jobEnv[k] = v
				}
			}

			job := &types.Job{
				Name:        jobName,
				RunsOn:      p.resolveRunner(config, &include),
				Stage:       stage,
				Image:       p.imageForLanguage(p.detectLanguage(include.Lang, config.Language)),
				Environment: jobEnv,
				If:          include.If,
				Steps: []types.Step{
					{
						Name:   fmt.Sprintf("%s script", stage),
						Run:    strings.Join(includeScript, "\n"),
						Script: includeScript,
					},
				},
			}
			pipeline.Jobs[jobName] = job
		}
	}

	// --- STEP 3: Handle top-level job definitions (e.g., build:) ---
	p.addTopLevelJobs(pipeline, rawData, config, globalEnv)

	// --- STEP 4: Ensure all job stages are in the stages list ---
	stagesSet := make(map[string]bool)
	for _, s := range pipeline.Stages {
		stagesSet[s] = true
	}
	for _, job := range pipeline.Jobs {
		if job.Stage != "" && !stagesSet[job.Stage] {
			pipeline.Stages = append(pipeline.Stages, job.Stage)
			stagesSet[job.Stage] = true
		}
	}

	// Set sequential stage ordering based on dependencies
	p.setStageDependencies(pipeline, config)

	return pipeline
}

// getVersionList extracts the language-specific version list from raw YAML
func (p *TravisParser) getVersionList(rawData map[string]interface{}, language string) []string {
	field, ok := languageVersionField[language]
	if !ok || field == "" {
		return nil
	}

	versionsRaw, ok := rawData[field]
	if !ok {
		return nil
	}

	switch v := versionsRaw.(type) {
	case []interface{}:
		var versions []string
		for _, item := range v {
			if str, ok := item.(string); ok {
				versions = append(versions, str)
			}
		}
		return versions
	case string:
		return []string{v}
	}

	return nil
}

// normalizeScript normalizes a script field (string or []interface{}) to []string
func (p *TravisParser) normalizeScript(raw interface{}) []string {
	if raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case string:
		if v == "skip" {
			return nil
		}
		return []string{v}
	case []interface{}:
		var result []string
		for _, item := range v {
			if str, ok := item.(string); ok {
				result = append(result, str)
			}
		}
		return result
	}
	return nil
}

// getLifecycleScript collects all lifecycle script commands in order
func (p *TravisParser) getLifecycleScript(config *TravisConfig) []string {
	var script []string

	script = append(script, p.parseScriptArray(config.BeforeInstall)...)

	if len(config.Install) > 0 {
		script = append(script, p.parseScriptArray(config.Install)...)
	}

	if len(config.BeforeScript) > 0 {
		script = append(script, p.parseScriptArray(config.BeforeScript)...)
	}

	script = append(script, p.parseScriptArray(config.Script)...)

	if len(config.AfterSuccess) > 0 {
		script = append(script, p.parseScriptArray(config.AfterSuccess)...)
	}

	if len(config.AfterFailure) > 0 {
		script = append(script, p.parseScriptArray(config.AfterFailure)...)
	}

	return script
}

// getIncludeScript collects lifecycle scripts for a job include entry
func (p *TravisParser) getIncludeScript(include *TravisJobInclude) []string {
	var script []string

	script = p.normalizeScript(include.Script)

	// Add before_deploy and after_deploy scripts
	beforeDeploy := p.normalizeScript(include.BeforeDeploy)
	script = append(script, beforeDeploy...)

	afterDeploy := p.normalizeScript(include.AfterDeploy)
	script = append(script, afterDeploy...)

	return script
}

// detectLanguage returns the effective language
func (p *TravisParser) detectLanguage(jobLang, configLang string) string {
	if jobLang != "" {
		return jobLang
	}
	if configLang != "" {
		return configLang
	}
	return "generic"
}

// imageForLanguage returns a default Docker image for a language
func (p *TravisParser) imageForLanguage(language string) string {
	switch language {
	case "go":
		return "golang:latest"
	case "node_js":
		return "node:latest"
	case "python":
		return "python:latest"
	case "ruby":
		return "ruby:latest"
	case "java":
		return "openjdk:latest"
	case "php":
		return "php:latest"
	case "rust":
		return "rust:latest"
	default:
		return "ubuntu:latest"
	}
}

// resolveRunner determines the runner for a job include
func (p *TravisParser) resolveRunner(config *TravisConfig, include *TravisJobInclude) string {
	os := include.OS
	if os == "" {
		os = p.firstOS(config.OS)
	}
	if os == "" {
		os = "linux"
	}

	dist := include.Dist
	if dist == "" {
		dist = config.Dist
	}

	if dist != "" {
		return fmt.Sprintf("%s-%s", os, dist)
	}
	return os
}

// firstOS returns the first OS value from the interface{} field
func (p *TravisParser) firstOS(os interface{}) string {
	if os == nil {
		return ""
	}
	switch v := os.(type) {
	case string:
		return v
	case []interface{}:
		if len(v) > 0 {
			if str, ok := v[0].(string); ok {
				return str
			}
		}
	case []string:
		if len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

// extractStages extracts stage names from config
func (p *TravisParser) extractStages(config *TravisConfig) []string {
	if len(config.Stages) > 0 {
		stages := make([]string, 0, len(config.Stages))
		for _, s := range config.Stages {
			switch v := s.(type) {
			case string:
				stages = append(stages, v)
			case map[string]interface{}:
				if name, ok := v["name"].(string); ok {
					stages = append(stages, name)
				}
			default:
				stages = append(stages, fmt.Sprintf("%v", s))
			}
		}
		return stages
	}

	// Extract stages from jobs.include
	if config.Jobs != nil {
		seen := make(map[string]bool)
		var stages []string
		for _, include := range config.Jobs.Include {
			if include.Stage != "" && !seen[include.Stage] {
				seen[include.Stage] = true
				stages = append(stages, include.Stage)
			}
		}
		return stages
	}

	return []string{"test", "deploy"}
}

// setStageDependencies creates stage-based dependencies
func (p *TravisParser) setStageDependencies(pipeline *types.Pipeline, config *TravisConfig) {
	if len(pipeline.Stages) == 0 {
		return
	}

	// Group jobs by stage
	jobsByStage := make(map[string][]string)
	for jobName, job := range pipeline.Jobs {
		stage := job.Stage
		jobsByStage[stage] = append(jobsByStage[stage], jobName)
	}

	// Set dependencies: jobs in stage N depend on all jobs in stage N-1
	stageOrder := pipeline.Stages
	for i := 1; i < len(stageOrder); i++ {
		prevStage := stageOrder[i-1]
		curStage := stageOrder[i]

		for _, curJob := range jobsByStage[curStage] {
			job := pipeline.Jobs[curJob]
			if job != nil {
				job.Needs = append(job.Needs, jobsByStage[prevStage]...)
			}
		}
	}
}

// parseScriptArray converts []interface{} to []string
func (p *TravisParser) parseScriptArray(data []interface{}) []string {
	var result []string
	for _, item := range data {
		if str, ok := item.(string); ok {
			result = append(result, str)
		}
	}
	return result
}

// parseEnvList parses Travis env list (supports "KEY=VALUE" strings)
func (p *TravisParser) parseEnvList(envList []interface{}, defaultKey string) map[string]string {
	result := make(map[string]string)
	if envList == nil {
		return result
	}
	for _, item := range envList {
		switch v := item.(type) {
		case string:
			parsed := p.parseEnvString(v)
			for k, val := range parsed {
				result[k] = val
			}
		case map[string]interface{}:
			for k, val := range v {
				result[k] = fmt.Sprintf("%v", val)
			}
		}
	}
	return result
}

// parseEnvString parses "KEY=VALUE" or "SECURE(KEY=VALUE)" format
func (p *TravisParser) parseEnvString(s string) map[string]string {
	result := make(map[string]string)
	s = strings.TrimSpace(s)

	// Handle secure prefix
	s = strings.TrimPrefix(s, "SECURE(")
	s = strings.TrimSuffix(s, ")")

	parts := strings.SplitN(s, "=", 2)
	if len(parts) == 2 {
		result[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}

	return result
}

// travisReservedKeys are YAML top-level keys that are NOT job definitions.
var travisReservedKeys = map[string]bool{
	"language": true, "os": true, "dist": true, "arch": true,
	"env": true, "services": true, "cache": true,
	"before_install": true, "install": true, "before_script": true,
	"script": true, "after_success": true, "after_failure": true,
	"before_deploy": true, "deploy": true, "after_deploy": true,
	"jobs": true, "stages": true, "branches": true,
	"notifications": true, "git": true, "if": true,
	"version": true, "name": true, "import": true,
	"sudo": true, "group": true, "addons": true,
}

// parseExcludes converts raw exclude entries into a list of field matchers.
func (p *TravisParser) parseExcludes(config *TravisConfig) []map[string]string {
	var excludes []map[string]string
	if config.Jobs == nil {
		return excludes
	}
	for _, raw := range config.Jobs.Exclude {
		if m, ok := raw.(map[string]interface{}); ok {
			entry := make(map[string]string)
			for k, v := range m {
				if str, ok := v.(string); ok {
					entry[k] = str
				}
			}
			if len(entry) > 0 {
				excludes = append(excludes, entry)
			}
		}
	}
	return excludes
}

// isExcluded checks whether a version+os combination should be excluded.
func (p *TravisParser) isExcluded(excludes []map[string]string, version, os, language string) bool {
	versionField := ""
	if f, ok := languageVersionField[language]; ok {
		versionField = f
	}
	for _, exclude := range excludes {
		// Check version field match (e.g., node_js: "16")
		if versionField != "" && exclude[versionField] != "" && exclude[versionField] != version {
			continue
		}
		// Check OS match
		if exclude["os"] != "" && exclude["os"] != os {
			continue
		}
		// All non-empty fields in this exclude match — skip this combination
		return true
	}
	return false
}

// uniqueIncludeName generates a unique job name for a jobs.include entry.
func (p *TravisParser) uniqueIncludeName(include *TravisJobInclude, stage string, config *TravisConfig, existingJobs map[string]*types.Job) string {
	if include.Name != "" {
		return include.Name
	}

	lang := p.detectLanguage(include.Lang, config.Language)
	parts := []string{stage, lang}

	if include.OS != "" {
		parts = append(parts, include.OS)
	}

	// Add env suffix for disambiguation (e.g., COVERAGE=true, EXPERIMENTAL=true)
	if include.Env != nil {
		if envStr, ok := include.Env.(string); ok {
			if parsed := p.parseEnvString(envStr); len(parsed) > 0 {
				for k := range parsed {
					parts = append(parts, strings.ToLower(k))
					break
				}
			}
		}
	}

	baseName := strings.Join(parts, "-")

	// Ensure uniqueness by appending a counter if needed
	if _, exists := existingJobs[baseName]; !exists {
		return baseName
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", baseName, i)
		if _, exists := existingJobs[candidate]; !exists {
			return candidate
		}
	}
}

// addTopLevelJobs handles Travis top-level keys that are job definitions
// (e.g., "build:" at the top level creates a build job).
func (p *TravisParser) addTopLevelJobs(pipeline *types.Pipeline, rawData map[string]interface{}, config *TravisConfig, globalEnv map[string]string) {
	for key, val := range rawData {
		if travisReservedKeys[key] {
			continue
		}
		if jobMap, ok := val.(map[string]interface{}); ok {
			stage := "test"
			if s, ok := jobMap["stage"].(string); ok {
				stage = s
			}

			var script []string
			if s, ok := jobMap["script"]; ok {
				script = p.normalizeScript(s)
			}

			if len(script) == 0 {
				continue
			}

			job := &types.Job{
				Name:        key,
				Stage:       stage,
				Image:       p.imageForLanguage(config.Language),
				Environment: globalEnv,
				Steps: []types.Step{
					{
						Name:   fmt.Sprintf("%s script", stage),
						Run:    strings.Join(script, "\n"),
						Script: script,
					},
				},
			}
			pipeline.Jobs[key] = job
		}
	}
}

// Validate validates the parsed pipeline
func (p *TravisParser) Validate(pipeline *types.Pipeline) error {
	if pipeline == nil {
		return fmt.Errorf("pipeline is nil")
	}

	var errors []string
	if len(pipeline.Jobs) == 0 {
		errors = append(errors, "no jobs defined in pipeline")
	}

	jobIDs := make(map[string]bool)
	for jobID := range pipeline.Jobs {
		jobIDs[jobID] = true
	}

	for jobID, job := range pipeline.Jobs {
		if len(job.Steps) == 0 {
			errors = append(errors, fmt.Sprintf("job '%s' has no script", jobID))
		}

		for _, need := range job.Needs {
			if !jobIDs[need] {
				errors = append(errors, fmt.Sprintf("job '%s' depends on non-existent job '%s'", jobID, need))
			}
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("validation errors:\n  - %s", strings.Join(errors, "\n  - "))
	}

	return nil
}

// GetProviderName returns the name of this parser
func (p *TravisParser) GetProviderName() string {
	return "travis"
}

// ParseDirectory parses all Travis CI files in a directory
func (p *TravisParser) ParseDirectory(dir string) ([]*types.Pipeline, error) {
	var pipelines []*types.Pipeline

	mainFile := filepath.Join(dir, ".travis.yml")
	if _, err := os.Stat(mainFile); err == nil {
		pipeline, err := p.Parse(mainFile)
		if err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", mainFile, err)
		}
		pipelines = append(pipelines, pipeline)
	}

	altFile := filepath.Join(dir, ".travis.yaml")
	if _, err := os.Stat(altFile); err == nil {
		pipeline, err := p.Parse(altFile)
		if err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", altFile, err)
		}
		pipelines = append(pipelines, pipeline)
	}

	if len(pipelines) == 0 {
		return nil, fmt.Errorf("no Travis CI files found in %s", dir)
	}

	return pipelines, nil
}
