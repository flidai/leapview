package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	definitions []access.SemanticAttributeDefinition
	lastSearch  access.SemanticAttributeSearch
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
func (r *semanticAttributeHTTPTestRepository) SearchSemanticAttributes(_ context.Context, filter access.SemanticAttributeSearch) ([]access.SemanticAttributeDefinition, error) {
	r.lastSearch = filter
	definitions := r.definitions
	if len(definitions) == 0 {
		definitions = []access.SemanticAttributeDefinition{r.definition}
	}
	filtered := make([]access.SemanticAttributeDefinition, 0, len(definitions))
	for _, definition := range definitions {
		if filter.OwnerKind != "" && definition.Metadata.Owner.Kind != filter.OwnerKind {
			continue
		}
		if filter.AfterName != "" && (definition.Name < filter.AfterName || (definition.Name == filter.AfterName && definition.ID <= filter.AfterDefinitionID)) {
			continue
		}
		filtered = append(filtered, definition)
	}
	if filter.Limit > 0 && len(filtered) > filter.Limit {
		filtered = filtered[:filter.Limit]
	}
	return filtered, nil
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

func TestSemanticAttributeHTTPDefinitionListFiltersBeforePagination(t *testing.T) {
	const nonMatchingDefinitions = 45
	const matchingDefinitions = 205
	definitions := make([]access.SemanticAttributeDefinition, 0, nonMatchingDefinitions+matchingDefinitions)
	for i := 0; i < nonMatchingDefinitions+matchingDefinitions; i++ {
		ownerKind := access.SemanticAttributeOwnerInstance
		if i >= nonMatchingDefinitions {
			ownerKind = access.SemanticAttributeOwnerGroup
		}
		definitions = append(definitions, access.SemanticAttributeDefinition{
			ID: fmt.Sprintf("definition-%03d", i), Name: fmt.Sprintf("attribute-%03d", i),
			Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar, Profile: semanticvalue.Profile,
			DefinitionVersion: 1, Metadata: access.SemanticAttributeMetadata{Owner: access.SemanticAttributeOwner{Kind: ownerKind}},
			LifecycleState: access.SemanticAttributeActive, Enabled: true,
		})
	}
	repo := &semanticAttributeHTTPTestRepository{admin: true, definitions: definitions}
	handler := semanticAttributeHTTPHandler(repo, true)

	var names []string
	pageToken := ""
	for {
		path := "/api/v1/semantic-attributes?ownerKind=group&limit=50"
		if pageToken != "" {
			path += "&pageToken=" + pageToken
		}
		response := httptest.NewRecorder()
		handler.ListSemanticAttributeDefinitions(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("list status=%d body=%s", response.Code, response.Body.String())
		}
		var body struct {
			Items []struct {
				Name      string `json:"name"`
				OwnerKind string `json:"ownerKind"`
			} `json:"items"`
			Page struct {
				NextCursor string `json:"nextCursor"`
			} `json:"page"`
		}
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		for _, item := range body.Items {
			if item.OwnerKind != string(access.SemanticAttributeOwnerGroup) {
				t.Fatalf("page included owner kind %q", item.OwnerKind)
			}
			names = append(names, item.Name)
		}
		pageToken = body.Page.NextCursor
		if pageToken == "" {
			break
		}
	}

	if len(names) != matchingDefinitions {
		t.Fatalf("filtered definitions = %d, want %d", len(names), matchingDefinitions)
	}
	for i, name := range names {
		want := fmt.Sprintf("attribute-%03d", nonMatchingDefinitions+i)
		if name != want {
			t.Fatalf("filtered definition %d = %q, want %q", i, name, want)
		}
	}
	if repo.lastSearch.OwnerKind != access.SemanticAttributeOwnerGroup || repo.lastSearch.Limit != 51 {
		t.Fatalf("repository search = %#v, want owner filter and limit+1 request", repo.lastSearch)
	}
}

func TestSemanticAttributeHTTPDefinitionListRejectsInvalidPageToken(t *testing.T) {
	handler := semanticAttributeHTTPHandler(semanticAttributeHTTPRepository(), true)
	for _, token := range []string{"not-base64", encodeKeyCursor("region"), encodeKeyCursor("\x00definition-1")} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/semantic-attributes?pageToken="+token, nil)
		response := httptest.NewRecorder()
		handler.ListSemanticAttributeDefinitions(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("pageToken %q status=%d body=%s", token, response.Code, response.Body.String())
		}
	}
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

func TestSemanticAttributeHTTPRegisterLocationUsesCanonicalAPIPath(t *testing.T) {
	repo := semanticAttributeHTTPRepository()
	request := semanticAttributeRequest(http.MethodPost, "/api/v1/semantic-attributes", `{"name":"cost_center","type":"String","shape":"scalar","metadata":{"ownerKind":"instance","displayName":"Cost center","description":"Cost center"}}`)
	response := httptest.NewRecorder()
	semanticAttributeHTTPHandler(repo, true).RegisterSemanticAttribute(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("register status=%d body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Location"); got != "/api/v1/semantic-attributes/cost_center" {
		t.Fatalf("register Location=%q", got)
	}
}

func TestSemanticAttributeHTTPPreviewUsesRequestedAssignmentDelta(t *testing.T) {
	tests := []struct {
		name            string
		assignmentValue string
		requestedValues string
		targetKind      access.SubjectKind
		targetID        string
		principalCount  int32
		groupCount      int32
	}{
		{name: "add", requestedValues: `[{"type":"String","stringValue":"us"}]`, principalCount: 1},
		{name: "no-op", assignmentValue: "us", requestedValues: `[{"type":"String","stringValue":"us"}]`},
		{name: "change", assignmentValue: "us", requestedValues: `[{"type":"String","stringValue":"eu"}]`, principalCount: 1},
		{name: "remove", assignmentValue: "us", requestedValues: `[]`, principalCount: 1},
		{name: "group change", assignmentValue: "us", requestedValues: `[{"type":"String","stringValue":"eu"}]`, targetKind: access.SubjectKindGroup, groupCount: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := semanticAttributeHTTPRepository()
			targetKind, targetID := test.targetKind, test.targetID
			if targetKind == "" {
				targetKind = access.SubjectKindPrincipal
			}
			if targetID == "" {
				targetID = string(targetKind) + "-1"
			}
			if test.assignmentValue != "" {
				values, digest, err := access.CanonicalSemanticAttributeValues(repo.definition, test.assignmentValue)
				if err != nil {
					t.Fatal(err)
				}
				repo.assignment = access.SemanticAttributeAssignment{ID: "assignment-1", DefinitionID: repo.definition.ID, DefinitionName: repo.definition.Name, Subject: access.SubjectRef{Kind: targetKind, ID: targetID}, Type: repo.definition.Type, Shape: repo.definition.Shape, CanonicalValues: values, ValueDigest: digest, AssignmentVersion: 1}
			}
			request := semanticAttributeRequest(http.MethodPost, "/api/v1/semantic-attributes/region/impact-preview", `{"targetKind":"`+string(targetKind)+`","targetId":"`+targetID+`","values":`+test.requestedValues+`}`, "attribute", "region")
			response := httptest.NewRecorder()
			semanticAttributeHTTPHandler(repo, true).PreviewSemanticAttributeImpact(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("preview status=%d body=%s", response.Code, response.Body.String())
			}
			var body struct {
				AffectedPrincipalCount int32 `json:"affectedPrincipalCount"`
				AffectedGroupCount     int32 `json:"affectedGroupCount"`
			}
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.AffectedPrincipalCount != test.principalCount || body.AffectedGroupCount != test.groupCount {
				t.Fatalf("preview counts = (%d, %d), want (%d, %d)", body.AffectedPrincipalCount, body.AffectedGroupCount, test.principalCount, test.groupCount)
			}
		})
	}
}

func TestSemanticAttributeHTTPPreviewRequiresValues(t *testing.T) {
	repo := semanticAttributeHTTPRepository()
	request := semanticAttributeRequest(http.MethodPost, "/api/v1/semantic-attributes/region/impact-preview", `{"targetKind":"principal","targetId":"principal-1"}`, "attribute", "region")
	response := httptest.NewRecorder()
	semanticAttributeHTTPHandler(repo, true).PreviewSemanticAttributeImpact(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("preview omitted values status=%d body=%s", response.Code, response.Body.String())
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
