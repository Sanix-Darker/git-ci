package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type ChildPipelineStrategy string

const (
	ChildPipelineAsync  ChildPipelineStrategy = "async"
	ChildPipelineMirror ChildPipelineStrategy = "mirror"
	ChildPipelineDepend ChildPipelineStrategy = "depend"
)

type EnqueueChildPipelineLink struct {
	ParentRunID string
	ParentJobID string
	SourceFile  string
	Strategy    ChildPipelineStrategy
	Depth       int
}

type ChildPipelineLink struct {
	ParentRunID  string                `json:"parentRunId"`
	ParentJobID  string                `json:"parentJobId"`
	ChildRunID   string                `json:"childRunId"`
	SourceFile   string                `json:"sourceFile"`
	Strategy     ChildPipelineStrategy `json:"strategy"`
	Depth        int                   `json:"depth"`
	ParentStatus Status                `json:"parentStatus"`
	ChildStatus  Status                `json:"childStatus"`
	CreatedAt    time.Time             `json:"createdAt"`
}

const childPipelineLinkColumns = `
	link.parent_run_id,
	link.parent_job_id,
	link.child_run_id,
	link.source_file,
	link.strategy,
	link.depth,
	parent.status,
	child.status,
	link.created_at`

func normalizeEnqueueChildPipelineLink(link EnqueueChildPipelineLink) (EnqueueChildPipelineLink, error) {
	var err error
	if link.ParentRunID, err = normalizeRequiredText("child pipeline parent run ID", link.ParentRunID); err != nil {
		return link, err
	}
	if link.ParentJobID, err = normalizeRequiredText("child pipeline parent job ID", link.ParentJobID); err != nil {
		return link, err
	}
	if link.SourceFile, err = normalizeRequiredText("child pipeline source file", link.SourceFile); err != nil {
		return link, err
	}
	link.Strategy = ChildPipelineStrategy(strings.ToLower(strings.TrimSpace(string(link.Strategy))))
	if link.Strategy != ChildPipelineAsync && link.Strategy != ChildPipelineMirror && link.Strategy != ChildPipelineDepend {
		return link, invalidInput("child pipeline strategy", "must be async, mirror, or depend")
	}
	if link.Depth < 1 || link.Depth > 2 {
		return link, invalidInput("child pipeline depth", "must be between one and two")
	}
	return link, nil
}

func insertChildPipelineLink(ctx context.Context, tx *sql.Tx, child Run, link EnqueueChildPipelineLink, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO child_pipeline_links (parent_run_id, parent_job_id, child_run_id, source_file, strategy, depth, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, link.ParentRunID, link.ParentJobID, child.ID, link.SourceFile, link.Strategy, link.Depth, now.UnixMilli()); err != nil {
		return fmt.Errorf("store: insert child pipeline lineage: %w", err)
	}
	if link.Strategy == ChildPipelineMirror || link.Strategy == ChildPipelineDepend {
		jobResult, err := tx.ExecContext(ctx, `UPDATE jobs SET status = ?, started_at = COALESCE(started_at, ?), updated_at = ? WHERE id = ? AND run_id = ? AND status = ?`, StatusWaiting, now.UnixMilli(), now.UnixMilli(), link.ParentJobID, link.ParentRunID, StatusQueued)
		if err != nil {
			return fmt.Errorf("store: pause child pipeline bridge: %w", err)
		}
		runResult, err := tx.ExecContext(ctx, `UPDATE runs SET status = ?, updated_at = ? WHERE id = ? AND status = ?`, StatusWaiting, now.UnixMilli(), link.ParentRunID, StatusRunning)
		if err != nil {
			return fmt.Errorf("store: pause child pipeline parent: %w", err)
		}
		jobs, _ := jobResult.RowsAffected()
		runs, _ := runResult.RowsAffected()
		if jobs != 1 || runs != 1 {
			return &ErrConflict{Resource: "child pipeline", Field: "parentState", Value: link.ParentRunID}
		}
	}
	return nil
}

func (s *Store) GetChildPipelineForJob(ctx context.Context, parentJobID string) (ChildPipelineLink, error) {
	db, err := s.dbHandle()
	if err != nil {
		return ChildPipelineLink{}, err
	}
	parentJobID, err = normalizeRequiredText("child pipeline parent job ID", parentJobID)
	if err != nil {
		return ChildPipelineLink{}, err
	}
	link, err := scanChildPipelineLink(db.QueryRowContext(ctx, `SELECT `+childPipelineLinkColumns+` FROM child_pipeline_links AS link JOIN runs AS parent ON parent.id = link.parent_run_id JOIN runs AS child ON child.id = link.child_run_id WHERE link.parent_job_id = ?`, parentJobID))
	if errors.Is(err, sql.ErrNoRows) {
		return ChildPipelineLink{}, &ErrNotFound{Resource: "child pipeline", Key: parentJobID}
	}
	if err != nil {
		return ChildPipelineLink{}, fmt.Errorf("store: get child pipeline: %w", err)
	}
	return link, nil
}

