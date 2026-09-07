package module

import (
	"context"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type ConnectionAdministrationPermission = connectionbinding.AdministrationPermission
type ConnectionTargetBinding = connectionbinding.TargetBinding
type ConnectionBindingDependency = connectionbinding.BindingDependency
type ConnectionRotationAuditEvent = connectionbinding.RotationAuditEvent
type ConnectionRotationAuditRecorder = connectionbinding.RotationAuditRecorder
type ConnectionAdministrationAuditEvent = connectionbinding.AdministrationAuditEvent
type ConnectionAdministrationAuditRecorder = connectionbinding.AdministrationAuditRecorder
type ConnectionBindingScope = connectionbinding.BindingScope
type ConnectionBindingEvidence = connectionbinding.BindingEvidence
type ConnectionRequirement = connectionbinding.Requirement
type ConnectionResolver = analyticsruntime.ConnectionResolver
type ConnectionID = projectgraph.ResourceID
type RuntimeBindingRequest = connectionbinding.RuntimeBindingRequest
type RuntimeBindingLeases = connectionbinding.RuntimeBindingLeases
type RuntimeBindingLeaser = connectionbinding.RuntimeBindingLeaser
type RuntimeBindingAuthorizer = connectionbinding.RuntimeBindingAuthorizer

const (
	PermissionManageConnectionMetadata = connectionbinding.PermissionManageConnectionMetadata
	PermissionTestConnection           = connectionbinding.PermissionTestConnection
	PermissionViewConnectionHealth     = connectionbinding.PermissionViewConnectionHealth
)

var ErrConnectionAdministrationUnavailable = connectionbinding.ErrProviderUnavailable
var ErrConnectionBindingUnauthorized = connectionbinding.ErrUnauthorizedBinding

type ConnectionDependencyInspector interface {
	Dependents(context.Context, ConnectionTargetBinding) ([]ConnectionBindingDependency, error)
}

type ConnectionAdministrationAuthorizer func(
	context.Context,
	string,
	ConnectionAdministrationPermission,
	ConnectionTargetBinding,
) error

type ConnectionAdministrationConfig struct {
	Authorize           ConnectionAdministrationAuthorizer
	EnsureScope         func(context.Context, ConnectionBindingScope) error
	Dependencies        ConnectionDependencyInspector
	Pools               connectionbinding.AdministrationPoolDirectory
	Now                 func() time.Time
	RefreshTimeout      time.Duration
	MaxConcurrent       int
	Audit               ConnectionRotationAuditRecorder
	AdministrationAudit ConnectionAdministrationAuditRecorder
	// AuditIntentRecorder is required for command-facing transactional
	// administration when the capability is not itself transaction-aware.
	AuditIntentRecorder access.AuditIntentRecorder
	RequireAuditIntent  bool
}

type RuntimeBindingLeaserConfig struct {
	Authorize      RuntimeBindingAuthorizer
	Now            func() time.Time
	RefreshTimeout time.Duration
	MaxConcurrent  int
	Audit          ConnectionRotationAuditRecorder
}
