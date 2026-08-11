package deployment

import (
	"context"
	"fmt"
	"sort"
	"strings"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/runtimehost"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

type CandidateConnectionRequirement struct {
	LogicalConnectionID string
	ConnectorKind       string
}

type CandidateAuthoredConnection struct {
	LogicalConnectionID string
	ConnectorKind       string
}

type CandidateRestriction struct {
	ID             string
	WorkspaceID    string
	ObjectID       string
	PolicyType     string
	ExpressionJSON string
}

type CandidateDataMode string

const (
	CandidateDataReuseSnapshot  CandidateDataMode = "reuse_snapshot"
	CandidateDataRefreshSources CandidateDataMode = "refresh_sources"
)

type CandidateConnectionEvidence struct {
	BindingID          string
	LogicalConnection  string
	ConnectorKind      string
	Revision           int64
	ProviderVersion    string
	EndpointConfigHash string
}

type CandidateConnectionRequest struct {
	CandidateID         string
	Actor               string
	TargetID            string
	WorkspaceID         string
	Environment         string
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
	PrepareAndRegisterCandidateSet(
		context.Context,
		[]runtimehost.CandidatePreparation,
	) error
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

type CandidateWorkspaceRuntime struct {
	WorkspaceID            string
	ServingStateID         string
	ArtifactDigest         string
	DataRevision           string
	DataMode               CandidateDataMode
	Connections            []CandidateConnectionRequirement
	AuthoredConnections    []CandidateAuthoredConnection
	ManagedDataConnections []string
	Restrictions           []CandidateRestriction
}

type CandidateRuntimeRequest struct {
	Candidate                Candidate
	AuthorizationFingerprint string
	Workspaces               []CandidateWorkspaceRuntime
}

type CandidateWorkspaceRuntimeReceipt struct {
	WorkspaceID string
	Bindings    []CandidateConnectionEvidence
}

type CandidateRuntimeReceipt struct {
	RuntimeVersion string
	Workspaces     []CandidateWorkspaceRuntimeReceipt
}

func NewCandidateRuntimeService(
	config CandidateRuntimeServiceConfig,
) (*CandidateRuntimeService, error) {
	config.RuntimeVersion = strings.TrimSpace(config.RuntimeVersion)
	if config.Connections == nil || config.Runtime == nil || config.RuntimeVersion == "" {
		return nil, fmt.Errorf(
			"%w: connection leaser, runtime host, and runtime version are required",
			ErrCandidateInvalid,
		)
	}
	return &CandidateRuntimeService{
		connections: config.Connections, runtime: config.Runtime,
		runtimeVersion: config.RuntimeVersion,
	}, nil
}

func (service *CandidateRuntimeService) Prepare(
	ctx context.Context,
	request CandidateRuntimeRequest,
) (CandidateRuntimeReceipt, error) {
	if service == nil {
		return CandidateRuntimeReceipt{}, ErrCandidateUnavailable
	}
	request.AuthorizationFingerprint = strings.TrimSpace(
		request.AuthorizationFingerprint,
	)
	candidate := request.Candidate
	if candidate.Status != CandidatePreparing ||
		candidate.ID == "" || candidate.OwnerID == "" || candidate.TargetID == "" ||
		candidate.Environment == "" || candidate.ExpiresAt.IsZero() ||
		request.AuthorizationFingerprint == "" || len(request.Workspaces) == 0 {
		return CandidateRuntimeReceipt{}, ErrCandidateInvalid
	}
	workspaces := append([]CandidateWorkspaceRuntime(nil), request.Workspaces...)
	for index := range workspaces {
		workspaces[index].WorkspaceID = strings.TrimSpace(workspaces[index].WorkspaceID)
		workspaces[index].ServingStateID = strings.TrimSpace(workspaces[index].ServingStateID)
		workspaces[index].ArtifactDigest = strings.TrimSpace(workspaces[index].ArtifactDigest)
		workspaces[index].DataRevision = strings.TrimSpace(workspaces[index].DataRevision)
		var err error
		workspaces[index].ManagedDataConnections, err = normalizeCandidateManagedConnections(
			workspaces[index].ManagedDataConnections,
		)
		if err != nil {
			return CandidateRuntimeReceipt{}, ErrCandidateInvalid
		}
		workspaces[index].AuthoredConnections, err = normalizeCandidateAuthoredConnections(
			workspaces[index].AuthoredConnections,
		)
		if err != nil {
			return CandidateRuntimeReceipt{}, ErrCandidateInvalid
		}
		if workspaces[index].WorkspaceID == "" || workspaces[index].ServingStateID == "" ||
			workspaces[index].ArtifactDigest == "" || workspaces[index].DataRevision == "" {
			return CandidateRuntimeReceipt{}, ErrCandidateInvalid
		}
		switch workspaces[index].DataMode {
		case CandidateDataReuseSnapshot:
			if len(workspaces[index].AuthoredConnections) != 0 {
				return CandidateRuntimeReceipt{}, ErrCandidateInvalid
			}
		case CandidateDataRefreshSources:
			if len(workspaces[index].Connections) == 0 &&
				len(workspaces[index].ManagedDataConnections) == 0 &&
				len(workspaces[index].AuthoredConnections) == 0 {
				return CandidateRuntimeReceipt{}, ErrCandidateInvalid
			}
		default:
			return CandidateRuntimeReceipt{}, ErrCandidateInvalid
		}
	}
	sort.Slice(workspaces, func(i, j int) bool {
		return workspaces[i].WorkspaceID < workspaces[j].WorkspaceID
	})
	for index := 1; index < len(workspaces); index++ {
		if workspaces[index-1].WorkspaceID == workspaces[index].WorkspaceID {
			return CandidateRuntimeReceipt{}, ErrCandidateInvalid
		}
	}
	inputs := make([]runtimehost.CandidatePreparation, 0, len(workspaces))
	receipt := CandidateRuntimeReceipt{
		RuntimeVersion: service.runtimeVersion,
		Workspaces: make(
			[]CandidateWorkspaceRuntimeReceipt,
			0,
			len(workspaces),
		),
	}
	owned := make([]CandidateConnectionLeases, 0, len(workspaces))
	releaseOwned := func() {
		for index := len(owned) - 1; index >= 0; index-- {
			_ = owned[index].Close()
		}
		owned = nil
	}
	for _, workspace := range workspaces {
		leases, err := service.connections.Acquire(ctx, CandidateConnectionRequest{
			CandidateID: candidate.ID,
			Actor:       candidate.OwnerID, TargetID: candidate.TargetID,
			WorkspaceID: workspace.WorkspaceID, Environment: candidate.Environment,
			Requirements: append(
				[]CandidateConnectionRequirement(nil),
				workspace.Connections...,
			),
			AuthoredConnections: append(
				[]CandidateAuthoredConnection(nil),
				workspace.AuthoredConnections...,
			),
		})
		if err != nil || leases == nil {
			releaseOwned()
			return CandidateRuntimeReceipt{}, fmt.Errorf(
				"%w: target connections unavailable for workspace %q",
				ErrCandidateUnavailable,
				workspace.WorkspaceID,
			)
		}
		owned = append(owned, leases)
		bindings, err := candidateBindingVersions(leases.Evidence())
		if err != nil {
			releaseOwned()
			return CandidateRuntimeReceipt{}, err
		}
		receipt.Workspaces = append(
			receipt.Workspaces,
			CandidateWorkspaceRuntimeReceipt{
				WorkspaceID: workspace.WorkspaceID,
				Bindings:    candidateConnectionEvidence(bindings),
			},
		)
		inputs = append(inputs, runtimehost.CandidatePreparation{
			Registration: runtimehost.CandidateRegistration{
				CandidateID: candidate.ID, OwnerID: candidate.OwnerID,
				WorkspaceID: servingstate.WorkspaceID(workspace.WorkspaceID),
				ExpiresAt:   candidate.ExpiresAt,
				Compatibility: runtimehost.CandidateCompatibility{
					ArtifactDigest:           workspace.ArtifactDigest,
					DataRevision:             workspace.DataRevision,
					DataMode:                 runtimehost.CandidateDataMode(workspace.DataMode),
					RuntimeVersion:           service.runtimeVersion,
					AuthorizationFingerprint: request.AuthorizationFingerprint,
					Bindings:                 bindings,
					ManagedDataConnections: append(
						[]string(nil),
						workspace.ManagedDataConnections...,
					),
					AuthoredConnections: candidateAuthoredConnections(
						workspace.AuthoredConnections,
					),
					Restrictions: candidateRestrictions(workspace.Restrictions),
				},
			},
			ServingStateID: workspace.ServingStateID,
			Lifetime:       leases,
		})
	}
	if err := service.runtime.PrepareAndRegisterCandidateSet(ctx, inputs); err != nil {
		// Runtime Host accepts ownership of every lifetime supplied to the set,
		// including failure paths.
		owned = nil
		return CandidateRuntimeReceipt{}, fmt.Errorf(
			"%w: candidate runtime preparation failed: %v",
			ErrCandidateUnavailable,
			err,
		)
	}
	owned = nil
	return receipt, nil
}

func normalizeCandidateManagedConnections(values []string) ([]string, error) {
	values = append([]string(nil), values...)
	for index := range values {
		values[index] = strings.TrimSpace(values[index])
	}
	sort.Strings(values)
	for index, value := range values {
		if value == "" || index > 0 && values[index-1] == value {
			return nil, ErrCandidateInvalid
		}
	}
	return values, nil
}

func normalizeCandidateAuthoredConnections(
	values []CandidateAuthoredConnection,
) ([]CandidateAuthoredConnection, error) {
	values = append([]CandidateAuthoredConnection(nil), values...)
	for index := range values {
		values[index].LogicalConnectionID = strings.TrimSpace(values[index].LogicalConnectionID)
		values[index].ConnectorKind = strings.TrimSpace(values[index].ConnectorKind)
	}
	sort.Slice(values, func(i, j int) bool {
		return values[i].LogicalConnectionID < values[j].LogicalConnectionID
	})
	for index, value := range values {
		if value.LogicalConnectionID == "" || value.ConnectorKind == "" ||
			index > 0 && values[index-1].LogicalConnectionID == value.LogicalConnectionID {
			return nil, ErrCandidateInvalid
		}
	}
	return values, nil
}

func candidateAuthoredConnections(
	values []CandidateAuthoredConnection,
) []runtimehost.CandidateAuthoredConnection {
	result := make([]runtimehost.CandidateAuthoredConnection, len(values))
	for index, value := range values {
		result[index] = runtimehost.CandidateAuthoredConnection{
			LogicalConnection: value.LogicalConnectionID,
			ConnectorKind:     value.ConnectorKind,
		}
	}
	return result
}

func candidateConnectionEvidence(
	values []runtimehost.CandidateBindingVersion,
) []CandidateConnectionEvidence {
	result := make([]CandidateConnectionEvidence, len(values))
	for index, value := range values {
		result[index] = CandidateConnectionEvidence{
			BindingID: value.BindingID, LogicalConnection: value.LogicalConnection,
			ConnectorKind: value.ConnectorKind, Revision: value.Revision,
			ProviderVersion: value.ProviderVersion, EndpointConfigHash: value.EndpointConfigHash,
		}
	}
	return result
}

func candidateRestrictions(values []CandidateRestriction) []runtimehost.CandidateRestriction {
	result := make([]runtimehost.CandidateRestriction, len(values))
	for index, value := range values {
		result[index] = runtimehost.CandidateRestriction{
			ID: value.ID, WorkspaceID: value.WorkspaceID, ObjectID: value.ObjectID,
			PolicyType: value.PolicyType, ExpressionJSON: value.ExpressionJSON,
		}
	}
	return result
}

func candidateBindingVersions(
	evidence []CandidateConnectionEvidence,
) ([]runtimehost.CandidateBindingVersion, error) {
	evidence = append([]CandidateConnectionEvidence(nil), evidence...)
	sort.Slice(evidence, func(i, j int) bool {
		return evidence[i].BindingID < evidence[j].BindingID
	})
	result := make([]runtimehost.CandidateBindingVersion, 0, len(evidence))
	for index, item := range evidence {
		item.BindingID = strings.TrimSpace(item.BindingID)
		item.LogicalConnection = strings.TrimSpace(item.LogicalConnection)
		item.ConnectorKind = strings.TrimSpace(item.ConnectorKind)
		item.ProviderVersion = strings.TrimSpace(item.ProviderVersion)
		item.EndpointConfigHash = strings.TrimSpace(item.EndpointConfigHash)
		if item.BindingID == "" || item.LogicalConnection == "" || item.ConnectorKind == "" ||
			item.Revision < 1 || item.ProviderVersion == "" || item.EndpointConfigHash == "" ||
			platformdigest.ValidateSHA256Identity(item.EndpointConfigHash) != nil ||
			index > 0 && evidence[index-1].BindingID == item.BindingID {
			return nil, ErrCandidateInvalid
		}
		result = append(result, runtimehost.CandidateBindingVersion{
			BindingID: item.BindingID, LogicalConnection: item.LogicalConnection,
			ConnectorKind: item.ConnectorKind, Revision: item.Revision,
			ProviderVersion: item.ProviderVersion, EndpointConfigHash: item.EndpointConfigHash,
		})
	}
	return result, nil
}
