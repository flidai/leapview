package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	appdeploymentpostgres "github.com/flidai/leapview/internal/app/deploymentpostgres"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
	"github.com/flidai/leapview/internal/release"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
	"github.com/google/uuid"
)

func TestForegroundRuntimeCredentialAuthorityUsesExactCommittedConnectionPin(t *testing.T) {
	authority, ctx, identity, resource, binding, want := newForegroundRuntimeCredentialAuthorityFixture(t)
	called := false
	originalRecheck := authority.recheck
	authority.recheck = func(ctx context.Context, principalID string, pair access.PermissionPair) error {
		called = true
		if principalID != "principal_credential" || pair.Action != access.ActionConnectionUse ||
			pair.Target.ProjectID != identity.ProjectID || pair.Target.ResourceID != binding.ConnectionID {
			t.Fatalf("request credential was not rechecked for exact connection.use: %#v", pair)
		}
		return originalRecheck(ctx, principalID, pair)
	}
	got, err := authority.ResolveRuntimeCredential(ctx, identity, resource)
	if err != nil {
		t.Fatalf("resolve exact committed credential: %v", err)
	}
	if got != want || got.Scope.Resource.ResourceID != binding.ConnectionID.String() {
		t.Fatalf("credential reference = %#v, want %#v", got, want)
	}
	lease := authority.provider.(*foregroundRuntimeAuthorityProvider).lease.(*foregroundRuntimeAuthorityLease)
	if !lease.released {
		t.Fatal("runtime lease remained held after metadata resolution")
	}
	if !called {
		t.Fatal("request credential recheck did not run")
	}
}

func TestForegroundRuntimeCredentialAuthorityRequiresBoundRequestPrincipalAndCredential(t *testing.T) {
	tests := []struct {
		name string
		edit func(context.Context, foregroundRuntimeCredentialAuthority) context.Context
	}{
		{"missing request credential", func(ctx context.Context, _ foregroundRuntimeCredentialAuthority) context.Context {
			return accessmodule.WithPrincipal(context.Background(), accessmodule.Principal{ID: "principal_credential"})
		}},
		{"missing principal", func(_ context.Context, _ foregroundRuntimeCredentialAuthority) context.Context {
			return context.Background()
		}},
		{"wrong principal", func(_ context.Context, _ foregroundRuntimeCredentialAuthority) context.Context {
			ctx := accessmodule.WithPrincipal(context.Background(), accessmodule.Principal{ID: "other_principal"})
			return foregroundAuthorityAPIContext(ctx, "principal_credential", true)
		}},
		{"token permission ceiling", func(_ context.Context, _ foregroundRuntimeCredentialAuthority) context.Context {
			ctx := accessmodule.WithPrincipal(context.Background(), accessmodule.Principal{ID: "principal_credential"})
			return foregroundAuthorityAPIContext(ctx, "principal_credential", false)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authority, ctx, identity, resource, _, _ := newForegroundRuntimeCredentialAuthorityFixture(t)
			ctx = test.edit(ctx, authority)
			if _, err := authority.ResolveRuntimeCredential(ctx, identity, resource); !errors.Is(err, credentialmodule.ErrRuntimeForbidden) {
				t.Fatalf("request authority error = %v, want forbidden", err)
			}
			provider := authority.provider.(*foregroundRuntimeAuthorityProvider)
			lease := provider.lease.(*foregroundRuntimeAuthorityLease)
			if provider.calls > 0 && !lease.released {
				t.Fatal("denied request leaked runtime lease")
			}
		})
	}
}

