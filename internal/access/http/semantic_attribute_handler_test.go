package http

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/access/trustedclaims"
	"github.com/flidai/leapview/internal/semanticvalue"
	"github.com/go-chi/chi/v5"
)

type semanticAttributeHTTPTestRepository struct {
	access.Repository
	admin       bool
	definition  access.SemanticAttributeDefinition
	assignment  access.SemanticAttributeAssignment
	mapping     access.TrustedClaimMapping
	mappingErr  error
	bumpOnWrite bool
}

func (r *semanticAttributeHTTPTestRepository) IsPlatformAdmin(context.Context, string) (bool, error) {
	return r.admin, nil
}

func (r *semanticAttributeHTTPTestRepository) SemanticAttributeRegistry(context.Context) (access.SemanticAttributeRegistrySnapshot, error) {
	return access.SemanticAttributeRegistrySnapshot{Definitions: []access.SemanticAttributeDefinition{r.definition}}, nil
}
func (r *semanticAttributeHTTPTestRepository) SemanticAttributeDefinition(_ context.Context, name string) (access.SemanticAttributeDefinition, error) {
	if name != r.definition.Name {
		return access.SemanticAttributeDefinition{}, sql.ErrNoRows
	}
	return r.definition, nil
}
func (r *semanticAttributeHTTPTestRepository) SemanticAttributeDefinitionByID(_ context.Context, id string) (access.SemanticAttributeDefinition, error) {
	if id != r.definition.ID {
		return access.SemanticAttributeDefinition{}, sql.ErrNoRows
	}
	return r.definition, nil
}
func (r *semanticAttributeHTTPTestRepository) SearchSemanticAttributes(context.Context, access.SemanticAttributeSearch) ([]access.SemanticAttributeDefinition, error) {
	return []access.SemanticAttributeDefinition{r.definition}, nil
}
func (r *semanticAttributeHTTPTestRepository) RegisterSemanticAttribute(_ context.Context, input access.RegisterSemanticAttributeInput) (access.SemanticAttributeDefinition, error) {
	r.definition = access.SemanticAttributeDefinition{ID: "definition-2", Name: input.Name, Type: input.Type, Shape: input.Shape, Profile: semanticvalue.Profile, DefinitionVersion: 1, Metadata: input.Metadata, LifecycleState: access.SemanticAttributeActive, Enabled: true}
	return r.definition, nil
}
func (r *semanticAttributeHTTPTestRepository) UpdateSemanticAttributeMetadata(_ context.Context, input access.UpdateSemanticAttributeMetadataInput) (access.SemanticAttributeDefinition, error) {
	r.definition.Metadata = input.Metadata
	r.definition.DefinitionVersion++
	return r.definition, nil
}
func (r *semanticAttributeHTTPTestRepository) UpdateSemanticAttributeMetadataExpected(ctx context.Context, input access.UpdateSemanticAttributeMetadataInput) (access.SemanticAttributeDefinition, error) {
	if r.bumpOnWrite {
		r.definition.DefinitionVersion++
		r.bumpOnWrite = false
	}
	if input.ExpectedVersion != 0 && input.ExpectedVersion != r.definition.DefinitionVersion {
		return access.SemanticAttributeDefinition{}, access.ErrSemanticAttributeConflict
	}
	return r.UpdateSemanticAttributeMetadata(ctx, input)
}
func (r *semanticAttributeHTTPTestRepository) SetSemanticAttributeEnabled(_ context.Context, _ string, enabled bool, _ access.SemanticAttributeMutationContext) (access.SemanticAttributeDefinition, error) {
	r.definition.Enabled = enabled
	if enabled {
		r.definition.LifecycleState = access.SemanticAttributeActive
	} else {
		r.definition.LifecycleState = access.SemanticAttributeDisabled
	}
	r.definition.DefinitionVersion++
	return r.definition, nil
}
func (r *semanticAttributeHTTPTestRepository) SetSemanticAttributeEnabledExpected(ctx context.Context, name string, enabled bool, expected int64, mutation access.SemanticAttributeMutationContext) (access.SemanticAttributeDefinition, error) {
	if r.bumpOnWrite {
		r.definition.DefinitionVersion++
		r.bumpOnWrite = false
	}
	if expected != 0 && expected != r.definition.DefinitionVersion {
		return access.SemanticAttributeDefinition{}, access.ErrSemanticAttributeConflict
	}
	return r.SetSemanticAttributeEnabled(ctx, name, enabled, mutation)
}
func (r *semanticAttributeHTTPTestRepository) ValidateSemanticAttributeValue(_ context.Context, _ string, input any) (access.CanonicalSemanticAttributeValue, error) {
	values, digest, err := access.CanonicalSemanticAttributeValues(r.definition, input)
	return access.CanonicalSemanticAttributeValue{DefinitionID: r.definition.ID, DefinitionVersion: r.definition.DefinitionVersion, Name: r.definition.Name, Type: r.definition.Type, Shape: r.definition.Shape, CanonicalValues: values, Digest: digest}, err
}

