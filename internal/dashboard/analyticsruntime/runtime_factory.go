package analyticsruntime

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	analyticscontract "github.com/flidai/leapview/internal/analytics/runtime"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	dashboardruntimefactory "github.com/flidai/leapview/internal/dashboard/runtimefactory"
)

type RuntimeFactoryConfig struct {
	Projects analyticscontract.ProjectFactory
	MaxRows  int
	MaxBytes int64
}

func NewRuntimeBuilder(config RuntimeFactoryConfig) dashboardruntimefactory.Builder {
	return func(ctx context.Context, input dashboardruntimefactory.Input) (*dashboardruntime.Service, error) {
		if config.Projects == nil {
			return nil, fmt.Errorf("analytical project factory is unavailable")
		}
		return dashboardruntime.NewFromGeneration(ctx, input.Directory, NewFactory(Options{
			Projects: config.Projects, ResultLimits: dataquery.ResultLimits{MaxRows: config.MaxRows, MaxBytes: config.MaxBytes},
			SnapshotID: input.SnapshotID, ServingStateID: input.Identity.GenerationID, ProjectID: input.Identity.ProjectID,
			Environment: input.Identity.Environment, SemanticModelDigest: input.SemanticModelDigest,
			ArtifactDigest: input.ArtifactDigest, SourceDataDigest: input.SourceDataDigest,
			CandidateID: input.CandidateID, AuthorizationFingerprint: input.AuthorizationFingerprint,
			BindingFingerprint: input.BindingFingerprint,
			RelationNamespace:  input.RelationNamespace,
			DependencyEvidence: input.DependencyEvidence,
			SkipInitialRefresh: input.SkipInitialRefresh,
		}), input.Identity, input.Definition)
	}
}
