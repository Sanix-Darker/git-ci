package httpapi

import (
	"net/http"

	"github.com/sanix-darker/git-ci/internal/compatibility"
)

func (a *API) handleCompatibility(writer http.ResponseWriter, request *http.Request) {
	report, err := compatibility.Query(compatibilityFilter(request))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_compatibility_filter", err.Error())
		return
	}
	writeJSON(writer, http.StatusOK, report)
}

func compatibilityFilter(request *http.Request) compatibility.Filter {
	return compatibility.Filter{
		Provider: request.URL.Query().Get("provider"),
		Category: request.URL.Query().Get("category"),
		State:    request.URL.Query().Get("state"),
		Search:   request.URL.Query().Get("q"),
	}
}
