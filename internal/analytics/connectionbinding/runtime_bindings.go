package connectionbinding

import (
	"context"
	"fmt"
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
	request.Actor = strings.TrimSpace(request.Actor)
	if _, err := ParseTargetID(request.TargetID.String()); err != nil {
		return nil, err
	}
	if request.Identity.ProjectID.String() != strings.TrimSpace(request.Identity.ProjectID.String()) ||
		request.Identity.Environment != strings.TrimSpace(request.Identity.Environment) ||
		request.Identity.GenerationID != strings.TrimSpace(request.Identity.GenerationID) {
		return nil, fmt.Errorf("%w: serving identity fields must be canonical", ErrInvalidBinding)
	}
	if request.Actor == "" || request.TargetID == "" ||
		request.Identity.Validate() != nil {
		return nil, fmt.Errorf(
			"%w: actor, target, and exact serving identity are required",
			ErrInvalidBinding,
		)
	}
	requirements, err := normalizeRuntimeRequirements(request.Requirements)
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
		binding, err := leaser.bindings.Binding(
			ctx,
			BindingScope{ProjectID: request.Identity.ProjectID, Environment: request.Identity.Environment},
			request.TargetID,
			requirement.ConnectionID,
		)
		if err != nil {
			return nil, err
		}
		if binding.TargetID != request.TargetID || binding.Scope.ProjectID != request.Identity.ProjectID || binding.Scope.Environment != request.Identity.Environment {
			return nil, ErrBindingNotFound
		}
		if err := leaser.authorize(ctx, request.Actor, binding); err != nil {
			return nil, ErrUnauthorizedBinding
		}
		if _, err := binding.CompatibleEvidence(requirement, true); err != nil {
			return nil, err
		}
		lease, err := leaser.pools.AcquireValidated(ctx, binding, request.Actor)
		if err != nil {
			return nil, err
		}
		persistentEvidence := lease.Evidence()
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
		normalized[index].ConnectorKind = strings.TrimSpace(normalized[index].ConnectorKind)
		normalized[index].ValidatedVersion = strings.TrimSpace(normalized[index].ValidatedVersion)
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
