package deploymentpostgres

// This file owns the identity contract for the native PostgreSQL BuildPlan
// command.  It intentionally stops before orchestration: callers can derive
// every durable consequence identity and safely encode/decode a terminal
// operation outcome without gaining access to a repository or writer.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/google/uuid"
)

const (
	nativeBuildOperationType   = "delivery.plan.build"
	maxNativeBuildOutcomeBytes = 16 << 10
	// Successor depth is persisted in the terminal outcome so replay can root
	// the append edge at the operation-derived attempt rather than trusting an
	// arbitrary UUID supplied as predecessor evidence.
	maxNativeBuildSuccessorDepth = 64
)

// nativeBuildOutcome is the terminal, replayable evidence persisted against a
// native BuildPlan operation.  IDs are strings at this boundary because the
// operation store persists JSON; every ID is still required to be a canonical
// UUIDv7 and every digest is required to be a canonical SHA-256 identity.
//
// The request fields are repeated deliberately.  An operation digest alone is
// not sufficient for a safe replay response: retaining the exact project,
// target, environment, plan, and principal binding makes the terminal record
// self-describing and prevents cross-scope consequence reuse.
type nativeBuildOutcome struct {
	OperationID      string `json:"operationId"`
	OperationOwnerID string `json:"operationOwnerId"`
	PlanID           string `json:"planId"`
	CandidateID      string `json:"candidateId"`
	AttemptID        string `json:"attemptId"`
	LeaseID          string `json:"leaseId"`
	// Successor outcomes carry the exact append edge.  Root outcomes leave
	// these fields empty and use operation-derived attempt/lease identities.
	AttemptIdentity       string `json:"attemptIdentity,omitempty"`
	PredecessorAttemptID  string `json:"predecessorAttemptId,omitempty"`
	SuccessorDepth        int    `json:"successorDepth,omitempty"`
	GenerationID          string `json:"generationId"`
	SealID                string `json:"sealId"`
	ServingArtifactID     string `json:"servingArtifactId"`
	EventID               string `json:"eventId"`
	AuditID               string `json:"auditId"`
	ProjectID             string `json:"projectId"`
	TargetID              string `json:"targetId"`
	Environment           string `json:"environment"`
	ActorID               string `json:"actorId"`
	IdempotencyKey        string `json:"idempotencyKey"`
	RequestDigest         string `json:"requestDigest"`
	PlanDigest            string `json:"planDigest"`
	SourceDigest          string `json:"sourceDigest"`
	ExecutionDigest       string `json:"executionDigest"`
	QualificationDigest   string `json:"qualificationDigest"`
	ServingArtifactDigest string `json:"servingArtifactDigest"`
	Status                string `json:"status"`
}

// validateNativeBuildRequest validates the exact immutable command identity.
// NativeDeliveryBuildRequest already carries a UUID value. Native plans are
// authority-allocated UUIDv7 records, so a non-v7 plan must fail before a
// request digest or build operation is produced.
func validateNativeBuildRequest(request deploymentmodule.NativeDeliveryBuildRequest) error {
	if err := request.ProjectID.Validate(); err != nil {
		return fmt.Errorf("%w: project identity: %v", deployment.ErrDeliveryInvalid, err)
	}
	if request.PlanID == uuid.Nil {
		return fmt.Errorf("%w: plan identity is required", deployment.ErrDeliveryInvalid)
	}
	if _, err := canonicalUUIDv7(request.PlanID.String()); err != nil {
		return fmt.Errorf("%w: plan identity: %v", deployment.ErrDeliveryInvalid, err)
	}
	for label, value := range map[string]string{
		"target": request.TargetID, "environment": request.Environment,
		"principal": request.PrincipalID, "idempotency key": request.IdempotencyKey,
	} {
		max := 255
		if label == "idempotency key" {
			max = 512
		}
		if err := validateText(value, label, max); err != nil {
			return fmt.Errorf("%w: %s is required and canonical: %v", deployment.ErrDeliveryInvalid, label, err)
		}
	}
	return nil
}

// normalizeNativeBuildRequest returns the canonical value used throughout a
// build command. NativeDeliveryBuildRequest currently contains only scalar
// strings and a UUID, nevertheless trimming is intentionally not performed:
// accepting a non-canonical wire value and silently changing its digest would
// make operation idempotency ambiguous.
func normalizeNativeBuildRequest(request deploymentmodule.NativeDeliveryBuildRequest) (deploymentmodule.NativeDeliveryBuildRequest, error) {
	if err := validateNativeBuildRequest(request); err != nil {
		return deploymentmodule.NativeDeliveryBuildRequest{}, err
	}
	return request, nil
}

