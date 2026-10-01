package module

import (
	"context"
	"fmt"

	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	"github.com/flidai/leapview/pkg/jobs"
)

func (m *Module) triggerScheduledRefresh(ctx context.Context, occurrence refreshschedule.Occurrence) error {
	principalID := "scheduler"
	var authority jobs.AuthorityEnvelope
	if m.scheduledAuthority != nil {
		grantID := m.executionGrantID
		if m.resolveExecutionGrantID != nil {
			var err error
			grantID, err = m.resolveExecutionGrantID(ctx, occurrence)
			if err != nil {
				return fmt.Errorf("resolve scheduled execution grant: %w", err)
			}
		}
		var err error
		authority, err = m.scheduledAuthority.Capture(ctx, grantID, occurrence.Identity, occurrence.PipelineID)
		if err != nil {
			return err
		}
		principalID = authority.ExecutionPrincipalID
	}
	result, err := m.service.QueuePipelineRefresh(ctx, refreshrun.QueuePipelineInput{
		Identity: occurrence.Identity, PrincipalID: principalID, EstimatedMemoryBytes: 1,
		PipelineID: occurrence.PipelineID, TriggerType: refreshrun.TriggerSchedule,
		ArtifactDigest: occurrence.ArtifactDigest, Occurrence: &occurrence, Authority: authority,
	})
	if err == nil && result.Run.Status == refreshrun.RunStatusSkipped {
		return refreshschedule.ErrOccurrenceSkipped
	}
	return err
}
