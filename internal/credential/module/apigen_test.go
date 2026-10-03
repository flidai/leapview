package module

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	credential "github.com/flidai/leapview/internal/credential"
	credentialgen "github.com/flidai/leapview/internal/credential/api/gen"
	"github.com/flidai/leapview/internal/credential/encryption"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

func TestDecodeCredentialDraftFieldsAcceptsTypedStringMap(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/drafts", strings.NewReader(`{"fields":{"username":"report-user","password":"p\uD83D\uDD10"}}`))
	response := httptest.NewRecorder()

	fields, ok := decodeCredentialDraftFields(response, request)
	if !ok {
		t.Fatalf("decodeCredentialDraftFields failed: status=%d body=%s", response.Code, response.Body.String())
	}
	if fields["username"] != "report-user" || fields["password"] != "p🔐" {
		t.Fatalf("decoded fields = %#v", fields)
	}
}

func TestDecodeCredentialDraftFieldsRejectsMalformedBodiesWithoutEchoing(t *testing.T) {
	invalidUTF8 := append([]byte(`{"fields":{"password":"`), 0xff)
	invalidUTF8 = append(invalidUTF8, []byte(`"}}`)...)
	secret := "do-not-echo-this-secret"
	cases := []struct {
		name          string
		body          []byte
		contentLength int64
		wantStatus    int
	}{
		{name: "empty body", body: nil, wantStatus: http.StatusBadRequest},
		{name: "top level property", body: []byte(`{"fields":{"password":"` + secret + `"},"extra":true}`), wantStatus: http.StatusBadRequest},
		{name: "wrong case top level property", body: []byte(`{"Fields":{"password":"` + secret + `"}}`), wantStatus: http.StatusBadRequest},
		{name: "duplicate top level property", body: []byte(`{"fields":{"password":"` + secret + `"},"fields":{}}`), wantStatus: http.StatusBadRequest},
		{name: "duplicate secret field", body: []byte(`{"fields":{"password":"` + secret + `","password":"second"}}`), wantStatus: http.StatusBadRequest},
		{name: "null field", body: []byte(`{"fields":{"password":null}}`), wantStatus: http.StatusBadRequest},
		{name: "non string field", body: []byte(`{"fields":{"password":{"value":"` + secret + `"}}}`), wantStatus: http.StatusBadRequest},
		{name: "unpaired high surrogate", body: []byte(`{"fields":{"password":"\uD800"}}`), wantStatus: http.StatusBadRequest},
		{name: "unpaired low surrogate", body: []byte(`{"fields":{"password":"\uDC00"}}`), wantStatus: http.StatusBadRequest},
		{name: "invalid utf8", body: invalidUTF8, wantStatus: http.StatusBadRequest},
		{name: "trailing json", body: []byte(`{"fields":{"password":"` + secret + `"}} {}`), wantStatus: http.StatusBadRequest},
		{name: "truncated declared body", body: []byte(`{"fields":{"password":"` + secret + `"}}`), contentLength: int64(len(secret) + 100), wantStatus: http.StatusBadRequest},
		{name: "encoded body too large", body: bytes.Repeat([]byte("x"), int(maxCredentialDraftEncodedBodyBytes+1)), wantStatus: http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/drafts", bytes.NewReader(tc.body))
			if tc.contentLength > 0 {
				request.ContentLength = tc.contentLength
				request.Body = io.NopCloser(&shortReader{data: tc.body})
			}
			response := httptest.NewRecorder()

			fields, ok := decodeCredentialDraftFields(response, request)
			if ok || fields != nil {
				t.Fatalf("malformed body accepted: fields=%#v", fields)
			}
			if response.Code != tc.wantStatus {
				t.Fatalf("status=%d, want %d: %s", response.Code, tc.wantStatus, response.Body.String())
			}
			if strings.Contains(response.Body.String(), secret) {
				t.Fatalf("response echoed secret input: %s", response.Body.String())
			}
		})
	}
}

func TestCredentialDraftListBoundsAreStrictWhenPresent(t *testing.T) {
	validLimit := int32(25)
	zero := int32(0)
	tooLarge := int32(101)
	emptyCursor := ""
	validCursor := "e736fa40-12f2-4e57-8c09-98a163badccb"
	for _, tc := range []struct {
		name   string
		limit  *int32
		cursor *string
		valid  bool
	}{
		{name: "defaults absent", valid: true},
		{name: "valid explicit", limit: &validLimit, valid: true},
		{name: "zero explicit limit", limit: &zero},
		{name: "too large explicit limit", limit: &tooLarge},
		{name: "empty cursor", cursor: &emptyCursor},
		{name: "valid cursor", cursor: &validCursor, valid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := validCredentialDraftListParams(tc.limit, tc.cursor); got != tc.valid {
				t.Fatalf("validCredentialDraftListParams = %t, want %t", got, tc.valid)
			}
		})
	}
}

