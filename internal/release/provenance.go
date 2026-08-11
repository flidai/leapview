package release

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	ocidigest "github.com/opencontainers/go-digest"
)

const ProvenanceVersion = 3

var ErrProvenanceInvalid = errors.New("release provenance invalid")

type WorkspaceArtifactProvenance struct {
	WorkspaceID    string `json:"workspaceId"`
	ArtifactDigest string `json:"artifactDigest"`
}

type ProjectArtifactProvenance struct {
	SourceDigest    string                        `json:"sourceDigest"`
	ProjectDigest   string                        `json:"projectDigest"`
	CompilerVersion string                        `json:"compilerVersion"`
	SchemaVersion   int                           `json:"schemaVersion"`
	Workspaces      []WorkspaceArtifactProvenance `json:"workspaces"`
}

type CandidateProvenance struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
	OwnerID  string `json:"ownerId"`
}

// SourceRevisionProvenance is optional, vendor-neutral change evidence supplied
// by an authoring client. It is release evidence, not an artifact identity:
// identical project bytes retain the same ArtifactDigest across revisions.
type SourceRevisionProvenance struct {
	Revision   string `json:"revision"`
	Repository string `json:"repository,omitempty"`
	Ref        string `json:"ref,omitempty"`
	ChangeID   string `json:"changeId,omitempty"`
}

type ManagedDataPin struct {
	ConnectionID string `json:"connectionId"`
	RevisionID   string `json:"revisionId"`
}

type BindingEvidence struct {
	BindingID          string `json:"bindingId"`
	LogicalConnection  string `json:"logicalConnection"`
	ConnectorKind      string `json:"connectorKind"`
	Revision           int64  `json:"revision"`
	ValidatedVersion   string `json:"validatedVersion"`
	EndpointConfigHash string `json:"endpointConfigHash"`
}

type AuthoredConnectionEvidence struct {
	LogicalConnection string `json:"logicalConnection"`
	ConnectorKind     string `json:"connectorKind"`
}

type TargetDataMode string

const (
	TargetDataReuseSnapshot  TargetDataMode = "reuse_snapshot"
	TargetDataRefreshSources TargetDataMode = "refresh_sources"
)

type TargetWorkspacePlan struct {
	WorkspaceID         string                       `json:"workspaceId"`
	ServingStateID      string                       `json:"servingStateId"`
	ArtifactDigest      string                       `json:"artifactDigest"`
	DataRevision        string                       `json:"dataRevision"`
	DataMode            TargetDataMode               `json:"dataMode"`
	ManagedDataPins     []ManagedDataPin             `json:"managedDataPins"`
	Bindings            []BindingEvidence            `json:"bindings"`
	AuthoredConnections []AuthoredConnectionEvidence `json:"authoredConnections"`
}

type TargetPlanProvenance struct {
	TargetID       string                `json:"targetId"`
	Environment    string                `json:"environment"`
	BaseGeneration string                `json:"baseGeneration"`
	RuntimeVersion string                `json:"runtimeVersion"`
	PolicyDigest   string                `json:"policyDigest"`
	Workspaces     []TargetWorkspacePlan `json:"workspaces"`
}

type ProvenanceInput struct {
	Artifact       ProjectArtifactProvenance
	Candidate      CandidateProvenance
	SourceRevision *SourceRevisionProvenance
	Plan           TargetPlanProvenance
}

type Provenance struct {
	Version        int                       `json:"version"`
	Artifact       ProjectArtifactProvenance `json:"artifact"`
	Candidate      CandidateProvenance       `json:"candidate"`
	SourceRevision *SourceRevisionProvenance `json:"sourceRevision,omitempty"`
	Plan           TargetPlanProvenance      `json:"plan"`
	ArtifactDigest string                    `json:"artifactDigest"`
	PlanDigest     string                    `json:"planDigest"`
	Digest         string                    `json:"digest"`
}

