package postgres

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/platform/compatibility"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestReleasePolicyAuthorityPublishesResolvesAndEvaluates(t *testing.T) {
	pool := testDB(t)
	repository := New(pool)
	predecessor := policyArtifact("v1.0.0", "1", "a", "d")
	candidate := policyArtifact("v1.1.0", "2", "b", "e")
	policy := policyForPair(t, predecessor, candidate, transitionpreflight.DecisionBinaryRollbackCompatible)

	published, err := repository.PublishReleasePolicy(t.Context(), predecessor, candidate, policy)
	if err != nil {
		t.Fatalf("publish release policy: %v", err)
	}
	replayed, err := repository.PublishReleasePolicy(t.Context(), predecessor, candidate, policy)
	if err != nil {
		t.Fatalf("replay release policy: %v", err)
	}
	resolved, err := repository.ResolveReleasePolicy(t.Context(), predecessor, candidate)
	if err != nil {
		t.Fatalf("resolve release policy: %v", err)
	}
	if !reflect.DeepEqual(published, replayed) || !reflect.DeepEqual(published, resolved) {
		t.Fatalf("published/replayed/resolved policy differ:\n%#v\n%#v\n%#v", published, replayed, resolved)
	}

	evidence, err := transitionpreflight.Evaluate(policyEvaluationInput(predecessor, candidate, resolved))
	if err != nil {
		t.Fatalf("evaluate authoritative policy: %v", err)
	}
	if evidence.Decision != transitionpreflight.DecisionBinaryRollbackCompatible || len(evidence.ReasonCodes) != 0 {
		t.Fatalf("authoritative policy decision = %q reasons=%v", evidence.Decision, evidence.ReasonCodes)
	}
}

func TestReleasePolicyAuthorityReadbackAcrossRepositoryRestart(t *testing.T) {
	pool := testDB(t)
	predecessor := policyArtifact("v1.0.0", "1", "a", "d")
	candidate := policyArtifact("v1.1.0", "2", "b", "e")
	policy := policyForPair(t, predecessor, candidate, transitionpreflight.DecisionBinaryRollbackCompatible)

	published, err := New(pool).PublishReleasePolicy(t.Context(), predecessor, candidate, policy)
	if err != nil {
		t.Fatalf("publish release policy: %v", err)
	}

	reopened, err := pgxpool.New(t.Context(), pool.Config().ConnString())
	if err != nil {
		t.Fatalf("reopen PostgreSQL pool: %v", err)
	}
	t.Cleanup(reopened.Close)
	resolved, err := New(reopened).ResolveReleasePolicy(t.Context(), predecessor, candidate)
	if err != nil {
		t.Fatalf("resolve release policy after repository restart: %v", err)
	}
	if !reflect.DeepEqual(published, resolved) {
		t.Fatalf("policy changed across repository restart:\n%#v\n%#v", published, resolved)
	}
}

