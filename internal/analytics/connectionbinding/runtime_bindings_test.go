package connectionbinding

import (
	"context"
	"errors"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

func TestRuntimeBindingLeaserAcquiresDeterministicValidatedEvidence(t *testing.T) {
	warehouse := validTargetBinding(t)
	reporting := warehouse
	reporting.ID = "binding_prod_reporting"
	reporting.ConnectionID = "reporting"
	repository := &runtimeBindingCatalog{
		bindings: map[projectgraph.ResourceID]TargetBinding{
			warehouse.ConnectionID: warehouse,
			reporting.ConnectionID: reporting,
		},
	}
	directory := &recordingValidatedPoolDirectory{}
	var authorized []projectgraph.ResourceID
	leaser, err := NewRuntimeBindingLeaser(RuntimeBindingLeaserConfig{
		Bindings: repository,
		Pools:    directory,
		Authorize: func(_ context.Context, actor string, binding TargetBinding) error {
			if actor != "principal:author_1" {
				t.Fatalf("authorization actor = %q", actor)
			}
			authorized = append(authorized, binding.ConnectionID)
			return nil
		},
	})
	require.NoError(t, err)

	leases, err := leaser.Acquire(t.Context(), RuntimeBindingRequest{
		Actor: "principal:author_1", Identity: servingIdentity(warehouse.Scope.ProjectID.String(), warehouse.Scope.Environment, "generation-1"),
		TargetID: warehouse.TargetID,
		Requirements: []Requirement{
			{ConnectionID: reporting.ConnectionID, ConnectorKind: reporting.ConnectorKind},
			{ConnectionID: warehouse.ConnectionID, ConnectorKind: warehouse.ConnectorKind},
		},
	})
	require.NoError(t, err)
	evidence := leases.Evidence()
	if len(evidence) != 2 ||
		evidence[0].ConnectionID != reporting.ConnectionID ||
		evidence[1].ConnectionID != warehouse.ConnectionID {
		t.Fatalf("deterministic evidence = %#v", evidence)
	}
	if len(authorized) != 2 || len(directory.acquired) != 2 {
		t.Fatalf("authorized=%#v acquired=%#v", authorized, directory.acquired)
	}
	leases.Release()
	leases.Release()
	for _, lease := range directory.leases {
		if lease.releases != 1 {
			t.Fatalf("lease releases = %d, want idempotent release", lease.releases)
		}
	}
}

func TestRuntimeBindingLeaserAllowsCredentialFreeCandidateAndReleasesPartialFailure(t *testing.T) {
	warehouse := validTargetBinding(t)
	reporting := warehouse
	reporting.ID = "binding_prod_reporting"
	reporting.ConnectionID = "reporting"
	repository := &runtimeBindingCatalog{
		bindings: map[projectgraph.ResourceID]TargetBinding{
			warehouse.ConnectionID: warehouse,
			reporting.ConnectionID: reporting,
		},
	}
	directory := &recordingValidatedPoolDirectory{failOn: warehouse.ConnectionID}
	leaser, err := NewRuntimeBindingLeaser(RuntimeBindingLeaserConfig{
		Bindings: repository, Pools: directory,
		Authorize: func(context.Context, string, TargetBinding) error { return nil },
	})
	require.NoError(t, err)

	credentialFree, err := leaser.Acquire(t.Context(), RuntimeBindingRequest{
		Actor: "principal:author_1", Identity: servingIdentity(warehouse.Scope.ProjectID.String(), warehouse.Scope.Environment, "generation-1"), TargetID: warehouse.TargetID,
	})
	require.NoError(t, err)
	if len(credentialFree.Evidence()) != 0 || len(directory.acquired) != 0 {
		t.Fatalf("credential-free acquisition touched target pools: %#v", directory.acquired)
	}
	credentialFree.Release()

	_, err = leaser.Acquire(t.Context(), RuntimeBindingRequest{
		Actor: "principal:author_1", Identity: servingIdentity(warehouse.Scope.ProjectID.String(), warehouse.Scope.Environment, "generation-1"), TargetID: warehouse.TargetID,
		Requirements: []Requirement{
			{ConnectionID: reporting.ConnectionID, ConnectorKind: reporting.ConnectorKind},
			{ConnectionID: warehouse.ConnectionID, ConnectorKind: warehouse.ConnectorKind},
		},
	})
	if !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("partial Acquire() error = %v", err)
	}
	if len(directory.leases) != 1 || directory.leases[0].releases != 1 {
		t.Fatalf("partial leases = %#v, want released first lease", directory.leases)
	}
}

