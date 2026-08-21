package httpapi

import "testing"

func TestJobSemanticBadgesExposeAllowedFailure(t *testing.T) {
	badges := jobSemanticBadges(workflowDefinitionJobDocument{AllowFailure: true})
	if !hasSemanticBadge(badges, "ALLOW FAILURE") {
		t.Fatalf("expected allowed-failure badge, got %#v", badges)
	}
	if hasSemanticBadge(jobSemanticBadges(workflowDefinitionJobDocument{}), "ALLOW FAILURE") {
		t.Fatal("ordinary job unexpectedly received allowed-failure badge")
	}
}
