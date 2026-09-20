package module

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/deployment"
	deploymentapi "github.com/flidai/leapview/internal/deployment/api"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
)

// RollbackDeliveryOperationID exposes the generated command identity to
// browser Settings bindings without manufacturing a second operation name.
func RollbackDeliveryOperationID() string {
	return deploymentgen.GenCommandOperationRollbackDeliveryGeneration().APIGenOperationID()
}

// AdminDeliveryData reads one bounded page of native publication history and
// retained generations for the instance-bound project.
func (m *Module) AdminDeliveryData(ctx context.Context, project string) (deploymentapi.AdminDeliveryData, error) {
	if m == nil || m.nativeDeliveryReader == nil {
		return deploymentapi.AdminDeliveryData{}, errors.New("delivery reader is unavailable")
	}
	projectID, err := projectgraph.NewResourceID(project)
	if err != nil || project != strings.TrimSpace(project) {
		return deploymentapi.AdminDeliveryData{}, fmt.Errorf("%w: invalid project", deployment.ErrDeliveryInvalid)
	}
	if m.instanceID == "" || m.handlerEnvironment() == "" {
		return deploymentapi.AdminDeliveryData{}, fmt.Errorf("%w: native delivery target scope is unavailable", deployment.ErrDeliveryInvalid)
	}
	collections, ok := m.nativeDeliveryReader.(nativeDeliveryCollectionReader)
	if !ok {
		return deploymentapi.AdminDeliveryData{}, errors.New("native delivery collection reader is unavailable")
	}
	snapshot, err := m.nativeDeliveryReader.OperatorSnapshot(ctx, m.instanceID)
	if err != nil {
		return deploymentapi.AdminDeliveryData{}, nativeReadError(err)
	}
	if snapshot.ProjectID != projectID.String() || snapshot.TargetID != m.instanceID || snapshot.Environment != m.handlerEnvironment() {
		return deploymentapi.AdminDeliveryData{}, fmt.Errorf("%w: delivery target scope differs", deployment.ErrNotFound)
	}
	result := deploymentapi.AdminDeliveryData{
		Operator:            nativeOperatorResponse(snapshot),
		Publications:        []deploymentgen.DeliveryPublicationEvidenceResponse{},
		RetainedGenerations: []deploymentgen.DeliveryGenerationStatusResponse{},
	}
	const limit int32 = 50
	publicationPage, err := collections.ListPublications(ctx, projectID.String(), m.instanceID, m.handlerEnvironment(), limit, "")
	if err != nil {
		return deploymentapi.AdminDeliveryData{}, nativeReadError(err)
	}
	for _, publication := range publicationPage.Items {
		generation, err := m.nativeDeliveryReader.Generation(ctx, publication.GenerationID)
		if err != nil {
			return deploymentapi.AdminDeliveryData{}, nativeReadError(err)
		}
		plan, err := nativeReadPlan(ctx, m.nativeDeliveryReader, generation.PlanID)
		if err != nil {
			return deploymentapi.AdminDeliveryData{}, err
		}
		if err := validateNativeReadScope(m, projectID.String(), plan); err != nil || generation.TargetID != m.instanceID || publication.TargetID != m.instanceID {
			if err == nil {
				err = fmt.Errorf("%w: publication target scope differs", deployment.ErrNotFound)
			}
			return deploymentapi.AdminDeliveryData{}, err
		}
		result.Publications = append(result.Publications, nativePublicationResponse(publication, generation, plan))
	}
	generationPage, err := collections.ListRetainedGenerations(ctx, projectID.String(), m.instanceID, m.handlerEnvironment(), limit, "")
	if err != nil {
		return deploymentapi.AdminDeliveryData{}, nativeReadError(err)
	}
	for _, generation := range generationPage.Items {
		if generation.TargetID != m.instanceID {
			return deploymentapi.AdminDeliveryData{}, fmt.Errorf("%w: generation target scope differs", deployment.ErrNotFound)
		}
		plan, err := nativeReadPlan(ctx, m.nativeDeliveryReader, generation.PlanID)
		if err != nil {
			return deploymentapi.AdminDeliveryData{}, err
		}
		if err := validateNativeReadScope(m, projectID.String(), plan); err != nil {
			return deploymentapi.AdminDeliveryData{}, err
		}
		seal, err := m.nativeDeliveryReader.SnapshotSeal(ctx, generation.SnapshotSealID)
		if err != nil {
			return deploymentapi.AdminDeliveryData{}, nativeReadError(err)
		}
		active, activatedAt, retiredAt, rollbackUntil, err := m.nativeGenerationLifecycle(ctx, generation, snapshot)
		if err != nil {
			return deploymentapi.AdminDeliveryData{}, err
		}
		result.RetainedGenerations = append(result.RetainedGenerations, nativeGenerationResponse(generation, plan, seal, active, activatedAt, retiredAt, rollbackUntil))
	}
	return result, nil
}

// RollbackDeliveryGenerationContext is the typed command boundary used by
// the Settings surface. The caller must begin the generated rollback command
// on ctx before invoking this method.
func (m *Module) RollbackDeliveryGenerationContext(ctx context.Context, project, generationID, idempotencyKey, principalID string) (deploymentgen.DeliveryPublicationEvidenceResponse, error) {
	if m == nil || m.nativeDeliveryPublication == nil {
		return deploymentgen.DeliveryPublicationEvidenceResponse{}, ErrDeliveryInputUnavailable
	}
	projectID, err := projectgraph.NewResourceID(project)
	if err != nil || project != strings.TrimSpace(project) {
		return deploymentgen.DeliveryPublicationEvidenceResponse{}, fmt.Errorf("%w: project identity must be canonical", deployment.ErrDeliveryInvalid)
	}
	generation, err := uuid.Parse(generationID)
	if err != nil || generation == uuid.Nil || generation.String() != generationID {
		return deploymentgen.DeliveryPublicationEvidenceResponse{}, fmt.Errorf("%w: generation identity must be a canonical UUID", deployment.ErrDeliveryInvalid)
	}
	request := NativeDeliveryRollbackRequest{ProjectID: projectID, TargetID: m.instanceID, Environment: m.handlerEnvironment(), GenerationID: generation, PrincipalID: principalID, IdempotencyKey: idempotencyKey}
	if err := request.validate(m.handlerEnvironment()); err != nil {
		return deploymentgen.DeliveryPublicationEvidenceResponse{}, err
	}
	publication, err := m.nativeDeliveryPublication.RollbackGeneration(ctx, request)
	if err != nil {
		return deploymentgen.DeliveryPublicationEvidenceResponse{}, err
	}
	if err := publication.validate(projectID, m.instanceID, m.handlerEnvironment()); err != nil {
		return deploymentgen.DeliveryPublicationEvidenceResponse{}, err
	}
	if err := completeNativeRollbackCommand(ctx, m.nativeDeliveryPublication, publication); err != nil {
		return deploymentgen.DeliveryPublicationEvidenceResponse{}, err
	}
	return nativePublicationEvidenceResponse(publication), nil
}
