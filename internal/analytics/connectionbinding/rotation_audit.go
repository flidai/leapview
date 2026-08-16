package connectionbinding

import (
	"context"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"strings"
	"time"
)

type RefreshOperation string

const (
	RefreshScheduled RefreshOperation = "credential.refresh.scheduled"
	RefreshRequested RefreshOperation = "credential.refresh.requested"
	RefreshTest      RefreshOperation = "credential.test.requested"
	RefreshRuntime   RefreshOperation = "credential.runtime.acquire"
)

type RotationOutcome string

const (
	RotationActivated RotationOutcome = "activated"
	RotationUnchanged RotationOutcome = "unchanged"
	RotationDegraded  RotationOutcome = "degraded"
)

type RefreshRequest struct {
	Actor     string
	Operation RefreshOperation
}

func (request RefreshRequest) valid() bool {
	request.Actor = strings.TrimSpace(request.Actor)
	if request.Actor == "" || len(request.Actor) > 256 {
		return false
	}
	return request.Operation == RefreshScheduled ||
		request.Operation == RefreshRequested ||
		request.Operation == RefreshTest ||
		request.Operation == RefreshRuntime
}

type RotationAuditEvent struct {
	BindingID       BindingID                    `json:"bindingId"`
	TargetID        string                       `json:"targetId"`
	Identity        projectgraph.ServingIdentity `json:"identity"`
	ProjectID       projectgraph.ResourceID      `json:"projectId"`
	ProviderVersion string                       `json:"providerVersion,omitempty"`
	Actor           string                       `json:"actor"`
	Operation       RefreshOperation             `json:"operation"`
	Timestamp       time.Time                    `json:"timestamp"`
	Outcome         RotationOutcome              `json:"outcome"`
	Reason          string                       `json:"reason,omitempty"`
}

type RotationAuditRecorder interface {
	RecordCredentialRotation(context.Context, RotationAuditEvent) error
}
