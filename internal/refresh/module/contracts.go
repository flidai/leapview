package module

import (
	refreshpresentation "github.com/flidai/leapview/internal/refresh/presentation"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	"github.com/flidai/leapview/internal/workload"
)

type Clock = refreshschedule.Clock
type RunRecord = refreshrun.RunRecord
type Service = refreshrun.Service
type QueuePipelineInput = refreshrun.QueuePipelineInput
type QueueAssetResult = refreshrun.QueueAssetResult
type WorkloadStats = workload.Stats
type AssetRefreshState = refreshpresentation.AssetRefreshState
type AssetDataVersion = refreshpresentation.AssetDataVersion
type AssetRefreshRun = refreshpresentation.AssetRefreshRun

const RunStatusSucceeded = refreshrun.RunStatusSucceeded

func NewRealClock() Clock {
	return refreshschedule.RealClock{}
}
