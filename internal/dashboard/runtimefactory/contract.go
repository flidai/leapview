package runtimefactory

import (
	"context"

	"github.com/flidai/leapview/internal/analytics/resultidentity"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type Input struct {
	Directory                                                 string
	Identity                                                  projectgraph.ServingIdentity
	SemanticModelDigest, ArtifactDigest, SourceDataDigest     string
	CandidateID, AuthorizationFingerprint, BindingFingerprint string
	SkipInitialRefresh                                        bool
	SnapshotID                                                int64
	Definition                                                *dashboardruntime.ProjectDefinition
	DependencyEvidence                                        map[string]resultidentity.Evidence
}

type Builder func(context.Context, Input) (*dashboardruntime.Service, error)
