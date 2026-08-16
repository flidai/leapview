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
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	ocidigest "github.com/opencontainers/go-digest"
)

// ProvenanceVersion is bumped whenever the canonical project-generation
// evidence shape changes. Provenance is immutable release evidence.
const ProvenanceVersion = 4

var ErrProvenanceInvalid = errors.New("release provenance invalid")

type CandidateProvenance struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
	OwnerID  string `json:"ownerId"`
}

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

type GenerationDataMode string

const (
	GenerationDataReuseSnapshot  GenerationDataMode = "reuse_snapshot"
	GenerationDataRefreshSources GenerationDataMode = "refresh_sources"
)

// ProjectArtifactProvenance identifies one complete project artifact. There
// is no workspace collection: a generation digest covers the whole graph.
type ProjectArtifactProvenance struct {
	SourceDigest    string `json:"sourceDigest"`
	ProjectDigest   string `json:"projectDigest"`
	ContentDigest   string `json:"contentDigest"`
	CompilerVersion string `json:"compilerVersion"`
	SchemaVersion   int    `json:"schemaVersion"`
}

// GenerationPlanProvenance binds candidate evidence to the exact serving identity
// that will be published. GenerationID is the sole runtime selector.
type GenerationPlanProvenance struct {
	Identity            projectgraph.ServingIdentity  `json:"identity"`
	BaseIdentity        *projectgraph.ServingIdentity `json:"baseIdentity,omitempty"`
	TargetID            string                        `json:"targetId"`
	RuntimeVersion      string                        `json:"runtimeVersion"`
	PolicyDigest        string                        `json:"policyDigest"`
	DataRevision        string                        `json:"dataRevision"`
	DataMode            GenerationDataMode            `json:"dataMode"`
	ManagedDataPins     []ManagedDataPin              `json:"managedDataPins"`
	Bindings            []BindingEvidence             `json:"bindings"`
	AuthoredConnections []AuthoredConnectionEvidence  `json:"authoredConnections"`
}

type ProvenanceInput struct {
	Artifact       ProjectArtifactProvenance
	Candidate      CandidateProvenance
	SourceRevision *SourceRevisionProvenance
	Plan           GenerationPlanProvenance
}

type Provenance struct {
	Version                  int                       `json:"version"`
	Artifact                 ProjectArtifactProvenance `json:"artifact"`
	Candidate                CandidateProvenance       `json:"candidate"`
	SourceRevision           *SourceRevisionProvenance `json:"sourceRevision,omitempty"`
	Plan                     GenerationPlanProvenance  `json:"plan"`
	ArtifactProvenanceDigest string                    `json:"artifactProvenanceDigest"`
	PlanDigest               string                    `json:"planDigest"`
	Digest                   string                    `json:"digest"`
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
	source, err := NormalizeSourceRevisionProvenance(input.SourceRevision)
	if err != nil {
		return Provenance{}, err
	}
	plan, err := normalizeGenerationPlanProvenance(input.Plan, artifact)
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
		Plan           GenerationPlanProvenance  `json:"plan"`
	}{candidate, source, plan})
	if err != nil {
		return Provenance{}, provenanceInvalid(err)
	}
	digest, err := canonicalDigest(struct {
		Version                  int    `json:"version"`
		ArtifactProvenanceDigest string `json:"artifactProvenanceDigest"`
		PlanDigest               string `json:"planDigest"`
	}{ProvenanceVersion, artifactDigest, planDigest})
	if err != nil {
		return Provenance{}, provenanceInvalid(err)
	}
	return Provenance{Version: ProvenanceVersion, Artifact: artifact, Candidate: candidate, SourceRevision: source, Plan: plan, ArtifactProvenanceDigest: artifactDigest, PlanDigest: planDigest, Digest: digest}, nil
}

func (p Provenance) Validate() error {
	if p.Version != ProvenanceVersion {
		return provenanceInvalid(fmt.Errorf("version = %d, want %d", p.Version, ProvenanceVersion))
	}
	expected, err := NewProvenance(ProvenanceInput{Artifact: p.Artifact, Candidate: p.Candidate, SourceRevision: p.SourceRevision, Plan: p.Plan})
	if err != nil {
		return err
	}
	if p.ArtifactProvenanceDigest != expected.ArtifactProvenanceDigest || p.PlanDigest != expected.PlanDigest || p.Digest != expected.Digest {
		return provenanceInvalid(errors.New("content digest mismatch"))
	}
	return nil
}