func TestReleasePolicyAuthorityFailsClosed(t *testing.T) {
	pool := testDB(t)
	repository := New(pool)
	predecessor := policyArtifact("v1.0.0", "1", "a", "d")
	candidate := policyArtifact("v1.1.0", "2", "b", "e")
	policy := policyForPair(t, predecessor, candidate, transitionpreflight.DecisionBinaryRollbackCompatible)

	for name, pair := range map[string][2]transitionpreflight.ArtifactIdentity{
		"unknown predecessor": {policyArtifact("v0.9.0", "3", "c", "3"), candidate},
		"unknown candidate":   {predecessor, policyArtifact("v1.2.0", "4", "4", "5")},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := repository.ResolveReleasePolicy(t.Context(), pair[0], pair[1]); !errors.Is(err, ErrPolicyNotFound) {
				t.Fatalf("resolve error = %v, want policy not found", err)
			}
		})
	}

	t.Run("unsupported version", func(t *testing.T) {
		invalid := policy
		invalid.Version = "release-policy/v2"
		if _, err := repository.PublishReleasePolicy(t.Context(), predecessor, candidate, invalid); !errors.Is(err, ErrPolicyInvalid) {
			t.Fatalf("publish error = %v, want invalid policy", err)
		}
	})

	t.Run("modified digest", func(t *testing.T) {
		invalid := policy
		invalid.Digest = digest("9")
		if _, err := repository.PublishReleasePolicy(t.Context(), predecessor, candidate, invalid); !errors.Is(err, ErrPolicyDigestMismatch) {
			t.Fatalf("publish error = %v, want digest mismatch", err)
		}
	})

	t.Run("ambiguous matching rules", func(t *testing.T) {
		invalid := policy
		invalid.Rules = append(invalid.Rules, invalid.Rules[0])
		invalid.Digest, _ = invalid.ContentDigest()
		if _, err := repository.PublishReleasePolicy(t.Context(), predecessor, candidate, invalid); !errors.Is(err, ErrPolicyBindingMismatch) {
			t.Fatalf("publish error = %v, want binding mismatch", err)
		}
	})

	if _, err := repository.PublishReleasePolicy(t.Context(), predecessor, candidate, policy); err != nil {
		t.Fatalf("publish initial policy: %v", err)
	}
	conflicting := policyForPair(t, predecessor, candidate, transitionpreflight.DecisionProviderRecoveryRequired)
	if _, err := repository.PublishReleasePolicy(t.Context(), predecessor, candidate, conflicting); !errors.Is(err, ErrPolicyConflict) {
		t.Fatalf("conflicting publication error = %v, want conflict", err)
	}

	staleCandidate := candidate
	staleCandidate.Release.SourceRevision = strings.Repeat("6", 40)
	if _, err := repository.ResolveReleasePolicy(t.Context(), predecessor, staleCandidate); !errors.Is(err, ErrPolicyNotFound) {
		t.Fatalf("stale identity error = %v, want policy not found", err)
	}

	for name, statement := range map[string]string{
		"update":   `UPDATE release.release_transition_policy SET policy_digest = '` + digest("7") + `'`,
		"delete":   `DELETE FROM release.release_transition_policy`,
		"truncate": `TRUNCATE release.release_transition_policy`,
	} {
		t.Run("immutable "+name, func(t *testing.T) {
			if _, err := pool.Exec(t.Context(), statement); err == nil {
				t.Fatalf("%s unexpectedly mutated policy authority", name)
			}
		})
	}
}

func TestReleasePolicyAuthorityRejectsCorruptStoredProjection(t *testing.T) {
	pool := testDB(t)
	repository := New(pool)
	predecessor := policyArtifact("v2.0.0", "7", "7", "8")
	candidate := policyArtifact("v2.1.0", "8", "8", "9")
	predecessorDigest, _ := predecessor.Digest()
	candidateDigest, _ := candidate.Digest()
	policy := policyForPair(t, predecessor, candidate, transitionpreflight.DecisionBinaryRollbackCompatible)
	policy.Digest = digest("0")
	document, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO release.release_transition_policy
		(predecessor_artifact_digest, candidate_artifact_digest, policy_version, policy_digest, policy_json)
		VALUES ($1, $2, $3, $4, $5::jsonb)`, predecessorDigest, candidateDigest, policy.Version, policy.Digest, document); err != nil {
		t.Fatalf("seed corrupt owner row: %v", err)
	}
	if _, err := repository.ResolveReleasePolicy(t.Context(), predecessor, candidate); !errors.Is(err, ErrPolicyDigestMismatch) {
		t.Fatalf("resolve corrupt row error = %v, want digest mismatch", err)
	}
}

func TestReleasePolicyAuthorityRejectsMissingStoredPolicyFields(t *testing.T) {
	pool := testDB(t)
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO release.release_transition_policy
		(predecessor_artifact_digest, candidate_artifact_digest, policy_version, policy_digest, policy_json)
		VALUES ($1, $2, $3, $4, '{}'::jsonb)`, digest("a"), digest("b"), transitionpreflight.ReleasePolicyVersion, digest("c")); err == nil {
		t.Fatal("stored policy without required JSON fields unexpectedly succeeded")
	}
}

func TestReleasePolicyAuthorityCanonicalizesRuleOrder(t *testing.T) {
	pool := testDB(t)
	repository := New(pool)
	predecessor := policyArtifact("v3.0.0", "a", "1", "2")
	candidate := policyArtifact("v3.1.0", "b", "2", "3")
	policy := policyForPair(t, predecessor, candidate, transitionpreflight.DecisionBinaryRollbackCompatible)
	extraPredecessor := policyArtifact("v2.9.0", "c", "3", "4")
	extraCandidate := policyArtifact("v2.9.1", "d", "4", "5")
	extra := policyForPair(t, extraPredecessor, extraCandidate, transitionpreflight.DecisionProviderRecoveryRequired).Rules[0]
	policy.Rules = append([]transitionpreflight.ReleasePolicyRule{extra}, policy.Rules...)
	policy.Digest, _ = policy.ContentDigest()

	first, err := repository.PublishReleasePolicy(t.Context(), predecessor, candidate, policy)
	if err != nil {
		t.Fatalf("publish reordered policy: %v", err)
	}
	reordered := policy
	reordered.Rules = []transitionpreflight.ReleasePolicyRule{policy.Rules[1], policy.Rules[0]}
	second, err := repository.PublishReleasePolicy(t.Context(), predecessor, candidate, reordered)
	if err != nil {
		t.Fatalf("replay reordered policy: %v", err)
	}
	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("canonical policy differs by input order:\n%s\n%s", firstJSON, secondJSON)
	}
}

