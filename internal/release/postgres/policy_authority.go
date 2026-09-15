package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	releasedb "github.com/flidai/leapview/internal/release/postgres/internal/db"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const maxStoredReleasePolicyBytes = 262144

// The transition policy is a release-owned immutable projection.  Keep these
// errors separate from ordinary release lifecycle errors so a caller can
// distinguish a missing policy from a malformed or conflicting publication.
var (
	ErrPolicyInvalid         = errors.New("invalid release transition policy")
	ErrPolicyNotFound        = errors.New("release transition policy not found")
	ErrPolicyConflict        = errors.New("release transition policy conflict")
	ErrPolicyDigestMismatch  = errors.New("release transition policy digest mismatch")
	ErrPolicyBindingMismatch = errors.New("release transition policy artifact binding mismatch")
)

// PublishReleasePolicy writes one trusted policy and exactly replays an
// existing row for the same artifact pair. The database grants this path only
// to the maintenance authority; production runtime callers have SELECT only.
// It never upserts an existing policy: a different policy for the pair is a
// hard conflict.
func (r *Repository) PublishReleasePolicy(ctx context.Context, predecessor, candidate transitionpreflight.ArtifactIdentity, policy transitionpreflight.ReleasePolicy) (transitionpreflight.ReleasePolicy, error) {
	if r == nil || r.db == nil {
		return transitionpreflight.ReleasePolicy{}, ErrPolicyInvalid
	}
	b, ok := r.db.(beginner)
	if !ok {
		return transitionpreflight.ReleasePolicy{}, errors.New("release PostgreSQL database does not support transactions")
	}
	var published transitionpreflight.ReleasePolicy
	err := pgx.BeginFunc(ctx, b, func(tx pgx.Tx) error {
		var err error
		published, err = r.PublishReleasePolicyTx(ctx, tx, predecessor, candidate, policy)
		return err
	})
	return published, err
}