// nativeBuildRequestDigest computes a deterministic digest over the complete
// BuildPlan command binding.  The fixed struct (rather than a map) gives the
// request a stable JSON shape and includes no operation- or consequence-ID
// material, so retries of the same command always address the same operation.
func nativeBuildRequestDigest(request deploymentmodule.NativeDeliveryBuildRequest) (string, error) {
	if err := validateNativeBuildRequest(request); err != nil {
		return "", err
	}
	canonical := struct {
		ProjectID, TargetID, Environment, PlanID, PrincipalID, IdempotencyKey string
	}{
		ProjectID: request.ProjectID.String(), TargetID: request.TargetID,
		Environment: request.Environment, PlanID: request.PlanID.String(),
		PrincipalID: request.PrincipalID, IdempotencyKey: request.IdempotencyKey,
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("marshal native build request identity: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// nativeBuildConsequenceID deterministically derives one UUIDv7-shaped
// consequence from the operation UUIDv7.  The operation's first six bytes
// retain its timestamp ordering; the role hash supplies the remaining bits,
// after which the UUID version and RFC 4122 variant are reasserted.
func nativeBuildConsequenceID(operationID, role string) (string, error) {
	canonical, err := canonicalUUIDv7(operationID)
	if err != nil {
		return "", err
	}
	switch role {
	case "candidate", "attempt", "lease", "generation", "seal", "event", "audit":
	default:
		return "", errors.New("native build consequence role is invalid")
	}
	id, err := uuid.Parse(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte("leapview/native-build/" + canonical + "/" + role))
	copy(id[6:], digest[:10])
	id[6] = id[6]&0x0f | 0x70
	id[8] = id[8]&0x3f | 0x80
	return id.String(), nil
}

// nativeBuildSuccessorDepth returns the deterministic chain distance from the
// operation-derived root attempt to predecessorID.  It is intentionally
// bounded: an outcome that cannot be rooted within this limit is rejected
// instead of accepting an unanchored UUID pair.
func nativeBuildSuccessorDepth(operationID, predecessorID string) (int, error) {
	root, err := nativeBuildConsequenceID(operationID, "attempt")
	if err != nil {
		return 0, err
	}
	canonical, err := canonicalUUIDv7(predecessorID)
	if err != nil || canonical != predecessorID {
		return 0, fmt.Errorf("%w: predecessor attempt identity is not canonical", deployment.ErrDeliveryConflict)
	}
	if predecessorID == root {
		return 0, nil
	}
	cursor := root
	for depth := 1; depth <= maxNativeBuildSuccessorDepth; depth++ {
		cursor, err = nativeBuildSuccessorID(cursor, "attempt")
		if err != nil {
			return 0, err
		}
		if cursor == predecessorID {
			return depth, nil
		}
	}
	return 0, fmt.Errorf("%w: successor predecessor is outside the bounded chain", deployment.ErrDeliveryConflict)
}

// encodeNativeBuildOutcome validates and bounds a terminal outcome before it
// is persisted. The exact request and operation bindings are required so a
// programmer cannot persist a well-formed consequence document under the
// wrong idempotency key or target scope.
func encodeNativeBuildOutcome(outcome nativeBuildOutcome, request deploymentmodule.NativeDeliveryBuildRequest, operationInput deploymentmodule.NativeOperationAcquireInput) (json.RawMessage, error) {
	if err := validateNativeBuildRequest(request); err != nil {
		return nil, err
	}
	if err := validateNativeBuildOutcome(outcome, &request, &operationInput); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(outcome)
	if err != nil {
		return nil, fmt.Errorf("marshal native build outcome: %w", err)
	}
	if len(encoded) > maxNativeBuildOutcomeBytes {
		return nil, fmt.Errorf("%w: native build outcome exceeds %d bytes", deployment.ErrDeliveryInvalid, maxNativeBuildOutcomeBytes)
	}
	return json.RawMessage(encoded), nil
}

// decodeNativeBuildOutcome strictly decodes one bounded terminal outcome.
// Unknown fields, duplicate keys, trailing bytes, oversized documents, and
// noncanonical identities are rejected before any replay binding is returned.
func decodeNativeBuildOutcome(raw json.RawMessage, request deploymentmodule.NativeDeliveryBuildRequest, operationInput deploymentmodule.NativeOperationAcquireInput) (nativeBuildOutcome, error) {
	if err := validateNativeBuildRequest(request); err != nil {
		return nativeBuildOutcome{}, err
	}
	var outcome nativeBuildOutcome
	if len(raw) == 0 {
		return nativeBuildOutcome{}, fmt.Errorf("%w: replay operation outcome is empty", deployment.ErrDeliveryConflict)
	}
	if err := strictjson.DecodeWithOptions(raw, &outcome, strictjson.Options{MaxBytes: maxNativeBuildOutcomeBytes}); err != nil {
		return nativeBuildOutcome{}, fmt.Errorf("%w: replay operation outcome is invalid: %v", deployment.ErrDeliveryConflict, err)
	}
	if err := validateNativeBuildOutcome(outcome, &request, &operationInput); err != nil {
		return nativeBuildOutcome{}, err
	}
	return outcome, nil
}

func validateNativeBuildOutcome(outcome nativeBuildOutcome, request *deploymentmodule.NativeDeliveryBuildRequest, operationInput *deploymentmodule.NativeOperationAcquireInput) error {
	for label, value := range map[string]string{
		"operation id": outcome.OperationID, "plan id": outcome.PlanID,
		"candidate id": outcome.CandidateID, "attempt id": outcome.AttemptID,
		"lease id": outcome.LeaseID, "generation id": outcome.GenerationID,
		"seal id": outcome.SealID, "event id": outcome.EventID, "audit id": outcome.AuditID,
	} {
		if _, err := canonicalUUIDv7(value); err != nil {
			return fmt.Errorf("%w: native build outcome %s: %v", deployment.ErrDeliveryConflict, label, err)
		}
	}
	if err := validateOutcomeResourceID(outcome.ProjectID); err != nil {
		return fmt.Errorf("%w: native build outcome project identity: %v", deployment.ErrDeliveryConflict, err)
	}
	for label, value := range map[string]string{
		"target id": outcome.TargetID, "environment": outcome.Environment,
		"actor id": outcome.ActorID, "idempotency key": outcome.IdempotencyKey,
	} {
		max := 255
		if label == "idempotency key" {
			max = 512
		}
		if err := validateText(value, label, max); err != nil {
			return fmt.Errorf("%w: native build outcome %s is not canonical: %v", deployment.ErrDeliveryConflict, label, err)
		}
	}
	if _, err := canonicalUUIDv7(outcome.OperationOwnerID); err != nil {
		return fmt.Errorf("%w: native build outcome operation owner identity: %v", deployment.ErrDeliveryConflict, err)
	}
	for label, value := range map[string]string{
		"request digest": outcome.RequestDigest, "plan digest": outcome.PlanDigest,
		"source digest": outcome.SourceDigest, "execution digest": outcome.ExecutionDigest,
		"qualification digest":    outcome.QualificationDigest,
		"serving artifact digest": outcome.ServingArtifactDigest,
	} {
		if err := platformdigest.ValidateSHA256Identity(value); err != nil {
			return fmt.Errorf("%w: native build outcome %s: %v", deployment.ErrDeliveryConflict, label, err)
		}
	}
	if err := validateText(outcome.ServingArtifactID, "serving artifact id", 255); err != nil {
		return fmt.Errorf("%w: native build outcome serving artifact id is not canonical: %v", deployment.ErrDeliveryConflict, err)
	}
	if outcome.AttemptIdentity != "" {
		if err := validateText(outcome.AttemptIdentity, "attempt identity", 512); err != nil {
			return fmt.Errorf("%w: native build outcome attempt identity is not canonical: %v", deployment.ErrDeliveryConflict, err)
		}
	}
	if outcome.PredecessorAttemptID != "" {
		if _, err := canonicalUUIDv7(outcome.PredecessorAttemptID); err != nil {
			return fmt.Errorf("%w: native build outcome predecessor attempt identity: %v", deployment.ErrDeliveryConflict, err)
		}
	}
	if outcome.SuccessorDepth < 0 || outcome.SuccessorDepth > maxNativeBuildSuccessorDepth {
		return fmt.Errorf("%w: native build outcome successor depth is outside the bounded chain", deployment.ErrDeliveryConflict)
	}
	switch outcome.Status {
	case "sealed":
	default:
		return fmt.Errorf("%w: native build outcome status %q is not terminal", deployment.ErrDeliveryConflict, outcome.Status)
	}

	if expected, err := nativeBuildConsequenceID(outcome.OperationID, "candidate"); err != nil || outcome.CandidateID != expected {
		return fmt.Errorf("%w: native build candidate identity differs from operation", deployment.ErrDeliveryConflict)
	}
	for role, actual := range map[string]string{
		"generation": outcome.GenerationID, "seal": outcome.SealID, "event": outcome.EventID, "audit": outcome.AuditID,
	} {
		expected, err := nativeBuildConsequenceID(outcome.OperationID, role)
		if err != nil || actual != expected {
			return fmt.Errorf("%w: native build %s identity differs from operation", deployment.ErrDeliveryConflict, role)
		}
	}
	// The initial attempt and lease use operation-derived identities. Every
	// successor carries its predecessor attempt ID and exact identity, allowing
	// arbitrary-depth deterministic chains without accepting arbitrary UUIDv7
	// pairs in a replay payload.
	rootAttempt, rootErr := nativeBuildConsequenceID(outcome.OperationID, "attempt")
	if rootErr != nil {
		return fmt.Errorf("%w: native build attempt identity differs from operation", deployment.ErrDeliveryConflict)
	}
	if outcome.SuccessorDepth == 0 {
		if outcome.PredecessorAttemptID != "" || outcome.AttemptIdentity != "" || outcome.AttemptID != rootAttempt {
			return fmt.Errorf("%w: native build root attempt identity differs from operation", deployment.ErrDeliveryConflict)
		}
		rootLease, leaseErr := nativeBuildConsequenceID(outcome.OperationID, "lease")
		if leaseErr != nil || outcome.LeaseID != rootLease {
			return fmt.Errorf("%w: native build lease identity differs from operation", deployment.ErrDeliveryConflict)
		}
	} else {
		// Walk from the operation-derived root exactly SuccessorDepth edges and
		// require the persisted predecessor/child pair to match that path.
		predecessor := rootAttempt
		var successorAttempt string
		var attemptErr error
		for depth := 1; depth <= outcome.SuccessorDepth; depth++ {
			successorAttempt, attemptErr = nativeBuildSuccessorID(predecessor, "attempt")
			if attemptErr != nil {
				break
			}
			if depth < outcome.SuccessorDepth {
				predecessor = successorAttempt
			}
		}
		successorLease, leaseErr := nativeBuildSuccessorID(predecessor, "lease")
		if attemptErr != nil || leaseErr != nil || outcome.PredecessorAttemptID != predecessor || outcome.AttemptID != successorAttempt || outcome.LeaseID != successorLease || outcome.AttemptIdentity != "native-build-successor/"+successorAttempt {
			return fmt.Errorf("%w: native build successor attempt or lease identity differs from predecessor", deployment.ErrDeliveryConflict)
		}
	}

	if request != nil {
		digest, err := nativeBuildRequestDigest(*request)
		if err != nil {
			return err
		}
		if outcome.ProjectID != request.ProjectID.String() || outcome.TargetID != request.TargetID || outcome.Environment != request.Environment || outcome.PlanID != request.PlanID.String() || outcome.ActorID != request.PrincipalID || outcome.IdempotencyKey != request.IdempotencyKey || outcome.RequestDigest != digest {
			return fmt.Errorf("%w: native build outcome request identity differs", deployment.ErrDeliveryConflict)
		}
	}
	if operationInput != nil {
		if validateText(operationInput.Scope, "operation scope", 255) != nil || validateText(operationInput.IdempotencyKey, "operation idempotency key", 512) != nil || validateText(operationInput.OwnerID, "operation owner id", 255) != nil || operationInput.OperationType != nativeBuildOperationType || platformdigest.ValidateSHA256Identity(operationInput.RequestDigest) != nil {
			return fmt.Errorf("%w: native build operation binding is incomplete", deployment.ErrDeliveryConflict)
		}
		if _, err := canonicalUUIDv7(operationInput.OwnerID); err != nil {
			return fmt.Errorf("%w: native build operation owner identity is invalid", deployment.ErrDeliveryConflict)
		}
		if outcome.TargetID != operationInput.Scope || outcome.IdempotencyKey != operationInput.IdempotencyKey || outcome.RequestDigest != operationInput.RequestDigest || outcome.OperationOwnerID != operationInput.OwnerID {
			return fmt.Errorf("%w: native build outcome operation identity differs", deployment.ErrDeliveryConflict)
		}
	}
	return nil
}

func validateOutcomeResourceID(value string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return errors.New("project identity is blank or noncanonical")
	}
	projectID := projectgraph.ResourceID(value)
	if err := projectID.Validate(); err != nil {
		return err
	}
	return nil
}
