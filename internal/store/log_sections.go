package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type LogSectionProvider string

const (
	LogSectionGitHub LogSectionProvider = "github"
	LogSectionGitLab LogSectionProvider = "gitlab"

	MaxStepLogSections    = 100
	MaxLogSectionDepth    = 32
	MaxLogSectionNameSize = 1 << 10
)

type StepLogSection struct {
	ID            string             `json:"id"`
	StepID        string             `json:"stepId"`
	Provider      LogSectionProvider `json:"provider"`
	Name          string             `json:"name"`
	Depth         int                `json:"depth"`
	Collapsed     bool               `json:"collapsed"`
	StartSequence int64              `json:"startSequence"`
	EndSequence   *int64             `json:"endSequence,omitempty"`
	CreatedAt     time.Time          `json:"createdAt"`
	UpdatedAt     time.Time          `json:"updatedAt"`
}

type StartStepLogSectionParams struct {
	ID            string
	StepID        string
	Provider      LogSectionProvider
	Name          string
	Depth         int
	Collapsed     bool
	StartSequence int64
}

type FinishStepLogSectionParams struct {
	ID          string
	StepID      string
	EndSequence int64
}

const stepLogSectionColumns = `id, step_id, provider, name, depth, collapsed, start_sequence, end_sequence, created_at, updated_at`

