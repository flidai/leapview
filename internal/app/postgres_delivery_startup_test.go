package app

import (
	"context"
	"errors"
	"testing"

	physicalpool "github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/deployment"
	nativepostgres "github.com/flidai/leapview/internal/deployment/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/servingstate"
)

const (
	startupTarget      = "target"
	startupProject     = "project"
	startupEnvironment = servingstate.Environment("prod")
	startupGeneration  = "generation"
	startupPublication = "publication"
	startupCandidate   = "candidate"
	startupPlan        = "plan"
	startupSeal        = "seal"
)

type postgresDeliveryStartupAuthorityFake struct {
	target         nativepostgres.DeliveryTarget
	targetErr      error
	generation     nativepostgres.DeliveryGeneration
	generationErr  error
	publication    nativepostgres.DeliveryPublication
	publicationErr error
	seal           nativepostgres.SnapshotSeal
	sealErr        error
}

func (f *postgresDeliveryStartupAuthorityFake) Target(context.Context, string) (nativepostgres.DeliveryTarget, error) {
	return f.target, f.targetErr
}
func (f *postgresDeliveryStartupAuthorityFake) Generation(context.Context, string) (nativepostgres.DeliveryGeneration, error) {
	return f.generation, f.generationErr
}
func (f *postgresDeliveryStartupAuthorityFake) Publication(context.Context, string) (nativepostgres.DeliveryPublication, error) {
	return f.publication, f.publicationErr
}
func (f *postgresDeliveryStartupAuthorityFake) SnapshotSeal(context.Context, string) (nativepostgres.SnapshotSeal, error) {
	return f.seal, f.sealErr
}

type postgresDeliveryStartupServingFake struct {
	state       servingstate.State
	stateErr    error
	artifact    servingstate.Artifact
	artifactErr error
}

func (f *postgresDeliveryStartupServingFake) ByID(context.Context, servingstate.ID) (servingstate.State, error) {
	return f.state, f.stateErr
}
func (f *postgresDeliveryStartupServingFake) ArtifactByServingState(context.Context, servingstate.ID) (servingstate.Artifact, error) {
	return f.artifact, f.artifactErr
}

type postgresDeliveryStartupPhysicalFake struct {
	contract physicalpool.AdmissionContract
	err      error
}

func (f postgresDeliveryStartupPhysicalFake) LoadAdmissionContractByCompatibilityDigest(context.Context, physicalpool.PoolID, string) (physicalpool.AdmissionContract, error) {
	return f.contract, f.err
}

