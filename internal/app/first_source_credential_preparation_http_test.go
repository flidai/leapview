package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	accessmodule "github.com/flidai/leapview/internal/access/module"
	apiprotocol "github.com/flidai/leapview/internal/app/api/protocol"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	platformbootstrap "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/gorilla/csrf"
	"github.com/stretchr/testify/require"
)

type firstSourcePreparationReadSpy struct {
	io.ReadCloser
	reads int
}

func (b *firstSourcePreparationReadSpy) Read(p []byte) (int, error) {
	b.reads++
	return b.ReadCloser.Read(p)
}

func TestFirstSourceCredentialPreparationSessionHTTPBoundary(t *testing.T) {
	f := newFirstSourcePreparationServiceFixture(t)
	auth, err := accessmodule.NewAuth(f.repository, accessmodule.AuthConfig{LocalAuth: true, CSRFKey: strings.Repeat("k", 32)})
	require.NoError(t, err)
	config := credentialmodule.CredentialDraftAPIGenConfig{FirstSourcePreparation: firstSourceCredentialPreparationCommands(&f.service), Environment: f.resource.Environment,
		CurrentPrincipal: func(r *http.Request) (string, bool) { principal, ok := auth.Principal(r); return principal.ID, ok }}
	project := func(ctx context.Context) (projectgraph.ResourceID, error) {
		claim, err := platformbootstrap.New(f.pool).GetProjectClaim(ctx)
		return projectgraph.ResourceID(claim.ProjectID), err
	}
	routes := newFirstSourceCredentialPreparationRoutes(config, f.resource.TargetID, project)
	require.NotNil(t, routes)
	mux := chi.NewRouter()
	mux.Use(auth.CSRFMiddleware)
	mux.Get("/csrf", func(w http.ResponseWriter, r *http.Request) { w.Header().Set("X-CSRF-Token", csrf.Token(r)) })
	// This method needs only the existing generated command/claim checks. The
	// application supplies its configured protocol; no public API auth is changed.
	mountFirstSourceCredentialPreparationRoutes(mux, routes, auth.Middleware, &apiprotocol.Protocol{})
	csrfResponse := httptest.NewRecorder()
	mux.ServeHTTP(csrfResponse, httptest.NewRequest(http.MethodGet, "http://example.com/csrf", nil))
	require.Equal(t, http.StatusOK, csrfResponse.Code)
	csrfToken := csrfResponse.Header().Get("X-CSRF-Token")
	require.NotEmpty(t, csrfToken)
	cookies := csrfResponse.Result().Cookies()
	preparePath := "/connections/" + f.resource.ResourceID + "/credential-drafts/" + f.request.VersionID + "/prepare-first-source"
	encoded, err := json.Marshal(map[string]any{"preparationId": f.request.PreparationID, "receiptId": f.request.ReceiptID, "sourceDigest": f.request.SourceDigest,
		"sourceAttestationDigest": f.request.SourceAttestationDigest, "planIdempotencyKey": f.request.PlanIdempotencyKey, "expectedTargetRevision": f.request.ExpectedTargetRevision})
	require.NoError(t, err)
	call := func(path, operation, body string, change func(*http.Request)) (*httptest.ResponseRecorder, *firstSourcePreparationReadSpy) {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "http://example.com"+path, nil)
		spy := &firstSourcePreparationReadSpy{ReadCloser: io.NopCloser(strings.NewReader(body))}
		request.Body, request.ContentLength = spy, int64(len(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json")
		request.Header.Set("Origin", "http://example.com")
		request.Header.Set("X-CSRF-Token", csrfToken)
		requestID, err := uuid.NewV7()
		require.NoError(t, err)
		request.Header.Set("X-Request-ID", requestID.String())
		request.Header.Set("X-LeapView-Operation-ID", operation)
		for _, cookie := range cookies {
			request.AddCookie(cookie)
		}
		request.AddCookie(&http.Cookie{Name: auth.SessionCookieName(), Value: f.session})
		if change != nil {
			change(request)
		}
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		return response, spy
	}
	for _, denial := range []struct {
		name, path string
		change     func(*http.Request)
	}{
		{"csrf", preparePath, func(r *http.Request) { r.Header.Del("X-CSRF-Token") }},
		{"operation-claim", preparePath, func(r *http.Request) { r.Header.Set("X-LeapView-Operation-ID", "retryCredentialActivation") }},
		{"request-id", preparePath, func(r *http.Request) { r.Header.Set("X-Request-ID", uuid.NewString()) }},
		{"idempotency-header", preparePath, func(r *http.Request) { r.Header["Idempotency-Key"] = []string{""} }},
		{"foreign-connection", strings.Replace(preparePath, "connection:warehouse", "connection:other", 1), nil},
		{"session", preparePath, func(r *http.Request) { r.Header.Del("Cookie") }},
		{"bearer", preparePath, func(r *http.Request) { r.Header.Del("Cookie"); r.Header.Set("Authorization", "Bearer invalid-bearer") }},
	} {
		t.Run(denial.name, func(t *testing.T) {
			response, spy := call(denial.path, "prepareFirstSourceCredential", string(encoded), denial.change)
			require.Contains(t, []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden}, response.Code)
			require.Zero(t, spy.reads, "authentication, CSRF and exact command/scope checks precede body reads")
			_, err := f.service.preparations.Preparation(t.Context(), f.resource.TargetID, f.request.PreparationID)
			require.ErrorIs(t, err, credentialmodule.ErrValidationNotFound)
		})
	}
	response, spy := call(preparePath, "prepareFirstSourceCredential", string(encoded), nil)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Positive(t, spy.reads)
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	var result map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	require.Len(t, result, 4)
	require.Equal(t, f.request.PreparationID, result["preparationId"])
	require.NotContains(t, response.Body.String(), f.request.ReceiptID)
	stored, err := f.service.preparations.Preparation(t.Context(), f.resource.TargetID, f.request.PreparationID)
	require.NoError(t, err)
	require.Equal(t, f.actor, stored.Intent.Receipt.ActorID)
	require.Equal(t, f.actor, stored.Intent.PublisherID)
	require.Equal(t, f.actor, stored.Intent.SourceOwnerID)
	assertAudit := func(action, operation string, expectedMetadata map[string]string) {
		t.Helper()
		var actualOperation, actualActor, kind, resource, metadata string
		require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT operation, actor_id, resource_kind, resource_id, metadata::text FROM audit.audit_event WHERE action=$1 ORDER BY occurred_at DESC LIMIT 1`, action).
			Scan(&actualOperation, &actualActor, &kind, &resource, &metadata))
		require.Equal(t, operation, actualOperation)
		require.Equal(t, f.actor, actualActor)
		require.Equal(t, "connection", kind)
		require.Equal(t, f.resource.ResourceID, resource)
		var actualMetadata map[string]string
		require.NoError(t, json.Unmarshal([]byte(metadata), &actualMetadata))
		require.Equal(t, expectedMetadata, actualMetadata, "audit contains only canonical safe command metadata")
	}
	assertAudit("credential.first_source.prepared", "prepareFirstSourceCredential", map[string]string{"preparationId": f.request.PreparationID, "intentDigest": result["intentDigest"].(string)})
	retry, _ := call(preparePath, "prepareFirstSourceCredential", string(encoded), nil)
	require.Equal(t, http.StatusOK, retry.Code)
	require.JSONEq(t, response.Body.String(), retry.Body.String())
	fresh, err := f.services.Validation.ValidateDraft(f.ctx, f.actor, f.resource, f.request.VersionID, 1)
	require.NoError(t, err)
	renewPath := "/connections/" + f.resource.ResourceID + "/first-source-preparations/" + f.request.PreparationID + "/renew"
	renewed, _ := call(renewPath, "renewFirstSourceCredentialPreparation", `{"receiptId":"`+fresh.ReceiptID+`"}`, nil)
	require.Equal(t, http.StatusOK, renewed.Code, renewed.Body.String())
	require.JSONEq(t, response.Body.String(), renewed.Body.String())
	assertAudit("credential.first_source.receipt_renewed", "renewFirstSourceCredentialPreparation", map[string]string{"preparationId": f.request.PreparationID, "receiptId": fresh.ReceiptID})
	var proofs int
	require.NoError(t, f.pool.QueryRow(t.Context(), "SELECT count(*) FROM credential.activation_request_receipt WHERE operation_id=$1", f.request.PreparationID).Scan(&proofs))
	require.Equal(t, 2, proofs)
	require.NoError(t, f.repository.DeleteSession(t.Context(), f.session))
	revoked, spy := call(renewPath, "renewFirstSourceCredentialPreparation", `{"receiptId":"`+fresh.ReceiptID+`"}`, nil)
	require.Equal(t, http.StatusUnauthorized, revoked.Code)
	require.Zero(t, spy.reads)
}
