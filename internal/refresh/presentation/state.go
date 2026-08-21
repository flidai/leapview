package presentation

import (
	"time"

	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

// AssetRefreshState is the refresh capability's presentation read model for a
// project asset. Consumers may adapt it into their own page contracts.
type AssetRefreshState struct {
	CSRFToken        string
	Unavailable      bool
	RunCommand       uicommand.Binding
	CancelCommand    uicommand.Binding
	Runs             []AssetRefreshRun
	Latest           AssetRefreshRun
	LatestSuccessful AssetRefreshRun
	DataVersion      AssetDataVersion
	NextRun          time.Time
}

type AssetDataVersion struct {
	SnapshotID     int64
	ServingStateID string
	RefreshedAt    time.Time
	Source         string
}

type AssetRefreshRun struct {
	ID                   string
	Environment          string
	ModelID              string
	ServingStateID       string
	PrincipalID          string
	PrincipalDisplayName string
	TriggerType          string
	ParentRunID          string
	TargetGeneration     int64
	Status               string
	CreatedAt            string
	UpdatedAt            string
	StartedAt            string
	FinishedAt           string
	Error                string
}