func NormalizeSourceRevisionProvenance(value *SourceRevisionProvenance) (*SourceRevisionProvenance, error) {
	if value == nil {
		return nil, nil
	}
	n := *value
	n.Revision, n.Repository, n.Ref, n.ChangeID = strings.TrimSpace(n.Revision), strings.TrimSpace(n.Repository), strings.TrimSpace(n.Ref), strings.TrimSpace(n.ChangeID)
	for _, field := range []struct {
		name, value string
		limit       int
	}{{"revision", n.Revision, 256}, {"repository", n.Repository, 2048}, {"ref", n.Ref, 1024}, {"change id", n.ChangeID, 512}} {
		if field.value == "" && field.name == "revision" || len(field.value) > field.limit || strings.IndexFunc(field.value, unicode.IsControl) >= 0 {
			return nil, provenanceInvalid(fmt.Errorf("source %s is invalid", field.name))
		}
	}
	if n.Repository != "" {
		parsed, err := url.Parse(n.Repository)
		if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, provenanceInvalid(errors.New("source repository must not contain credentials, query, or fragment"))
		}
	}
	return &n, nil
}

func normalizeProjectArtifactProvenance(a ProjectArtifactProvenance) (ProjectArtifactProvenance, error) {
	if !canonicalLiteral(a.SourceDigest) || !canonicalLiteral(a.ProjectDigest) || !canonicalLiteral(a.ContentDigest) || !canonicalLiteral(a.CompilerVersion) {
		return ProjectArtifactProvenance{}, provenanceInvalid(errors.New("artifact provenance literals must be canonical"))
	}
	if platformdigest.ValidateSHA256Identity(a.SourceDigest) != nil || platformdigest.ValidateSHA256Identity(a.ProjectDigest) != nil || platformdigest.ValidateSHA256Identity(a.ContentDigest) != nil || a.CompilerVersion == "" || a.SchemaVersion < 1 {
		return ProjectArtifactProvenance{}, provenanceInvalid(errors.New("source, project, artifact, compiler, and schema are required"))
	}
	return a, nil
}

func normalizeCandidateProvenance(c CandidateProvenance) (CandidateProvenance, error) {
	if !canonicalLiteral(c.ID) || !canonicalLiteral(c.OwnerID) {
		return CandidateProvenance{}, provenanceInvalid(errors.New("candidate identity literals must be canonical"))
	}
	if c.ID == "" || c.OwnerID == "" || c.Revision < 1 {
		return CandidateProvenance{}, provenanceInvalid(errors.New("candidate identity, owner, and positive revision are required"))
	}
	return c, nil
}

func normalizeGenerationPlanProvenance(p GenerationPlanProvenance, artifact ProjectArtifactProvenance) (GenerationPlanProvenance, error) {
	if err := p.Identity.Validate(); err != nil {
		return GenerationPlanProvenance{}, provenanceInvalid(err)
	}
	if p.BaseIdentity != nil {
		base := *p.BaseIdentity
		p.BaseIdentity = &base
	}
	if p.BaseIdentity != nil {
		if err := p.BaseIdentity.Validate(); err != nil {
			return GenerationPlanProvenance{}, provenanceInvalid(err)
		}
		if p.BaseIdentity.ProjectID != p.Identity.ProjectID || p.BaseIdentity.Environment != p.Identity.Environment {
			return GenerationPlanProvenance{}, provenanceInvalid(errors.New("base identity scope does not match generation identity"))
		}
	}
	if !canonicalLiteral(p.RuntimeVersion) || !canonicalLiteral(p.PolicyDigest) || !canonicalLiteral(p.DataRevision) {
		return GenerationPlanProvenance{}, provenanceInvalid(errors.New("generation plan literals must be canonical"))
	}
	if validateOperationalID(p.TargetID) != nil || p.RuntimeVersion == "" || platformdigest.ValidateSHA256Identity(p.PolicyDigest) != nil || p.DataRevision == "" || artifact.ContentDigest == "" {
		return GenerationPlanProvenance{}, provenanceInvalid(errors.New("runtime, policy, data, and artifact evidence are required"))
	}
	if p.DataMode != GenerationDataReuseSnapshot && p.DataMode != GenerationDataRefreshSources {
		return GenerationPlanProvenance{}, provenanceInvalid(errors.New("data mode is invalid"))
	}
	var err error
	p.ManagedDataPins, err = normalizeManagedDataPins(p.ManagedDataPins)
	if err != nil {
		return GenerationPlanProvenance{}, err
	}
	p.Bindings, err = normalizeBindingEvidence(p.Bindings)
	if err != nil {
		return GenerationPlanProvenance{}, err
	}
	p.AuthoredConnections, err = normalizeAuthoredConnectionEvidence(p.AuthoredConnections)
	if err != nil {
		return GenerationPlanProvenance{}, err
	}
	if p.DataMode == GenerationDataReuseSnapshot && len(p.AuthoredConnections) != 0 {
		return GenerationPlanProvenance{}, provenanceInvalid(errors.New("snapshot reuse cannot retain authored refresh connection evidence"))
	}
	if p.DataMode == GenerationDataRefreshSources && len(p.Bindings) == 0 && len(p.ManagedDataPins) == 0 && len(p.AuthoredConnections) == 0 {
		return GenerationPlanProvenance{}, provenanceInvalid(errors.New("source refresh requires connection evidence"))
	}
	return p, nil
}

