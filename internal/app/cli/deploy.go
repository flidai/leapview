package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	"github.com/flidai/leapview/internal/platform/cliapi"
	"github.com/flidai/leapview/internal/platform/safetext"
	projectcli "github.com/flidai/leapview/internal/project/cli"
	projectdevloop "github.com/flidai/leapview/internal/project/devloop"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/spf13/cobra"
)

const projectDeploymentCandidateKey = "deploy"

type projectDeployOperations struct {
	client      cliapi.Client
	planner     projectcli.DeliveryPlanOperations
	builder     projectcli.DeliveryBuildOperations
	publisher   projectcli.PublishOperations
	checkpoints *projectcli.CandidateCheckpointStore
	operations  *projectcli.DeploymentOperationStore
	// sourceCapture is injectable for adapters/tests, but the application
	// command defaults to the filesystem capture below. It is deliberately
	// invoked before the first source-retention or delivery request.
	sourceCapture func(context.Context, string, string, string) (projectdevloop.Snapshot, error)
	now           func() time.Time
}

type deploymentTargetIdentityReader interface {
	TargetIdentity(context.Context, cliapi.Credentials) (targetID, canonicalOrigin, environment string, err error)
}

func deployCommand(ctx context.Context, opts *rootOptions) *cobra.Command {
	client := capabilityAPIClient{httpClient: authoringRefreshingHTTPClient(http.DefaultClient), validateAuthoring: true}
	checkpoints := projectcli.NewCandidateCheckpointStore(candidateCheckpointPath())
	operationStore := projectcli.NewDeploymentOperationStore(deploymentOperationPath())
	return projectcli.DeployCommand(ctx, client, projectDeployOperations{
		client:      client,
		planner:     projectDeliveryPlanOperations{client: client, remotes: projectDevRemoteFactory{client: client}, checkpoints: checkpoints},
		builder:     projectDeliveryBuildOperations{client: client, checkpoints: checkpoints},
		publisher:   projectPublishOperations{client: client, checkpoints: checkpoints},
		checkpoints: checkpoints, operations: operationStore,
	})
}

func deploymentOperationPath() string {
	return filepath.Join(filepath.Dir(clientConfigPath()), "deployment-operations.json")
}

func (operations projectDeployOperations) Deploy(ctx context.Context, options projectcli.DeployOptions, out io.Writer) error {
	if operations.client == nil || operations.planner == nil || operations.builder == nil || operations.publisher == nil {
		return fmt.Errorf("canonical project deployment operations are required (plan, build, and publish)")
	}
	if operations.operations == nil {
		return fmt.Errorf("durable deployment operation store is required")
	}
	if strings.TrimSpace(options.Credentials.ProjectID) == "" {
		return deploymentSelectionFailure(options, out, "PROJECT_ID_REQUIRED", options.OperationHandle, "target-bound Project identity is required before deployment operation creation")
	}
	var preCapturedSource *projectdevloop.Snapshot
	if strings.TrimSpace(options.Intent) == "new" {
		captured, captureErr := operations.captureOperationSource(ctx, options.SourceRoot, options.Credentials.ProjectID, "deployment-source-pending")
		if captureErr != nil {
			return deploymentSelectionFailure(options, out, "SOURCE_CAPTURE_FAILED", options.OperationHandle, captureErr.Error())
		}
		preCapturedSource = &captured
	}
	environment, err := operations.client.Environment(ctx, options.Credentials, options.Environment)
	if err != nil {
		return err
	}
	if asserted := strings.TrimSpace(options.Environment); asserted != "" && strings.TrimSpace(environment) != asserted {
		return fmt.Errorf("target instance environment %q does not match asserted environment %q", environment, asserted)
	}
	return operations.deployWithOperation(ctx, options, environment, out, preCapturedSource)
}