func TestPostgresDeliveryStartupAllowsFreshUnclaimedTarget(t *testing.T) {
	authority := &postgresDeliveryStartupAuthorityFake{targetErr: nativepostgres.ErrNotFound}
	check, err := newPostgresDeliveryStartupCheck(postgresDeliveryStartupCheckConfig{
		TargetID: startupTarget, Environment: startupEnvironment,
		ReadClaim: func(context.Context) (projectgraph.ResourceID, bool, error) { return "", false, nil },
		Delivery:  authority, Serving: &postgresDeliveryStartupServingFake{}, Physical: postgresDeliveryStartupPhysicalFake{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := check(context.Background()); err != nil {
		t.Fatalf("fresh startup = %v, want admitted", err)
	}
}

func TestPostgresDeliveryStartupAllowsClaimedPrePublicationTarget(t *testing.T) {
	check, err := newPostgresDeliveryStartupCheck(postgresDeliveryStartupCheckConfig{
		TargetID: startupTarget, Environment: startupEnvironment,
		ReadClaim: func(context.Context) (projectgraph.ResourceID, bool, error) { return startupProject, true, nil },
		Delivery:  &postgresDeliveryStartupAuthorityFake{target: nativepostgres.DeliveryTarget{TargetID: startupTarget, ProjectID: startupProject, Environment: string(startupEnvironment), TargetRevision: 1}},
		Serving:   &postgresDeliveryStartupServingFake{}, Physical: postgresDeliveryStartupPhysicalFake{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := check(context.Background()); err != nil {
		t.Fatalf("claimed pre-publication startup = %v, want admitted", err)
	}
}

func TestPostgresDeliveryStartupRejectsClaimTargetPartialState(t *testing.T) {
	for _, test := range []struct {
		name      string
		claim     bool
		targetErr error
	}{
		{name: "claim without target", claim: true, targetErr: nativepostgres.ErrNotFound},
		{name: "target without claim", targetErr: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			authority := &postgresDeliveryStartupAuthorityFake{
				target:    nativepostgres.DeliveryTarget{TargetID: startupTarget, ProjectID: startupProject, Environment: string(startupEnvironment), TargetRevision: 1},
				targetErr: test.targetErr,
			}
			check, err := newPostgresDeliveryStartupCheck(postgresDeliveryStartupCheckConfig{
				TargetID: startupTarget, Environment: startupEnvironment,
				ReadClaim: func(context.Context) (projectgraph.ResourceID, bool, error) {
					if test.claim {
						return startupProject, true, nil
					}
					return "", false, nil
				},
				Delivery: authority, Serving: &postgresDeliveryStartupServingFake{}, Physical: postgresDeliveryStartupPhysicalFake{},
			})
			if err != nil {
				t.Fatal(err)
			}
			assertPostgresStartupDiagnostic(t, check(context.Background()), deployment.DeliveryStartupClaimTargetPartial)
		})
	}
}

func TestPostgresDeliveryStartupRejectsTargetAndPointerIdentityDrift(t *testing.T) {
	tests := []struct {
		name   string
		target nativepostgres.DeliveryTarget
		want   deployment.DeliveryStartupDiagnosticCode
	}{
		{name: "target id", target: nativepostgres.DeliveryTarget{TargetID: "other-target", ProjectID: startupProject, Environment: string(startupEnvironment), TargetRevision: 1}, want: deployment.DeliveryStartupTargetIdentityMismatch},
		{name: "target scope", target: nativepostgres.DeliveryTarget{TargetID: startupTarget, ProjectID: "other", Environment: string(startupEnvironment), TargetRevision: 1}, want: deployment.DeliveryStartupTargetIdentityMismatch},
		{name: "generation without publication", target: nativepostgres.DeliveryTarget{TargetID: startupTarget, ProjectID: startupProject, Environment: string(startupEnvironment), TargetRevision: 1, ActiveGenerationID: startupGeneration}, want: deployment.DeliveryStartupActivePointerMismatch},
		{name: "publication without generation", target: nativepostgres.DeliveryTarget{TargetID: startupTarget, ProjectID: startupProject, Environment: string(startupEnvironment), TargetRevision: 1, ActivePublicationID: startupPublication}, want: deployment.DeliveryStartupActivePointerMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authority := &postgresDeliveryStartupAuthorityFake{target: test.target}
			check, err := newPostgresDeliveryStartupCheck(postgresDeliveryStartupCheckConfig{
				TargetID: startupTarget, Environment: startupEnvironment,
				ReadClaim: func(context.Context) (projectgraph.ResourceID, bool, error) { return startupProject, true, nil },
				Delivery:  authority, Serving: &postgresDeliveryStartupServingFake{}, Physical: postgresDeliveryStartupPhysicalFake{},
			})
			if err != nil {
				t.Fatal(err)
			}
			assertPostgresStartupDiagnostic(t, check(context.Background()), test.want)
		})
	}
}

func TestPostgresDeliveryStartupRejectsMissingActiveEvidence(t *testing.T) {
	pool, admission := startupPhysicalPool(t)
	baseAuthority := postgresDeliveryStartupAuthorityFake{
		target:      nativepostgres.DeliveryTarget{TargetID: startupTarget, ProjectID: startupProject, Environment: string(startupEnvironment), TargetRevision: 2, ActiveGenerationID: startupGeneration, ActivePublicationID: startupPublication},
		generation:  nativepostgres.DeliveryGeneration{GenerationID: startupGeneration, TargetID: startupTarget, CandidateID: startupCandidate, PlanID: startupPlan, SnapshotSealID: startupSeal, PlanDigest: "plan-digest", ServingArtifactDigest: "artifact-digest", CompiledGraphDigest: "graph-digest", CompiledConfigDigest: "config-digest", SecurityDomainFingerprint: "security-digest", ArtifactRoot: "root", ArtifactRootDigest: "root-digest"},
		publication: nativepostgres.DeliveryPublication{PublicationID: startupPublication, TargetID: startupTarget, GenerationID: startupGeneration, CandidateID: startupCandidate, SnapshotSealID: startupSeal, State: "committed", ExpectedTargetRevision: 1, ResultTargetRevision: 2},
		seal:        nativepostgres.SnapshotSeal{SealID: startupSeal, CandidateID: startupCandidate, PhysicalPoolID: string(pool.ID), CompatibilityDigest: admission.CompatibilityDigest, DuckLakeSnapshotID: 42, PlanDigest: "plan-digest", ServingArtifactID: "artifact-id", ServingArtifactDigest: "artifact-digest", CompiledGraphDigest: "graph-digest", CompiledConfigDigest: "config-digest", SecurityDomainFingerprint: "security-digest", ArtifactRoot: "root", ArtifactRootDigest: "root-digest"},
	}
	for _, test := range []struct {
		name string
		make func(*postgresDeliveryStartupAuthorityFake, *postgresDeliveryStartupServingFake)
		want deployment.DeliveryStartupDiagnosticCode
	}{
		{name: "generation", make: func(a *postgresDeliveryStartupAuthorityFake, _ *postgresDeliveryStartupServingFake) {
			a.generationErr = nativepostgres.ErrNotFound
		}, want: deployment.DeliveryStartupMissingServingGeneration},
		{name: "publication", make: func(a *postgresDeliveryStartupAuthorityFake, _ *postgresDeliveryStartupServingFake) {
			a.publicationErr = nativepostgres.ErrNotFound
		}, want: deployment.DeliveryStartupMissingPublication},
		{name: "serving state", make: func(_ *postgresDeliveryStartupAuthorityFake, s *postgresDeliveryStartupServingFake) {
			s.stateErr = servingstate.ErrNotFound
		}, want: deployment.DeliveryStartupMissingServingState},
		{name: "seal", make: func(a *postgresDeliveryStartupAuthorityFake, _ *postgresDeliveryStartupServingFake) {
			a.sealErr = nativepostgres.ErrNotFound
		}, want: deployment.DeliveryStartupMissingSeal},
	} {
		t.Run(test.name, func(t *testing.T) {
			authority := baseAuthority
			serving := postgresDeliveryStartupServingFake{state: servingstate.State{ID: startupGeneration, ProjectID: startupProject, Environment: startupEnvironment, Status: servingstate.StatusActive, Digest: "artifact-digest", DuckLakeSnapshotID: 42}, artifact: servingstate.Artifact{ID: "artifact-id", ServingStateID: startupGeneration, Digest: "artifact-digest", Path: "pool/object"}}
			test.make(&authority, &serving)
			check, err := newPostgresDeliveryStartupCheck(postgresDeliveryStartupCheckConfig{
				TargetID: startupTarget, Environment: startupEnvironment,
				ReadClaim: func(context.Context) (projectgraph.ResourceID, bool, error) { return startupProject, true, nil },
				Delivery:  &authority, Serving: &serving, Physical: postgresDeliveryStartupPhysicalFake{contract: physicalpool.AdmissionContract{Pool: pool, Admission: admission}},
			})
			if err != nil {
				t.Fatal(err)
			}
			assertPostgresStartupDiagnostic(t, check(context.Background()), test.want)
		})
	}
}

func TestPostgresDeliveryStartupAcceptsExactActiveEvidenceAndChecksRevision(t *testing.T) {
	pool, admission := startupPhysicalPool(t)
	authority := &postgresDeliveryStartupAuthorityFake{
		target:      nativepostgres.DeliveryTarget{TargetID: startupTarget, ProjectID: startupProject, Environment: string(startupEnvironment), TargetRevision: 2, ActiveGenerationID: startupGeneration, ActivePublicationID: startupPublication},
		generation:  nativepostgres.DeliveryGeneration{GenerationID: startupGeneration, TargetID: startupTarget, CandidateID: startupCandidate, PlanID: startupPlan, SnapshotSealID: startupSeal, PlanDigest: "plan-digest", ServingArtifactDigest: "artifact-digest", CompiledGraphDigest: "graph-digest", CompiledConfigDigest: "config-digest", SecurityDomainFingerprint: "security-digest", ArtifactRoot: "root", ArtifactRootDigest: "root-digest"},
		publication: nativepostgres.DeliveryPublication{PublicationID: startupPublication, TargetID: startupTarget, GenerationID: startupGeneration, CandidateID: startupCandidate, SnapshotSealID: startupSeal, State: "committed", ExpectedTargetRevision: 1, ResultTargetRevision: 2},
		seal:        nativepostgres.SnapshotSeal{SealID: startupSeal, CandidateID: startupCandidate, PhysicalPoolID: string(pool.ID), CompatibilityDigest: admission.CompatibilityDigest, DuckLakeSnapshotID: 42, PlanDigest: "plan-digest", ServingArtifactID: "artifact-id", ServingArtifactDigest: "artifact-digest", CompiledGraphDigest: "graph-digest", CompiledConfigDigest: "config-digest", SecurityDomainFingerprint: "security-digest", ArtifactRoot: "root", ArtifactRootDigest: "root-digest"},
	}
	serving := &postgresDeliveryStartupServingFake{
		state:    servingstate.State{ID: startupGeneration, ProjectID: startupProject, Environment: startupEnvironment, Status: servingstate.StatusActive, Digest: "artifact-digest", DuckLakeSnapshotID: 42},
		artifact: servingstate.Artifact{ID: "artifact-id", ServingStateID: startupGeneration, Digest: "artifact-digest", Path: "pool/object"},
	}
	check, err := newPostgresDeliveryStartupCheck(postgresDeliveryStartupCheckConfig{
		TargetID: startupTarget, Environment: startupEnvironment,
		ReadClaim: func(context.Context) (projectgraph.ResourceID, bool, error) { return startupProject, true, nil },
		Delivery:  authority, Serving: serving, Physical: postgresDeliveryStartupPhysicalFake{contract: physicalpool.AdmissionContract{Pool: pool, Admission: admission}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := check(context.Background()); err != nil {
		t.Fatalf("exact active evidence = %v, want admitted", err)
	}

	authority.publication.ResultTargetRevision = 1
	assertPostgresStartupDiagnostic(t, check(context.Background()), deployment.DeliveryStartupActivePointerMismatch)
	authority.publication.ResultTargetRevision = 2
	authority.seal.CompiledConfigDigest = "wrong"
	assertPostgresStartupDiagnostic(t, check(context.Background()), deployment.DeliveryStartupSealEvidenceMismatch)
	authority.seal.CompiledConfigDigest = "config-digest"
	checkPhysicalFailure, err := newPostgresDeliveryStartupCheck(postgresDeliveryStartupCheckConfig{
		TargetID: startupTarget, Environment: startupEnvironment,
		ReadClaim: func(context.Context) (projectgraph.ResourceID, bool, error) { return startupProject, true, nil },
		Delivery:  authority, Serving: serving, Physical: postgresDeliveryStartupPhysicalFake{err: physicalpool.ErrPoolNotAdmitted},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertPostgresStartupDiagnostic(t, checkPhysicalFailure(context.Background()), deployment.DeliveryStartupMissingPoolAdmission)
}

func startupPhysicalPool(t *testing.T) (physicalpool.PhysicalPool, physicalpool.PoolAdmission) {
	t.Helper()
	tuple := physicalpool.Compatibility{DuckDBRuntime: "duckdb", DuckLakeExtension: "ducklake", CatalogFormat: "catalog", StorageImplementation: "s3", ObjectNamingContract: "object-v1"}
	pool, err := physicalpool.NewPhysicalPool(physicalpool.PoolIdentity{StorageLocation: "s3://bucket/data", StorageNamespace: "namespace", IsolationBoundary: "target", RetentionAuthority: "retention", Compatibility: tuple})
	if err != nil {
		t.Fatal(err)
	}
	compatibilityDigest, err := tuple.Digest()
	if err != nil {
		t.Fatal(err)
	}
	admission := physicalpool.PoolAdmission{PoolID: pool.ID, Compatibility: tuple, CompatibilityDigest: compatibilityDigest, EvidenceDigest: "sha256:0000000000000000000000000000000000000000000000000000000000000000", ConformanceVersion: "v1"}
	pool, err = pool.ApplyAdmission(admission)
	if err != nil {
		t.Fatal(err)
	}
	return pool, admission
}

func assertPostgresStartupDiagnostic(t *testing.T, err error, want deployment.DeliveryStartupDiagnosticCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("startup succeeded, want %s", want)
	}
	if !errors.Is(err, deployment.ErrDeliveryStartupNotReady) {
		t.Fatalf("startup error = %v, want not-ready", err)
	}
	diagnostics := deployment.DeliveryStartupDiagnosticsOf(err)
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == want {
			return
		}
	}
	t.Fatalf("startup diagnostics = %#v, want %s", diagnostics, want)
}
