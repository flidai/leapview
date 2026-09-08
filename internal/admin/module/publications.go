package module

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/admin/ui"
	uisignals "github.com/flidai/leapview/internal/admin/ui/signals"
	"github.com/flidai/leapview/internal/dashboard/publication"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	"github.com/flidai/leapview/pkg/pagestream"
	"github.com/google/uuid"
)

// authorizePublicationReplay re-checks the exact project capability required
// by a completed browser publication command. The durable protocol may return
// a captured response without dispatching PublicationCommand, so this check
// must run against the current principal and credential on every replay.
func (m *Module) authorizePublicationReplay(r *http.Request) bool {
	if m == nil || r == nil {
		return false
	}
	principal, ok := m.principal(r)
	if !ok {
		return false
	}
	var signals struct {
		AdminPublicationCommand uisignals.AdminPublicationCommand `json:"adminPublicationCommand"`
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return false
	}
	// The protocol may invoke replay authorization more than once while a
	// concurrent first request is still completing. Preserve the body for the
	// next authorization pass (and for any downstream handler on first use).
	r.Body = io.NopCloser(bytes.NewReader(body))
	probe := r.Clone(r.Context())
	probe.Body = io.NopCloser(bytes.NewReader(body))
	if err := pagestream.ReadSignals(probe, &signals); err != nil {
		return false
	}
	command := signals.AdminPublicationCommand
	if strings.TrimSpace(command.ProjectID) == "" || strings.TrimSpace(command.Publication) == "" || strings.TrimSpace(command.Action) == "" || !adminPublicationRevisionMatches(r, command.ExpectedRevision) {
		return false
	}
	if principal.DevBypass {
		return true
	}
	credential, hasCredential := m.credential(r)
	allowed, err := m.capabilityAllowed(r, principal.ID, command.ProjectID, access.CapabilityResourcePublish, credential, hasCredential)
	return err == nil && allowed
}

func (m *Module) mutatePublication(r *http.Request, command uisignals.AdminPublicationCommand) error {
	if m == nil || m.publications == nil || !m.publications.PublicationsConfigured() {
		return publication.ErrNotFound
	}
	principal, ok := m.principal(r)
	if !ok {
		return publication.ErrConflict
	}
	if !principal.DevBypass {
		credential, hasCredential := m.credential(r)
		allowed, err := m.capabilityAllowed(r, principal.ID, command.ProjectID, access.CapabilityResourcePublish, credential, hasCredential)
		if err != nil {
			return err
		}
		if !allowed {
			return publication.ErrNotFound
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
		generated, err := uuid.NewV7()
		if err != nil {
			return err
		}
		requestID = generated.String()
		r.Header.Set("X-Request-ID", requestID)
	}
	correlationID := firstAdminPublicationHeader(r, "X-Correlation-Id", "X-Correlation-ID")
	if correlationID == "" {
		correlationID = requestID
	}
	idempotencyKey := firstAdminPublicationHeader(r, "Idempotency-Key")
	if idempotencyKey == "" {
		return fmt.Errorf("%w: missing Idempotency-Key", publication.ErrConflict)
	}
	parsedID, err := uuid.Parse(idempotencyKey)
	if err != nil || parsedID.String() != idempotencyKey || parsedID.Version() != 7 {
		return fmt.Errorf("%w: Idempotency-Key must be canonical UUIDv7", publication.ErrConflict)
	}
	if !adminPublicationRevisionMatches(r, command.ExpectedRevision) {
		return fmt.Errorf("%w: If-Match does not match expected publication revision", publication.ErrConflict)
	}
	invocation := publication.CommandInvocation{
		OperationID:      binding.OperationID(),
		Surface:          "ui",
		IdempotencyKey:   idempotencyKey,
		RequestID:        requestID,
		CorrelationID:    correlationID,
		ExpectedRevision: command.ExpectedRevision,
	}
	_, err = m.publications.MutatePublicationWithInvocation(r.Context(), command.ProjectID, command.Publication, principal.ID, publication.Action(command.Action), invocation)
	return err
}

func adminPublicationRevisionMatches(r *http.Request, expected int64) bool {
	return r != nil && expected > 0 && strings.TrimSpace(r.Header.Get("If-Match")) == fmt.Sprintf("\"%d\"", expected)
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
	canManage := principal.DevBypass || (m.access == nil && m.currentEffectiveCapabilities == nil)
	if !canManage && m.authorizeAnyProject != nil {
		canManage, err = m.authorizeAnyProject(r.Context(), principal.ID, credential, access.CapabilityResourcePublish)
		if err != nil {
			return nil, false, err
		}
	}
	out := make([]ui.AdminPublication, 0, len(rows))
	for _, row := range rows {
		allowed := principal.DevBypass || (m.access == nil && m.currentEffectiveCapabilities == nil)
		if !allowed {
			credentialValue := access.APICredential{}
			if credential != nil {
				credentialValue = *credential
			}
			allowed, err = m.capabilityAllowed(r, principal.ID, row.ProjectID.String(), access.CapabilityResourcePublish, credentialValue, credential != nil)
			if err != nil {
				return nil, false, err
			}
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
			ProjectID: row.ProjectID.String(), Name: row.Name, Dashboard: row.Dashboard, DefaultPage: row.DefaultPage,
			Status: string(row.Status()), Revision: row.Revision, Origins: append([]string(nil), row.AllowedOrigins...), Generation: row.ServingStateID,
			PublicURL: dto.PublicURL, EmbedURL: dto.EmbedURL, IFrameSnippet: dto.IFrameSnippet,
			ConfiguredAt: row.ConfiguredAt, SuspendedAt: row.SuspendedAt, DisabledAt: row.DisabledAt, RotatedAt: row.RotatedAt,
			History: history,
		})
	}
	return out, canManage, nil
}

func (m *Module) capabilityAllowed(r *http.Request, principalID, projectID string, required access.Capability, credential access.APICredential, hasCredential bool) (bool, error) {
	if m.currentEffectiveCapabilities == nil {
		return m.access == nil && !hasCredential, nil
	}
	effective, err := m.currentEffectiveCapabilities(r.Context(), principalID)
	if err != nil {
		return false, err
	}
	effectiveHas := false
	for _, capability := range effective {
		if capability == required {
			effectiveHas = true
			break
		}
	}
	if !effectiveHas {
		return false, nil
	}
	if !hasCredential {
		return true, nil
	}
	if credential.Authoring != nil {
		if credential.Authoring.Scope.ProjectID.String() != strings.TrimSpace(projectID) {
			return false, nil
		}
		for _, capability := range credential.Authoring.Scope.Capabilities {
			if capability == required {
				return true, nil
			}
		}
		return false, nil
	}
	// A nil token capability list is dynamic and inherits the current snapshot;
	// an explicit empty list denies every capability.
	if credential.Token.Capabilities == nil {
		return true, nil
	}
	for _, capability := range credential.Token.Capabilities {
		if capability == required {
			return true, nil
		}
	}
	return false, nil
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