func NewProvenance(input ProvenanceInput) (Provenance, error) {
	artifact, err := normalizeProjectArtifactProvenance(input.Artifact)
	if err != nil {
		return Provenance{}, err
	}
	candidate, err := normalizeCandidateProvenance(input.Candidate)
	if err != nil {
		return Provenance{}, err
	}
	sourceRevision, err := NormalizeSourceRevisionProvenance(input.SourceRevision)
	if err != nil {
		return Provenance{}, err
	}
	plan, err := normalizeTargetPlanProvenance(input.Plan, artifact)
	if err != nil {
		return Provenance{}, err
	}
	artifactDigest, err := canonicalDigest(artifact)
	if err != nil {
		return Provenance{}, provenanceInvalid(err)
	}
	planDigest, err := canonicalDigest(struct {
		Candidate      CandidateProvenance       `json:"candidate"`
		SourceRevision *SourceRevisionProvenance `json:"sourceRevision,omitempty"`
		Plan           TargetPlanProvenance      `json:"plan"`
	}{Candidate: candidate, SourceRevision: sourceRevision, Plan: plan})
	if err != nil {
		return Provenance{}, provenanceInvalid(err)
	}
	releaseDigest, err := canonicalDigest(struct {
		Version        int    `json:"version"`
		ArtifactDigest string `json:"artifactDigest"`
		PlanDigest     string `json:"planDigest"`
	}{
		Version: ProvenanceVersion, ArtifactDigest: artifactDigest,
		PlanDigest: planDigest,
	})
	if err != nil {
		return Provenance{}, provenanceInvalid(err)
	}
	return Provenance{
		Version: ProvenanceVersion, Artifact: artifact, Candidate: candidate,
		SourceRevision: sourceRevision, Plan: plan,
		ArtifactDigest: artifactDigest, PlanDigest: planDigest,
		Digest: releaseDigest,
	}, nil
}

func (provenance Provenance) Validate() error {
	if provenance.Version != ProvenanceVersion {
		return provenanceInvalid(fmt.Errorf(
			"version = %d, want %d; reset target state before deploying",
			provenance.Version,
			ProvenanceVersion,
		))
	}
	expected, err := NewProvenance(ProvenanceInput{
		Artifact: provenance.Artifact, Candidate: provenance.Candidate,
		SourceRevision: provenance.SourceRevision, Plan: provenance.Plan,
	})
	if err != nil {
		return err
	}
	if provenance.ArtifactDigest != expected.ArtifactDigest ||
		provenance.PlanDigest != expected.PlanDigest ||
		provenance.Digest != expected.Digest {
		return provenanceInvalid(fmt.Errorf("content digest mismatch"))
	}
	return nil
}

func NormalizeSourceRevisionProvenance(
	value *SourceRevisionProvenance,
) (*SourceRevisionProvenance, error) {
	if value == nil {
		return nil, nil
	}
	normalized := *value
	normalized.Revision = strings.TrimSpace(normalized.Revision)
	normalized.Repository = strings.TrimSpace(normalized.Repository)
	normalized.Ref = strings.TrimSpace(normalized.Ref)
	normalized.ChangeID = strings.TrimSpace(normalized.ChangeID)
	fields := []struct {
		name  string
		value string
		limit int
	}{
		{name: "revision", value: normalized.Revision, limit: 256},
		{name: "repository", value: normalized.Repository, limit: 2048},
		{name: "ref", value: normalized.Ref, limit: 1024},
		{name: "change id", value: normalized.ChangeID, limit: 512},
	}
	for _, field := range fields {
		if len(field.value) > field.limit ||
			strings.IndexFunc(field.value, unicode.IsControl) >= 0 {
			return nil, provenanceInvalid(fmt.Errorf(
				"source %s is invalid",
				field.name,
			))
		}
	}
	if normalized.Revision == "" {
		return nil, provenanceInvalid(fmt.Errorf("source revision is required"))
	}
	if normalized.Repository != "" {
		parsed, err := url.Parse(normalized.Repository)
		if err != nil || parsed.User != nil ||
			parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, provenanceInvalid(
				fmt.Errorf("source repository must not contain credentials, query, or fragment"),
			)
		}
	}
	return &normalized, nil
}

func normalizeProjectArtifactProvenance(
	artifact ProjectArtifactProvenance,
) (ProjectArtifactProvenance, error) {
	artifact.SourceDigest = strings.TrimSpace(artifact.SourceDigest)
	artifact.ProjectDigest = strings.TrimSpace(artifact.ProjectDigest)
	artifact.CompilerVersion = strings.TrimSpace(artifact.CompilerVersion)
	if platformdigest.ValidateSHA256Identity(artifact.SourceDigest) != nil ||
		platformdigest.ValidateSHA256Identity(artifact.ProjectDigest) != nil ||
		artifact.CompilerVersion == "" || artifact.SchemaVersion < 1 ||
		len(artifact.Workspaces) == 0 {
		return ProjectArtifactProvenance{}, provenanceInvalid(
			fmt.Errorf("source, project, compiler, schema, and workspaces are required"),
		)
	}
	artifact.Workspaces = append(
		[]WorkspaceArtifactProvenance(nil),
		artifact.Workspaces...,
	)
	for index := range artifact.Workspaces {
		item := &artifact.Workspaces[index]
		item.WorkspaceID = strings.TrimSpace(item.WorkspaceID)
		item.ArtifactDigest = strings.TrimSpace(item.ArtifactDigest)
		if item.WorkspaceID == "" ||
			platformdigest.ValidateSHA256Identity(item.ArtifactDigest) != nil {
			return ProjectArtifactProvenance{}, provenanceInvalid(
				fmt.Errorf("workspace identity and artifact digest are required"),
			)
		}
	}
	sort.Slice(artifact.Workspaces, func(i, j int) bool {
		return artifact.Workspaces[i].WorkspaceID < artifact.Workspaces[j].WorkspaceID
	})
	for index := 1; index < len(artifact.Workspaces); index++ {
		if artifact.Workspaces[index-1].WorkspaceID == artifact.Workspaces[index].WorkspaceID {
			return ProjectArtifactProvenance{}, provenanceInvalid(
				fmt.Errorf("duplicate artifact workspace %q", artifact.Workspaces[index].WorkspaceID),
			)
		}
	}
	return artifact, nil
}