func (operations projectDeployOperations) deployWithOperation(ctx context.Context, options projectcli.DeployOptions, environment string, out io.Writer, preCapturedSource *projectdevloop.Snapshot) error {
	store := operations.operations
	origin := strings.TrimSpace(options.Credentials.CanonicalOrigin)
	if origin == "" {
		origin = strings.TrimSpace(options.Credentials.Target)
	}
	projectID := strings.TrimSpace(options.Credentials.ProjectID)
	preflightTargetID := ""
	if projectID == "" {
		return deploymentSelectionFailure(options, out, "PROJECT_ID_REQUIRED", options.OperationHandle, "target-bound Project identity is required before deployment operation creation")
	}
	if identityReader, ok := operations.client.(deploymentTargetIdentityReader); ok {
		if targetID, canonicalOrigin, targetEnvironment, identityErr := identityReader.TargetIdentity(ctx, options.Credentials); identityErr != nil {
			return identityErr
		} else {
			if strings.TrimSpace(canonicalOrigin) != "" {
				origin = strings.TrimSpace(canonicalOrigin)
			}
			if strings.TrimSpace(targetEnvironment) != "" && targetEnvironment != environment {
				return fmt.Errorf("target identity environment %q does not match asserted environment %q", targetEnvironment, environment)
			}
			if strings.TrimSpace(targetID) != "" {
				preflightTargetID = strings.TrimSpace(targetID)
				options.Credentials.CanonicalOrigin = origin
			}
		}
	}
	// An interactive --resume without a handle is a request to choose from
	// retained work. Headless resume remains fail-closed below and requires an
	// exact --operation handle.
	if strings.TrimSpace(options.Intent) == "resume" && strings.TrimSpace(options.OperationHandle) == "" && options.Interactive {
		options.Intent = ""
	}
	if strings.TrimSpace(options.Intent) == "" && options.Interactive {
		if err := operations.interactiveDeploymentSelection(ctx, &options, out, origin, projectID, environment); err != nil {
			return err
		}
	}
	if strings.TrimSpace(options.Intent) == "" {
		return deploymentSelectionFailure(options, out, "NONINTERACTIVE_INTENT_REQUIRED", "", "headless deploy requires --new or --resume --operation <handle>")
	}

	var descriptor projectcli.DeploymentOperationDescriptor
	var err error
	var sourceSnapshot *projectdevloop.Snapshot
	switch strings.TrimSpace(options.Intent) {
	case "resume":
		if strings.TrimSpace(options.OperationHandle) == "" {
			return deploymentSelectionFailure(options, out, "OPERATION_HANDLE_REQUIRED", "", "--resume requires --operation <handle>")
		}
		descriptor, err = store.SelectExact(options.OperationHandle, origin, preflightTargetID, projectID, environment)
		if err != nil {
			return deploymentSelectionFailure(options, out, selectionErrorCode(err), options.OperationHandle, err.Error())
		}
		if descriptor.TargetID == "" && preflightTargetID != "" {
			descriptor.TargetID = preflightTargetID
			if err := store.Save(descriptor); err != nil {
				return fmt.Errorf("persist reconciled target identity: %w", err)
			}
		}
		if descriptor.PublicationID != "" {
			return operations.reconcileAndReport(ctx, options, descriptor, out)
		}
		if descriptor.Outcome == projectcli.DeploymentOperationFailure {
			return operations.reportOperation(descriptor, options.Format, out, "inspect retained operation before retrying")
		}
		if descriptor.PlanID == "" {
			snapshot, snapshotErr := operations.operationSourceSnapshot(ctx, descriptor)
			if snapshotErr != nil {
				return operations.markErrorAndReport(descriptor, projectcli.DeploymentOperationIndeterminate, "retained source snapshot is unavailable; explicit new planning is required", snapshotErr, out, options.Format)
			}
			sourceSnapshot = snapshot
		}
	case "new", "":
		retained, listErr := store.List(origin, projectID, environment)
		if listErr != nil {
			return listErr
		}
		unresolved := make([]string, 0)
		for _, candidate := range retained {
			if candidate.Outcome == projectcli.DeploymentOperationIndeterminate && candidate.PublicationID != "" {
				reconciled, reconcileErr := operations.reconcileDescriptor(ctx, options, candidate)
				if reconcileErr != nil {
					return deploymentSelectionFailure(options, out, "INDETERMINATE_PUBLICATION", candidate.Handle, fmt.Sprintf("indeterminate publication could not be reconciled: %v", reconcileErr))
				}
				candidate = reconciled
				if candidate.Outcome == projectcli.DeploymentOperationIndeterminate {
					return deploymentSelectionFailure(options, out, "INDETERMINATE_PUBLICATION", candidate.Handle, "publication remains indeterminate after reconciliation; resume it before starting a new operation")
				}
			}
			if candidate.Outcome == projectcli.DeploymentOperationIndeterminate && strings.TrimSpace(options.Intent) == "new" {
				return deploymentSelectionFailure(options, out, "INDETERMINATE_PUBLICATION", candidate.Handle, "publication remains indeterminate; resume and reconcile this operation before starting a new operation")
			}
			if strings.TrimSpace(options.Intent) == "" {
				if candidate.Outcome == projectcli.DeploymentOperationUnknown || candidate.Outcome == projectcli.DeploymentOperationPendingApproval || candidate.Outcome == projectcli.DeploymentOperationIndeterminate {
					unresolved = append(unresolved, candidate.Handle)
				}
			}
		}
		if len(unresolved) > 0 {
			return deploymentSelectionFailure(options, out, "OPERATION_SELECTION_REQUIRED", "", fmt.Sprintf("retained operations require explicit --resume --operation (or --new): %s", strings.Join(unresolved, ", ")))
		}
		descriptor, err = projectcli.NewDeploymentOperation(options.OperationHandle, origin, options.Credentials.Target, projectID, environment, options.SourceRoot, "")
		if err != nil {
			return err
		}
		descriptor.SourceSnapshotRef = "candidate-sync:" + descriptor.Handle
		descriptor.TargetID = preflightTargetID
		if preCapturedSource != nil {
			preCapturedSource.CandidateKey = descriptor.SourceSnapshotRef
			sourceSnapshot = preCapturedSource
		} else {
			capturedSnapshot, captureErr := operations.captureOperationSource(ctx, options.SourceRoot, projectID, descriptor.SourceSnapshotRef)
			if captureErr != nil {
				return deploymentSelectionFailure(options, out, "SOURCE_CAPTURE_FAILED", descriptor.Handle, captureErr.Error())
			}
			sourceSnapshot = &capturedSnapshot
		}
		retainOperationSnapshot(&descriptor, *sourceSnapshot)
		if err := store.Create(descriptor); err != nil {
			code := "OPERATION_DESCRIPTOR_INVALID"
			if strings.Contains(err.Error(), "already exists") {
				code = "OPERATION_HANDLE_EXISTS"
			}
			return deploymentSelectionFailure(options, out, code, descriptor.Handle, err.Error())
		}
	default:
		return deploymentSelectionFailure(options, out, "INVALID_INTENT", options.OperationHandle, fmt.Sprintf("unsupported intent %q", options.Intent))
	}

	plan := projectcli.DeliveryPlanResult{}
	if descriptor.PlanID == "" {
		plan, err = operations.planner.Create(ctx, projectcli.DeliveryPlanOptions{SourceRoot: descriptor.SourceRoot, Credentials: options.Credentials, Operation: "code_change", CandidateKey: descriptor.SourceSnapshotRef, UploadConcurrency: 4, Environment: environment, IdempotencyKey: descriptor.PlanIdempotencyKey, SourceSnapshot: sourceSnapshot})
		if err != nil {
			return operations.markErrorAndReport(descriptor, classifyDeploymentError(err), "plan acknowledgement was not established", err, out, options.Format)
		}
		if plan.PlanID == "" || plan.ProjectID != projectID || plan.Environment != environment || plan.TargetID == "" {
			return operations.markErrorAndReport(descriptor, projectcli.DeploymentOperationIndeterminate, "target returned incomplete plan identity", fmt.Errorf("incomplete plan identity"), out, options.Format)
		}
		if descriptor.TargetID != "" && descriptor.TargetID != plan.TargetID {
			return operations.markErrorAndReport(descriptor, projectcli.DeploymentOperationFailure, "target identity changed", fmt.Errorf("plan target %q does not match retained target %q", plan.TargetID, descriptor.TargetID), out, options.Format)
		}
		if sourceSnapshot != nil && plan.SourceDigest != sourceSnapshot.Digest {
			return operations.markErrorAndReport(descriptor, projectcli.DeploymentOperationFailure, "target retained a different source snapshot", fmt.Errorf("plan source digest %q does not match retained source digest %q", plan.SourceDigest, sourceSnapshot.Digest), out, options.Format)
		}
		descriptor.TargetID, descriptor.SourceDigest = plan.TargetID, plan.SourceDigest
		descriptor.SourceAttestationDigest, descriptor.ProvenanceDigest = plan.SourceAttestationDigest, plan.ProvenanceDigest
		descriptor.PlanID, descriptor.PlanDigest = plan.PlanID, plan.PlanDigest
		descriptor.PlanStatus, descriptor.PlanExpiresAt, descriptor.PlanEvidence = plan.Status, plan.ExpiresAt, plan.Evidence
		descriptor.GovernanceDigest = plan.GovernanceDigest
		descriptor.BaseGenerationID, descriptor.BaseTargetRevision = plan.BaseGenerationID, plan.BaseTargetRevision
		descriptor.ExecutionDigest, descriptor.EvidenceDigest = plan.ExecutionDigest, plan.EvidenceDigest
		if err := store.Save(descriptor); err != nil {
			return fmt.Errorf("persist deployment plan identity: %w", err)
		}
		if strings.TrimSpace(options.Format) != "json" {
			// Text mode gets the target-owned impact/physical-work review before
			// any build or publication request. JSON remains one result envelope.
			if err := projectcli.WriteDeliveryPlanResult(out, "text", plan); err != nil {
				return err
			}
		}
	} else {
		plan = projectcli.DeliveryPlanResult{PlanID: descriptor.PlanID, ProjectID: descriptor.ProjectID, TargetID: descriptor.TargetID, Environment: descriptor.Environment, SourceDigest: descriptor.SourceDigest, SourceAttestationDigest: descriptor.SourceAttestationDigest, ProvenanceDigest: descriptor.ProvenanceDigest, PlanDigest: descriptor.PlanDigest, Status: descriptor.PlanStatus, ExpiresAt: descriptor.PlanExpiresAt, Evidence: descriptor.PlanEvidence, GovernanceDigest: descriptor.GovernanceDigest, BaseGenerationID: descriptor.BaseGenerationID, BaseTargetRevision: descriptor.BaseTargetRevision, ExecutionDigest: descriptor.ExecutionDigest, EvidenceDigest: descriptor.EvidenceDigest}
	}
	if descriptor.CandidateID == "" {
		if planErr := validateDeploymentPlanForBuild(plan, operations.currentTime()); planErr != nil {
			return operations.markErrorAndReport(descriptor, projectcli.DeploymentOperationFailure, "start an explicit new operation to obtain and review a fresh plan", planErr, out, options.Format)
		}
	}
	if descriptor.PublicationID == "" {
		if descriptor.CandidateID != "" || plan.PlanID == descriptor.PlanID && strings.TrimSpace(options.Intent) == "resume" {
			if reviewErr := writeDeploymentPlanReview(out, options.Format, descriptor, plan); reviewErr != nil {
				return reviewErr
			}
		}
		if confirmationErr := confirmDeploymentPlan(options, descriptor); confirmationErr != nil {
			return operations.markErrorAndReport(descriptor, projectcli.DeploymentOperationPendingApproval, "provide --confirm-plan with the exact retained plan digest, then resume this handle", confirmationErr, out, options.Format)
		}
	}

	if descriptor.CandidateID == "" {
		build, buildErr := operations.builder.Build(ctx, projectcli.DeliveryBuildOptions{ProjectID: projectID, PlanID: descriptor.PlanID, Credentials: options.Credentials, IdempotencyKey: descriptor.BuildIdempotencyKey})
		if buildErr != nil {
			return operations.markErrorAndReport(descriptor, classifyDeploymentError(buildErr), "build acknowledgement was not established", buildErr, out, options.Format)
		}
		descriptor.BuildID, descriptor.CandidateID, descriptor.SealID = build.BuildID, build.CandidateID, build.SealID
		descriptor.BuildRevision, descriptor.CandidateRevision = build.Revision, build.CandidateRevision
		if build.PlanID != "" && build.PlanID != descriptor.PlanID {
			return operations.markErrorAndReport(descriptor, projectcli.DeploymentOperationFailure, "build does not match retained plan", fmt.Errorf("build plan %q does not match retained plan %q", build.PlanID, descriptor.PlanID), out, options.Format)
		}
		if build.SourceDigest != "" && build.SourceDigest != descriptor.SourceDigest {
			return operations.markErrorAndReport(descriptor, projectcli.DeploymentOperationFailure, "build does not match retained source", fmt.Errorf("build source digest %q does not match retained source %q", build.SourceDigest, descriptor.SourceDigest), out, options.Format)
		}
		if build.PlanDigest != "" && descriptor.PlanDigest != "" && build.PlanDigest != descriptor.PlanDigest {
			return operations.markErrorAndReport(descriptor, projectcli.DeploymentOperationFailure, "build does not match retained plan", fmt.Errorf("build plan digest mismatch"), out, options.Format)
		}
		if build.ExecutionDigest != "" && descriptor.ExecutionDigest != "" && build.ExecutionDigest != descriptor.ExecutionDigest {
			return operations.markErrorAndReport(descriptor, projectcli.DeploymentOperationFailure, "build does not match retained execution evidence", fmt.Errorf("build execution digest mismatch"), out, options.Format)
		}
		if build.CandidateID == "" {
			return operations.markErrorAndReport(descriptor, projectcli.DeploymentOperationFailure, "build did not produce a sealed candidate", fmt.Errorf("build %s is %s", build.BuildID, build.Status), out, options.Format)
		}
		if build.CandidateRevision <= 0 || strings.TrimSpace(build.SealID) == "" {
			return operations.markErrorAndReport(descriptor, projectcli.DeploymentOperationFailure, "build returned incomplete sealed-candidate evidence", fmt.Errorf("candidate revision and seal identity are required"), out, options.Format)
		}
		if err := store.Save(descriptor); err != nil {
			return fmt.Errorf("persist deployment candidate identity: %w", err)
		}
	}
	if descriptor.CandidateID != "" && descriptor.StatusURL == "" {
		statusURL, statusErr := deploymentCandidateReviewURL(descriptor.TargetOrigin, descriptor.CandidateID)
		if statusErr != nil {
			return operations.markErrorAndReport(descriptor, projectcli.DeploymentOperationFailure, "candidate review URL could not be established", statusErr, out, options.Format)
		}
		descriptor.StatusURL = statusURL
		if err := store.Save(descriptor); err != nil {
			return fmt.Errorf("persist candidate review URL: %w", err)
		}
	}

	if descriptor.SourceDigest == "" || descriptor.ProvenanceDigest == "" {
		return operations.markAndReport(descriptor, projectcli.DeploymentOperationIndeterminate, "retained source provenance evidence is unavailable", "", out, options.Format)
	}
	checkpoint := projectcli.CandidateCheckpoint{SourceRoot: descriptor.SourceRoot, TargetOrigin: descriptor.TargetOrigin, TargetSelector: descriptor.TargetSelector, TargetID: descriptor.TargetID, Environment: descriptor.Environment, ProjectID: descriptor.ProjectID, CandidateID: descriptor.CandidateID, CandidateKey: descriptor.SourceSnapshotRef, CandidateRevision: descriptor.CandidateRevision, BuildID: descriptor.BuildID, BuildRevision: descriptor.BuildRevision, SealID: descriptor.SealID, ArtifactDigest: descriptor.SourceDigest, ProvenanceDigest: descriptor.ProvenanceDigest, PlanID: descriptor.PlanID, PlanDigest: descriptor.PlanDigest, ExecutionDigest: descriptor.ExecutionDigest, EvidenceDigest: descriptor.EvidenceDigest}
	if operations.checkpoints != nil {
		if err := operations.checkpoints.Save(checkpoint); err != nil {
			return fmt.Errorf("persist deployment checkpoint: %w", err)
		}
	}

	if rich, ok := operations.publisher.(projectcli.PublishResultOperations); ok {
		result, publishErr := rich.PublishResult(ctx, projectcli.PublishOptions{ProjectID: descriptor.ProjectID, Credentials: options.Credentials, Checkpoint: checkpoint, CandidateID: descriptor.CandidateID, Format: options.Format, IdempotencyKey: descriptor.PublicationIdempotencyKey})
		if publishErr != nil {
			return operations.markErrorAndReport(descriptor, classifyDeploymentError(publishErr), "publication acknowledgement was not established; reconcile before retrying", publishErr, out, options.Format)
		}
		if strings.TrimSpace(result.PublicationID) == "" {
			return operations.markAndReport(descriptor, projectcli.DeploymentOperationIndeterminate, "publication identity was not returned; reconcile before retrying", "publisher returned no durable publication identity", out, options.Format)
		}
		if validationErr := validateDeploymentPublishResult(result, checkpoint); validationErr != nil {
			return operations.markErrorAndReport(descriptor, projectcli.DeploymentOperationFailure, "target publication evidence does not match retained checkpoint", validationErr, out, options.Format)
		}
		descriptor.PublicationID, descriptor.GenerationID, descriptor.PublicationStatus = result.PublicationID, result.GenerationID, result.Status
		descriptor.PublicationTargetRevision = result.TargetRevision
		switch result.Status {
		case "committed":
			if strings.TrimSpace(result.GenerationID) == "" {
				descriptor.Outcome = projectcli.DeploymentOperationIndeterminate
				descriptor.FailureDetail = "target reported committed publication without generation identity"
			} else {
				descriptor.Outcome = projectcli.DeploymentOperationActive
			}
		case "pending":
			descriptor.Outcome = projectcli.DeploymentOperationPendingApproval
		case "rejected":
			descriptor.Outcome = projectcli.DeploymentOperationFailure
		default:
			descriptor.Outcome = projectcli.DeploymentOperationIndeterminate
		}
		if err := store.Save(descriptor); err != nil {
			return fmt.Errorf("persist deployment publication identity: %w", err)
		}
		return operations.reportOperation(descriptor, options.Format, out, nextActionForOutcome(descriptor.Outcome))
	}
	if err := operations.publisher.Publish(ctx, projectcli.PublishOptions{ProjectID: descriptor.ProjectID, Credentials: options.Credentials, Checkpoint: checkpoint, CandidateID: descriptor.CandidateID, Format: options.Format, IdempotencyKey: descriptor.PublicationIdempotencyKey}, out); err != nil {
		return operations.markErrorAndReport(descriptor, classifyDeploymentError(err), "publication acknowledgement was not established; reconcile before retrying", err, out, options.Format)
	}
	return operations.markAndReport(descriptor, projectcli.DeploymentOperationIndeterminate, "publication identity was not returned by the adapter; reconcile before retrying", "", out, options.Format)
}

