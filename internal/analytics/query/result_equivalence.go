package query

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// ResultEquivalenceVersion identifies the planner-owned result identity wire
// contract. Bump it when the projection below changes.
const ResultEquivalenceVersion = 1

const resultEquivalenceDomain = "flid.resultidentity.result-equivalence.v1"

// ResultIdentity contains the two target-independent projections needed to
// address one planned result. Both values come from the same validated PlanIR
// serialization so cache admission does not repeat graph validation and JSON
// canonicalization.
type ResultIdentity struct {
	Dependencies      DependencyProjection
	EquivalenceDigest string
}

// ResultIdentity derives dependency and result-equivalence evidence in one
// pass while preserving the exact wire identities exposed by the independent
// ResultDependencies and ResultEquivalenceDigest methods.
func (p Plan) ResultIdentity() (ResultIdentity, error) {
	if p.IR == nil {
		return ResultIdentity{}, fmt.Errorf("plan has no PlanIR result identity evidence")
	}
	dependencies, canonical, err := p.IR.DependencyEvidence()
	if err != nil {
		return ResultIdentity{}, fmt.Errorf("canonicalize PlanIR result identity: %w", err)
	}
	if len(dependencies.Datasets) == 0 {
		return ResultIdentity{}, fmt.Errorf("plan has no participating dataset")
	}
	plannerDigest := sha256.Sum256(canonical)
	return ResultIdentity{
		Dependencies: DependencyProjection{
			Datasets:      append([]string(nil), dependencies.Datasets...),
			PlannerDigest: "sha256:" + hex.EncodeToString(plannerDigest[:]),
		},
		EquivalenceDigest: digestResultEquivalence(canonical),
	}, nil
}

// ResultEquivalenceCanonical returns the planner's canonical logical PlanIR
// bytes. Physical relation names are intentionally omitted by using
// DependencyCanonical; relation revisions are bound separately by
// resultidentity.Dependency. PlanIR already carries typed literals, output
// projection, ordering, and pagination, so no second query projection is
// maintained here.
func (p Plan) ResultEquivalenceCanonical() ([]byte, error) {
	if p.IR == nil {
		return nil, fmt.Errorf("plan has no PlanIR result-equivalence evidence")
	}
	canonical, err := p.IR.DependencyCanonical()
	if err != nil {
		return nil, fmt.Errorf("canonicalize PlanIR result equivalence: %w", err)
	}
	return canonical, nil
}

// ResultEquivalenceDigest returns the domain-separated SHA-256 identity of a
// planner-normalized executable result. It is suitable for cache key input.
func (p Plan) ResultEquivalenceDigest() (string, error) {
	canonical, err := p.ResultEquivalenceCanonical()
	if err != nil {
		return "", err
	}
	return digestResultEquivalence(canonical), nil
}

func digestResultEquivalence(canonical []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(resultEquivalenceDomain))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(canonical)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

// CanonicalResultDigest is a convenience wrapper for callers that hold a
// plan value rather than a planner instance.
func CanonicalResultDigest(plan Plan) (string, error) {
	return plan.ResultEquivalenceDigest()
}
