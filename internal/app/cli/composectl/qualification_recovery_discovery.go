package composectl

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
)

const qualificationRecoveryCollectionLimit int32 = 100

// qualificationFreshOperatorRecoveryEvidence is deliberately made only from
// the redacted delivery read models. It is the durable proof that recovery can
// start with an empty operator home and still discover the target's state from
// server-owned PostgreSQL projections. No credentials or local checkpoint
// contents are included.
type qualificationFreshOperatorRecoveryEvidence struct {
	SchemaVersion        int                                                 `json:"schemaVersion"`
	CheckpointFiles      []string                                            `json:"checkpointFiles"`
	Operator             deploymentgen.DeliveryOperatorSnapshotResponse      `json:"operator"`
	Plans                []deploymentgen.DeliveryPlanPreviewResponse         `json:"plans"`
	Builds               []deploymentgen.DeliveryBuildStatusResponse         `json:"builds"`
	Candidates           []deploymentgen.DeliveryCandidateStatusResponse     `json:"candidates"`
	Approvals            []deploymentgen.DeploymentApprovalResponse          `json:"approvals"`
	Publications         []deploymentgen.DeliveryPublicationEvidenceResponse `json:"publications"`
	Generations          []deploymentgen.DeliveryGenerationStatusResponse    `json:"generations"`
	SelectedGenerationID string                                              `json:"selectedGenerationId"`
	RollbackPublication  deploymentgen.DeliveryPublicationEvidenceResponse   `json:"rollbackPublication"`
	PostRollbackOperator deploymentgen.DeliveryOperatorSnapshotResponse      `json:"postRollbackOperator"`
}

// selectQualificationRollbackGeneration chooses a retained, non-active
// generation using only the server response. The generation endpoint excludes
// expired retention roots; this check additionally keeps non-reversible state
// out of the drill's rollback picker.
func selectQualificationRollbackGeneration(
	generations []deploymentgen.DeliveryGenerationStatusResponse,
	activeID string,
) (deploymentgen.DeliveryGenerationStatusResponse, error) {
	activeID = strings.TrimSpace(activeID)
	if activeID == "" {
		return deploymentgen.DeliveryGenerationStatusResponse{}, fmt.Errorf("delivery operator snapshot has no active generation")
	}
	activeSeen := false
	for _, generation := range generations {
		if generation.Id == activeID {
			if generation.Status != deploymentgen.DeliveryGenerationStatusActive {
				return deploymentgen.DeliveryGenerationStatusResponse{}, fmt.Errorf("active generation %q is reported as %q", activeID, generation.Status)
			}
			activeSeen = true
		}
	}
	if !activeSeen {
		return deploymentgen.DeliveryGenerationStatusResponse{}, fmt.Errorf("active generation %q is absent from retained generation collection", activeID)
	}
	for _, generation := range generations {
		if generation.Id == activeID || generation.Status != deploymentgen.DeliveryGenerationStatusRetired {
			continue
		}
		if (generation.RollbackClass != deploymentgen.DeliveryRollbackClassRollbackSafe &&
			generation.RollbackClass != deploymentgen.DeliveryRollbackClassServingSafe) || generation.RollbackUntil == nil {
			continue
		}
		return generation, nil
	}
	return deploymentgen.DeliveryGenerationStatusResponse{}, fmt.Errorf("retained generation collection has no rollback-safe prior generation")
}

func qualificationFreshOperatorCheckpointFiles(home string) ([]string, error) {
	entries, err := os.ReadDir(home)
	if err != nil {
		return nil, err
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		files = append(files, entry.Name())
	}
	return files, nil
}

