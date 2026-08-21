package httpapi

import (
	"testing"

	"github.com/sanix-darker/git-ci/internal/store"
	"github.com/sanix-darker/git-ci/internal/webui"
)

func TestBuildStepLogEntriesNestsAndExcludesBoundaries(t *testing.T) {
	outerEnd, innerEnd := int64(6), int64(5)
	entries := buildStepLogEntries([]webui.LogView{
		{Sequence: 1, Message: "outer start"}, {Sequence: 2, Message: "outer body"},
		{Sequence: 3, Message: "inner start"}, {Sequence: 4, Message: "inner body"},
		{Sequence: 5, Message: "inner end"}, {Sequence: 6, Message: "outer end"},
	}, []store.StepLogSection{
		{ID: "outer", Provider: store.LogSectionGitHub, Name: "Outer", StartSequence: 1, EndSequence: &outerEnd},
		{ID: "inner", Provider: store.LogSectionGitLab, Name: "Inner <unsafe>", Depth: 1, Collapsed: true, StartSequence: 3, EndSequence: &innerEnd},
	})
	if len(entries) != 1 || entries[0].Group == nil || entries[0].Group.LineCount != 2 || len(entries[0].Group.Entries) != 2 {
		t.Fatalf("outer entries = %#v", entries)
	}
	inner := entries[0].Group.Entries[1].Group
	if inner == nil || inner.Open || inner.LineCount != 1 || len(inner.Entries) != 1 || inner.Entries[0].Line.Message != "inner body" {
		t.Fatalf("inner group = %#v", inner)
	}
}
