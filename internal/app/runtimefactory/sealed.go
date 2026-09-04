// PostgreSQL sealed-serving contracts shared by the delivery resolver and
// runtime factory.

package runtimefactory

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/analytics/resulttier"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	dashboardruntimefactory "github.com/flidai/leapview/internal/dashboard/runtimefactory"
	"github.com/flidai/leapview/internal/runtimehost"
)

var (
	ErrSealedRootUnavailable = errors.New("sealed serving root is unavailable")
	ErrSealedRootMismatch    = errors.New("sealed serving root is not bound to the serving artifact")
)

// SealedServingRoot is the exact durable root selected for one serving
// generation. ServingStateID and ServingArtifactDigest bind the new delivery
// pointer to the compiled graph artifact; a resolver must reject mismatches.
type SealedServingRoot struct {
	// TargetID is the canonical configured delivery target that owns this
	// serving root. DeliveryID remains build provenance and must never be used
	// as a serving-scope selector.
	TargetID              string
	GenerationID          string
	CandidateID           string
	AttemptID             string
	SealID                string
	ClosureDigest         string
	QualificationDigest   string
	PhysicalPoolID        string
	Compatibility         ducklake.CompatibilityTuple
	PoolContract          *ducklake.PoolContract
	ServingStateID        string
	ServingArtifactID     string
	ServingArtifactDigest string
	// PostgreSQL-backed roots carry relational catalog identity and qualification
	// evidence selected by durable delivery state.
	CatalogDatabase           string
	CatalogID                 string
	CatalogUUID               string
	CatalogMetadataSchema     string
	CatalogSnapshotID         int64
	DataPath                  string
	CatalogVersion            string
	CatalogVersionNumber      int64
	DuckDBVersion             string
	DuckLakeExtensionVersion  string
	DuckLakeSpecVersion       string
	CatalogSchemaVersion      string
	RelationNamespace         string
	RelationManifestDigest    string
	ObjectRoot                string
	ObjectRootDigest          string
	ArtifactRoot              string
	ArtifactRootDigest        string
	CompiledGraphDigest       string
	CompiledConfigDigest      string
	RequestDigest             string
	PlanDigest                string
	TenantDomain              string
	Region                    string
	EncryptionDomain          string
	ObjectNamespace           string
	DeliveryID                string
	FencingEpoch              int64
	CompatibilityDigest       string
	RuntimeVersion            string
	SecurityDomainFingerprint string
}

// SealedRootResolver resolves the active delivery generation (or a candidate
// preview) and its exact graph-artifact binding from durable control state.
// Returning a root for an unrelated serving state is a hard error.
type SealedRootResolver func(context.Context, runtimehost.RuntimeInput) (SealedServingRoot, error)

// SealedDashboardRuntimeBuilder opens dashboard data runtimes against the
// supplied immutable read-only environment. It must not retain the
// environment after the returned dashboard service is closed.
type SealedDashboardRuntimeBuilder func(context.Context, dashboardruntimefactory.Input, *ducklake.Environment, resulttier.Tier) (*dashboardruntime.Service, error)
