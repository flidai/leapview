package http

import (
	"context"
	"errors"
	stdhttp "net/http"

	accessgen "github.com/flidai/leapview/internal/access/api/gen"
)

// APIGenDispatcher contains identity, credential, group, audit, avatar,
// authoring, and platform semantic-attribute administration operations.
// Project authorization endpoints are owned by the immutable serving-state
// authorization surface.
type APIGenDispatcher struct{ handler Handler }

// APIGenTransportErrorResponder adapts generated transport failures to the
// access API's intentionally small JSON error envelope.
type APIGenTransportErrorResponder struct{}

func (APIGenTransportErrorResponder) RespondTransportError(_ context.Context, w stdhttp.ResponseWriter, _ *stdhttp.Request, failure accessgen.GenTransportError) {
	status := failure.StatusCode
	if status <= 0 {
		status = stdhttp.StatusInternalServerError
	}
	detail := failure.PublicDetail
	if detail == "" && failure.Cause != nil {
		detail = failure.Cause.Error()
	}
	writeJSONError(w, errors.New(detail), status)
}

func NewAPIGenDispatcher(handler Handler) *APIGenDispatcher {
	return &APIGenDispatcher{handler: handler}
}

func (d *APIGenDispatcher) GetCurrentPrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	d.handler.GetCurrentPrincipal(w, r)
}
func (d *APIGenDispatcher) ListCurrentEffectiveCapabilities(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	d.handler.ListCurrentEffectiveCapabilities(w, r)
}
func (d *APIGenDispatcher) UpdateCurrentPrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request, headers accessgen.GenUpdateCurrentPrincipalHeaders) {
	if value := headers.IfMatch; value != "" {
		r.Header.Set("If-Match", value)
	}
	d.handler.UpdateCurrentPrincipal(w, r)
}
func (d *APIGenDispatcher) ChangeCurrentPassword(w stdhttp.ResponseWriter, r *stdhttp.Request, _ accessgen.GenChangeCurrentPasswordHeaders) {
	d.handler.ChangeCurrentPassword(w, r)
}
func (d *APIGenDispatcher) UpdateCurrentTheme(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	d.handler.UpdateCurrentTheme(w, r)
}
func (d *APIGenDispatcher) ListCurrentAPITokens(w stdhttp.ResponseWriter, r *stdhttp.Request, _ accessgen.GenListCurrentAPITokensParams) {
	d.handler.ListCurrentAPITokens(w, r)
}
func (d *APIGenDispatcher) CreateCurrentAPIToken(w stdhttp.ResponseWriter, r *stdhttp.Request, _ accessgen.GenCreateCurrentAPITokenHeaders) {
	d.handler.CreateCurrentAPIToken(w, r)
}
func (d *APIGenDispatcher) RevokeCurrentAPIToken(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string) {
	d.handler.RevokeCurrentAPIToken(w, r)
}
func (d *APIGenDispatcher) ListCurrentSessions(w stdhttp.ResponseWriter, r *stdhttp.Request, _ accessgen.GenListCurrentSessionsParams) {
	d.handler.ListCurrentSessions(w, r)
}
func (d *APIGenDispatcher) RevokeCurrentSession(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string) {
	d.handler.RevokeCurrentSession(w, r)
}
func (d *APIGenDispatcher) ListPrincipals(w stdhttp.ResponseWriter, r *stdhttp.Request, _ accessgen.GenListPrincipalsParams) {
	d.handler.ListPrincipals(w, r)
}
func (d *APIGenDispatcher) CreatePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request, _ accessgen.GenCreatePrincipalHeaders) {
	d.handler.CreatePrincipal(w, r)
}
func (d *APIGenDispatcher) GetPrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string) {
	d.handler.GetPrincipal(w, r)
}
func (d *APIGenDispatcher) DeletePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string) {
	d.handler.DeletePrincipal(w, r)
}
func (d *APIGenDispatcher) UpdatePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, headers accessgen.GenUpdatePrincipalHeaders) {
	if value := headers.IfMatch; value != "" {
		r.Header.Set("If-Match", value)
	}
	d.handler.UpdatePrincipal(w, r)
}
func (d *APIGenDispatcher) ResetPrincipalPassword(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenResetPrincipalPasswordHeaders) {
	d.handler.ResetPrincipalPassword(w, r)
}
func (d *APIGenDispatcher) DisablePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenDisablePrincipalHeaders) {
	d.handler.DisablePrincipal(w, r)
}
func (d *APIGenDispatcher) EnablePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenEnablePrincipalHeaders) {
	d.handler.EnablePrincipal(w, r)
}
func (d *APIGenDispatcher) ListPrincipalSessions(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenListPrincipalSessionsParams) {
	d.handler.ListPrincipalSessions(w, r)
}
func (d *APIGenDispatcher) RevokePrincipalSession(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string) {
	d.handler.RevokePrincipalSession(w, r)
}
func (d *APIGenDispatcher) ListServicePrincipals(w stdhttp.ResponseWriter, r *stdhttp.Request, _ accessgen.GenListServicePrincipalsParams) {
	d.handler.ListServicePrincipals(w, r)
}
func (d *APIGenDispatcher) CreateServicePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request, _ accessgen.GenCreateServicePrincipalHeaders) {
	d.handler.CreateServicePrincipal(w, r)
}
func (d *APIGenDispatcher) GetServicePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string) {
	d.handler.GetServicePrincipal(w, r)
}
func (d *APIGenDispatcher) UpdateServicePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenUpdateServicePrincipalHeaders) {
	d.handler.UpdateServicePrincipal(w, r)
}
func (d *APIGenDispatcher) DeleteServicePrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string) {
	d.handler.DeleteServicePrincipal(w, r)
}
func (d *APIGenDispatcher) CreateServicePrincipalSecret(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenCreateServicePrincipalSecretHeaders) {
	d.handler.CreateServicePrincipalSecret(w, r)
}
func (d *APIGenDispatcher) ListServicePrincipalSecrets(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenListServicePrincipalSecretsParams) {
	d.handler.ListServicePrincipalSecrets(w, r)
}
func (d *APIGenDispatcher) GetServicePrincipalSecret(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string) {
	d.handler.GetServicePrincipalSecret(w, r)
}
func (d *APIGenDispatcher) RevokeServicePrincipalSecret(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string) {
	d.handler.RevokeServicePrincipalSecret(w, r)
}
func (d *APIGenDispatcher) ListGroups(w stdhttp.ResponseWriter, r *stdhttp.Request, _ accessgen.GenListGroupsParams) {
	d.handler.ListGroups(w, r)
}
func (d *APIGenDispatcher) CreateGroup(w stdhttp.ResponseWriter, r *stdhttp.Request, _ accessgen.GenCreateGroupHeaders) {
	d.handler.CreateGroup(w, r)
}
func (d *APIGenDispatcher) GetGroup(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string) {
	d.handler.GetGroup(w, r)
}
func (d *APIGenDispatcher) UpdateGroup(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string, _ accessgen.GenUpdateGroupHeaders) {
	d.handler.UpdateGroup(w, r)
}
func (d *APIGenDispatcher) DeleteGroup(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string) {
	d.handler.DeleteGroup(w, r)
}
func (d *APIGenDispatcher) ListGroupMembers(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenListGroupMembersParams) {
	d.handler.ListGroupMembers(w, r)
}
func (d *APIGenDispatcher) AddGroupMember(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string) {
	d.handler.AddGroupMember(w, r)
}
func (d *APIGenDispatcher) RemoveGroupMember(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string) {
	d.handler.RemoveGroupMember(w, r)
}
func (d *APIGenDispatcher) ListGroupSemanticAttributeAssignments(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenListGroupSemanticAttributeAssignmentsParams) {
	d.handler.ListGroupSemanticAttributeAssignments(w, r)
}
func (d *APIGenDispatcher) RemoveGroupSemanticAttributeAssignment(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string, headers accessgen.GenRemoveGroupSemanticAttributeAssignmentHeaders) {
	if headers.IfMatch != "" {
		r.Header.Set("If-Match", headers.IfMatch)
	}
	d.handler.RemoveGroupSemanticAttributeAssignment(w, r)
}
func (d *APIGenDispatcher) UpsertGroupSemanticAttributeAssignment(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string, headers accessgen.GenUpsertGroupSemanticAttributeAssignmentHeaders) {
	if headers.IfMatch != "" {
		r.Header.Set("If-Match", headers.IfMatch)
	}
	d.handler.UpsertGroupSemanticAttributeAssignment(w, r)
}
func (d *APIGenDispatcher) ListPrincipalSemanticAttributeAssignments(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenListPrincipalSemanticAttributeAssignmentsParams) {
	d.handler.ListPrincipalSemanticAttributeAssignments(w, r)
}
func (d *APIGenDispatcher) RemovePrincipalSemanticAttributeAssignment(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string, headers accessgen.GenRemovePrincipalSemanticAttributeAssignmentHeaders) {
	if headers.IfMatch != "" {
		r.Header.Set("If-Match", headers.IfMatch)
	}
	d.handler.RemovePrincipalSemanticAttributeAssignment(w, r)
}
func (d *APIGenDispatcher) UpsertPrincipalSemanticAttributeAssignment(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string, headers accessgen.GenUpsertPrincipalSemanticAttributeAssignmentHeaders) {
	if headers.IfMatch != "" {
		r.Header.Set("If-Match", headers.IfMatch)
	}
	d.handler.UpsertPrincipalSemanticAttributeAssignment(w, r)
}
func (d *APIGenDispatcher) ListSemanticAttributeDefinitions(w stdhttp.ResponseWriter, r *stdhttp.Request, _ accessgen.GenListSemanticAttributeDefinitionsParams) {
	d.handler.ListSemanticAttributeDefinitions(w, r)
}
func (d *APIGenDispatcher) RegisterSemanticAttribute(w stdhttp.ResponseWriter, r *stdhttp.Request, headers accessgen.GenRegisterSemanticAttributeHeaders) {
	if headers.IdempotencyKey != "" {
		r.Header.Set("Idempotency-Key", headers.IdempotencyKey)
	}
	d.handler.RegisterSemanticAttribute(w, r)
}
func (d *APIGenDispatcher) GetSemanticAttributeDefinition(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string) {
	d.handler.GetSemanticAttributeDefinition(w, r)
}
func (d *APIGenDispatcher) UpdateSemanticAttributeMetadata(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, headers accessgen.GenUpdateSemanticAttributeMetadataHeaders) {
	if headers.IfMatch != "" {
		r.Header.Set("If-Match", headers.IfMatch)
	}
	d.handler.UpdateSemanticAttributeMetadata(w, r)
}
func (d *APIGenDispatcher) ListSemanticAttributeClaimMappings(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ accessgen.GenListSemanticAttributeClaimMappingsParams) {
	d.handler.ListSemanticAttributeClaimMappings(w, r)
}
func (d *APIGenDispatcher) UpsertSemanticAttributeClaimMapping(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, headers accessgen.GenUpsertSemanticAttributeClaimMappingHeaders) {
	if headers.IfMatch != "" {
		r.Header.Set("If-Match", headers.IfMatch)
	}
	if headers.IdempotencyKey != "" {
		r.Header.Set("Idempotency-Key", headers.IdempotencyKey)
	}
	d.handler.UpsertSemanticAttributeClaimMapping(w, r)
}
func (d *APIGenDispatcher) RemoveSemanticAttributeClaimMapping(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string, headers accessgen.GenRemoveSemanticAttributeClaimMappingHeaders) {
	if headers.IfMatch != "" {
		r.Header.Set("If-Match", headers.IfMatch)
	}
	d.handler.RemoveSemanticAttributeClaimMapping(w, r)
}
func (d *APIGenDispatcher) DisableSemanticAttribute(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, headers accessgen.GenDisableSemanticAttributeHeaders) {
	if headers.IfMatch != "" {
		r.Header.Set("If-Match", headers.IfMatch)
	}
	if headers.IdempotencyKey != "" {
		r.Header.Set("Idempotency-Key", headers.IdempotencyKey)
	}
	d.handler.DisableSemanticAttribute(w, r)
}
func (d *APIGenDispatcher) PreviewSemanticAttributeImpact(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string) {
	d.handler.PreviewSemanticAttributeImpact(w, r)
}
func (d *APIGenDispatcher) RestoreSemanticAttribute(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, headers accessgen.GenRestoreSemanticAttributeHeaders) {
	if headers.IfMatch != "" {
		r.Header.Set("If-Match", headers.IfMatch)
	}
	if headers.IdempotencyKey != "" {
		r.Header.Set("Idempotency-Key", headers.IdempotencyKey)
	}
	d.handler.RestoreSemanticAttribute(w, r)
}
func (d *APIGenDispatcher) ListAuditEvents(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string, _ accessgen.GenListAuditEventsParams) {
	d.handler.ListAuditEvents(w, r)
}
func (d *APIGenDispatcher) ListPlatformAuditEvents(w stdhttp.ResponseWriter, r *stdhttp.Request, _ accessgen.GenListPlatformAuditEventsParams) {
	d.handler.ListPlatformAuditEvents(w, r)
}
