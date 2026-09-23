package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/platform/safetext"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func normalizeDeploymentOperation(descriptor DeploymentOperationDescriptor) (DeploymentOperationDescriptor, error) {
	descriptor.Handle = strings.TrimSpace(descriptor.Handle)
	if err := validateDeploymentOperationHandle(descriptor.Handle); err != nil {
		return DeploymentOperationDescriptor{}, err
	}
	var err error
	descriptor.TargetOrigin, err = canonicalOperationOrigin(descriptor.TargetOrigin)
	if err != nil {
		return DeploymentOperationDescriptor{}, err
	}
	created, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(descriptor.CreatedAt))
	if err != nil || created.IsZero() {
		return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation createdAt must be RFC3339")
	}
	descriptor.CreatedAt = created.UTC().Format(time.RFC3339Nano)
	descriptor.ProjectID, descriptor.Environment = strings.TrimSpace(descriptor.ProjectID), strings.TrimSpace(descriptor.Environment)
	if descriptor.ProjectID == "" || descriptor.Environment == "" {
		return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation requires project and environment")
	}
	descriptor.TargetID = strings.TrimSpace(descriptor.TargetID)
	descriptor.TargetSelector = strings.TrimSpace(descriptor.TargetSelector)
	descriptor.SourceSnapshotRef = strings.TrimSpace(descriptor.SourceSnapshotRef)
	descriptor.SourceSnapshotProjectID = strings.TrimSpace(descriptor.SourceSnapshotProjectID)
	descriptor.SourceSnapshotGraphDigest = strings.TrimSpace(descriptor.SourceSnapshotGraphDigest)
	descriptor.SourceRevision = strings.TrimSpace(descriptor.SourceRevision)
	descriptor.SourceRepository = strings.TrimSpace(descriptor.SourceRepository)
	descriptor.SourceRef = strings.TrimSpace(descriptor.SourceRef)
	descriptor.SourceChangeID = strings.TrimSpace(descriptor.SourceChangeID)
	descriptor.PlanID = strings.TrimSpace(descriptor.PlanID)
	descriptor.PlanDigest = strings.TrimSpace(descriptor.PlanDigest)
	descriptor.PlanStatus = strings.TrimSpace(descriptor.PlanStatus)
	descriptor.PlanExpiresAt = strings.TrimSpace(descriptor.PlanExpiresAt)
	descriptor.GovernanceDigest = strings.TrimSpace(descriptor.GovernanceDigest)
	descriptor.SourceDigest = strings.TrimSpace(descriptor.SourceDigest)
	descriptor.SourceAttestationDigest = strings.TrimSpace(descriptor.SourceAttestationDigest)
	descriptor.ProvenanceDigest = strings.TrimSpace(descriptor.ProvenanceDigest)
	descriptor.ExecutionDigest = strings.TrimSpace(descriptor.ExecutionDigest)
	descriptor.EvidenceDigest = strings.TrimSpace(descriptor.EvidenceDigest)
	descriptor.BuildID = strings.TrimSpace(descriptor.BuildID)
	descriptor.CandidateID = strings.TrimSpace(descriptor.CandidateID)
	descriptor.SealID = strings.TrimSpace(descriptor.SealID)
	descriptor.PublicationID = strings.TrimSpace(descriptor.PublicationID)
	descriptor.GenerationID = strings.TrimSpace(descriptor.GenerationID)
	descriptor.PublicationStatus = strings.TrimSpace(descriptor.PublicationStatus)
	descriptor.FailureCode = strings.TrimSpace(descriptor.FailureCode)
	descriptor.FailureDetail = strings.TrimSpace(descriptor.FailureDetail)
	descriptor.StatusURL = strings.TrimSpace(descriptor.StatusURL)
	if _, err := projectgraph.NewResourceID(descriptor.ProjectID); err != nil {
		return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation project identity is invalid: %w", err)
	}
	if descriptor.TargetID != "" {
		if err := validateDeploymentOperationIdentity("targetId", descriptor.TargetID, 256); err != nil {
			return DeploymentOperationDescriptor{}, err
		}
	}
	if err := projectgraph.ValidateServingEnvironment(descriptor.Environment); err != nil {
		return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation environment is invalid: %w", err)
	}
	for _, value := range []struct {
		name, value string
	}{
		{"targetId", descriptor.TargetID}, {"targetSelector", descriptor.TargetSelector}, {"projectId", descriptor.ProjectID}, {"environment", descriptor.Environment},
		{"sourceSnapshotRef", descriptor.SourceSnapshotRef}, {"sourceSnapshotProjectId", descriptor.SourceSnapshotProjectID}, {"sourceRevision", descriptor.SourceRevision}, {"sourceRepository", descriptor.SourceRepository}, {"sourceRef", descriptor.SourceRef}, {"sourceChangeId", descriptor.SourceChangeID},
		{"planId", descriptor.PlanID}, {"planStatus", descriptor.PlanStatus}, {"planExpiresAt", descriptor.PlanExpiresAt}, {"baseGenerationId", descriptor.BaseGenerationID}, {"buildId", descriptor.BuildID}, {"candidateId", descriptor.CandidateID}, {"sealId", descriptor.SealID}, {"publicationId", descriptor.PublicationID}, {"generationId", descriptor.GenerationID}, {"publicationStatus", descriptor.PublicationStatus}, {"failureCode", descriptor.FailureCode}, {"failureDetail", descriptor.FailureDetail},
	} {
		if err := validateDeploymentOperationText(value.name, value.value, maxDeploymentOperationTextBytes); err != nil {
			return DeploymentOperationDescriptor{}, err
		}
	}
	for name, value := range map[string]string{"targetSelector": descriptor.TargetSelector, "sourceRepository": descriptor.SourceRepository} {
		if strings.Contains(value, "://") {
			parsed, parseErr := url.Parse(value)
			if parseErr != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
				return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation %s must not contain credentials or query state", name)
			}
		}
	}
	for _, value := range []struct{ name, value string }{
		{"planIdempotencyKey", descriptor.PlanIdempotencyKey}, {"buildIdempotencyKey", descriptor.BuildIdempotencyKey}, {"publicationIdempotencyKey", descriptor.PublicationIdempotencyKey},
	} {
		if err := validateDeploymentOperationText(value.name, value.value, maxDeploymentOperationTextBytes); err != nil {
			return DeploymentOperationDescriptor{}, err
		}
	}
	if descriptor.SourceRoot != "" {
		descriptor.SourceRoot, err = filepath.Abs(descriptor.SourceRoot)
		if err != nil {
			return DeploymentOperationDescriptor{}, fmt.Errorf("resolve deployment source root: %w", err)
		}
		descriptor.SourceRoot = filepath.Clean(descriptor.SourceRoot)
		if err := validateDeploymentOperationText("sourceRoot", descriptor.SourceRoot, maxDeploymentOperationTextBytes); err != nil {
			return DeploymentOperationDescriptor{}, err
		}
	}
	if descriptor.SourceSnapshotProjectID != "" {
		if descriptor.SourceDigest == "" {
			return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation source snapshot requires a source digest")
		}
		if _, err := projectgraph.NewResourceID(descriptor.SourceSnapshotProjectID); err != nil {
			return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation source snapshot Project identity is invalid: %w", err)
		}
		sort.Slice(descriptor.SourceArtifacts, func(i, j int) bool { return descriptor.SourceArtifacts[i].Path < descriptor.SourceArtifacts[j].Path })
		if err := validatePortableSourceArtifacts(descriptor.SourceSnapshotProjectID, descriptor.SourceSnapshotGraphDigest, descriptor.SourceDigest, descriptor.SourceArtifacts); err != nil {
			return DeploymentOperationDescriptor{}, err
		}
	} else if len(descriptor.SourceArtifacts) != 0 {
		return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation source artifacts require a snapshot Project identity")
	}
	descriptor.SourceRoot = strings.TrimSpace(descriptor.SourceRoot)
	for _, value := range []struct{ name, value string }{
		{"sourceDigest", descriptor.SourceDigest}, {"sourceAttestationDigest", descriptor.SourceAttestationDigest}, {"provenanceDigest", descriptor.ProvenanceDigest}, {"planDigest", descriptor.PlanDigest}, {"executionDigest", descriptor.ExecutionDigest}, {"evidenceDigest", descriptor.EvidenceDigest}, {"governanceDigest", descriptor.GovernanceDigest},
	} {
		if value.value != "" {
			if err := digest.ValidateSHA256Identity(value.value); err != nil {
				return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation %s is invalid: %w", value.name, err)
			}
		}
	}
	if descriptor.SourceSnapshotGraphDigest != "" {
		if err := digest.ValidateSHA256Identity(descriptor.SourceSnapshotGraphDigest); err != nil {
			return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation source graph digest is invalid: %w", err)
		}
	}
	if descriptor.StatusURL != "" {
		if err := validateDeploymentOperationStatusURL(descriptor.TargetOrigin, descriptor.StatusURL); err != nil {
			return DeploymentOperationDescriptor{}, err
		}
	}
	if descriptor.PlanEvidence.Digest != "" {
		if err := digest.ValidateSHA256Identity(descriptor.PlanEvidence.Digest); err != nil {
			return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation plan evidence digest is invalid: %w", err)
		}
	}
	if descriptor.PlanStatus != "" && descriptor.PlanStatus != "planned" && descriptor.PlanStatus != "expired" {
		return DeploymentOperationDescriptor{}, fmt.Errorf("unsupported deployment operation plan status %q", descriptor.PlanStatus)
	}
	if descriptor.PlanExpiresAt != "" {
		expiresAt, err := time.Parse(time.RFC3339, descriptor.PlanExpiresAt)
		if err != nil || expiresAt.IsZero() {
			return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation planExpiresAt must be RFC3339")
		}
		descriptor.PlanExpiresAt = expiresAt.UTC().Format(time.RFC3339)
	}
	if descriptor.PublicationStatus != "" {
		switch descriptor.PublicationStatus {
		case "pending", "committed", "rejected", "indeterminate":
		default:
			return DeploymentOperationDescriptor{}, fmt.Errorf("unsupported deployment operation publication status %q", descriptor.PublicationStatus)
		}
	}
	if descriptor.BuildRevision < 0 || descriptor.CandidateRevision < 0 || descriptor.BaseTargetRevision < 0 || descriptor.PublicationTargetRevision < 0 {
		return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation revisions cannot be negative")
	}
	if err := validateDeploymentPlanEvidence(descriptor.PlanEvidence); err != nil {
		return DeploymentOperationDescriptor{}, err
	}
	if descriptor.PlanIdempotencyKey == "" || descriptor.BuildIdempotencyKey == "" || descriptor.PublicationIdempotencyKey == "" {
		return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation requires stable mutation idempotency keys")
	}
	if descriptor.PlanIdempotencyKey != deploymentOperationIdempotencyKey(descriptor.Handle, "plan") || descriptor.BuildIdempotencyKey != deploymentOperationIdempotencyKey(descriptor.Handle, "build") || descriptor.PublicationIdempotencyKey != deploymentOperationIdempotencyKey(descriptor.Handle, "publish") {
		return DeploymentOperationDescriptor{}, fmt.Errorf("deployment operation mutation idempotency keys are not stable for handle %q", descriptor.Handle)
	}
	if descriptor.Outcome == "" {
		descriptor.Outcome = DeploymentOperationUnknown
	}
	switch descriptor.Outcome {
	case DeploymentOperationUnknown, DeploymentOperationPendingApproval, DeploymentOperationActive, DeploymentOperationFailure, DeploymentOperationIndeterminate:
	default:
		return DeploymentOperationDescriptor{}, fmt.Errorf("unsupported deployment operation outcome %q", descriptor.Outcome)
	}
	return descriptor, nil
}

