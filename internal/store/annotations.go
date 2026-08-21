package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

type AnnotationLevel string

const (
	AnnotationNotice  AnnotationLevel = "notice"
	AnnotationWarning AnnotationLevel = "warning"
	AnnotationError   AnnotationLevel = "error"

	MaxStepAnnotations       = 50
	MaxAnnotationMessageSize = 4 << 10
	MaxAnnotationFieldSize   = 1 << 10
)

type StepAnnotation struct {
	ID          string          `json:"id"`
	StepID      string          `json:"stepId"`
	Level       AnnotationLevel `json:"level"`
	Message     string          `json:"message"`
	Title       string          `json:"title,omitempty"`
	File        string          `json:"file,omitempty"`
	StartLine   *int            `json:"line,omitempty"`
	EndLine     *int            `json:"endLine,omitempty"`
	StartColumn *int            `json:"column,omitempty"`
	EndColumn   *int            `json:"endColumn,omitempty"`
	CreatedAt   time.Time       `json:"createdAt"`
}

type AppendStepAnnotationParams struct {
	StepID      string
	Level       AnnotationLevel
	Message     string
	Title       string
	File        string
	StartLine   *int
	EndLine     *int
	StartColumn *int
	EndColumn   *int
}

const stepAnnotationColumns = `id, step_id, level, message, title, file, start_line, end_line, start_column, end_column, created_at`
const selectedStepAnnotationColumns = `annotation.id, annotation.step_id, annotation.level, annotation.message, annotation.title, annotation.file, annotation.start_line, annotation.end_line, annotation.start_column, annotation.end_column, annotation.created_at`

func (s *Store) AppendStepAnnotation(ctx context.Context, params AppendStepAnnotationParams) (StepAnnotation, error) {
	if err := requireContext(ctx); err != nil {
		return StepAnnotation{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return StepAnnotation{}, err
	}
	params.StepID, err = normalizeRequiredText("annotation step ID", params.StepID)
	if err != nil {
		return StepAnnotation{}, err
	}
	if params.Level != AnnotationNotice && params.Level != AnnotationWarning && params.Level != AnnotationError {
		return StepAnnotation{}, invalidInput("annotation level", "must be notice, warning, or error")
	}
	if err := validateAnnotationText("annotation message", params.Message, MaxAnnotationMessageSize, true); err != nil {
		return StepAnnotation{}, err
	}
	if err := validateAnnotationText("annotation title", params.Title, MaxAnnotationFieldSize, false); err != nil {
		return StepAnnotation{}, err
	}
	if err := validateAnnotationText("annotation file", params.File, MaxAnnotationFieldSize, false); err != nil {
		return StepAnnotation{}, err
	}
	for label, value := range map[string]*int{
		"annotation line": params.StartLine, "annotation end line": params.EndLine,
		"annotation column": params.StartColumn, "annotation end column": params.EndColumn,
	} {
		if value != nil && *value < 1 {
			return StepAnnotation{}, invalidInput(label, "must be greater than zero")
		}
	}

	transaction, err := db.BeginTx(ctx, nil)
	if err != nil {
		return StepAnnotation{}, fmt.Errorf("store: begin step annotation: %w", err)
	}
	defer transaction.Rollback()
	var exists int
	if err := transaction.QueryRowContext(ctx, `SELECT 1 FROM steps WHERE id = ?`, params.StepID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return StepAnnotation{}, &ErrNotFound{Resource: "step", Key: params.StepID}
	} else if err != nil {
		return StepAnnotation{}, fmt.Errorf("store: resolve annotation step: %w", err)
	}
	id, err := randomOpaqueID()
	if err != nil {
		return StepAnnotation{}, fmt.Errorf("store: generate step annotation ID: %w", err)
	}
	now := nowUTC()
	_, err = transaction.ExecContext(ctx, `
		INSERT INTO step_annotations (
			id, step_id, level, message, title, file, start_line, end_line,
			start_column, end_column, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, params.StepID, params.Level, params.Message, params.Title, params.File,
		params.StartLine, params.EndLine, params.StartColumn, params.EndColumn, now.UnixMilli())
	if err != nil {
		if strings.Contains(err.Error(), "step annotation limit reached") {
			return StepAnnotation{}, invalidInput("step annotation", "limit reached")
		}
		return StepAnnotation{}, fmt.Errorf("store: append step annotation: %w", err)
	}
	annotation, err := scanStepAnnotation(transaction.QueryRowContext(ctx, `SELECT `+stepAnnotationColumns+` FROM step_annotations WHERE id = ?`, id))
	if err != nil {
		return StepAnnotation{}, fmt.Errorf("store: reload step annotation: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return StepAnnotation{}, fmt.Errorf("store: commit step annotation: %w", err)
	}
	return annotation, nil
}

func (s *Store) ListStepAnnotations(ctx context.Context, stepID string) ([]StepAnnotation, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	stepID, err = normalizeRequiredText("annotation step ID", stepID)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT `+stepAnnotationColumns+` FROM step_annotations WHERE step_id = ? ORDER BY created_at, id`, stepID)
	if err != nil {
		return nil, fmt.Errorf("store: list step annotations: %w", err)
	}
	defer rows.Close()
	return collectStepAnnotations(rows)
}

func listRunStepAnnotations(ctx context.Context, db *sql.DB, runID string) (map[string][]StepAnnotation, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT `+selectedStepAnnotationColumns+`
		FROM step_annotations AS annotation
		JOIN steps AS step ON step.id = annotation.step_id
		JOIN jobs AS job ON job.id = step.job_id
		WHERE job.run_id = ?
		ORDER BY annotation.created_at, annotation.id
	`, runID)
	if err != nil {
		return nil, fmt.Errorf("store: list run step annotations: %w", err)
	}
	defer rows.Close()
	items, err := collectStepAnnotations(rows)
	if err != nil {
		return nil, err
	}
	grouped := make(map[string][]StepAnnotation)
	for _, item := range items {
		grouped[item.StepID] = append(grouped[item.StepID], item)
	}
	return grouped, nil
}

type annotationScanner interface {
	Scan(dest ...any) error
}

func scanStepAnnotation(scanner annotationScanner) (StepAnnotation, error) {
	var item StepAnnotation
	var createdAt int64
	if err := scanner.Scan(&item.ID, &item.StepID, &item.Level, &item.Message, &item.Title, &item.File,
		&item.StartLine, &item.EndLine, &item.StartColumn, &item.EndColumn, &createdAt); err != nil {
		return StepAnnotation{}, err
	}
	item.CreatedAt = time.UnixMilli(createdAt).UTC()
	return item, nil
}

func collectStepAnnotations(rows *sql.Rows) ([]StepAnnotation, error) {
	items := make([]StepAnnotation, 0)
	for rows.Next() {
		item, err := scanStepAnnotation(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan step annotation: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate step annotations: %w", err)
	}
	return items, nil
}

func validateAnnotationText(label, value string, limit int, required bool) error {
	if required && strings.TrimSpace(value) == "" {
		return invalidInput(label, "must not be empty")
	}
	if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return invalidInput(label, "must be valid UTF-8 without null bytes")
	}
	if len(value) > limit {
		return invalidInput(label, fmt.Sprintf("must be at most %d bytes", limit))
	}
	return nil
}
