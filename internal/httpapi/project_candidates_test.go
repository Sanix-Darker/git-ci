package httpapi

import (
	"testing"

	"github.com/sanix-darker/git-ci/internal/projects"
	"github.com/sanix-darker/git-ci/internal/store"
)

func TestUnregisteredProjectCandidates(t *testing.T) {
	registeredPath := "/srv/repos/alpha"
	items := []store.Project{{CanonicalPath: &registeredPath}, {CanonicalPath: nil}}
	candidates := []projects.Project{
		{Path: registeredPath, SuggestedSlug: "alpha"},
		{Path: "/srv/repos/beta", SuggestedSlug: "beta"},
		{Path: "/srv/repos/gamma", SuggestedSlug: "gamma"},
	}

	result := unregisteredProjectCandidates(items, candidates)
	if len(result) != 2 {
		t.Fatalf("expected two unregistered candidates, got %d", len(result))
	}
	if result[0].SuggestedSlug != "beta" || result[1].SuggestedSlug != "gamma" {
		t.Fatalf("candidate order was not preserved: %#v", result)
	}
	if len(candidates) != 3 {
		t.Fatal("input candidates were mutated")
	}
}
