package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	physicalpool "github.com/flidai/leapview/internal/analytics/physicalpool"
	appdeploymentpostgres "github.com/flidai/leapview/internal/app/deploymentpostgres"
	"github.com/flidai/leapview/internal/deployment"
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
	target         appdeploymentpostgres.StartupTarget
	targetErr      error
	generation     appdeploymentpostgres.StartupGeneration
	generationErr  error
	publication    appdeploymentpostgres.StartupPublication
	publicationErr error
	seal           appdeploymentpostgres.StartupSnapshotSeal
	sealErr        error
}

func (f *postgresDeliveryStartupAuthorityFake) Target(context.Context, string) (appdeploymentpostgres.StartupTarget, error) {
	return f.target, f.targetErr
}
func (f *postgresDeliveryStartupAuthorityFake) Generation(context.Context, string) (appdeploymentpostgres.StartupGeneration, error) {
	return f.generation, f.generationErr
}
func (f *postgresDeliveryStartupAuthorityFake) Publication(context.Context, string) (appdeploymentpostgres.StartupPublication, error) {
	return f.publication, f.publicationErr
}
func (f *postgresDeliveryStartupAuthorityFake) SnapshotSeal(context.Context, string) (appdeploymentpostgres.StartupSnapshotSeal, error) {
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
	authority := &postgresDeliveryStartupAuthorityFake{targetErr: deployment.ErrNotFound}
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
		Delivery:  &postgresDeliveryStartupAuthorityFake{target: appdeploymentpostgres.StartupTarget{TargetID: startupTarget, ProjectID: startupProject, Environment: string(startupEnvironment), TargetRevision: 1}},
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
		{name: "claim without target", claim: true, targetErr: deployment.ErrNotFound},
		{name: "target without claim", targetErr: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			authority := &postgresDeliveryStartupAuthorityFake{
				target:    appdeploymentpostgres.StartupTarget{TargetID: startupTarget, ProjectID: startupProject, Environment: string(startupEnvironment), TargetRevision: 1},
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
		target appdeploymentpostgres.StartupTarget
		want   deployment.DeliveryStartupDiagnosticCode
	}{
		{name: "target id", target: appdeploymentpostgres.StartupTarget{TargetID: "other-target", ProjectID: startupProject, Environment: string(startupEnvironment), TargetRevision: 1}, want: deployment.DeliveryStartupTargetIdentityMismatch},
		{name: "target scope", target: appdeploymentpostgres.StartupTarget{TargetID: startupTarget, ProjectID: "other", Environment: string(startupEnvironment), TargetRevision: 1}, want: deployment.DeliveryStartupTargetIdentityMismatch},
		{name: "generation without publication", target: appdeploymentpostgres.StartupTarget{TargetID: startupTarget, ProjectID: startupProject, Environment: string(startupEnvironment), TargetRevision: 1, ActiveGenerationID: startupGeneration}, want: deployment.DeliveryStartupActivePointerMismatch},
		{name: "publication without generation", target: appdeploymentpostgres.StartupTarget{TargetID: startupTarget, ProjectID: startupProject, Environment: string(startupEnvironment), TargetRevision: 1, ActivePublicationID: startupPublication}, want: deployment.DeliveryStartupActivePointerMismatch},
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
	artifact := startupNativeArtifact()
	baseAuthority := postgresDeliveryStartupAuthorityFake{
		target:      appdeploymentpostgres.StartupTarget{TargetID: startupTarget, ProjectID: startupProject, Environment: string(startupEnvironment), TargetRevision: 2, ActiveGenerationID: startupGeneration, ActivePublicationID: startupPublication},
		generation:  appdeploymentpostgres.StartupGeneration{GenerationID: startupGeneration, TargetID: startupTarget, CandidateID: startupCandidate, PlanID: startupPlan, SnapshotSealID: startupSeal, PlanDigest: "plan-digest", ServingArtifactDigest: artifact.Digest, CompiledGraphDigest: "graph-digest", CompiledConfigDigest: "config-digest", SecurityDomainFingerprint: "security-digest", ArtifactRoot: "root", ArtifactRootDigest: "root-digest"},
		publication: appdeploymentpostgres.StartupPublication{PublicationID: startupPublication, TargetID: startupTarget, GenerationID: startupGeneration, CandidateID: startupCandidate, SnapshotSealID: startupSeal, State: "committed", ExpectedTargetRevision: 1, ResultTargetRevision: 2},
		seal:        appdeploymentpostgres.StartupSnapshotSeal{SealID: startupSeal, CandidateID: startupCandidate, PhysicalPoolID: string(pool.ID), CompatibilityDigest: admission.CompatibilityDigest, DuckLakeSnapshotID: 42, PlanDigest: "plan-digest", ServingArtifactID: artifact.ID, ServingArtifactDigest: artifact.Digest, CompiledGraphDigest: "graph-digest", CompiledConfigDigest: "config-digest", SecurityDomainFingerprint: "security-digest", ArtifactRoot: "root", ArtifactRootDigest: "root-digest"},
	}
	for _, test := range []struct {
		name string
		make func(*postgresDeliveryStartupAuthorityFake, *postgresDeliveryStartupServingFake)
		want deployment.DeliveryStartupDiagnosticCode
	}{
		{name: "generation", make: func(a *postgresDeliveryStartupAuthorityFake, _ *postgresDeliveryStartupServingFake) {
			a.generationErr = deployment.ErrNotFound
		}, want: deployment.DeliveryStartupMissingServingGeneration},
		{name: "publication", make: func(a *postgresDeliveryStartupAuthorityFake, _ *postgresDeliveryStartupServingFake) {
			a.publicationErr = deployment.ErrNotFound
		}, want: deployment.DeliveryStartupMissingPublication},
		{name: "serving state", make: func(_ *postgresDeliveryStartupAuthorityFake, s *postgresDeliveryStartupServingFake) {
			s.stateErr = servingstate.ErrNotFound
		}, want: deployment.DeliveryStartupMissingServingState},
		{name: "seal", make: func(a *postgresDeliveryStartupAuthorityFake, _ *postgresDeliveryStartupServingFake) {
			a.sealErr = deployment.ErrNotFound
		}, want: deployment.DeliveryStartupMissingSeal},
	} {
		t.Run(test.name, func(t *testing.T) {
			authority := baseAuthority
			serving := postgresDeliveryStartupServingFake{state: servingstate.State{ID: startupGeneration, ProjectID: startupProject, Environment: startupEnvironment, Status: servingstate.StatusActive, Digest: artifact.Digest, DuckLakeSnapshotID: 42}, artifact: artifact}
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
	artifact := startupNativeArtifact()
	authority := &postgresDeliveryStartupAuthorityFake{
		target:      appdeploymentpostgres.StartupTarget{TargetID: startupTarget, ProjectID: startupProject, Environment: string(startupEnvironment), TargetRevision: 2, ActiveGenerationID: startupGeneration, ActivePublicationID: startupPublication},
		generation:  appdeploymentpostgres.StartupGeneration{GenerationID: startupGeneration, TargetID: startupTarget, CandidateID: startupCandidate, PlanID: startupPlan, SnapshotSealID: startupSeal, PlanDigest: "plan-digest", ServingArtifactDigest: artifact.Digest, CompiledGraphDigest: "graph-digest", CompiledConfigDigest: "config-digest", SecurityDomainFingerprint: "security-digest", ArtifactRoot: "root", ArtifactRootDigest: "root-digest"},
		publication: appdeploymentpostgres.StartupPublication{PublicationID: startupPublication, TargetID: startupTarget, GenerationID: startupGeneration, CandidateID: startupCandidate, SnapshotSealID: startupSeal, State: "committed", ExpectedTargetRevision: 1, ResultTargetRevision: 2},
		seal:        appdeploymentpostgres.StartupSnapshotSeal{SealID: startupSeal, CandidateID: startupCandidate, PhysicalPoolID: string(pool.ID), CompatibilityDigest: admission.CompatibilityDigest, DuckLakeSnapshotID: 42, PlanDigest: "plan-digest", ServingArtifactID: artifact.ID, ServingArtifactDigest: artifact.Digest, CompiledGraphDigest: "graph-digest", CompiledConfigDigest: "config-digest", SecurityDomainFingerprint: "security-digest", ArtifactRoot: "root", ArtifactRootDigest: "root-digest"},
	}
	serving := &postgresDeliveryStartupServingFake{
		state:    servingstate.State{ID: startupGeneration, ProjectID: startupProject, Environment: startupEnvironment, Status: servingstate.StatusActive, Digest: artifact.Digest, DuckLakeSnapshotID: 42},
		artifact: artifact,
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

func TestPostgresDeliveryStartupRejectsMalformedNativeArtifactEvidence(t *testing.T) {
	pool, admission := startupPhysicalPool(t)
	artifact := startupNativeArtifact()
	authority := &postgresDeliveryStartupAuthorityFake{
		target:      appdeploymentpostgres.StartupTarget{TargetID: startupTarget, ProjectID: startupProject, Environment: string(startupEnvironment), TargetRevision: 2, ActiveGenerationID: startupGeneration, ActivePublicationID: startupPublication},
		generation:  appdeploymentpostgres.StartupGeneration{GenerationID: startupGeneration, TargetID: startupTarget, CandidateID: startupCandidate, PlanID: startupPlan, SnapshotSealID: startupSeal, PlanDigest: "plan-digest", ServingArtifactDigest: artifact.Digest, CompiledGraphDigest: "graph-digest", CompiledConfigDigest: "config-digest", SecurityDomainFingerprint: "security-digest", ArtifactRoot: "root", ArtifactRootDigest: "root-digest"},
		publication: appdeploymentpostgres.StartupPublication{PublicationID: startupPublication, TargetID: startupTarget, GenerationID: startupGeneration, CandidateID: startupCandidate, SnapshotSealID: startupSeal, State: "committed", ExpectedTargetRevision: 1, ResultTargetRevision: 2},
		seal:        appdeploymentpostgres.StartupSnapshotSeal{SealID: startupSeal, CandidateID: startupCandidate, PhysicalPoolID: string(pool.ID), CompatibilityDigest: admission.CompatibilityDigest, DuckLakeSnapshotID: 42, PlanDigest: "plan-digest", ServingArtifactID: artifact.ID, ServingArtifactDigest: artifact.Digest, CompiledGraphDigest: "graph-digest", CompiledConfigDigest: "config-digest", SecurityDomainFingerprint: "security-digest", ArtifactRoot: "root", ArtifactRootDigest: "root-digest"},
	}
	serving := &postgresDeliveryStartupServingFake{
		state:    servingstate.State{ID: startupGeneration, ProjectID: startupProject, Environment: startupEnvironment, Status: servingstate.StatusActive, Digest: artifact.Digest, DuckLakeSnapshotID: 42},
		artifact: artifact,
	}
	check, err := newPostgresDeliveryStartupCheck(postgresDeliveryStartupCheckConfig{
		TargetID: startupTarget, Environment: startupEnvironment,
		ReadClaim: func(context.Context) (projectgraph.ResourceID, bool, error) { return startupProject, true, nil },
		Delivery:  authority, Serving: serving, Physical: postgresDeliveryStartupPhysicalFake{contract: physicalpool.AdmissionContract{Pool: pool, Admission: admission}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*servingstate.Artifact)
	}{
		{name: "missing locator", mutate: func(a *servingstate.Artifact) { a.Locator = "" }},
		{name: "wrong locator", mutate: func(a *servingstate.Artifact) { a.Locator = "serving-artifacts/not-the-digest.tar.gz" }},
		{name: "missing security domain", mutate: func(a *servingstate.Artifact) { a.StorageSecurityDomain = "" }},
		{name: "malformed security domain", mutate: func(a *servingstate.Artifact) { a.StorageSecurityDomain = " runtime" }},
		{name: "missing metadata digest", mutate: func(a *servingstate.Artifact) { a.MetadataDigest = "" }},
		{name: "malformed metadata digest", mutate: func(a *servingstate.Artifact) { a.MetadataDigest = "not-a-digest" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			serving.artifact = startupNativeArtifact()
			test.mutate(&serving.artifact)
			assertPostgresStartupDiagnostic(t, check(context.Background()), deployment.DeliveryStartupServingEvidenceMismatch)
		})
	}
}

func startupNativeArtifact() servingstate.Artifact {
	digest := "sha256:" + strings.Repeat("a", 64)
	return servingstate.Artifact{
		ID: "artifact-" + strings.TrimPrefix(digest, "sha256:"), ServingStateID: startupGeneration, Digest: digest,
		Format: servingstate.ArtifactBundleFormat, Locator: "serving-artifacts/" + strings.TrimPrefix(digest, "sha256:") + ".tar.gz",
		StorageSecurityDomain: "runtime", ContentType: servingstate.ArtifactBundleContentType, MetadataDigest: "sha256:" + strings.Repeat("b", 64), SizeBytes: 1,
	}
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
