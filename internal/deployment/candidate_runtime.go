package deployment

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	"github.com/flidai/leapview/internal/runtimehost"
)

// CandidateConnectionRequirement and CandidateAuthoredConnection are
// project-level connection evidence. They are intentionally not nested in a
// partial target collection.
type CandidateConnectionRequirement struct {
	ConnectionID  projectgraph.ResourceID
	ConnectorKind string
	Access        semanticmodel.ConnectionAccess
}

type CandidateAuthoredConnection struct {
	ConnectionID  projectgraph.ResourceID
	ConnectorKind string
	Access        semanticmodel.ConnectionAccess
}

type CandidateRestriction struct {
	ID             string
	ObjectID       projectgraph.ResourceID
	ObjectKind     projectgraph.Kind
	Subject        *access.SubjectRef
	PolicyType     string
	ExpressionJSON string
}

type CandidateDataMode string

const (
	CandidateDataReuseBase      CandidateDataMode = "reuse_base"
	CandidateDataRefreshSources CandidateDataMode = "refresh_sources"
)

type CandidateConnectionEvidence struct {
	BindingID          string
	ConnectionID       projectgraph.ResourceID
	ConnectorKind      string
	Revision           int64
	ProviderVersion    string
	EndpointConfigHash string
	Access             semanticmodel.ConnectionAccess
}

// CandidateConnectionRequest is one project-generation connection lease.
type CandidateConnectionRequest struct {
	CandidateID         string
	Actor               string
	TargetID            string
	Identity            projectgraph.ServingIdentity
	Requirements        []CandidateConnectionRequirement
	AuthoredConnections []CandidateAuthoredConnection
}

type CandidateConnectionLeases interface {
	runtimehost.RuntimeLifetime
	Evidence() []CandidateConnectionEvidence
}

type CandidateConnectionLeaser interface {
	Acquire(context.Context, CandidateConnectionRequest) (CandidateConnectionLeases, error)
}

type CandidateRuntimeHost interface {
	PrepareAndRegisterCandidateSet(context.Context, []runtimehost.CandidatePreparation) error
}

type CandidateRuntimeServiceConfig struct {
	Connections    CandidateConnectionLeaser
	Runtime        CandidateRuntimeHost
	RuntimeVersion string
}

type CandidateRuntimeService struct {
	connections    CandidateConnectionLeaser
	runtime        CandidateRuntimeHost
	runtimeVersion string
}

// CandidateGenerationRuntime is the sole candidate preparation handoff. It
// represents one immutable generation artifact and its project-wide evidence.
type CandidateGenerationRuntime struct {
	Identity               projectgraph.ServingIdentity
	ArtifactDigest         string
	DataRevision           string
	DataMode               CandidateDataMode
	Connections            []CandidateConnectionRequirement
	AuthoredConnections    []CandidateAuthoredConnection
	ManagedDataConnections []string
	Restrictions           []CandidateRestriction
	BindingFingerprint     string
	GateEvidence           *release.GateEvidence
}

type CandidateRuntimeRequest struct {
	Candidate                Candidate
	AuthorizationFingerprint string
	Generation               CandidateGenerationRuntime
}

type CandidateRuntimeReceipt struct {
	RuntimeVersion     string
	Bindings           []CandidateConnectionEvidence
	BindingFingerprint string
	GateEvidence       *release.GateEvidence
}

// BindingFingerprint hashes the canonical, non-secret evidence returned by a
// target connection lease. Endpoint material and credentials never enter this
// preimage; only their validated configuration digest is retained.
func BindingFingerprint(values []CandidateConnectionEvidence) (string, error) {
	bindings, err := candidateBindingVersions(values)
	if err != nil {
		return "", err
	}
	preimage := make([]struct {
		BindingID          string                         `json:"bindingId"`
		ConnectionID       string                         `json:"connectionId"`
		ConnectorKind      string                         `json:"connectorKind"`
		Revision           int64                          `json:"revision"`
		ProviderVersion    string                         `json:"providerVersion"`
		EndpointConfigHash string                         `json:"endpointConfigHash"`
		Access             semanticmodel.ConnectionAccess `json:"access,omitempty"`
	}, len(bindings))
	for i, binding := range bindings {
		preimage[i] = struct {
			BindingID          string                         `json:"bindingId"`
			ConnectionID       string                         `json:"connectionId"`
			ConnectorKind      string                         `json:"connectorKind"`
			Revision           int64                          `json:"revision"`
			ProviderVersion    string                         `json:"providerVersion"`
			EndpointConfigHash string                         `json:"endpointConfigHash"`
			Access             semanticmodel.ConnectionAccess `json:"access,omitempty"`
		}{binding.BindingID, binding.LogicalConnection, binding.ConnectorKind, binding.Revision, binding.ProviderVersion, binding.EndpointConfigHash, binding.Access}
	}
	encoded, err := json.Marshal(preimage)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + fmt.Sprintf("%x", sum), nil
}

