package composectl

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
)

const qualificationProjectID = "project:leapview-evaluation"

// qualificationDeliveryPersistenceEvidence is the canonical, read-only
// delivery identity captured around a controller-side application upgrade.
type qualificationDeliveryPersistenceEvidence struct {
	CandidateID           string
	GenerationID          string
	SnapshotSealID        string
	PlanID                string
	PlanDigest            string
	TargetID              string
	PhysicalPoolID        string
	CatalogDigest         string
	CompatibilityDigest   string
	ServingArtifactID     string
	ServingArtifactDigest string
	ServingStateID        string
}

// runQualificationApplicationUpgrade recreates the immutable application and
// proves that its native delivery identity and credential boundary survived.
func (c *Controller) runQualificationApplicationUpgrade(
	ctx context.Context,
	containerID string,
	token string,
	authoring qualificationAuthoringReport,
) (string, error) {
	if c == nil {
		return "", errors.New("controller is required")
	}
	if err := assertQualificationNativeServingCredentialBoundary(c.path(appEnvName)); err != nil {
		return "", err
	}
	before, err := c.qualificationDeliveryPersistenceEvidence(
		ctx, "http://127.0.0.1:8080", qualificationProjectID,
		authoring.Candidate, authoring.GenerationID, token,
	)
	if err != nil {
		return "", fmt.Errorf("capture delivery evidence before application upgrade: %w", err)
	}
	if authoring.SnapshotSealID == "" || before.SnapshotSealID != authoring.SnapshotSealID {
		return "", errors.New("authoring report and active snapshot seal differ")
	}
	upgradedContainerID, err := c.upgradeQualificationApplication(ctx, containerID)
	if err != nil {
		return "", err
	}
	if err := assertQualificationNativeServingCredentialBoundary(c.path(appEnvName)); err != nil {
		return "", err
	}
	after, err := c.qualificationDeliveryPersistenceEvidence(
		ctx, "http://127.0.0.1:8080", qualificationProjectID,
		authoring.Candidate, authoring.GenerationID, token,
	)
	if err != nil {
		return "", fmt.Errorf("capture delivery evidence after application upgrade: %w", err)
	}
	if before != after {
		return "", errors.New("application upgrade changed active delivery evidence")
	}
	if err := assertQualificationNativePostgresOnly(ctx, c.qualificationContainers.Existing(upgradedContainerID)); err != nil {
		return "", err
	}
	return upgradedContainerID, nil
}

