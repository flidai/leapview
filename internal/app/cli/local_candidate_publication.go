package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	projectdevloop "github.com/flidai/leapview/internal/project/devloop"
	"github.com/google/uuid"
)

const localActivationTimeout = 2 * time.Minute

type localActivationResult struct {
	PublicationID  string
	GenerationID   string
	TargetRevision int64
}

// publishAndActivateLocalCandidate uses the same durable publication and
// worker-driven activation as ordinary delivery. It is restricted to the
// checkout-owned loopback target and never selects a remote target from the
// ambient CLI profile. Local plans do not require reviewer approval.
func publishAndActivateLocalCandidate(ctx context.Context, client *deploymentgen.GenClient, local localDevelopmentSession, candidate projectdevloop.Candidate) (localActivationResult, error) {
	if client == nil {
		return localActivationResult{}, errors.New("local publication client is unavailable")
	}
	state := local.state
	if _, err := localSessionOrigin(state.Network.URL); err != nil {
		return localActivationResult{}, fmt.Errorf("local publication requires a checkout-owned loopback runtime: %w", err)
	}
	if state.Authority.Environment != "dev" || state.Authority.InstanceID == "" || state.Authority.ProjectUID == "" ||
		candidate.Environment != state.Authority.Environment || candidate.TargetID != state.Authority.InstanceID ||
		candidate.ProjectID.String() != state.Authority.ProjectUID || candidate.PlanID == "" || candidate.PlanDigest == "" {
		return localActivationResult{}, errors.New("candidate does not match the local development target and plan")
	}
	if parsed, err := uuid.Parse(candidate.ID); err != nil || parsed == uuid.Nil || parsed.String() != candidate.ID {
		return localActivationResult{}, errors.New("local candidate identity is invalid")
	}
	key := deploymentIdempotencyKey("local-dev-publish", candidate.ProjectID.String(), candidate.TargetID, candidate.ID, candidate.PlanID, candidate.PlanDigest)
	published, err := client.PublishDeliveryCandidate(ctx, deploymentgen.GenPublishDeliveryCandidateClientRequest{
		Project: candidate.ProjectID.String(), Candidate: candidate.ID,
		Headers: deploymentgen.GenPublishDeliveryCandidateClientHeaders{IdempotencyKey: key},
	})
	if err != nil {
		return localActivationResult{}, fmt.Errorf("publish valid candidate to the local instance: %w", err)
	}
	if err := validateLocalPublication(published.Body, candidate); err != nil {
		return localActivationResult{}, err
	}
	publicationID := published.Body.Id
	waitContext, cancel := context.WithTimeout(ctx, localActivationTimeout)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		read, readErr := client.GetDeliveryPublicationEvidence(waitContext, deploymentgen.GenGetDeliveryPublicationEvidenceClientRequest{
			Project: candidate.ProjectID.String(), Publication: publicationID,
		})
		if readErr != nil {
			var problem *apigenclient.ProblemError
			if read.StatusCode != http.StatusServiceUnavailable &&
				(!errors.As(readErr, &problem) || problem.Response.StatusCode != http.StatusServiceUnavailable) &&
				!strings.HasSuffix(strings.TrimSpace(readErr.Error()), ": "+http.StatusText(http.StatusServiceUnavailable)) {
				return localActivationResult{}, fmt.Errorf("read local publication: %w", readErr)
			}
			// The first publication can commit before the in-process runtime is
			// ready. Older servers authorize this read from the active snapshot
			// and return 503 during that short bootstrap interval.
		} else {
			publication := read.Body
			if err := validateLocalPublication(publication, candidate); err != nil {
				return localActivationResult{}, err
			}
			if publication.Id != publicationID || publication.GenerationId != published.Body.GenerationId {
				return localActivationResult{}, errors.New("local publication identity changed while waiting for activation")
			}
			switch publication.Status {
			case deploymentgen.DeliveryPublicationStatusCommitted:
				if publication.ResultTargetRevision <= 0 {
					return localActivationResult{}, errors.New("local activation has no committed target revision")
				}
				operator, operatorErr := client.GetDeliveryOperatorSnapshot(waitContext, deploymentgen.GenGetDeliveryOperatorSnapshotClientRequest{Project: candidate.ProjectID.String()})
				if operatorErr != nil {
					return localActivationResult{}, fmt.Errorf("verify active local generation: %w", operatorErr)
				}
				if operator.Body.ProjectId != candidate.ProjectID.String() || operator.Body.TargetId != candidate.TargetID || operator.Body.Environment != candidate.Environment {
					return localActivationResult{}, errors.New("local operator snapshot differs from the published target")
				}
				if operator.Body.TargetRevision >= publication.ResultTargetRevision {
					if operator.Body.ActiveGeneration == nil || *operator.Body.ActiveGeneration != publication.GenerationId || operator.Body.TargetRevision != publication.ResultTargetRevision {
						return localActivationResult{}, errors.New("local active generation differs from the exact published candidate")
					}
					return localActivationResult{PublicationID: publicationID, GenerationID: publication.GenerationId, TargetRevision: publication.ResultTargetRevision}, nil
				}
			case deploymentgen.DeliveryPublicationStatusRejected, deploymentgen.DeliveryPublicationStatusIndeterminate:
				reason := ""
				if publication.Reason != nil {
					reason = strings.TrimSpace(*publication.Reason)
				}
				if reason == "" {
					reason = string(publication.Status)
				}
				return localActivationResult{}, fmt.Errorf("local candidate activation did not commit: %s", reason)
			case deploymentgen.DeliveryPublicationStatusPending:
			default:
				return localActivationResult{}, fmt.Errorf("local publication has unexpected status %q", publication.Status)
			}
		}
		select {
		case <-waitContext.Done():
			return localActivationResult{}, fmt.Errorf("wait for local candidate activation: %w", waitContext.Err())
		case <-ticker.C:
		}
	}
}

func validateLocalPublication(value deploymentgen.DeliveryPublicationEvidenceResponse, candidate projectdevloop.Candidate) error {
	if value.Id == "" || value.GenerationId == "" || value.ProjectId != candidate.ProjectID.String() ||
		value.TargetId != candidate.TargetID || value.Environment != candidate.Environment ||
		value.CandidateId != candidate.ID || value.PlanId != candidate.PlanID || value.PlanDigest != candidate.PlanDigest {
		return errors.New("local publication response does not match the exact candidate and plan")
	}
	return nil
}