func NewCandidateRuntimeService(config CandidateRuntimeServiceConfig) (*CandidateRuntimeService, error) {
	if config.RuntimeVersion != strings.TrimSpace(config.RuntimeVersion) || config.Connections == nil || config.Runtime == nil || config.RuntimeVersion == "" {
		return nil, fmt.Errorf("%w: connection leaser, runtime host, and runtime version are required", ErrCandidateInvalid)
	}
	return &CandidateRuntimeService{connections: config.Connections, runtime: config.Runtime, runtimeVersion: config.RuntimeVersion}, nil
}

func (service *CandidateRuntimeService) Prepare(ctx context.Context, request CandidateRuntimeRequest) (CandidateRuntimeReceipt, error) {
	if service == nil {
		return CandidateRuntimeReceipt{}, ErrCandidateUnavailable
	}
	rawAuthorization := request.AuthorizationFingerprint
	if rawAuthorization != strings.TrimSpace(rawAuthorization) {
		return CandidateRuntimeReceipt{}, ErrCandidateInvalid
	}
	candidate := request.Candidate
	generation := request.Generation
	if candidate.Status != CandidatePreparing || candidate.ID == "" || candidate.OwnerID == "" || candidate.TargetID == "" || candidate.Scope.Validate() != nil || candidate.Scope.Environment == "" || candidate.ExpiresAt.IsZero() || request.AuthorizationFingerprint == "" {
		return CandidateRuntimeReceipt{}, ErrCandidateInvalid
	}
	rawArtifact, rawDataRevision := generation.ArtifactDigest, generation.DataRevision
	if rawArtifact != strings.TrimSpace(rawArtifact) || rawDataRevision != strings.TrimSpace(rawDataRevision) {
		return CandidateRuntimeReceipt{}, ErrCandidateInvalid
	}
	if generation.Identity.Validate() != nil || generation.ArtifactDigest == "" || generation.DataRevision == "" || platformdigest.ValidateSHA256Identity(generation.ArtifactDigest) != nil {
		return CandidateRuntimeReceipt{}, ErrCandidateInvalid
	}
	var err error
	generation.ManagedDataConnections, err = normalizeCandidateManagedConnections(generation.ManagedDataConnections)
	if err != nil {
		return CandidateRuntimeReceipt{}, ErrCandidateInvalid
	}
	generation.AuthoredConnections, err = normalizeCandidateAuthoredConnections(generation.AuthoredConnections)
	if err != nil {
		return CandidateRuntimeReceipt{}, ErrCandidateInvalid
	}
	generation.Restrictions, err = normalizeCandidateRestrictions(generation.Restrictions)
	if err != nil {
		return CandidateRuntimeReceipt{}, ErrCandidateInvalid
	}
	switch generation.DataMode {
	case CandidateDataReuseBase:
		if len(generation.AuthoredConnections) != 0 {
			return CandidateRuntimeReceipt{}, ErrCandidateInvalid
		}
	case CandidateDataRefreshSources:
		if len(generation.Connections) == 0 && len(generation.ManagedDataConnections) == 0 && len(generation.AuthoredConnections) == 0 {
			return CandidateRuntimeReceipt{}, ErrCandidateInvalid
		}
	default:
		return CandidateRuntimeReceipt{}, ErrCandidateInvalid
	}
	if generation.Identity.ProjectID != candidate.Scope.ProjectID || generation.Identity.Environment != candidate.Scope.Environment {
		return CandidateRuntimeReceipt{}, ErrCandidateInvalid
	}
	if generation.GateEvidence != nil {
		canonical, gateErr := generation.GateEvidence.Canonical()
		if gateErr != nil || canonical.CandidateID == "" || canonical.CandidateID != candidate.ID || canonical.RuntimeVersion != service.runtimeVersion || generation.BindingFingerprint == "" || canonical.BindingGeneration != generation.BindingFingerprint {
			return CandidateRuntimeReceipt{}, ErrCandidateInvalid
		}
		generation.GateEvidence = &canonical
	}
	projectID := generation.Identity.ProjectID
	leases, err := service.connections.Acquire(ctx, CandidateConnectionRequest{
		CandidateID: candidate.ID, Actor: candidate.OwnerID, TargetID: candidate.TargetID,
		Identity:            generation.Identity,
		Requirements:        append([]CandidateConnectionRequirement(nil), generation.Connections...),
		AuthoredConnections: append([]CandidateAuthoredConnection(nil), generation.AuthoredConnections...),
	})
	if err != nil || leases == nil {
		return CandidateRuntimeReceipt{}, fmt.Errorf("%w: target connections unavailable", ErrCandidateUnavailable)
	}
	bindings, err := candidateBindingVersions(leases.Evidence())
	if err != nil {
		_ = leases.Close()
		return CandidateRuntimeReceipt{}, err
	}
	bindingFingerprint, err := BindingFingerprint(leases.Evidence())
	if err != nil {
		_ = leases.Close()
		return CandidateRuntimeReceipt{}, err
	}
	if generation.BindingFingerprint != "" && generation.BindingFingerprint != bindingFingerprint {
		_ = leases.Close()
		return CandidateRuntimeReceipt{}, fmt.Errorf("%w: acquired connection binding evidence changed", ErrCandidateInvalid)
	}
	preparation := runtimehost.CandidatePreparation{
		Registration: runtimehost.CandidateRegistration{
			CandidateID: candidate.ID, OwnerID: candidate.OwnerID, ProjectID: projectID, ExpiresAt: candidate.ExpiresAt,
			Compatibility: runtimehost.CandidateCompatibility{
				ArtifactDigest: generation.ArtifactDigest, DataRevision: generation.DataRevision,
				DataMode: runtimehost.CandidateDataMode(generation.DataMode), RuntimeVersion: service.runtimeVersion,
				AuthorizationFingerprint: request.AuthorizationFingerprint, Bindings: bindings,
				BindingFingerprint:     bindingFingerprint,
				GateEvidenceDigest:     gateEvidenceDigest(generation.GateEvidence),
				ManagedDataConnections: append([]string(nil), generation.ManagedDataConnections...),
				AuthoredConnections:    candidateAuthoredConnections(generation.AuthoredConnections),
				Restrictions:           candidateRestrictions(generation.Restrictions),
			},
		},
		Identity: generation.Identity,
		Lifetime: leases,
	}
	if err := service.runtime.PrepareAndRegisterCandidateSet(ctx, []runtimehost.CandidatePreparation{preparation}); err != nil {
		return CandidateRuntimeReceipt{}, fmt.Errorf("%w: candidate runtime preparation failed: %v", ErrCandidateUnavailable, err)
	}
	return CandidateRuntimeReceipt{RuntimeVersion: service.runtimeVersion, Bindings: candidateConnectionEvidence(bindings), BindingFingerprint: bindingFingerprint, GateEvidence: generation.GateEvidence}, nil
}

