package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	"github.com/flidai/leapview/internal/app/config"
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
	targetSelector := strings.TrimSpace(options.Credentials.Target)
	if targetSelector == "" {
		targetSelector = strings.TrimSpace(config.MustLoad().Target)
	}
	credentials, err := operations.client.Resolve(ctx, options.Credentials)
	if err != nil {
		return projectcli.DeliveryPlanResult{}, err
	}
	projectID, sourceDigest := strings.TrimSpace(options.ProjectID), strings.TrimSpace(options.SourceDigest)
	sourceAttestationDigest := strings.TrimSpace(options.SourceAttestationDigest)
	targetID, candidateID := strings.TrimSpace(options.TargetID), strings.TrimSpace(options.CandidateID)
	environment := strings.TrimSpace(options.Environment)
	if options.ResolveCandidatePlan {
		return operations.resolveCandidatePlan(
			ctx,
			credentials,
			targetSelector,
			projectID,
			candidateID,
			targetID,
			environment,
			sourceDigest,
		)
	}
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
		remote, err := devloop.NewTransportRemote(
			newProjectDevSynchronizationTransport(
				newCandidateSynchronizationTransport(deploymentgen.NewGenClient(generic)),
			),
			options.UploadConcurrency,
		)
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
	operationKey, err := newDeploymentIdempotencyKey("delivery-plan", projectID, targetID, operation, sourceDigest, candidateID)
	if err != nil {
		return projectcli.DeliveryPlanResult{}, err
	}
	response, err := deploymentgen.NewGenClient(transport).CreateDeliveryPlan(ctx, deploymentgen.GenCreateDeliveryPlanClientRequest{
		Project: projectID,
		Headers: deploymentgen.GenCreateDeliveryPlanClientHeaders{IdempotencyKey: operationKey},
		Body:    deploymentgen.DeliveryPlanRequest{TargetId: targetID, Operation: deploymentgen.DeliveryOperationKind(operation), SourceDigest: sourceDigest, SourceAttestationDigest: sourceAttestationDigest},
	})
	if err != nil {
		return projectcli.DeliveryPlanResult{}, mapDeliveryCLIError("create delivery plan", err)
	}
	result := deliveryPlanResult(response.Body)
	if operations.checkpoints != nil {
		if err := operations.checkpoints.SavePlan(projectcli.DeliveryPlanCheckpoint{PlanID: result.PlanID, ProjectID: result.ProjectID, TargetID: result.TargetID, Environment: result.Environment, TargetOrigin: credentials.Target, TargetSelector: targetSelector, SourceDigest: result.SourceDigest, SourceAttestationDigest: result.SourceAttestationDigest, PlanDigest: result.PlanDigest, ExecutionDigest: result.ExecutionDigest, EvidenceDigest: result.EvidenceDigest}); err != nil {
			return projectcli.DeliveryPlanResult{}, fmt.Errorf("persist delivery plan checkpoint: %w", err)
		}
	}
	return result, nil
}

