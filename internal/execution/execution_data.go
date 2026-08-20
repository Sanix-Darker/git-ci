package execution

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sanix-darker/git-ci/internal/store"
	"github.com/sanix-darker/git-ci/pkg/types"
)

func (m *Manager) executeBuiltinAction(ctx context.Context, run store.Run, job store.Job, step store.Step, workspace string) (bool, error) {
	if step.Action == nil {
		return false, nil
	}
	action := strings.ToLower(strings.TrimSpace(*step.Action))
	inputs, err := decodeActionInputs(step.Environment)
	if err != nil {
		return true, err
	}
	switch {
	case strings.HasPrefix(action, "actions/checkout@"):
		return true, m.appendSystem(ctx, step.ID, "using pinned commit workspace "+pointerValue(run.CommitSHA))
	case strings.HasPrefix(action, "actions/upload-artifact@"):
		return true, m.uploadArtifactAction(ctx, run, job, step, workspace, inputs)
	case strings.HasPrefix(action, "actions/download-artifact@"):
		return true, m.downloadArtifactAction(ctx, run, step, workspace, inputs)
	case strings.HasPrefix(action, "actions/cache@"):
		return true, m.restoreCacheAction(ctx, run, step, workspace, inputs)
	default:
		return false, nil
	}
}

func (m *Manager) uploadArtifactAction(ctx context.Context, run store.Run, job store.Job, step store.Step, workspace string, inputs map[string]string) error {
	name := strings.TrimSpace(inputs["name"])
	if name == "" {
		name = "artifact"
	}
	artifact, err := m.captureArtifact(ctx, run, job, &step, workspace, &types.ArtifactConfig{
		Name: name, Paths: actionPathList(inputs["path"]), When: "always",
	}, parseRetentionDays(inputs["retention-days"]))
	if errors.Is(err, errArchiveNoFiles) {
		if strings.EqualFold(strings.TrimSpace(inputs["if-no-files-found"]), "error") {
			return err
		}
		return m.appendSystem(ctx, step.ID, "artifact "+name+" matched no files")
	}
	if err != nil {
		return err
	}
	return m.appendSystem(ctx, step.ID, fmt.Sprintf("artifact %s stored (%d files, sha256:%s)", artifact.Name, artifact.FileCount, artifact.SHA256[:12]))
}

func (m *Manager) downloadArtifactAction(ctx context.Context, run store.Run, step store.Step, workspace string, inputs map[string]string) error {
	destination, err := archiveDestination(workspace, inputs["path"])
	if err != nil {
		return err
	}
	name := strings.TrimSpace(inputs["name"])
	var artifacts []store.Artifact
	if name != "" {
		artifact, err := m.store.GetRunArtifactByName(ctx, run.ID, name)
		if err != nil {
			return err
		}
		artifacts = []store.Artifact{artifact}
	} else {
		artifacts, err = m.store.ListRunArtifacts(ctx, run.ID)
		if err != nil {
			return err
		}
	}
	if len(artifacts) == 0 {
		return errors.New("execution: no artifacts are available for this run")
	}
	for _, artifact := range artifacts {
		target := destination
		if name == "" {
			target = filepath.Join(destination, safeOutputName(artifact.Name))
		}
		if err := m.archives.ExtractZip(artifact.StorageKey, target); err != nil {
			return err
		}
	}
	return m.appendSystem(ctx, step.ID, fmt.Sprintf("downloaded %d artifact archive(s)", len(artifacts)))
}

func (m *Manager) restoreCacheAction(ctx context.Context, run store.Run, step store.Step, workspace string, inputs map[string]string) error {
	key := strings.TrimSpace(inputs["key"])
	if key == "" {
		return errors.New("execution: actions/cache requires key")
	}
	found, entry, err := m.restoreCache(ctx, run, workspace, key, actionPathList(inputs["restore-keys"]))
	if err != nil {
		return err
	}
	if !found {
		return m.appendSystem(ctx, step.ID, "cache miss: "+key)
	}
	return m.appendSystem(ctx, step.ID, "cache restored: "+entry.Key)
}

