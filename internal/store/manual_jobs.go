package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxManualPlayVariables     = 32
	MaxManualPlayValueBytes    = 4096
	MaxManualPlayVariableBytes = 16384
)

var manualVariableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

type ManualJobState struct {
	JobID        string    `json:"jobId"`
	RunID        string    `json:"runId"`
	Blocking     bool      `json:"blocking"`
	Confirmation string    `json:"confirmation,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
}

type ManualJobPlay struct {
	ID             string            `json:"id"`
	JobID          string            `json:"jobId"`
	RunID          string            `json:"runId"`
	Actor          string            `json:"actor"`
	IdempotencyKey string            `json:"idempotencyKey"`
	Variables      map[string]string `json:"variables"`
	Confirmed      bool              `json:"confirmed"`
	CreatedAt      time.Time         `json:"createdAt"`
}

type PauseManualJobParams struct {
	JobID        string
	Blocking     bool
	Confirmation string
}

type PlayManualJobParams struct {
	RunID, JobID, Actor, IdempotencyKey string
	Variables                           map[string]string
	Confirmed                           bool
}

type ManualJobPlayResult struct {
	Run        Run           `json:"run"`
	Job        Job           `json:"job"`
	Play       ManualJobPlay `json:"play"`
	Idempotent bool          `json:"idempotent"`
}

type ErrManualJobPlay struct {
	Code, Message string
}

func (e *ErrManualJobPlay) Error() string {
	if e == nil || e.Message == "" {
		return "manual job cannot be played"
	}
	return e.Message
}

func (s *Store) PauseManualJob(ctx context.Context, params PauseManualJobParams) (ManualJobState, error) {
	if err := requireContext(ctx); err != nil {
		return ManualJobState{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return ManualJobState{}, err
	}
	params.JobID, err = normalizeRequiredText("manual job ID", params.JobID)
	if err != nil {
		return ManualJobState{}, err
	}
	params.Confirmation, err = normalizeOptionalText("manual confirmation", params.Confirmation)
	if err != nil {
		return ManualJobState{}, err
	}
	if utf8.RuneCountInString(params.Confirmation) > 240 {
		return ManualJobState{}, invalidInput("manual confirmation", "must contain at most 240 characters")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return ManualJobState{}, fmt.Errorf("store: begin manual job pause: %w", err)
	}
	defer tx.Rollback()
	job, err := scanJob(tx.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = ?`, params.JobID))
	if errors.Is(err, sql.ErrNoRows) {
		return ManualJobState{}, &ErrNotFound{Resource: "job", Key: params.JobID}
	}
	if err != nil {
		return ManualJobState{}, fmt.Errorf("store: load manual job: %w", err)
	}
	if job.Status == StatusManual {
		state, stateErr := scanManualJobState(tx.QueryRowContext(ctx, `SELECT job_id, run_id, blocking, confirmation, created_at FROM manual_job_states WHERE job_id = ?`, job.ID))
		return state, stateErr
	}
	if job.Status != StatusQueued {
		return ManualJobState{}, &ErrManualJobPlay{Code: "manual_job_not_ready", Message: "job is not queued for a manual pause"}
	}
	run, err := scanRun(tx.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE id = ?`, job.RunID))
	if err != nil {
		return ManualJobState{}, fmt.Errorf("store: load manual job run: %w", err)
	}
	if run.Status != StatusRunning {
		return ManualJobState{}, &ErrManualJobPlay{Code: "manual_job_not_ready", Message: "run is not active for a manual pause"}
	}
	now := nowUTC()
	state := ManualJobState{JobID: job.ID, RunID: job.RunID, Blocking: params.Blocking, Confirmation: params.Confirmation, CreatedAt: now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO manual_job_states (job_id, run_id, blocking, confirmation, created_at) VALUES (?, ?, ?, ?, ?)`, state.JobID, state.RunID, state.Blocking, nullableText(state.Confirmation), now.UnixMilli()); err != nil {
		return ManualJobState{}, fmt.Errorf("store: insert manual job state: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET status = ?, updated_at = ? WHERE id = ? AND status = ?`, StatusManual, now.UnixMilli(), job.ID, StatusQueued)
	if err != nil {
		return ManualJobState{}, fmt.Errorf("store: pause manual job: %w", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ManualJobState{}, &ErrConflict{Resource: "job", Field: "status", Value: job.ID}
	}
	if params.Blocking {
		result, err = tx.ExecContext(ctx, `UPDATE runs SET status = ?, updated_at = ? WHERE id = ? AND status = ?`, StatusWaiting, now.UnixMilli(), run.ID, StatusRunning)
		if err != nil {
			return ManualJobState{}, fmt.Errorf("store: wait for manual job: %w", err)
		}
		if count, _ := result.RowsAffected(); count != 1 {
			return ManualJobState{}, &ErrConflict{Resource: "run", Field: "status", Value: run.ID}
		}
	}
	if err := tx.Commit(); err != nil {
		return ManualJobState{}, fmt.Errorf("store: commit manual job pause: %w", err)
	}
	return state, nil
}

func (s *Store) PlayManualJob(ctx context.Context, params PlayManualJobParams) (ManualJobPlayResult, error) {
	if err := requireContext(ctx); err != nil {
		return ManualJobPlayResult{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return ManualJobPlayResult{}, err
	}
	params, err = normalizeManualJobPlay(params)
	if err != nil {
		return ManualJobPlayResult{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return ManualJobPlayResult{}, fmt.Errorf("store: begin manual job play: %w", err)
	}
	defer tx.Rollback()
	if existing, existingErr := scanManualJobPlay(tx.QueryRowContext(ctx, `SELECT id, job_id, run_id, actor, idempotency_key, variables_json, confirmed, created_at FROM manual_job_plays WHERE job_id = ? AND idempotency_key = ?`, params.JobID, params.IdempotencyKey)); existingErr == nil {
		if existing.RunID != params.RunID {
			return ManualJobPlayResult{}, &ErrManualJobPlay{Code: "manual_play_ownership_mismatch", Message: "job does not belong to the requested run"}
		}
		run, runErr := scanRun(tx.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE id = ?`, existing.RunID))
		job, jobErr := scanJob(tx.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = ?`, existing.JobID))
		if runErr != nil || jobErr != nil {
			return ManualJobPlayResult{}, fmt.Errorf("store: reload idempotent manual play")
		}
		return ManualJobPlayResult{Run: run, Job: job, Play: existing, Idempotent: true}, nil
	} else if !errors.Is(existingErr, sql.ErrNoRows) {
		return ManualJobPlayResult{}, fmt.Errorf("store: lookup manual play: %w", existingErr)
	}
	job, err := scanJob(tx.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = ?`, params.JobID))
	if errors.Is(err, sql.ErrNoRows) {
		return ManualJobPlayResult{}, &ErrNotFound{Resource: "job", Key: params.JobID}
	}
	if err != nil {
		return ManualJobPlayResult{}, fmt.Errorf("store: load playable job: %w", err)
	}
	if job.RunID != params.RunID {
		return ManualJobPlayResult{}, &ErrManualJobPlay{Code: "manual_play_ownership_mismatch", Message: "job does not belong to the requested run"}
	}
	state, err := scanManualJobState(tx.QueryRowContext(ctx, `SELECT job_id, run_id, blocking, confirmation, created_at FROM manual_job_states WHERE job_id = ?`, job.ID))
	if errors.Is(err, sql.ErrNoRows) || job.Status != StatusManual {
		return ManualJobPlayResult{}, &ErrManualJobPlay{Code: "manual_job_not_ready", Message: "job is not waiting for a manual play"}
	}
	if err != nil {
		return ManualJobPlayResult{}, fmt.Errorf("store: load manual state: %w", err)
	}
	if state.Confirmation != "" && !params.Confirmed {
		return ManualJobPlayResult{}, &ErrManualJobPlay{Code: "manual_confirmation_required", Message: "manual job confirmation is required"}
	}
	run, err := scanRun(tx.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE id = ?`, params.RunID))
	if err != nil {
		return ManualJobPlayResult{}, fmt.Errorf("store: load playable run: %w", err)
	}
	wantStatus := StatusSucceeded
	if state.Blocking {
		wantStatus = StatusWaiting
	}
	if run.Status != wantStatus || run.CancellationRequested {
		message := "optional manual jobs become playable after the initial pipeline pass"
		if state.Blocking {
			message = "blocking manual job run is not waiting"
		}
		return ManualJobPlayResult{}, &ErrManualJobPlay{Code: "manual_run_not_playable", Message: message}
	}
	variablesJSON, _ := json.Marshal(params.Variables)
	playID, err := randomOpaqueID()
	if err != nil {
		return ManualJobPlayResult{}, fmt.Errorf("store: generate manual play ID: %w", err)
	}
	now := nowUTC()
	play := ManualJobPlay{ID: playID, JobID: job.ID, RunID: run.ID, Actor: params.Actor, IdempotencyKey: params.IdempotencyKey, Variables: params.Variables, Confirmed: params.Confirmed, CreatedAt: now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO manual_job_plays (id, job_id, run_id, actor, idempotency_key, variables_json, confirmed, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, play.ID, play.JobID, play.RunID, play.Actor, play.IdempotencyKey, string(variablesJSON), play.Confirmed, now.UnixMilli()); err != nil {
		return ManualJobPlayResult{}, fmt.Errorf("store: insert manual play: %w", err)
	}
	updatedJob, err := scanJob(tx.QueryRowContext(ctx, `UPDATE jobs SET status = ?, started_at = NULL, finished_at = NULL, updated_at = ? WHERE id = ? AND status = ? RETURNING `+jobColumns, StatusQueued, now.UnixMilli(), job.ID, StatusManual))
	if errors.Is(err, sql.ErrNoRows) {
		return ManualJobPlayResult{}, &ErrConflict{Resource: "job", Field: "status", Value: job.ID}
	}
	if err != nil {
		return ManualJobPlayResult{}, fmt.Errorf("store: queue manual job: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE steps SET status = ?, started_at = NULL, finished_at = NULL, updated_at = ? WHERE job_id = ? AND status IN (?, ?)`, StatusQueued, now.UnixMilli(), job.ID, StatusQueued, StatusSkipped); err != nil {
		return ManualJobPlayResult{}, fmt.Errorf("store: reset manual job steps: %w", err)
	}
	updatedRun, err := scanRun(tx.QueryRowContext(ctx, `UPDATE runs SET status = ?, worker_id = NULL, claimed_at = NULL, failure_reason = NULL, finished_at = NULL, updated_at = ? WHERE id = ? AND status = ? RETURNING `+runColumns, StatusQueued, now.UnixMilli(), run.ID, wantStatus))
	if errors.Is(err, sql.ErrNoRows) {
		return ManualJobPlayResult{}, &ErrConflict{Resource: "run", Field: "status", Value: run.ID}
	}
	if err != nil {
		return ManualJobPlayResult{}, fmt.Errorf("store: queue manual run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ManualJobPlayResult{}, fmt.Errorf("store: commit manual play: %w", err)
	}
	return ManualJobPlayResult{Run: updatedRun, Job: updatedJob, Play: play}, nil
}

func normalizeManualJobPlay(params PlayManualJobParams) (PlayManualJobParams, error) {
	var err error
	if params.RunID, err = normalizeRequiredText("manual play run ID", params.RunID); err != nil {
		return PlayManualJobParams{}, err
	}
	if params.JobID, err = normalizeRequiredText("manual play job ID", params.JobID); err != nil {
		return PlayManualJobParams{}, err
	}
	if params.Actor, err = normalizeRequiredText("manual play actor", params.Actor); err != nil {
		return PlayManualJobParams{}, err
	}
	if params.IdempotencyKey, err = normalizeRequiredText("manual play idempotency key", params.IdempotencyKey); err != nil {
		return PlayManualJobParams{}, err
	}
	if len(params.IdempotencyKey) > 200 {
		return PlayManualJobParams{}, &ErrManualJobPlay{Code: "manual_play_invalid", Message: "idempotency key must contain at most 200 bytes"}
	}
	if len(params.Variables) > MaxManualPlayVariables {
		return PlayManualJobParams{}, &ErrManualJobPlay{Code: "manual_play_variables_invalid", Message: "manual play accepts at most 32 variables"}
	}
	keys := make([]string, 0, len(params.Variables))
	for key := range params.Variables {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	clean := make(map[string]string, len(keys))
	total := 0
	for _, key := range keys {
		value := params.Variables[key]
		if !manualVariableName.MatchString(key) {
			return PlayManualJobParams{}, &ErrManualJobPlay{Code: "manual_play_variables_invalid", Message: "manual variable names must match [A-Za-z_][A-Za-z0-9_]*"}
		}
		if !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 || len(value) > MaxManualPlayValueBytes {
			return PlayManualJobParams{}, &ErrManualJobPlay{Code: "manual_play_variables_invalid", Message: "manual variable values must be UTF-8 and contain at most 4096 bytes"}
		}
		total += len(key) + len(value)
		if total > MaxManualPlayVariableBytes {
			return PlayManualJobParams{}, &ErrManualJobPlay{Code: "manual_play_variables_invalid", Message: "manual variables exceed the 16384 byte limit"}
		}
		clean[key] = value
	}
	params.Variables = clean
	return params, nil
}

func attachManualJobs(ctx context.Context, db *sql.DB, graph *RunGraph) error {
	index := make(map[string]int, len(graph.Jobs))
	for i := range graph.Jobs {
		index[graph.Jobs[i].Job.ID] = i
	}
	rows, err := db.QueryContext(ctx, `SELECT job_id, run_id, blocking, confirmation, created_at FROM manual_job_states WHERE run_id = ?`, graph.Run.ID)
	if err != nil {
		return fmt.Errorf("store: list manual job states: %w", err)
	}
	for rows.Next() {
		state, scanErr := scanManualJobState(rows)
		if scanErr != nil {
			rows.Close()
			return fmt.Errorf("store: scan manual job state: %w", scanErr)
		}
		if i, ok := index[state.JobID]; ok {
			graph.Jobs[i].Job.ManualState = &state
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = db.QueryContext(ctx, `SELECT id, job_id, run_id, actor, idempotency_key, variables_json, confirmed, created_at FROM manual_job_plays WHERE run_id = ? ORDER BY created_at DESC, id DESC`, graph.Run.ID)
	if err != nil {
		return fmt.Errorf("store: list manual job plays: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		play, scanErr := scanManualJobPlay(rows)
		if scanErr != nil {
			return fmt.Errorf("store: scan manual job play: %w", scanErr)
		}
		if i, ok := index[play.JobID]; ok && graph.Jobs[i].Job.ManualPlay == nil {
			graph.Jobs[i].Job.ManualPlay = &play
		}
	}
	return rows.Err()
}

func scanManualJobState(scanner executionScanner) (ManualJobState, error) {
	var state ManualJobState
	var blocking int64
	var confirmation sql.NullString
	var createdAt int64
	if err := scanner.Scan(&state.JobID, &state.RunID, &blocking, &confirmation, &createdAt); err != nil {
		return ManualJobState{}, err
	}
	state.Blocking = blocking != 0
	state.Confirmation = confirmation.String
	state.CreatedAt = timeFromMillis(createdAt)
	return state, nil
}

func scanManualJobPlay(scanner executionScanner) (ManualJobPlay, error) {
	var play ManualJobPlay
	var variables string
	var confirmed int64
	var createdAt int64
	if err := scanner.Scan(&play.ID, &play.JobID, &play.RunID, &play.Actor, &play.IdempotencyKey, &variables, &confirmed, &createdAt); err != nil {
		return ManualJobPlay{}, err
	}
	if err := json.Unmarshal([]byte(variables), &play.Variables); err != nil {
		return ManualJobPlay{}, err
	}
	play.Confirmed = confirmed != 0
	play.CreatedAt = timeFromMillis(createdAt)
	return play, nil
}
