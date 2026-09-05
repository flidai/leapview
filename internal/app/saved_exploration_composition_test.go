package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	analyticsgen "github.com/flidai/leapview/internal/analytics/api/gen"
	saved "github.com/flidai/leapview/internal/analytics/exploration/saved"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	"github.com/flidai/leapview/internal/platform/transaction"
	projecthttp "github.com/flidai/leapview/internal/project/http"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

type savedCompositionProvider struct{}

func (savedCompositionProvider) Acquire(context.Context) (projectruntime.Lease, error) {
	return nil, errors.New("saved composition test provider is not executable")
}

func TestNewSavedExplorationServiceRequiresCompleteComposition(t *testing.T) {
	store := testStore(t)
	base := SavedExplorationServiceOptions{
		Database:            store.SQLDB(),
		AuditIntentRecorder: access.AuditIntentRecorderFunc(func(context.Context, transaction.Transaction, access.AuditIntent) error { return nil }),
		AccessModule:        savedAdapterAccessStub{},
		Runtime:             savedCompositionProvider{},
		Admitter:            &savedAdapterAdmission{},
		AuditRecorder:       &savedAdapterAudit{},
	}

	if service, err := NewSavedExplorationService(base); err != nil || service == nil {
		t.Fatalf("complete composition: service=%v err=%v", service, err)
	}

	tests := []struct {
		name   string
		mutate func(*SavedExplorationServiceOptions)
	}{
		{name: "database", mutate: func(options *SavedExplorationServiceOptions) { options.Database = nil }},
		{name: "audit intent recorder", mutate: func(options *SavedExplorationServiceOptions) { options.AuditIntentRecorder = nil }},
		{name: "access module", mutate: func(options *SavedExplorationServiceOptions) { options.AccessModule = nil }},
		{name: "runtime provider", mutate: func(options *SavedExplorationServiceOptions) { options.Runtime = nil }},
		{name: "workload admitter", mutate: func(options *SavedExplorationServiceOptions) { options.Admitter = nil }},
		{name: "canonical audit recorder", mutate: func(options *SavedExplorationServiceOptions) { options.AuditRecorder = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := base
			test.mutate(&options)
			if service, err := NewSavedExplorationService(options); err == nil || service != nil {
				t.Fatalf("incomplete composition: service=%v err=%v", service, err)
			}
		})
	}
}

