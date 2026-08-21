package parsers

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestGitLabRetryJobAndDefaultSemantics(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".gitlab-ci.yml")
	contents := []byte(`
default:
  retry:
    max: 2
    when: runner_system_failure
    exit_codes: 137
inherited:
  script: ["printf inherited"]
overridden:
  retry:
    max: 1
    when: [script_failure, job_execution_timeout]
    exit_codes: [17, 42]
  script: ["printf overridden"]
disabled:
  retry: 0
  script: ["printf disabled"]
`)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	pipeline, err := NewGitlabParser().Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if retry := pipeline.Jobs["inherited"].Retry; retry == nil || retry.MaxAttempts != 2 || !reflect.DeepEqual(retry.When, []string{"runner_system_failure"}) || !reflect.DeepEqual(retry.ExitCodes, []int{137}) {
		t.Fatalf("inherited retry = %#v", retry)
	}
	if retry := pipeline.Jobs["overridden"].Retry; retry == nil || retry.MaxAttempts != 1 || !reflect.DeepEqual(retry.When, []string{"script_failure", "job_execution_timeout"}) || !reflect.DeepEqual(retry.ExitCodes, []int{17, 42}) {
		t.Fatalf("overridden retry = %#v", retry)
	}
	if retry := pipeline.Jobs["disabled"].Retry; retry == nil || retry.MaxAttempts != 0 {
		t.Fatalf("disabled retry = %#v", retry)
	}
}
