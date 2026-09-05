package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	accessmodule "github.com/flidai/leapview/internal/access/module"
	analyticsgen "github.com/flidai/leapview/internal/analytics/api/gen"
	saved "github.com/flidai/leapview/internal/analytics/exploration/saved"
)

// TestSavedExplorationGeneratedRouterCommands exercises the assembled
// APIGen transport, rather than the feature handler directly. In particular,
// successful command responses prove that the generated invocation guard was
// completed after the repository CAS, while stale requests prove that the
// same CAS rejects without changing durable state.
func TestSavedExplorationGeneratedRouterCommands(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	principal := testPlatformPrincipal(t, ctx, store, "saved-api-integration@example.com", "Saved API Integration")
	token := testAPIToken(t, ctx, store, principal.ID, "saved-api-integration")
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	server, err := assembleRuntimeChecked(ctx, fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth}))
	if err != nil {
		t.Fatalf("assemble runtime: %v", err)
	}

	const project = "project:test"
	const path = "/api/v1/projects/" + project + "/saved-explorations"
	createBody := `{"title":"Orders","slug":"orders","visibility":"private","spec":{"schemaVersion":1,"modelId":"test","datasetId":"orders","dimensions":[{"field":"orders.status"}],"metrics":[{"field":"order_count"}],"filters":[],"sort":[],"limit":100}}`
	create := savedAPICall(t, server.Routes(), token, http.MethodPost, path, createBody, map[string]string{
		"Idempotency-Key": "generated-create-1",
	})
	if create.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d (%s)", create.Code, http.StatusCreated, create.Body.String())
	}
	var created analyticsgen.SavedExplorationResponse
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.Id == "" {
		t.Fatal("create response did not include exploration ID")
	}
	initialETag := create.Header().Get("ETag")
	if initialETag == "" {
		t.Fatal("create response did not include ETag")
	}
	itemPath := path + "/" + created.Id

	updateBody := `{"title":"Orders Updated","slug":"orders-updated","visibility":"private","spec":{"schemaVersion":1,"modelId":"test","datasetId":"orders","dimensions":[{"field":"orders.status"}],"metrics":[{"field":"order_count"}],"filters":[],"sort":[],"limit":100}}`
	update := savedAPICall(t, server.Routes(), token, http.MethodPatch, itemPath, updateBody, map[string]string{
		"Idempotency-Key": "generated-update-1", "If-Match": initialETag,
	})
	if update.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, want %d (%s)", update.Code, http.StatusOK, update.Body.String())
	}
	updatedETag := update.Header().Get("ETag")
	if updatedETag == "" || updatedETag == initialETag {
		t.Fatalf("PATCH ETag = %q, want a new non-empty token", updatedETag)
	}

	staleUpdate := savedAPICall(t, server.Routes(), token, http.MethodPatch, itemPath, updateBody, map[string]string{
		"Idempotency-Key": "generated-update-stale", "If-Match": initialETag,
	})
	if staleUpdate.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale PATCH status = %d, want %d (%s)", staleUpdate.Code, http.StatusPreconditionFailed, staleUpdate.Body.String())
	}
	assertSavedExplorationState(t, store.SQLDB(), created.Id, "Orders Updated", "active")

	duplicate := savedAPICall(t, server.Routes(), token, http.MethodPost, itemPath+"/duplicate", `{ "title": "Orders Copy" }`, map[string]string{
		"Idempotency-Key": "duplicate", "If-Match": updatedETag,
	})
	if duplicate.Code != http.StatusCreated {
		t.Fatalf("duplicate status = %d, want %d (%s)", duplicate.Code, http.StatusCreated, duplicate.Body.String())
	}
	var copied analyticsgen.SavedExplorationResponse
	if err := json.Unmarshal(duplicate.Body.Bytes(), &copied); err != nil {
		t.Fatalf("decode duplicate response: %v", err)
	}
	if copied.Id == "" || copied.Id == created.Id {
		t.Fatalf("duplicate response ID = %q, want a new exploration", copied.Id)
	}
	countBeforeStaleDuplicate := countSavedExplorations(t, store.SQLDB())
	staleDuplicate := savedAPICall(t, server.Routes(), token, http.MethodPost, itemPath+"/duplicate", `{ "title": "Should Not Exist" }`, map[string]string{
		"Idempotency-Key": "generated-duplicate-stale", "If-Match": initialETag,
	})
	if staleDuplicate.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale duplicate status = %d, want %d (%s)", staleDuplicate.Code, http.StatusPreconditionFailed, staleDuplicate.Body.String())
	}
	if got := countSavedExplorations(t, store.SQLDB()); got != countBeforeStaleDuplicate {
		t.Fatalf("stale duplicate changed exploration count from %d to %d", countBeforeStaleDuplicate, got)
	}

	staleArchive := savedAPICall(t, server.Routes(), token, http.MethodPost, itemPath+"/archive", "", map[string]string{
		"Idempotency-Key": "generated-archive-stale", "If-Match": initialETag,
	})
	if staleArchive.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale archive status = %d, want %d (%s)", staleArchive.Code, http.StatusPreconditionFailed, staleArchive.Body.String())
	}
	assertSavedExplorationState(t, store.SQLDB(), created.Id, "Orders Updated", "active")
	archive := savedAPICall(t, server.Routes(), token, http.MethodPost, itemPath+"/archive", "", map[string]string{
		"Idempotency-Key": "generated-archive-1", "If-Match": updatedETag,
	})
	if archive.Code != http.StatusOK {
		t.Fatalf("archive status = %d, want %d (%s)", archive.Code, http.StatusOK, archive.Body.String())
	}
	assertSavedExplorationState(t, store.SQLDB(), created.Id, "Orders Updated", "archived")
}

