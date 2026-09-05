package connectionbinding

import (
	"context"
	"fmt"
	"github.com/flidai/leapview/internal/analytics/connectors"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"sort"
	"strings"
	"sync"
)

type RuntimeBindingAuthorizer func(context.Context, string, TargetBinding) error

type RuntimeBindingLeaserConfig struct {
	Bindings  BindingCatalog
	Pools     ValidatedPoolDirectory
	Authorize RuntimeBindingAuthorizer
}

type RuntimeBindingLeaser struct {
	bindings  BindingCatalog
	pools     ValidatedPoolDirectory
	authorize RuntimeBindingAuthorizer
}

type RuntimeBindingRequest struct {
	Actor        string
	Identity     projectgraph.ServingIdentity
	TargetID     TargetID
	Requirements []Requirement
}

// RuntimeBindingLeases holds target-owned pool generations for the lifetime of
// one candidate runtime. It contains only non-secret validation evidence.
type RuntimeBindingLeases struct {
	once     sync.Once
	mu       sync.RWMutex
	leases   []ValidatedPoolLease
	evidence []RuntimeBindingEvidence
}

func NewRuntimeBindingLeaser(config RuntimeBindingLeaserConfig) (*RuntimeBindingLeaser, error) {
	if config.Bindings == nil || config.Pools == nil || config.Authorize == nil {
		return nil, fmt.Errorf(
			"%w: binding catalog, validated pool directory, and authorizer are required",
			ErrInvalidBinding,
		)
	}
	return &RuntimeBindingLeaser{
		bindings: config.Bindings, pools: config.Pools, authorize: config.Authorize,
	}, nil
}

func (leaser *RuntimeBindingLeaser) Acquire(
	ctx context.Context,
	request RuntimeBindingRequest,
) (_ *RuntimeBindingLeases, resultErr error) {
	if leaser == nil {
		return nil, ErrProviderUnavailable
	}
	requirements, err := leaser.validateRequest(request)
	if err != nil {
		return nil, err
	}
	result := &RuntimeBindingLeases{
		leases:   make([]ValidatedPoolLease, 0, len(requirements)),
		evidence: make([]RuntimeBindingEvidence, 0, len(requirements)),
	}
	defer func() {
		if resultErr != nil {
			result.Release()
		}
	}()
	for _, requirement := range requirements {
		binding, err := leaser.validateRequirement(ctx, request, requirement)
		if err != nil {
			return nil, err
		}
		lease, err := leaser.pools.AcquireValidated(ctx, binding, request.Actor)
		if err != nil {
			return nil, err
		}
		persistentEvidence := lease.Evidence()
		persistentEvidence.Access = requirement.Access
		evidence := RuntimeBindingEvidence{BindingEvidence: persistentEvidence, Identity: request.Identity}
		if err := validateRuntimeBindingEvidence(binding, requirement, evidence); err != nil {
			lease.Release()
			return nil, err
		}
		result.leases = append(result.leases, lease)
		result.evidence = append(result.evidence, evidence)
	}
	return result, nil
}

// Inspect validates a candidate's exact serving identity and resolves the
// durable, non-secret binding evidence without acquiring a pool, resolving
// credentials, or registering a candidate runtime. The returned evidence is
// suitable for compatibility and binding-fingerprint checks only.
func (leaser *RuntimeBindingLeaser) Inspect(
	ctx context.Context,
	request RuntimeBindingRequest,
) ([]RuntimeBindingEvidence, error) {
	if leaser == nil {
		return nil, ErrProviderUnavailable
	}
	requirements, err := leaser.validateRequest(request)
	if err != nil {
		return nil, err
	}
	evidence := make([]RuntimeBindingEvidence, 0, len(requirements))
	for _, requirement := range requirements {
		binding, err := leaser.validateRequirement(ctx, request, requirement)
		if err != nil {
			return nil, err
		}
		// Inspection is deliberately backed by durable health evidence. A
		// pending or degraded binding must go through pool acquisition and
		// health validation before it can be used for runtime compatibility.
		if binding.Health != HealthHealthy || strings.TrimSpace(binding.ValidatedVersion) == "" || binding.LastValidatedAt.IsZero() {
			return nil, ErrIncompatibleBinding
		}
		persistentEvidence := binding.Evidence()
		persistentEvidence.Access = requirement.Access
		runtimeEvidence := RuntimeBindingEvidence{BindingEvidence: persistentEvidence, Identity: request.Identity}
		if err := validateRuntimeBindingEvidence(binding, requirement, runtimeEvidence); err != nil {
			return nil, err
		}
		evidence = append(evidence, runtimeEvidence)
	}
	return evidence, nil
}