func gateEvidenceDigest(value *release.GateEvidence) string {
	if value == nil {
		return ""
	}
	return value.Digest
}

func normalizeCandidateManagedConnections(values []string) ([]string, error) {
	values = append([]string(nil), values...)
	for i := range values {
		if values[i] != strings.TrimSpace(values[i]) {
			return nil, ErrCandidateInvalid
		}
	}
	sort.Strings(values)
	for i, value := range values {
		if value == "" || i > 0 && values[i-1] == value {
			return nil, ErrCandidateInvalid
		}
	}
	return values, nil
}

func normalizeCandidateAuthoredConnections(values []CandidateAuthoredConnection) ([]CandidateAuthoredConnection, error) {
	values = append([]CandidateAuthoredConnection(nil), values...)
	for i := range values {
		rawConnection := values[i].ConnectionID.String()
		if rawConnection != strings.TrimSpace(rawConnection) || values[i].ConnectorKind != strings.TrimSpace(values[i].ConnectorKind) {
			return nil, ErrCandidateInvalid
		}
		if err := values[i].ConnectionID.Validate(); err != nil {
			return nil, ErrCandidateInvalid
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ConnectionID < values[j].ConnectionID })
	for i, value := range values {
		if !value.ConnectionID.Valid() || value.ConnectorKind == "" || i > 0 && values[i-1].ConnectionID == value.ConnectionID {
			return nil, ErrCandidateInvalid
		}
	}
	return values, nil
}

