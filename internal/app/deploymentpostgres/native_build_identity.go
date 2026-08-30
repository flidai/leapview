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
	OperationID           string `json:"operationId"`
	PlanID                string `json:"planId"`
	CandidateID           string `json:"candidateId"`
	AttemptID             string `json:"attemptId"`
	LeaseID               string `json:"leaseId"`
	GenerationID          string `json:"generationId"`
	SealID                string `json:"sealId"`
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
	ServingArtifactDigest string `json:"servingArtifactDigest"`
	Status                string `json:"status"`
}

// nativeBuildTerminalOutcome is retained as a descriptive alias for callers
// that name the persisted document by its terminal property.
type nativeBuildTerminalOutcome = nativeBuildOutcome

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
	for label, value := range map[string]string{
		"request digest": outcome.RequestDigest, "plan digest": outcome.PlanDigest,
		"source digest": outcome.SourceDigest, "execution digest": outcome.ExecutionDigest,
		"serving artifact digest": outcome.ServingArtifactDigest,
	} {
		if err := platformdigest.ValidateSHA256Identity(value); err != nil {
			return fmt.Errorf("%w: native build outcome %s: %v", deployment.ErrDeliveryConflict, label, err)
		}
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
		"attempt": outcome.AttemptID, "lease": outcome.LeaseID, "generation": outcome.GenerationID,
		"seal": outcome.SealID, "event": outcome.EventID, "audit": outcome.AuditID,
	} {
		expected, err := nativeBuildConsequenceID(outcome.OperationID, role)
		if err != nil || actual != expected {
			return fmt.Errorf("%w: native build %s identity differs from operation", deployment.ErrDeliveryConflict, role)
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
		if validateText(operationInput.Scope, "operation scope", 255) != nil || validateText(operationInput.IdempotencyKey, "operation idempotency key", 512) != nil || operationInput.OperationType != nativeBuildOperationType || platformdigest.ValidateSHA256Identity(operationInput.RequestDigest) != nil {
			return fmt.Errorf("%w: native build operation binding is incomplete", deployment.ErrDeliveryConflict)
		}
		if outcome.TargetID != operationInput.Scope || outcome.IdempotencyKey != operationInput.IdempotencyKey || outcome.RequestDigest != operationInput.RequestDigest {
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