func (leaser *RuntimeBindingLeaser) validateRequest(request RuntimeBindingRequest) ([]Requirement, error) {
	if request.Actor != strings.TrimSpace(request.Actor) {
		return nil, fmt.Errorf("%w: actor must be canonical", ErrInvalidBinding)
	}
	if _, err := ParseTargetID(request.TargetID.String()); err != nil {
		return nil, err
	}
	if request.Identity.ProjectID.String() != strings.TrimSpace(request.Identity.ProjectID.String()) ||
		request.Identity.Environment != strings.TrimSpace(request.Identity.Environment) ||
		request.Identity.GenerationID != strings.TrimSpace(request.Identity.GenerationID) {
		return nil, fmt.Errorf("%w: serving identity fields must be canonical", ErrInvalidBinding)
	}
	if request.Actor == "" || request.TargetID == "" || request.Identity.Validate() != nil {
		return nil, fmt.Errorf(
			"%w: actor, target, and exact serving identity are required",
			ErrInvalidBinding,
		)
	}
	return normalizeRuntimeRequirements(request.Requirements)
}

func (leaser *RuntimeBindingLeaser) validateRequirement(
	ctx context.Context,
	request RuntimeBindingRequest,
	requirement Requirement,
) (TargetBinding, error) {
	binding, err := leaser.bindings.Binding(
		ctx,
		BindingScope{ProjectID: request.Identity.ProjectID, Environment: request.Identity.Environment},
		request.TargetID,
		requirement.ConnectionID,
	)
	if err != nil {
		return TargetBinding{}, err
	}
	if binding.TargetID != request.TargetID || binding.Scope.ProjectID != request.Identity.ProjectID || binding.Scope.Environment != request.Identity.Environment {
		return TargetBinding{}, ErrBindingNotFound
	}
	if err := leaser.authorize(ctx, request.Actor, binding); err != nil {
		return TargetBinding{}, ErrUnauthorizedBinding
	}
	spec, exists := connectors.LookupConnection(requirement.ConnectorKind)
	if !exists || (requirement.Access != "" && requirement.Access != semanticmodel.ConnectionAccessPublic) {
		return TargetBinding{}, ErrIncompatibleBinding
	}
	if requirement.Access == semanticmodel.ConnectionAccessPublic {
		if !spec.AllowPublicAccess || binding.AuthenticationMode != AuthenticationNone {
			return TargetBinding{}, ErrIncompatibleBinding
		}
	} else if binding.AuthenticationMode == AuthenticationNone {
		return TargetBinding{}, ErrIncompatibleBinding
	}
	if _, err := binding.CompatibleEvidence(requirement, true); err != nil {
		return TargetBinding{}, err
	}
	return binding, nil
}

func (leases *RuntimeBindingLeases) Evidence() []RuntimeBindingEvidence {
	if leases == nil {
		return nil
	}
	leases.mu.RLock()
	defer leases.mu.RUnlock()
	return append([]RuntimeBindingEvidence(nil), leases.evidence...)
}

