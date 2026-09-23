package connectionbinding

import (
	"context"
	"errors"
	"testing"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
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

func TestRuntimeBindingLeaserInspectsDurableEvidenceWithoutPoolAcquisition(t *testing.T) {
	binding := validTargetBinding(t)
	validated, err := binding.MarkValidated("provider-v1", binding.UpdatedAt.Add(time.Minute))
	require.NoError(t, err)
	directory := &recordingValidatedPoolDirectory{}
	leaser, err := NewRuntimeBindingLeaser(RuntimeBindingLeaserConfig{
		Bindings: &runtimeBindingCatalog{bindings: map[projectgraph.ResourceID]TargetBinding{binding.ConnectionID: validated}},
		Pools:    directory,
		Authorize: func(_ context.Context, actor string, got TargetBinding) error {
			if actor != "principal:author_1" || got.ID != binding.ID {
				t.Fatalf("authorization request = %q, %#v", actor, got)
			}
			return nil
		},
	})
	require.NoError(t, err)

	got, err := leaser.Inspect(t.Context(), RuntimeBindingRequest{
		Actor: "principal:author_1", Identity: servingIdentity(binding.Scope.ProjectID.String(), binding.Scope.Environment, "generation-1"), TargetID: binding.TargetID,
		Requirements: []Requirement{{ConnectionID: binding.ConnectionID, ConnectorKind: binding.ConnectorKind}},
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, validated.ID, got[0].BindingID)
	require.Equal(t, validated.Revision, got[0].BindingRevision)
	require.Equal(t, validated.ValidatedVersion, got[0].ValidatedVersion)
	require.Equal(t, validated.Evidence().EndpointConfigHash, got[0].EndpointConfigHash)
	require.Equal(t, HealthHealthy, got[0].Health)
	require.Equal(t, "generation-1", got[0].Identity.GenerationID)
	require.Empty(t, directory.acquired)
}

func TestRuntimeBindingLeaserProfileApplicationAdmissionGatesAcquireAndInspect(t *testing.T) {
	application := profileApplicationFixture(t, ProfileApplicationApplied)
	store := newMemoryProfileApplicationStore()
	saveProfileApplication(t, store, application)
	checker, err := NewProfileApplicationAdmissionChecker(store)
	require.NoError(t, err)
	binding := profileAdmissionTargetBinding(t)

	newLeaser := func(directory *recordingValidatedPoolDirectory) *RuntimeBindingLeaser {
		leaser, err := NewRuntimeBindingLeaser(RuntimeBindingLeaserConfig{
			Bindings: &runtimeBindingCatalog{bindings: map[projectgraph.ResourceID]TargetBinding{binding.ConnectionID: binding}},
			Pools:    directory, Authorize: func(context.Context, string, TargetBinding) error { return nil },
			ProfileApplicationAdmission: checker,
			CheckoutID:                  "checkout-1", RuntimeID: "runtime-1", ProfileName: "local",
			GraphDigest: application.GraphDigest, ProfileDigest: application.ProfileDigest,
		})
		require.NoError(t, err)
		return leaser
	}
	request := RuntimeBindingRequest{
		Actor: "author-1", Identity: servingIdentity("project-1", "dev", "generation-1"), TargetID: binding.TargetID,
		Requirements: []Requirement{{ConnectionID: binding.ConnectionID, ConnectorKind: binding.ConnectorKind}},
	}

	directory := &recordingValidatedPoolDirectory{}
	leaser := newLeaser(directory)
	leases, err := leaser.Acquire(t.Context(), request)
	require.NoError(t, err)
	require.Len(t, leases.Evidence(), 1)
	require.Len(t, directory.acquired, 1)
	leases.Release()

	directory = &recordingValidatedPoolDirectory{}
	leaser = newLeaser(directory)
	evidence, err := leaser.Inspect(t.Context(), request)
	require.NoError(t, err)
	require.Len(t, evidence, 1)
	require.Empty(t, directory.acquired)
}

func TestRuntimeBindingLeaserProfileApplicationAdmissionRejectsProfileAndEvidenceDriftBeforeAcquire(t *testing.T) {
	application := profileApplicationFixture(t, ProfileApplicationApplied)
	store := newMemoryProfileApplicationStore()
	saveProfileApplication(t, store, application)
	checker, err := NewProfileApplicationAdmissionChecker(store)
	require.NoError(t, err)
	binding := profileAdmissionTargetBinding(t)
	request := RuntimeBindingRequest{
		Actor: "author-1", Identity: servingIdentity("project-1", "dev", "generation-1"), TargetID: binding.TargetID,
		Requirements: []Requirement{{ConnectionID: binding.ConnectionID, ConnectorKind: binding.ConnectorKind}},
	}

	tests := []struct {
		name       string
		configure  func(*RuntimeBindingLeaserConfig)
		newBinding func(TargetBinding) TargetBinding
	}{
		{name: "profile", configure: func(config *RuntimeBindingLeaserConfig) { config.ProfileDigest = profileApplicationDigest('e') }},
		{name: "evidence", newBinding: func(binding TargetBinding) TargetBinding { binding.ValidatedVersion = "provider-drift"; return binding }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := &recordingValidatedPoolDirectory{}
			currentBinding := binding
			if test.newBinding != nil {
				currentBinding = test.newBinding(currentBinding)
			}
			config := RuntimeBindingLeaserConfig{
				Bindings: &runtimeBindingCatalog{bindings: map[projectgraph.ResourceID]TargetBinding{currentBinding.ConnectionID: currentBinding}},
				Pools:    directory, Authorize: func(context.Context, string, TargetBinding) error { return nil },
				ProfileApplicationAdmission: checker,
				CheckoutID:                  "checkout-1", RuntimeID: "runtime-1", ProfileName: "local",
				GraphDigest: application.GraphDigest, ProfileDigest: application.ProfileDigest,
			}
			if test.configure != nil {
				test.configure(&config)
			}
			leaser, err := NewRuntimeBindingLeaser(config)
			require.NoError(t, err)
			_, err = leaser.Acquire(t.Context(), request)
			require.ErrorIs(t, err, ErrProfileApplicationNotAdmitted)
			require.Empty(t, directory.acquired)

			_, err = leaser.Inspect(t.Context(), request)
			require.ErrorIs(t, err, ErrProfileApplicationNotAdmitted)
			require.Empty(t, directory.acquired)
		})
	}
}

func TestRuntimeBindingLeaserProductionBehaviorIsUnchangedWithoutProfileAdmission(t *testing.T) {
	binding := profileAdmissionTargetBinding(t)
	directory := &recordingValidatedPoolDirectory{}
	leaser, err := NewRuntimeBindingLeaser(RuntimeBindingLeaserConfig{
		Bindings: &runtimeBindingCatalog{bindings: map[projectgraph.ResourceID]TargetBinding{binding.ConnectionID: binding}},
		Pools:    directory, Authorize: func(context.Context, string, TargetBinding) error { return nil },
		// Local-only identities are ignored when the optional checker is nil.
		CheckoutID: "not-configured", RuntimeID: "not-configured", ProfileName: "not-configured",
		GraphDigest: "not-a-digest", ProfileDigest: "not-a-digest",
	})
	require.NoError(t, err)
	leases, err := leaser.Acquire(t.Context(), RuntimeBindingRequest{
		Actor: "author-1", Identity: servingIdentity("project-1", "dev", "generation-1"), TargetID: binding.TargetID,
		Requirements: []Requirement{{ConnectionID: binding.ConnectionID, ConnectorKind: binding.ConnectorKind}},
	})
	require.NoError(t, err)
	require.Len(t, directory.acquired, 1)
	leases.Release()
}

func profileAdmissionTargetBinding(t *testing.T) TargetBinding {
	t.Helper()
	binding, err := NewTargetBinding(TargetBindingInput{
		ID: "binding-a", TargetID: "target-local", ConnectionID: "connection-a", ConnectorKind: "postgres",
		AuthenticationMode: AuthenticationExternalBundle, Scope: BindingScope{ProjectID: "project-1", Environment: "dev"},
		Endpoint:            EndpointConfig{Host: "127.0.0.1", Port: 5432, Database: "analytics", TLSMode: "disable"},
		CredentialReference: CredentialReference{ProjectID: "project-1", Environment: "dev", SecretPath: "/", SecretKey: "LEAPVIEW_DEV_CONNECTION_A"},
		Enabled:             true, Now: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	binding, err = binding.MarkValidated("provider-a", binding.UpdatedAt.Add(time.Minute))
	require.NoError(t, err)
	binding.Revision = 4
	return binding
}

func TestRuntimeBindingLeaserInspectFailsClosedForAuthorizationScopeAndCompatibility(t *testing.T) {
	binding := validTargetBinding(t)
	validated, err := binding.MarkValidated("provider-v1", binding.UpdatedAt.Add(time.Minute))
	require.NoError(t, err)
	newLeaser := func(authorize RuntimeBindingAuthorizer) *RuntimeBindingLeaser {
		leaser, err := NewRuntimeBindingLeaser(RuntimeBindingLeaserConfig{
			Bindings:  &runtimeBindingCatalog{bindings: map[projectgraph.ResourceID]TargetBinding{binding.ConnectionID: validated}},
			Pools:     &recordingValidatedPoolDirectory{},
			Authorize: authorize,
		})
		require.NoError(t, err)
		return leaser
	}
	request := RuntimeBindingRequest{
		Actor: "principal:author_1", Identity: servingIdentity(binding.Scope.ProjectID.String(), binding.Scope.Environment, "generation-1"), TargetID: binding.TargetID,
		Requirements: []Requirement{{ConnectionID: binding.ConnectionID, ConnectorKind: binding.ConnectorKind}},
	}
	_, err = newLeaser(func(context.Context, string, TargetBinding) error { return ErrUnauthorizedBinding }).Inspect(t.Context(), request)
	require.ErrorIs(t, err, ErrUnauthorizedBinding)

	scopeRequest := request
	scopeRequest.Identity = servingIdentity("other_project", binding.Scope.Environment, "generation-1")
	_, err = newLeaser(func(context.Context, string, TargetBinding) error { return nil }).Inspect(t.Context(), scopeRequest)
	require.ErrorIs(t, err, ErrBindingNotFound)

	compatibilityRequest := request
	compatibilityRequest.Requirements = []Requirement{{ConnectionID: binding.ConnectionID, ConnectorKind: "unknown"}}
	_, err = newLeaser(func(context.Context, string, TargetBinding) error { return nil }).Inspect(t.Context(), compatibilityRequest)
	require.ErrorIs(t, err, ErrIncompatibleBinding)

	pendingLeaser, err := NewRuntimeBindingLeaser(RuntimeBindingLeaserConfig{
		Bindings:  &runtimeBindingCatalog{bindings: map[projectgraph.ResourceID]TargetBinding{binding.ConnectionID: binding}},
		Pools:     &recordingValidatedPoolDirectory{},
		Authorize: func(context.Context, string, TargetBinding) error { return nil },
	})
	require.NoError(t, err)
	_, err = pendingLeaser.Inspect(t.Context(), request)
	require.ErrorIs(t, err, ErrIncompatibleBinding)
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

func TestRuntimeBindingLeaserPublicRequiresNoAuthBinding(t *testing.T) {
	binding, err := NewTargetBinding(TargetBindingInput{
		ID: "binding_public_s3", TargetID: "target_1", ConnectionID: "public_files", ConnectorKind: "s3",
		AuthenticationMode: AuthenticationNone,
		Scope:              BindingScope{ProjectID: "project_1", Environment: "prod"},
		Endpoint:           EndpointConfig{ObjectScope: "s3://public/"}, Enabled: true, Now: time.Now(),
	})
	require.NoError(t, err)
	directory := &recordingValidatedPoolDirectory{}
	leaser, err := NewRuntimeBindingLeaser(RuntimeBindingLeaserConfig{
		Bindings: &runtimeBindingCatalog{bindings: map[projectgraph.ResourceID]TargetBinding{binding.ConnectionID: binding}},
		Pools:    directory, Authorize: func(context.Context, string, TargetBinding) error { return nil },
	})
	require.NoError(t, err)
	leases, err := leaser.Acquire(t.Context(), RuntimeBindingRequest{
		Actor: "author_1", Identity: servingIdentity("project_1", "prod", "generation-1"), TargetID: binding.TargetID,
		Requirements: []Requirement{{ConnectionID: binding.ConnectionID, ConnectorKind: "s3", Access: semanticmodel.ConnectionAccessPublic}},
	})
	require.NoError(t, err)
	defer leases.Release()
	if len(directory.acquired) != 1 || leases.Evidence()[0].Access != semanticmodel.ConnectionAccessPublic || leases.Evidence()[0].ValidatedVersion != NoAuthProviderVersion {
		t.Fatalf("public binding evidence/acquisition = %#v, %#v", directory.acquired, leases.Evidence())
	}
	_, err = leaser.Acquire(t.Context(), RuntimeBindingRequest{
		Actor: "author_1", Identity: servingIdentity("project_1", "prod", "generation-2"), TargetID: binding.TargetID,
		Requirements: []Requirement{{ConnectionID: binding.ConnectionID, ConnectorKind: "s3"}},
	})
	if !errors.Is(err, ErrIncompatibleBinding) {
		t.Fatalf("private request with no-auth binding error = %v", err)
	}
	workload := binding
	workload.ID = "binding_workload_s3"
	workload.ConnectionID = "workload_files"
	workload.AuthenticationMode = AuthenticationWorkload
	workload.CredentialReference = CredentialReference{}
	workload.Revision++
	workload.UpdatedAt = workload.UpdatedAt.Add(time.Second)
	workload.LastValidatedAt = time.Time{}
	workload.ValidatedVersion = ""
	workload.Health = HealthPending
	workloadCatalog := &runtimeBindingCatalog{bindings: map[projectgraph.ResourceID]TargetBinding{workload.ConnectionID: workload}}
	workloadLeaser, err := NewRuntimeBindingLeaser(RuntimeBindingLeaserConfig{
		Bindings: workloadCatalog, Pools: &recordingValidatedPoolDirectory{},
		Authorize: func(context.Context, string, TargetBinding) error { return nil },
	})
	require.NoError(t, err)
	workloadLeases, err := workloadLeaser.Acquire(t.Context(), RuntimeBindingRequest{
		Actor: "author_1", Identity: servingIdentity("project_1", "prod", "generation-3"), TargetID: workload.TargetID,
		Requirements: []Requirement{{ConnectionID: workload.ConnectionID, ConnectorKind: workload.ConnectorKind}},
	})
	require.NoError(t, err)
	defer workloadLeases.Release()
	if got := workloadLeases.Evidence(); len(got) != 1 || got[0].Access != "" {
		t.Fatalf("workload binding evidence = %#v, want private access policy", got)
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
	if binding.AuthenticationMode == AuthenticationNone {
		evidence.ValidatedVersion = NoAuthProviderVersion
	} else {
		evidence.ValidatedVersion = "provider-v1"
	}
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