func (operations projectDeployOperations) interactiveDeploymentSelection(ctx context.Context, options *projectcli.DeployOptions, out io.Writer, origin, projectID, environment string) error {
	retained, err := operations.operations.List(origin, projectID, environment)
	if err != nil {
		return err
	}
	if len(retained) == 0 {
		options.Intent = "new"
		return nil
	}
	if len(retained) > 0 {
		fmt.Fprintln(out, "retained deployment operations:")
		for index, candidate := range retained {
			fmt.Fprintf(out, "%d) %s target=%s project=%s environment=%s source=%s revision=%s created=%s outcome=%s\n", index+1, candidate.Handle, candidate.TargetID, candidate.ProjectID, candidate.Environment, candidate.SourceDigest, candidate.SourceRevision, candidate.CreatedAt, candidate.Outcome)
		}
	}
	if options.ConfirmationReader == nil {
		return deploymentSelectionFailure(*options, out, "OPERATION_SELECTION_REQUIRED", "", "interactive operation selection requires a terminal input")
	}
	writer := options.ConfirmationWriter
	if writer == nil {
		writer = out
	}
	fmt.Fprint(writer, "select an operation number to resume, or n for a new operation: ")
	line, readErr := bufio.NewReader(options.ConfirmationReader).ReadString('\n')
	if readErr != nil && strings.TrimSpace(line) == "" {
		return deploymentSelectionFailure(*options, out, "OPERATION_SELECTION_REQUIRED", "", "no operation selection was provided")
	}
	choice := strings.TrimSpace(line)
	if strings.EqualFold(choice, "n") || strings.EqualFold(choice, "new") {
		options.Intent = "new"
		return nil
	}
	selection, parseErr := strconv.Atoi(choice)
	if parseErr != nil || selection < 1 || selection > len(retained) {
		return deploymentSelectionFailure(*options, out, "INVALID_OPERATION_SELECTION", "", "choose a listed operation number or n for a new operation")
	}
	options.Intent = "resume"
	options.OperationHandle = retained[selection-1].Handle
	return nil
}

