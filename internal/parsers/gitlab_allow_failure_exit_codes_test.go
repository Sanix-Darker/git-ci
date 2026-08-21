package parsers

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGitlabParserAllowFailureExitCodes(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".gitlab-ci.yml")
	contents := []byte(`scalar:
  script: ["exit 137"]
  allow_failure:
    exit_codes: 137
array:
  script: ["exit 255"]
  allow_failure:
    exit_codes: [255, 137, 255]
boolean:
  script: ["exit 1"]
  allow_failure: true
`)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	pipeline, err := NewGitlabParser().Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if scalar := pipeline.Jobs["scalar"]; scalar.AllowFailure || !reflect.DeepEqual(scalar.AllowFailureExitCodes, []int{137}) {
		t.Fatalf("scalar allow failure = %#v", scalar)
	}
	if array := pipeline.Jobs["array"]; array.AllowFailure || !reflect.DeepEqual(array.AllowFailureExitCodes, []int{255, 137}) {
		t.Fatalf("array allow failure = %#v", array)
	}
	if boolean := pipeline.Jobs["boolean"]; !boolean.AllowFailure || len(boolean.AllowFailureExitCodes) != 0 {
		t.Fatalf("boolean allow failure = %#v", boolean)
	}
}

func TestGitlabParserRejectsInvalidAllowFailureExitCodes(t *testing.T) {
	cases := map[string]string{
		"empty":     "[]",
		"string":    "failure",
		"mixed":     "[137, failure]",
		"negative":  "-1",
		"too-large": "256",
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".gitlab-ci.yml")
			contents := "job:\n  script: [\"exit 1\"]\n  allow_failure:\n    exit_codes: " + value + "\n"
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := NewGitlabParser().Parse(path)
			if err == nil || !strings.Contains(err.Error(), "allow_failure") {
				t.Fatalf("invalid allow_failure error = %v", err)
			}
		})
	}
}