func normalizeCandidateProvenance(
	candidate CandidateProvenance,
) (CandidateProvenance, error) {
	candidate.ID = strings.TrimSpace(candidate.ID)
	candidate.OwnerID = strings.TrimSpace(candidate.OwnerID)
	if candidate.ID == "" || candidate.OwnerID == "" || candidate.Revision < 1 {
		return CandidateProvenance{}, provenanceInvalid(
			fmt.Errorf("candidate identity, owner, and positive revision are required"),
		)
	}
	return candidate, nil
}

func normalizeTargetPlanProvenance(
	plan TargetPlanProvenance,
	artifact ProjectArtifactProvenance,
) (TargetPlanProvenance, error) {
	plan.TargetID = strings.TrimSpace(plan.TargetID)
	plan.Environment = strings.TrimSpace(plan.Environment)
	plan.BaseGeneration = strings.TrimSpace(plan.BaseGeneration)
	plan.RuntimeVersion = strings.TrimSpace(plan.RuntimeVersion)
	plan.PolicyDigest = strings.TrimSpace(plan.PolicyDigest)
	if plan.TargetID == "" || plan.Environment == "" ||
		plan.BaseGeneration == "" || plan.RuntimeVersion == "" ||
		platformdigest.ValidateSHA256Identity(plan.PolicyDigest) != nil ||
		len(plan.Workspaces) == 0 {
		return TargetPlanProvenance{}, provenanceInvalid(
			fmt.Errorf("target, environment, base, runtime, policy, and workspaces are required"),
		)
	}
	artifactWorkspaces := make(map[string]struct{}, len(artifact.Workspaces))
	for _, workspace := range artifact.Workspaces {
		artifactWorkspaces[workspace.WorkspaceID] = struct{}{}
	}
	plan.Workspaces = append([]TargetWorkspacePlan(nil), plan.Workspaces...)
	for index := range plan.Workspaces {
		workspace := &plan.Workspaces[index]
		workspace.WorkspaceID = strings.TrimSpace(workspace.WorkspaceID)
		workspace.ServingStateID = strings.TrimSpace(workspace.ServingStateID)
		workspace.ArtifactDigest = strings.TrimSpace(workspace.ArtifactDigest)
		workspace.DataRevision = strings.TrimSpace(workspace.DataRevision)
		if _, exists := artifactWorkspaces[workspace.WorkspaceID]; !exists ||
			workspace.ServingStateID == "" || workspace.DataRevision == "" ||
			platformdigest.ValidateSHA256Identity(workspace.ArtifactDigest) != nil {
			return TargetPlanProvenance{}, provenanceInvalid(
				fmt.Errorf("target workspace identity, artifact, and data revision are required"),
			)
		}
		var err error
		workspace.ManagedDataPins, err = normalizeManagedDataPins(workspace.ManagedDataPins)
		if err != nil {
			return TargetPlanProvenance{}, err
		}
		workspace.Bindings, err = normalizeBindingEvidence(workspace.Bindings)
		if err != nil {
			return TargetPlanProvenance{}, err
		}
		workspace.AuthoredConnections, err = normalizeAuthoredConnectionEvidence(
			workspace.AuthoredConnections,
		)
		if err != nil {
			return TargetPlanProvenance{}, err
		}
		switch workspace.DataMode {
		case TargetDataReuseSnapshot:
			if len(workspace.AuthoredConnections) != 0 {
				return TargetPlanProvenance{}, provenanceInvalid(
					fmt.Errorf("snapshot reuse cannot retain authored refresh connection evidence"),
				)
			}
		case TargetDataRefreshSources:
			if len(workspace.Bindings) == 0 &&
				len(workspace.ManagedDataPins) == 0 &&
				len(workspace.AuthoredConnections) == 0 {
				return TargetPlanProvenance{}, provenanceInvalid(
					fmt.Errorf("source refresh requires target, managed-data, or authored connection evidence"),
				)
			}
		default:
			return TargetPlanProvenance{}, provenanceInvalid(
				fmt.Errorf("target workspace data mode is invalid"),
			)
		}
	}
	sort.Slice(plan.Workspaces, func(i, j int) bool {
		return plan.Workspaces[i].WorkspaceID < plan.Workspaces[j].WorkspaceID
	})
	if len(plan.Workspaces) != len(artifact.Workspaces) {
		return TargetPlanProvenance{}, provenanceInvalid(
			fmt.Errorf("target plan must cover every artifact workspace"),
		)
	}
	for index := range plan.Workspaces {
		if plan.Workspaces[index].WorkspaceID != artifact.Workspaces[index].WorkspaceID ||
			index > 0 && plan.Workspaces[index-1].WorkspaceID == plan.Workspaces[index].WorkspaceID {
			return TargetPlanProvenance{}, provenanceInvalid(
				fmt.Errorf("target plan workspace set does not match artifact"),
			)
		}
	}
	return plan, nil
}

