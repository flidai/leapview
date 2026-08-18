package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	"github.com/flidai/leapview/internal/platform/cliapi"
	projectcli "github.com/flidai/leapview/internal/project/cli"
	"github.com/flidai/leapview/internal/project/devloop"
	"github.com/spf13/cobra"
)

func buildCommand(ctx context.Context) *cobra.Command {
	client := capabilityAPIClient{httpClient: authoringRefreshingHTTPClient(http.DefaultClient), validateAuthoring: true}
	return projectcli.DeliveryBuildCommand(ctx, projectDeliveryBuildOperations{client: client, checkpoints: projectcli.NewCandidateCheckpointStore(candidateCheckpointPath())})
}

func rollbackCommand(ctx context.Context) *cobra.Command {
	client := capabilityAPIClient{httpClient: authoringRefreshingHTTPClient(http.DefaultClient), validateAuthoring: true}
	return projectcli.DeliveryRollbackCommand(ctx, projectDeliveryRollbackOperations{client: client, checkpoints: projectcli.NewCandidateCheckpointStore(candidateCheckpointPath())})
}

// projectDeliveryPlanOperations captures the exact source snapshot through
// candidate-sync's sourceOnly mode before creating the target-owned plan.
// The adapter never supplies target policy, base revisions, evidence, or
// physical identities from the CLI.
type projectDeliveryPlanOperations struct {
	client      cliapi.Client
	remotes     projectcli.DevRemoteFactory
	checkpoints *projectcli.CandidateCheckpointStore
}

