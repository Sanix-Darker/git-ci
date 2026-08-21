package httpapi

import (
	"sort"

	"github.com/sanix-darker/git-ci/internal/store"
	"github.com/sanix-darker/git-ci/internal/webui"
)

func buildStepLogEntries(lines []webui.LogView, sections []store.StepLogSection) []webui.LogEntryView {
	ordered := append([]store.StepLogSection(nil), sections...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].StartSequence == ordered[j].StartSequence {
			return ordered[i].Depth < ordered[j].Depth
		}
		return ordered[i].StartSequence < ordered[j].StartSequence
	})
	starts := make(map[int][]store.StepLogSection)
	ends := make(map[int][]store.StepLogSection)
	for _, section := range ordered {
		starts[int(section.StartSequence)] = append(starts[int(section.StartSequence)], section)
		if section.EndSequence != nil {
			ends[int(*section.EndSequence)] = append(ends[int(*section.EndSequence)], section)
		}
	}
	roots := make([]webui.LogEntryView, 0, len(lines))
	stack := make([]*webui.LogGroupView, 0)
	appendEntry := func(entry webui.LogEntryView) {
		if len(stack) == 0 {
			roots = append(roots, entry)
			return
		}
		parent := stack[len(stack)-1]
		parent.Entries = append(parent.Entries, entry)
	}
	for _, line := range lines {
		for _, section := range starts[line.Sequence] {
			dot := "blue"
			if section.Provider == store.LogSectionGitLab {
				dot = "green"
			}
			group := &webui.LogGroupView{
				ID: section.ID, Provider: string(section.Provider), Name: section.Name,
				Dot: dot, Open: !section.Collapsed,
			}
			appendEntry(webui.LogEntryView{Group: group})
			stack = append(stack, group)
		}
		_, startsSection := starts[line.Sequence]
		_, endsSection := ends[line.Sequence]
		if !startsSection && !endsSection {
			lineCopy := line
			appendEntry(webui.LogEntryView{Line: &lineCopy})
			for _, group := range stack {
				group.LineCount++
			}
		}
		for _, section := range ends[line.Sequence] {
			for index := len(stack) - 1; index >= 0; index-- {
				if stack[index].ID == section.ID {
					stack = stack[:index]
					break
				}
			}
		}
	}
	return roots
}