func TestForegroundRuntimeCredentialAuthorityRequiresExactLeasedGenerationAndSnapshot(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*foregroundRuntimeCredentialAuthority, projectgraph.ServingIdentity)
	}{
		{"lease project", func(authority *foregroundRuntimeCredentialAuthority, identity projectgraph.ServingIdentity) {
			lease := authority.provider.(*foregroundRuntimeAuthorityProvider).lease.(*foregroundRuntimeAuthorityLease)
			lease.identity.ProjectID = "project_other"
		}},
		{"lease environment", func(authority *foregroundRuntimeCredentialAuthority, _ projectgraph.ServingIdentity) {
			lease := authority.provider.(*foregroundRuntimeAuthorityProvider).lease.(*foregroundRuntimeAuthorityLease)
			lease.identity.Environment = "stage"
		}},
		{"lease generation", func(authority *foregroundRuntimeCredentialAuthority, _ projectgraph.ServingIdentity) {
			lease := authority.provider.(*foregroundRuntimeAuthorityProvider).lease.(*foregroundRuntimeAuthorityLease)
			lease.identity.GenerationID = uuid.NewString()
		}},
		{"authorization snapshot", func(authority *foregroundRuntimeCredentialAuthority, identity projectgraph.ServingIdentity) {
			lease := authority.provider.(*foregroundRuntimeAuthorityProvider).lease.(*foregroundRuntimeAuthorityLease)
			other, err := projectgraph.NewServingIdentity(identity.ProjectID, identity.Environment, uuid.NewString())
			if err != nil {
				t.Fatal(err)
			}
			lease.snapshot = foregroundAuthoritySnapshot(t, other, identity.ProjectID, "principal_credential", true)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			authority, ctx, identity, resource, _, _ := newForegroundRuntimeCredentialAuthorityFixture(t)
			test.edit(&authority, identity)
			if _, err := authority.ResolveRuntimeCredential(ctx, identity, resource); !errors.Is(err, credentialmodule.ErrRuntimeConflict) {
				t.Fatalf("exact identity mismatch error = %v, want conflict", err)
			}
			lease := authority.provider.(*foregroundRuntimeAuthorityProvider).lease.(*foregroundRuntimeAuthorityLease)
			if !lease.released {
				t.Fatal("identity mismatch leaked runtime lease")
			}
		})
	}
}

func TestForegroundRuntimeCredentialAuthorityRequiresSnapshotConnectionUseGrant(t *testing.T) {
	authority, ctx, identity, resource, _, _ := newForegroundRuntimeCredentialAuthorityFixture(t)
	lease := authority.provider.(*foregroundRuntimeAuthorityProvider).lease.(*foregroundRuntimeAuthorityLease)
	lease.snapshot = foregroundAuthoritySnapshot(t, identity, identity.ProjectID, "principal_credential", false)
	if _, err := authority.ResolveRuntimeCredential(ctx, identity, resource); !errors.Is(err, credentialmodule.ErrRuntimeForbidden) {
		t.Fatalf("missing connection.use grant error = %v, want forbidden", err)
	}
	if !lease.released {
		t.Fatal("grant denial leaked runtime lease")
	}
}