func normalizeAuthoredConnectionEvidence(values []AuthoredConnectionEvidence) ([]AuthoredConnectionEvidence, error) {
	values = append([]AuthoredConnectionEvidence(nil), values...)
	for i := range values {
		if values[i].ConnectionID != strings.TrimSpace(values[i].ConnectionID) || values[i].ConnectorKind != strings.TrimSpace(values[i].ConnectorKind) {
			return nil, provenanceInvalid(errors.New("authored connection identity must be canonical"))
		}
		values[i].ConnectionID, values[i].ConnectorKind = strings.TrimSpace(values[i].ConnectionID), strings.TrimSpace(values[i].ConnectorKind)
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ConnectionID < values[j].ConnectionID })
	for i, v := range values {
		if v.ConnectionID == "" || projectgraph.ResourceID(v.ConnectionID).Validate() != nil || v.ConnectorKind == "" || i > 0 && values[i-1].ConnectionID == v.ConnectionID {
			return nil, provenanceInvalid(errors.New("authored connection evidence must be unique"))
		}
	}
	return values, nil
}

func normalizeManagedDataPins(values []ManagedDataPin) ([]ManagedDataPin, error) {
	values = append([]ManagedDataPin(nil), values...)
	for i := range values {
		if values[i].ConnectionID != strings.TrimSpace(values[i].ConnectionID) || values[i].RevisionID != strings.TrimSpace(values[i].RevisionID) {
			return nil, provenanceInvalid(errors.New("managed-data pin identity must be canonical"))
		}
		values[i].ConnectionID, values[i].RevisionID = strings.TrimSpace(values[i].ConnectionID), strings.TrimSpace(values[i].RevisionID)
		if values[i].ConnectionID == "" || projectgraph.ResourceID(values[i].ConnectionID).Validate() != nil || validateOperationalID(values[i].RevisionID) != nil {
			return nil, provenanceInvalid(errors.New("managed-data pin is invalid"))
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ConnectionID < values[j].ConnectionID })
	for i := 1; i < len(values); i++ {
		if values[i-1].ConnectionID == values[i].ConnectionID {
			return nil, provenanceInvalid(errors.New("duplicate managed-data pin"))
		}
	}
	return values, nil
}

func normalizeBindingEvidence(values []BindingEvidence) ([]BindingEvidence, error) {
	values = append([]BindingEvidence(nil), values...)
	for i := range values {
		v := &values[i]
		if v.BindingID != strings.TrimSpace(v.BindingID) || v.ConnectionID != strings.TrimSpace(v.ConnectionID) || v.ConnectorKind != strings.TrimSpace(v.ConnectorKind) || v.ValidatedVersion != strings.TrimSpace(v.ValidatedVersion) || v.EndpointConfigHash != strings.TrimSpace(v.EndpointConfigHash) {
			return nil, provenanceInvalid(errors.New("binding evidence identity must be canonical"))
		}
		v.BindingID, v.ConnectionID, v.ConnectorKind, v.ValidatedVersion, v.EndpointConfigHash = strings.TrimSpace(v.BindingID), strings.TrimSpace(v.ConnectionID), strings.TrimSpace(v.ConnectorKind), strings.TrimSpace(v.ValidatedVersion), strings.TrimSpace(v.EndpointConfigHash)
		if validateOperationalID(v.BindingID) != nil || v.ConnectionID == "" || projectgraph.ResourceID(v.ConnectionID).Validate() != nil || v.ConnectorKind == "" || v.Revision < 1 || v.ValidatedVersion == "" || platformdigest.ValidateSHA256Identity(v.EndpointConfigHash) != nil {
			return nil, provenanceInvalid(errors.New("binding evidence is invalid"))
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].BindingID < values[j].BindingID })
	for i := 1; i < len(values); i++ {
		if values[i-1].BindingID == values[i].BindingID {
			return nil, provenanceInvalid(errors.New("duplicate binding evidence"))
		}
	}
	return values, nil
}

// validateOperationalID checks an opaque managed-data/binding identifier
// without imposing graph-resource syntax on another subsystem's namespace.
func validateOperationalID(value string) error {
	if value == "" || value != strings.TrimSpace(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return errors.New("operational identity is invalid")
	}
	return nil
}

func canonicalLiteral(value string) bool {
	return value == strings.TrimSpace(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

func canonicalDigest(value any) (string, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return ocidigest.FromBytes(b).String(), nil
}

func provenanceInvalid(err error) error { return fmt.Errorf("%w: %v", ErrProvenanceInvalid, err) }
