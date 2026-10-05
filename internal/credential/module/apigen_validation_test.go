package module

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/credential"
	"github.com/flidai/leapview/internal/credential/encryption"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
)

func TestValidateCredentialDraftHandlerReturnsRedactedReceiptForExactSavedVersion(t *testing.T) {
	fixture := newValidationTransportFixture(t)
	response := fixture.call(t, fixture.versionID, `{"expectedBindingRevision":41}`)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d cache-control=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode validation response: %v", err)
	}
	if len(body) != 6 || body["receiptId"] == "" || body["versionId"] != fixture.versionID ||
		body["bindingRevision"] != float64(41) || body["state"] != "validated" ||
		body["validatedAt"] != "2026-09-28T10:00:00Z" || body["expiresAt"] != "2026-09-28T10:05:00Z" {
		t.Fatalf("validation response is not the expected receipt DTO: %#v", body)
	}
	if _, err := uuid.Parse(body["receiptId"].(string)); err != nil {
		t.Fatalf("receipt ID is invalid: %#v", body["receiptId"])
	}
	for _, forbidden := range []string{
		fixture.secret, fixture.target.BindingID, fixture.target.ConfigurationDigest,
		"transport-key-id", "ciphertext-sentinel", "password", "connection_string", "active", "activated",
	} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("response disclosed validation internals %q: %s", forbidden, response.Body.String())
		}
	}
	if fixture.probe.probeCalls != 1 || fixture.probe.versionID != fixture.versionID || !fixture.probe.fieldsMatched || len(fixture.probe.fields) != 0 {
		t.Fatalf("probe did not use the saved version fields: calls=%d version=%q fields=%#v", fixture.probe.probeCalls, fixture.probe.versionID, fixture.probe.fields)
	}
	if len(fixture.repository.receipts) != 1 || fixture.repository.receipts[0].Binding.VersionID != fixture.versionID ||
		fixture.repository.receipts[0].BindingRevision != 41 || len(fixture.repository.audits) != 1 {
		t.Fatalf("successful probe did not persist one exact receipt and audit: receipts=%#v audits=%d", fixture.repository.receipts, len(fixture.repository.audits))
	}
	if fixture.repository.stored.Metadata.Binding.VersionID != fixture.versionID {
		t.Fatal("validation changed the saved draft or implied credential activation")
	}
}

