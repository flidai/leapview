package transitionpreflight

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestAuthoritativeResolverEvaluatesOwnerProjectionsDeterministically(t *testing.T) {
	resolver, request, _ := resolverFixture(t)
	targetDigest, err := (TargetIdentity{TargetID: "target", TargetRevision: 1}).Digest()
	if err != nil {
		t.Fatal(err)
	}
	if targetDigest != "sha256:5054051d5bb2ded8859edfbe25d2afda75f25636bcb927bffaffb766f7f0eabf" {
		t.Fatalf("target identity digest = %s", targetDigest)
	}
	first, err := resolver.ResolveAndEvaluate(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := resolver.ResolveAndEvaluate(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Evidence.Decision != DecisionBinaryRollbackCompatible {
		t.Fatalf("decision = %q", first.Evidence.Decision)
	}
	if first.EvidenceDigest != second.EvidenceDigest || string(first.CanonicalEvidence) != string(second.CanonicalEvidence) {
		t.Fatal("identical authoritative inputs produced different evidence")
	}
	parsed, err := ParseEvidence(first.CanonicalEvidence)
	if err != nil {
		t.Fatal(err)
	}
	if digest, err := parsed.Digest(); err != nil || digest != first.EvidenceDigest {
		t.Fatalf("round-trip digest = %q, %v", digest, err)
	}
}

func TestAuthoritativeResolverPreservesProviderRecoveryDecision(t *testing.T) {
	resolver, request, input := resolverFixture(t)
	input.Control.Compatibility = CompatibilityIncompatible
	input.ReleasePolicy.Rules[0].Decision = DecisionProviderRecoveryRequired
	input.ReleasePolicy.Digest, _ = input.ReleasePolicy.ContentDigest()
	resolver.migrations = MigrationAuthorityFunc(func(context.Context, TargetIdentity, ArtifactIdentity, ArtifactIdentity) (MigrationResolution, error) {
		return migrationFromInput(input), nil
	})
	resolver.policies = ReleasePolicyAuthorityFunc(func(context.Context, ArtifactIdentity, ArtifactIdentity) (ReleasePolicy, error) {
		return input.ReleasePolicy, nil
	})
	result, err := resolver.ResolveAndEvaluate(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Evidence.Decision != DecisionProviderRecoveryRequired {
		t.Fatalf("decision = %q", result.Evidence.Decision)
	}
}

func TestAuthoritativeResolverRejectsOwnerFailuresBeforeEvaluation(t *testing.T) {
	tests := map[string]struct {
		mutate func(*Resolver, *ResolutionRequest, *Input)
		want   error
	}{
		"missing predecessor": {func(r *Resolver, _ *ResolutionRequest, _ *Input) {
			r.artifacts = ArtifactAuthorityFunc(func(_ context.Context, ref string) (ArtifactIdentity, error) {
				if ref == "predecessor" {
					return ArtifactIdentity{}, ErrResolutionNotFound
				}
				return testInput().Candidate, nil
			})
		}, ErrResolutionNotFound},
		"missing candidate": {func(r *Resolver, _ *ResolutionRequest, in *Input) {
			r.artifacts = ArtifactAuthorityFunc(func(_ context.Context, ref string) (ArtifactIdentity, error) {
				if ref == "candidate" {
					return ArtifactIdentity{}, ErrResolutionNotFound
				}
				return in.Predecessor, nil
			})
		}, ErrResolutionNotFound},
		"ambiguous candidate": {func(r *Resolver, _ *ResolutionRequest, in *Input) {
			r.artifacts = ArtifactAuthorityFunc(func(_ context.Context, ref string) (ArtifactIdentity, error) {
				if ref == "candidate" {
					return ArtifactIdentity{}, ErrResolutionConflict
				}
				return in.Predecessor, nil
			})
		}, ErrResolutionConflict},
		"mutable candidate": {func(r *Resolver, _ *ResolutionRequest, in *Input) {
			r.artifacts = ArtifactAuthorityFunc(func(_ context.Context, ref string) (ArtifactIdentity, error) {
				if ref == "predecessor" {
					return in.Predecessor, nil
				}
				candidate := in.Candidate
				candidate.Release.Image = "ghcr.io/flidai/leapview:latest"
				return candidate, nil
			})
		}, ErrMutableArtifact},
		"ownership conflict": {func(r *Resolver, _ *ResolutionRequest, in *Input) {
			m := migrationFromInput(*in)
			m.Ownership.GooseControlSchemaOwner = OwnerRiver
			r.migrations = MigrationAuthorityFunc(func(context.Context, TargetIdentity, ArtifactIdentity, ArtifactIdentity) (MigrationResolution, error) {
				return m, nil
			})
		}, ErrResolutionConflict},
		"unknown ducklake authority": {func(r *Resolver, _ *ResolutionRequest, in *Input) {
			m := migrationFromInput(*in)
			m.DuckLakeOwner = ""
			r.migrations = MigrationAuthorityFunc(func(context.Context, TargetIdentity, ArtifactIdentity, ArtifactIdentity) (MigrationResolution, error) {
				return m, nil
			})
		}, ErrInvalidOwnerProjection},
		"missing frontier": {func(r *Resolver, _ *ResolutionRequest, _ *Input) {
			r.frontiers = RecoveryFrontierAuthorityFunc(func(context.Context, RecoveryFrontierRef, TargetIdentity) (RecoveryFrontierRef, error) {
				return RecoveryFrontierRef{}, ErrResolutionNotFound
			})
		}, ErrResolutionNotFound},
		"stale frontier": {func(r *Resolver, _ *ResolutionRequest, _ *Input) {
			r.frontiers = RecoveryFrontierAuthorityFunc(func(context.Context, RecoveryFrontierRef, TargetIdentity) (RecoveryFrontierRef, error) {
				return RecoveryFrontierRef{}, ErrStaleFrontier
			})
		}, ErrStaleFrontier},
		"frontier target mismatch": {func(r *Resolver, _ *ResolutionRequest, in *Input) {
			r.frontiers = RecoveryFrontierAuthorityFunc(func(_ context.Context, ref RecoveryFrontierRef, _ TargetIdentity) (RecoveryFrontierRef, error) {
				ref.TargetIdentityDigest = "sha256:" + strings.Repeat("9", 64)
				return ref, nil
			})
			_ = in
		}, ErrResolutionMismatch},
		"requested frontier target mismatch": {func(_ *Resolver, request *ResolutionRequest, _ *Input) {
			request.RecoveryFrontier.TargetIdentityDigest = "sha256:" + strings.Repeat("6", 64)
		}, ErrResolutionMismatch},
		"migration artifact mismatch": {func(r *Resolver, _ *ResolutionRequest, in *Input) {
			m := migrationFromInput(*in)
			m.CandidateArtifactDigest = "sha256:" + strings.Repeat("7", 64)
			r.migrations = MigrationAuthorityFunc(func(context.Context, TargetIdentity, ArtifactIdentity, ArtifactIdentity) (MigrationResolution, error) {
				return m, nil
			})
		}, ErrResolutionMismatch},
		"unsupported policy": {func(r *Resolver, _ *ResolutionRequest, in *Input) {
			policy := in.ReleasePolicy
			policy.Version = "release-policy/v9"
			r.policies = ReleasePolicyAuthorityFunc(func(context.Context, ArtifactIdentity, ArtifactIdentity) (ReleasePolicy, error) { return policy, nil })
		}, ErrInvalidOwnerProjection},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			resolver, request, input := resolverFixture(t)
			tc.mutate(resolver, &request, &input)
			if _, err := resolver.ResolveAndEvaluate(t.Context(), request); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestAuthoritativeResolverEmitsUnsupportedEvidenceForPolicyMismatch(t *testing.T) {
	resolver, request, input := resolverFixture(t)
	policy := input.ReleasePolicy
	policy.Rules[0].CandidateArtifactDigest = "sha256:" + strings.Repeat("8", 64)
	policy.Digest, _ = policy.ContentDigest()
	resolver.policies = ReleasePolicyAuthorityFunc(func(context.Context, ArtifactIdentity, ArtifactIdentity) (ReleasePolicy, error) { return policy, nil })
	result, err := resolver.ResolveAndEvaluate(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Evidence.Decision != DecisionUnsupported || !containsReason(result.Evidence.ReasonCodes, ReasonPolicyMismatch) {
		t.Fatalf("evidence = %#v", result.Evidence)
	}
}

func TestAuthoritativeResolverEmitsUnsupportedEvidenceForUnknownMigrationState(t *testing.T) {
	resolver, request, input := resolverFixture(t)
	migration := migrationFromInput(input)
	migration.Control.Compatibility = CompatibilityUnknown
	resolver.migrations = MigrationAuthorityFunc(func(context.Context, TargetIdentity, ArtifactIdentity, ArtifactIdentity) (MigrationResolution, error) {
		return migration, nil
	})
	result, err := resolver.ResolveAndEvaluate(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Evidence.Decision != DecisionUnsupported || !containsReason(result.Evidence.ReasonCodes, ReasonUnknownControlCompatibility) {
		t.Fatalf("evidence = %#v", result.Evidence)
	}
}

func TestAuthoritativeResolverRejectsTypedNilAuthority(t *testing.T) {
	resolver, request, _ := resolverFixture(t)
	resolver.artifacts = ArtifactAuthorityFunc(nil)
	if _, err := resolver.ResolveAndEvaluate(t.Context(), request); !errors.Is(err, ErrResolutionNotFound) {
		t.Fatalf("error = %v, want ErrResolutionNotFound", err)
	}
}

func resolverFixture(t *testing.T) (*Resolver, ResolutionRequest, Input) {
	t.Helper()
	input := testInput()
	target := TargetIdentity{TargetID: "target", TargetRevision: 1}
	targetDigest, err := target.Digest()
	if err != nil {
		t.Fatal(err)
	}
	input.TargetIdentityDigest = targetDigest
	input.Control.TargetIdentityDigest = targetDigest
	input.River.TargetIdentityDigest = targetDigest
	input.DuckLake.TargetIdentityDigest = targetDigest
	input.RecoveryFrontier.TargetIdentityDigest = targetDigest
	request := ResolutionRequest{PredecessorRef: "predecessor", CandidateRef: "candidate", TargetRef: "target", RecoveryFrontier: *input.RecoveryFrontier}
	artifacts := ArtifactAuthorityFunc(func(_ context.Context, ref string) (ArtifactIdentity, error) {
		switch ref {
		case "predecessor":
			return input.Predecessor, nil
		case "candidate":
			return input.Candidate, nil
		default:
			return ArtifactIdentity{}, ErrResolutionNotFound
		}
	})
	targets := TargetAuthorityFunc(func(_ context.Context, ref string) (TargetIdentity, error) {
		if ref != "target" {
			return TargetIdentity{}, ErrResolutionNotFound
		}
		return target, nil
	})
	migrations := MigrationAuthorityFunc(func(context.Context, TargetIdentity, ArtifactIdentity, ArtifactIdentity) (MigrationResolution, error) {
		return migrationFromInput(input), nil
	})
	policies := ReleasePolicyAuthorityFunc(func(context.Context, ArtifactIdentity, ArtifactIdentity) (ReleasePolicy, error) {
		return input.ReleasePolicy, nil
	})
	frontiers := RecoveryFrontierAuthorityFunc(func(_ context.Context, ref RecoveryFrontierRef, _ TargetIdentity) (RecoveryFrontierRef, error) {
		ref.TargetIdentityDigest = targetDigest
		return ref, nil
	})
	return NewResolver(artifacts, targets, migrations, policies, frontiers), request, input
}

func migrationFromInput(input Input) MigrationResolution {
	predDigest, _ := input.Predecessor.Digest()
	candidateDigest, _ := input.Candidate.Digest()
	return MigrationResolution{PredecessorArtifactDigest: predDigest, CandidateArtifactDigest: candidateDigest, Ownership: input.MigrationOwnership, DuckLakeOwner: OwnerLeapView, Control: input.Control, River: input.River, DuckLake: input.DuckLake}
}
