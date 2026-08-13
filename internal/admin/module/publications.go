package module

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/admin/ui"
	uisignals "github.com/flidai/leapview/internal/admin/ui/signals"
	"github.com/flidai/leapview/internal/dashboard/publication"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

func (m *Module) mutatePublication(r *http.Request, command uisignals.AdminPublicationCommand) error {
	if m == nil || m.publications == nil || !m.publications.PublicationsConfigured() {
		return publication.ErrNotFound
	}
	principal, ok := m.principal(r)
	if !ok {
		return publication.ErrConflict
	}
	if !principal.DevBypass {
		if credential, ok := m.credential(r); ok && !access.TokenAllows(credential.Token, command.WorkspaceID, access.PrivilegeManagePublications) {
			return publication.ErrNotFound
		}
		if m.access != nil {
			decision, err := m.access.Authorize(r.Context(), principal.ID, access.PrivilegeManagePublications, access.WorkspaceObject(command.WorkspaceID))
			if err != nil {
				return err
			}
			if !decision.Allowed {
				return publication.ErrNotFound
			}
		}
	}
	binding, ok := m.publicationCommands[strings.TrimSpace(command.Action)]
	if !ok {
		return publication.ErrConflict
	}
	if err := uicommand.VerifyClaim(uicommand.OperationClaims(r), binding.OperationID()); err != nil {
		return err
	}
	requestID := firstAdminPublicationHeader(r, "X-Request-Id", "X-Request-ID")
	if requestID == "" {
		requestID = apitransport.NewRequestID()
		r.Header.Set("X-Request-ID", requestID)
	}
	correlationID := firstAdminPublicationHeader(r, "X-Correlation-Id", "X-Correlation-ID")
	if correlationID == "" {
		correlationID = requestID
	}
	idempotencyKey := firstAdminPublicationHeader(r, "Idempotency-Key")
	if idempotencyKey == "" {
		// Browser commands identify one mutation with the request ID. This keeps
		// the UI retry identity stable without requiring a separate signal field.
		idempotencyKey = requestID
	}
	invocation := publication.CommandInvocation{
		OperationID:    binding.OperationID(),
		Surface:        "ui",
		IdempotencyKey: idempotencyKey,
		RequestID:      requestID,
		CorrelationID:  correlationID,
	}
	_, err := m.publications.MutatePublicationWithInvocation(r.Context(), command.WorkspaceID, command.Publication, principal.ID, publication.Action(command.Action), invocation)
	return err
}

func firstAdminPublicationHeader(r *http.Request, names ...string) string {
	if r == nil {
		return ""
	}
	for _, name := range names {
		if value := strings.TrimSpace(r.Header.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func (m *Module) adminPublications(r *http.Request) ([]ui.AdminPublication, bool, error) {
	if m == nil || m.publications == nil || !m.publications.PublicationsConfigured() {
		return nil, false, nil
	}
	principal, ok := m.principal(r)
	if !ok {
		return nil, false, nil
	}
	rows, err := m.publications.AllPublications(r.Context())
	if err != nil {
		return nil, false, err
	}
	var credential *access.APICredential
	if resolved, ok := m.credential(r); ok {
		credential = &resolved
	}
	canManage := principal.DevBypass || m.access == nil
	if !canManage && m.authorizeAnyWorkspace != nil {
		canManage, err = m.authorizeAnyWorkspace(r.Context(), principal.ID, credential, access.PrivilegeManagePublications)
		if err != nil {
			return nil, false, err
		}
	}
	out := make([]ui.AdminPublication, 0, len(rows))
	for _, row := range rows {
		allowed := principal.DevBypass || m.access == nil
		if !allowed {
			if credential != nil && !access.TokenAllows(credential.Token, row.WorkspaceID, access.PrivilegeManagePublications) {
				continue
			}
			decision, err := m.access.Authorize(r.Context(), principal.ID, access.PrivilegeManagePublications, access.WorkspaceObject(row.WorkspaceID))
			if err != nil {
				return nil, false, err
			}
			allowed = decision.Allowed
		}
		if !allowed {
			continue
		}
		dto := m.publications.PublicationDTO(row)
		events, err := m.publications.PublicationEvents(r.Context(), row.ID)
		if err != nil {
			return nil, false, err
		}
		history := make([]string, 0, len(events))
		for _, event := range events {
			actor := event.ActorID
			if actor == "" {
				actor = "system"
			}
			history = append(history, fmt.Sprintf("%s · %s · %s", event.CreatedAt, event.Type, actor))
		}
		out = append(out, ui.AdminPublication{
			WorkspaceID: row.WorkspaceID, Name: row.Name, Dashboard: row.Dashboard, DefaultPage: row.DefaultPage,
			Status: string(row.Status()), Origins: append([]string(nil), row.AllowedOrigins...), Generation: row.ServingStateID,
			PublicURL: dto.PublicURL, EmbedURL: dto.EmbedURL, IFrameSnippet: dto.IFrameSnippet,
			ConfiguredAt: row.ConfiguredAt, SuspendedAt: row.SuspendedAt, DisabledAt: row.DisabledAt, RotatedAt: row.RotatedAt,
			History: history,
		})
	}
	return out, canManage, nil
}

func (m *Module) principal(r *http.Request) (adminPrincipal, bool) {
	if m.currentPrincipal == nil {
		return adminPrincipal{}, false
	}
	principal, ok := m.currentPrincipal(r)
	return adminPrincipal{ID: principal.ID, DevBypass: principal.DevBypass}, ok
}

type adminPrincipal struct {
	ID        string
	DevBypass bool
}

func (m *Module) credential(r *http.Request) (access.APICredential, bool) {
	if m.currentCredential == nil {
		return access.APICredential{}, false
	}
	return m.currentCredential(r)
}