func (m *Manager) restoreDeclaredCache(ctx context.Context, run store.Run, steps []store.Step, workspace string, config *types.CacheConfig) error {
	if config == nil || !cachePolicyAllows(config.Policy, "pull") {
		return nil
	}
	key := strings.TrimSpace(config.Key)
	if key == "" {
		key = "default"
	}
	found, entry, err := m.restoreCache(ctx, run, workspace, key, config.Fallback)
	if err != nil {
		return err
	}
	if stepID := jobLogStepID(steps); stepID != "" {
		message := "cache miss: " + key
		if found {
			message = "cache restored: " + entry.Key
		}
		_ = m.appendSystem(ctx, stepID, message)
	}
	return nil
}

func (m *Manager) restoreCache(ctx context.Context, run store.Run, workspace, key string, fallback []string) (bool, store.CacheEntry, error) {
	project, err := m.store.GetProject(ctx, run.ProjectID)
	if err != nil {
		return false, store.CacheEntry{}, err
	}
	entry, found, err := m.store.FindCacheEntry(ctx, run.ProjectID, cacheRefs(run, project.DefaultBranch), key, fallback)
	if err != nil || !found {
		return found, entry, err
	}
	if err := m.archives.ExtractTarGz(entry.StorageKey, workspace); err != nil {
		return false, store.CacheEntry{}, err
	}
	return true, entry, nil
}

func (m *Manager) saveJobCaches(ctx context.Context, run store.Run, steps []store.Step, workspace string, semantics *frozenJobSemantics) error {
	if semantics != nil && semantics.Cache != nil && cachePolicyAllows(semantics.Cache.Policy, "push") && cacheWhenMatches(semantics.Cache.When, true) {
		key := strings.TrimSpace(semantics.Cache.Key)
		if key == "" {
			key = "default"
		}
		if err := m.saveCache(ctx, run, workspace, key, semantics.Cache.Paths); err != nil && !errors.Is(err, errArchiveNoFiles) {
			return err
		}
	}
	for _, step := range steps {
		if step.Action == nil || !strings.HasPrefix(strings.ToLower(*step.Action), "actions/cache@") {
			continue
		}
		inputs, err := decodeActionInputs(step.Environment)
		if err != nil {
			return err
		}
		key := strings.TrimSpace(inputs["key"])
		if key == "" {
			continue
		}
		if err := m.saveCache(ctx, run, workspace, key, actionPathList(inputs["path"])); err != nil {
			if errors.Is(err, errArchiveNoFiles) {
				_ = m.appendSystem(ctx, step.ID, "cache not saved: configured paths matched no files")
				continue
			}
			return err
		}
		_ = m.appendSystem(ctx, step.ID, "cache saved: "+key)
	}
	return nil
}

func (m *Manager) saveCache(ctx context.Context, run store.Run, workspace, key string, paths []string) error {
	ref := cacheWriteRef(run)
	if _, found, err := m.store.FindCacheEntry(ctx, run.ProjectID, []string{ref}, key, nil); err != nil {
		return err
	} else if found {
		return nil
	}
	object, err := m.archives.CreateTarGz(workspace, paths, nil)
	if err != nil {
		return err
	}
	_, err = m.store.PutCacheEntry(ctx, store.PutCacheEntryParams{
		ProjectID: run.ProjectID, Ref: ref, Key: key, StorageKey: object.Key,
		SHA256: object.SHA256, SizeBytes: object.SizeBytes, FileCount: object.FileCount,
	})
	if err != nil {
		m.archives.Remove(object.Key)
		var conflict *store.ErrConflict
		if errors.As(err, &conflict) {
			return nil
		}
	}
	return err
}

func (m *Manager) captureJobArtifact(ctx context.Context, run store.Run, job store.Job, steps []store.Step, workspace string, config *types.ArtifactConfig, succeeded bool) error {
	if config == nil || !artifactWhenMatches(config.When, succeeded) {
		return nil
	}
	paths := append([]string(nil), config.Paths...)
	for kind, reportPath := range config.Reports {
		if strings.EqualFold(kind, "junit") {
			paths = append(paths, actionPathList(reportPath)...)
		}
	}
	copy := *config
	copy.Paths = paths
	artifact, err := m.captureArtifact(ctx, run, job, nil, workspace, &copy, parseExpireIn(config.ExpireIn))
	if errors.Is(err, errArchiveNoFiles) {
		if stepID := jobLogStepID(steps); stepID != "" {
			_ = m.appendSystem(ctx, stepID, "artifact paths matched no files")
		}
		return nil
	}
	if err != nil {
		return err
	}
	if stepID := jobLogStepID(steps); stepID != "" {
		_ = m.appendSystem(ctx, stepID, fmt.Sprintf("artifact %s stored (%d files)", artifact.Name, artifact.FileCount))
	}
	return nil
}

