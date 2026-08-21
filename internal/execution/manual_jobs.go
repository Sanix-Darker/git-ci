package execution

import (
	"context"

	"github.com/sanix-darker/git-ci/internal/store"
)

func (m *Manager) PlayManualJob(ctx context.Context, params store.PlayManualJobParams) (store.ManualJobPlayResult, error) {
	result, err := m.store.PlayManualJob(ctx, params)
	if err != nil {
		return store.ManualJobPlayResult{}, err
	}
	m.Notify()
	return result, nil
}