func (operations projectDeliveryPlanOperations) resolveCandidatePlan(
	ctx context.Context,
	credentials cliapi.Credentials,
	targetSelector string,
	projectID string,
	candidateID string,
	targetID string,
	environment string,
	sourceDigest string,
) (projectcli.DeliveryPlanResult, error) {
	if projectID == "" || candidateID == "" {
		return projectcli.DeliveryPlanResult{}, fmt.Errorf("candidate plan lookup requires project and candidate identities")
	}
	transport, err := operations.client.Transport(ctx, credentials)
	if err != nil {
		return projectcli.DeliveryPlanResult{}, err
	}
	client := deploymentgen.NewGenClient(transport)
	candidate, err := client.GetDeliveryCandidateStatus(
		ctx,
		deploymentgen.GenGetDeliveryCandidateStatusClientRequest{
			Project: projectID, Candidate: candidateID,
		},
	)
	if err != nil {
		return projectcli.DeliveryPlanResult{}, mapDeliveryCLIError("read synchronized delivery candidate", err)
	}
	if candidate.Body.Id != candidateID || candidate.Body.ProjectId != projectID ||
		(targetID != "" && candidate.Body.TargetId != targetID) ||
		(environment != "" && candidate.Body.Environment != environment) ||
		(sourceDigest != "" && candidate.Body.SourceDigest != sourceDigest) ||
		candidate.Body.Status != deploymentgen.DeliveryCandidateStatusReady {
		return projectcli.DeliveryPlanResult{}, fmt.Errorf("synchronized delivery candidate does not match the dev result")
	}
	plan, err := client.GetDeliveryPlanPreview(
		ctx,
		deploymentgen.GenGetDeliveryPlanPreviewClientRequest{
			Project: projectID, Plan: candidate.Body.PlanId,
		},
	)
	if err != nil {
		return projectcli.DeliveryPlanResult{}, mapDeliveryCLIError("read synchronized delivery plan", err)
	}
	if plan.Body.Id != candidate.Body.PlanId ||
		plan.Body.PlanDigest != candidate.Body.PlanDigest ||
		plan.Body.ProjectId != candidate.Body.ProjectId ||
		plan.Body.TargetId != candidate.Body.TargetId ||
		plan.Body.Environment != candidate.Body.Environment ||
		plan.Body.SourceDigest != candidate.Body.SourceDigest {
		return projectcli.DeliveryPlanResult{}, fmt.Errorf("synchronized delivery plan does not match its candidate")
	}
	result := deliveryPlanResult(plan.Body)
	if operations.checkpoints != nil {
		if err := operations.checkpoints.SavePlan(projectcli.DeliveryPlanCheckpoint{
			PlanID: result.PlanID, ProjectID: result.ProjectID,
			TargetID: result.TargetID, Environment: result.Environment,
			TargetOrigin: credentials.Target, TargetSelector: targetSelector, SourceDigest: result.SourceDigest,
			SourceAttestationDigest: result.SourceAttestationDigest,
			PlanDigest:              result.PlanDigest, ExecutionDigest: result.ExecutionDigest,
			EvidenceDigest: result.EvidenceDigest,
		}); err != nil {
			return projectcli.DeliveryPlanResult{}, fmt.Errorf("persist synchronized delivery plan checkpoint: %w", err)
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
	var planCheckpoint projectcli.DeliveryPlanCheckpoint
	if strings.TrimSpace(options.ProjectID) == "" && operations.checkpoints != nil {
		plan, lookupErr := operations.checkpoints.LoadPlan(options.PlanID)
		if lookupErr != nil {
			return projectcli.DeliveryBuildResult{}, fmt.Errorf("resolve plan checkpoint: %w", lookupErr)
		}
		planCheckpoint = plan
		options.ProjectID = plan.ProjectID
		if options.Credentials.Target == "" {
			options.Credentials.Target = plan.TargetSelector
			if strings.TrimSpace(options.Credentials.Target) == "" {
				options.Credentials.Target = plan.TargetOrigin
			}
		}
	}
	if strings.TrimSpace(options.ProjectID) == "" {
		return projectcli.DeliveryBuildResult{}, fmt.Errorf("plan checkpoint has no project identity")
	}
	transport, err := operations.client.Transport(ctx, options.Credentials)
	if err != nil {
		return projectcli.DeliveryBuildResult{}, err
	}
	operationKey := deploymentIdempotencyKey("delivery-build", options.ProjectID, options.PlanID)
	if operations.checkpoints != nil && planCheckpoint.PlanID != "" {
		freshKey, keyErr := newDeploymentIdempotencyKey("delivery-build", options.ProjectID, options.PlanID)
		if keyErr != nil {
			return projectcli.DeliveryBuildResult{}, keyErr
		}
		operationKey, keyErr = operations.checkpoints.BindPlanBuildIdempotencyKey(options.PlanID, "", freshKey)
		if keyErr != nil {
			return projectcli.DeliveryBuildResult{}, fmt.Errorf("persist delivery build operation: %w", keyErr)
		}
	}
	client := deploymentgen.NewGenClient(transport)
	build := func(key string) (deploymentgen.GenBuildDeliveryPlanClientResponse, error) {
		return client.BuildDeliveryPlan(ctx, deploymentgen.GenBuildDeliveryPlanClientRequest{
			Project: options.ProjectID, Plan: options.PlanID,
			Headers: deploymentgen.GenBuildDeliveryPlanClientHeaders{IdempotencyKey: key},
		})
	}
	response, err := build(operationKey)
	if err != nil && deliveryCLIProblemCode(err, "DELIVERY_IDEMPOTENCY_DRIFT") {
		freshKey, keyErr := newDeploymentIdempotencyKey("delivery-build", options.ProjectID, options.PlanID)
		if keyErr != nil {
			return projectcli.DeliveryBuildResult{}, keyErr
		}
		if operations.checkpoints != nil && planCheckpoint.PlanID != "" {
			freshKey, keyErr = operations.checkpoints.BindPlanBuildIdempotencyKey(options.PlanID, operationKey, freshKey)
			if keyErr != nil {
				return projectcli.DeliveryBuildResult{}, fmt.Errorf("rotate delivery build operation: %w", keyErr)
			}
		}
		if freshKey != operationKey {
			response, err = build(freshKey)
		}
	}
	if err != nil {
		return projectcli.DeliveryBuildResult{}, mapDeliveryCLIError("build delivery plan", err)
	}
	value := response.Body
	result := projectcli.DeliveryBuildResult{SchemaVersion: 1, BuildID: value.Id, PlanID: value.PlanId, PlanDigest: value.PlanDigest, SourceDigest: value.SourceDigest, ExecutionDigest: value.ExecutionDigest, CandidateID: optionalString(value.CandidateId), SealID: optionalString(value.SealId), Status: string(value.Status), Revision: value.Revision}
	if result.CandidateID != "" && operations.checkpoints != nil {
		if planCheckpoint.PlanID == "" {
			planCheckpoint, _ = operations.checkpoints.LoadPlan(options.PlanID)
		}
		origin := planCheckpoint.TargetOrigin
		if origin == "" {
			origin = options.Credentials.Target
		}
		_ = operations.checkpoints.SaveObjectIdentity("candidate", result.CandidateID, projectcli.DeliveryObjectCheckpoint{ProjectID: options.ProjectID, TargetOrigin: origin, TargetSelector: planCheckpoint.TargetSelector, TargetID: planCheckpoint.TargetID, Environment: planCheckpoint.Environment})
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
			options.Credentials.Target = identity.TargetSelector
			if strings.TrimSpace(options.Credentials.Target) == "" {
				options.Credentials.Target = identity.TargetOrigin
			}
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

func deliveryCLIProblemCode(err error, code string) bool {
	var problem *apigenclient.ProblemError
	return errors.As(err, &problem) && strings.TrimSpace(problem.Problem.Code) == code
}
