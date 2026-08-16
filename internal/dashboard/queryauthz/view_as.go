package authz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// ViewAsCapability is installed by a trusted preview handler. It identifies
// both authenticated actor and effective subject; neither is accepted from a
// query payload.
type ViewAsCapability struct {
	ActorPrincipalID   string
	SubjectPrincipalID string
	ProjectID          projectgraph.ResourceID
}

type viewAsCapabilityKey struct{}

func WithViewAsCapability(ctx context.Context, capability ViewAsCapability) context.Context {
	return context.WithValue(ctx, viewAsCapabilityKey{}, capability)
}

func viewAsCapabilityFromContext(ctx context.Context) (ViewAsCapability, bool) {
	capability, ok := ctx.Value(viewAsCapabilityKey{}).(ViewAsCapability)
	return capability, ok
}

func (m Metrics) authorizeViewAs(ctx context.Context, actor Principal, request dataquery.Query, capability ViewAsCapability) (dataquery.Query, error) {
	actorID := strings.TrimSpace(capability.ActorPrincipalID)
	subjectID := strings.TrimSpace(capability.SubjectPrincipalID)
	deny := func(cause error) (dataquery.Query, error) {
		denied := DeniedError{PrincipalID: actor.ID, Capability: access.CapabilityProjectAdmin}
		if auditErr := m.recordViewAsAudit(ctx, request, actor.ID, subjectID, capability.ProjectID, "denied", cause); auditErr != nil {
			return request, errors.Join(denied, auditErr)
		}
		return request, denied
	}
	if actorID == "" || subjectID == "" || capability.ProjectID == "" {
		return deny(errors.New("view-as capability is incomplete"))
	}
	if actor.ID == "" || actor.ID != actorID {
		return deny(errors.New("view-as actor does not match the authenticated principal"))
	}
	if subjectID == actorID {
		return deny(errors.New("view-as subject must differ from the authenticated principal"))
	}
	if request.ProjectID != capability.ProjectID {
		return deny(fmt.Errorf("view-as project %q does not match query project %q", capability.ProjectID, request.ProjectID))
	}
	snapshot, err := m.authorizationSnapshot(ctx, capability.ProjectID)
	if err != nil {
		return request, err
	}
	projectRef, err := access.NewResourceRef(capability.ProjectID, projectgraph.KindProject)
	if err != nil {
		return deny(err)
	}
	if credential, ok := m.currentCredential(ctx); ok {
		allowed, err := m.capabilityAllowed(ctx, snapshot, actorID, credential.Token, projectRef, access.CapabilityProjectAdmin)
		if err != nil || !allowed {
			if err == nil {
				err = errors.New("view-as credential lacks PROJECT_ADMIN")
			}
			return deny(err)
		}
	}
	subjects, err := m.subjects(ctx, actorID)
	if err != nil {
		return deny(err)
	}
	allowed := false
	for _, subject := range subjects {
		ok, allowErr := snapshot.Allows(subject, projectRef, access.CapabilityProjectAdmin)
		if allowErr != nil {
			return deny(allowErr)
		}
		allowed = allowed || ok
	}
	if !allowed {
		return deny(errors.New("actor lacks PROJECT_ADMIN"))
	}
	if err := m.recordViewAsAudit(ctx, request, actor.ID, subjectID, capability.ProjectID, "authorized", nil); err != nil {
		return request, err
	}
	request.PrincipalID = subjectID
	return request, nil
}

func (m Metrics) recordViewAsAudit(ctx context.Context, request dataquery.Query, actorID, subjectID string, projectID projectgraph.ResourceID, status string, cause error) error {
	metadata := map[string]any{"subjectPrincipalId": subjectID, "operation": request.Operation, "surface": request.Surface}
	if cause != nil {
		metadata["error"] = cause.Error()
	}
	bytes, _ := json.Marshal(metadata)
	snapshot, err := m.authorizationSnapshot(ctx, projectID)
	if err != nil {
		return err
	}
	resource, err := access.NewResourceRef(projectID, projectgraph.KindProject)
	if err != nil {
		return err
	}
	return m.persistCanonicalAudit(ctx, snapshot, access.CanonicalAuditEvent{
		Identity: snapshot.Identity(), PrincipalID: actorID, Action: "data_policy.view_as", Resource: resource,
		Capability: access.CapabilityProjectAdmin, Status: status, RequestID: request.RequestID,
		CorrelationID: request.CorrelationID, MetadataJSON: string(bytes),
	})
}
