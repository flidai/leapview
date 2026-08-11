package module

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/deployment"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
)

func requireCandidateAuditSink(
	audit func(context.Context, deployment.CandidateEvent) error,
) error {
	for _, contract := range deploymentgen.GetAPIGenOperationContracts() {
		if contract.Command == nil || !contract.Command.Audit.Required {
			continue
		}
		switch contract.Command.Audit.SuccessAction {
		case deployment.CandidateAuditStarted,
			deployment.CandidateAuditArtifactReplaced,
			deployment.CandidateAuditReady,
			deployment.CandidateAuditFailed,
			deployment.CandidateAuditRetried,
			deployment.CandidateAuditCancelled,
			deployment.CandidateAuditExpired:
			if audit == nil {
				return fmt.Errorf(
					"%w: generated command %s requires action %s",
					deployment.ErrCandidateAuditUnavailable,
					contract.OperationID,
					contract.Command.Audit.SuccessAction,
				)
			}
		}
	}
	return nil
}