func (operations projectDeliveryPlanOperations) Create(ctx context.Context, options projectcli.DeliveryPlanOptions) (projectcli.DeliveryPlanResult, error) {
	if operations.client == nil {
		return projectcli.DeliveryPlanResult{}, fmt.Errorf("delivery plan API client is required")
	}
	credentials, err := operations.client.Resolve(ctx, options.Credentials)
	if err != nil {
		return projectcli.DeliveryPlanResult{}, err
	}
	projectID, sourceDigest := strings.TrimSpace(options.ProjectID), strings.TrimSpace(options.SourceDigest)
	sourceAttestationDigest := strings.TrimSpace(options.SourceAttestationDigest)
	targetID, candidateID := strings.TrimSpace(options.TargetID), strings.TrimSpace(options.CandidateID)
	environment := strings.TrimSpace(options.Environment)
	if projectID == "" || targetID == "" || sourceDigest == "" || sourceAttestationDigest == "" {
		generic, err := operations.client.Transport(ctx, credentials)
		if err != nil {
			return projectcli.DeliveryPlanResult{}, err
		}
		builder := devloop.FilesystemBuilder{ProjectPath: options.ProjectPath, CandidateKey: options.CandidateKey}
		snapshot, err := builder.Build(ctx)
		if err != nil {
			return projectcli.DeliveryPlanResult{}, fmt.Errorf("capture project source snapshot: %w", err)
		}
		remote, err := devloop.NewTransportRemote(newCandidateSynchronizationTransport(deploymentgen.NewGenClient(generic)), options.UploadConcurrency)
		if err != nil {
			return projectcli.DeliveryPlanResult{}, err
		}
		retained, err := remote.RetainSource(ctx, snapshot)
		if err != nil {
			return projectcli.DeliveryPlanResult{}, fmt.Errorf("retain project source snapshot: %w", err)
		}
		retainedProjectID, retainedSourceDigest, retainedAttestationDigest := retained.ProjectID.String(), retained.SourceDigest, retained.SourceAttestationDigest
		for _, assertion := range []struct {
			name, asserted, actual string
		}{
			{name: "project", asserted: projectID, actual: retainedProjectID},
			{name: "target", asserted: targetID, actual: retained.TargetID},
			{name: "source digest", asserted: sourceDigest, actual: retainedSourceDigest},
			{name: "source attestation", asserted: sourceAttestationDigest, actual: retainedAttestationDigest},
			{name: "environment", asserted: environment, actual: retained.Environment},
		} {
			if assertion.asserted == "" || assertion.asserted == assertion.actual {
				continue
			}
			return projectcli.DeliveryPlanResult{}, fmt.Errorf("retained source %s %q does not match asserted %q", assertion.name, assertion.actual, assertion.asserted)
		}
		if projectID == "" {
			projectID = retainedProjectID
		}
		if targetID == "" {
			targetID = retained.TargetID
		}
		if sourceDigest == "" {
			sourceDigest = retainedSourceDigest
		}
		if sourceAttestationDigest == "" {
			sourceAttestationDigest = retainedAttestationDigest
		}
		if environment == "" {
			environment = retained.Environment
		}
	}
	if projectID == "" || sourceDigest == "" || sourceAttestationDigest == "" || targetID == "" {
		return projectcli.DeliveryPlanResult{}, fmt.Errorf("delivery plan source snapshot is missing project, target, source digest, or source attestation")
	}
	operation := strings.TrimSpace(options.Operation)
	if operation == "" {
		operation = "code_change"
	}
	transport, err := operations.client.Transport(ctx, credentials)
	if err != nil {
		return projectcli.DeliveryPlanResult{}, err
	}
	response, err := deploymentgen.NewGenClient(transport).CreateDeliveryPlan(ctx, deploymentgen.GenCreateDeliveryPlanClientRequest{
		Project: projectID,
		Headers: deploymentgen.GenCreateDeliveryPlanClientHeaders{IdempotencyKey: deploymentIdempotencyKey("delivery-plan", projectID, targetID, operation, sourceDigest, candidateID)},
		Body:    deploymentgen.DeliveryPlanRequest{TargetId: targetID, Operation: deploymentgen.DeliveryOperationKind(operation), SourceDigest: sourceDigest, SourceAttestationDigest: sourceAttestationDigest},
	})
	if err != nil {
		return projectcli.DeliveryPlanResult{}, mapDeliveryCLIError("create delivery plan", err)
	}
	result := deliveryPlanResult(response.Body)
	if operations.checkpoints != nil {
		if err := operations.checkpoints.SavePlan(projectcli.DeliveryPlanCheckpoint{PlanID: result.PlanID, ProjectID: result.ProjectID, TargetID: result.TargetID, Environment: result.Environment, TargetOrigin: credentials.Target, SourceDigest: result.SourceDigest, SourceAttestationDigest: result.SourceAttestationDigest, PlanDigest: result.PlanDigest, ExecutionDigest: result.ExecutionDigest, EvidenceDigest: result.EvidenceDigest}); err != nil {
			return projectcli.DeliveryPlanResult{}, fmt.Errorf("persist delivery plan checkpoint: %w", err)
		}
	}
	return result, nil
}