func writeDeploymentPlanReview(out io.Writer, format string, descriptor projectcli.DeploymentOperationDescriptor, plan projectcli.DeliveryPlanResult) error {
	if strings.TrimSpace(format) == "json" {
		return nil
	}
	fmt.Fprintf(out, "plan-review operation %s target %s environment %s source %s plan %s digest %s\n", descriptor.Handle, descriptor.TargetID, descriptor.Environment, descriptor.SourceDigest, descriptor.PlanID, descriptor.PlanDigest)
	return projectcli.WriteDeliveryPlanResult(out, "text", plan)
}

func confirmDeploymentPlan(options projectcli.DeployOptions, descriptor projectcli.DeploymentOperationDescriptor) error {
	expected := strings.TrimSpace(descriptor.PlanDigest)
	if expected == "" {
		return fmt.Errorf("retained plan has no plan digest")
	}
	provided := strings.TrimSpace(options.ConfirmPlan)
	if provided == "" && options.Interactive && strings.TrimSpace(options.Format) != "json" && options.ConfirmationReader != nil {
		if options.ConfirmationWriter != nil {
			fmt.Fprintf(options.ConfirmationWriter, "confirm exact plan digest %s: ", expected)
		}
		line, _ := bufio.NewReader(options.ConfirmationReader).ReadString('\n')
		provided = strings.TrimSpace(line)
	}
	if provided != expected {
		return fmt.Errorf("exact plan confirmation is required; expected retained plan digest %s", expected)
	}
	return nil
}

