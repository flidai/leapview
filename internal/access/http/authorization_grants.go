package http

import (
	"context"
	"errors"
	"fmt"
	stdhttp "net/http"
	"net/url"
	"strconv"
	"strings"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	platformgen "github.com/flidai/leapview/internal/platform/http/api/gen"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

func (h Handler) ListGrants(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	projectID, _, err := h.controlContext(r)
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusServiceUnavailable)
		return
	}
	kindFilter, idFilter := strings.TrimSpace(r.URL.Query().Get("resourceKind")), strings.TrimSpace(r.URL.Query().Get("resourceId"))
	if (kindFilter == "") != (idFilter == "") {
		writeJSONError(w, errors.New("resourceKind and resourceId must be supplied together"), stdhttp.StatusBadRequest)
		return
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("includeInherited")); raw != "" {
		includeInherited, parseErr := strconv.ParseBool(raw)
		if parseErr != nil {
			writeJSONError(w, errors.New("includeInherited must be a boolean"), stdhttp.StatusBadRequest)
			return
		}
		if includeInherited {
			writeJSONError(w, errors.New("inherited grants are represented by effective-capabilities, not grant records"), stdhttp.StatusBadRequest)
			return
		}
	}
	rows, err := h.Control.ListControlGrants(r.Context(), h.InstanceID, projectID)
	if err != nil {
		writeControlError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	items := make([]accessgen.GrantResponse, 0, len(rows))
	for _, row := range rows {
		if kindFilter != "" && (string(row.Resource.Kind()) != kindFilter || row.Resource.ID().String() != idFilter) {
			continue
		}
		items = append(items, controlGrantDTO(row))
	}
	_ = writePagedJSON(w, r, items)
}

func (h Handler) CreateGrant(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if strings.TrimSpace(r.Header.Get("Idempotency-Key")) == "" {
		writeJSONError(w, errors.New("Idempotency-Key header is required"), stdhttp.StatusBadRequest)
		return
	}
	projectID, project, err := h.controlContext(r)
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusServiceUnavailable)
		return
	}
	principal, ok := h.currentPrincipal(r)
	if !ok {
		writeJSONError(w, errUnauthorized, stdhttp.StatusUnauthorized)
		return
	}
	input, err := decodeControlGrantRequest(r)
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	input.InstanceID, input.ProjectID, input.ActorID = h.InstanceID, projectID, principal.ID
	input.RequestID, input.CorrelationID = requestIDFromRequest(r), correlationIDFromRequest(r)
	var row access.ControlGrant
	err = h.executeControlCommand(r, accessgen.GenCommandOperationCreateGrant(), "grant.created", "", func(ctx context.Context) error {
		var mutationErr error
		row, mutationErr = h.Control.CreateGrant(ctx, input, project)
		return mutationErr
	})
	if err != nil {
		writeControlError(w, err, stdhttp.StatusBadRequest)
		return
	}
	w.Header().Set("ETag", controlRevisionETag(row.Revision))
	w.Header().Set("Location", strings.TrimRight(r.URL.Path, "/")+"/"+url.PathEscape(row.ID))
	writeJSON(w, stdhttp.StatusCreated, controlGrantDTO(row))
}

