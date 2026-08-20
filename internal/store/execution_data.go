package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const artifactColumns = `
	id, project_id, run_id, job_id, step_id, name, format, storage_key,
	sha256, size_bytes, file_count, expires_at, created_at`

const cacheEntryColumns = `
	id, project_id, ref, cache_key, storage_key, sha256, size_bytes,
	file_count, created_at, accessed_at, expires_at`

const testReportColumns = `
	id, artifact_id, project_id, run_id, job_id, step_id, format, name,
	tests, failures, errors, skipped, duration_seconds, created_at`

type Artifact struct {
	ID         string     `json:"id"`
	ProjectID  string     `json:"projectId"`
	RunID      string     `json:"runId"`
	JobID      string     `json:"jobId"`
	StepID     *string    `json:"stepId,omitempty"`
	Name       string     `json:"name"`
	Format     string     `json:"format"`
	StorageKey string     `json:"-"`
	SHA256     string     `json:"sha256"`
	SizeBytes  int64      `json:"sizeBytes"`
	FileCount  int        `json:"fileCount"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
}

type CreateArtifactParams struct {
	ProjectID, RunID, JobID, StepID string
	Name, StorageKey, SHA256        string
	SizeBytes                       int64
	FileCount                       int
	ExpiresAt                       *time.Time
}

type CacheEntry struct {
	ID         string     `json:"id"`
	ProjectID  string     `json:"projectId"`
	Ref        string     `json:"ref"`
	Key        string     `json:"key"`
	StorageKey string     `json:"-"`
	SHA256     string     `json:"sha256"`
	SizeBytes  int64      `json:"sizeBytes"`
	FileCount  int        `json:"fileCount"`
	CreatedAt  time.Time  `json:"createdAt"`
	AccessedAt time.Time  `json:"accessedAt"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
}

type PutCacheEntryParams struct {
	ProjectID, Ref, Key, StorageKey, SHA256 string
	SizeBytes                               int64
	FileCount                               int
	ExpiresAt                               *time.Time
}

type TestReport struct {
	ID              string    `json:"id"`
	ArtifactID      *string   `json:"artifactId,omitempty"`
	ProjectID       string    `json:"projectId"`
	RunID           string    `json:"runId"`
	JobID           string    `json:"jobId"`
	StepID          *string   `json:"stepId,omitempty"`
	Format          string    `json:"format"`
	Name            string    `json:"name"`
	Tests           int       `json:"tests"`
	Failures        int       `json:"failures"`
	Errors          int       `json:"errors"`
	Skipped         int       `json:"skipped"`
	DurationSeconds float64   `json:"durationSeconds"`
	CreatedAt       time.Time `json:"createdAt"`
}

type CreateTestReportParams struct {
	ArtifactID, ProjectID, RunID, JobID, StepID string
	Name                                        string
	Tests, Failures, Errors, Skipped            int
	DurationSeconds                             float64
}