func (operations projectDeployOperations) currentTime() time.Time {
	if operations.now != nil {
		return operations.now().UTC()
	}
	return time.Now().UTC()
}

func validateDeploymentPlanForBuild(plan projectcli.DeliveryPlanResult, now time.Time) error {
	if strings.TrimSpace(plan.Status) != "planned" {
		return &projectcli.DeliveryError{Operation: "build", Kind: "conflict", Code: "DELIVERY_PLAN_EXPIRED", Status: http.StatusConflict, Detail: "the retained delivery plan is not active; create and review a fresh plan"}
	}
	expiresAt, err := time.Parse(time.RFC3339, strings.TrimSpace(plan.ExpiresAt))
	if err != nil || expiresAt.IsZero() {
		return &projectcli.DeliveryError{Operation: "build", Kind: "conflict", Code: "DELIVERY_PLAN_EXPIRY_UNVERIFIABLE", Status: http.StatusConflict, Detail: "the retained delivery plan has no verifiable expiry; create and review a fresh plan"}
	}
	if !now.Before(expiresAt) {
		return &projectcli.DeliveryError{Operation: "build", Kind: "conflict", Code: "DELIVERY_PLAN_EXPIRED", Status: http.StatusConflict, Detail: "the retained delivery plan has expired; create and review a fresh plan"}
	}
	return nil
}

