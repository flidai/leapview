package module

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	releasegen "github.com/flidai/leapview/internal/release/api/gen"
)

const (
	releaseCreatedAuditAction          = "release.created"
	releaseArtifactUploadedAuditAction = "release.artifact_uploaded"
	releaseValidatingAuditAction       = "release.validating"
)

type releaseAuditCommandInput struct {
	OperationID    string
	ProjectID      projectgraph.ResourceID
	ReleaseID      string
	IdempotencyKey string
	PrincipalID    string
	RequestID      string
	CorrelationID  string
	Surface        string
	ProjectDigest  string
	Status         string
	CreatedBy      string
	GenerationID   string
	ArtifactDigest string
	ArtifactSize   int64
	Outcome        string
}

// buildReleaseCreatedAuditIntent translates the generated command contract
// into Access' durable, non-secret handoff. The release aggregate is stable
// before persistence because its identity is derived from the idempotency key.
func buildReleaseCreatedAuditIntent(input releaseAuditCommandInput) (access.AuditIntent, error) {
	contract, ok := releasegen.GetAPIGenOperationContract(input.OperationID)
	if !ok || contract.Command == nil {
		return access.AuditIntent{}, fmt.Errorf("release operation %q has no command audit contract", input.OperationID)
	}
	if contract.Command.Audit.Guarantee != "transactional" {
		return access.AuditIntent{}, fmt.Errorf("release operation %q does not provide transactional auditing", input.OperationID)
	}
	var metadata string
	var err error
	switch input.OperationID {
	case string(releasegen.GenOperationCreateRelease):
		metadata, err = releasegen.EncodeGenCreateReleaseAuditPayload(releasegen.GenSchemaReleaseCreatedAuditPayload{
			OperationId: input.OperationID, ReleaseId: input.ReleaseID, ProjectId: input.ProjectID.String(),
			ProjectDigest: input.ProjectDigest, Status: input.Status, CreatedBy: input.CreatedBy,
		})
	case string(releasegen.GenOperationUploadReleaseArtifact):
		metadata, err = releasegen.EncodeGenUploadReleaseArtifactAuditPayload(releasegen.GenSchemaReleaseArtifactUploadedAuditPayload{
			OperationId: input.OperationID, ReleaseId: input.ReleaseID, GenerationId: input.GenerationID,
			Digest: input.ArtifactDigest, SizeBytes: input.ArtifactSize,
		})
	case string(releasegen.GenOperationFinalizeRelease):
		metadata, err = releasegen.EncodeGenFinalizeReleaseAuditPayload(releasegen.GenSchemaReleaseValidatingAuditPayload{
			OperationId: input.OperationID, ReleaseId: input.ReleaseID, ProjectId: input.ProjectID.String(), Status: input.Status,
		})
	default:
		return access.AuditIntent{}, fmt.Errorf("release operation %q has no transactional audit payload", input.OperationID)
	}
	if err != nil {
		return access.AuditIntent{}, err
	}
	aggregateKey := "release:" + input.ProjectID.String() + ":" + input.ReleaseID
	key := strings.TrimSpace(input.IdempotencyKey)
	if key == "" {
		return access.AuditIntent{}, fmt.Errorf("release audit intent requires idempotency key")
	}
	sum := sha256.Sum256([]byte("release\x00" + input.OperationID + "\x00" + aggregateKey + "\x00" + key))
	sequence := int64(1)
	switch input.OperationID {
	case string(releasegen.GenOperationUploadReleaseArtifact):
		sequence = 2
	case string(releasegen.GenOperationFinalizeRelease):
		sequence = 3
	}
	outcome := strings.TrimSpace(input.Outcome)
	if outcome == "" {
		outcome = "success"
	}
	intent := access.AuditIntent{
		EventID: "release:" + hex.EncodeToString(sum[:16]), Source: "release", Operation: input.OperationID,
		PrincipalID: strings.TrimSpace(input.PrincipalID), Action: contract.Command.Audit.SuccessAction,
		ResourceKind: "project", ResourceID: input.ProjectID.String(), Capability: access.CapabilityResourcePublish,
		Outcome: outcome, RequestID: strings.TrimSpace(input.RequestID), CorrelationID: strings.TrimSpace(input.CorrelationID),
		AggregateKey: aggregateKey, AggregateSequence: sequence, MetadataJSON: metadata,
	}
	return intent.Canonicalize()
}