func (c *Controller) qualificationDeliveryPersistenceEvidence(
	ctx context.Context,
	target, projectID, candidateID, generationID, token string,
) (qualificationDeliveryPersistenceEvidence, error) {
	if c == nil {
		return qualificationDeliveryPersistenceEvidence{}, errors.New("controller is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(target) == "" || strings.TrimSpace(projectID) == "" ||
		strings.TrimSpace(candidateID) == "" || strings.TrimSpace(generationID) == "" {
		return qualificationDeliveryPersistenceEvidence{}, errors.New("qualification delivery identity inputs are required")
	}
	if strings.TrimSpace(token) == "" {
		return qualificationDeliveryPersistenceEvidence{}, errors.New("qualification delivery identity token is required")
	}
	client := deploymentgen.NewGenClient(qualificationGeneratedTransport(
		target, token, &http.Client{Timeout: 30 * time.Second},
	))
	candidate, err := client.GetDeliveryCandidateStatus(ctx, deploymentgen.GenGetDeliveryCandidateStatusClientRequest{
		Project: projectID, Candidate: candidateID,
	})
	if err != nil {
		return qualificationDeliveryPersistenceEvidence{}, fmt.Errorf("read delivery candidate identity: %w", err)
	}
	generation, err := client.GetDeliveryGenerationStatus(ctx, deploymentgen.GenGetDeliveryGenerationStatusClientRequest{
		Project: projectID, Generation: generationID,
	})
	if err != nil {
		return qualificationDeliveryPersistenceEvidence{}, fmt.Errorf("read delivery generation identity: %w", err)
	}
	// Keep mismatch diagnostics structural and value-free. These responses carry
	// immutable IDs and digests; including their values (or the bearer token)
	// in an upgrade qualification error would turn a useful diagnosis into a
	// credential/evidence disclosure. Field paths identify the broken binding
	// while preserving that boundary.
	candidateMismatches := make([]string, 0, 3)
	if candidate.Body.Id != candidateID {
		candidateMismatches = append(candidateMismatches, "candidate.id")
	}
	if candidate.Body.Status != deploymentgen.DeliveryCandidateStatusReady {
		candidateMismatches = append(candidateMismatches, "candidate.status")
	}
	if strings.TrimSpace(candidate.Body.SealId) == "" {
		candidateMismatches = append(candidateMismatches, "candidate.sealId")
	}

	crossMismatches := make([]string, 0, 10)
	if candidate.Body.ServingStateId != generation.Body.ServingStateId {
		crossMismatches = append(crossMismatches, "candidate.servingStateId vs generation.servingStateId")
	}
	if candidate.Body.PlanId != generation.Body.PlanId {
		crossMismatches = append(crossMismatches, "candidate.planId vs generation.planId")
	}
	if candidate.Body.PlanDigest != generation.Body.PlanDigest {
		crossMismatches = append(crossMismatches, "candidate.planDigest vs generation.planDigest")
	}
	if candidate.Body.TargetId != generation.Body.TargetId {
		crossMismatches = append(crossMismatches, "candidate.targetId vs generation.targetId")
	}
	if candidate.Body.PhysicalPoolId != generation.Body.PhysicalPoolId {
		crossMismatches = append(crossMismatches, "candidate.physicalPoolId vs generation.physicalPoolId")
	}
	if candidate.Body.CatalogDigest != generation.Body.CatalogDigest {
		crossMismatches = append(crossMismatches, "candidate.catalogDigest vs generation.catalogDigest")
	}
	if candidate.Body.CompatibilityDigest != generation.Body.CompatibilityDigest {
		crossMismatches = append(crossMismatches, "candidate.compatibilityDigest vs generation.compatibilityDigest")
	}
	if candidate.Body.ServingArtifactId != generation.Body.ServingArtifactId {
		crossMismatches = append(crossMismatches, "candidate.servingArtifactId vs generation.servingArtifactId")
	}
	if candidate.Body.ServingArtifactDigest != generation.Body.ServingArtifactDigest {
		crossMismatches = append(crossMismatches, "candidate.servingArtifactDigest vs generation.servingArtifactDigest")
	}
	if generation.Body.CandidateId != candidateID {
		crossMismatches = append(crossMismatches, "generation.candidateId")
	}

	generationMismatches := make([]string, 0, 2)
	if generation.Body.Id != generationID {
		generationMismatches = append(generationMismatches, "generation.id")
	}
	if generation.Body.Status != deploymentgen.DeliveryGenerationStatusActive {
		generationMismatches = append(generationMismatches, "generation.status")
	}
	if len(candidateMismatches) > 0 || len(crossMismatches) > 0 {
		fields := append(candidateMismatches, crossMismatches...)
		// Include generation lifecycle mismatches too when another binding is
		// broken, so one qualification run reports every known bad field.
		fields = append(fields, generationMismatches...)
		return qualificationDeliveryPersistenceEvidence{}, fmt.Errorf(
			"delivery candidate and generation identity differ: %s", strings.Join(fields, ", "),
		)
	}
	if len(generationMismatches) > 0 {
		return qualificationDeliveryPersistenceEvidence{}, fmt.Errorf(
			"delivery generation is not the active generation: %s", strings.Join(generationMismatches, ", "),
		)
	}
	return qualificationDeliveryPersistenceEvidence{
		CandidateID: candidate.Body.Id, GenerationID: generation.Body.Id,
		SnapshotSealID: candidate.Body.SealId, PlanID: generation.Body.PlanId,
		PlanDigest: generation.Body.PlanDigest, TargetID: generation.Body.TargetId,
		PhysicalPoolID: generation.Body.PhysicalPoolId, CatalogDigest: generation.Body.CatalogDigest,
		CompatibilityDigest:   generation.Body.CompatibilityDigest,
		ServingArtifactID:     generation.Body.ServingArtifactId,
		ServingArtifactDigest: generation.Body.ServingArtifactDigest,
		ServingStateID:        generation.Body.ServingStateId,
	}, nil
}

func (c *Controller) upgradeQualificationApplication(ctx context.Context, previousContainerID string) (string, error) {
	if c == nil {
		return "", errors.New("controller is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	previousContainerID = strings.TrimSpace(previousContainerID)
	if previousContainerID == "" {
		return "", errors.New("previous qualification application container is required")
	}
	if _, err := c.qualificationCompose(ctx, c.root, "up", "-d", "--no-deps", "--force-recreate", "leapview"); err != nil {
		return "", fmt.Errorf("recreate qualification application for upgrade: %w", err)
	}
	output, err := c.qualificationCompose(ctx, c.root, "ps", "--quiet", "leapview")
	if err != nil {
		return "", fmt.Errorf("resolve upgraded qualification application: %w", err)
	}
	containerID := strings.TrimSpace(string(output))
	if containerID == "" {
		return "", errors.New("upgraded qualification application container is missing")
	}
	if containerID == previousContainerID {
		return "", errors.New("qualification application upgrade did not recreate the container")
	}
	container := c.qualificationContainers.Existing(containerID)
	if container == nil {
		return "", errors.New("upgraded qualification application container is unavailable")
	}
	if err := waitQualificationHealthcheck(ctx, container, "http://127.0.0.1:8080/readyz", 3*time.Minute); err != nil {
		return "", fmt.Errorf("wait for qualification readiness after application upgrade: %w", err)
	}
	if err := c.waitQualificationContainerValue(ctx, containerID, "{{.State.Health.Status}}", "healthy", time.Minute); err != nil {
		return "", fmt.Errorf("wait for Docker health after application upgrade: %w", err)
	}
	return containerID, nil
}

func assertQualificationNativePostgresOnly(ctx context.Context, container qualificationContainer) error {
	if container == nil {
		return errors.New("qualification application container is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	const stateRoot = "/var/lib/leapview/home"
	paths := []string{
		stateRoot + "/leapview.db", stateRoot + "/leapview.db-wal", stateRoot + "/leapview.db-shm",
		stateRoot + "/libredash.db", stateRoot + "/libredash.db-wal", stateRoot + "/libredash.db-shm",
		stateRoot + "/ducklake/catalog.sqlite", stateRoot + "/ducklake/catalog.sqlite-wal", stateRoot + "/ducklake/catalog.sqlite-shm",
	}
	checks := []string{"set -eu"}
	for _, path := range paths {
		checks = append(checks, fmt.Sprintf("if [ -e %q ]; then echo %q; exit 1; fi", path, path))
	}
	output, err := container.Exec(ctx, nil, "sh", "-ec", strings.Join(checks, "\n"))
	if err == nil {
		return nil
	}
	if message := strings.TrimSpace(string(redactQualificationBytes(output))); message != "" {
		return fmt.Errorf("production created a forbidden SQLite authority file: %s: %w", message, err)
	}
	return qualificationContainerOperationError(ctx, container, "verify PostgreSQL-only production state", err)
}