func (r *semanticAttributeHTTPTestRepository) SemanticAttributeAssignments(_ context.Context, filter access.SemanticAttributeAssignmentFilter) ([]access.SemanticAttributeAssignment, error) {
	if r.assignment.ID == "" || (filter.DefinitionID != "" && filter.DefinitionID != r.assignment.DefinitionID) || (filter.Subject.ID != "" && filter.Subject != r.assignment.Subject) {
		return nil, nil
	}
	return []access.SemanticAttributeAssignment{r.assignment}, nil
}
func (r *semanticAttributeHTTPTestRepository) EffectiveSemanticAttributeAssignments(context.Context, access.SubjectRef, trustedclaims.Envelope) ([]access.EffectiveSemanticAttribute, error) {
	return nil, nil
}
func (r *semanticAttributeHTTPTestRepository) EffectiveDirectSemanticAttributeAssignments(context.Context, access.SubjectRef) ([]access.EffectiveSemanticAttribute, error) {
	return nil, nil
}
func (r *semanticAttributeHTTPTestRepository) SetSemanticAttributeAssignment(_ context.Context, input access.SemanticAttributeAssignmentInput) (access.SemanticAttributeAssignment, error) {
	if r.assignment.ID != "" && !r.assignment.Tombstoned {
		if input.ExpectedVersion != r.assignment.AssignmentVersion {
			return access.SemanticAttributeAssignment{}, access.ErrSemanticAttributeAssignmentConflict
		}
		r.assignment.CanonicalValues, r.assignment.ValueDigest, _ = access.CanonicalSemanticAttributeValues(r.definition, input.Values)
		r.assignment.AssignmentVersion++
		return r.assignment, nil
	}
	if input.ExpectedVersion != 0 {
		return access.SemanticAttributeAssignment{}, access.ErrSemanticAttributeAssignmentConflict
	}
	values, digest, err := access.CanonicalSemanticAttributeValues(r.definition, input.Values)
	if err != nil {
		return access.SemanticAttributeAssignment{}, err
	}
	r.assignment = access.SemanticAttributeAssignment{ID: "assignment-1", DefinitionID: r.definition.ID, DefinitionName: r.definition.Name, DefinitionVersion: r.definition.DefinitionVersion, Type: r.definition.Type, Shape: r.definition.Shape, Subject: input.Subject, CanonicalValues: values, ValueDigest: digest, AssignmentVersion: 1, CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z"}
	return r.assignment, nil
}
func (r *semanticAttributeHTTPTestRepository) TombstoneSemanticAttributeAssignment(_ context.Context, id string, expected int64, _ access.SemanticAttributeMutationContext) (access.SemanticAttributeAssignment, error) {
	if id != r.assignment.ID || expected != r.assignment.AssignmentVersion || r.assignment.Tombstoned {
		return access.SemanticAttributeAssignment{}, access.ErrSemanticAttributeAssignmentConflict
	}
	r.assignment.Tombstoned = true
	r.assignment.TombstonedAt = "2026-01-01T01:00:00Z"
	r.assignment.AssignmentVersion++
	return r.assignment, nil
}

func (r *semanticAttributeHTTPTestRepository) TrustedClaimMappings(_ context.Context, _ access.TrustedClaimMappingFilter) ([]access.TrustedClaimMapping, error) {
	if r.mappingErr != nil {
		return nil, r.mappingErr
	}
	if r.mapping.ID == "" {
		return nil, nil
	}
	return []access.TrustedClaimMapping{r.mapping}, nil
}
func (r *semanticAttributeHTTPTestRepository) SetTrustedClaimMapping(_ context.Context, input access.TrustedClaimMappingInput) (access.TrustedClaimMapping, error) {
	if r.mapping.ID != "" && !r.mapping.Tombstoned {
		if input.ExpectedVersion != r.mapping.MappingVersion {
			return access.TrustedClaimMapping{}, access.ErrSemanticAttributeMappingConflict
		}
		r.mapping.MappingVersion++
		return r.mapping, nil
	}
	if input.ExpectedVersion != 0 {
		return access.TrustedClaimMapping{}, access.ErrSemanticAttributeMappingConflict
	}
	r.mapping = access.TrustedClaimMapping{ID: "mapping-1", SourceKind: input.SourceKind, Provider: input.Provider, Issuer: input.Issuer, Audience: input.Audience, Claim: input.Claim, DefinitionID: r.definition.ID, DefinitionName: r.definition.Name, MappingVersion: 1, Type: r.definition.Type, Shape: r.definition.Shape, CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z"}
	return r.mapping, nil
}
func (r *semanticAttributeHTTPTestRepository) TombstoneTrustedClaimMapping(_ context.Context, id string, expected int64, _ access.SemanticAttributeMutationContext) (access.TrustedClaimMapping, error) {
	if id != r.mapping.ID || expected != r.mapping.MappingVersion || r.mapping.Tombstoned {
		return access.TrustedClaimMapping{}, access.ErrSemanticAttributeMappingConflict
	}
	r.mapping.Tombstoned = true
	r.mapping.TombstonedAt = "2026-01-01T01:00:00Z"
	r.mapping.MappingVersion++
	return r.mapping, nil
}

func semanticAttributeHTTPHandler(repo *semanticAttributeHTTPTestRepository, principal bool) Handler {
	return Handler{Repository: func() (access.Repository, error) { return repo, nil }, CurrentPrincipal: func(*http.Request) (Principal, bool) { return Principal{ID: "admin"}, principal }, PlatformAdmin: func(context.Context, string) (bool, error) { return repo.admin, nil }}
}
func semanticAttributeRequest(method, path, body string, params ...string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	route := chi.NewRouteContext()
	for i := 0; i+1 < len(params); i += 2 {
		route.URLParams.Add(params[i], params[i+1])
	}
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route))
}
func semanticAttributeHTTPRepository() *semanticAttributeHTTPTestRepository {
	return &semanticAttributeHTTPTestRepository{admin: true, definition: access.SemanticAttributeDefinition{ID: "definition-1", Name: "region", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar, Profile: semanticvalue.Profile, DefinitionVersion: 1, Metadata: access.SemanticAttributeMetadata{Owner: access.SemanticAttributeOwner{Kind: access.SemanticAttributeOwnerInstance}, DisplayName: "Region", Description: "Region"}, LifecycleState: access.SemanticAttributeActive, Enabled: true}}
}