// runQualificationFreshOperatorDeliveryRecovery is the bounded FAI-937 drill.
// It uses the same target and PostgreSQL-backed API as an operator, but starts
// with a newly-created empty home and never reads a checkpoint or admin CLI
// recovery file. The rollback call is intentional: discovery is not considered
// qualified until the selected retained generation is accepted and activated.
func runQualificationFreshOperatorDeliveryRecovery(
	ctx context.Context,
	client *http.Client,
	apiRoot string,
	options qualificationRecoveryOptions,
	deploymentCandidate QualificationCandidate,
	activePublication QualificationPublication,
	workDir string,
) (qualificationFreshOperatorRecoveryEvidence, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if client == nil {
		return qualificationFreshOperatorRecoveryEvidence{}, fmt.Errorf("fresh operator recovery requires an HTTP client")
	}
	if strings.TrimSpace(apiRoot) == "" {
		return qualificationFreshOperatorRecoveryEvidence{}, fmt.Errorf("fresh operator recovery API root is required")
	}
	if strings.TrimSpace(options.ProjectID) == "" || strings.TrimSpace(options.Target) == "" {
		return qualificationFreshOperatorRecoveryEvidence{}, fmt.Errorf("fresh operator recovery project and target are required")
	}
	if strings.TrimSpace(workDir) == "" {
		return qualificationFreshOperatorRecoveryEvidence{}, fmt.Errorf("fresh operator recovery work directory is required")
	}
	operatorHome := filepath.Join(workDir, "fresh-operator-home")
	if err := os.MkdirAll(operatorHome, 0o700); err != nil {
		return qualificationFreshOperatorRecoveryEvidence{}, err
	}
	if err := os.Chmod(operatorHome, 0o700); err != nil {
		return qualificationFreshOperatorRecoveryEvidence{}, err
	}
	checkpointFiles, err := qualificationFreshOperatorCheckpointFiles(operatorHome)
	if err != nil {
		return qualificationFreshOperatorRecoveryEvidence{}, err
	}
	if len(checkpointFiles) != 0 {
		return qualificationFreshOperatorRecoveryEvidence{}, fmt.Errorf("fresh operator home contains checkpoint files")
	}

	readClient := deploymentgen.NewGenClient(qualificationGeneratedTransport(apiRoot, options.ProjectDataToken, client))
	limit := qualificationRecoveryCollectionLimit
	params := func() deploymentgen.GenListDeliveryPlansClientParams {
		return deploymentgen.GenListDeliveryPlansClientParams{Limit: &limit}
	}
	evidence := qualificationFreshOperatorRecoveryEvidence{
		SchemaVersion:   qualificationEvidenceSchema,
		CheckpointFiles: checkpointFiles,
	}
	operator, err := readClient.GetDeliveryOperatorSnapshot(ctx, deploymentgen.GenGetDeliveryOperatorSnapshotClientRequest{Project: options.ProjectID})
	if err != nil {
		return evidence, fmt.Errorf("discover delivery operator snapshot from PostgreSQL state: %w", err)
	}
	evidence.Operator = operator.Body
	if evidence.Operator.ProjectId != options.ProjectID || evidence.Operator.TargetId == "" || evidence.Operator.ActiveGeneration == nil {
		return evidence, fmt.Errorf("delivery operator snapshot is incomplete")
	}
	activeID := strings.TrimSpace(*evidence.Operator.ActiveGeneration)

	plans, err := readClient.ListDeliveryPlans(ctx, deploymentgen.GenListDeliveryPlansClientRequest{Project: options.ProjectID, Params: params()})
	if err != nil {
		return evidence, fmt.Errorf("discover delivery plans: %w", err)
	}
	evidence.Plans = plans.Body.Items
	builds, err := readClient.ListDeliveryBuildAttempts(ctx, deploymentgen.GenListDeliveryBuildAttemptsClientRequest{Project: options.ProjectID, Params: deploymentgen.GenListDeliveryBuildAttemptsClientParams{Limit: &limit}})
	if err != nil {
		return evidence, fmt.Errorf("discover delivery build attempts: %w", err)
	}
	evidence.Builds = builds.Body.Items
	candidates, err := readClient.ListDeliveryCandidates(ctx, deploymentgen.GenListDeliveryCandidatesClientRequest{Project: options.ProjectID, Params: deploymentgen.GenListDeliveryCandidatesClientParams{Limit: &limit}})
	if err != nil {
		return evidence, fmt.Errorf("discover delivery candidates: %w", err)
	}
	evidence.Candidates = candidates.Body.Items
	approvals, err := readClient.ListDeliveryApprovalRequests(ctx, deploymentgen.GenListDeliveryApprovalRequestsClientRequest{Project: options.ProjectID, Params: deploymentgen.GenListDeliveryApprovalRequestsClientParams{Limit: &limit}})
	if err != nil {
		return evidence, fmt.Errorf("discover delivery approval requests: %w", err)
	}
	evidence.Approvals = approvals.Body.Items
	publications, err := readClient.ListDeliveryPublications(ctx, deploymentgen.GenListDeliveryPublicationsClientRequest{Project: options.ProjectID, Params: deploymentgen.GenListDeliveryPublicationsClientParams{Limit: &limit}})
	if err != nil {
		return evidence, fmt.Errorf("discover delivery publications: %w", err)
	}
	evidence.Publications = publications.Body.Items
	generations, err := readClient.ListRetainedDeliveryGenerations(ctx, deploymentgen.GenListRetainedDeliveryGenerationsClientRequest{Project: options.ProjectID, Params: deploymentgen.GenListRetainedDeliveryGenerationsClientParams{Limit: &limit}})
	if err != nil {
		return evidence, fmt.Errorf("discover retained delivery generations: %w", err)
	}
	evidence.Generations = generations.Body.Items

	prior, err := selectQualificationRollbackGeneration(evidence.Generations, activeID)
	if err != nil {
		return evidence, err
	}
	evidence.SelectedGenerationID = prior.Id
	if err := qualifyFreshOperatorEvidenceLinks(evidence, deploymentCandidate, activePublication, activeID); err != nil {
		return evidence, err
	}

	controlClient := deploymentgen.NewGenClient(qualificationGeneratedTransport(apiRoot, options.RecoveryControlToken, client))
	rollback, err := controlClient.RollbackDeliveryGeneration(ctx, deploymentgen.GenRollbackDeliveryGenerationClientRequest{
		Project: options.ProjectID, Generation: prior.Id,
		Headers: deploymentgen.GenRollbackDeliveryGenerationClientHeaders{
			IdempotencyKey: fmt.Sprintf("qualification-recovery-discovery-rollback-%d", time.Now().UnixNano()),
		},
	})
	if err != nil {
		return evidence, fmt.Errorf("select retained generation %q for rollback: %w", prior.Id, err)
	}
	evidence.RollbackPublication = rollback.Body
	if rollback.Body.GenerationId != prior.Id || rollback.Body.TargetId != evidence.Operator.TargetId {
		return evidence, fmt.Errorf("rollback response does not target selected retained generation")
	}
	pending := QualificationPublication{
		DeploymentID: rollback.Body.Id,
		CandidateID:  rollback.Body.CandidateId,
		TargetID:     rollback.Body.TargetId,
		GenerationID: rollback.Body.GenerationId,
		PlanID:       rollback.Body.PlanId,
		PlanDigest:   rollback.Body.PlanDigest,
		Status:       string(rollback.Body.Status),
	}
	committed, err := waitQualificationNativePublication(ctx, client, qualificationAuthoringOptions{Target: apiRoot, ProjectID: options.ProjectID}, options.RecoveryControlToken, pending)
	if err != nil {
		return evidence, fmt.Errorf("wait for selected rollback generation activation: %w", err)
	}
	evidence.RollbackPublication.Status = deploymentgen.DeliveryPublicationStatus(committed.Status)
	postRollback, err := waitForQualificationActiveGeneration(ctx, readClient, options.ProjectID, prior.Id)
	if err != nil {
		return evidence, err
	}
	evidence.PostRollbackOperator = postRollback
	return evidence, nil
}

