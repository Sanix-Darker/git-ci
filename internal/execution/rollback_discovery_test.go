package execution

import (
	"strings"
	"testing"

	"github.com/sanix-darker/git-ci/internal/store"
)

func TestDiscoverNormalizesExplicitRollbackExtension(t *testing.T) {
	for _, fixture := range []struct{ name, file, yaml string }{
		{"github", ".github/workflows/deploy.yml", strings.Join([]string{"name: deploy", "on: push", "jobs:", "  deploy:", "    runs-on: local", "    environment: production", "    x-gci:", "      rollback: ./release rollback", "      verify: ./release verify", "    steps:", "      - run: ./release deploy"}, "\n")},
		{"gitlab", ".gitlab-ci.yml", strings.Join([]string{"deploy:", "  environment:", "    name: production", "  x-gci:", "    rollback: ./release rollback", "    verify: ./release verify", "  script:", "    - ./release deploy"}, "\n")},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			root := t.TempDir()
			writeWorkflowFixture(t, root, fixture.file, fixture.yaml)
			definitions, err := Discover([]store.Project{fixtureProject(t, root, "rollback-"+fixture.name)})
			if err != nil || len(definitions) != 1 || len(definitions[0].Jobs) != 1 {
				t.Fatalf("definitions = %#v, %v", definitions, err)
			}
			job := definitions[0].Jobs[0]
			if job.RollbackCommand != "./release rollback" || job.VerifyCommand != "./release verify" {
				t.Fatalf("rollback extension = %#v", job)
			}
		})
	}
}

func TestDiscoverRejectsRollbackWithoutEnvironment(t *testing.T) {
	root := t.TempDir()
	writeWorkflowFixture(t, root, ".github/workflows/invalid.yml", strings.Join([]string{"name: invalid", "on: push", "jobs:", "  build:", "    runs-on: local", "    x-gci:", "      rollback: ./rollback", "    steps:", "      - run: true"}, "\n"))
	_, err := Discover([]store.Project{fixtureProject(t, root, "invalid-rollback")})
	if err == nil || !strings.Contains(err.Error(), "requires a deployment environment") {
		t.Fatalf("rollback validation error = %v", err)
	}
}