func TestSavedExplorationGeneratedRouterQueriesListGetPaginationAndAuthorization(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	owner := testPlatformPrincipal(t, ctx, store, "saved-api-query-owner@example.com", "Saved API Query Owner")
	viewer := testPrincipal(t, ctx, store, "saved-api-query-viewer@example.com", "Saved API Query Viewer")
	ownerToken := testAPIToken(t, ctx, store, owner.ID, "saved-api-query-owner")
	viewerToken := testAPIToken(t, ctx, store, viewer.ID, "saved-api-query-viewer")
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	server, err := assembleRuntimeChecked(ctx, fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth}))
	if err != nil {
		t.Fatalf("assemble runtime: %v", err)
	}

	const path = "/api/v1/projects/project:test/saved-explorations"
	const repeatedBody = `{"title":"Repeated","visibility":"private","spec":{"schemaVersion":1,"modelId":"test","datasetId":"orders","dimensions":[{"field":"orders.status"}],"metrics":[{"field":"order_count"}],"filters":[],"sort":[],"limit":100}}`
	created := make([]analyticsgen.SavedExplorationResponse, 0, 3)
	etags := make([]string, 0, 3)
	for _, key := range []string{"query-create-1", "query-create-2"} {
		recorder := savedAPICall(t, server.Routes(), ownerToken, http.MethodPost, path, repeatedBody, map[string]string{"Idempotency-Key": key})
		if recorder.Code != http.StatusCreated {
			t.Fatalf("repeated create status = %d, want %d (%s)", recorder.Code, http.StatusCreated, recorder.Body.String())
		}
		var response analyticsgen.SavedExplorationResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode repeated create: %v", err)
		}
		created = append(created, response)
		etags = append(etags, recorder.Header().Get("ETag"))
	}
	explicit := savedAPICall(t, server.Routes(), ownerToken, http.MethodPost, path, `{"title":"Explicit","slug":"custom-api","visibility":"private","spec":{"schemaVersion":1,"modelId":"test","datasetId":"orders","dimensions":[{"field":"orders.status"}],"metrics":[{"field":"order_count"}],"filters":[],"sort":[],"limit":100}}`, map[string]string{"Idempotency-Key": "query-create-explicit"})
	if explicit.Code != http.StatusCreated {
		t.Fatalf("explicit create status = %d, want %d (%s)", explicit.Code, http.StatusCreated, explicit.Body.String())
	}
	var explicitResponse analyticsgen.SavedExplorationResponse
	if err := json.Unmarshal(explicit.Body.Bytes(), &explicitResponse); err != nil {
		t.Fatalf("decode explicit create: %v", err)
	}
	if explicitResponse.Slug != "custom-api" {
		t.Fatalf("explicit slug = %q, want custom-api", explicitResponse.Slug)
	}
	created = append(created, explicitResponse)
	etags = append(etags, explicit.Header().Get("ETag"))
	if created[0].Slug == created[1].Slug || len(created[0].Slug) > saved.MaxSlugLength || len(created[1].Slug) > saved.MaxSlugLength {
		t.Fatalf("repeated generated slugs = %q, %q, want distinct values <= %d", created[0].Slug, created[1].Slug, saved.MaxSlugLength)
	}

	itemPath := path + "/" + created[0].Id
	get := savedAPICall(t, server.Routes(), ownerToken, http.MethodGet, itemPath, "", nil)
	if get.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want %d (%s)", get.Code, http.StatusOK, get.Body.String())
	}
	if got := get.Header().Get("ETag"); got != etags[0] {
		t.Fatalf("GET ETag = %q, want create ETag %q", got, etags[0])
	}
	var reopened analyticsgen.SavedExplorationResponse
	if err := json.Unmarshal(get.Body.Bytes(), &reopened); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	if reopened.Id != created[0].Id || reopened.Title != "Repeated" || reopened.Spec.ModelID != "test" {
		t.Fatalf("GET response = %#v, want persisted working copy", reopened)
	}

	first := savedAPICall(t, server.Routes(), ownerToken, http.MethodGet, path+"?limit=2", "", nil)
	if first.Code != http.StatusOK {
		t.Fatalf("first list status = %d, want %d (%s)", first.Code, http.StatusOK, first.Body.String())
	}
	var firstPage analyticsgen.SavedExplorationListResponse
	if err := json.Unmarshal(first.Body.Bytes(), &firstPage); err != nil {
		t.Fatalf("decode first list: %v", err)
	}
	if len(firstPage.Items) != 2 || firstPage.Page.NextCursor == nil {
		t.Fatalf("first list = %#v, want two items and a continuation cursor", firstPage)
	}
	if firstPage.Items[0].Id >= firstPage.Items[1].Id {
		t.Fatalf("first list IDs are not stable ascending order: %q, %q", firstPage.Items[0].Id, firstPage.Items[1].Id)
	}
	second := savedAPICall(t, server.Routes(), ownerToken, http.MethodGet, path+"?limit=2&pageToken="+url.QueryEscape(*firstPage.Page.NextCursor), "", nil)
	if second.Code != http.StatusOK {
		t.Fatalf("second list status = %d, want %d (%s)", second.Code, http.StatusOK, second.Body.String())
	}
	var secondPage analyticsgen.SavedExplorationListResponse
	if err := json.Unmarshal(second.Body.Bytes(), &secondPage); err != nil {
		t.Fatalf("decode second list: %v", err)
	}
	if len(secondPage.Items) != 1 || secondPage.Page.NextCursor != nil {
		t.Fatalf("second list = %#v, want final item and no cursor", secondPage)
	}
	seen := map[string]bool{firstPage.Items[0].Id: true, firstPage.Items[1].Id: true, secondPage.Items[0].Id: true}
	for _, response := range created {
		if !seen[response.Id] {
			t.Fatalf("paginated list omitted created exploration %q", response.Id)
		}
	}

	viewerList := savedAPICall(t, server.Routes(), viewerToken, http.MethodGet, path, "", nil)
	if viewerList.Code != http.StatusOK {
		t.Fatalf("viewer list status = %d, want %d (%s)", viewerList.Code, http.StatusOK, viewerList.Body.String())
	}
	var viewerPage analyticsgen.SavedExplorationListResponse
	if err := json.Unmarshal(viewerList.Body.Bytes(), &viewerPage); err != nil {
		t.Fatalf("decode viewer list: %v", err)
	}
	if len(viewerPage.Items) != 0 || viewerPage.Page.NextCursor != nil {
		t.Fatalf("viewer list = %#v, want private rows hidden", viewerPage)
	}
	viewerGet := savedAPICall(t, server.Routes(), viewerToken, http.MethodGet, itemPath, "", nil)
	if viewerGet.Code != http.StatusNotFound {
		t.Fatalf("viewer GET status = %d, want %d (%s)", viewerGet.Code, http.StatusNotFound, viewerGet.Body.String())
	}
	unauthenticated := savedAPICall(t, server.Routes(), "", http.MethodGet, path, "", nil)
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list status = %d, want %d (%s)", unauthenticated.Code, http.StatusUnauthorized, unauthenticated.Body.String())
	}

	tooLongTitle := strings.Repeat("T", saved.MaxTitleLength+1)
	tooLongCreateBody := strings.Replace(repeatedBody, `"title":"Repeated"`, `"title":"`+tooLongTitle+`"`, 1)
	tooLongCreate := savedAPICall(t, server.Routes(), ownerToken, http.MethodPost, path, tooLongCreateBody, map[string]string{"Idempotency-Key": "query-create-too-long"})
	if tooLongCreate.Code != http.StatusUnprocessableEntity {
		t.Fatalf("max-title create status = %d, want %d (%s)", tooLongCreate.Code, http.StatusUnprocessableEntity, tooLongCreate.Body.String())
	}
	invalidSlug := strings.Replace(repeatedBody, `"title":"Repeated"`, `"title":"Invalid slug"`, 1)
	invalidSlug = strings.Replace(invalidSlug, `"visibility":"private"`, `"slug":"Not Valid","visibility":"private"`, 1)
	invalidSlugResponse := savedAPICall(t, server.Routes(), ownerToken, http.MethodPost, path, invalidSlug, map[string]string{"Idempotency-Key": "query-create-invalid-slug"})
	if invalidSlugResponse.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid explicit slug status = %d, want %d (%s)", invalidSlugResponse.Code, http.StatusUnprocessableEntity, invalidSlugResponse.Body.String())
	}
	tooLongDuplicate := savedAPICall(t, server.Routes(), ownerToken, http.MethodPost, itemPath+"/duplicate", `{"title":"`+tooLongTitle+`"}`, map[string]string{"Idempotency-Key": "query-duplicate-too-long", "If-Match": etags[0]})
	if tooLongDuplicate.Code != http.StatusUnprocessableEntity {
		t.Fatalf("max-title duplicate status = %d, want %d (%s)", tooLongDuplicate.Code, http.StatusUnprocessableEntity, tooLongDuplicate.Body.String())
	}
	exactTitle := strings.Repeat("M", saved.MaxTitleLength)
	exactTitleBody := strings.Replace(repeatedBody, `"title":"Repeated"`, `"title":"`+exactTitle+`"`, 1)
	exactTitleCreate := savedAPICall(t, server.Routes(), ownerToken, http.MethodPost, path, exactTitleBody, map[string]string{"Idempotency-Key": "query-create-max-title"})
	if exactTitleCreate.Code != http.StatusCreated {
		t.Fatalf("exact max-title create status = %d, want %d (%s)", exactTitleCreate.Code, http.StatusCreated, exactTitleCreate.Body.String())
	}
	var exactTitleResponse analyticsgen.SavedExplorationResponse
	if err := json.Unmarshal(exactTitleCreate.Body.Bytes(), &exactTitleResponse); err != nil {
		t.Fatalf("decode exact max-title response: %v", err)
	}
	if len(exactTitleResponse.Title) != saved.MaxTitleLength || len(exactTitleResponse.Slug) == 0 || len(exactTitleResponse.Slug) > saved.MaxSlugLength {
		t.Fatalf("exact max-title response title/slug lengths = %d/%d, want %d/1-%d", len(exactTitleResponse.Title), len(exactTitleResponse.Slug), saved.MaxTitleLength, saved.MaxSlugLength)
	}
}

func savedAPICall(t *testing.T, handler http.Handler, token, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var payload *bytes.Reader
	if body == "" {
		payload = bytes.NewReader(nil)
	} else {
		payload = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, path, payload)
	req.Header.Set("Accept", "application/json")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func assertSavedExplorationState(t *testing.T, db *sql.DB, explorationID, wantTitle, wantStatus string) {
	t.Helper()
	var title, status string
	if err := db.QueryRowContext(context.Background(), `SELECT title, status FROM saved_explorations WHERE exploration_id = ?`, explorationID).Scan(&title, &status); err != nil {
		t.Fatalf("read saved exploration state: %v", err)
	}
	if title != wantTitle || status != wantStatus {
		t.Fatalf("saved exploration state = title %q status %q, want title %q status %q", title, status, wantTitle, wantStatus)
	}
}

func countSavedExplorations(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM saved_explorations`).Scan(&count); err != nil {
		t.Fatalf("count saved explorations: %v", err)
	}
	return count
}
