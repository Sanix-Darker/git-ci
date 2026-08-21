package store

import "testing"

func TestStepLogSectionsPersistAndValidateBoundaries(t *testing.T) {
	ctx := t.Context()
	path := t.TempDir() + "/log-sections.db"
	database, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	project, workflow := createExecutionProjectAndWorkflow(t, ctx, database, "log-sections")
	run, err := database.EnqueueRun(ctx, testEnqueueRunParams(project.ID, workflow.ID, "log-sections"))
	if err != nil {
		t.Fatal(err)
	}
	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	stepID := graph.Jobs[0].Steps[0].ID
	start, err := database.AppendLogLine(ctx, AppendLogLineParams{StepID: stepID, Stream: LogStreamStdout, Message: "section start"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.AppendLogLine(ctx, AppendLogLineParams{StepID: stepID, Stream: LogStreamStdout, Message: "body"}); err != nil {
		t.Fatal(err)
	}
	end, err := database.AppendLogLine(ctx, AppendLogLineParams{StepID: stepID, Stream: LogStreamStdout, Message: "section end"})
	if err != nil {
		t.Fatal(err)
	}
	section, err := database.StartStepLogSection(ctx, StartStepLogSectionParams{
		ID: "section-1", StepID: stepID, Provider: LogSectionGitLab, Name: "Compile <unsafe>",
		Depth: 0, Collapsed: true, StartSequence: start.Sequence,
	})
	if err != nil || !section.Collapsed {
		t.Fatalf("start section = %#v, %v", section, err)
	}
	finished, err := database.FinishStepLogSection(ctx, FinishStepLogSectionParams{ID: section.ID, StepID: stepID, EndSequence: end.Sequence})
	if err != nil || finished.EndSequence == nil || *finished.EndSequence != end.Sequence {
		t.Fatalf("finish section = %#v, %v", finished, err)
	}
	if _, err := database.StartStepLogSection(ctx, StartStepLogSectionParams{ID: "bad", StepID: stepID, Provider: LogSectionGitHub, Name: "bad", StartSequence: end.Sequence + 100}); err == nil {
		t.Fatal("foreign boundary unexpectedly accepted")
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	sections, err := reopened.ListStepLogSections(ctx, stepID)
	if err != nil || len(sections) != 1 || sections[0].Name != "Compile <unsafe>" || sections[0].EndSequence == nil {
		t.Fatalf("persisted sections = %#v, %v", sections, err)
	}
}