func (operations projectDeployOperations) reconcileAndReport(ctx context.Context, options projectcli.DeployOptions, descriptor projectcli.DeploymentOperationDescriptor, out io.Writer) error {
	reconciled, err := operations.reconcileDescriptor(ctx, options, descriptor)
	if err != nil {
		return operations.markErrorAndReport(descriptor, classifyDeploymentError(err), "publication evidence could not be reconciled", err, out, options.Format)
	}
	return operations.reportOperation(reconciled, options.Format, out, nextActionForOutcome(reconciled.Outcome))
}

func (operations projectDeployOperations) reconcileDescriptor(ctx context.Context, options projectcli.DeployOptions, descriptor projectcli.DeploymentOperationDescriptor) (projectcli.DeploymentOperationDescriptor, error) {
	transport, err := operations.client.Transport(ctx, options.Credentials)
	if err != nil {
		return descriptor, err
	}
	response, err := deploymentgen.NewGenClient(transport).GetDeliveryPublicationEvidence(ctx, deploymentgen.GenGetDeliveryPublicationEvidenceClientRequest{Project: descriptor.ProjectID, Publication: descriptor.PublicationID})
	if err != nil {
		return descriptor, mapDeliveryCLIError("read delivery publication evidence", err)
	}
	evidence := response.Body
	if evidence.Id != descriptor.PublicationID || evidence.ProjectId != descriptor.ProjectID || evidence.Environment != descriptor.Environment || (descriptor.TargetID != "" && evidence.TargetId != descriptor.TargetID) || (descriptor.CandidateID != "" && evidence.CandidateId != descriptor.CandidateID) || (descriptor.PlanID != "" && evidence.PlanId != descriptor.PlanID) || (descriptor.PlanDigest != "" && evidence.PlanDigest != descriptor.PlanDigest) {
		return descriptor, fmt.Errorf("publication evidence does not match retained operation: publication identity mismatch")
	}
	descriptor.TargetID, descriptor.GenerationID, descriptor.PublicationStatus = evidence.TargetId, evidence.GenerationId, string(evidence.Status)
	descriptor.PublicationTargetRevision = evidence.ResultTargetRevision
	switch evidence.Status {
	case deploymentgen.DeliveryPublicationStatusCommitted:
		descriptor.Outcome = projectcli.DeploymentOperationActive
	case deploymentgen.DeliveryPublicationStatusPending:
		descriptor.Outcome = projectcli.DeploymentOperationPendingApproval
	case deploymentgen.DeliveryPublicationStatusRejected:
		descriptor.Outcome = projectcli.DeploymentOperationFailure
		descriptor.FailureDetail = optionalString(evidence.Reason)
	case deploymentgen.DeliveryPublicationStatusIndeterminate:
		descriptor.Outcome = projectcli.DeploymentOperationIndeterminate
	default:
		descriptor.Outcome = projectcli.DeploymentOperationIndeterminate
	}
	if descriptor.Outcome == projectcli.DeploymentOperationActive && descriptor.GenerationID == "" {
		descriptor.Outcome = projectcli.DeploymentOperationIndeterminate
		descriptor.FailureDetail = "target reported committed publication without generation identity"
	}
	if descriptor.StatusURL == "" && descriptor.CandidateID != "" {
		statusURL, statusErr := deploymentCandidateReviewURL(descriptor.TargetOrigin, descriptor.CandidateID)
		if statusErr != nil {
			return descriptor, statusErr
		}
		descriptor.StatusURL = statusURL
	}
	if err := operations.operations.Save(descriptor); err != nil {
		return descriptor, err
	}
	return descriptor, nil
}