func policyArtifact(version, revisionDigit, imageDigit, admissionDigit string) transitionpreflight.ArtifactIdentity {
	return transitionpreflight.ArtifactIdentity{
		Release: compatibility.ReleaseIdentity{
			ReleaseID:      version,
			Version:        strings.TrimPrefix(version, "v"),
			SourceRevision: strings.Repeat(revisionDigit, 40),
			Image:          "ghcr.io/flidai/leapview@sha256:" + strings.Repeat(imageDigit, 64),
			Distribution:   "public",
			Platform:       "linux/amd64",
		},
		ArchitectureMarker:      transitionpreflight.ArchitecturePostgreSQL,
		ArtifactAdmissionDigest: digest(admissionDigit),
	}
}

func policyForPair(t *testing.T, predecessor, candidate transitionpreflight.ArtifactIdentity, decision transitionpreflight.Decision) transitionpreflight.ReleasePolicy {
	t.Helper()
	predecessorDigest, err := predecessor.Digest()
	if err != nil {
		t.Fatal(err)
	}
	candidateDigest, err := candidate.Digest()
	if err != nil {
		t.Fatal(err)
	}
	policy := transitionpreflight.ReleasePolicy{
		Version: transitionpreflight.ReleasePolicyVersion,
		Rules: []transitionpreflight.ReleasePolicyRule{{
			PredecessorArtifactDigest:  predecessorDigest,
			CandidateArtifactDigest:    candidateDigest,
			RollbackFromArtifactDigest: candidateDigest,
			RollbackToArtifactDigest:   predecessorDigest,
			Decision:                   decision,
		}},
	}
	policy.Digest, err = policy.ContentDigest()
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func policyEvaluationInput(predecessor, candidate transitionpreflight.ArtifactIdentity, policy transitionpreflight.ReleasePolicy) transitionpreflight.Input {
	targetDigest := digest("f")
	tuple := physicalpool.Compatibility{
		DuckDBRuntime:         "duckdb:1.5.4",
		DuckLakeExtension:     "ducklake:0.3.0",
		CatalogFormat:         "ducklake-catalog:v1",
		StorageImplementation: "s3",
		ObjectNamingContract:  "uuidv7:v1",
	}
	return transitionpreflight.Input{
		SchemaVersion:        transitionpreflight.SchemaVersion,
		TargetIdentityDigest: targetDigest,
		Predecessor:          predecessor,
		Candidate:            candidate,
		MigrationOwnership: transitionpreflight.MigrationOwnership{
			GooseControlSchemaOwner:     transitionpreflight.OwnerLeapView,
			RiverOperationalSchemaOwner: transitionpreflight.OwnerRiver,
			RiverJobHistoryOwner:        transitionpreflight.OwnerLeapView,
		},
		Control: transitionpreflight.PostgreSQLControlProjection{
			Compatibility:            transitionpreflight.CompatibilityBackwardCompatible,
			PredecessorSchemaVersion: "goose/v1",
			CandidateSchemaVersion:   "goose/v2",
			TargetIdentityDigest:     targetDigest,
		},
		River: transitionpreflight.RiverJobProjection{
			SchemaCompatibility:       transitionpreflight.CompatibilityBackwardCompatible,
			JobHistoryCompatibility:   transitionpreflight.CompatibilityBackwardCompatible,
			ExistingSchemaVersion:     "river/v1",
			RequiredSchemaVersion:     "river/v2",
			ExistingJobHistoryVersion: "jobs/v1",
			RequiredJobHistoryVersion: "jobs/v2",
			TargetIdentityDigest:      targetDigest,
		},
		DuckLake: transitionpreflight.DuckLakeProjection{
			Compatibility:        transitionpreflight.CompatibilityBackwardCompatible,
			Predecessor:          tuple,
			Candidate:            tuple,
			TargetIdentityDigest: targetDigest,
		},
		RecoveryFrontier: &transitionpreflight.RecoveryFrontierRef{
			SetID:                "018f3f83-7b2f-7b37-9f9e-000000000010",
			Digest:               digest("c"),
			TargetIdentityDigest: targetDigest,
		},
		ReleasePolicy: policy,
	}
}
