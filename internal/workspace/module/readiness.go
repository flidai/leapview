package module

import (
	"context"

	"github.com/flidai/leapview/internal/workspace"
)

type activeWorkspaceLister interface {
	ListWithActiveMetadata(context.Context, string) ([]workspace.Summary, error)
}

func (m *Module) ActiveRuntimeWorkspaces(ctx context.Context) ([]string, error) {
	if m == nil {
		return nil, nil
	}
	if lister, ok := m.readModel.(activeWorkspaceLister); ok {
		summaries, err := lister.ListWithActiveMetadata(ctx, m.runtimeEnvironment)
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, len(summaries))
		for _, summary := range summaries {
			if summary.ID != "" && summary.ActiveServingStateID != "" {
				out = append(out, string(summary.ID))
			}
		}
		return out, nil
	}
	return nil, nil
}