func validatePortableSourceArtifacts(projectID, graphDigest, sourceDigest string, artifacts []DeploymentSourceArtifact) error {
	if strings.TrimSpace(projectID) == "" || len(artifacts) == 0 {
		return fmt.Errorf("deployment operation source snapshot requires project and artifacts")
	}
	if len(artifacts) > maxDeploymentOperationArtifacts {
		return fmt.Errorf("deployment operation source snapshot contains too many artifacts")
	}
	if graphDigest != "" {
		if err := digest.ValidateSHA256Identity(graphDigest); err != nil {
			return fmt.Errorf("deployment operation source graph digest is invalid: %w", err)
		}
	}
	seen := make(map[string]struct{}, len(artifacts))
	var totalBytes int64
	for _, artifact := range artifacts {
		artifact.Path = strings.TrimSpace(artifact.Path)
		if artifact.Path == "" || path.IsAbs(artifact.Path) || path.Clean(artifact.Path) != artifact.Path || artifact.Path == "." || strings.HasPrefix(artifact.Path, "../") || strings.Contains(artifact.Path, "\\") {
			return fmt.Errorf("deployment operation source artifact path %q is not canonical", artifact.Path)
		}
		if len(artifact.Path) > 4096 {
			return fmt.Errorf("deployment operation source artifact path is too long")
		}
		if _, exists := seen[artifact.Path]; exists {
			return fmt.Errorf("deployment operation source snapshot repeats path %q", artifact.Path)
		}
		seen[artifact.Path] = struct{}{}
		if artifact.SizeBytes != int64(len(artifact.Content)) {
			return fmt.Errorf("deployment operation source artifact %q size does not match content", artifact.Path)
		}
		if artifact.SizeBytes < 0 || artifact.SizeBytes > maxDeploymentOperationArtifactSize {
			return fmt.Errorf("deployment operation source artifact %q exceeds the per-file size limit", artifact.Path)
		}
		totalBytes += artifact.SizeBytes
		if totalBytes > maxDeploymentOperationSourceBytes {
			return fmt.Errorf("deployment operation source snapshot exceeds the total size limit")
		}
		if err := digest.ValidateSHA256Identity(artifact.Digest); err != nil {
			return fmt.Errorf("deployment operation source artifact %q digest is invalid: %w", artifact.Path, err)
		}
		hash := sha256.Sum256(artifact.Content)
		if artifact.Digest != "sha256:"+hex.EncodeToString(hash[:]) {
			return fmt.Errorf("deployment operation source artifact %q content does not match digest", artifact.Path)
		}
		if safetext.Credentials(string(artifact.Content)) != string(artifact.Content) {
			return fmt.Errorf("deployment operation source artifact %q appears to contain credential material", artifact.Path)
		}
	}
	if sourceDigest != "" {
		ordered := append([]DeploymentSourceArtifact(nil), artifacts...)
		sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
		hash := sha256.New()
		for _, artifact := range ordered {
			_, _ = fmt.Fprintf(hash, "%d:%s:%d:%s:%d:", len(artifact.Path), artifact.Path, len(artifact.Digest), artifact.Digest, artifact.SizeBytes)
		}
		if actual := "sha256:" + hex.EncodeToString(hash.Sum(nil)); actual != sourceDigest {
			return fmt.Errorf("deployment operation source snapshot digest does not match artifacts")
		}
	}
	return nil
}

