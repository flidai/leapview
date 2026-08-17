// Package api contains the stable HTTP contract for project-generation
// releases. A release identifies one immutable serving generation; it does
// not embed partial graph manifests or target collections.
package api

type PageInfo struct {
	NextCursor *string `json:"nextCursor,omitempty"`
}

type PageParams struct {
	Limit     *int32
	PageToken *string
}

type ServingIdentity struct {
	ProjectID    string `json:"projectId"`
	Environment  string `json:"environment"`
	GenerationID string `json:"generationId"`
}

type ConnectionPin struct {
	Connection string `json:"connection"`
	RevisionID string `json:"revisionId"`
}

type ProjectArtifactProvenance struct {
	SourceDigest    string `json:"sourceDigest"`
	ProjectDigest   string `json:"projectDigest"`
	ContentDigest   string `json:"contentDigest"`
	CompilerVersion string `json:"compilerVersion"`
	SchemaVersion   int32  `json:"schemaVersion"`
}

type CandidateProvenance struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
	OwnerID  string `json:"ownerId"`
}

type SourceRevisionProvenance struct {
	Revision   string  `json:"revision"`
	Repository *string `json:"repository,omitempty"`
	Ref        *string `json:"ref,omitempty"`
	ChangeID   *string `json:"changeId,omitempty"`
}

type ManagedDataPin struct {
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

type AuthoredConnectionEvidence struct {
	ConnectionID  string `json:"connectionId"`
	ConnectorKind string `json:"connectorKind"`
	DisplayName   string `json:"displayName,omitempty"`
}

type GenerationPlanProvenance struct {
	Identity            ServingIdentity              `json:"identity"`
	BaseIdentity        *ServingIdentity             `json:"baseIdentity,omitempty"`
	TargetID            string                       `json:"targetId"`
	RuntimeVersion      string                       `json:"runtimeVersion"`
	PolicyDigest        string                       `json:"policyDigest"`
	DataRevision        string                       `json:"dataRevision"`
	DataMode            string                       `json:"dataMode"`
	ManagedDataPins     []ManagedDataPin             `json:"managedDataPins"`
	Bindings            []BindingEvidence            `json:"bindings"`
	AuthoredConnections []AuthoredConnectionEvidence `json:"authoredConnections"`
}

type Provenance struct {
	Version                  int32                     `json:"version"`
	Artifact                 ProjectArtifactProvenance `json:"artifact"`
	Candidate                CandidateProvenance       `json:"candidate"`
	SourceRevision           *SourceRevisionProvenance `json:"sourceRevision,omitempty"`
	Plan                     GenerationPlanProvenance  `json:"plan"`
	ArtifactProvenanceDigest string                    `json:"artifactProvenanceDigest"`
	PlanDigest               string                    `json:"planDigest"`
	Digest                   string                    `json:"digest"`
}

type CreateRequest struct {
	Environment    string          `json:"environment"`
	GenerationID   string          `json:"generationId"`
	ArtifactDigest string          `json:"artifactDigest"`
	Connections    []ConnectionPin `json:"connections"`
	ProjectDigest  string          `json:"projectDigest"`
	RequestDigest  string          `json:"requestDigest,omitempty"`
	Provenance     *Provenance     `json:"provenance,omitempty"`
}

type Status string

type Response struct {
	ArtifactDigest string          `json:"artifactDigest"`
	ArtifactSize   int64           `json:"artifactSizeBytes"`
	ActualDigest   string          `json:"actualDigest,omitempty"`
	Environment    string          `json:"environment"`
	GenerationID   string          `json:"generationId"`
	Connections    []ConnectionPin `json:"connections"`
	CreatedAt      string          `json:"createdAt"`
	CreatedBy      string          `json:"createdBy"`
	Error          *string         `json:"error,omitempty"`
	FinalizedAt    *string         `json:"finalizedAt,omitempty"`
	ID             string          `json:"id"`
	ProjectDigest  string          `json:"projectDigest"`
	ProjectID      string          `json:"projectId"`
	Provenance     *Provenance     `json:"provenance,omitempty"`
	Status         Status          `json:"status"`
}

type ListResponse struct {
	Items []Response `json:"items"`
	Page  PageInfo   `json:"page"`
}

type ArtifactResponse struct {
	ActualDigest string `json:"actualDigest"`
	Digest       string `json:"digest"`
	GenerationID string `json:"generationId"`
	ReleaseID    string `json:"releaseId"`
	SizeBytes    int64  `json:"sizeBytes"`
}

type ManagedConnectionResponse struct {
	ActiveRevisionID *string `json:"activeRevisionId,omitempty"`
	Description      *string `json:"description,omitempty"`
	ID               string  `json:"id"`
	ProjectID        string  `json:"projectId"`
	Title            string  `json:"title"`
}

type ManagedConnectionListResponse struct {
	Items []ManagedConnectionResponse `json:"items"`
	Page  PageInfo                    `json:"page"`
}