func TestSemanticAttributeHTTPPlatformBoundary(t *testing.T) {
	repo := semanticAttributeHTTPRepository()
	response := httptest.NewRecorder()
	semanticAttributeHTTPHandler(repo, false).GetSemanticAttributeDefinition(response, semanticAttributeRequest(http.MethodGet, "/semantic-attributes/region", "", "attribute", "region"))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", response.Code)
	}
	response = httptest.NewRecorder()
	semanticAttributeHTTPHandler(repo, true).GetSemanticAttributeDefinition(response, semanticAttributeRequest(http.MethodGet, "/semantic-attributes/region", "", "attribute", "region"))
	if response.Code != http.StatusOK {
		t.Fatalf("admin status=%d body=%s", response.Code, response.Body.String())
	}
	repo.admin = false
	response = httptest.NewRecorder()
	semanticAttributeHTTPHandler(repo, true).GetSemanticAttributeDefinition(response, semanticAttributeRequest(http.MethodGet, "/semantic-attributes/region", "", "attribute", "region"))
	if response.Code != http.StatusForbidden {
		t.Fatalf("non-admin status=%d", response.Code)
	}
}

func TestSemanticAttributeHTTPCredentialAttenuation(t *testing.T) {
	repo := semanticAttributeHTTPRepository()
	handler := semanticAttributeHTTPHandler(repo, true)
	credential := access.APICredential{Principal: access.Principal{ID: "admin"}, Token: access.APIToken{ID: "narrow", PrincipalID: "admin", Capabilities: []access.Capability{access.CapabilityResourceRead}}}
	handler.CurrentCredential = func(*http.Request) (access.APICredential, bool) { return credential, true }
	request := semanticAttributeRequest(http.MethodGet, "/semantic-attributes/region", "", "attribute", "region")
	response := httptest.NewRecorder()
	handler.GetSemanticAttributeDefinition(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("narrow token status=%d", response.Code)
	}
	credential.Token.Capabilities = []access.Capability{access.CapabilityProjectAdmin}
	response = httptest.NewRecorder()
	handler.GetSemanticAttributeDefinition(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("project-admin token status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSemanticAttributeHTTPRejectsMixedValuesAndInvalidOwner(t *testing.T) {
	repo := semanticAttributeHTTPRepository()
	handler := semanticAttributeHTTPHandler(repo, true)
	for name, body := range map[string]string{
		"mixed":  `{"values":[{"type":"String","stringValue":"us","integerValue":"1"}]}`,
		"object": `{"values":[{"type":"String","stringValue":{"raw":"us"}}]}`,
		"nested": `{"values":[{"type":"String","stringValue":["us"]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			request := semanticAttributeRequest(http.MethodPut, "/principals/p/semantic-attributes/region", body, "principal", "p", "attribute", "region")
			request.Header.Set("If-Match", "*")
			response := httptest.NewRecorder()
			handler.UpsertPrincipalSemanticAttributeAssignment(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	owner := semanticAttributeRequest(http.MethodPost, "/semantic-attributes", `{"name":"cost","type":"String","shape":"scalar","metadata":{"ownerKind":"principal","displayName":"Cost","description":"Cost"}}`)
	response := httptest.NewRecorder()
	handler.RegisterSemanticAttribute(response, owner)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("owner status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSemanticAttributeHTTPDefinitionLifecycleAndTransactionConflict(t *testing.T) {
	repo := semanticAttributeHTTPRepository()
	handler := semanticAttributeHTTPHandler(repo, true)
	get := httptest.NewRecorder()
	handler.GetSemanticAttributeDefinition(get, semanticAttributeRequest(http.MethodGet, "/semantic-attributes/region", "", "attribute", "region"))
	etag := get.Header().Get("ETag")
	stale := semanticAttributeRequest(http.MethodPatch, "/semantic-attributes/region", `{"ownerKind":"instance","displayName":"New","description":"New"}`, "attribute", "region")
	stale.Header.Set("If-Match", `"stale"`)
	response := httptest.NewRecorder()
	handler.UpdateSemanticAttributeMetadata(response, stale)
	if response.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale status=%d", response.Code)
	}
	racy := semanticAttributeRequest(http.MethodPatch, "/semantic-attributes/region", `{"ownerKind":"instance","displayName":"New","description":"New"}`, "attribute", "region")
	racy.Header.Set("If-Match", etag)
	repo.bumpOnWrite = true
	response = httptest.NewRecorder()
	handler.UpdateSemanticAttributeMetadata(response, racy)
	if response.Code != http.StatusConflict {
		t.Fatalf("transaction conflict status=%d body=%s", response.Code, response.Body.String())
	}
	get = httptest.NewRecorder()
	handler.GetSemanticAttributeDefinition(get, semanticAttributeRequest(http.MethodGet, "/semantic-attributes/region", "", "attribute", "region"))
	etag = get.Header().Get("ETag")
	disable := semanticAttributeRequest(http.MethodPost, "/semantic-attributes/region/disable", "", "attribute", "region")
	disable.Header.Set("If-Match", etag)
	response = httptest.NewRecorder()
	handler.DisableSemanticAttribute(response, disable)
	if response.Code != http.StatusOK || repo.definition.Enabled {
		t.Fatalf("disable response=%d enabled=%v", response.Code, repo.definition.Enabled)
	}
}

func TestSemanticAttributeHTTPAssignmentAndMappingTombstoneRequireETag(t *testing.T) {
	repo := semanticAttributeHTTPRepository()
	handler := semanticAttributeHTTPHandler(repo, true)
	upsert := semanticAttributeRequest(http.MethodPut, "/principals/p/semantic-attributes/region", `{"values":[{"type":"String","stringValue":"us"}]}`, "principal", "p", "attribute", "region")
	upsert.Header.Set("If-Match", "*")
	created := httptest.NewRecorder()
	handler.UpsertPrincipalSemanticAttributeAssignment(created, upsert)
	if created.Code != http.StatusOK {
		t.Fatalf("assignment create=%d body=%s", created.Code, created.Body.String())
	}
	remove := semanticAttributeRequest(http.MethodDelete, "/principals/p/semantic-attributes/region", "", "principal", "p", "attribute", "region")
	response := httptest.NewRecorder()
	handler.RemovePrincipalSemanticAttributeAssignment(response, remove)
	if response.Code != http.StatusPreconditionFailed {
		t.Fatalf("missing assignment If-Match=%d", response.Code)
	}
	remove.Header.Set("If-Match", created.Header().Get("ETag"))
	response = httptest.NewRecorder()
	handler.RemovePrincipalSemanticAttributeAssignment(response, remove)
	if response.Code != http.StatusNoContent {
		t.Fatalf("assignment remove=%d body=%s", response.Code, response.Body.String())
	}

	claim := semanticAttributeRequest(http.MethodPost, "/semantic-attributes/region/claim-mappings", `{"sourceKind":"oidc","provider":"corp","issuer":"https://issuer","audience":"aud","claim":"department"}`, "attribute", "region")
	claim.Header.Set("If-Match", "*")
	claim.Header.Set("Idempotency-Key", "test-1")
	createdMapping := httptest.NewRecorder()
	handler.UpsertSemanticAttributeClaimMapping(createdMapping, claim)
	if createdMapping.Code != http.StatusOK {
		t.Fatalf("mapping create=%d body=%s", createdMapping.Code, createdMapping.Body.String())
	}
	removeMapping := semanticAttributeRequest(http.MethodDelete, "/semantic-attributes/region/claim-mappings/mapping-1", "", "attribute", "region", "mapping", "mapping-1")
	removeMapping.Header.Set("If-Match", createdMapping.Header().Get("ETag"))
	removedMapping := httptest.NewRecorder()
	handler.RemoveSemanticAttributeClaimMapping(removedMapping, removeMapping)
	if removedMapping.Code != http.StatusNoContent {
		t.Fatalf("mapping remove=%d body=%s", removedMapping.Code, removedMapping.Body.String())
	}
}

func TestSemanticAttributeHTTPClaimMappingRequiresCompleteTrustIdentity(t *testing.T) {
	repo := semanticAttributeHTTPRepository()
	handler := semanticAttributeHTTPHandler(repo, true)

	for _, body := range []string{
		`{"sourceKind":"oidc","provider":"corp","audience":"aud","claim":"department"}`,
		`{"sourceKind":"oidc","provider":"corp","issuer":"https://issuer","audience":" ","claim":"department"}`,
	} {
		request := semanticAttributeRequest(http.MethodPost, "/semantic-attributes/region/claim-mappings", body, "attribute", "region")
		request.Header.Set("If-Match", "*")
		response := httptest.NewRecorder()
		handler.UpsertSemanticAttributeClaimMapping(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("incomplete trust identity status=%d body=%s", response.Code, response.Body.String())
		}
		if repo.mapping.ID != "" {
			t.Fatal("incomplete trust identity reached the mapping writer")
		}
	}
}

func TestSemanticAttributeHTTPDoesNotExposeRepositoryErrors(t *testing.T) {
	repo := semanticAttributeHTTPRepository()
	handler := semanticAttributeHTTPHandler(repo, true)
	// A malformed provider failure must not be reflected verbatim in the HTTP
	// response, where it could disclose raw claim values or SQL diagnostics.
	request := semanticAttributeRequest(http.MethodGet, "/semantic-attributes/region/claim-mappings", "", "attribute", "region")
	repo.mappingErr = errors.New("raw-secret claim value leaked")
	response := httptest.NewRecorder()
	handler.ListSemanticAttributeClaimMappings(response, request)
	if strings.Contains(response.Body.String(), "raw-secret") {
		t.Fatal("repository diagnostics leaked")
	}
}