func attachChildPipelineLinks(ctx context.Context, db *sql.DB, graph *RunGraph) error {
	rows, err := db.QueryContext(ctx, `SELECT `+childPipelineLinkColumns+` FROM child_pipeline_links AS link JOIN runs AS parent ON parent.id = link.parent_run_id JOIN runs AS child ON child.id = link.child_run_id WHERE link.parent_run_id = ? ORDER BY link.created_at ASC, link.child_run_id ASC`, graph.Run.ID)
	if err != nil {
		return fmt.Errorf("store: list child pipelines: %w", err)
	}
	for rows.Next() {
		link, err := scanChildPipelineLink(rows)
		if err != nil {
			rows.Close()
			return fmt.Errorf("store: scan child pipeline: %w", err)
		}
		graph.ChildPipelines = append(graph.ChildPipelines, link)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("store: close child pipelines: %w", err)
	}
	parent, err := scanChildPipelineLink(db.QueryRowContext(ctx, `SELECT `+childPipelineLinkColumns+` FROM child_pipeline_links AS link JOIN runs AS parent ON parent.id = link.parent_run_id JOIN runs AS child ON child.id = link.child_run_id WHERE link.child_run_id = ?`, graph.Run.ID))
	if err == nil {
		graph.ParentPipeline = &parent
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return fmt.Errorf("store: get parent pipeline: %w", err)
}

func scanChildPipelineLink(scanner interface{ Scan(...any) error }) (ChildPipelineLink, error) {
	var link ChildPipelineLink
	var created int64
	if err := scanner.Scan(&link.ParentRunID, &link.ParentJobID, &link.ChildRunID, &link.SourceFile, &link.Strategy, &link.Depth, &link.ParentStatus, &link.ChildStatus, &created); err != nil {
		return ChildPipelineLink{}, err
	}
	link.CreatedAt = timeFromMillis(created)
	return link, nil
}

// ReconcileCompletedChildPipelines settles durable mirror bridges. Calling it
// before every claim makes a crash after child completion recoverable.
func (s *Store) ReconcileCompletedChildPipelines(ctx context.Context) (int, error) {
	db, err := s.dbHandle()
	if err != nil {
		return 0, err
	}
	rows, err := db.QueryContext(ctx, `SELECT `+childPipelineLinkColumns+` FROM child_pipeline_links AS link JOIN runs AS parent ON parent.id = link.parent_run_id JOIN runs AS child ON child.id = link.child_run_id JOIN jobs AS bridge ON bridge.id = link.parent_job_id WHERE link.strategy IN (?, ?) AND parent.status = ? AND bridge.status = ? AND child.status IN (?, ?, ?, ?) ORDER BY link.created_at ASC`, ChildPipelineMirror, ChildPipelineDepend, StatusWaiting, StatusWaiting, StatusSucceeded, StatusFailed, StatusCancelled, StatusSkipped)
	if err != nil {
		return 0, fmt.Errorf("store: list completed child pipelines: %w", err)
	}
	links := make([]ChildPipelineLink, 0)
	for rows.Next() {
		link, err := scanChildPipelineLink(rows)
		if err != nil {
			rows.Close()
			return 0, fmt.Errorf("store: scan completed child pipeline: %w", err)
		}
		links = append(links, link)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("store: close completed child pipelines: %w", err)
	}
	settled := 0
	for _, link := range links {
		if link.ChildStatus == StatusCancelled {
			if _, err := s.RequestRunCancellation(ctx, link.ParentRunID); err != nil {
				return settled, fmt.Errorf("store: cancel mirrored parent: %w", err)
			}
			settled++
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return settled, fmt.Errorf("store: begin child pipeline settlement: %w", err)
		}
		now := nowUTC()
		jobResult, err := tx.ExecContext(ctx, `UPDATE jobs SET status = ?, finished_at = ?, updated_at = ? WHERE id = ? AND run_id = ? AND status = ?`, link.ChildStatus, now.UnixMilli(), now.UnixMilli(), link.ParentJobID, link.ParentRunID, StatusWaiting)
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE runs SET status = ?, worker_id = NULL, claimed_at = NULL, updated_at = ? WHERE id = ? AND status = ?`, StatusQueued, now.UnixMilli(), link.ParentRunID, StatusWaiting)
		}
		if err != nil {
			tx.Rollback()
			return settled, fmt.Errorf("store: settle child pipeline: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return settled, fmt.Errorf("store: commit child pipeline settlement: %w", err)
		}
		changed, _ := jobResult.RowsAffected()
		if changed == 1 {
			settled++
		}
	}
	return settled, nil
}

func (s *Store) cascadeChildPipelineCancellation(ctx context.Context, parentRunID string) error {
	db, err := s.dbHandle()
	if err != nil {
		return err
	}
	rows, err := db.QueryContext(ctx, `SELECT child_run_id FROM child_pipeline_links WHERE parent_run_id = ? ORDER BY created_at ASC`, parentRunID)
	if err != nil {
		return fmt.Errorf("store: list child pipelines for cancellation: %w", err)
	}
	children := make([]string, 0)
	for rows.Next() {
		var child string
		if err := rows.Scan(&child); err != nil {
			rows.Close()
			return fmt.Errorf("store: scan child cancellation: %w", err)
		}
		children = append(children, child)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("store: close child cancellation list: %w", err)
	}
	for _, child := range children {
		if _, err := s.RequestRunCancellation(ctx, child); err != nil {
			return fmt.Errorf("store: cancel child pipeline %s: %w", child, err)
		}
	}
	return nil
}
