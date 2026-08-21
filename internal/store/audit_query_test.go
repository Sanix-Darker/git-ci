package store

import (
	"context"
	"testing"
	"time"
)

func TestListAuditFiltersPaginatesAndBuildsHistogram(t *testing.T) {
	database, _ := newTestStore(t)
	ctx := context.Background()
	project, err := database.CreateProject(ctx, testProjectParams("audit-query"))
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	for _, event := range []AuditEvent{
		{ProjectID: project.ID, Action: "run.queued", Actor: "operator", ResourceType: "run", ResourceID: "run-1", Metadata: []byte(`{"ref":"main"}`)},
		{ProjectID: project.ID, Action: "deployment.created", Actor: "operator", ResourceType: "deployment", ResourceID: "deploy-1", Metadata: []byte(`{"environment":"production"}`)},
		{Action: "session.login", Actor: "admin", ResourceType: "session", Metadata: []byte(`{"method":"token"}`)},
	} {
		if _, err := database.RecordAudit(ctx, event); err != nil {
			t.Fatalf("record %s: %v", event.Action, err)
		}
	}
	now := time.Now().UTC().Add(time.Second)
	report, err := database.ListAudit(ctx, AuditFilter{ProjectID: project.ID, Actor: "operator", Search: "production", Since: now.Add(-time.Hour), Until: now, Limit: 1})
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if report.Total != 1 || report.Count != 1 || report.Items[0].Action != "deployment.created" {
		t.Fatalf("filtered report = %#v", report)
	}
	if len(report.Buckets) != 12 {
		t.Fatalf("bucket count = %d, want 12", len(report.Buckets))
	}
	bucketTotal := 0
	for _, bucket := range report.Buckets {
		bucketTotal += bucket.Count
	}
	if bucketTotal != report.Total {
		t.Fatalf("histogram total = %d, want %d", bucketTotal, report.Total)
	}
	if len(report.Facets.Actions) < 2 || len(report.Facets.Actors) != 1 || len(report.Facets.ResourceTypes) < 2 {
		t.Fatalf("facets = %#v", report.Facets)
	}

	page, err := database.ListAudit(ctx, AuditFilter{Since: now.Add(-time.Hour), Until: now, Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("paginated audit: %v", err)
	}
	if page.Total != 3 || page.Count != 1 {
		t.Fatalf("paginated counts = %d/%d, want 1/3", page.Count, page.Total)
	}
}

func TestListAuditRejectsInvalidBounds(t *testing.T) {
	database, _ := newTestStore(t)
	now := time.Now().UTC()
	for _, filter := range []AuditFilter{{Limit: 201}, {Offset: -1}, {Since: now.Add(time.Hour), Until: now}} {
		if _, err := database.ListAudit(context.Background(), filter); err == nil {
			t.Fatalf("invalid filter accepted: %#v", filter)
		}
	}
}