func normalizeAuthoredConnectionEvidence(
	values []AuthoredConnectionEvidence,
) ([]AuthoredConnectionEvidence, error) {
	values = append([]AuthoredConnectionEvidence(nil), values...)
	for index := range values {
		values[index].LogicalConnection = strings.TrimSpace(values[index].LogicalConnection)
		values[index].ConnectorKind = strings.TrimSpace(values[index].ConnectorKind)
	}
	sort.Slice(values, func(i, j int) bool {
		return values[i].LogicalConnection < values[j].LogicalConnection
	})
	for index, value := range values {
		if value.LogicalConnection == "" || value.ConnectorKind == "" ||
			index > 0 && values[index-1].LogicalConnection == value.LogicalConnection {
			return nil, provenanceInvalid(fmt.Errorf(
				"authored connection identity and connector kind are required and unique",
			))
		}
	}
	return values, nil
}

func normalizeManagedDataPins(values []ManagedDataPin) ([]ManagedDataPin, error) {
	values = append([]ManagedDataPin(nil), values...)
	for index := range values {
		values[index].ConnectionID = strings.TrimSpace(values[index].ConnectionID)
		values[index].RevisionID = strings.TrimSpace(values[index].RevisionID)
		if values[index].ConnectionID == "" ||
			platformdigest.ValidateSHA256Identity(values[index].RevisionID) != nil {
			return nil, provenanceInvalid(
				fmt.Errorf("managed-data pin identity and revision are required"),
			)
		}
	}
	sort.Slice(values, func(i, j int) bool {
		return values[i].ConnectionID < values[j].ConnectionID
	})
	for index := 1; index < len(values); index++ {
		if values[index-1].ConnectionID == values[index].ConnectionID {
			return nil, provenanceInvalid(
				fmt.Errorf("duplicate managed-data pin %q", values[index].ConnectionID),
			)
		}
	}
	return values, nil
}

func normalizeBindingEvidence(values []BindingEvidence) ([]BindingEvidence, error) {
	values = append([]BindingEvidence(nil), values...)
	logicalConnections := make(map[string]struct{}, len(values))
	for index := range values {
		values[index].BindingID = strings.TrimSpace(values[index].BindingID)
		values[index].LogicalConnection = strings.TrimSpace(values[index].LogicalConnection)
		values[index].ConnectorKind = strings.TrimSpace(values[index].ConnectorKind)
		values[index].ValidatedVersion = strings.TrimSpace(values[index].ValidatedVersion)
		values[index].EndpointConfigHash = strings.TrimSpace(values[index].EndpointConfigHash)
		if values[index].BindingID == "" || values[index].LogicalConnection == "" ||
			values[index].ConnectorKind == "" || values[index].Revision < 1 ||
			values[index].ValidatedVersion == "" ||
			platformdigest.ValidateSHA256Identity(values[index].EndpointConfigHash) != nil {
			return nil, provenanceInvalid(
				fmt.Errorf("binding identity, connector, revision, version, and endpoint hash are required"),
			)
		}
		if _, exists := logicalConnections[values[index].LogicalConnection]; exists {
			return nil, provenanceInvalid(
				fmt.Errorf("duplicate logical connection evidence %q", values[index].LogicalConnection),
			)
		}
		logicalConnections[values[index].LogicalConnection] = struct{}{}
	}
	sort.Slice(values, func(i, j int) bool {
		return values[i].BindingID < values[j].BindingID
	})
	for index := 1; index < len(values); index++ {
		if values[index-1].BindingID == values[index].BindingID {
			return nil, provenanceInvalid(
				fmt.Errorf("duplicate binding evidence %q", values[index].BindingID),
			)
		}
	}
	return values, nil
}

func canonicalDigest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return ocidigest.FromBytes(encoded).String(), nil
}

func provenanceInvalid(err error) error {
	return fmt.Errorf("%w: %v", ErrProvenanceInvalid, err)
}