// PublishReleasePolicyTx is the caller-owned transaction form.  The caller
// owns commit and rollback; this method only appends and verifies evidence.
func (r *Repository) PublishReleasePolicyTx(ctx context.Context, tx Tx, predecessor, candidate transitionpreflight.ArtifactIdentity, policy transitionpreflight.ReleasePolicy) (transitionpreflight.ReleasePolicy, error) {
	if tx == nil {
		return transitionpreflight.ReleasePolicy{}, ErrPolicyInvalid
	}
	predecessorDigest, candidateDigest, err := validatePolicyPair(predecessor, candidate)
	if err != nil {
		return transitionpreflight.ReleasePolicy{}, err
	}
	canonicalPolicy, encoded, err := trustedPolicy(policy, predecessorDigest, candidateDigest)
	if err != nil {
		return transitionpreflight.ReleasePolicy{}, err
	}
	q := releasedb.New(tx)
	if _, err := q.InsertReleaseTransitionPolicy(ctx, releasedb.InsertReleaseTransitionPolicyParams{
		PredecessorArtifactDigest: predecessorDigest,
		CandidateArtifactDigest:   candidateDigest,
		PolicyVersion:             canonicalPolicy.Version,
		PolicyDigest:              canonicalPolicy.Digest,
		PolicyJson:                encoded,
	}); err != nil {
		return transitionpreflight.ReleasePolicy{}, mapPolicyDatabaseError(err)
	}
	row, err := q.GetReleaseTransitionPolicy(ctx, releasedb.GetReleaseTransitionPolicyParams{
		PredecessorArtifactDigest: predecessorDigest,
		CandidateArtifactDigest:   candidateDigest,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return transitionpreflight.ReleasePolicy{}, ErrPolicyNotFound
	}
	if err != nil {
		return transitionpreflight.ReleasePolicy{}, err
	}
	stored, err := readPolicyRow(row, predecessorDigest, candidateDigest)
	if err != nil {
		return transitionpreflight.ReleasePolicy{}, err
	}
	if !samePolicyJSON(stored, canonicalPolicy) || stored.Digest != canonicalPolicy.Digest || stored.Version != canonicalPolicy.Version {
		return transitionpreflight.ReleasePolicy{}, ErrPolicyConflict
	}
	return stored, nil
}

// ResolveReleasePolicy implements the read-only authority consumed by the
// transition preflight resolver.  It selects one exact pair and has no
// latest/current fallback.
func (r *Repository) ResolveReleasePolicy(ctx context.Context, predecessor, candidate transitionpreflight.ArtifactIdentity) (transitionpreflight.ReleasePolicy, error) {
	if r == nil || r.db == nil {
		return transitionpreflight.ReleasePolicy{}, ErrPolicyInvalid
	}
	predecessorDigest, candidateDigest, err := validatePolicyPair(predecessor, candidate)
	if err != nil {
		return transitionpreflight.ReleasePolicy{}, err
	}
	return r.resolveReleasePolicy(ctx, r.db, predecessorDigest, candidateDigest)
}

func (r *Repository) resolveReleasePolicy(ctx context.Context, db DBTX, predecessorDigest, candidateDigest string) (transitionpreflight.ReleasePolicy, error) {
	row, err := releasedb.New(db).GetReleaseTransitionPolicy(ctx, releasedb.GetReleaseTransitionPolicyParams{
		PredecessorArtifactDigest: predecessorDigest,
		CandidateArtifactDigest:   candidateDigest,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return transitionpreflight.ReleasePolicy{}, ErrPolicyNotFound
	}
	if err != nil {
		return transitionpreflight.ReleasePolicy{}, err
	}
	return readPolicyRow(row, predecessorDigest, candidateDigest)
}

func validatePolicyPair(predecessor, candidate transitionpreflight.ArtifactIdentity) (string, string, error) {
	predecessorDigest, err := predecessor.Digest()
	if err != nil {
		return "", "", fmt.Errorf("%w: predecessor artifact: %v", ErrPolicyInvalid, err)
	}
	candidateDigest, err := candidate.Digest()
	if err != nil {
		return "", "", fmt.Errorf("%w: candidate artifact: %v", ErrPolicyInvalid, err)
	}
	if predecessorDigest == candidateDigest || predecessor.Release.Image == candidate.Release.Image {
		return "", "", fmt.Errorf("%w: predecessor and candidate must differ", ErrPolicyInvalid)
	}
	return predecessorDigest, candidateDigest, nil
}

// trustedPolicy canonicalizes the policy before storage and requires the
// caller-provided digest to already agree with the policy content.  The
// publisher is therefore not an authority that silently repairs untrusted
// policy metadata.
func trustedPolicy(policy transitionpreflight.ReleasePolicy, predecessorDigest, candidateDigest string) (transitionpreflight.ReleasePolicy, []byte, error) {
	if policy.Version != strings.TrimSpace(policy.Version) || policy.Digest != strings.TrimSpace(policy.Digest) {
		return transitionpreflight.ReleasePolicy{}, nil, ErrPolicyInvalid
	}
	if policy.Version != transitionpreflight.ReleasePolicyVersion {
		return transitionpreflight.ReleasePolicy{}, nil, fmt.Errorf("%w: unsupported policy version", ErrPolicyInvalid)
	}
	if policy.Digest == "" || platformdigest.ValidateSHA256Identity(policy.Digest) != nil {
		return transitionpreflight.ReleasePolicy{}, nil, ErrPolicyDigestMismatch
	}
	expectedDigest, err := policy.ContentDigest()
	if err != nil {
		return transitionpreflight.ReleasePolicy{}, nil, fmt.Errorf("%w: %v", ErrPolicyInvalid, err)
	}
	if expectedDigest != policy.Digest {
		return transitionpreflight.ReleasePolicy{}, nil, ErrPolicyDigestMismatch
	}
	canonical := policy
	canonical.Version = strings.TrimSpace(canonical.Version)
	canonical.Digest = expectedDigest
	canonical.Rules = append([]transitionpreflight.ReleasePolicyRule(nil), policy.Rules...)
	for i := range canonical.Rules {
		canonical.Rules[i].PredecessorArtifactDigest = strings.TrimSpace(canonical.Rules[i].PredecessorArtifactDigest)
		canonical.Rules[i].CandidateArtifactDigest = strings.TrimSpace(canonical.Rules[i].CandidateArtifactDigest)
		canonical.Rules[i].RollbackFromArtifactDigest = strings.TrimSpace(canonical.Rules[i].RollbackFromArtifactDigest)
		canonical.Rules[i].RollbackToArtifactDigest = strings.TrimSpace(canonical.Rules[i].RollbackToArtifactDigest)
		canonical.Rules[i].Decision = transitionpreflight.Decision(strings.TrimSpace(string(canonical.Rules[i].Decision)))
		switch canonical.Rules[i].Decision {
		case transitionpreflight.DecisionBinaryRollbackCompatible, transitionpreflight.DecisionProviderRecoveryRequired:
		default:
			return transitionpreflight.ReleasePolicy{}, nil, fmt.Errorf("%w: unsupported policy decision", ErrPolicyInvalid)
		}
	}
	sort.Slice(canonical.Rules, func(i, j int) bool {
		left, right := canonical.Rules[i], canonical.Rules[j]
		if left.PredecessorArtifactDigest != right.PredecessorArtifactDigest {
			return left.PredecessorArtifactDigest < right.PredecessorArtifactDigest
		}
		if left.CandidateArtifactDigest != right.CandidateArtifactDigest {
			return left.CandidateArtifactDigest < right.CandidateArtifactDigest
		}
		if left.RollbackFromArtifactDigest != right.RollbackFromArtifactDigest {
			return left.RollbackFromArtifactDigest < right.RollbackFromArtifactDigest
		}
		if left.RollbackToArtifactDigest != right.RollbackToArtifactDigest {
			return left.RollbackToArtifactDigest < right.RollbackToArtifactDigest
		}
		return left.Decision < right.Decision
	})
	matching := 0
	for _, rule := range canonical.Rules {
		if rule.PredecessorArtifactDigest == predecessorDigest &&
			rule.CandidateArtifactDigest == candidateDigest &&
			rule.RollbackFromArtifactDigest == candidateDigest &&
			rule.RollbackToArtifactDigest == predecessorDigest {
			matching++
		}
	}
	if matching != 1 {
		return transitionpreflight.ReleasePolicy{}, nil, fmt.Errorf("%w: expected one matching rollback rule", ErrPolicyBindingMismatch)
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return transitionpreflight.ReleasePolicy{}, nil, fmt.Errorf("%w: encode policy: %v", ErrPolicyInvalid, err)
	}
	if len(encoded) > maxStoredReleasePolicyBytes {
		return transitionpreflight.ReleasePolicy{}, nil, fmt.Errorf("%w: policy exceeds bounded storage size", ErrPolicyInvalid)
	}
	return canonical, encoded, nil
}

func readPolicyRow(row releasedb.GetReleaseTransitionPolicyRow, predecessorDigest, candidateDigest string) (transitionpreflight.ReleasePolicy, error) {
	if row.PredecessorArtifactDigest != predecessorDigest || row.CandidateArtifactDigest != candidateDigest {
		return transitionpreflight.ReleasePolicy{}, ErrPolicyBindingMismatch
	}
	if row.PolicyVersion != transitionpreflight.ReleasePolicyVersion || row.PolicyJson == "" {
		return transitionpreflight.ReleasePolicy{}, ErrPolicyInvalid
	}
	policy, err := decodePolicyJSON(row.PolicyJson)
	if err != nil {
		return transitionpreflight.ReleasePolicy{}, fmt.Errorf("%w: decode policy: %v", ErrPolicyInvalid, err)
	}
	if policy.Version != row.PolicyVersion || policy.Digest != row.PolicyDigest {
		return transitionpreflight.ReleasePolicy{}, ErrPolicyDigestMismatch
	}
	canonical, _, err := trustedPolicy(policy, predecessorDigest, candidateDigest)
	if err != nil {
		return transitionpreflight.ReleasePolicy{}, err
	}
	if canonical.Digest != row.PolicyDigest {
		return transitionpreflight.ReleasePolicy{}, ErrPolicyDigestMismatch
	}
	return canonical, nil
}

func decodePolicyJSON(raw string) (transitionpreflight.ReleasePolicy, error) {
	decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
	decoder.DisallowUnknownFields()
	var policy transitionpreflight.ReleasePolicy
	if err := decoder.Decode(&policy); err != nil {
		return transitionpreflight.ReleasePolicy{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return transitionpreflight.ReleasePolicy{}, errors.New("policy JSON has trailing values")
		}
		return transitionpreflight.ReleasePolicy{}, err
	}
	return policy, nil
}

func samePolicyJSON(left, right transitionpreflight.ReleasePolicy) bool {
	leftBytes, leftErr := json.Marshal(left)
	rightBytes, rightErr := json.Marshal(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	var leftValue, rightValue any
	if json.Unmarshal(leftBytes, &leftValue) != nil || json.Unmarshal(rightBytes, &rightValue) != nil {
		return false
	}
	leftBytes, leftErr = json.Marshal(leftValue)
	rightBytes, rightErr = json.Marshal(rightValue)
	return leftErr == nil && rightErr == nil && string(leftBytes) == string(rightBytes)
}

func mapPolicyDatabaseError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation: an existing exact pair is read back.
			return ErrPolicyConflict
		case "23514", "22P02": // check_violation / invalid_text_representation.
			return ErrPolicyInvalid
		}
	}
	return err
}
