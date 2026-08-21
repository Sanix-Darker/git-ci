package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sanix-darker/git-ci/internal/store"
	"github.com/sanix-darker/git-ci/internal/webui"
)

type auditResponse struct {
	Items   []store.AuditEvent  `json:"items"`
	Count   int                 `json:"count"`
	Total   int                 `json:"total"`
	Range   string              `json:"range"`
	Filter  store.AuditFilter   `json:"filter"`
	Facets  store.AuditFacets   `json:"facets"`
	Buckets []store.AuditBucket `json:"buckets"`
}

func (a *API) handleAudit(writer http.ResponseWriter, request *http.Request) {
	filter, window, err := auditFilterFromRequest(request, 50, time.Now().UTC())
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_audit_filter", err.Error())
		return
	}
	report, err := a.store.ListAudit(request.Context(), filter)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "audit_query_failed", "failed to query audit events")
		return
	}
	writeJSON(writer, http.StatusOK, auditResponse{Items: report.Items, Count: report.Count, Total: report.Total, Range: window, Filter: report.Filter, Facets: report.Facets, Buckets: report.Buckets})
}

func auditFilterFromRequest(request *http.Request, defaultLimit int, now time.Time) (store.AuditFilter, string, error) {
	query := request.URL.Query()
	window := strings.ToLower(strings.TrimSpace(query.Get("range")))
	if window == "" {
		window = "24h"
	}
	filter := store.AuditFilter{ProjectID: strings.TrimSpace(query.Get("project")), Actor: strings.TrimSpace(query.Get("actor")), Action: strings.TrimSpace(query.Get("action")), ResourceType: strings.TrimSpace(query.Get("resource")), Search: strings.TrimSpace(query.Get("q")), Until: now.UTC(), Limit: defaultLimit}
	switch window {
	case "1h":
		filter.Since = filter.Until.Add(-time.Hour)
	case "24h":
		filter.Since = filter.Until.Add(-24 * time.Hour)
	case "7d":
		filter.Since = filter.Until.Add(-7 * 24 * time.Hour)
	case "30d":
		filter.Since = filter.Until.Add(-30 * 24 * time.Hour)
	case "all":
	default:
		return store.AuditFilter{}, "", fmt.Errorf("range must be one of 1h, 24h, 7d, 30d, or all")
	}
	for name, target := range map[string]*int{"limit": &filter.Limit, "offset": &filter.Offset} {
		if raw := strings.TrimSpace(query.Get(name)); raw != "" {
			value, parseErr := strconv.Atoi(raw)
			if parseErr != nil {
				return store.AuditFilter{}, "", fmt.Errorf("%s must be an integer", name)
			}
			*target = value
		}
	}
	return filter, window, nil
}

func auditView(report store.AuditReport, window string) webui.AuditView {
	view := webui.AuditView{Total: report.Total, Count: report.Count, Window: strings.ToUpper(window), Actors: report.Facets.Actors, Actions: report.Facets.Actions, ResourceTypes: report.Facets.ResourceTypes}
	for _, bucket := range report.Buckets {
		view.Buckets = append(view.Buckets, webui.HistogramBarView{Label: bucket.Label, Count: bucket.Count, Level: bucket.Level})
	}
	for _, event := range report.Items {
		actor := event.Actor
		if actor == "" {
			actor = "system"
		}
		resourceType := event.ResourceType
		if resourceType == "" {
			resourceType = "service"
		}
		metadata := strings.TrimSpace(string(event.Metadata))
		if metadata == "" {
			metadata = "{}"
		}
		view.Items = append(view.Items, webui.AuditEventView{ID: event.ID, ProjectID: event.ProjectID, Action: event.Action, Actor: actor, ResourceType: resourceType, ResourceID: event.ResourceID, Metadata: metadata, CreatedAt: event.CreatedAt.Format("2006-01-02 15:04:05Z")})
	}
	return view
}

func auditFilterView(filter store.AuditFilter, window string) webui.AuditFilterView {
	return webui.AuditFilterView{Range: window, Query: filter.Search, Project: filter.ProjectID, Actor: filter.Actor, Action: filter.Action, ResourceType: filter.ResourceType}
}
