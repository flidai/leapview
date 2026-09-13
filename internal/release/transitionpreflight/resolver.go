package transitionpreflight

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/platform/ociref"
)

const targetIdentityDomain = "leapview/release-transition-target/v1\n"

var (
	ErrResolution             = errors.New("transition preflight resolution failed")
	ErrResolutionNotFound     = errors.New("authoritative transition input not found")
	ErrResolutionConflict     = errors.New("authoritative transition input is ambiguous or conflicting")
	ErrInvalidOwnerProjection = errors.New("authoritative transition projection is invalid")
	ErrMutableArtifact        = errors.New("release artifact identity is mutable")
	ErrStaleFrontier          = errors.New("recovery frontier is not currently published")
	ErrResolutionMismatch     = errors.New("authoritative transition identities do not match")
)

// ResolutionRequest contains only exact lookup references. Resolved release,
// policy, migration, target, and recovery data always comes from its owner.
type ResolutionRequest struct {
	PredecessorRef   string
	CandidateRef     string
	TargetRef        string
	RecoveryFrontier RecoveryFrontierRef
}

// TargetIdentity is the immutable target projection owned by deployment.
// Its digest binds the target in every migration and recovery projection.
type TargetIdentity struct {
	TargetID       string `json:"targetId"`
	TargetRevision int64  `json:"targetRevision"`
}

