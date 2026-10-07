package cli

import (
	"context"
	"errors"
	"fmt"

	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	projectdevloop "github.com/flidai/leapview/internal/project/devloop"
)

func (transport *candidateSynchronizationTransport) localDeliveryTargetRevision(ctx context.Context, retained projectdevloop.RetainedSource) (int64, error) {
	local := transport.localDevelopment.state
	if _, err := localSessionOrigin(local.Network.URL); err != nil {
		return 0, fmt.Errorf("local planning requires a checkout-owned loopback runtime: %w", err)
	}
	if local.Authority.Environment != "dev" || retained.Environment != local.Authority.Environment ||
		local.Authority.InstanceID == "" || retained.TargetID != local.Authority.InstanceID ||
		local.Authority.ProjectUID == "" || retained.ProjectID.String() != local.Authority.ProjectUID {
		return 0, errors.New("retained source does not match the local development target")
	}
	snapshot, err := transport.client.GetDeliveryOperatorSnapshot(ctx, deploymentgen.GenGetDeliveryOperatorSnapshotClientRequest{Project: retained.ProjectID.String()})
	if err != nil {
		return 0, fmt.Errorf("read local planning target revision: %w", err)
	}
	if snapshot.Body.ProjectId != local.Authority.ProjectUID || snapshot.Body.TargetId != local.Authority.InstanceID ||
		snapshot.Body.Environment != local.Authority.Environment || snapshot.Body.TargetRevision < 1 {
		return 0, errors.New("local planning operator snapshot does not match the target identity and revision")
	}
	return snapshot.Body.TargetRevision, nil
}