func TestForegroundRuntimeCredentialAuthorityRejectsMissingPinAndCommitment(t *testing.T) {
	t.Run("no local pin", func(t *testing.T) {
		authority, ctx, identity, resource, _, _ := newForegroundRuntimeCredentialAuthorityFixture(t)
		stub := authority.evidence.releases.(sourceSchemaProvenanceStub)
		stub.provenance.Plan.Bindings[0].CredentialVersionID = ""
		authority.evidence.releases = stub
		if _, err := authority.ResolveRuntimeCredential(ctx, identity, resource); !errors.Is(err, credentialmodule.ErrRuntimeNotFound) {
			t.Fatalf("missing local pin error = %v, want not found", err)
		}
	})

	t.Run("commitment unavailable", func(t *testing.T) {
		authority, ctx, identity, resource, _, _ := newForegroundRuntimeCredentialAuthorityFixture(t)
		authority.evidence.commitments.(*committedCredentialGenerationStub).err = errors.New("secret commitment backend detail")
		if _, err := authority.ResolveRuntimeCredential(ctx, identity, resource); !errors.Is(err, credentialmodule.ErrRuntimeNotFound) {
			t.Fatalf("missing commitment error = %v, want not found", err)
		}
	})

	t.Run("mixed local and provider pins", func(t *testing.T) {
		authority, ctx, identity, resource, _, _ := newForegroundRuntimeCredentialAuthorityFixture(t)
		stub := authority.evidence.releases.(sourceSchemaProvenanceStub)
		plan := stub.provenance.Plan
		warehouse := plan.Bindings[0]
		warehouse.CredentialVersionID = ""
		warehouse.ValidatedVersion = "provider-version-v1"
		other := warehouse
		other.BindingID = "binding_other"
		other.ConnectionID = "other_warehouse"
		other.ValidatedVersion = ""
		other.CredentialVersionID = uuid.NewString()
		plan.Bindings = []release.BindingEvidence{warehouse, other}
		gate := *plan.GateEvidence
		gate.BindingGeneration = release.BindingFingerprint(plan.Bindings)
		canonicalGate, err := gate.Canonical()
		if err != nil {
			t.Fatal(err)
		}
		plan.GateEvidence = &canonicalGate
		provenance, err := release.NewProvenance(release.ProvenanceInput{
			Artifact: stub.provenance.Artifact, Candidate: stub.provenance.Candidate,
			SourceRevision: stub.provenance.SourceRevision, Plan: plan,
		})
		if err != nil {
			t.Fatal(err)
		}
		stub.provenance = provenance
		authority.evidence.releases = stub
		authority.evidence.commitments.(*committedCredentialGenerationStub).evidence.BindingFingerprint = release.BindingFingerprint(plan.Bindings)
		if _, err := authority.ResolveRuntimeCredential(ctx, identity, resource); !errors.Is(err, credentialmodule.ErrRuntimeNotFound) {
			t.Fatalf("target without its own local pin error = %v, want not found", err)
		}
	})
}

func TestForegroundRuntimeCredentialAuthorityRejectsBindingDriftAndDisabledBindings(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*testCredentialBindingLookup)
	}{
		{"binding revision", func(lookup *testCredentialBindingLookup) { lookup.binding.Revision++ }},
		{"binding identity", func(lookup *testCredentialBindingLookup) { lookup.binding.ID = "binding_other" }},
		{"endpoint", func(lookup *testCredentialBindingLookup) { lookup.binding.Endpoint.Host = "attacker.example" }},
		{"scope", func(lookup *testCredentialBindingLookup) { lookup.binding.Scope.Environment = "stage" }},
		{"disabled", func(lookup *testCredentialBindingLookup) {
			lookup.binding.Enabled = false
			lookup.binding.Health = connectionbinding.HealthDisabled
		}},
		{"authentication mode", func(lookup *testCredentialBindingLookup) {
			lookup.binding.AuthenticationMode = connectionbinding.AuthenticationNone
			lookup.binding.CredentialReference = connectionbinding.CredentialReference{}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			authority, ctx, identity, resource, _, _ := newForegroundRuntimeCredentialAuthorityFixture(t)
			test.edit(authority.bindings.(*testCredentialBindingLookup))
			if _, err := authority.ResolveRuntimeCredential(ctx, identity, resource); err == nil {
				t.Fatal("binding drift was accepted")
			}
		})
	}
}

