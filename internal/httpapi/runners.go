package httpapi

import (
	"net/http"
	"strings"

	"github.com/sanix-darker/git-ci/internal/runnerinventory"
	"github.com/sanix-darker/git-ci/internal/webui"
)

func (a *API) handleRunners(writer http.ResponseWriter, _ *http.Request) {
	inventory := a.execution.RunnerInventory()
	writeJSON(writer, http.StatusOK, map[string]any{"items": inventory.Runners, "count": len(inventory.Runners)})
}

func runnerInventoryViews(inventory runnerinventory.Inventory) []webui.RunnerView {
	views := make([]webui.RunnerView, 0, len(inventory.Runners))
	for _, runner := range inventory.Runners {
		dot := "dot-red"
		if strings.EqualFold(runner.Status, "online") {
			dot = "dot-green"
		}
		views = append(views, webui.RunnerView{
			ID: runner.ID, Name: runner.Name, Status: strings.ToUpper(runner.Status), Dot: dot,
			Mode: strings.ToUpper(runner.Mode), OS: strings.ToUpper(runner.OS), Architecture: strings.ToUpper(runner.Architecture),
			Group: runner.Group, Labels: runner.Labels, Tags: runner.Tags, DockerAvailable: runner.DockerAvailable,
			RunUntagged: runner.RunUntagged, MaxParallel: runner.MaxParallel,
		})
	}
	return views
}