func TestCredentialDraftGeneratedDispatcherSaveListGetAndDenies(t *testing.T) {
	const (
		actor       = "principal_test"
		project     = "project_one"
		target      = "lvinst_0123456789abcdefghijklmnopqrstuv"
		connection  = "warehouse"
		deployment  = target
		environment = "production"
	)
	repository := &apiTestRepository{}
	allowed := true
	authorizer := connectionCredentialAuthorizer{authorize: func(context.Context, string, string, string, access.Action) (bool, error) { return allowed, nil }}
	bindings := scopeBindingReader{binding: TargetConnectionBinding{
		TargetID: target, ProjectID: project, Environment: environment, ConnectionID: connection,
		ConnectorKind: "postgres", AuthenticationMode: "external_bundle",
		EndpointConfigHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}
	scopes := connectionCredentialScopeResolver{
		instanceID: target, environment: environment, ownerReader: scopeOwnerReader{owner: "customer_one"},
		currentProject: func(context.Context) (projectgraph.ResourceID, error) { return projectgraph.ResourceID(project), nil },
		bindings:       bindings,
	}
	service, err := credential.NewService(repository, apiTestEncryptor{deployment: deployment}, scopes, authorizer)
	if err != nil {
		t.Fatal(err)
	}
	config := CredentialDraftAPIGenConfig{
		Service: service, Environment: environment,
		CurrentPrincipal: func(*http.Request) (string, bool) { return actor, true },
	}
	basePath := "/api/v1/projects/" + project + "/targets/" + target + "/connection-bindings/" + connection + "/credential-drafts"
	secret := "transport-secret-must-not-return"
	request := httptest.NewRequest(http.MethodPost, basePath, strings.NewReader(`{"fields":{"connection_string":"postgres://analyst@warehouse/db?password=`+secret+`"}}`))
	request.Header.Set("Content-Type", "application/json")
	request = withCredentialDraftRouteParams(request, project, target, connection, "")
	commandContext, guard, err := credentialgen.BeginGenSaveCredentialDraftCommand(request.Context(), credentialgen.GenSaveCredentialDraftCommandInvocation{
		Surface: apigencommand.SurfaceAPI, Connection: connection,
	})
	if err != nil {
		t.Fatalf("begin generated command: %v", err)
	}
	request = request.WithContext(commandContext)
	response := httptest.NewRecorder()
	if !DispatchAPIGenOperation(config, "saveCredentialDraft", slog.Default(), response, request) {
		t.Fatal("generated dispatcher did not handle saveCredentialDraft")
	}
	if response.Code != http.StatusCreated || !guard.Completed() {
		t.Fatalf("save status=%d guard completed=%t body=%s", response.Code, guard.Completed(), response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" || strings.Contains(response.Body.String(), secret) {
		t.Fatalf("save response cache policy/body is unsafe: headers=%v body=%s", response.Header(), response.Body.String())
	}
	var saved map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
		t.Fatalf("decode save response: %v", err)
	}
	versionID, _ := saved["versionId"].(string)
	if versionID == "" || len(saved) != 3 || saved["state"] != "draft" || saved["createdAt"] == nil {
		t.Fatalf("save response includes unexpected metadata: %#v", saved)
	}
	if len(repository.saved) != 1 || len(repository.audits) != 1 || strings.Contains(repository.audits[0].MetadataJSON, secret) {
		t.Fatalf("saved versions=%d audits=%d audit=%q", len(repository.saved), len(repository.audits), repository.audits[0].MetadataJSON)
	}

	listRequest := httptest.NewRequest(http.MethodGet, basePath+"?limit=1", nil)
	listRequest = withCredentialDraftRouteParams(listRequest, project, target, connection, "")
	listResponse := httptest.NewRecorder()
	if !DispatchAPIGenOperation(config, "listConnectionCredentialDrafts", nil, listResponse, listRequest) {
		t.Fatal("generated dispatcher did not handle listConnectionCredentialDrafts")
	}
	if listResponse.Code != http.StatusOK || listResponse.Header().Get("Cache-Control") != "no-store" || strings.Contains(listResponse.Body.String(), secret) {
		t.Fatalf("list status=%d headers=%v body=%s", listResponse.Code, listResponse.Header(), listResponse.Body.String())
	}
	var list map[string]any
	if err := json.Unmarshal(listResponse.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	items, _ := list["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["versionId"] != versionID {
		t.Fatalf("list response = %#v", list)
	}

	getRequest := httptest.NewRequest(http.MethodGet, basePath+"/"+versionID, nil)
	getRequest = withCredentialDraftRouteParams(getRequest, project, target, connection, versionID)
	getResponse := httptest.NewRecorder()
	if !DispatchAPIGenOperation(config, "getConnectionCredentialDraft", nil, getResponse, getRequest) {
		t.Fatal("generated dispatcher did not handle getConnectionCredentialDraft")
	}
	if getResponse.Code != http.StatusOK || getResponse.Header().Get("Cache-Control") != "no-store" || strings.Contains(getResponse.Body.String(), secret) {
		t.Fatalf("get status=%d headers=%v body=%s", getResponse.Code, getResponse.Header(), getResponse.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(getResponse.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if got["versionId"] != versionID || got["state"] != "draft" || len(got) != 3 {
		t.Fatalf("get response = %#v", got)
	}

	allowed = false
	denied := httptest.NewRecorder()
	if !DispatchAPIGenOperation(config, "getConnectionCredentialDraft", nil, denied, getRequest) || denied.Code != http.StatusForbidden {
		t.Fatalf("denied read status=%d body=%s", denied.Code, denied.Body.String())
	}
	if strings.Contains(denied.Body.String(), secret) {
		t.Fatalf("denial response echoed secret: %s", denied.Body.String())
	}
}

type apiTestRepository struct {
	saved  []credential.StoredVersion
	audits []access.AuditIntent
}

func (*apiTestRepository) ReserveEncryption(context.Context, string, string, encryption.KeyCommitment) error {
	return nil
}

func (repository *apiTestRepository) SaveDraft(_ context.Context, version credential.StoredVersion, audit access.AuditIntent) error {
	repository.saved = append(repository.saved, version)
	repository.audits = append(repository.audits, audit)
	return nil
}

func (repository *apiTestRepository) GetDraftMetadata(_ context.Context, deploymentID, ownerID string, resource credential.Resource, versionID string) (credential.Metadata, error) {
	for _, version := range repository.saved {
		metadata := version.Metadata
		binding := metadata.Binding
		if binding.DeploymentID == deploymentID && binding.OwnerID == ownerID && binding.VersionID == versionID &&
			(credential.Resource{ScopeKind: binding.ScopeKind, TargetID: binding.TargetID, ProjectID: binding.ProjectID, Environment: binding.Environment, ResourceID: binding.ResourceID}) == resource {
			return metadata, nil
		}
	}
	return credential.Metadata{}, credential.ErrNotFound
}

func (repository *apiTestRepository) ListDrafts(_ context.Context, deploymentID, ownerID string, resource credential.Resource, limit int, beforeVersionID string) (credential.DraftPage, error) {
	items := make([]credential.Metadata, 0, len(repository.saved))
	for _, version := range repository.saved {
		metadata := version.Metadata
		binding := metadata.Binding
		if binding.DeploymentID == deploymentID && binding.OwnerID == ownerID &&
			(credential.Resource{ScopeKind: binding.ScopeKind, TargetID: binding.TargetID, ProjectID: binding.ProjectID, Environment: binding.Environment, ResourceID: binding.ResourceID}) == resource {
			items = append(items, metadata)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	if beforeVersionID != "" {
		return credential.DraftPage{}, credential.ErrInvalidCursor
	}
	if len(items) > limit {
		items = items[:limit]
	}
	return credential.DraftPage{Items: items}, nil
}

type apiTestEncryptor struct{ deployment string }

func (encryptor apiTestEncryptor) DeploymentID() string { return encryptor.deployment }

func (encryptor apiTestEncryptor) Encrypt(ctx context.Context, budget encryption.Budget, binding encryption.Binding, _ []byte) (encryption.Envelope, error) {
	commitment := encryption.KeyCommitment(sha256.Sum256([]byte("credential-draft-test-key")))
	if err := budget.ReserveEncryption(ctx, binding.DeploymentID, "test-key", commitment); err != nil {
		return encryption.Envelope{}, err
	}
	return encryption.Envelope{Format: "test-only", KeyID: "test-key", Ciphertext: []byte("opaque")}, nil
}

func withCredentialDraftRouteParams(r *http.Request, project, target, connection, version string) *http.Request {
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("project", project)
	routeContext.URLParams.Add("target", target)
	routeContext.URLParams.Add("connection", connection)
	if version != "" {
		routeContext.URLParams.Add("version", version)
	}
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, routeContext))
}

type shortReader struct{ data []byte }

func (r *shortReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.ErrUnexpectedEOF
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}