func TestRuntimeBindingLeaserFailsClosedBeforePoolAcquisition(t *testing.T) {
	binding := validTargetBinding(t)
	for name, authorize := range map[string]RuntimeBindingAuthorizer{
		"unauthorized": func(context.Context, string, TargetBinding) error {
			return ErrUnauthorizedBinding
		},
		"revoked": func(context.Context, string, TargetBinding) error {
			return errors.New("policy changed")
		},
	} {
		t.Run(name, func(t *testing.T) {
			directory := &recordingValidatedPoolDirectory{}
			leaser, err := NewRuntimeBindingLeaser(RuntimeBindingLeaserConfig{
				Bindings: &runtimeBindingCatalog{
					bindings: map[projectgraph.ResourceID]TargetBinding{
						binding.ConnectionID: binding,
					},
				},
				Pools: directory, Authorize: authorize,
			})
			require.NoError(t, err)
			_, err = leaser.Acquire(t.Context(), RuntimeBindingRequest{
				Actor: "principal:author_1", Identity: servingIdentity(binding.Scope.ProjectID.String(), binding.Scope.Environment, "generation-1"), TargetID: binding.TargetID,
				Requirements: []Requirement{{
					ConnectionID:  binding.ConnectionID,
					ConnectorKind: binding.ConnectorKind,
				}},
			})
			if !errors.Is(err, ErrUnauthorizedBinding) {
				t.Fatalf("Acquire() error = %v, want unauthorized", err)
			}
			if len(directory.acquired) != 0 {
				t.Fatalf("unauthorized request acquired pools: %#v", directory.acquired)
			}
		})
	}
}

func TestRuntimeBindingLeasesExposeOnlyTheValidatedLogicalPool(t *testing.T) {
	binding := validTargetBinding(t)
	pool := &recordingRuntimePool{}
	directory := &recordingValidatedPoolDirectory{pool: pool}
	leaser, err := NewRuntimeBindingLeaser(RuntimeBindingLeaserConfig{
		Bindings: &runtimeBindingCatalog{
			bindings: map[projectgraph.ResourceID]TargetBinding{
				binding.ConnectionID: binding,
			},
		},
		Pools: directory,
		Authorize: func(context.Context, string, TargetBinding) error {
			return nil
		},
	})
	require.NoError(t, err)
	leases, err := leaser.Acquire(t.Context(), RuntimeBindingRequest{
		Actor: "author_1", Identity: servingIdentity(binding.Scope.ProjectID.String(), binding.Scope.Environment, "generation-1"), TargetID: binding.TargetID,
		Requirements: []Requirement{{
			ConnectionID:  binding.ConnectionID,
			ConnectorKind: binding.ConnectorKind,
		}},
	})
	require.NoError(t, err)
	defer leases.Release()

	var used RuntimePool
	if err := leases.UsePool(binding.ConnectionID, func(candidate RuntimePool) error {
		used = candidate
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if used != pool {
		t.Fatalf("UsePool() pool = %T %p, want validated pool %p", used, used, pool)
	}
	if err := leases.UsePool("reporting", func(RuntimePool) error {
		t.Fatal("consumer called for an unleased logical connection")
		return nil
	}); !errors.Is(err, ErrBindingNotFound) {
		t.Fatalf("UsePool() error = %v, want binding not found", err)
	}
}

type runtimeBindingCatalog struct {
	bindings map[projectgraph.ResourceID]TargetBinding
}

func (*runtimeBindingCatalog) Create(context.Context, TargetBinding) error { return nil }

func (catalog *runtimeBindingCatalog) Binding(
	_ context.Context,
	_ BindingScope,
	_ TargetID,
	connectionID projectgraph.ResourceID,
) (TargetBinding, error) {
	binding, ok := catalog.bindings[connectionID]
	if !ok {
		return TargetBinding{}, ErrBindingNotFound
	}
	return binding, nil
}

func (catalog *runtimeBindingCatalog) List(
	context.Context,
	BindingScope,
	TargetID,
) ([]TargetBinding, error) {
	return nil, nil
}

func (*runtimeBindingCatalog) Save(
	context.Context,
	TargetBinding,
	int64,
) (TargetBinding, error) {
	return TargetBinding{}, errors.New("unused")
}

type recordingValidatedPoolDirectory struct {
	acquired []projectgraph.ResourceID
	leases   []*recordingValidatedPoolLease
	failOn   projectgraph.ResourceID
	pool     RuntimePool
}

func (directory *recordingValidatedPoolDirectory) AcquireValidated(
	_ context.Context,
	binding TargetBinding,
	_ string,
) (ValidatedPoolLease, error) {
	directory.acquired = append(directory.acquired, binding.ConnectionID)
	if binding.ConnectionID == directory.failOn {
		return nil, ErrProviderUnavailable
	}
	evidence := binding.Evidence()
	evidence.ValidatedVersion = "provider-v1"
	evidence.Health = HealthHealthy
	pool := directory.pool
	if pool == nil {
		pool = &recordingRuntimePool{}
	}
	lease := &recordingValidatedPoolLease{evidence: evidence, pool: pool}
	directory.leases = append(directory.leases, lease)
	return lease, nil
}

type recordingValidatedPoolLease struct {
	evidence BindingEvidence
	releases int
	pool     RuntimePool
}

func (lease *recordingValidatedPoolLease) Pool() RuntimePool { return lease.pool }
func (lease *recordingValidatedPoolLease) Evidence() BindingEvidence {
	return lease.evidence
}
func (lease *recordingValidatedPoolLease) Release() {
	lease.releases++
}