func deploymentCandidateReviewURL(origin, candidateID string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", fmt.Errorf("target origin is not a canonical HTTP origin")
	}
	candidateID = strings.TrimSpace(candidateID)
	if candidateID == "" {
		return "", fmt.Errorf("candidate identity is required")
	}
	parsed.Path = ""
	return strings.TrimRight(parsed.String(), "/") + "/candidates/" + url.PathEscape(candidateID) + "/review", nil
}

func validateDeploymentPublishResult(result projectcli.PublishResult, checkpoint projectcli.CandidateCheckpoint) error {
	for _, identity := range []struct {
		name, expected, actual string
	}{
		{"candidate", strings.TrimSpace(checkpoint.CandidateID), strings.TrimSpace(result.CandidateID)},
		{"plan", strings.TrimSpace(checkpoint.PlanID), strings.TrimSpace(result.PlanID)},
		{"plan digest", strings.TrimSpace(checkpoint.PlanDigest), strings.TrimSpace(result.PlanDigest)},
	} {
		if identity.expected != "" && identity.expected != identity.actual {
			return fmt.Errorf("publication %s identity %q does not match retained checkpoint %q", identity.name, identity.actual, identity.expected)
		}
	}
	return nil
}

func classifyDeploymentError(err error) projectcli.DeploymentOperationOutcome {
	var deliveryErr *projectcli.DeliveryError
	if errors.As(err, &deliveryErr) {
		if deliveryErr.Status == http.StatusRequestTimeout || deliveryErr.Status == http.StatusTooEarly || deliveryErr.Status == http.StatusTooManyRequests {
			// These responses do not establish that a target mutation failed. A
			// timeout can lose an acknowledgement, and retry throttling/Too Early
			// is explicitly transient; retain the operation for exact replay.
			return projectcli.DeploymentOperationIndeterminate
		}
		switch strings.ToLower(strings.TrimSpace(deliveryErr.Kind)) {
		case "approval":
			return projectcli.DeploymentOperationPendingApproval
		case "conflict", "forbidden", "authentication":
			return projectcli.DeploymentOperationFailure
		}
		if deliveryErr.Status >= 400 && deliveryErr.Status < 500 {
			return projectcli.DeploymentOperationFailure
		}
	}
	return projectcli.DeploymentOperationIndeterminate
}

func (operations projectDeployOperations) markAndReport(descriptor projectcli.DeploymentOperationDescriptor, outcome projectcli.DeploymentOperationOutcome, nextAction, detail string, out io.Writer, format string) error {
	descriptor.Outcome, descriptor.FailureDetail = outcome, safetext.BoundedSummary(detail, 4096)
	if err := operations.operations.Save(descriptor); err != nil {
		return err
	}
	return operations.reportOperation(descriptor, format, out, nextAction)
}

func (operations projectDeployOperations) markErrorAndReport(descriptor projectcli.DeploymentOperationDescriptor, outcome projectcli.DeploymentOperationOutcome, nextAction string, cause error, out io.Writer, format string) error {
	detail, code := "", ""
	if cause != nil {
		detail = cause.Error()
		var deliveryErr *projectcli.DeliveryError
		if errors.As(cause, &deliveryErr) {
			code, detail = deliveryErr.Code, deliveryErr.Detail
		}
	}
	descriptor.FailureCode, descriptor.FailureDetail = code, detail
	return operations.markAndReport(descriptor, outcome, nextAction, detail, out, format)
}

func (operations projectDeployOperations) reportOperation(descriptor projectcli.DeploymentOperationDescriptor, format string, out io.Writer, nextAction string) error {
	result := descriptor.DeploymentOperationResult()
	result.NextAction = nextAction
	if strings.TrimSpace(format) == "" {
		format = "text"
	}
	if err := projectcli.WriteDeploymentOperationResult(out, format, result); err != nil {
		return err
	}
	if result.Outcome == projectcli.DeploymentOperationActive {
		return nil
	}
	return &projectcli.DeploymentStatusError{Result: result}
}

