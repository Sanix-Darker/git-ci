package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/sanix-darker/git-ci/internal/auth"
	"github.com/sanix-darker/git-ci/internal/store"
	"github.com/sanix-darker/git-ci/internal/webui"
)

func (a *API) handleReleasePageWeb(writer http.ResponseWriter, request *http.Request) {
	a.renderAppSectionState(writer, request, "releases", "", strings.TrimSpace(request.URL.Query().Get("notice")), http.StatusOK)
}

func (a *API) handleCreateReleaseWeb(writer http.ResponseWriter, request *http.Request) {
	projectID := strings.TrimSpace(request.PathValue("project"))
	if projectID == "" {
		projectID = strings.TrimSpace(request.FormValue("projectId"))
	}
	payload := releaseCreatePayload{RunID: request.FormValue("runId"), TagName: request.FormValue("tagName"), Name: request.FormValue("name"), Notes: request.FormValue("notes"), Prerelease: request.FormValue("prerelease") == "on"}
	item, err := a.createRelease(request, projectID, payload)
	if err != nil {
		a.renderReleaseMutationError(writer, request, projectID, err)
		return
	}
	a.recordReleaseAudit(request, item, "release.created")
	a.redirectWeb(writer, request, "/app/releases/"+item.ID+"?notice="+url.QueryEscape("RELEASE DRAFT CREATED"))
}

func (a *API) handleUpdateReleaseWeb(writer http.ResponseWriter, request *http.Request) {
	id := request.PathValue("release")
	item, err := a.store.UpdateRelease(request.Context(), store.UpdateReleaseParams{ReleaseID: id, Name: request.FormValue("name"), Notes: request.FormValue("notes"), Prerelease: request.FormValue("prerelease") == "on"})
	if err != nil {
		a.renderReleaseMutationError(writer, request, "", err)
		return
	}
	a.recordReleaseAudit(request, item, "release.updated")
	a.redirectWeb(writer, request, "/app/releases/"+item.ID+"?notice="+url.QueryEscape("RELEASE DRAFT UPDATED"))
}

func (a *API) handlePublishReleaseWeb(writer http.ResponseWriter, request *http.Request) {
	principal := request.Context().Value(principalContextKey{}).(auth.Principal)
	item, err := a.store.PublishRelease(request.Context(), request.PathValue("release"), principal.Subject)
	if err != nil {
		a.renderReleaseMutationError(writer, request, "", err)
		return
	}
	a.recordReleaseAudit(request, item, "release.published")
	a.redirectWeb(writer, request, "/app/releases/"+item.ID+"?notice="+url.QueryEscape("RELEASE PUBLISHED"))
}

func (a *API) handleDeleteReleaseWeb(writer http.ResponseWriter, request *http.Request) {
	item, err := a.store.GetRelease(request.Context(), request.PathValue("release"))
	if err != nil {
		a.renderReleaseMutationError(writer, request, "", err)
		return
	}
	if err := a.store.DeleteDraftRelease(request.Context(), item.ID); err != nil {
		a.renderReleaseMutationError(writer, request, "", err)
		return
	}
	a.recordReleaseAudit(request, item, "release.deleted")
	target := "/app/releases?notice=" + url.QueryEscape("RELEASE DRAFT DELETED")
	if request.FormValue("returnProject") == item.ProjectID {
		target = "/app/projects/" + item.ProjectID + "?notice=" + url.QueryEscape("RELEASE DRAFT DELETED")
	}
	a.redirectWeb(writer, request, target)
}

func (a *API) renderReleaseMutationError(writer http.ResponseWriter, request *http.Request, projectID string, err error) {
	if projectID == "" {
		if item, getErr := a.store.GetRelease(request.Context(), request.PathValue("release")); getErr == nil {
			projectID = item.ProjectID
		}
	}
	if request.FormValue("returnProject") == projectID && projectID != "" {
		a.renderProjectWorkspace(writer, request, projectID, err.Error(), "", http.StatusUnprocessableEntity)
		return
	}
	a.renderAppSectionState(writer, request, "releases", err.Error(), "", http.StatusUnprocessableEntity)
}

