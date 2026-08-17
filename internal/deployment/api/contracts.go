package api

type PageInfo struct {
	NextCursor *string `json:"nextCursor,omitempty"`
}

type PageParams struct {
	Limit     *int32
	PageToken *string
}

type CandidateStartRequest struct {
	ArtifactDigest string  `json:"artifactDigest"`
	CandidateKey   *string `json:"candidateKey,omitempty"`
}

type CandidateArtifactRequest struct {
	ExpectedArtifactDigest string `json:"expectedArtifactDigest"`
	ArtifactDigest         string `json:"artifactDigest"`
}

type CandidatePublishRequest struct {
	ExpectedRevision int64  `json:"expectedRevision"`
	ProvenanceDigest string `json:"provenanceDigest"`
	TargetID         string `json:"targetId"`
	Bootstrap        bool   `json:"bootstrap,omitempty"`
}

type CandidateSourceArtifact struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

type CandidateSourceRevision struct {
	Revision   string  `json:"revision"`
	Repository *string `json:"repository,omitempty"`
	Ref        *string `json:"ref,omitempty"`
	ChangeID   *string `json:"changeId,omitempty"`
}

type CandidateSynchronizationRequest struct {
	ProjectFile            string                    `json:"projectFile"`
	ArtifactDigest         string                    `json:"artifactDigest"`
	CandidateKey           *string                   `json:"candidateKey,omitempty"`
	SourceRevision         *CandidateSourceRevision  `json:"sourceRevision,omitempty"`
	ExpectedCandidateID    *string                   `json:"expectedCandidateId,omitempty"`
	ExpectedArtifactDigest *string                   `json:"expectedArtifactDigest,omitempty"`
	Artifacts              []CandidateSourceArtifact `json:"artifacts"`
}

type CandidateSynchronizationPlanResponse struct {
	ArtifactDigest string   `json:"artifactDigest"`
	MissingDigests []string `json:"missingDigests"`
}

type CandidateSourceBlobResponse struct {
	Digest    string `json:"digest"`
	SizeBytes int64  `json:"sizeBytes"`
}

type CandidateResponse struct {
	ID               string  `json:"id"`
	ProjectID        string  `json:"projectId"`
	CandidateKey     string  `json:"candidateKey"`
	TargetID         string  `json:"targetId"`
	Environment      string  `json:"environment"`
	OwnerID          string  `json:"ownerId"`
	BaseGeneration   string  `json:"baseGeneration"`
	ArtifactDigest   string  `json:"artifactDigest"`
	ProvenanceDigest *string `json:"provenanceDigest,omitempty"`
	Status           string  `json:"status"`
	FailureReason    *string `json:"failureReason,omitempty"`
	PreviewURL       string  `json:"previewUrl"`
	ExpiresAt        string  `json:"expiresAt"`
	CreatedAt        string  `json:"createdAt"`
	UpdatedAt        string  `json:"updatedAt"`
	Revision         int64   `json:"revision"`
	Resumed          *bool   `json:"resumed,omitempty"`
}

type CreateRequest struct {
	ReleaseID string `json:"releaseId"`
}

type ApprovalDecisionRequest struct {
	ExpectedRevision int64 `json:"expectedRevision"`
}

type ApprovalResponse struct {
	ID            string  `json:"id"`
	ProjectID     string  `json:"projectId"`
	DeploymentID  string  `json:"deploymentId"`
	Environment   string  `json:"environment"`
	RequestDigest string  `json:"requestDigest"`
	ReleaseID     string  `json:"releaseId"`
	Status        string  `json:"status"`
	RequestedBy   string  `json:"requestedBy"`
	RequestedAt   string  `json:"requestedAt"`
	ApprovedBy    *string `json:"approvedBy,omitempty"`
	ApprovedAt    *string `json:"approvedAt,omitempty"`
	DeniedBy      *string `json:"deniedBy,omitempty"`
	DeniedAt      *string `json:"deniedAt,omitempty"`
	RevokedBy     *string `json:"revokedBy,omitempty"`
	RevokedAt     *string `json:"revokedAt,omitempty"`
	ExpiresAt     string  `json:"expiresAt"`
	Revision      int64   `json:"revision"`
}

type PublishEvidenceResponse struct {
	ReleaseDigest            string                   `json:"releaseDigest"`
	ArtifactContentDigest    string                   `json:"artifactContentDigest"`
	ArtifactProvenanceDigest string                   `json:"artifactProvenanceDigest"`
	PlanDigest               string                   `json:"planDigest"`
	CandidateID              string                   `json:"candidateId"`
	CandidateRevision        int64                    `json:"candidateRevision"`
	TargetID                 string                   `json:"targetId"`
	Environment              string                   `json:"environment"`
	GenerationID             string                   `json:"generationId"`
	BaseGenerationID         *string                  `json:"baseGenerationId,omitempty"`
	RuntimeVersion           string                   `json:"runtimeVersion"`
	PolicyDigest             string                   `json:"policyDigest"`
	SourceRevision           *CandidateSourceRevision `json:"sourceRevision,omitempty"`
}

type ManagedDataPinEvidence struct {
	ConnectionID string `json:"connectionId"`
	RevisionID   string `json:"revisionId"`
}

type BindingEvidence struct {
	BindingID          string `json:"bindingId"`
	ConnectionID       string `json:"connectionId"`
	ConnectorKind      string `json:"connectorKind"`
	Revision           int64  `json:"revision"`
	ValidatedVersion   string `json:"validatedVersion"`
	EndpointConfigHash string `json:"endpointConfigHash"`
}

type Status string

const StatusQueued Status = "queued"

type VerificationResponse struct {
	Digest     string `json:"digest"`
	VerifiedAt string `json:"verifiedAt"`
}

type Response struct {
	CreatedAt           string                  `json:"createdAt"`
	CreatedBy           string                  `json:"createdBy"`
	ActivationPrincipal *string                 `json:"activationPrincipal,omitempty"`
	Verification        *VerificationResponse   `json:"verification,omitempty"`
	Environment         string                  `json:"environment"`
	GenerationID        string                  `json:"generationId"`
	ArtifactDigest      string                  `json:"artifactDigest"`
	PriorGenerationID   *string                 `json:"priorGenerationId,omitempty"`
	RequestDigest       string                  `json:"requestDigest"`
	Evidence            PublishEvidenceResponse `json:"evidence"`
	Error               *string                 `json:"error,omitempty"`
	Approval            *ApprovalResponse       `json:"approval,omitempty"`
	FinishedAt          *string                 `json:"finishedAt,omitempty"`
	ID                  string                  `json:"id"`
	ProjectID           string                  `json:"projectId"`
	ReleaseID           string                  `json:"releaseId"`
	StartedAt           *string                 `json:"startedAt,omitempty"`
	Status              Status                  `json:"status"`
}

type ListResponse struct {
	Items []Response `json:"items"`
	Page  PageInfo   `json:"page"`
}
