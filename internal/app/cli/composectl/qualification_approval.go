package composectl

import (
	"context"
	"fmt"
	"net/http"

	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
)

type qualificationApprovalEvidence struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	ApprovedBy    string `json:"approvedBy"`
	DeploymentID  string `json:"deploymentId"`
	ProjectID     string `json:"projectId"`
	Environment   string `json:"environment"`
	RequestDigest string `json:"requestDigest"`
}

func approveQualificationPublication(
	ctx context.Context,
	client *http.Client,
	options qualificationAuthoringOptions,
	authorToken string,
	reviewerToken string,
	publication QualificationPublication,
	reviewerPrincipalID string,
	runSuffix string,
) (qualificationApprovalEvidence, error) {
	var evidence qualificationApprovalEvidence
	author := deploymentgen.NewGenClient(qualificationGeneratedTransport(
		options.Target,
		authorToken,
		client,
	))
	requested, err := author.RequestDeliveryPublicationApproval(
		ctx,
		deploymentgen.GenRequestDeliveryPublicationApprovalClientRequest{
			Project: options.ProjectID, Publication: publication.DeploymentID,
			Headers: deploymentgen.GenRequestDeliveryPublicationApprovalClientHeaders{
				IdempotencyKey: "authoring-request-approval-" + runSuffix,
			},
		},
	)
	if err != nil {
		return evidence, fmt.Errorf("read canonical publication approval: %w", err)
	}
	if requested.Body.Id == "" || requested.Body.Status != "pending" ||
		publication.PrincipalID == "" || requested.Body.RequestedBy != publication.PrincipalID ||
		requested.Body.ProjectId != options.ProjectID || requested.Body.Environment != options.Environment ||
		requested.Body.DeploymentId != publication.DeploymentID || requested.Body.RequestDigest == "" {
		return evidence, fmt.Errorf("canonical publication approval is not pending for the requested publication scope")
	}
	reviewer := deploymentgen.NewGenClient(qualificationGeneratedTransport(
		options.Target,
		reviewerToken,
		client,
	))
	approval, err := reviewer.ApproveDeliveryPublicationApproval(
		ctx,
		deploymentgen.GenApproveDeliveryPublicationApprovalClientRequest{
			Project: options.ProjectID, Publication: publication.DeploymentID,
			Approval: requested.Body.Id,
			Headers: deploymentgen.GenApproveDeliveryPublicationApprovalClientHeaders{
				IdempotencyKey: "authoring-approve-" + runSuffix,
			},
			Body: deploymentgen.GenSchemaDeploymentApprovalDecisionRequest{
				ExpectedRevision: requested.Body.Revision,
			},
		},
	)
	if err != nil {
		return evidence, fmt.Errorf("approve canonical publication: %w", err)
	}
	if approval.Body.Status != "approved" || approval.Body.Id != requested.Body.Id ||
		approval.Body.ProjectId != options.ProjectID || approval.Body.Environment != options.Environment ||
		approval.Body.DeploymentId != publication.DeploymentID || approval.Body.RequestDigest != requested.Body.RequestDigest ||
		approval.Body.ApprovedBy == nil || *approval.Body.ApprovedBy != reviewerPrincipalID || reviewerPrincipalID == publication.PrincipalID {
		return evidence, fmt.Errorf("publication approval result does not bind the independent reviewer to the requested publication")
	}
	evidence = qualificationApprovalEvidence{
		ID: approval.Body.Id, Status: string(approval.Body.Status), ApprovedBy: *approval.Body.ApprovedBy,
		DeploymentID: approval.Body.DeploymentId, ProjectID: approval.Body.ProjectId,
		Environment: approval.Body.Environment, RequestDigest: approval.Body.RequestDigest,
	}
	return evidence, nil
}