func TestValidateCredentialDraftHandlerRejectsMalformedRevisionBodiesWithoutEchoing(t *testing.T) {
	const secret = "revision-body-sentinel"
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "duplicate revision", body: `{"expectedBindingRevision":41,"expectedBindingRevision":42}`},
		{name: "unknown member", body: `{"expectedBindingRevision":41,"unexpected":"` + secret + `"}`},
		{name: "null revision", body: `{"expectedBindingRevision":null}`},
		{name: "wrong case revision", body: `{"ExpectedBindingRevision":41}`},
		{name: "wrong type revision", body: `{"expectedBindingRevision":"41"}`},
		{name: "nonpositive revision", body: `{"expectedBindingRevision":0}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newValidationTransportFixture(t)
			response := fixture.call(t, fixture.versionID, test.body)
			if response.Code != http.StatusBadRequest || fixture.probe.probeCalls != 0 ||
				len(fixture.repository.receipts) != 0 || fixture.keys.decryptCalls != 0 {
				t.Fatalf("malformed revision reached validation: status=%d probes=%d decrypts=%d receipts=%d body=%s",
					response.Code, fixture.probe.probeCalls, fixture.keys.decryptCalls, len(fixture.repository.receipts), response.Body.String())
			}
			if strings.Contains(response.Body.String(), secret) || strings.Contains(response.Body.String(), fixture.secret) {
				t.Fatalf("malformed request value was echoed: %s", response.Body.String())
			}
		})
	}
}

func TestValidateCredentialDraftHandlerBoundsBodyAndRequiresValidationService(t *testing.T) {
	t.Run("body limit", func(t *testing.T) {
		fixture := newValidationTransportFixture(t)
		response := fixture.call(t, fixture.versionID, strings.Repeat(" ", 1025))
		if response.Code != http.StatusRequestEntityTooLarge || fixture.probe.probeCalls != 0 || fixture.keys.decryptCalls != 0 {
			t.Fatalf("oversized body reached validation: status=%d probes=%d decrypts=%d body=%s", response.Code, fixture.probe.probeCalls, fixture.keys.decryptCalls, response.Body.String())
		}
	})
	t.Run("validation service missing", func(t *testing.T) {
		fixture := newValidationTransportFixture(t)
		fixture.config.Validation = nil
		response := fixture.call(t, fixture.versionID, `{"expectedBindingRevision":41}`)
		if response.Code != http.StatusServiceUnavailable || fixture.probe.probeCalls != 0 || len(fixture.repository.receipts) != 0 {
			t.Fatalf("missing validation service status=%d probes=%d receipts=%d body=%s", response.Code, fixture.probe.probeCalls, len(fixture.repository.receipts), response.Body.String())
		}
	})
}

func TestValidateCredentialDraftHandlerRejectsStaleRevisionAndUnknownVersion(t *testing.T) {
	t.Run("stale binding revision", func(t *testing.T) {
		fixture := newValidationTransportFixture(t)
		response := fixture.call(t, fixture.versionID, `{"expectedBindingRevision":42}`)
		if response.Code != http.StatusConflict || fixture.keys.decryptCalls != 0 || fixture.probe.probeCalls != 0 || len(fixture.repository.receipts) != 0 {
			t.Fatalf("stale revision was not rejected before decrypt/probe: status=%d decrypts=%d probes=%d receipts=%d body=%s",
				response.Code, fixture.keys.decryptCalls, fixture.probe.probeCalls, len(fixture.repository.receipts), response.Body.String())
		}
	})
	t.Run("unknown version", func(t *testing.T) {
		fixture := newValidationTransportFixture(t)
		unknownVersion := uuid.NewString()
		response := fixture.call(t, unknownVersion, `{"expectedBindingRevision":41}`)
		if response.Code != http.StatusNotFound || fixture.keys.decryptCalls != 0 || fixture.probe.probeCalls != 0 || len(fixture.repository.receipts) != 0 {
			t.Fatalf("unknown version reached decrypt/probe: status=%d decrypts=%d probes=%d receipts=%d body=%s",
				response.Code, fixture.keys.decryptCalls, fixture.probe.probeCalls, len(fixture.repository.receipts), response.Body.String())
		}
	})
}

func TestValidateCredentialDraftHandlerRedactsProbeFailure(t *testing.T) {
	fixture := newValidationTransportFixture(t)
	fixture.probe.err = errors.New("provider response exposed probe-secret and endpoint-ref")
	response := fixture.call(t, fixture.versionID, `{"expectedBindingRevision":41}`)
	if response.Code != http.StatusUnprocessableEntity || fixture.probe.probeCalls != 1 || len(fixture.repository.receipts) != 0 {
		t.Fatalf("failed probe result: status=%d probes=%d receipts=%d body=%s", response.Code, fixture.probe.probeCalls, len(fixture.repository.receipts), response.Body.String())
	}
	for _, forbidden := range []string{
		"probe-secret", "endpoint-ref", fixture.secret, fixture.target.BindingID,
		fixture.target.ConfigurationDigest, "transport-key-id", "ciphertext-sentinel",
	} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("probe failure response disclosed %q: %s", forbidden, response.Body.String())
		}
	}
}

func TestValidateCredentialDraftHandlerRechecksAuthorityAfterProbe(t *testing.T) {
	fixture := newValidationTransportFixture(t)
	fixture.probe.afterProbe = func() { fixture.authority.allowed = false }
	response := fixture.call(t, fixture.versionID, `{"expectedBindingRevision":41}`)
	if response.Code != http.StatusForbidden || fixture.probe.probeCalls != 1 || len(fixture.repository.receipts) != 0 {
		t.Fatalf("authority revocation after probe was not enforced: status=%d probes=%d receipts=%d body=%s", response.Code, fixture.probe.probeCalls, len(fixture.repository.receipts), response.Body.String())
	}
	if len(fixture.authority.actions) != 3 || fixture.authority.actions[0] != access.ActionConnectionManage ||
		fixture.authority.actions[1] != access.ActionConnectionUse || fixture.authority.actions[2] != access.ActionConnectionManage {
		t.Fatalf("authority was not checked before and after provider I/O: %#v", fixture.authority.actions)
	}
	if strings.Contains(response.Body.String(), fixture.secret) || strings.Contains(response.Body.String(), fixture.target.ConfigurationDigest) {
		t.Fatalf("authorization response disclosed credential or destination metadata: %s", response.Body.String())
	}
}

type validationTransportFixture struct {
	config     CredentialDraftAPIGenConfig
	project    string
	targetID   string
	connection string
	versionID  string
	secret     string
	target     credential.ValidationTarget
	repository *validationTransportRepository
	keys       *validationTransportKeyring
	probe      *validationTransportProbe
	authority  *validationTransportAuthorizer
}

func newValidationTransportFixture(t *testing.T) validationTransportFixture {
	t.Helper()
	const (
		actor       = "principal_test"
		project     = "project_one"
		targetID    = "lvinst_0123456789abcdefghijklmnopqrstuv"
		connection  = "warehouse"
		environment = "production"
		secret      = "transport-secret-must-never-be-returned"
	)
	resource := credential.Resource{ScopeKind: "connection", TargetID: targetID, ProjectID: project, Environment: environment, ResourceID: connection}
	destination := "sha256:" + strings.Repeat("a", 64)
	scope := credential.Scope{
		Resource: resource, OwnerID: "customer_one", Purpose: connectionCredentialPurpose,
		Provider: "postgres", Destination: destination,
	}
	bindings := scopeBindingReader{binding: TargetConnectionBinding{
		TargetID: targetID, ProjectID: project, Environment: environment, ConnectionID: connection,
		ConnectorKind: "postgres", AuthenticationMode: "external_bundle", EndpointConfigHash: destination,
	}}
	scopes := connectionCredentialScopeResolver{
		instanceID: targetID, environment: environment, ownerReader: scopeOwnerReader{owner: scope.OwnerID},
		currentProject: func(context.Context) (projectgraph.ResourceID, error) { return projectgraph.ResourceID(project), nil },
		bindings:       bindings,
	}
	versionID := uuid.NewString()
	binding := encryption.Binding{
		DeploymentID: targetID, OwnerID: scope.OwnerID, ScopeKind: resource.ScopeKind,
		TargetID: targetID, ProjectID: project, Environment: environment, ResourceID: connection,
		Purpose: scope.Purpose, Provider: scope.Provider, Destination: destination, VersionID: versionID,
	}
	plaintext, err := json.Marshal(map[string]string{"password": secret})
	if err != nil {
		t.Fatal(err)
	}
	stored := credential.StoredVersion{
		Metadata: credential.Metadata{
			Binding: binding, ActorID: "draft_actor", CreatedAt: time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC),
		},
		Envelope: encryption.Envelope{Format: "test-only", KeyID: "transport-key-id", Ciphertext: []byte("ciphertext-sentinel")},
	}
	repository := &validationTransportRepository{stored: stored}
	keys := &validationTransportKeyring{deployment: targetID, plaintext: plaintext}
	target := credential.ValidationTarget{
		Scope: scope, BindingID: "connection-binding-production", BindingRevision: 41,
		ConfigurationDigest: "sha256:" + strings.Repeat("b", 64),
	}
	probe := &validationTransportProbe{target: target, expectedSecret: secret}
	authority := &validationTransportAuthorizer{allowed: true}
	validation, err := credential.NewValidationService(repository, keys, scopes, authority, probe, func() time.Time {
		return time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatal(err)
	}
	draftService, err := credential.NewService(&apiTestRepository{}, apiTestEncryptor{deployment: targetID}, scopes, authority)
	if err != nil {
		t.Fatal(err)
	}
	return validationTransportFixture{
		config: CredentialDraftAPIGenConfig{
			Service: draftService, Validation: validation, Environment: environment,
			CurrentPrincipal: func(*http.Request) (string, bool) { return actor, true },
		},
		project: project, targetID: targetID, connection: connection, versionID: versionID, secret: secret,
		target: target, repository: repository, keys: keys, probe: probe, authority: authority,
	}
}

func (fixture validationTransportFixture) call(t *testing.T, version, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/credential-drafts/"+version+"/validate", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	credentialDraftAPIGenDispatcher{config: fixture.config}.ValidateCredentialDraft(
		response, request, fixture.project, fixture.targetID, fixture.connection, version,
	)
	return response
}

type validationTransportRepository struct {
	stored   credential.StoredVersion
	receipts []credential.ValidationReceipt
	audits   []access.AuditIntent
}

func (repository *validationTransportRepository) GetStoredDraft(_ context.Context, deploymentID, ownerID string, resource credential.Resource, versionID string) (credential.StoredVersion, error) {
	binding := repository.stored.Metadata.Binding
	storedResource := credential.Resource{
		ScopeKind: binding.ScopeKind, TargetID: binding.TargetID, ProjectID: binding.ProjectID,
		Environment: binding.Environment, ResourceID: binding.ResourceID,
	}
	if binding.DeploymentID != deploymentID || binding.OwnerID != ownerID || storedResource != resource || binding.VersionID != versionID {
		return credential.StoredVersion{}, credential.ErrNotFound
	}
	return repository.stored, nil
}

func (repository *validationTransportRepository) SaveValidation(_ context.Context, receipt credential.ValidationReceipt, audit access.AuditIntent) error {
	repository.receipts = append(repository.receipts, receipt)
	repository.audits = append(repository.audits, audit)
	return nil
}

type validationTransportKeyring struct {
	deployment   string
	plaintext    []byte
	decryptCalls int
}

func (keys *validationTransportKeyring) DeploymentID() string { return keys.deployment }

func (keys *validationTransportKeyring) Decrypt(_ encryption.Binding, _ encryption.Envelope) ([]byte, error) {
	keys.decryptCalls++
	return append([]byte(nil), keys.plaintext...), nil
}

type validationTransportAuthorizer struct {
	allowed bool
	actions []access.Action
}

func (authorizer *validationTransportAuthorizer) RequirePermission(_ context.Context, _ string, pair access.PermissionPair) error {
	authorizer.actions = append(authorizer.actions, pair.Action)
	if !authorizer.allowed {
		return credential.ErrForbidden
	}
	return nil
}

type validationTransportProbe struct {
	target         credential.ValidationTarget
	expectedSecret string
	versionID      string
	fields         map[string]string
	fieldsMatched  bool
	probeCalls     int
	err            error
	afterProbe     func()
}

func (probe *validationTransportProbe) ResolveValidationTarget(_ context.Context, resource credential.Resource, scope credential.Scope) (credential.ValidationTarget, error) {
	if probe.target.Scope.Resource != resource || probe.target.Scope != scope {
		return credential.ValidationTarget{}, credential.ErrNotFound
	}
	return probe.target, nil
}

func (probe *validationTransportProbe) ProbeCredential(_ context.Context, _ credential.ValidationTarget, versionID string, fields map[string]string) error {
	probe.probeCalls++
	probe.versionID = versionID
	probe.fields = fields
	probe.fieldsMatched = fields["password"] == probe.expectedSecret && len(fields) == 1
	if probe.afterProbe != nil {
		probe.afterProbe()
	}
	return probe.err
}