func (a *API) populateReleasePage(ctx context.Context, data *webui.PageData, request *http.Request, scopedProjectID string) error {
	filter := releaseFilterFromRequest(request)
	if scopedProjectID != "" {
		filter.ProjectID = scopedProjectID
	} else if data.Page != "releases" {
		filter = store.ReleaseFilter{Limit: 200}
	}
	items, err := a.store.ListReleases(ctx, filter)
	if err != nil {
		return err
	}
	data.ReleaseFilter = webui.ReleaseFilterView{Project: filter.ProjectID, State: string(filter.State), Query: filter.Query}
	for _, item := range items {
		data.Releases = append(data.Releases, releaseView(item))
	}
	for _, run := range data.Runs {
		if !strings.EqualFold(run.Status, string(store.StatusSucceeded)) || (len(run.CommitSHA) != 40 && len(run.CommitSHA) != 64) {
			continue
		}
		if run.ProjectID == "" || (filter.ProjectID != "" && run.ProjectID != filter.ProjectID) {
			continue
		}
		data.ReleaseCandidates = append(data.ReleaseCandidates, webui.ReleaseRunView{ID: run.ID, ProjectID: run.ProjectID, ProjectName: run.ProjectName, WorkflowName: run.WorkflowName, Ref: run.Ref, CommitSHA: run.CommitSHA, CreatedAt: run.CreatedAt})
	}
	selectedID := strings.TrimSpace(request.PathValue("release"))
	if data.Page != "releases" || selectedID == "" {
		return nil
	}
	resource, err := a.releaseResource(request, selectedID)
	if err != nil {
		return err
	}
	detail := webui.ReleaseDetailView{Release: releaseView(resource.Release), SourceRun: webui.ReleaseRunView{ID: resource.Run.ID, ProjectID: resource.Run.ProjectID, ProjectName: resource.Release.ProjectName, WorkflowName: stringValue(resource.Run.WorkflowKey), Ref: stringValue(resource.Run.Ref), CommitSHA: stringValue(resource.Run.CommitSHA), CreatedAt: resource.Run.CreatedAt.UTC().Format("2006-01-02 15:04:05Z")}}
	for _, artifact := range resource.Artifacts {
		detail.Artifacts = append(detail.Artifacts, webui.ReleaseArtifactView{ID: artifact.ID, Name: artifact.Name, SHA256: artifact.SHA256, Size: strconv.FormatInt(artifact.SizeBytes, 10) + " B", FileCount: artifact.FileCount, Download: "/app/runs/" + resource.Run.ID + "/artifacts/" + artifact.ID})
	}
	for _, deployment := range resource.Deployments {
		detail.Deployments = append(detail.Deployments, webui.ReleaseDeploymentView{ID: deployment.ID, RunID: deployment.RunID, Environment: deployment.Environment, Tier: string(deployment.DeploymentTier), Status: strings.ToUpper(string(deployment.Status)), UpdatedAt: deployment.UpdatedAt.UTC().Format("2006-01-02 15:04:05Z")})
	}
	data.SelectedRelease = &detail
	return nil
}

func releaseView(item store.Release) webui.ReleaseView {
	dot := ""
	if item.State == store.ReleasePublished {
		dot = "dot-green"
	}
	publishedAt := "DRAFT"
	if item.PublishedAt != nil {
		publishedAt = item.PublishedAt.UTC().Format("2006-01-02 15:04:05Z")
	}
	return webui.ReleaseView{ID: item.ID, ProjectID: item.ProjectID, ProjectName: item.ProjectName, RunID: item.RunID, TagName: item.TagName, TargetCommitSHA: item.TargetCommitSHA, Name: item.Name, Notes: item.Notes, State: strings.ToUpper(string(item.State)), Dot: dot, CreatedBy: item.CreatedBy, CreatedAt: item.CreatedAt.UTC().Format("2006-01-02 15:04:05Z"), PublishedAt: publishedAt, Prerelease: item.Prerelease}
}
