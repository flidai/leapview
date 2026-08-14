// Package runtime defines typed analytical contracts shared with
// consumer-owned adapters. Capability modules expose capabilities rather than
// DuckDB or cache implementations.
package runtime

import (
	"context"
	"time"

	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	analyticsmaterialize "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/analytics/resource"
	"github.com/flidai/leapview/internal/platform/transaction"
)

type WorkspaceDatabase interface {
	analyticsmaterialize.Database
	resource.SessionProvider
	ValidateSnapshot(context.Context, int64) error
	CommitTransaction(context.Context, string, map[string]string, func(transaction.Transaction) error) (int64, error)
}

// ConnectionResolver supplies a fully target-bound connection only while an
// admitted Analytics runtime holds the validated pool generation that owns it.
type ConnectionResolver interface {
	Resolve(context.Context, string, semanticmodel.Connection) (semanticmodel.Connection, error)
}

// WorkspaceRequest describes a governed analytical workspace without exposing
// DuckDB construction or cache implementation details to consumer capabilities.
type WorkspaceRequest struct {
	Models                   map[string]*semanticmodel.Model
	SnapshotID               int64
	ServingStateID           string
	WorkspaceID              string
	Environment              string
	SemanticDigest           string
	ArtifactDigest           string
	SourceDataDigest         string
	CandidateID              string
	AuthorizationFingerprint string
	BindingFingerprint       string
	RequiredExtensions       []string
	ResultLimits             dataquery.ResultLimits
}

// Workspace is the narrow analytical runtime consumed by dashboard adapters.
type Workspace interface {
	ExecuteDataQuery(context.Context, dataquery.Query) (dataquery.Result, error)
	ExecuteDataQueryArrow(context.Context, dataquery.Query, arrowquery.Sink) (dataquery.Result, error)
	ExecuteDataQueryBundle(context.Context, []dataquery.BundleRequest) (dataquery.BundleResult, error)
	Refresh(context.Context) error
	RefreshModelTables(context.Context, string, []string) error
	Close() error
	LastRefresh() time.Time
	DuckLakeSnapshotID() int64
	ReadConcurrency() int
}

type WorkspaceFactory interface {
	OpenWorkspace(context.Context, WorkspaceRequest) (Workspace, error)
}

type WorkspaceFactoryFunc func(context.Context, WorkspaceRequest) (Workspace, error)

func (f WorkspaceFactoryFunc) OpenWorkspace(ctx context.Context, request WorkspaceRequest) (Workspace, error) {
	return f(ctx, request)
}