func qualifyFreshOperatorEvidenceLinks(
	evidence qualificationFreshOperatorRecoveryEvidence,
	deploymentCandidate QualificationCandidate,
	activePublication QualificationPublication,
	activeID string,
) error {
	planFound := false
	for _, plan := range evidence.Plans {
		if plan.Id == deploymentCandidate.PlanID && plan.PlanDigest == deploymentCandidate.PlanDigest && plan.TargetId == deploymentCandidate.TargetID {
			planFound = true
			break
		}
	}
	if !planFound {
		return fmt.Errorf("delivery plan %q is absent from fresh operator discovery", deploymentCandidate.PlanID)
	}
	candidateFound := false
	for _, candidate := range evidence.Candidates {
		if candidate.Id == deploymentCandidate.ID && candidate.PlanId == deploymentCandidate.PlanID && candidate.Status == deploymentgen.DeliveryCandidateStatusReady {
			candidateFound = true
			break
		}
	}
	if !candidateFound {
		return fmt.Errorf("delivery candidate %q is absent or not ready in fresh operator discovery", deploymentCandidate.ID)
	}
	buildFound := false
	for _, build := range evidence.Builds {
		if build.CandidateId != nil && *build.CandidateId == deploymentCandidate.ID && build.PlanId == deploymentCandidate.PlanID && build.Status == deploymentgen.DeliveryBuildStatusSealed {
			buildFound = true
			break
		}
	}
	if !buildFound {
		return fmt.Errorf("sealed build evidence for candidate %q is absent from fresh operator discovery", deploymentCandidate.ID)
	}
	publicationFound := false
	for _, publication := range evidence.Publications {
		if publication.Id == activePublication.DeploymentID && publication.GenerationId == activeID && publication.Status == deploymentgen.DeliveryPublicationStatusCommitted {
			publicationFound = true
			break
		}
	}
	if !publicationFound {
		return fmt.Errorf("committed publication %q is absent from fresh operator discovery", activePublication.DeploymentID)
	}
	approvalFound := false
	for _, approval := range evidence.Approvals {
		if approval.DeploymentId == activePublication.DeploymentID && approval.Status == deploymentgen.DeploymentApprovalStatusApproved {
			approvalFound = true
			break
		}
	}
	if !approvalFound {
		return fmt.Errorf("approved publication request for %q is absent from fresh operator discovery", activePublication.DeploymentID)
	}
	return nil
}

func waitForQualificationActiveGeneration(
	ctx context.Context,
	client *deploymentgen.GenClient,
	projectID string,
	want string,
) (deploymentgen.DeliveryOperatorSnapshotResponse, error) {
	waitCtx, cancel := qualificationContext(ctx, 5*time.Minute)
	defer cancel()
	var snapshot deploymentgen.DeliveryOperatorSnapshotResponse
	err := qualificationWait(waitCtx, time.Second, func(pollCtx context.Context) (bool, error) {
		response, err := client.GetDeliveryOperatorSnapshot(pollCtx, deploymentgen.GenGetDeliveryOperatorSnapshotClientRequest{Project: projectID})
		if err != nil {
			if qualificationTransientDeploymentError(err) {
				return false, nil
			}
			return false, err
		}
		snapshot = response.Body
		return snapshot.ActiveGeneration != nil && *snapshot.ActiveGeneration == want, nil
	})
	if err != nil {
		return deploymentgen.DeliveryOperatorSnapshotResponse{}, fmt.Errorf("wait for PostgreSQL active generation %q after rollback: %w", want, err)
	}
	return snapshot, nil
}