func deliveryPlanResult(value deploymentgen.DeliveryPlanPreviewResponse) projectcli.DeliveryPlanResult {
	evidence := projectcli.DeliveryPlanEvidenceResult{
		Digest: value.Evidence.Digest, CompatibilityBreaking: value.Evidence.CompatibilityBreaking,
		AddedCount: value.Evidence.AddedCount, RemovedCount: value.Evidence.RemovedCount,
		DirectlyModifiedCount: value.Evidence.DirectlyModifiedCount, IndirectlyAffectedCount: value.Evidence.IndirectlyAffectedCount,
		ReuseCount: value.Evidence.ReuseCount, QualificationStepCount: value.Evidence.QualificationStepCount,
		ImpactStatement: optionalString(value.Evidence.ImpactStatement), PhysicalWorkStatement: optionalString(value.Evidence.PhysicalWorkStatement),
		ReuseStatement: optionalString(value.Evidence.ReuseStatement), RollbackClass: optionalEnumString(value.Evidence.RollbackClass), QualificationPolicy: value.Evidence.QualificationPolicy,
		StalePolicy: projectcli.DeliveryStalePolicyResult{Mode: string(value.Evidence.StalePolicy.Mode), AllowRetainedBase: value.Evidence.StalePolicy.AllowRetainedBase, Description: optionalString(value.Evidence.StalePolicy.Description)},
	}
	for _, input := range value.Evidence.PlannedInputs {
		evidence.PlannedInputs = append(evidence.PlannedInputs, projectcli.DeliveryPlannedInputResult{ID: input.Id, Mode: string(input.Mode), Revision: optionalString(input.Revision), Bound: optionalString(input.Bound)})
	}
	for _, step := range value.Evidence.QualificationSteps {
		evidence.QualificationSteps = append(evidence.QualificationSteps, projectcli.DeliveryQualificationStepResult{ID: step.Id, Kind: step.Kind, Description: step.Description, Required: step.Required, Blocking: step.Blocking})
	}
	for _, decision := range value.Evidence.ReuseDecisions {
		evidence.ReuseDecisions = append(evidence.ReuseDecisions, projectcli.DeliveryReuseDecisionResult{ResourceID: decision.ResourceId, Reusable: decision.Reusable, RetainBase: decision.RetainBase, Reason: decision.Reason, ReuseKeyDigest: optionalString(decision.ReuseKeyDigest)})
	}
	return projectcli.DeliveryPlanResult{
		SchemaVersion: 1, PlanID: value.Id, ProjectID: value.ProjectId, TargetID: value.TargetId,
		Environment: value.Environment, Operation: string(value.Operation), SourceDigest: value.SourceDigest, SourceAttestationDigest: value.SourceAttestationDigest,
		PlanDigest: value.PlanDigest, ExecutionDigest: value.ExecutionDigest, ProvenanceDigest: value.ProvenanceDigest,
		GovernanceDigest: value.GovernanceDigest, EvidenceDigest: value.EvidenceDigest, Status: string(value.Status), Evidence: evidence,
		BaseGenerationID: optionalString(value.BaseGenerationId), BaseTargetRevision: value.BaseTargetRevision,
	}
}

type projectDeliveryBuildOperations struct {
	client      cliapi.Client
	checkpoints *projectcli.CandidateCheckpointStore
}

func (operations projectDeliveryBuildOperations) Build(ctx context.Context, options projectcli.DeliveryBuildOptions) (projectcli.DeliveryBuildResult, error) {
	if operations.client == nil {
		return projectcli.DeliveryBuildResult{}, fmt.Errorf("delivery build API client is required")
	}
	if strings.TrimSpace(options.ProjectID) == "" && operations.checkpoints != nil {
		plan, lookupErr := operations.checkpoints.LoadPlan(options.PlanID)
		if lookupErr != nil {
			return projectcli.DeliveryBuildResult{}, fmt.Errorf("resolve plan checkpoint: %w", lookupErr)
		}
		options.ProjectID = plan.ProjectID
		if options.Credentials.Target == "" {
			options.Credentials.Target = plan.TargetOrigin
		}
	}
	if strings.TrimSpace(options.ProjectID) == "" {
		return projectcli.DeliveryBuildResult{}, fmt.Errorf("plan checkpoint has no project identity")
	}
	transport, err := operations.client.Transport(ctx, options.Credentials)
	if err != nil {
		return projectcli.DeliveryBuildResult{}, err
	}
	response, err := deploymentgen.NewGenClient(transport).BuildDeliveryPlan(ctx, deploymentgen.GenBuildDeliveryPlanClientRequest{
		Project: options.ProjectID, Plan: options.PlanID,
		Headers: deploymentgen.GenBuildDeliveryPlanClientHeaders{IdempotencyKey: deploymentIdempotencyKey("delivery-build", options.ProjectID, options.PlanID)},
	})
	if err != nil {
		return projectcli.DeliveryBuildResult{}, mapDeliveryCLIError("build delivery plan", err)
	}
	value := response.Body
	result := projectcli.DeliveryBuildResult{SchemaVersion: 1, BuildID: value.Id, PlanID: value.PlanId, PlanDigest: value.PlanDigest, SourceDigest: value.SourceDigest, ExecutionDigest: value.ExecutionDigest, CandidateID: optionalString(value.CandidateId), SealID: optionalString(value.SealId), Status: string(value.Status), Revision: value.Revision}
	if result.CandidateID != "" && operations.checkpoints != nil {
		origin := options.Credentials.Target
		var targetID, environment string
		if origin == "" {
			if plan, lookupErr := operations.checkpoints.LoadPlan(options.PlanID); lookupErr == nil {
				origin = plan.TargetOrigin
				targetID, environment = plan.TargetID, plan.Environment
			}
		} else if plan, lookupErr := operations.checkpoints.LoadPlan(options.PlanID); lookupErr == nil {
			targetID, environment = plan.TargetID, plan.Environment
		}
		_ = operations.checkpoints.SaveObjectIdentity("candidate", result.CandidateID, projectcli.DeliveryObjectCheckpoint{ProjectID: options.ProjectID, TargetOrigin: origin, TargetID: targetID, Environment: environment})
	}
	return result, nil
}