func (h Handler) GetGrant(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if _, _, err := h.controlContext(r); err != nil {
		writeJSONError(w, err, stdhttp.StatusServiceUnavailable)
		return
	}
	row, err := h.Control.ControlGrant(r.Context(), h.InstanceID, chi.URLParam(r, "grant"))
	if err != nil {
		writeControlError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	w.Header().Set("ETag", controlRevisionETag(row.Revision))
	writeJSON(w, stdhttp.StatusOK, controlGrantDTO(row))
}

func (h Handler) UpdateGrant(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	projectID, project, err := h.controlContext(r)
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusServiceUnavailable)
		return
	}
	principal, ok := h.currentPrincipal(r)
	if !ok {
		writeJSONError(w, errUnauthorized, stdhttp.StatusUnauthorized)
		return
	}
	grantID := chi.URLParam(r, "grant")
	current, err := h.Control.ControlGrant(r.Context(), h.InstanceID, grantID)
	if err != nil {
		writeControlError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	if err := checkIfMatch(r.Header.Get("If-Match"), controlRevisionETag(current.Revision)); err != nil {
		writeAuditedMutationError(w, r, accessgen.GenCommandOperationUpdateGrant(), err, stdhttp.StatusPreconditionFailed)
		return
	}
	input, err := decodeControlGrantRequest(r)
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusUnprocessableEntity)
		return
	}
	input.ID, input.InstanceID, input.ProjectID = grantID, h.InstanceID, projectID
	input.ExpectedRevision, input.ActorID = current.Revision, principal.ID
	input.RequestID, input.CorrelationID, input.Name = requestIDFromRequest(r), correlationIDFromRequest(r), current.Name
	var row access.ControlGrant
	err = h.executeControlCommand(r, accessgen.GenCommandOperationUpdateGrant(), "grant.updated", controlRevisionETag(current.Revision), func(ctx context.Context) error {
		var mutationErr error
		row, mutationErr = h.Control.UpdateGrant(ctx, input, project)
		return mutationErr
	})
	if err != nil {
		writeControlError(w, err, stdhttp.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("ETag", controlRevisionETag(row.Revision))
	writeJSON(w, stdhttp.StatusOK, controlGrantDTO(row))
}

func (h Handler) DeleteGrant(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if _, _, err := h.controlContext(r); err != nil {
		writeJSONError(w, err, stdhttp.StatusServiceUnavailable)
		return
	}
	principal, ok := h.currentPrincipal(r)
	if !ok {
		writeJSONError(w, errUnauthorized, stdhttp.StatusUnauthorized)
		return
	}
	grantID := chi.URLParam(r, "grant")
	current, err := h.Control.ControlGrant(r.Context(), h.InstanceID, grantID)
	if err != nil {
		writeControlError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	if err := h.executeControlCommand(r, accessgen.GenCommandOperationDeleteGrant(), "grant.deleted", "", func(ctx context.Context) error {
		_, mutationErr := h.Control.RevokeGrant(ctx, h.InstanceID, grantID, current.Revision, principal.ID)
		return mutationErr
	}); err != nil {
		writeControlError(w, err, stdhttp.StatusConflict)
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}

// executeControlCommand joins the generated command guarantee to the live
// store's own mutation-and-audit transaction. Direct domain tests and legacy
// in-process callers have no generated guard and execute the same mutation
// once without manufacturing a second command framework.
func (h Handler) executeControlCommand(r *stdhttp.Request, operation accessgen.GenCommandOperationID, auditAction, currentRevision string, mutation func(context.Context) error) error {
	if _, generated := apigencommand.OperationID(r.Context()); !generated {
		return mutation(r.Context())
	}
	executor, err := apigencommand.NewExecutor(accessgen.GetAPIGenCommandRuntimeContract, nil)
	if err != nil {
		return err
	}
	if currentRevision != "" {
		if err := executor.CheckConcurrency(r.Context(), operation.APIGenOperationID(), r.Header.Get("If-Match"), currentRevision); err != nil {
			return err
		}
	}
	return executor.Execute(r.Context(), operation.APIGenOperationID(), apigencommand.Execution{
		Transactional: func(ctx context.Context, contract apigencommand.Contract) error {
			if contract.AuditAction != auditAction {
				return fmt.Errorf("generated audit action %q does not match control mutation action %q", contract.AuditAction, auditAction)
			}
			return mutation(ctx)
		},
	})
}

func (h Handler) controlContext(r *stdhttp.Request) (string, projectgraph.ProjectGraph, error) {
	if h.Control == nil || strings.TrimSpace(h.InstanceID) == "" || h.AuthorizationSnapshot == nil {
		return "", projectgraph.ProjectGraph{}, errors.New("live access control authority is unavailable")
	}
	snapshot, err := h.AuthorizationSnapshot(r.Context())
	if err != nil {
		return "", projectgraph.ProjectGraph{}, err
	}
	if err := snapshot.ValidateBound(); err != nil {
		return "", projectgraph.ProjectGraph{}, err
	}
	return snapshot.Identity().ProjectID.String(), snapshot.Project(), nil
}

func decodeControlGrantRequest(r *stdhttp.Request) (access.ControlGrantInput, error) {
	var body accessgen.GrantRequest
	if err := decodeStrictJSON(r, &body); err != nil {
		return access.ControlGrantInput{}, err
	}
	resource, _, err := authorizationResource(string(body.ResourceKind), body.ResourceId)
	if err != nil {
		return access.ControlGrantInput{}, err
	}
	subject, err := access.NewSubjectRef(access.SubjectKind(body.SubjectType), body.SubjectId)
	if err != nil {
		return access.ControlGrantInput{}, err
	}
	capability, err := access.ParseCapability(string(body.Capability))
	if err != nil {
		return access.ControlGrantInput{}, err
	}
	return access.ControlGrantInput{Subject: subject, Resource: resource, Capability: capability}, nil
}

func controlGrantDTO(row access.ControlGrant) accessgen.GrantResponse {
	return accessgen.GrantResponse{
		Id: row.ID, ResourceId: row.Resource.ID().String(), ResourceKind: accessgen.AccessResourceKind(row.Resource.Kind()),
		SubjectType: string(row.Subject.Kind), SubjectId: row.Subject.ID, Capability: platformgen.Capability(row.Capability),
		CreatedAt: row.CreatedAt,
	}
}

func controlRevisionETag(revision int64) string {
	return `"revision-` + strconv.FormatInt(revision, 10) + `"`
}

func writeControlError(w stdhttp.ResponseWriter, err error, fallback int) {
	status := fallback
	switch {
	case errors.Is(err, access.ErrControlNotFound), errors.Is(err, access.ErrControlTargetNotFound):
		status = stdhttp.StatusNotFound
	case errors.Is(err, access.ErrControlRevisionConflict):
		status = stdhttp.StatusPreconditionFailed
	case errors.Is(err, access.ErrControlConflict), errors.Is(err, access.ErrControlIdentityConflict), errors.Is(err, access.ErrControlReferenceConflict), errors.Is(err, access.ErrControlRevoked):
		status = stdhttp.StatusConflict
	case errors.Is(err, access.ErrControlInvalidInput), errors.Is(err, access.ErrControlTargetConflict), errors.Is(err, access.ErrInvalidCanonicalGrant), errors.Is(err, access.ErrInvalidResourceRef), errors.Is(err, access.ErrInvalidCapability):
		status = fallback
	}
	writeJSONError(w, err, status)
}