// normalizeCandidateRestrictions validates the immutable authorization
// evidence carried by a candidate generation. Restrictions are project-wide
// records, so the object identity/kind and optional explicit subject must be
// checked before handing them to the runtime host; no owner-based filtering
// is performed here.
func normalizeCandidateRestrictions(values []CandidateRestriction) ([]CandidateRestriction, error) {
	values = append([]CandidateRestriction(nil), values...)
	for i := range values {
		item := &values[i]
		if item.ID != strings.TrimSpace(item.ID) || item.PolicyType != strings.TrimSpace(item.PolicyType) || item.ExpressionJSON != strings.TrimSpace(item.ExpressionJSON) {
			return nil, ErrCandidateInvalid
		}
		if item.ID == "" || item.PolicyType == "" || item.ExpressionJSON == "" || item.ObjectID.Validate() != nil || !item.ObjectKind.Valid() {
			return nil, ErrCandidateInvalid
		}
		if item.PolicyType != "row_filter" && item.PolicyType != "column_mask" {
			return nil, ErrCandidateInvalid
		}
		if item.Subject != nil && item.Subject.Validate() != nil {
			return nil, ErrCandidateInvalid
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	for i := 1; i < len(values); i++ {
		if values[i-1].ID == values[i].ID {
			return nil, ErrCandidateInvalid
		}
	}
	return values, nil
}

func candidateAuthoredConnections(values []CandidateAuthoredConnection) []runtimehost.CandidateAuthoredConnection {
	result := make([]runtimehost.CandidateAuthoredConnection, len(values))
	for i, value := range values {
		result[i] = runtimehost.CandidateAuthoredConnection{LogicalConnection: value.ConnectionID.String(), ConnectorKind: value.ConnectorKind, Access: value.Access}
	}
	return result
}

func candidateConnectionEvidence(values []runtimehost.CandidateBindingVersion) []CandidateConnectionEvidence {
	result := make([]CandidateConnectionEvidence, len(values))
	for i, value := range values {
		connectionID, err := projectgraph.NewResourceID(value.LogicalConnection)
		if err != nil {
			continue
		}
		result[i] = CandidateConnectionEvidence{BindingID: value.BindingID, ConnectionID: connectionID, ConnectorKind: value.ConnectorKind, Revision: value.Revision, ProviderVersion: value.ProviderVersion, EndpointConfigHash: value.EndpointConfigHash, Access: value.Access}
	}
	return result
}

func candidateRestrictions(values []CandidateRestriction) []runtimehost.CandidateRestriction {
	result := make([]runtimehost.CandidateRestriction, len(values))
	for i, value := range values {
		result[i] = runtimehost.CandidateRestriction{ID: value.ID, ObjectID: value.ObjectID, ObjectKind: value.ObjectKind, Subject: value.Subject, PolicyType: value.PolicyType, ExpressionJSON: value.ExpressionJSON}
	}
	return result
}

func candidateBindingVersions(evidence []CandidateConnectionEvidence) ([]runtimehost.CandidateBindingVersion, error) {
	evidence = append([]CandidateConnectionEvidence(nil), evidence...)
	for i := range evidence {
		item := &evidence[i]
		rawConnection := item.ConnectionID.String()
		if item.BindingID != strings.TrimSpace(item.BindingID) || item.ConnectorKind != strings.TrimSpace(item.ConnectorKind) || item.ProviderVersion != strings.TrimSpace(item.ProviderVersion) || item.EndpointConfigHash != strings.TrimSpace(item.EndpointConfigHash) || rawConnection != strings.TrimSpace(rawConnection) {
			return nil, ErrCandidateInvalid
		}
		if item.BindingID == "" || item.ConnectorKind == "" || item.ProviderVersion == "" || item.Revision < 1 || platformdigest.ValidateSHA256Identity(item.EndpointConfigHash) != nil || item.ConnectionID.Validate() != nil {
			return nil, ErrCandidateInvalid
		}
	}
	sort.Slice(evidence, func(i, j int) bool { return evidence[i].BindingID < evidence[j].BindingID })
	result := make([]runtimehost.CandidateBindingVersion, 0, len(evidence))
	for i, item := range evidence {
		if i > 0 && evidence[i-1].BindingID == item.BindingID {
			return nil, ErrCandidateInvalid
		}
		result = append(result, runtimehost.CandidateBindingVersion{BindingID: item.BindingID, LogicalConnection: item.ConnectionID.String(), ConnectorKind: item.ConnectorKind, Revision: item.Revision, ProviderVersion: item.ProviderVersion, EndpointConfigHash: item.EndpointConfigHash, Access: item.Access})
	}
	return result, nil
}