func nextActionForOutcome(outcome projectcli.DeploymentOperationOutcome) string {
	switch outcome {
	case projectcli.DeploymentOperationActive:
		return "activation committed"
	case projectcli.DeploymentOperationPendingApproval:
		return "obtain target approval, then resume this operation"
	case projectcli.DeploymentOperationFailure:
		return "inspect target failure and start an explicit new operation if needed"
	case projectcli.DeploymentOperationIndeterminate:
		return "reconcile target publication evidence before retrying"
	default:
		return "resume this operation"
	}
}

func selectionErrorCode(err error) string {
	switch {
	case errors.Is(err, projectcli.ErrDeploymentOperationNotFound):
		return "OPERATION_NOT_FOUND"
	case errors.Is(err, projectcli.ErrDeploymentOperationTargetMismatch):
		return "TARGET_MISMATCH"
	default:
		return "SELECTION_FAILED"
	}
}

func deploymentSelectionFailure(options projectcli.DeployOptions, out io.Writer, code, handle, detail string) error {
	format := options.Format
	if strings.TrimSpace(format) == "" {
		format = "text"
	}
	detail = safetext.BoundedSummary(detail, 4096)
	if err := projectcli.WriteDeploymentSelectionError(out, format, code, handle, detail); err != nil {
		return err
	}
	return &projectcli.DeploymentSelectionError{SchemaVersion: 1, Code: code, Handle: strings.TrimSpace(handle), Detail: strings.TrimSpace(detail)}
}

func (operations projectDeployOperations) captureOperationSource(ctx context.Context, sourceRoot, projectID, candidateKey string) (projectdevloop.Snapshot, error) {
	if operations.sourceCapture != nil {
		return operations.sourceCapture(ctx, sourceRoot, projectID, candidateKey)
	}
	boundProjectID, err := projectgraph.NewResourceID(strings.TrimSpace(projectID))
	if err != nil {
		return projectdevloop.Snapshot{}, fmt.Errorf("target-bound Project identity is invalid: %w", err)
	}
	return (projectdevloop.FilesystemBuilder{
		SourceRoot:   sourceRoot,
		ProjectID:    boundProjectID,
		CandidateKey: candidateKey,
	}).Build(ctx)
}

func (operations projectDeployOperations) operationSourceSnapshot(_ context.Context, descriptor projectcli.DeploymentOperationDescriptor) (*projectdevloop.Snapshot, error) {
	if strings.TrimSpace(descriptor.SourceSnapshotProjectID) == "" || len(descriptor.SourceArtifacts) == 0 {
		return nil, fmt.Errorf("operation %s has no portable source snapshot", descriptor.Handle)
	}
	projectID, err := projectgraph.NewResourceID(descriptor.SourceSnapshotProjectID)
	if err != nil {
		return nil, fmt.Errorf("retained source Project identity is invalid: %w", err)
	}
	artifacts := make([]projectdevloop.Artifact, len(descriptor.SourceArtifacts))
	for index, artifact := range descriptor.SourceArtifacts {
		artifacts[index] = projectdevloop.Artifact{
			Path: artifact.Path, Digest: artifact.Digest, SizeBytes: artifact.SizeBytes,
			Content: append([]byte(nil), artifact.Content...),
		}
	}
	return &projectdevloop.Snapshot{
		ProjectID: projectID, Digest: descriptor.SourceDigest,
		GraphDigest: descriptor.SourceSnapshotGraphDigest, Artifacts: artifacts,
		SourceRevision: deploymentSourceRevision(descriptor), CandidateKey: descriptor.SourceSnapshotRef,
	}, nil
}

func retainOperationSnapshot(descriptor *projectcli.DeploymentOperationDescriptor, snapshot projectdevloop.Snapshot) {
	descriptor.SourceDigest = snapshot.Digest
	descriptor.SourceSnapshotProjectID = snapshot.ProjectID.String()
	descriptor.SourceSnapshotGraphDigest = snapshot.GraphDigest
	descriptor.SourceArtifacts = make([]projectcli.DeploymentSourceArtifact, len(snapshot.Artifacts))
	for index, artifact := range snapshot.Artifacts {
		descriptor.SourceArtifacts[index] = projectcli.DeploymentSourceArtifact{
			Path: artifact.Path, Digest: artifact.Digest, SizeBytes: artifact.SizeBytes,
			Content: append([]byte(nil), artifact.Content...),
		}
	}
	if snapshot.SourceRevision != nil {
		descriptor.SourceRevision = snapshot.SourceRevision.Revision
		descriptor.SourceRepository = snapshot.SourceRevision.Repository
		descriptor.SourceRef = snapshot.SourceRevision.Ref
		descriptor.SourceChangeID = snapshot.SourceRevision.ChangeID
	}
}

func deploymentSourceRevision(descriptor projectcli.DeploymentOperationDescriptor) *projectdevloop.SourceRevision {
	if descriptor.SourceRevision == "" && descriptor.SourceRepository == "" && descriptor.SourceRef == "" && descriptor.SourceChangeID == "" {
		return nil
	}
	return &projectdevloop.SourceRevision{Revision: descriptor.SourceRevision, Repository: descriptor.SourceRepository, Ref: descriptor.SourceRef, ChangeID: descriptor.SourceChangeID}
}