func (m *Manager) captureArtifact(ctx context.Context, run store.Run, job store.Job, step *store.Step, workspace string, config *types.ArtifactConfig, expiresAt *time.Time) (store.Artifact, error) {
	object, err := m.archives.CreateZip(workspace, config.Paths, config.Exclude)
	if err != nil {
		return store.Artifact{}, err
	}
	name := strings.TrimSpace(config.Name)
	if name == "" {
		name = job.Name
	}
	stepID := ""
	if step != nil {
		stepID = step.ID
	}
	artifact, err := m.store.CreateArtifact(ctx, store.CreateArtifactParams{
		ProjectID: run.ProjectID, RunID: run.ID, JobID: job.ID, StepID: stepID,
		Name: name, StorageKey: object.Key, SHA256: object.SHA256,
		SizeBytes: object.SizeBytes, FileCount: object.FileCount, ExpiresAt: expiresAt,
	})
	if err != nil {
		m.archives.Remove(object.Key)
		return store.Artifact{}, err
	}
	for kind, rawPaths := range config.Reports {
		if !strings.EqualFold(kind, "junit") {
			continue
		}
		summaries, err := parseJUnitReports(workspace, actionPathList(rawPaths))
		if err != nil {
			return artifact, err
		}
		for _, summary := range summaries {
			_, err := m.store.CreateTestReport(ctx, store.CreateTestReportParams{
				ArtifactID: artifact.ID, ProjectID: run.ProjectID, RunID: run.ID,
				JobID: job.ID, StepID: stepID, Name: summary.Name, Tests: summary.Tests,
				Failures: summary.Failures, Errors: summary.Errors, Skipped: summary.Skipped,
				DurationSeconds: summary.DurationSeconds,
			})
			if err != nil {
				return artifact, err
			}
		}
	}
	return artifact, nil
}

func (m *Manager) OpenRunArtifact(ctx context.Context, runID, artifactID string) (store.Artifact, *os.File, error) {
	artifact, err := m.store.GetArtifact(ctx, artifactID)
	if err != nil {
		return store.Artifact{}, nil, err
	}
	if artifact.RunID != runID {
		return store.Artifact{}, nil, &store.ErrNotFound{Resource: "artifact", Key: artifactID}
	}
	file, err := m.archives.OpenArtifact(artifact.StorageKey)
	return artifact, file, err
}

func decodeActionInputs(environment json.RawMessage) (map[string]string, error) {
	var values map[string]string
	if err := json.Unmarshal(environment, &values); err != nil {
		return nil, fmt.Errorf("execution: decode action environment: %w", err)
	}
	inputs := make(map[string]string)
	encoded := values["GCI_ACTION_INPUTS_JSON"]
	if encoded == "" {
		return inputs, nil
	}
	if err := json.Unmarshal([]byte(encoded), &inputs); err != nil {
		return nil, fmt.Errorf("execution: decode action inputs: %w", err)
	}
	return inputs, nil
}

func actionPathList(value string) []string {
	var result []string
	for _, item := range strings.FieldsFunc(value, func(r rune) bool { return r == '\n' || r == '\r' || r == ',' }) {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func archiveDestination(workspace, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return workspace, nil
	}
	if filepath.IsAbs(value) {
		value = filepath.Clean(value)
		if !pathWithin(workspace, value) {
			return "", errors.New("execution: artifact destination escapes workspace")
		}
	} else {
		value = filepath.Join(workspace, filepath.Clean(value))
	}
	if !pathWithin(workspace, value) {
		return "", errors.New("execution: artifact destination escapes workspace")
	}
	if err := os.MkdirAll(value, 0o700); err != nil {
		return "", err
	}
	return value, nil
}

func cacheRefs(run store.Run, defaultBranch string) []string {
	refs := []string{cacheWriteRef(run)}
	defaultBranch = strings.TrimSpace(defaultBranch)
	if defaultBranch != "" {
		refs = append(refs, defaultBranch, "refs/heads/"+strings.TrimPrefix(defaultBranch, "refs/heads/"))
	}
	return sortedUnique(refs)
}

func cacheWriteRef(run store.Run) string {
	ref := strings.TrimSpace(pointerValue(run.Ref))
	if ref == "" {
		return "refs/heads/default"
	}
	return ref
}

func cachePolicyAllows(policy, operation string) bool {
	policy = strings.ToLower(strings.TrimSpace(policy))
	if policy == "" || policy == "pull-push" {
		return true
	}
	return strings.Contains(policy, operation)
}

func cacheWhenMatches(when string, succeeded bool) bool {
	when = strings.ToLower(strings.TrimSpace(when))
	return when == "" || when == "always" || (succeeded && (when == "on_success" || when == "success"))
}

func artifactWhenMatches(when string, succeeded bool) bool {
	switch strings.ToLower(strings.TrimSpace(when)) {
	case "", "on_success", "success":
		return succeeded
	case "always":
		return true
	case "on_failure", "failure":
		return !succeeded
	default:
		return succeeded
	}
}

func parseRetentionDays(value string) *time.Time {
	days, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || days < 1 {
		return nil
	}
	expires := time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour)
	return &expires
}