func validateDeploymentOperationText(name, value string, maxBytes int) error {
	if len(value) > maxBytes {
		return fmt.Errorf("deployment operation %s is too large", name)
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return fmt.Errorf("deployment operation %s contains control characters", name)
		}
	}
	if safetext.Credentials(value) != value {
		return fmt.Errorf("deployment operation %s appears to contain credential material", name)
	}
	return nil
}

func validateDeploymentOperationIdentity(name, value string, maxBytes int) error {
	if value == "" {
		return nil
	}
	if len(value) > maxBytes {
		return fmt.Errorf("deployment operation %s is too large", name)
	}
	for index, character := range value {
		if index == 0 && !((character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9')) {
			return fmt.Errorf("deployment operation %s is not canonical", name)
		}
		if index > 0 && !((character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || strings.ContainsRune("_.:@/-", character)) {
			return fmt.Errorf("deployment operation %s is not canonical", name)
		}
	}
	return nil
}

func validateDeploymentOperationStatusURL(origin, statusURL string) error {
	base, err := url.Parse(origin)
	if err != nil {
		return fmt.Errorf("deployment operation target origin is invalid: %w", err)
	}
	parsed, err := url.Parse(strings.TrimSpace(statusURL))
	if err != nil || parsed.Scheme != base.Scheme || parsed.Host != base.Host || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path == "" || !strings.HasPrefix(parsed.Path, "/candidates/") {
		return fmt.Errorf("deployment operation status URL must be a credential-free candidate URL under the target origin")
	}
	return nil
}

func validateDeploymentPlanEvidence(evidence DeliveryPlanEvidenceResult) error {
	if evidence.Digest != "" {
		if err := digest.ValidateSHA256Identity(evidence.Digest); err != nil {
			return fmt.Errorf("deployment operation plan evidence digest is invalid: %w", err)
		}
	}
	if len(evidence.PlannedInputs) > maxDeploymentOperationArtifacts || len(evidence.QualificationSteps) > maxDeploymentOperationArtifacts || len(evidence.ReuseDecisions) > maxDeploymentOperationArtifacts {
		return fmt.Errorf("deployment operation plan evidence contains too many entries")
	}
	if evidence.AddedCount < 0 || evidence.RemovedCount < 0 || evidence.DirectlyModifiedCount < 0 || evidence.IndirectlyAffectedCount < 0 || evidence.ReuseCount < 0 || evidence.QualificationStepCount < 0 {
		return fmt.Errorf("deployment operation plan evidence counts cannot be negative")
	}
	texts := []struct {
		name, value string
	}{
		{"plan impact statement", evidence.ImpactStatement}, {"plan physical-work statement", evidence.PhysicalWorkStatement}, {"plan reuse statement", evidence.ReuseStatement}, {"plan rollback class", evidence.RollbackClass}, {"plan qualification policy", evidence.QualificationPolicy}, {"stale policy description", evidence.StalePolicy.Description}, {"stale policy mode", evidence.StalePolicy.Mode},
	}
	for _, text := range texts {
		if err := validateDeploymentOperationText(text.name, text.value, maxDeploymentOperationTextBytes); err != nil {
			return err
		}
	}
	for index, input := range evidence.PlannedInputs {
		for _, text := range []struct{ name, value string }{{"plan input id", input.ID}, {"plan input mode", input.Mode}, {"plan input revision", input.Revision}, {"plan input bound", input.Bound}} {
			if err := validateDeploymentOperationText(fmt.Sprintf("plan input %d %s", index, text.name), text.value, maxDeploymentOperationTextBytes); err != nil {
				return err
			}
		}
	}
	for index, step := range evidence.QualificationSteps {
		for _, text := range []struct{ name, value string }{{"qualification step id", step.ID}, {"qualification step kind", step.Kind}, {"qualification step description", step.Description}} {
			if err := validateDeploymentOperationText(fmt.Sprintf("qualification step %d %s", index, text.name), text.value, maxDeploymentOperationTextBytes); err != nil {
				return err
			}
		}
	}
	for index, decision := range evidence.ReuseDecisions {
		if err := validateDeploymentOperationText(fmt.Sprintf("reuse decision %d resource id", index), decision.ResourceID, maxDeploymentOperationTextBytes); err != nil {
			return err
		}
		if err := validateDeploymentOperationText(fmt.Sprintf("reuse decision %d reason", index), decision.Reason, maxDeploymentOperationTextBytes); err != nil {
			return err
		}
		if decision.ReuseKeyDigest != "" {
			if err := digest.ValidateSHA256Identity(decision.ReuseKeyDigest); err != nil {
				return fmt.Errorf("deployment operation reuse key digest is invalid: %w", err)
			}
		}
	}
	return nil
}
