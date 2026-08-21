package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/sanix-darker/git-ci/internal/auth"
	"github.com/sanix-darker/git-ci/internal/gitrepository"
	"github.com/sanix-darker/git-ci/internal/store"
)

type releaseCreatePayload struct {
	RunID      string `json:"runId"`
	TagName    string `json:"tagName"`
	Name       string `json:"name"`
	Notes      string `json:"notes"`
	Prerelease bool   `json:"prerelease"`
}

type releaseResource struct {
	Release     store.Release             `json:"release"`
	Project     store.Project             `json:"project"`
	Run         store.Run                 `json:"run"`
	Artifacts   []releaseArtifactResource `json:"artifacts"`
	Deployments []store.Deployment        `json:"deployments"`
}

type releaseArtifactResource struct {
	store.Artifact
	DownloadURL string `json:"downloadUrl"`
}

func (a *API) handleProjectReleases(writer http.ResponseWriter, request *http.Request) {
	projectID := strings.TrimSpace(request.PathValue("project"))
	if request.Method == http.MethodGet {
		filter := releaseFilterFromRequest(request)
		filter.ProjectID = projectID
		items, err := a.store.ListReleases(request.Context(), filter)
		if err != nil {
			a.writeReleaseError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"items": items, "count": len(items), "filter": filter})
		return
	}
	var payload releaseCreatePayload
	if !a.decodeJSON(writer, request, &payload) {
		return
	}
	item, err := a.createRelease(request, projectID, payload)
	if err != nil {
		a.writeReleaseError(writer, err)
		return
	}
	a.recordReleaseAudit(request, item, "release.created")
	writeJSON(writer, http.StatusCreated, item)
}

