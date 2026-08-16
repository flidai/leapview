package connectionbinding

import (
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"time"
)

type BindingHealthStatus struct {
	BindingID         BindingID               `json:"bindingId"`
	TargetID          TargetID                `json:"targetId"`
	ConnectionID      projectgraph.ResourceID `json:"connectionId"`
	LogicalConnection projectgraph.ResourceID `json:"-"`
	ConnectorKind     string                  `json:"connectorKind"`
	Scope             BindingScope            `json:"scope"`
	BindingRevision   int64                   `json:"bindingRevision"`
	ValidatedVersion  string                  `json:"validatedVersion,omitempty"`
	Health            BindingHealth           `json:"health"`
	DiagnosticCode    string                  `json:"reason,omitempty"`
	LastAttemptAt     time.Time               `json:"lastAttemptAt,omitempty"`
	LastValidatedAt   time.Time               `json:"lastValidatedAt,omitempty"`
	StaleAgeSeconds   int64                   `json:"staleAgeSeconds,omitempty"`
	HasActivePool     bool                    `json:"hasActivePool"`
}