type projectDeliveryRollbackOperations struct {
	client      cliapi.Client
	checkpoints *projectcli.CandidateCheckpointStore
}

func (operations projectDeliveryRollbackOperations) Rollback(ctx context.Context, options projectcli.DeliveryRollbackOptions) (projectcli.DeliveryRollbackResult, error) {
	if operations.client == nil {
		return projectcli.DeliveryRollbackResult{}, fmt.Errorf("delivery rollback API client is required")
	}
	if strings.TrimSpace(options.ProjectID) == "" && operations.checkpoints != nil {
		identity, lookupErr := operations.checkpoints.LoadObjectIdentity("generation", options.GenerationID)
		if lookupErr != nil {
			return projectcli.DeliveryRollbackResult{}, fmt.Errorf("resolve generation checkpoint: %w", lookupErr)
		}
		options.ProjectID = identity.ProjectID
		if options.Credentials.Target == "" {
			options.Credentials.Target = identity.TargetOrigin
		}
	}
	if strings.TrimSpace(options.ProjectID) == "" {
		return projectcli.DeliveryRollbackResult{}, fmt.Errorf("generation checkpoint has no project identity")
	}
	transport, err := operations.client.Transport(ctx, options.Credentials)
	if err != nil {
		return projectcli.DeliveryRollbackResult{}, err
	}
	response, err := deploymentgen.NewGenClient(transport).RollbackDeliveryGeneration(ctx, deploymentgen.GenRollbackDeliveryGenerationClientRequest{
		Project: options.ProjectID, Generation: options.GenerationID,
		Headers: deploymentgen.GenRollbackDeliveryGenerationClientHeaders{IdempotencyKey: deploymentIdempotencyKey("delivery-rollback", options.ProjectID, options.GenerationID)},
	})
	if err != nil {
		return projectcli.DeliveryRollbackResult{}, mapDeliveryCLIError("rollback delivery generation", err)
	}
	value := response.Body
	return projectcli.DeliveryRollbackResult{SchemaVersion: 1, PublicationID: value.Id, GenerationID: value.GenerationId, CandidateID: value.CandidateId, PlanID: value.PlanId, PlanDigest: value.PlanDigest, Status: string(value.Status), ExpectedTargetRevision: value.ExpectedTargetRevision, ResultTargetRevision: value.ResultTargetRevision}, nil
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func optionalEnumString[T ~string](value *T) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(string(*value))
}

func mapDeliveryCLIError(operation string, err error) error {
	if err == nil {
		return nil
	}
	var problem *apigenclient.ProblemError
	if !errors.As(err, &problem) {
		return err
	}
	code := strings.TrimSpace(problem.Problem.Code)
	kind := "other"
	switch {
	case strings.Contains(code, "APPROVAL") || strings.Contains(code, "EXPIRED"):
		kind = "approval"
	case strings.Contains(code, "STALE") || strings.Contains(code, "CONFLICT") || problem.Response.StatusCode == 409:
		kind = "conflict"
	case problem.Response.StatusCode == 403 || strings.Contains(code, "FORBIDDEN") || strings.Contains(code, "REQUIRED"):
		kind = "forbidden"
	case problem.Response.StatusCode == 401:
		kind = "authentication"
	}
	return &projectcli.DeliveryError{Operation: operation, Kind: kind, Code: code, Status: problem.Response.StatusCode, Detail: problem.Problem.Detail, Cause: err}
}