func (a *API) handleRelease(writer http.ResponseWriter, request *http.Request) {
	id := request.PathValue("release")
	if request.Method == http.MethodGet {
		resource, err := a.releaseResource(request, id)
		if err != nil {
			a.writeReleaseError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, resource)
		return
	}
	if request.Method == http.MethodDelete {
		item, err := a.store.GetRelease(request.Context(), id)
		if err != nil {
			a.writeReleaseError(writer, err)
			return
		}
		if err := a.store.DeleteDraftRelease(request.Context(), id); err != nil {
			a.writeReleaseError(writer, err)
			return
		}
		a.recordReleaseAudit(request, item, "release.deleted")
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	current, err := a.store.GetRelease(request.Context(), id)
	if err != nil {
		a.writeReleaseError(writer, err)
		return
	}
	var payload struct {
		Name       *string `json:"name"`
		Notes      *string `json:"notes"`
		Prerelease *bool   `json:"prerelease"`
	}
	if !a.decodeJSON(writer, request, &payload) {
		return
	}
	name, notes, prerelease := current.Name, current.Notes, current.Prerelease
	if payload.Name != nil {
		name = *payload.Name
	}
	if payload.Notes != nil {
		notes = *payload.Notes
	}
	if payload.Prerelease != nil {
		prerelease = *payload.Prerelease
	}
	item, err := a.store.UpdateRelease(request.Context(), store.UpdateReleaseParams{ReleaseID: id, Name: name, Notes: notes, Prerelease: prerelease})
	if err != nil {
		a.writeReleaseError(writer, err)
		return
	}
	a.recordReleaseAudit(request, item, "release.updated")
	writeJSON(writer, http.StatusOK, item)
}

func (a *API) handlePublishRelease(writer http.ResponseWriter, request *http.Request) {
	principal := request.Context().Value(principalContextKey{}).(auth.Principal)
	item, err := a.store.PublishRelease(request.Context(), request.PathValue("release"), principal.Subject)
	if err != nil {
		a.writeReleaseError(writer, err)
		return
	}
	a.recordReleaseAudit(request, item, "release.published")
	writeJSON(writer, http.StatusOK, item)
}

func (a *API) handleLatestRelease(writer http.ResponseWriter, request *http.Request) {
	projectID := strings.TrimSpace(request.URL.Query().Get("project"))
	if projectID == "" {
		writeError(writer, http.StatusBadRequest, "project_required", "project query parameter is required")
		return
	}
	item, err := a.store.GetLatestRelease(request.Context(), projectID)
	if err != nil {
		a.writeReleaseError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, item)
}

func (a *API) createRelease(request *http.Request, projectID string, payload releaseCreatePayload) (store.Release, error) {
	project, err := a.store.GetProject(request.Context(), projectID)
	if err != nil {
		return store.Release{}, err
	}
	if !project.Active || project.CanonicalPath == nil {
		return store.Release{}, &store.ErrReleaseTransition{Code: "project_inactive", Message: "release project must be an active local checkout"}
	}
	graph, err := a.store.GetRunGraph(request.Context(), payload.RunID)
	if err != nil {
		return store.Release{}, err
	}
	if graph.Run.ProjectID != project.ID {
		return store.Release{}, &store.ErrReleaseTransition{Code: "source_ownership_mismatch", Message: "release source run does not belong to the project"}
	}
	resolved, err := gitrepository.ResolveTagCommit(request.Context(), *project.CanonicalPath, payload.TagName)
	if err != nil {
		return store.Release{}, err
	}
	principal := request.Context().Value(principalContextKey{}).(auth.Principal)
	return a.store.CreateRelease(request.Context(), store.CreateReleaseParams{
		ProjectID: project.ID, RunID: payload.RunID, TagName: payload.TagName,
		TargetCommitSHA: resolved, Name: payload.Name, Notes: payload.Notes,
		Prerelease: payload.Prerelease, Actor: principal.Subject,
	})
}

func (a *API) releaseResource(request *http.Request, releaseID string) (releaseResource, error) {
	item, err := a.store.GetRelease(request.Context(), releaseID)
	if err != nil {
		return releaseResource{}, err
	}
	project, err := a.store.GetProject(request.Context(), item.ProjectID)
	if err != nil {
		return releaseResource{}, err
	}
	graph, err := a.store.GetRunGraph(request.Context(), item.RunID)
	if err != nil {
		return releaseResource{}, err
	}
	artifacts, err := a.store.ListRunArtifacts(request.Context(), item.RunID)
	if err != nil {
		return releaseResource{}, err
	}
	artifactResources := make([]releaseArtifactResource, 0, len(artifacts))
	for _, artifact := range artifacts {
		artifactResources = append(artifactResources, releaseArtifactResource{Artifact: artifact, DownloadURL: "/api/v1/runs/" + item.RunID + "/artifacts/" + artifact.ID})
	}
	allDeployments, err := a.store.ListDeployments(request.Context(), item.ProjectID)
	if err != nil {
		return releaseResource{}, err
	}
	deployments := make([]store.Deployment, 0)
	for _, deployment := range allDeployments {
		if deployment.RunID == item.RunID {
			deployments = append(deployments, deployment)
		}
	}
	return releaseResource{Release: item, Project: project, Run: graph.Run, Artifacts: artifactResources, Deployments: deployments}, nil
}

func releaseFilterFromRequest(request *http.Request) store.ReleaseFilter {
	return store.ReleaseFilter{
		ProjectID: strings.TrimSpace(request.URL.Query().Get("project")),
		State:     store.ReleaseState(strings.ToLower(strings.TrimSpace(request.URL.Query().Get("state")))),
		Query:     strings.TrimSpace(request.URL.Query().Get("q")),
		Limit:     200,
	}
}

func (a *API) recordReleaseAudit(request *http.Request, item store.Release, action string) {
	principal := request.Context().Value(principalContextKey{}).(auth.Principal)
	metadata, _ := json.Marshal(map[string]string{"tagName": item.TagName, "runId": item.RunID, "state": string(item.State)})
	_, _ = a.store.RecordAudit(request.Context(), store.AuditEvent{ProjectID: item.ProjectID, Action: action, Actor: principal.Subject, ResourceType: "release", ResourceID: item.ID, Metadata: metadata})
}

func (a *API) writeReleaseError(writer http.ResponseWriter, err error) {
	var notFound *store.ErrNotFound
	var conflict *store.ErrConflict
	var transition *store.ErrReleaseTransition
	switch {
	case errors.As(err, &notFound):
		writeError(writer, http.StatusNotFound, "not_found", err.Error())
	case errors.As(err, &conflict):
		writeError(writer, http.StatusConflict, "conflict", err.Error())
	case errors.As(err, &transition):
		writeError(writer, http.StatusUnprocessableEntity, transition.Code, transition.Message)
	default:
		writeError(writer, http.StatusUnprocessableEntity, "invalid_release", err.Error())
	}
}