func (s *Store) CreateArtifact(ctx context.Context, params CreateArtifactParams) (Artifact, error) {
	if err := requireContext(ctx); err != nil {
		return Artifact{}, err
	}
	var err error
	for field, value := range map[string]*string{
		"artifact project ID": &params.ProjectID, "artifact run ID": &params.RunID,
		"artifact job ID": &params.JobID, "artifact name": &params.Name,
		"artifact storage key": &params.StorageKey,
	} {
		*value, err = normalizeRequiredText(field, *value)
		if err != nil {
			return Artifact{}, err
		}
	}
	if len(params.Name) > 255 || len(params.StorageKey) > 1024 {
		return Artifact{}, invalidInput("artifact", "name or storage key is too long")
	}
	if err := validateExecutionDigest(params.SHA256); err != nil {
		return Artifact{}, err
	}
	if params.SizeBytes < 0 || params.FileCount < 0 {
		return Artifact{}, invalidInput("artifact size", "must not be negative")
	}
	id, err := randomOpaqueID()
	if err != nil {
		return Artifact{}, fmt.Errorf("store: generate artifact ID: %w", err)
	}
	now := nowUTC()
	db, err := s.dbHandle()
	if err != nil {
		return Artifact{}, err
	}
	result, err := db.ExecContext(ctx, `
		INSERT OR IGNORE INTO artifacts (
			id, project_id, run_id, job_id, step_id, name, format, storage_key,
			sha256, size_bytes, file_count, expires_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, 'zip', ?, ?, ?, ?, ?, ?)
	`, id, params.ProjectID, params.RunID, params.JobID, executionDataNullableString(params.StepID),
		params.Name, params.StorageKey, strings.ToLower(params.SHA256), params.SizeBytes,
		params.FileCount, nullableTime(params.ExpiresAt), now.UnixMilli())
	if err != nil {
		return Artifact{}, fmt.Errorf("store: create artifact: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Artifact{}, fmt.Errorf("store: create artifact rows: %w", err)
	}
	if affected == 0 {
		return Artifact{}, &ErrConflict{Resource: "artifact", Field: "run/name", Value: params.RunID + "/" + params.Name}
	}
	return s.GetArtifact(ctx, id)
}

func (s *Store) ListRunArtifacts(ctx context.Context, runID string) ([]Artifact, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	runID, err := normalizeRequiredText("run ID", runID)
	if err != nil {
		return nil, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT `+artifactColumns+` FROM artifacts WHERE run_id = ? ORDER BY created_at ASC, id ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("store: list run artifacts: %w", err)
	}
	defer rows.Close()
	items := make([]Artifact, 0)
	for rows.Next() {
		item, scanErr := scanArtifact(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("store: scan run artifact: %w", scanErr)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetArtifact(ctx context.Context, artifactID string) (Artifact, error) {
	if err := requireContext(ctx); err != nil {
		return Artifact{}, err
	}
	artifactID, err := normalizeRequiredText("artifact ID", artifactID)
	if err != nil {
		return Artifact{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return Artifact{}, err
	}
	item, err := scanArtifact(db.QueryRowContext(ctx, `SELECT `+artifactColumns+` FROM artifacts WHERE id = ?`, artifactID))
	if errors.Is(err, sql.ErrNoRows) {
		return Artifact{}, &ErrNotFound{Resource: "artifact", Key: artifactID}
	}
	if err != nil {
		return Artifact{}, fmt.Errorf("store: get artifact: %w", err)
	}
	return item, nil
}

func (s *Store) GetRunArtifactByName(ctx context.Context, runID, name string) (Artifact, error) {
	if err := requireContext(ctx); err != nil {
		return Artifact{}, err
	}
	var err error
	runID, err = normalizeRequiredText("run ID", runID)
	if err != nil {
		return Artifact{}, err
	}
	name, err = normalizeRequiredText("artifact name", name)
	if err != nil {
		return Artifact{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return Artifact{}, err
	}
	item, err := scanArtifact(db.QueryRowContext(ctx, `SELECT `+artifactColumns+` FROM artifacts WHERE run_id = ? AND name = ?`, runID, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Artifact{}, &ErrNotFound{Resource: "artifact", Key: runID + "/" + name}
	}
	if err != nil {
		return Artifact{}, fmt.Errorf("store: get run artifact: %w", err)
	}
	return item, nil
}

func (s *Store) PutCacheEntry(ctx context.Context, params PutCacheEntryParams) (CacheEntry, error) {
	if err := requireContext(ctx); err != nil {
		return CacheEntry{}, err
	}
	var err error
	for field, value := range map[string]*string{
		"cache project ID": &params.ProjectID, "cache ref": &params.Ref,
		"cache key": &params.Key, "cache storage key": &params.StorageKey,
	} {
		*value, err = normalizeRequiredText(field, *value)
		if err != nil {
			return CacheEntry{}, err
		}
	}
	if len(params.Ref) > 1024 || len(params.Key) > 512 || len(params.StorageKey) > 1024 {
		return CacheEntry{}, invalidInput("cache entry", "ref, key, or storage key is too long")
	}
	if err := validateExecutionDigest(params.SHA256); err != nil {
		return CacheEntry{}, err
	}
	if params.SizeBytes < 0 || params.FileCount < 0 {
		return CacheEntry{}, invalidInput("cache size", "must not be negative")
	}
	id, err := randomOpaqueID()
	if err != nil {
		return CacheEntry{}, fmt.Errorf("store: generate cache ID: %w", err)
	}
	now := nowUTC()
	db, err := s.dbHandle()
	if err != nil {
		return CacheEntry{}, err
	}
	result, err := db.ExecContext(ctx, `
		INSERT OR IGNORE INTO cache_entries (
			id, project_id, ref, cache_key, storage_key, sha256, size_bytes,
			file_count, created_at, accessed_at, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, params.ProjectID, params.Ref, params.Key, params.StorageKey,
		strings.ToLower(params.SHA256), params.SizeBytes, params.FileCount,
		now.UnixMilli(), now.UnixMilli(), nullableTime(params.ExpiresAt))
	if err != nil {
		return CacheEntry{}, fmt.Errorf("store: put cache entry: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return CacheEntry{}, fmt.Errorf("store: put cache entry rows: %w", err)
	}
	if affected == 0 {
		return CacheEntry{}, &ErrConflict{Resource: "cache entry", Field: "project/ref/key", Value: params.ProjectID + "/" + params.Ref + "/" + params.Key}
	}
	return s.getCacheEntry(ctx, id)
}

func (s *Store) FindCacheEntry(ctx context.Context, projectID string, refs []string, key string, prefixes []string) (CacheEntry, bool, error) {
	if err := requireContext(ctx); err != nil {
		return CacheEntry{}, false, err
	}
	projectID, err := normalizeRequiredText("cache project ID", projectID)
	if err != nil {
		return CacheEntry{}, false, err
	}
	key, err = normalizeRequiredText("cache key", key)
	if err != nil {
		return CacheEntry{}, false, err
	}
	if len(key) > 512 {
		return CacheEntry{}, false, invalidInput("cache key", "must not exceed 512 characters")
	}
	db, err := s.dbHandle()
	if err != nil {
		return CacheEntry{}, false, err
	}
	normalizedRefs := executionDataUniqueStrings(refs)
	normalizedPrefixes := executionDataUniqueStrings(prefixes)
	for _, ref := range normalizedRefs {
		item, scanErr := scanCacheEntry(db.QueryRowContext(ctx, `
			SELECT `+cacheEntryColumns+` FROM cache_entries
			WHERE project_id = ? AND ref = ? AND cache_key = ?
			ORDER BY created_at DESC LIMIT 1
		`, projectID, ref, key))
		if scanErr == nil {
			return s.touchCacheEntry(ctx, item)
		}
		if !errors.Is(scanErr, sql.ErrNoRows) {
			return CacheEntry{}, false, fmt.Errorf("store: find exact cache entry: %w", scanErr)
		}
		for _, prefix := range normalizedPrefixes {
			item, scanErr = scanCacheEntry(db.QueryRowContext(ctx, `
				SELECT `+cacheEntryColumns+` FROM cache_entries
				WHERE project_id = ? AND ref = ? AND cache_key LIKE ? ESCAPE '\'
				ORDER BY created_at DESC, id DESC LIMIT 1
			`, projectID, ref, escapeLikePrefix(prefix)+"%"))
			if scanErr == nil {
				return s.touchCacheEntry(ctx, item)
			}
			if !errors.Is(scanErr, sql.ErrNoRows) {
				return CacheEntry{}, false, fmt.Errorf("store: find cache prefix: %w", scanErr)
			}
		}
	}
	return CacheEntry{}, false, nil
}

func (s *Store) ListProjectCaches(ctx context.Context, projectID string) ([]CacheEntry, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	projectID, err := normalizeRequiredText("cache project ID", projectID)
	if err != nil {
		return nil, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT `+cacheEntryColumns+` FROM cache_entries
		WHERE project_id = ? ORDER BY accessed_at DESC, created_at DESC LIMIT 200
	`, projectID)
	if err != nil {
		return nil, fmt.Errorf("store: list project caches: %w", err)
	}
	defer rows.Close()
	items := make([]CacheEntry, 0)
	for rows.Next() {
		item, scanErr := scanCacheEntry(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("store: scan project cache: %w", scanErr)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CreateTestReport(ctx context.Context, params CreateTestReportParams) (TestReport, error) {
	if err := requireContext(ctx); err != nil {
		return TestReport{}, err
	}
	var err error
	for field, value := range map[string]*string{
		"report project ID": &params.ProjectID, "report run ID": &params.RunID,
		"report job ID": &params.JobID, "report name": &params.Name,
	} {
		*value, err = normalizeRequiredText(field, *value)
		if err != nil {
			return TestReport{}, err
		}
	}
	if len(params.Name) > 255 || params.Tests < 0 || params.Failures < 0 ||
		params.Errors < 0 || params.Skipped < 0 || params.DurationSeconds < 0 {
		return TestReport{}, invalidInput("test report", "contains an invalid aggregate")
	}
	id, err := randomOpaqueID()
	if err != nil {
		return TestReport{}, fmt.Errorf("store: generate report ID: %w", err)
	}
	now := nowUTC()
	db, err := s.dbHandle()
	if err != nil {
		return TestReport{}, err
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO test_reports (
			id, artifact_id, project_id, run_id, job_id, step_id, format, name,
			tests, failures, errors, skipped, duration_seconds, created_at
		) VALUES (?, ?, ?, ?, ?, ?, 'junit', ?, ?, ?, ?, ?, ?, ?)
	`, id, executionDataNullableString(params.ArtifactID), params.ProjectID, params.RunID,
		params.JobID, executionDataNullableString(params.StepID), params.Name, params.Tests,
		params.Failures, params.Errors, params.Skipped, params.DurationSeconds, now.UnixMilli())
	if err != nil {
		return TestReport{}, fmt.Errorf("store: create test report: %w", err)
	}
	return s.getTestReport(ctx, id)
}

func (s *Store) ListRunTestReports(ctx context.Context, runID string) ([]TestReport, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	runID, err := normalizeRequiredText("run ID", runID)
	if err != nil {
		return nil, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT `+testReportColumns+` FROM test_reports WHERE run_id = ? ORDER BY created_at ASC, id ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("store: list run test reports: %w", err)
	}
	defer rows.Close()
	items := make([]TestReport, 0)
	for rows.Next() {
		item, scanErr := scanTestReport(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("store: scan run test report: %w", scanErr)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) getCacheEntry(ctx context.Context, id string) (CacheEntry, error) {
	db, err := s.dbHandle()
	if err != nil {
		return CacheEntry{}, err
	}
	item, err := scanCacheEntry(db.QueryRowContext(ctx, `SELECT `+cacheEntryColumns+` FROM cache_entries WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return CacheEntry{}, &ErrNotFound{Resource: "cache entry", Key: id}
	}
	if err != nil {
		return CacheEntry{}, fmt.Errorf("store: get cache entry: %w", err)
	}
	return item, nil
}

func (s *Store) touchCacheEntry(ctx context.Context, item CacheEntry) (CacheEntry, bool, error) {
	db, err := s.dbHandle()
	if err != nil {
		return CacheEntry{}, false, err
	}
	now := nowUTC()
	if _, err := db.ExecContext(ctx, `UPDATE cache_entries SET accessed_at = ? WHERE id = ?`, now.UnixMilli(), item.ID); err != nil {
		return CacheEntry{}, false, fmt.Errorf("store: touch cache entry: %w", err)
	}
	item.AccessedAt = now
	return item, true, nil
}

func (s *Store) getTestReport(ctx context.Context, id string) (TestReport, error) {
	db, err := s.dbHandle()
	if err != nil {
		return TestReport{}, err
	}
	item, err := scanTestReport(db.QueryRowContext(ctx, `SELECT `+testReportColumns+` FROM test_reports WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return TestReport{}, &ErrNotFound{Resource: "test report", Key: id}
	}
	if err != nil {
		return TestReport{}, fmt.Errorf("store: get test report: %w", err)
	}
	return item, nil
}

func scanArtifact(scanner interface{ Scan(...any) error }) (Artifact, error) {
	var item Artifact
	var stepID sql.NullString
	var expiresAt sql.NullInt64
	var createdAt int64
	err := scanner.Scan(&item.ID, &item.ProjectID, &item.RunID, &item.JobID, &stepID,
		&item.Name, &item.Format, &item.StorageKey, &item.SHA256, &item.SizeBytes,
		&item.FileCount, &expiresAt, &createdAt)
	item.StepID = nullStringPointer(stepID)
	item.ExpiresAt = nullTimePointer(expiresAt)
	item.CreatedAt = timeFromMillis(createdAt)
	return item, err
}

func scanCacheEntry(scanner interface{ Scan(...any) error }) (CacheEntry, error) {
	var item CacheEntry
	var createdAt, accessedAt int64
	var expiresAt sql.NullInt64
	err := scanner.Scan(&item.ID, &item.ProjectID, &item.Ref, &item.Key, &item.StorageKey,
		&item.SHA256, &item.SizeBytes, &item.FileCount, &createdAt, &accessedAt, &expiresAt)
	item.CreatedAt = timeFromMillis(createdAt)
	item.AccessedAt = timeFromMillis(accessedAt)
	item.ExpiresAt = nullTimePointer(expiresAt)
	return item, err
}

func scanTestReport(scanner interface{ Scan(...any) error }) (TestReport, error) {
	var item TestReport
	var artifactID, stepID sql.NullString
	var createdAt int64
	err := scanner.Scan(&item.ID, &artifactID, &item.ProjectID, &item.RunID, &item.JobID,
		&stepID, &item.Format, &item.Name, &item.Tests, &item.Failures, &item.Errors,
		&item.Skipped, &item.DurationSeconds, &createdAt)
	item.ArtifactID = nullStringPointer(artifactID)
	item.StepID = nullStringPointer(stepID)
	item.CreatedAt = timeFromMillis(createdAt)
	return item, err
}

func validateExecutionDigest(value string) error {
	value = strings.TrimSpace(value)
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return invalidInput("SHA-256 digest", "must be 64 hexadecimal characters")
	}
	return nil
}

func executionDataNullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func executionDataUniqueStrings(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
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
	return result
}

func escapeLikePrefix(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "%", `\%`)
	return strings.ReplaceAll(value, "_", `\_`)
}
