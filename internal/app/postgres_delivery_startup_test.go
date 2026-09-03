package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	physicalpool "github.com/flidai/leapview/internal/analytics/physicalpool"
	appdeploymentpostgres "github.com/flidai/leapview/internal/app/deploymentpostgres"
	"github.com/flidai/leapview/internal/deployment"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/recoveryset"
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

type postgresDeliveryStartupRecoveryFake struct {
	set            recoveryset.RecoverySet
	err            error
	seen           string
	attempt        recoveryset.ValidationAttempt
	attemptErr     error
	result         recoveryset.ValidationResult
	resultErr      error
	validationSeen string
}

func (f *postgresDeliveryStartupRecoveryFake) ReadExact(_ context.Context, id string) (recoveryset.RecoverySet, error) {
	f.seen = id
	return f.set, f.err
}

func (f *postgresDeliveryStartupRecoveryFake) ValidationAttempt(_ context.Context, id string) (recoveryset.ValidationAttempt, error) {
	f.validationSeen = id
	return f.attempt, f.attemptErr
}

func (f *postgresDeliveryStartupRecoveryFake) ValidationResult(_ context.Context, id string) (recoveryset.ValidationResult, error) {
	f.validationSeen = id
	return f.result, f.resultErr
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

func TestPostgresDeliveryStartupValidatesExplicitRecoverySet(t *testing.T) {
	fixture := startupRecoveryFixture(t)
	recovery := &postgresDeliveryStartupRecoveryFake{set: fixture.set}
	check, err := newPostgresDeliveryStartupCheck(fixture.config(recovery))
	if err != nil {
		t.Fatal(err)
	}
	if err := check(t.Context()); err != nil {
		t.Fatalf("exact recovery startup = %v", err)
	}
	if recovery.seen != fixture.set.ID {
		t.Fatalf("recovery lookup = %q, want exact %q", recovery.seen, fixture.set.ID)
	}
}

func TestPostgresDeliveryStartupRequiresExactPassedRecoveryValidation(t *testing.T) {
	fixture := startupRecoveryFixture(t)
	for _, test := range []struct {
		name string
		make func(*postgresDeliveryStartupRecoveryFake)
		want deployment.DeliveryStartupDiagnosticCode
	}{
		{name: "missing attempt", make: func(r *postgresDeliveryStartupRecoveryFake) { r.attemptErr = recoveryset.ErrNotFound }, want: deployment.DeliveryStartupRecoverySetValidationMissing},
		{name: "missing result", make: func(r *postgresDeliveryStartupRecoveryFake) { r.resultErr = recoveryset.ErrNotFound }, want: deployment.DeliveryStartupRecoverySetValidationMissing},
		{name: "running attempt", make: func(r *postgresDeliveryStartupRecoveryFake) { r.attempt.Status = recoveryset.ValidationRunning }, want: deployment.DeliveryStartupRecoverySetValidationNotPassed},
		{name: "failed attempt", make: func(r *postgresDeliveryStartupRecoveryFake) { r.attempt.Status = recoveryset.ValidationFailed }, want: deployment.DeliveryStartupRecoverySetValidationNotPassed},
		{name: "attempt belongs to another set", make: func(r *postgresDeliveryStartupRecoveryFake) { r.attempt.SetID = "018f3f83-7b2f-7b37-9f9e-0000000002ff" }, want: deployment.DeliveryStartupRecoverySetValidationMismatch},
		{name: "result digest drift", make: func(r *postgresDeliveryStartupRecoveryFake) {
			r.result.ResultDigest = "sha256:" + strings.Repeat("f", 64)
		}, want: deployment.DeliveryStartupRecoverySetValidationMismatch},
		{name: "result evidence frontier drift", make: func(r *postgresDeliveryStartupRecoveryFake) {
			envelope, err := recoveryset.ParseValidationEvidenceEnvelope(r.result.Evidence)
			if err != nil {
				t.Fatalf("parse validation evidence: %v", err)
			}
			envelope.ObjectRoots[0].VersionID = "different-version"
			result, err := recoveryset.NewValidationResult(envelope, r.result.RecordedAt)
			if err != nil {
				t.Fatalf("construct validation result: %v", err)
			}
			r.result = result
			r.attempt.ResultDigest = result.ResultDigest
		}, want: deployment.DeliveryStartupRecoverySetValidationMismatch},
	} {
		t.Run(test.name, func(t *testing.T) {
			recovery := &postgresDeliveryStartupRecoveryFake{}
			fixture.config(recovery)
			test.make(recovery)
			check, err := newPostgresDeliveryStartupCheck(fixture.config(recovery))
			if err != nil {
				t.Fatal(err)
			}
			assertPostgresStartupDiagnostic(t, check(t.Context()), test.want)
		})
	}
}

func TestPostgresDeliveryStartupPreservesRecoveryValidationStoreFailure(t *testing.T) {
	fixture := startupRecoveryFixture(t)
	want := errors.New("validation store unavailable")
	recovery := &postgresDeliveryStartupRecoveryFake{attemptErr: want}
	check, err := newPostgresDeliveryStartupCheck(fixture.config(recovery))
	if err != nil {
		t.Fatal(err)
	}
	if err := check(t.Context()); !errors.Is(err, want) {
		t.Fatalf("startup validation error = %v, want wrapped store failure", err)
	}
}

func TestPostgresDeliveryStartupRejectsMissingInvalidAndUnpublishedRecoverySet(t *testing.T) {
	fixture := startupRecoveryFixture(t)
	for _, test := range []struct {
		name string
		make func(*postgresDeliveryStartupRecoveryFake)
		want deployment.DeliveryStartupDiagnosticCode
	}{
		{name: "missing", make: func(r *postgresDeliveryStartupRecoveryFake) { r.err = recoveryset.ErrNotFound }, want: deployment.DeliveryStartupRecoverySetMissing},
		{name: "read invalid", make: func(r *postgresDeliveryStartupRecoveryFake) { r.err = recoveryset.ErrInvalid }, want: deployment.DeliveryStartupRecoverySetInvalid},
		{name: "invalid", make: func(r *postgresDeliveryStartupRecoveryFake) { r.set.Delivery.TargetRevision = 0 }, want: deployment.DeliveryStartupRecoverySetInvalid},
		{name: "missing validation pointer", make: func(r *postgresDeliveryStartupRecoveryFake) { r.set.PublishedValidationAttemptID = "" }, want: deployment.DeliveryStartupRecoverySetInvalid},
		{name: "invalid status", make: func(r *postgresDeliveryStartupRecoveryFake) { r.set.Status = recoveryset.StatusInvalid }, want: deployment.DeliveryStartupRecoverySetInvalid},
		{name: "not published", make: func(r *postgresDeliveryStartupRecoveryFake) {
			r.set.Status = recoveryset.StatusPrepared
			r.set.PublishedValidationAttemptID = ""
		}, want: deployment.DeliveryStartupRecoverySetNotPublished},
	} {
		t.Run(test.name, func(t *testing.T) {
			recovery := &postgresDeliveryStartupRecoveryFake{set: fixture.set}
			test.make(recovery)
			check, err := newPostgresDeliveryStartupCheck(fixture.config(recovery))
			if err != nil {
				t.Fatal(err)
			}
			assertPostgresStartupDiagnostic(t, check(t.Context()), test.want)
		})
	}
}

func TestPostgresDeliveryStartupClassifiesRecoverySetMismatches(t *testing.T) {
	fixture := startupRecoveryFixture(t)
	for _, test := range []struct {
		name string
		make func(*recoveryset.RecoverySet)
		want deployment.DeliveryStartupDiagnosticCode
	}{
		{name: "pointer", make: func(set *recoveryset.RecoverySet) { set.Delivery.TargetRevision++ }, want: deployment.DeliveryStartupRecoverySetPointerMismatch},
		{name: "artifact", make: func(set *recoveryset.RecoverySet) { set.Serving.ServingArtifactID = "artifact-other" }, want: deployment.DeliveryStartupRecoverySetArtifactMismatch},
		{name: "catalog", make: func(set *recoveryset.RecoverySet) {
			set.Catalog.CatalogID, set.Serving.CatalogID = "catalog-other", "catalog-other"
		}, want: deployment.DeliveryStartupRecoverySetCatalogMismatch},
		{name: "compatibility", make: func(set *recoveryset.RecoverySet) {
			set.Compatibility.StorageImplementation = "local"
			set.Serving.CompatibilityDigest, _ = set.Compatibility.Digest()
		}, want: deployment.DeliveryStartupRecoverySetCompatibilityMismatch},
		{name: "seal", make: func(set *recoveryset.RecoverySet) { set.Serving.Region = "region-other" }, want: deployment.DeliveryStartupRecoverySetSealMismatch},
		{name: "object roots", make: func(set *recoveryset.RecoverySet) { set.ObjectRoots = set.ObjectRoots[:1] }, want: deployment.DeliveryStartupRecoverySetSealMismatch},
	} {
		t.Run(test.name, func(t *testing.T) {
			set := fixture.set
			test.make(&set)
			recovery := &postgresDeliveryStartupRecoveryFake{set: set}
			check, err := newPostgresDeliveryStartupCheck(fixture.config(recovery))
			if err != nil {
				t.Fatal(err)
			}
			assertPostgresStartupDiagnostic(t, check(t.Context()), test.want)
		})
	}
}

func TestPostgresDeliveryStartupRequiresCanonicalRecoverySetID(t *testing.T) {
	fixture := startupRecoveryFixture(t)
	for _, id := range []string{"not-a-uuid", " " + fixture.set.ID} {
		config := fixture.config(&postgresDeliveryStartupRecoveryFake{set: fixture.set})
		config.RecoverySetID = id
		if _, err := newPostgresDeliveryStartupCheck(config); err == nil {
			t.Fatalf("recovery set id %q accepted", id)
		}
	}
}

type postgresDeliveryStartupRecoveryFixture struct {
	set        recoveryset.RecoverySet
	authority  *postgresDeliveryStartupAuthorityFake
	serving    *postgresDeliveryStartupServingFake
	physical   postgresDeliveryStartupPhysicalFake
	validation recoveryset.ValidationAttempt
	result     recoveryset.ValidationResult
}

func (f postgresDeliveryStartupRecoveryFixture) config(recovery *postgresDeliveryStartupRecoveryFake) postgresDeliveryStartupCheckConfig {
	if recovery != nil {
		if recovery.set.ID == "" {
			recovery.set = f.set
		}
		if recovery.attempt.AttemptID == "" {
			recovery.attempt = f.validation
		}
		if recovery.result.AttemptID == "" {
			recovery.result = f.result
		}
	}
	return postgresDeliveryStartupCheckConfig{
		TargetID: startupTarget, Environment: startupEnvironment, RecoverySetID: f.set.ID,
		ReadClaim: func(context.Context) (projectgraph.ResourceID, bool, error) { return startupProject, true, nil },
		Delivery:  f.authority, Recovery: recovery, Serving: f.serving, Physical: f.physical,
	}
}

func startupRecoveryFixture(t *testing.T) postgresDeliveryStartupRecoveryFixture {
	t.Helper()
	const (
		setID         = "018f3f83-7b2f-7b37-9f9e-000000000200"
		generationID  = "018f3f83-7b2f-7b37-9f9e-000000000201"
		publicationID = "018f3f83-7b2f-7b37-9f9e-000000000202"
		sealID        = "018f3f83-7b2f-7b37-9f9e-000000000203"
		validationID  = "018f3f83-7b2f-7b37-9f9e-000000000204"
	)
	digest := func(ch string) string { return "sha256:" + strings.Repeat(ch, 64) }
	pool, admission := startupPhysicalPool(t)
	artifact := startupNativeArtifact()
	artifact.ServingStateID = servingstate.ID(generationID)
	poolDataPath, err := pool.DataPath()
	if err != nil {
		t.Fatal(err)
	}
	seal := appdeploymentpostgres.StartupSnapshotSeal{
		SealID: sealID, AttemptID: "attempt", CandidateID: startupCandidate,
		PhysicalPoolID: string(pool.ID), TenantDomain: "tenant", Region: "region", EncryptionDomain: "encryption", ObjectNamespace: "namespace",
		CatalogDatabase: "ducklake", CatalogID: "catalog", CatalogUUID: "catalog-uuid", CatalogVersion: 3, DuckLakeSnapshotID: 42,
		RelationNamespace: "candidate", RelationManifestDigest: digest("1"), ClosureDigest: digest("2"), ObjectRoot: poolDataPath, ObjectRootDigest: digest("3"),
		ArtifactRoot: "artifacts/target", ArtifactRootDigest: digest("4"), ServingArtifactID: artifact.ID, ServingArtifactDigest: artifact.Digest,
		CompiledGraphDigest: digest("5"), CompiledConfigDigest: digest("6"), SecurityDomainFingerprint: digest("7"), RequestDigest: digest("8"), PlanDigest: digest("9"), CompatibilityDigest: admission.CompatibilityDigest,
		DuckDBVersion: "1", RuntimeVersion: "runtime-1", DuckLakeExtensionVersion: "1", DuckLakeSpecVersion: "1", CatalogSchemaVersion: "1",
	}
	generation := appdeploymentpostgres.StartupGeneration{GenerationID: generationID, TargetID: startupTarget, CandidateID: startupCandidate, PlanID: startupPlan, SnapshotSealID: sealID, PlanDigest: seal.PlanDigest, ArtifactRoot: seal.ArtifactRoot, ArtifactRootDigest: seal.ArtifactRootDigest, ServingArtifactDigest: seal.ServingArtifactDigest, CompiledGraphDigest: seal.CompiledGraphDigest, CompiledConfigDigest: seal.CompiledConfigDigest, SecurityDomainFingerprint: seal.SecurityDomainFingerprint}
	set := recoveryset.RecoverySet{
		ID: setID, SchemaVersion: recoveryset.SchemaVersion,
		ClusterPoints: []recoveryset.ClusterRecoveryPoint{{DatabaseRole: recoveryset.DatabaseControl, ClusterIdentity: "cluster", DatabaseIdentity: "control", RecoveryIdentity: "lsn:0/100"}, {DatabaseRole: recoveryset.DatabaseDuckLake, ClusterIdentity: "cluster", DatabaseIdentity: "ducklake", RecoveryIdentity: "lsn:0/100"}},
		Delivery:      recoveryset.DeliveryPointer{TargetID: startupTarget, GenerationID: generationID, PublicationID: publicationID, TargetRevision: 2},
		Serving:       recoveryset.SnapshotSeal{SealID: seal.SealID, PhysicalPoolID: seal.PhysicalPoolID, TenantDomain: seal.TenantDomain, Region: seal.Region, EncryptionDomain: seal.EncryptionDomain, ObjectNamespace: seal.ObjectNamespace, CatalogDatabase: seal.CatalogDatabase, CatalogID: seal.CatalogID, CatalogUUID: seal.CatalogUUID, CatalogVersion: seal.CatalogVersion, DuckLakeSnapshotID: seal.DuckLakeSnapshotID, RelationManifestDigest: seal.RelationManifestDigest, RelationNamespace: seal.RelationNamespace, ClosureDigest: seal.ClosureDigest, ObjectRoot: seal.ObjectRoot, ObjectRootDigest: seal.ObjectRootDigest, ArtifactRoot: seal.ArtifactRoot, ArtifactRootDigest: seal.ArtifactRootDigest, ServingArtifactID: seal.ServingArtifactID, ServingArtifactDigest: seal.ServingArtifactDigest, CompiledGraphDigest: seal.CompiledGraphDigest, CompiledConfigDigest: seal.CompiledConfigDigest, SecurityDomainFingerprint: seal.SecurityDomainFingerprint, RequestDigest: seal.RequestDigest, PlanDigest: seal.PlanDigest, CompatibilityDigest: seal.CompatibilityDigest, DuckDBVersion: seal.DuckDBVersion, RuntimeVersion: seal.RuntimeVersion, DuckLakeExtensionVersion: seal.DuckLakeExtensionVersion, DuckLakeSpecVersion: seal.DuckLakeSpecVersion, CatalogSchemaVersion: seal.CatalogSchemaVersion},
		Catalog:       recoveryset.CatalogCommit{CatalogID: seal.CatalogID, CatalogDatabase: seal.CatalogDatabase, CatalogUUID: seal.CatalogUUID, CatalogVersion: seal.CatalogVersion, SnapshotID: seal.DuckLakeSnapshotID},
		ObjectRoots: []recoveryset.ObjectRoot{
			{Kind: recoveryset.ObjectRootDuckLake, URI: seal.ObjectRoot, VersionID: "v42", Digest: seal.ObjectRootDigest, ProviderRecoveryFrontier: "s3-version:v42"},
			{Kind: recoveryset.ObjectRootServingArtifact, URI: seal.ArtifactRoot, VersionID: "v2", Digest: seal.ArtifactRootDigest, ProviderRecoveryFrontier: "s3-version:v2"},
		}, Compatibility: admission.Compatibility,
		FenceEpoch: 1, AuditIdentity: "recovery-selection", Status: recoveryset.StatusPublished, PublishedValidationAttemptID: validationID, CreatedBy: "operator", CreatedAt: time.Now().UTC(),
	}
	if err := set.Validate(); err != nil {
		t.Fatalf("recovery fixture: %v", err)
	}
	validationStarted := time.Now().UTC().Truncate(time.Microsecond)
	validationEnvelope, err := recoveryset.NewValidationEvidenceEnvelope(set, validationID)
	if err != nil {
		t.Fatalf("validation envelope: %v", err)
	}
	validationResult, err := recoveryset.NewValidationResult(validationEnvelope, validationStarted.Add(500*time.Millisecond))
	if err != nil {
		t.Fatalf("validation result: %v", err)
	}
	return postgresDeliveryStartupRecoveryFixture{
		set: set,
		authority: &postgresDeliveryStartupAuthorityFake{
			target:     appdeploymentpostgres.StartupTarget{TargetID: startupTarget, ProjectID: startupProject, Environment: string(startupEnvironment), TargetRevision: 2, ActiveGenerationID: generationID, ActivePublicationID: publicationID},
			generation: generation, publication: appdeploymentpostgres.StartupPublication{PublicationID: publicationID, TargetID: startupTarget, GenerationID: generationID, CandidateID: startupCandidate, SnapshotSealID: sealID, State: "committed", ExpectedTargetRevision: 1, ResultTargetRevision: 2}, seal: seal,
		},
		serving:  &postgresDeliveryStartupServingFake{state: servingstate.State{ID: servingstate.ID(generationID), ProjectID: startupProject, Environment: startupEnvironment, Status: servingstate.StatusActive, Digest: artifact.Digest, DuckLakeSnapshotID: 42}, artifact: artifact},
		physical: postgresDeliveryStartupPhysicalFake{contract: physicalpool.AdmissionContract{Pool: pool, Admission: admission}},
		// The published pointer must resolve to one exact passed attempt and
		// matching immutable result evidence during readiness.
		validation: recoveryset.ValidationAttempt{
			AttemptID: validationID, SetID: setID, OwnerID: "validator", FenceEpoch: set.FenceEpoch,
			AuditIdentity: "recovery-selection", Status: recoveryset.ValidationPassed, ResultDigest: validationResult.ResultDigest,
			StartedAt: validationStarted, CompletedAt: validationStarted.Add(time.Second),
		},
		result: validationResult,
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
	pool, err := physicalpool.NewPhysicalPool(physicalpool.PoolIdentity{StorageLocation: "s3://bucket/data", StorageNamespace: "namespace", Region: "region", Tenant: "tenant", EncryptionDomain: "encryption", IsolationBoundary: "target", RetentionAuthority: "retention", Compatibility: tuple})
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