func parseExpireIn(value string) *time.Time {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(value)))
	if len(fields) == 0 {
		return nil
	}
	amount, err := strconv.Atoi(fields[0])
	if err != nil || amount < 1 {
		return nil
	}
	unit := "day"
	if len(fields) > 1 {
		unit = fields[1]
	}
	multiplier := 24 * time.Hour
	if strings.HasPrefix(unit, "hour") || strings.HasPrefix(unit, "hr") {
		multiplier = time.Hour
	} else if strings.HasPrefix(unit, "week") {
		multiplier = 7 * 24 * time.Hour
	} else if strings.HasPrefix(unit, "month") {
		multiplier = 30 * 24 * time.Hour
	}
	expires := time.Now().UTC().Add(time.Duration(amount) * multiplier)
	return &expires
}

func safeOutputName(value string) string {
	value = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r < 32 {
			return '-'
		}
		return r
	}, strings.TrimSpace(value))
	if value == "" {
		return "artifact"
	}
	return value
}

func jobLogStepID(steps []store.Step) string {
	if len(steps) > 0 {
		return steps[len(steps)-1].ID
	}
	return ""
}

type junitSummary struct {
	Name                             string
	Tests, Failures, Errors, Skipped int
	DurationSeconds                  float64
}

func parseJUnitReports(workspace string, patterns []string) ([]junitSummary, error) {
	files, err := collectArchiveFiles(workspace, patterns, nil)
	if errors.Is(err, errArchiveNoFiles) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]junitSummary, 0, len(files))
	for _, file := range files {
		input, err := os.Open(file.absolute)
		if err != nil {
			return nil, err
		}
		summary, err := decodeJUnit(io.LimitReader(input, 64<<20))
		_ = input.Close()
		if err != nil {
			return nil, fmt.Errorf("execution: parse JUnit %q: %w", file.relative, err)
		}
		summary.Name = file.relative
		result = append(result, summary)
	}
	return result, nil
}

func decodeJUnit(reader io.Reader) (junitSummary, error) {
	decoder := xml.NewDecoder(reader)
	var total junitSummary
	depth, suitesDepth := 0, -1
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return total, nil
		}
		if err != nil {
			return junitSummary{}, err
		}
		switch value := token.(type) {
		case xml.StartElement:
			depth++
			if value.Name.Local == "testsuites" && suitesDepth < 0 {
				suitesDepth = depth
			}
			if value.Name.Local != "testsuite" || (suitesDepth >= 0 && depth != suitesDepth+1) {
				continue
			}
			for _, attribute := range value.Attr {
				switch attribute.Name.Local {
				case "tests":
					total.Tests += parseJUnitInt(attribute.Value)
				case "failures":
					total.Failures += parseJUnitInt(attribute.Value)
				case "errors":
					total.Errors += parseJUnitInt(attribute.Value)
				case "skipped", "disabled":
					total.Skipped += parseJUnitInt(attribute.Value)
				case "time":
					duration, _ := strconv.ParseFloat(attribute.Value, 64)
					if duration > 0 {
						total.DurationSeconds += duration
					}
				}
			}
		case xml.EndElement:
			depth--
		}
	}
}

func parseJUnitInt(value string) int {
	number, _ := strconv.Atoi(value)
	if number < 0 {
		return 0
	}
	return number
}
