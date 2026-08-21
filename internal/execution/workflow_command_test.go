package execution

import (
	"strings"
	"testing"

	"github.com/sanix-darker/git-ci/internal/store"
)

func TestWorkflowCommandStateMasksStopsAndBuildsAnnotations(t *testing.T) {
	state := newWorkflowCommandState(map[string]string{"TOKEN": "static-secret"})
	masked := state.process("step", store.LogStreamStdout, "::AdD-MaSk::runtime%25secret")
	if masked.line != "::add-mask::***" || masked.diagnostic != "" {
		t.Fatalf("mask result = %#v", masked)
	}
	if got := state.process("step", store.LogStreamStderr, "static-secret runtime%secret").line; got != "*** ***" {
		t.Fatalf("redacted stderr = %q", got)
	}
	state.process("step", store.LogStreamStdout, "::stop-commands::pause-token")
	ignored := state.process("step", store.LogStreamStdout, "::warning::ignored")
	if ignored.annotation != nil {
		t.Fatalf("stopped annotation = %#v", ignored.annotation)
	}
	state.process("step", store.LogStreamStdout, "::PAUSE-TOKEN::")
	result := state.process("step", store.LogStreamStdout, "::WaRnInG file=src%2Capp.go,line=12,col=4,title=Compile%3Ahint::bad%0Aline")
	if result.annotation == nil || result.annotation.Level != store.AnnotationWarning || result.annotation.File != "src,app.go" || result.annotation.Title != "Compile:hint" || result.annotation.StartLine == nil || *result.annotation.StartLine != 12 || result.annotation.Message != "bad\nline" {
		t.Fatalf("annotation = %#v", result.annotation)
	}
}

func TestWorkflowCommandStateRejectsUnsafeCommands(t *testing.T) {
	state := newWorkflowCommandState(nil)
	for _, line := range []string{"::add-mask", "::stop-commands::bad token", "::warning line=no::message", "::error::"} {
		result := state.process("step", store.LogStreamStdout, line)
		if result.diagnostic == "" {
			t.Fatalf("unsafe command %q had no diagnostic: %#v", line, result)
		}
	}
	oversized := "::notice::" + strings.Repeat("x", maxWorkflowCommandBytes)
	if result := state.process("step", store.LogStreamStdout, oversized); result.diagnostic == "" {
		t.Fatalf("oversized command result = %#v", result)
	}
}
