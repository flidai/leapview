// Package publication owns anonymous dashboard publication lifecycle state.
package publication

import (
	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
)

var (
	ErrNotFound               = apigenfailure.New("not_found", "dashboard publication not found")
	ErrConflict               = apigenfailure.New("conflict", "dashboard publication conflict")
	ErrStreamStateUnavailable = apigenfailure.New("stream_state_unavailable", "durable publication stream state is unavailable")
)

type Status string

const (
	StatusActive       Status = "active"
	StatusSuspended    Status = "suspended"
	StatusUnconfigured Status = "unconfigured"
)

type Publication struct {
	ID                  string
	ProjectID           string
	WorkspaceID         string
	Name                string
	PublicID            string
	Dashboard           string
	DefaultPage         string
	ConfigurationDigest string
	AllowedOrigins      []string
	DependencyAssetIDs  []string
	Configured          bool
	ServingStateID      string
	SuspendedAt         string
	SuspendedBy         string
	ConfiguredAt        string
	DisabledAt          string
	RotatedAt           string
	CreatedAt           string
	UpdatedAt           string
}

// Definition is the immutable, compiled authorization boundary for one
// anonymously published dashboard. DependencyAssetIDs is the complete
// transitive graph reachable from the dashboard in its serving state.
type Definition struct {
	Name                string   `json:"name"`
	Dashboard           string   `json:"dashboard"`
	DefaultPage         string   `json:"defaultPage"`
	AllowedOrigins      []string `json:"allowedOrigins,omitempty"`
	DependencyAssetIDs  []string `json:"dependencyAssetIds"`
	ConfigurationDigest string   `json:"configurationDigest"`
}

func (p Publication) Status() Status {
	if !p.Configured || p.ServingStateID == "" {
		return StatusUnconfigured
	}
	if p.SuspendedAt != "" {
		return StatusSuspended
	}
	return StatusActive
}

type ReconcileInput struct {
	ProjectID      string
	WorkspaceID    string
	ServingStateID string
	ActorID        string
	Publications   map[string]Definition
}

type Event struct {
	Type           string
	ActorID        string
	ServingStateID string
	CreatedAt      string
}