func TestForegroundRuntimeCredentialAuthorityFailsClosedForMissingDependenciesAndRedactsErrors(t *testing.T) {
	t.Run("typed nil provider", func(t *testing.T) {
		authority, ctx, identity, resource, _, _ := newForegroundRuntimeCredentialAuthorityFixture(t)
		var provider *foregroundRuntimeAuthorityProvider
		authority.provider = provider
		if _, err := authority.ResolveRuntimeCredential(ctx, identity, resource); !errors.Is(err, credentialmodule.ErrRuntimeUnavailable) {
			t.Fatalf("typed nil provider error = %v, want unavailable", err)
		}
	})

	t.Run("owner backend detail", func(t *testing.T) {
		authority, ctx, identity, resource, _, _ := newForegroundRuntimeCredentialAuthorityFixture(t)
		authority.owners = foregroundRuntimeAuthorityOwner{err: errors.New("secret owner backend detail")}
		_, err := authority.ResolveRuntimeCredential(ctx, identity, resource)
		if !errors.Is(err, credentialmodule.ErrRuntimeUnavailable) || strings.Contains(err.Error(), "secret") {
			t.Fatalf("owner error was not safely redacted: %v", err)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		authority, ctx, identity, resource, _, _ := newForegroundRuntimeCredentialAuthorityFixture(t)
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := authority.ResolveRuntimeCredential(canceled, identity, resource); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error = %v, want context.Canceled", err)
		}
	})

	t.Run("rechecker cancellation", func(t *testing.T) {
		authority, ctx, identity, resource, _, _ := newForegroundRuntimeCredentialAuthorityFixture(t)
		authority.recheck = func(context.Context, string, access.PermissionPair) error { return context.Canceled }
		if _, err := authority.ResolveRuntimeCredential(ctx, identity, resource); !errors.Is(err, context.Canceled) {
			t.Fatalf("rechecker cancellation error = %v, want context.Canceled", err)
		}
		lease := authority.provider.(*foregroundRuntimeAuthorityProvider).lease.(*foregroundRuntimeAuthorityLease)
		if !lease.released {
			t.Fatal("rechecker cancellation leaked runtime lease")
		}
	})
}

func TestForegroundRuntimeCredentialAuthorityReleasesPartialLeaseOnAcquireError(t *testing.T) {
	authority, ctx, identity, resource, _, _ := newForegroundRuntimeCredentialAuthorityFixture(t)
	provider := authority.provider.(*foregroundRuntimeAuthorityProvider)
	provider.err = errors.New("secret acquire diagnostic")
	if _, err := authority.ResolveRuntimeCredential(ctx, identity, resource); !errors.Is(err, credentialmodule.ErrRuntimeUnavailable) {
		t.Fatalf("acquire error = %v, want unavailable", err)
	}
	lease := provider.lease.(*foregroundRuntimeAuthorityLease)
	if !lease.released {
		t.Fatal("partial lease returned with error was not released")
	}
}

