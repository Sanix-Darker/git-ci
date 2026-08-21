package httpapi

import (
	"fmt"

	"github.com/sanix-darker/git-ci/internal/store"
	"github.com/sanix-darker/git-ci/internal/webui"
)

func buildRunStepAnnotationViews(items []store.StepAnnotation) []webui.RunStepAnnotationView {
	views := make([]webui.RunStepAnnotationView, 0, len(items))
	for _, item := range items {
		dot := "blue"
		if item.Level == store.AnnotationNotice {
			dot = "green"
		} else if item.Level == store.AnnotationError {
			dot = "red"
		}
		location := item.File
		if item.StartLine != nil {
			location += fmt.Sprintf(":%d", *item.StartLine)
			if item.StartColumn != nil {
				location += fmt.Sprintf(":%d", *item.StartColumn)
			}
		}
		views = append(views, webui.RunStepAnnotationView{
			Level: string(item.Level), Dot: dot, Title: item.Title,
			Message: item.Message, Location: location,
		})
	}
	return views
}
