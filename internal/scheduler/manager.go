package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sanix-darker/git-ci/internal/store"
)

type Enqueuer interface {
	EnqueueTriggered(context.Context, string, string, string, string) (store.Run, error)
}

type Manager struct {
	store    *store.Store
	enqueuer Enqueuer
	now      func() time.Time
	wake     chan struct{}
}

func NewManager(database *store.Store, enqueuer Enqueuer) (*Manager, error) {
	if database == nil || enqueuer == nil {
		return nil, errors.New("scheduler: store and enqueuer are required")
	}
	return &Manager{store: database, enqueuer: enqueuer, now: time.Now, wake: make(chan struct{}, 1)}, nil
}

func (m *Manager) Create(ctx context.Context, projectID, workflowID, cron, ref, timezone string, enabled bool) (store.WorkflowSchedule, error) {
	timezone = normalizedTimezone(timezone)
	expression, location, err := validateSchedule(cron, timezone)
	if err != nil {
		return store.WorkflowSchedule{}, err
	}
	var next *time.Time
	if enabled {
		value, err := expression.Next(m.now(), location)
		if err != nil {
			return store.WorkflowSchedule{}, err
		}
		next = &value
	}
	ref = strings.TrimSpace(ref)
	var refPointer *string
	if ref != "" {
		refPointer = &ref
	}
	item, err := m.store.CreateWorkflowSchedule(ctx, store.CreateWorkflowScheduleParams{
		ProjectID: projectID, WorkflowID: workflowID, Cron: strings.TrimSpace(cron), Ref: refPointer,
		Timezone: strings.TrimSpace(timezone), Enabled: enabled, NextRunAt: next,
	})
	if err == nil {
		m.Notify()
	}
	return item, err
}

func (m *Manager) Update(ctx context.Context, scheduleID, cron, ref, timezone string, enabled bool) (store.WorkflowSchedule, error) {
	current, err := m.store.GetWorkflowSchedule(ctx, scheduleID)
	if err != nil {
		return store.WorkflowSchedule{}, err
	}
	timezone = normalizedTimezone(timezone)
	expression, location, err := validateSchedule(cron, timezone)
	if err != nil {
		return store.WorkflowSchedule{}, err
	}
	var next *time.Time
	if enabled {
		value, err := expression.Next(m.now(), location)
		if err != nil {
			return store.WorkflowSchedule{}, err
		}
		next = &value
	}
	ref = strings.TrimSpace(ref)
	var refPointer *string
	if ref != "" {
		refPointer = &ref
	}
	item, err := m.store.UpdateWorkflowSchedule(ctx, scheduleID, store.UpdateWorkflowScheduleParams{
		Cron: strings.TrimSpace(cron), Ref: refPointer, Timezone: strings.TrimSpace(timezone),
		Enabled: enabled, NextRunAt: next, LastRunAt: current.LastRunAt,
	})
	if err == nil {
		m.Notify()
	}
	return item, err
}

func (m *Manager) Delete(ctx context.Context, scheduleID string) error {
	return m.store.DeleteWorkflowSchedule(ctx, scheduleID)
}

func (m *Manager) Notify() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Manager) Run(ctx context.Context) error {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		if err := m.ProcessDue(ctx, m.now()); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case <-m.wake:
		}
	}
}

func (m *Manager) ProcessDue(ctx context.Context, now time.Time) error {
	claims, err := m.store.ClaimDueWorkflowSchedules(ctx, now, 25)
	if err != nil {
		return fmt.Errorf("scheduler: claim due schedules: %w", err)
	}
	for _, claim := range claims {
		schedule := claim.Schedule
		ref := ""
		if schedule.Ref != nil {
			ref = *schedule.Ref
		}
		_, enqueueErr := m.enqueuer.EnqueueTriggered(ctx, schedule.WorkflowID, ref, "", "schedule")
		expression, location, parseErr := validateSchedule(schedule.Cron, schedule.Timezone)
		if parseErr != nil {
			return parseErr
		}
		next, nextErr := expression.Next(claim.DueAt, location)
		if nextErr != nil {
			return nextErr
		}
		lastRun := claim.DueAt
		if enqueueErr != nil {
			next = now.Add(time.Minute).UTC()
			lastRun = time.Time{}
		}
		params := store.UpdateWorkflowScheduleParams{
			Cron: schedule.Cron, Ref: schedule.Ref, Timezone: schedule.Timezone,
			Enabled: schedule.Enabled, NextRunAt: &next, LastRunAt: &lastRun,
		}
		if lastRun.IsZero() {
			params.LastRunAt = schedule.LastRunAt
		}
		if _, err := m.store.UpdateWorkflowSchedule(ctx, schedule.ID, params); err != nil {
			return fmt.Errorf("scheduler: advance schedule: %w", err)
		}
	}
	return nil
}

func validateSchedule(cron, timezone string) (Expression, *time.Location, error) {
	expression, err := Parse(strings.TrimSpace(cron))
	if err != nil {
		return Expression{}, nil, err
	}
	timezone = strings.TrimSpace(timezone)
	if timezone == "" {
		timezone = "UTC"
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return Expression{}, nil, fmt.Errorf("scheduler: invalid timezone %q", timezone)
	}
	return expression, location, nil
}

func normalizedTimezone(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "UTC"
	}
	return value
}
