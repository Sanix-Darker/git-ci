package execution

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/sanix-darker/git-ci/internal/store"
	"github.com/sanix-darker/git-ci/pkg/types"
)

var processExitStatus = regexp.MustCompile(`(?i)(?:exit status|exited with status|exit code)\s+([0-9]+)`)

type jobAttemptOutcome struct {
	FailureKind string
	ExitCode    *int
	Message     string
}

func (outcome *jobAttemptOutcome) record(err error) {
	if outcome == nil || err == nil || outcome.FailureKind != "" {
		return
	}
	outcome.Message = err.Error()
	if errors.Is(err, context.DeadlineExceeded) {
		outcome.FailureKind = "job_execution_timeout"
		return
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		code := exitError.ExitCode()
		outcome.FailureKind = "script_failure"
		outcome.ExitCode = &code
		return
	}
	if match := processExitStatus.FindStringSubmatch(err.Error()); len(match) == 2 {
		if code, parseErr := strconv.Atoi(match[1]); parseErr == nil {
			outcome.FailureKind = "script_failure"
			outcome.ExitCode = &code
			return
		}
	}
	outcome.FailureKind = "runner_system_failure"
}

func retryPolicyMatches(policy *types.RetryPolicy, attemptNumber int, status store.Status, outcome jobAttemptOutcome, cancelled bool) bool {
	if policy == nil || policy.MaxAttempts <= 0 || attemptNumber > boundedRetryCount(policy.MaxAttempts) || status != store.StatusFailed || cancelled {
		return false
	}
	whenMatch := len(policy.When) == 0
	for _, selector := range policy.When {
		selector = strings.TrimSpace(selector)
		if selector == "always" || selector == outcome.FailureKind || timeoutRetryAlias(selector, outcome.FailureKind) {
			whenMatch = true
			break
		}
	}
	exitMatch := false
	if outcome.ExitCode != nil {
		for _, code := range policy.ExitCodes {
			if code == *outcome.ExitCode {
				exitMatch = true
				break
			}
		}
	}
	if len(policy.When) > 0 && len(policy.ExitCodes) > 0 {
		return whenMatch || exitMatch
	}
	if len(policy.ExitCodes) > 0 {
		return exitMatch
	}
	return whenMatch
}

func boundedRetryCount(value int) int {
	if value < 0 {
		return 0
	}
	if value > 2 {
		return 2
	}
	return value
}

func timeoutRetryAlias(selector, failureKind string) bool {
	return failureKind == "job_execution_timeout" && (selector == "stuck_or_timeout_failure" || selector == "job_execution_timeout")
}

func (m *Manager) executeJobWithRetry(ctx context.Context, run store.Run, item store.JobGraph, workspacePath string, secretValues map[string]string, outputContext *runtimeOutputContext) (store.Status, jobAttemptOutcome, error) {
	frozen, present, decodeErr := decodeJobSemantics(item.Job.Environment)
	if decodeErr != nil {
		return store.StatusFailed, jobAttemptOutcome{}, fmt.Errorf("decode retry policy: %w", decodeErr)
	}
	var policy *types.RetryPolicy
	if present {
		policy = frozen.Retry
	}
	for {
		attempt, err := m.store.StartJobAttempt(ctx, item.Job.ID)
		if err != nil {
			return store.StatusFailed, jobAttemptOutcome{}, fmt.Errorf("start job attempt: %w", err)
		}
		outcome := jobAttemptOutcome{}
		status, executeErr := m.executeJob(ctx, run, item, workspacePath, secretValues, outputContext, &outcome)
		if status == "" {
			status = store.StatusFailed
			_, _ = m.store.TransitionJob(context.WithoutCancel(ctx), item.Job.ID, store.StatusFailed)
		}
		if outcome.FailureKind == "" && status == store.StatusFailed {
			outcome.record(executeErr)
			if outcome.FailureKind == "" {
				outcome.FailureKind = "runner_system_failure"
				outcome.Message = "job failed without a process exit status"
			}
		}
		willRetry := retryPolicyMatches(policy, attempt.AttemptNumber, status, outcome, ctx.Err() != nil)
		finished, finishErr := m.store.FinishJobAttempt(ctx, store.FinishJobAttemptParams{AttemptID: attempt.ID, Status: status, FailureKind: outcome.FailureKind, ExitCode: outcome.ExitCode, Message: outcome.Message, WillRetry: willRetry})
		if finishErr != nil {
			return status, outcome, fmt.Errorf("finish job attempt: %w", finishErr)
		}
		if !willRetry {
			return status, outcome, executeErr
		}
		if len(item.Steps) > 0 {
			_ = m.appendSystem(context.WithoutCancel(ctx), item.Steps[0].ID, fmt.Sprintf("automatic retry scheduled after attempt %d (%s)", attempt.AttemptNumber, outcome.FailureKind))
		}
		if err := m.store.ResetJobForRetry(ctx, item.Job.ID, finished.ID); err != nil {
			return status, outcome, fmt.Errorf("reset job for retry: %w", err)
		}
	}
}

func jobFailureAllowed(job store.Job, status store.Status, outcome jobAttemptOutcome) bool {
	if job.AllowFailure {
		return true
	}
	if status != store.StatusFailed || outcome.ExitCode == nil {
		return false
	}
	semantics, present, err := decodeJobSemantics(job.Environment)
	if err != nil || !present {
		return false
	}
	for _, code := range semantics.AllowFailureExitCodes {
		if code == *outcome.ExitCode {
			return true
		}
	}
	return false
}

func persistedJobFailureAllowed(job store.Job) bool {
	if job.AllowFailure {
		return true
	}
	if job.Status != store.StatusFailed || len(job.Attempts) == 0 {
		return false
	}
	latest := job.Attempts[len(job.Attempts)-1]
	return jobFailureAllowed(job, job.Status, jobAttemptOutcome{FailureKind: latest.FailureKind, ExitCode: latest.ExitCode, Message: latest.Message})
}