// UsePool exposes one admitted pool generation to an in-process Analytics
// consumer without exposing credential snapshots to Deployment or Runtime Host.
func (leases *RuntimeBindingLeases) UsePool(
	connectionID projectgraph.ResourceID,
	consumer func(RuntimePool) error,
) error {
	if leases == nil || consumer == nil {
		return ErrBindingNotFound
	}
	normalized, err := ParseConnectionID(connectionID.String())
	if err != nil {
		return ErrBindingNotFound
	}
	leases.mu.RLock()
	defer leases.mu.RUnlock()
	for index, evidence := range leases.evidence {
		if evidence.ConnectionID != normalized || index >= len(leases.leases) {
			continue
		}
		pool := leases.leases[index].Pool()
		if pool == nil {
			return ErrProviderUnavailable
		}
		return consumer(pool)
	}
	return ErrBindingNotFound
}

func (leases *RuntimeBindingLeases) Release() {
	if leases == nil {
		return
	}
	leases.once.Do(func() {
		leases.mu.Lock()
		defer leases.mu.Unlock()
		for index := len(leases.leases) - 1; index >= 0; index-- {
			leases.leases[index].Release()
		}
		leases.leases = nil
		leases.evidence = nil
	})
}

func (leases *RuntimeBindingLeases) Close() error {
	leases.Release()
	return nil
}

func normalizeRuntimeRequirements(requirements []Requirement) ([]Requirement, error) {
	normalized := append([]Requirement(nil), requirements...)
	for index := range normalized {
		connectionID, err := ParseConnectionID(normalized[index].ConnectionID.String())
		if err != nil {
			return nil, err
		}
		normalized[index].ConnectionID = connectionID
		if normalized[index].ConnectorKind != strings.TrimSpace(normalized[index].ConnectorKind) ||
			normalized[index].ValidatedVersion != strings.TrimSpace(normalized[index].ValidatedVersion) {
			return nil, fmt.Errorf("%w: runtime requirement identities must be canonical", ErrInvalidBinding)
		}
		if normalized[index].Access != "" && normalized[index].Access != semanticmodel.ConnectionAccessPublic {
			return nil, fmt.Errorf("%w: runtime requirement access policy is unsupported", ErrInvalidBinding)
		}
		if normalized[index].ConnectorKind == "" || normalized[index].BindingRevision < 0 {
			return nil, fmt.Errorf(
				"%w: connector kind and non-negative binding revision are required",
				ErrInvalidBinding,
			)
		}
	}
	sort.Slice(normalized, func(i, j int) bool {
		return normalized[i].ConnectionID < normalized[j].ConnectionID
	})
	for index := 1; index < len(normalized); index++ {
		if normalized[index-1].ConnectionID == normalized[index].ConnectionID {
			return nil, fmt.Errorf(
				"%w: duplicate runtime requirement %q",
				ErrInvalidBinding,
				normalized[index].ConnectionID,
			)
		}
	}
	return normalized, nil
}

func validateRuntimeBindingEvidence(
	binding TargetBinding,
	requirement Requirement,
	evidence RuntimeBindingEvidence,
) error {
	if evidence.BindingID != binding.ID ||
		evidence.TargetID != binding.TargetID ||
		evidence.ConnectionID != binding.ConnectionID ||
		evidence.ConnectorKind != binding.ConnectorKind ||
		evidence.Scope != binding.Scope ||
		evidence.BindingRevision < 1 ||
		strings.TrimSpace(evidence.ValidatedVersion) == "" ||
		evidence.Health == HealthDisabled {
		return ErrIncompatibleBinding
	}
	if err := evidence.Identity.Validate(); err != nil ||
		evidence.Identity.ProjectID != binding.Scope.ProjectID ||
		evidence.Identity.Environment != binding.Scope.Environment {
		return ErrIncompatibleBinding
	}
	if requirement.BindingRevision > 0 &&
		requirement.BindingRevision != evidence.BindingRevision {
		return ErrIncompatibleBinding
	}
	if requirement.ValidatedVersion != "" &&
		requirement.ValidatedVersion != evidence.ValidatedVersion {
		return ErrIncompatibleBinding
	}
	return nil
}