func (s *Store) StartStepLogSection(ctx context.Context, params StartStepLogSectionParams) (StepLogSection, error) {
	if err := requireContext(ctx); err != nil {
		return StepLogSection{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return StepLogSection{}, err
	}
	params.ID, err = normalizeRequiredText("log section ID", params.ID)
	if err != nil {
		return StepLogSection{}, err
	}
	params.StepID, err = normalizeRequiredText("log section step ID", params.StepID)
	if err != nil {
		return StepLogSection{}, err
	}
	if params.Provider != LogSectionGitHub && params.Provider != LogSectionGitLab {
		return StepLogSection{}, invalidInput("log section provider", "must be github or gitlab")
	}
	if err := validateAnnotationText("log section name", params.Name, MaxLogSectionNameSize, true); err != nil {
		return StepLogSection{}, err
	}
	if params.Depth < 0 || params.Depth >= MaxLogSectionDepth {
		return StepLogSection{}, invalidInput("log section depth", "must be between 0 and 31")
	}
	if params.StartSequence < 1 {
		return StepLogSection{}, invalidInput("log section start sequence", "must be greater than zero")
	}
	var boundary int
	if err := db.QueryRowContext(ctx, `SELECT 1 FROM run_log_lines WHERE step_id = ? AND sequence = ?`, params.StepID, params.StartSequence).Scan(&boundary); errors.Is(err, sql.ErrNoRows) {
		return StepLogSection{}, &ErrNotFound{Resource: "step log boundary", Key: fmt.Sprintf("%s:%d", params.StepID, params.StartSequence)}
	} else if err != nil {
		return StepLogSection{}, fmt.Errorf("store: resolve log section boundary: %w", err)
	}
	now := nowUTC()
	_, err = db.ExecContext(ctx, `
		INSERT INTO step_log_sections (
			id, step_id, provider, name, depth, collapsed, start_sequence, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, params.ID, params.StepID, params.Provider, params.Name, params.Depth, boolToInteger(params.Collapsed), params.StartSequence, now.UnixMilli(), now.UnixMilli())
	if err != nil {
		if strings.Contains(err.Error(), "step log section limit reached") {
			return StepLogSection{}, invalidInput("step log section", "limit reached")
		}
		return StepLogSection{}, fmt.Errorf("store: start step log section: %w", err)
	}
	section, err := scanStepLogSection(db.QueryRowContext(ctx, `SELECT `+stepLogSectionColumns+` FROM step_log_sections WHERE id = ?`, params.ID))
	if err != nil {
		return StepLogSection{}, fmt.Errorf("store: reload step log section: %w", err)
	}
	return section, nil
}

func (s *Store) FinishStepLogSection(ctx context.Context, params FinishStepLogSectionParams) (StepLogSection, error) {
	if err := requireContext(ctx); err != nil {
		return StepLogSection{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return StepLogSection{}, err
	}
	params.ID, err = normalizeRequiredText("log section ID", params.ID)
	if err != nil {
		return StepLogSection{}, err
	}
	params.StepID, err = normalizeRequiredText("log section step ID", params.StepID)
	if err != nil {
		return StepLogSection{}, err
	}
	section, err := scanStepLogSection(db.QueryRowContext(ctx, `SELECT `+stepLogSectionColumns+` FROM step_log_sections WHERE id = ?`, params.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return StepLogSection{}, &ErrNotFound{Resource: "step log section", Key: params.ID}
	}
	if err != nil {
		return StepLogSection{}, fmt.Errorf("store: load step log section: %w", err)
	}
	if section.StepID != params.StepID {
		return StepLogSection{}, invalidInput("log section step ID", "does not own the section")
	}
	if section.EndSequence != nil {
		return StepLogSection{}, invalidInput("step log section", "has already ended")
	}
	if params.EndSequence < section.StartSequence {
		return StepLogSection{}, invalidInput("log section end sequence", "must not precede its start")
	}
	var boundary int
	if err := db.QueryRowContext(ctx, `SELECT 1 FROM run_log_lines WHERE step_id = ? AND sequence = ?`, params.StepID, params.EndSequence).Scan(&boundary); errors.Is(err, sql.ErrNoRows) {
		return StepLogSection{}, &ErrNotFound{Resource: "step log boundary", Key: fmt.Sprintf("%s:%d", params.StepID, params.EndSequence)}
	} else if err != nil {
		return StepLogSection{}, fmt.Errorf("store: resolve log section end boundary: %w", err)
	}
	now := nowUTC()
	result, err := db.ExecContext(ctx, `UPDATE step_log_sections SET end_sequence = ?, updated_at = ? WHERE id = ? AND end_sequence IS NULL`, params.EndSequence, now.UnixMilli(), params.ID)
	if err != nil {
		return StepLogSection{}, fmt.Errorf("store: finish step log section: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		return StepLogSection{}, fmt.Errorf("store: step log section changed concurrently")
	}
	section, err = scanStepLogSection(db.QueryRowContext(ctx, `SELECT `+stepLogSectionColumns+` FROM step_log_sections WHERE id = ?`, params.ID))
	if err != nil {
		return StepLogSection{}, fmt.Errorf("store: reload finished step log section: %w", err)
	}
	return section, nil
}

func (s *Store) ListStepLogSections(ctx context.Context, stepID string) ([]StepLogSection, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	stepID, err = normalizeRequiredText("log section step ID", stepID)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT `+stepLogSectionColumns+` FROM step_log_sections WHERE step_id = ? ORDER BY start_sequence, depth, id`, stepID)
	if err != nil {
		return nil, fmt.Errorf("store: list step log sections: %w", err)
	}
	defer rows.Close()
	items := make([]StepLogSection, 0)
	for rows.Next() {
		item, err := scanStepLogSection(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan step log section: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate step log sections: %w", err)
	}
	return items, nil
}

func scanStepLogSection(scanner annotationScanner) (StepLogSection, error) {
	var item StepLogSection
	var collapsed int
	var createdAt, updatedAt int64
	if err := scanner.Scan(&item.ID, &item.StepID, &item.Provider, &item.Name, &item.Depth, &collapsed,
		&item.StartSequence, &item.EndSequence, &createdAt, &updatedAt); err != nil {
		return StepLogSection{}, err
	}
	item.Collapsed = collapsed != 0
	item.CreatedAt = time.UnixMilli(createdAt).UTC()
	item.UpdatedAt = time.UnixMilli(updatedAt).UTC()
	return item, nil
}