func (t TargetIdentity) Digest() (string, error) {
	if err := canonicalText(t.TargetID); err != nil {
		return "", fmt.Errorf("targetId: %w", err)
	}
	if t.TargetRevision <= 0 {
		return "", errors.New("targetRevision must be positive")
	}
	encoded, err := json.Marshal(t)
	if err != nil {
		return "", fmt.Errorf("encode target identity: %w", err)
	}
	sum := sha256.Sum256(append([]byte(targetIdentityDomain), encoded...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// MigrationResolution is the single owner-produced comparison across all
// PostgreSQL-era persistent domains. DuckLakeOwner is resolution metadata;
// the existing evidence format retains the exact DuckLake tuple instead of a
// fourth ownership string.
type MigrationResolution struct {
	PredecessorArtifactDigest string
	CandidateArtifactDigest   string
	Ownership                 MigrationOwnership
	DuckLakeOwner             string
	Control                   PostgreSQLControlProjection
	River                     RiverJobProjection
	DuckLake                  DuckLakeProjection
}

type ArtifactAuthority interface {
	ResolveArtifact(context.Context, string) (ArtifactIdentity, error)
}

type TargetAuthority interface {
	ResolveTarget(context.Context, string) (TargetIdentity, error)
}

type MigrationAuthority interface {
	ResolveMigration(context.Context, TargetIdentity, ArtifactIdentity, ArtifactIdentity) (MigrationResolution, error)
}

type ReleasePolicyAuthority interface {
	ResolveReleasePolicy(context.Context, ArtifactIdentity, ArtifactIdentity) (ReleasePolicy, error)
}

type RecoveryFrontierAuthority interface {
	ResolveRecoveryFrontier(context.Context, RecoveryFrontierRef, TargetIdentity) (RecoveryFrontierRef, error)
}

// Function adapters keep composition narrow without giving the resolver a
// database, filesystem, registry, or migration execution dependency.
type ArtifactAuthorityFunc func(context.Context, string) (ArtifactIdentity, error)

func (f ArtifactAuthorityFunc) ResolveArtifact(ctx context.Context, ref string) (ArtifactIdentity, error) {
	if f == nil {
		return ArtifactIdentity{}, ErrResolutionNotFound
	}
	return f(ctx, ref)
}

type TargetAuthorityFunc func(context.Context, string) (TargetIdentity, error)

func (f TargetAuthorityFunc) ResolveTarget(ctx context.Context, ref string) (TargetIdentity, error) {
	if f == nil {
		return TargetIdentity{}, ErrResolutionNotFound
	}
	return f(ctx, ref)
}

type MigrationAuthorityFunc func(context.Context, TargetIdentity, ArtifactIdentity, ArtifactIdentity) (MigrationResolution, error)

func (f MigrationAuthorityFunc) ResolveMigration(ctx context.Context, target TargetIdentity, predecessor, candidate ArtifactIdentity) (MigrationResolution, error) {
	if f == nil {
		return MigrationResolution{}, ErrResolutionNotFound
	}
	return f(ctx, target, predecessor, candidate)
}

type ReleasePolicyAuthorityFunc func(context.Context, ArtifactIdentity, ArtifactIdentity) (ReleasePolicy, error)

func (f ReleasePolicyAuthorityFunc) ResolveReleasePolicy(ctx context.Context, predecessor, candidate ArtifactIdentity) (ReleasePolicy, error) {
	if f == nil {
		return ReleasePolicy{}, ErrResolutionNotFound
	}
	return f(ctx, predecessor, candidate)
}

type RecoveryFrontierAuthorityFunc func(context.Context, RecoveryFrontierRef, TargetIdentity) (RecoveryFrontierRef, error)

func (f RecoveryFrontierAuthorityFunc) ResolveRecoveryFrontier(ctx context.Context, ref RecoveryFrontierRef, target TargetIdentity) (RecoveryFrontierRef, error) {
	if f == nil {
		return RecoveryFrontierRef{}, ErrResolutionNotFound
	}
	return f(ctx, ref, target)
}

// Resolver owns the read-only authoritative resolution sequence.
type Resolver struct {
	artifacts  ArtifactAuthority
	targets    TargetAuthority
	migrations MigrationAuthority
	policies   ReleasePolicyAuthority
	frontiers  RecoveryFrontierAuthority
}

func NewResolver(artifacts ArtifactAuthority, targets TargetAuthority, migrations MigrationAuthority, policies ReleasePolicyAuthority, frontiers RecoveryFrontierAuthority) *Resolver {
	return &Resolver{artifacts: artifacts, targets: targets, migrations: migrations, policies: policies, frontiers: frontiers}
}

// ResolutionResult is an in-memory result, not another evidence wire format.
// CanonicalEvidence is exactly Evidence.CanonicalJSON.
type ResolutionResult struct {
	Evidence          Evidence
	CanonicalEvidence []byte
	EvidenceDigest    string
}

// ResolveAndEvaluate resolves every input from its authority, checks all
// cross-owner bindings, and then delegates to the existing evaluator.
func (r *Resolver) ResolveAndEvaluate(ctx context.Context, request ResolutionRequest) (ResolutionResult, error) {
	if r == nil || r.artifacts == nil || r.targets == nil || r.migrations == nil || r.policies == nil || r.frontiers == nil {
		return ResolutionResult{}, fmt.Errorf("%w: required authority is unavailable", ErrResolution)
	}
	for _, field := range []struct{ name, value string }{
		{"predecessorRef", request.PredecessorRef}, {"candidateRef", request.CandidateRef},
		{"targetRef", request.TargetRef}, {"recoverySetId", request.RecoveryFrontier.SetID},
	} {
		if err := canonicalText(field.value); err != nil {
			return ResolutionResult{}, fmt.Errorf("%w: %s", ErrResolution, field.name)
		}
	}

	predecessor, err := r.artifacts.ResolveArtifact(ctx, request.PredecessorRef)
	if err != nil {
		return ResolutionResult{}, ownerFailure("predecessor", err)
	}
	if err := validateResolvedArtifact(predecessor); err != nil {
		return ResolutionResult{}, fmt.Errorf("%w: predecessor: %w", ErrInvalidOwnerProjection, err)
	}
	candidate, err := r.artifacts.ResolveArtifact(ctx, request.CandidateRef)
	if err != nil {
		return ResolutionResult{}, ownerFailure("candidate", err)
	}
	if err := validateResolvedArtifact(candidate); err != nil {
		return ResolutionResult{}, fmt.Errorf("%w: candidate: %w", ErrInvalidOwnerProjection, err)
	}

	target, err := r.targets.ResolveTarget(ctx, request.TargetRef)
	if err != nil {
		return ResolutionResult{}, ownerFailure("target", err)
	}
	targetDigest, err := target.Digest()
	if err != nil {
		return ResolutionResult{}, fmt.Errorf("%w: target: %v", ErrInvalidOwnerProjection, err)
	}
	if request.RecoveryFrontier.TargetIdentityDigest != "" && request.RecoveryFrontier.TargetIdentityDigest != targetDigest {
		return ResolutionResult{}, fmt.Errorf("%w: requested recovery frontier target", ErrResolutionMismatch)
	}
	migration, err := r.migrations.ResolveMigration(ctx, target, predecessor, candidate)
	if err != nil {
		return ResolutionResult{}, ownerFailure("migration", err)
	}
	predecessorDigest, _ := predecessor.Digest()
	candidateDigest, _ := candidate.Digest()
	if err := validateMigrationResolution(migration, targetDigest, predecessorDigest, candidateDigest); err != nil {
		return ResolutionResult{}, err
	}

	policy, err := r.policies.ResolveReleasePolicy(ctx, predecessor, candidate)
	if err != nil {
		return ResolutionResult{}, ownerFailure("release policy", err)
	}
	if policy.Version != ReleasePolicyVersion {
		return ResolutionResult{}, fmt.Errorf("%w: unsupported release policy version", ErrInvalidOwnerProjection)
	}
	policyDigest, err := policy.ContentDigest()
	if err != nil || policy.Digest != policyDigest {
		return ResolutionResult{}, fmt.Errorf("%w: release policy digest", ErrInvalidOwnerProjection)
	}

	frontier, err := r.frontiers.ResolveRecoveryFrontier(ctx, request.RecoveryFrontier, target)
	if err != nil {
		return ResolutionResult{}, ownerFailure("recovery frontier", err)
	}
	if err := frontier.Validate(); err != nil {
		return ResolutionResult{}, fmt.Errorf("%w: recovery frontier: %v", ErrInvalidOwnerProjection, err)
	}
	if frontier.SetID != request.RecoveryFrontier.SetID || frontier.Digest != request.RecoveryFrontier.Digest || frontier.TargetIdentityDigest != targetDigest {
		return ResolutionResult{}, fmt.Errorf("%w: recovery frontier", ErrResolutionMismatch)
	}

	evidence, err := Evaluate(Input{
		SchemaVersion: SchemaVersion, TargetIdentityDigest: targetDigest,
		Predecessor: predecessor, Candidate: candidate,
		MigrationOwnership: migration.Ownership, Control: migration.Control,
		River: migration.River, DuckLake: migration.DuckLake,
		RecoveryFrontier: &frontier, ReleasePolicy: policy,
	})
	if err != nil {
		return ResolutionResult{}, fmt.Errorf("%w: evaluate: %v", ErrInvalidOwnerProjection, err)
	}
	canonical, err := evidence.CanonicalJSON()
	if err != nil {
		return ResolutionResult{}, fmt.Errorf("%w: canonical evidence: %v", ErrInvalidOwnerProjection, err)
	}
	digest, err := evidence.Digest()
	if err != nil || platformdigest.ValidateSHA256Identity(digest) != nil {
		return ResolutionResult{}, fmt.Errorf("%w: evidence digest", ErrInvalidOwnerProjection)
	}
	parsed, err := ParseEvidence(canonical)
	if err != nil {
		return ResolutionResult{}, fmt.Errorf("%w: evaluator emitted unreadable evidence: %v", ErrInvalidOwnerProjection, err)
	}
	parsedDigest, err := parsed.Digest()
	if err != nil || parsedDigest != digest {
		return ResolutionResult{}, fmt.Errorf("%w: evidence round trip", ErrResolutionMismatch)
	}
	return ResolutionResult{Evidence: evidence, CanonicalEvidence: append([]byte(nil), canonical...), EvidenceDigest: digest}, nil
}

func validateResolvedArtifact(artifact ArtifactIdentity) error {
	if err := ociref.ValidateImmutable(artifact.Release.Image); err != nil {
		return fmt.Errorf("%w: image", ErrMutableArtifact)
	}
	_, err := artifact.Digest()
	return err
}

func validateMigrationResolution(m MigrationResolution, targetDigest, predecessorDigest, candidateDigest string) error {
	if m.PredecessorArtifactDigest != predecessorDigest || m.CandidateArtifactDigest != candidateDigest {
		return fmt.Errorf("%w: migration artifact pair", ErrResolutionMismatch)
	}
	if m.Ownership.GooseControlSchemaOwner == "" || m.Ownership.RiverOperationalSchemaOwner == "" || m.Ownership.RiverJobHistoryOwner == "" || strings.TrimSpace(m.DuckLakeOwner) == "" {
		return fmt.Errorf("%w: migration ownership is incomplete", ErrInvalidOwnerProjection)
	}
	if m.Ownership.GooseControlSchemaOwner != GooseControlSchemaOwnerLeapView || m.Ownership.RiverOperationalSchemaOwner != RiverOperationalSchemaOwnerRiver || m.Ownership.RiverJobHistoryOwner != RiverJobHistoryOwnerLeapView || m.DuckLakeOwner != OwnerLeapView {
		return fmt.Errorf("%w: migration ownership", ErrResolutionConflict)
	}
	for _, digest := range []string{m.Control.TargetIdentityDigest, m.River.TargetIdentityDigest, m.DuckLake.TargetIdentityDigest} {
		if digest != targetDigest {
			return fmt.Errorf("%w: migration target", ErrResolutionMismatch)
		}
	}
	return nil
}

func ownerFailure(stage string, err error) error {
	if err == nil {
		return fmt.Errorf("%w: %s", ErrResolution, stage)
	}
	return fmt.Errorf("%w: %s: %w", ErrResolution, stage, err)
}
