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
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	"github.com/flidai/leapview/internal/analytics/resulttier"
	"github.com/flidai/leapview/internal/platform/transaction"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type ProjectDatabase interface {
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

// ProjectRequest describes a governed analytical project without exposing
// DuckDB construction or cache implementation details to consumer capabilities.
type ProjectRequest struct {
	Models         map[string]*semanticmodel.Model
	SnapshotID     int64
	ServingStateID string
	// TargetID is the canonical configured delivery target that owns this
	// runtime. It scopes stable query-result partitions independently of a
	// serving generation.
	TargetID string
	// SnapshotSealID is the exact durable seal admitted for this runtime. It is
	// cache-admission provenance and is intentionally excluded from the stable
	// result partition/key so equivalent results can survive generation
	// cutovers.
	SnapshotSealID           string
	ProjectID                projectgraph.ResourceID
	Environment              string
	SemanticDigest           string
	ArtifactDigest           string
	SourceDataDigest         string
	CandidateID              string
	AuthorizationFingerprint string
	BindingFingerprint       string
	// RelationNamespace is the authority-derived DuckLake schema used for
	// snapshot-qualified serving reads. Empty retains the legacy model schema.
	RelationNamespace  string
	RequiredExtensions []string
	// SkipInitialRefresh is used when a private candidate starts from an
	// exact sealed base and the caller refreshes only impacted relations.
	SkipInitialRefresh bool
	ResultLimits       dataquery.ResultLimits
	DependencyEvidence map[string]resultidentity.Evidence
	// ResultTier is an optional durable result-cache tier (for example, the
	// target-scoped L3 object cache). A nil value leaves the in-process cache
	// behaviour unchanged.
	ResultTier resulttier.Tier
}

// Project is the narrow analytical runtime consumed by dashboard adapters.
type Project interface {
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

type ProjectFactory interface {
	OpenProject(context.Context, ProjectRequest) (Project, error)
}

type ProjectFactoryFunc func(context.Context, ProjectRequest) (Project, error)

func (f ProjectFactoryFunc) OpenProject(ctx context.Context, request ProjectRequest) (Project, error) {
	return f(ctx, request)
}