// newForegroundRuntimeCredentialAuthorityFixture is shared with the resolver
// integration test. It constructs one exact serving pin and the corresponding
// request authority; callers can replace its private ports to exercise races.
func newForegroundRuntimeCredentialAuthorityFixture(
	t testing.TB,
) (foregroundRuntimeCredentialAuthority, context.Context, projectgraph.ServingIdentity, credentialmodule.RuntimeResource, connectionbinding.TargetBinding, credentialmodule.RuntimeCredentialReference) {
	t.Helper()
	const (
		instanceID  = "instance_credential"
		principalID = "principal_credential"
		ownerID     = "customer_credential"
	)
	identity, err := projectgraph.NewServingIdentity("project_credential", "prod", uuid.Must(uuid.NewV7()).String())
	if err != nil {
		t.Fatal(err)
	}
	binding, err := connectionbinding.NewTargetBinding(connectionbinding.TargetBindingInput{
		ID: "binding_warehouse", TargetID: connectionbinding.TargetID(instanceID), ConnectionID: "warehouse",
		ConnectorKind: "postgres", AuthenticationMode: connectionbinding.AuthenticationExternalBundle,
		Scope:               connectionbinding.BindingScope{ProjectID: identity.ProjectID, Environment: identity.Environment},
		Endpoint:            connectionbinding.EndpointConfig{Host: "warehouse.internal", Port: 5432, Database: "analytics", TLSMode: "verify-full", SourceIdentity: "warehouse_source"},
		CredentialReference: connectionbinding.CredentialReference{ProjectID: "credential_secret_project", Environment: identity.Environment, SecretPath: "/leapview/warehouse", SecretKey: "password"},
		Enabled:             true, Now: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	versionID := uuid.NewString()
	resource := credentialmodule.RuntimeResource{
		ScopeKind: "connection", TargetID: instanceID, ProjectID: identity.ProjectID.String(),
		Environment: identity.Environment, ResourceID: binding.ConnectionID.String(),
	}
	want := credentialmodule.RuntimeCredentialReference{VersionID: versionID, Scope: credentialmodule.RuntimeScope{
		Resource: resource, OwnerID: ownerID, Purpose: "connection-authentication", Provider: "postgres",
		Destination: binding.Evidence().EndpointConfigHash,
	}}
	planBinding := release.BindingEvidence{
		BindingID: binding.ID.String(), ConnectionID: binding.ConnectionID.String(), ConnectorKind: "postgres",
		Revision: binding.Revision, CredentialVersionID: versionID, EndpointConfigHash: binding.Evidence().EndpointConfigHash,
	}
	candidate := release.CandidateProvenance{ID: uuid.NewString(), Revision: 4, OwnerID: "release_builder"}
	artifact := release.ProjectArtifactProvenance{
		SourceDigest: activeResultIdentityDigest('a'), ProjectDigest: activeResultIdentityDigest('b'),
		ContentDigest: activeResultIdentityDigest('c'), CompilerVersion: "compiler:v1", SchemaVersion: 1,
	}
	gate, err := (release.GateEvidence{
		Version: 1, CandidateID: candidate.ID, SourceDigest: artifact.SourceDigest,
		BindingGeneration: release.BindingFingerprint([]release.BindingEvidence{planBinding}), RuntimeVersion: "runtime:v1", DuckDBVersion: "duckdb:test",
		Outcome: release.GateSuccess, EvaluatedAt: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC),
		Bounds: release.GateBounds{MaxRows: 10, MaxQueries: 1, MaxMillis: 1000},
	}).Canonical()
	if err != nil {
		t.Fatal(err)
	}
	provenance, err := release.NewProvenance(release.ProvenanceInput{
		Artifact: artifact, Candidate: candidate,
		Plan: release.GenerationPlanProvenance{
			Identity: identity, TargetID: instanceID, RuntimeVersion: "runtime:v1",
			PolicyDigest: activeResultIdentityDigest('d'), PolicyRevision: 1, AuthorizationDigest: activeResultIdentityDigest('f'),
			DataRevision: "sources:1", DataMode: release.GenerationDataRefreshSources, Bindings: []release.BindingEvidence{planBinding}, GateEvidence: &gate,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	commitments := &committedCredentialGenerationStub{evidence: appdeploymentpostgres.CommittedGenerationEvidence{
		TargetID: instanceID, Identity: identity, PublicationID: uuid.NewString(), SnapshotSealID: uuid.NewString(),
		CandidateID: candidate.ID, CandidateRevision: candidate.Revision,
		ServingArtifactDigest: artifact.ContentDigest, BindingFingerprint: release.BindingFingerprint([]release.BindingEvidence{planBinding}),
	}}
	evidence := activeConnectionEvidenceSource{
		releases: sourceSchemaProvenanceStub{provenance: provenance}, commitments: commitments,
		targetID: instanceID, environment: identity.Environment,
	}
	snapshot := foregroundAuthoritySnapshot(t, identity, identity.ProjectID, principalID, true)
	lease := &foregroundRuntimeAuthorityLease{identity: identity, snapshot: snapshot}
	provider := &foregroundRuntimeAuthorityProvider{lease: lease}
	bindings := &testCredentialBindingLookup{binding: binding}
	ctx := accessmodule.WithPrincipal(context.Background(), accessmodule.Principal{ID: principalID, Kind: access.PrincipalKindServicePrincipal})
	ctx = foregroundAuthorityAPIContext(ctx, principalID, true)
	var subject access.SubjectRef
	subject, err = access.NewSubjectRef(access.SubjectKindPrincipal, principalID)
	if err != nil {
		t.Fatal(err)
	}
	authority := foregroundRuntimeCredentialAuthority{
		instanceID: instanceID, environment: identity.Environment, principalID: principalID,
		provider: provider, evidence: evidence, owners: foregroundRuntimeAuthorityOwner{owner: ownerID}, bindings: bindings,
		subjects: func(context.Context, string) ([]access.SubjectRef, error) { return []access.SubjectRef{subject}, nil },
		recheck: func(_ context.Context, gotPrincipal string, pair access.PermissionPair) error {
			authorityPrincipalAndPair := gotPrincipal == principalID && pair.Action == access.ActionConnectionUse &&
				pair.Target.ProjectID == identity.ProjectID && pair.Target.ResourceID == binding.ConnectionID
			if !authorityPrincipalAndPair {
				return access.ErrForbidden
			}
			return nil
		},
	}
	return authority, ctx, identity, resource, binding, want
}

func foregroundAuthorityAPIContext(ctx context.Context, principalID string, allow bool) context.Context {
	identity, err := projectgraph.NewServingIdentity("project_credential", "prod", "generation_unused")
	if err != nil {
		panic(err)
	}
	resource, err := access.NewResourceRef("warehouse", projectgraph.KindConnection)
	if err != nil {
		panic(err)
	}
	pair, err := access.NewExactPermissionPair(access.ActionConnectionUse, identity.ProjectID, resource)
	if err != nil {
		panic(err)
	}
	var permissions []access.PermissionPair
	if allow {
		permissions = []access.PermissionPair{pair}
	}
	return accessmodule.WithAPICredential(ctx, access.APICredential{Token: access.APIToken{
		ID: "token_credential", PrincipalID: principalID, TokenFingerprint: "fingerprint_credential",
		PermissionProfile: access.PermissionCatalogProfile, Permissions: permissions,
	}, Principal: access.Principal{ID: principalID}})
}

func foregroundAuthoritySnapshot(t testing.TB, identity projectgraph.ServingIdentity, projectID projectgraph.ResourceID, principalID string, allow bool) accesssnapshot.AuthorizationSnapshot {
	t.Helper()
	resources := []projectgraph.Resource{{ID: "warehouse", Kind: projectgraph.KindConnection, Name: "warehouse"}}
	graph, err := projectgraph.NewProjectGraph(resources, nil)
	if err != nil {
		t.Fatal(err)
	}
	var grants []accesssnapshot.Grant
	if allow {
		subject, err := access.NewSubjectRef(access.SubjectKindPrincipal, principalID)
		if err != nil {
			t.Fatal(err)
		}
		resource, err := access.NewResourceRef("warehouse", projectgraph.KindConnection)
		if err != nil {
			t.Fatal(err)
		}
		pair, err := access.NewExactPermissionPair(access.ActionConnectionUse, projectID, resource)
		if err != nil {
			t.Fatal(err)
		}
		grant, err := accesssnapshot.NewTypedGrant("grant_connection_use", "connection_use", subject, []access.PermissionPair{pair})
		if err != nil {
			t.Fatal(err)
		}
		grants = []accesssnapshot.Grant{grant}
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, grants, nil)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

type foregroundRuntimeAuthorityProvider struct {
	lease runtimehostmodule.Lease
	err   error
	calls int
}

func (provider *foregroundRuntimeAuthorityProvider) Acquire(context.Context) (runtimehostmodule.Lease, error) {
	provider.calls++
	return provider.lease, provider.err
}

type foregroundRuntimeAuthorityLease struct {
	identity projectgraph.ServingIdentity
	snapshot accesssnapshot.AuthorizationSnapshot
	released bool
}

func (lease *foregroundRuntimeAuthorityLease) Runtime() projectruntime.Runtime { return nil }
func (lease *foregroundRuntimeAuthorityLease) Identity() projectgraph.ServingIdentity {
	return lease.identity
}
func (lease *foregroundRuntimeAuthorityLease) Release() { lease.released = true }
func (lease *foregroundRuntimeAuthorityLease) AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot {
	return lease.snapshot
}

type foregroundRuntimeAuthorityOwner struct {
	owner string
	err   error
}

func (owner foregroundRuntimeAuthorityOwner) CustomerOwner(context.Context) (string, error) {
	return owner.owner, owner.err
}

var _ runtimehostmodule.Provider = (*foregroundRuntimeAuthorityProvider)(nil)
var _ runtimehostmodule.Lease = (*foregroundRuntimeAuthorityLease)(nil)