func TestSavedExplorationRuntimeCompositionMountsWithPersistence(t *testing.T) {
	store := testStore(t)
	ctx := t.Context()
	principal := testPlatformPrincipal(t, ctx, store, "saved-composition@example.com", "Saved Composition")
	token := testAPIToken(t, ctx, store, principal.ID, "saved-composition")
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	server, err := assembleRuntimeChecked(ctx, fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, AnalyticsModule: analyticsmodule.NewSurface(nil, nil)}))
	if err != nil {
		t.Fatalf("assemble runtime: %v", err)
	}
	if server.runtime.savedExplorationService == nil {
		t.Fatal("saved exploration service is not composed with persistence")
	}
	if server.routes.projectBrowser == nil || server.routes.projectBrowser.SavedExplorations == nil {
		t.Fatal("saved exploration service is not bound to the project browser")
	}
	if server.routes.projectBrowser.BeginSavedExplorationCommand == nil || server.routes.projectBrowser.ExecuteSavedExplorationCommand == nil {
		t.Fatal("saved exploration generated command executor is not bound to the project browser")
	}
	if server.routes.projectBrowser.SavedExplorationCommands.Create.OperationID() != analyticsgen.GenUIActionCreateSavedExploration().OperationID() ||
		server.routes.projectBrowser.SavedExplorationCommands.Update.OperationID() != analyticsgen.GenUIActionUpdateSavedExploration().OperationID() ||
		server.routes.projectBrowser.SavedExplorationCommands.Duplicate.OperationID() != analyticsgen.GenUIActionDuplicateSavedExploration().OperationID() ||
		server.routes.projectBrowser.SavedExplorationCommands.Archive.OperationID() != analyticsgen.GenUIActionArchiveSavedExploration().OperationID() {
		t.Fatalf("saved exploration browser command bindings = %#v, want generated identities", server.routes.projectBrowser.SavedExplorationCommands)
	}
	if server.platform.apiGenServers.Analytics == nil {
		t.Fatal("analytics generated API server is not mounted")
	}

	body := []byte(`{"title":"Orders","slug":"orders","visibility":"private","spec":{"schemaVersion":1,"modelId":"test","datasetId":"orders","dimensions":[{"field":"orders.status"}],"metrics":[{"field":"order_count"}],"filters":[],"sort":[],"limit":100}}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project:test/saved-explorations", bytes.NewReader(body))
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Idempotency-Key", "composition-create-1")
	request.Header.Set("X-Request-ID", "composition-request-1")
	request.Header.Set("X-Correlation-ID", "composition-correlation-1")
	create := httptest.NewRecorder()
	server.Routes().ServeHTTP(create, request)
	if create.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d (%s)", create.Code, http.StatusCreated, create.Body.String())
	}
	createETag := create.Header().Get("ETag")
	if createETag == "" {
		t.Fatal("create response did not include an ETag")
	}
	var created analyticsgen.SavedExplorationResponse
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.Id == "" || created.OwnerPrincipalId != principal.ID || created.Status != analyticsgen.SavedExplorationStatusActive || created.Spec.ModelID != "test" {
		t.Fatalf("create response = %#v, want persisted authenticated exploration", created)
	}
	// The browser mutation callback normally populates this token from the
	// service's MutationResult. This direct generated-command composition check
	// supplies the same independently returned CAS observation explicitly.
	attestedRevision := saved.RevisionToken{RevisionID: saved.RevisionID(created.Revision.RevisionId), Number: uint64(created.Revision.Number), ContentHash: created.Revision.ContentHash}
	uiInvocation := projecthttp.SavedExplorationCommandInvocation{
		Action: "update", Project: "project:test", Resource: created.Id,
		IdempotencyKey: "ui:composition-update-1", RequestID: "composition-update-1", CorrelationID: "composition-update-1",
		Revision:            saved.RevisionToken{RevisionID: saved.RevisionID(created.Revision.RevisionId), Number: uint64(created.Revision.Number), ContentHash: created.Revision.ContentHash},
		ConcurrencyRevision: &attestedRevision,
	}
	started, err := server.routes.projectBrowser.BeginSavedExplorationCommand(ctx, uiInvocation)
	if err != nil {
		t.Fatalf("begin generated browser update command: %v", err)
	}
	callbackCalls := 0
	if err := server.routes.projectBrowser.ExecuteSavedExplorationCommand(started, uiInvocation, func(context.Context) error {
		callbackCalls++
		return nil
	}); err != nil {
		t.Fatalf("execute generated browser update command: %v", err)
	}
	if callbackCalls != 1 {
		t.Fatalf("generated browser update callback count = %d, want one", callbackCalls)
	}
	wantLocation := "/api/v1/projects/project:test/saved-explorations/" + created.Id
	if got := create.Header().Get("Location"); got != wantLocation {
		t.Fatalf("create Location = %q, want %q", got, wantLocation)
	}

	var (
		storedOwner, storedStatus, storedModel, storedRevisionID, storedContentHash string
		storedRevisionNumber                                                        int64
	)
	if err := store.SQLDB().QueryRowContext(ctx, `
		SELECT owner_principal_id, status, semantic_model_id,
		       current_revision_id, current_revision_number, current_content_hash
		FROM saved_explorations
		WHERE project_id = ? AND exploration_id = ?`, "project:test", created.Id).Scan(
		&storedOwner, &storedStatus, &storedModel, &storedRevisionID, &storedRevisionNumber, &storedContentHash); err != nil {
		t.Fatalf("read persisted lifecycle: %v", err)
	}
	if storedOwner != principal.ID || storedStatus != "active" || storedModel != "test" || storedRevisionNumber != 1 || storedRevisionID == "" || storedContentHash == "" {
		t.Fatalf("persisted lifecycle = owner=%q status=%q model=%q revision=%q/%d hash=%q", storedOwner, storedStatus, storedModel, storedRevisionID, storedRevisionNumber, storedContentHash)
	}

	var (
		operationActor, operationFingerprint, operationRequestID, operationCorrelationID string
		operationVersion                                                                 int64
	)
	if err := store.SQLDB().QueryRowContext(ctx, `
		SELECT actor_id, request_fingerprint, evidence_version,
		       evidence_request_id, evidence_correlation_id
		FROM saved_exploration_operations
		WHERE project_id = ? AND operation_kind = 'create' AND idempotency_key = ?`,
		"project:test", "composition-create-1").Scan(&operationActor, &operationFingerprint, &operationVersion, &operationRequestID, &operationCorrelationID); err != nil {
		t.Fatalf("read persisted mutation evidence: %v", err)
	}
	if operationActor != principal.ID || operationFingerprint == "" || operationVersion != 1 || operationRequestID != "composition-request-1" || operationCorrelationID != "composition-correlation-1" {
		t.Fatalf("persisted mutation evidence = actor=%q fingerprint=%q version=%d request=%q correlation=%q", operationActor, operationFingerprint, operationVersion, operationRequestID, operationCorrelationID)
	}

	var auditMetadata string
	if err := store.SQLDB().QueryRowContext(ctx, `
		SELECT metadata_json
		FROM audit_outbox
		WHERE aggregate_key = ? AND action = 'saved_exploration.created'`, "saved_exploration:project:test:"+created.Id).Scan(&auditMetadata); err != nil {
		t.Fatalf("read durable audit evidence: %v", err)
	}
	var audit struct {
		Payload struct {
			MutationEvidenceVersion int64  `json:"mutationEvidenceVersion"`
			ActorID                 string `json:"actorId"`
			Action                  string `json:"action"`
			IdempotencyKey          string `json:"idempotencyKey"`
			Fingerprint             string `json:"fingerprint"`
			RequestID               string `json:"requestId"`
			CorrelationID           string `json:"correlationId"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(auditMetadata), &audit); err != nil {
		t.Fatalf("decode durable audit evidence: %v", err)
	}
	if audit.Payload.MutationEvidenceVersion != 1 || audit.Payload.ActorID != principal.ID || audit.Payload.Action != "create" || audit.Payload.IdempotencyKey != "composition-create-1" || audit.Payload.Fingerprint != operationFingerprint || audit.Payload.RequestID != "composition-request-1" || audit.Payload.CorrelationID != "composition-correlation-1" {
		t.Fatalf("durable audit payload = %#v", audit.Payload)
	}

	reopen := httptest.NewRequest(http.MethodGet, "/api/v1/projects/project:test/saved-explorations/"+created.Id, nil)
	reopen.Header.Set("Accept", "application/json")
	reopen.Header.Set("Authorization", "Bearer "+token)
	reopened := httptest.NewRecorder()
	server.Routes().ServeHTTP(reopened, reopen)
	if reopened.Code != http.StatusOK {
		t.Fatalf("reopen status = %d, want %d (%s)", reopened.Code, http.StatusOK, reopened.Body.String())
	}
	if got := reopened.Header().Get("ETag"); got != createETag {
		t.Fatalf("reopen ETag = %q, want create ETag %q", got, createETag)
	}
	var reopenedPayload analyticsgen.SavedExplorationResponse
	if err := json.Unmarshal(reopened.Body.Bytes(), &reopenedPayload); err != nil {
		t.Fatalf("decode reopen response: %v", err)
	}
	if reopenedPayload.Id != created.Id || reopenedPayload.OwnerPrincipalId != principal.ID || reopenedPayload.Title != "Orders" || reopenedPayload.Spec.ModelID != "test" || len(reopenedPayload.Spec.Dimensions) != 1 || reopenedPayload.Spec.Dimensions[0].Field != "orders.status" {
		t.Fatalf("reopen response = %#v, want persisted working copy", reopenedPayload)
	}
}
