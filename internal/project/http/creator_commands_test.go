package http

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/connectionadmin"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	projectview "github.com/flidai/leapview/internal/project"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/internal/project/ui/signals"
	refreshgen "github.com/flidai/leapview/internal/refresh/api/gen"
	refreshpresentation "github.com/flidai/leapview/internal/refresh/presentation"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

func TestConnectionConfigurationCarriesReferencesButNeverSecretValues(t *testing.T) {
	configuration, err := connectionConfiguration(signals.ConnectionAdministrationCommandSignal{
		ConnectorKind: "postgres", AuthenticationMode: "external_bundle", Host: "warehouse.internal", Port: "5432",
		CredentialProjectID: "project:secrets", CredentialEnvironment: "prod", SecretPath: "/connections/warehouse", SecretKey: "bundle",
		Options: `{"sslmode":"verify-full"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if configuration.CredentialReference.SecretPath != "/connections/warehouse" || configuration.CredentialReference.SecretKey != "bundle" {
		t.Fatalf("credential reference = %#v", configuration.CredentialReference)
	}
	if configuration.Endpoint.Options["sslmode"] != "verify-full" || configuration.Endpoint.Port != 5432 {
		t.Fatalf("endpoint = %#v", configuration.Endpoint)
	}
	encoded := configuration.CredentialReference.SecretPath + configuration.CredentialReference.SecretKey
	if strings.Contains(encoded, "password") {
		t.Fatal("credential value crossed the browser command configuration")
	}
}

func TestConnectionCommandResponsesRedactWriteOnlyCredentialReferences(t *testing.T) {
	redacted := redactConnectionCommand(signals.ConnectionAdministrationCommandSignal{
		Action: "update", CredentialProjectID: "project:secrets", CredentialEnvironment: "prod",
		SecretPath: "/connections/warehouse", SecretKey: "bundle", Host: "warehouse.internal",
	})
	if redacted.CredentialProjectID != "" || redacted.CredentialEnvironment != "" || redacted.SecretPath != "" || redacted.SecretKey != "" {
		t.Fatalf("command response retained write-only credential reference: %#v", redacted)
	}
	if redacted.Action != "update" || redacted.Host != "warehouse.internal" {
		t.Fatalf("command response lost non-secret metadata: %#v", redacted)
	}
}

func TestCreatorInvocationsRequireBrowserRequestIdentity(t *testing.T) {
	h := &BrowserHandler{BeginConnectionCommand: func(ctx context.Context, invocation CreatorCommandInvocation) (context.Context, error) {
		if invocation.Action != "create" || invocation.Project != "project:test" || invocation.IdempotencyKey != "ui:ui-command-1" {
			t.Fatalf("invocation = %#v", invocation)
		}
		return context.WithValue(ctx, creatorInvocationTestKey{}, true), nil
	}}
	request := httptest.NewRequest(http.MethodPost, "/connections/administration/configuration", nil)
	if _, err := h.beginConnectionInvocation(request, "create", "project:test", "connection:warehouse", 0); err == nil {
		t.Fatal("connection command accepted missing X-Request-ID")
	}
	request.Header.Set("X-Request-ID", "ui-command-1")
	request.Header.Set("X-LeapView-Operation-ID", "createTargetConnectionBinding")
	started, err := h.beginConnectionInvocation(request, "create", "project:test", "connection:warehouse", 0)
	if err != nil {
		t.Fatal(err)
	}
	if started == nil || started.Context() == request.Context() {
		t.Fatal("connection command did not establish generated invocation context")
	}
}

func TestPipelineInvocationUsesGeneratedUIIdempotency(t *testing.T) {
	h := &BrowserHandler{BeginPipelineCommand: func(ctx context.Context, invocation CreatorCommandInvocation) (context.Context, error) {
		if invocation.Action != "retry" || invocation.Project != "project:test" || invocation.IdempotencyKey != "ui:pipeline-command-1" {
			t.Fatalf("invocation = %#v", invocation)
		}
		return context.WithValue(ctx, creatorInvocationTestKey{}, true), nil
	}}
	request := httptest.NewRequest(http.MethodPost, "/pipelines/command", nil)
	request.Header.Set("X-Request-ID", "pipeline-command-1")
	started, err := h.beginPipelineInvocation(request, "retry", "project:test")
	if err != nil {
		t.Fatal(err)
	}
	if started == nil || started.Context() == request.Context() {
		t.Fatal("pipeline command did not establish generated invocation context")
	}
}

type creatorInvocationTestKey struct{}

func TestPipelineCommandFailsClosedWithoutResourceAuthorizer(t *testing.T) {
	called := false
	h := &BrowserHandler{
		PipelineRunCommand:    refreshgen.GenUIActionCreateRefreshRun(),
		PipelineCancelCommand: refreshgen.GenUIActionCancelRefreshRun(),
		CurrentUser: func(*http.Request) (Principal, bool) {
			return Principal{ID: "user:test"}, true
		},
		RunPipeline: func(context.Context, string, string, string) error {
			called = true
			return nil
		},
	}
	body := bytes.NewBufferString(`{"pipelineCommand":{"action":"run","assetId":"pipeline:sales","pipelineId":"pipeline:sales","runId":""}}`)
	request := httptest.NewRequest(http.MethodPost, "/pipelines/command", body)
	request.Header.Set("X-LeapView-Operation-ID", refreshgen.GenUIActionCreateRefreshRun().OperationID())
	recorder := httptest.NewRecorder()
	h.PipelineCommand(recorder, request)
	if called {
		t.Fatal("pipeline command ran without an in-handler resource authorizer")
	}
	if !strings.Contains(recorder.Body.String(), "Pipeline operation is unavailable") {
		t.Fatalf("fail-closed response = %q", recorder.Body.String())
	}
}

func TestPipelineCommandUsesCanonicalAssetIDForAuthorizationAndRefresh(t *testing.T) {
	var authorizedID, queuedID string
	h := &BrowserHandler{
		PipelineRunCommand: refreshgen.GenUIActionCreateRefreshRun(),
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) {
			return "project:test", nil
		},
		CurrentUser: func(*http.Request) (Principal, bool) {
			return Principal{ID: "user:test"}, true
		},
		AuthorizePipeline: func(_ *http.Request, pipelineID string, capability access.Capability) (bool, error) {
			if capability != access.CapabilityResourceUse {
				t.Fatalf("capability = %q, want RESOURCE_USE", capability)
			}
			authorizedID = pipelineID
			return true, nil
		},
		BeginPipelineCommand: func(ctx context.Context, _ CreatorCommandInvocation) (context.Context, error) {
			return ctx, nil
		},
		RunPipeline: func(_ context.Context, pipelineID, _ string, _ string) error {
			queuedID = pipelineID
			return errors.New("injected queue failure")
		},
	}
	body := bytes.NewBufferString(`{"pipelineCommand":{"action":"run","assetId":"pipeline:sales","pipelineId":"sales","runId":""}}`)
	request := httptest.NewRequest(http.MethodPost, "/pipelines/command", body)
	request.Header.Set("X-LeapView-Operation-ID", refreshgen.GenUIActionCreateRefreshRun().OperationID())
	request.Header.Set("X-Request-ID", "pipeline-canonical-1")
	recorder := httptest.NewRecorder()
	h.PipelineCommand(recorder, request)
	if authorizedID != "pipeline:sales" || queuedID != "pipeline:sales" {
		t.Fatalf("pipeline IDs: authorized=%q queued=%q, want canonical asset ID", authorizedID, queuedID)
	}
}

func TestPipelineCommandAndReplayAllowConfiguredDevelopmentBypass(t *testing.T) {
	authorizerCalled := false
	queued := false
	h := &BrowserHandler{
		PipelineRunCommand: refreshgen.GenUIActionCreateRefreshRun(),
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) {
			return "project:test", nil
		},
		CurrentUser: func(*http.Request) (Principal, bool) {
			return Principal{ID: "dev", DevBypass: true}, true
		},
		AuthorizePipeline: func(*http.Request, string, access.Capability) (bool, error) {
			authorizerCalled = true
			return false, nil
		},
		BeginPipelineCommand: func(ctx context.Context, _ CreatorCommandInvocation) (context.Context, error) {
			return ctx, nil
		},
		RunPipeline: func(context.Context, string, string, string) error {
			queued = true
			return errors.New("injected queue failure")
		},
	}
	body := `{"pipelineCommand":{"action":"run","assetId":"pipeline:sales","pipelineId":"pipeline:sales","runId":""}}`
	request := httptest.NewRequest(http.MethodPost, "/pipelines/command", strings.NewReader(body))
	request.Header.Set("X-LeapView-Operation-ID", refreshgen.GenUIActionCreateRefreshRun().OperationID())
	request.Header.Set("X-Request-ID", "pipeline-dev-bypass-1")
	recorder := httptest.NewRecorder()
	h.PipelineCommand(recorder, request)
	if !queued {
		t.Fatalf("development pipeline command was not queued; response=%s", recorder.Body.String())
	}
	if authorizerCalled {
		t.Fatal("development pipeline command unexpectedly consulted the serving-state resource authorizer")
	}

	replay := httptest.NewRequest(http.MethodPost, "/pipelines/command", strings.NewReader(body))
	if !h.AuthorizeCreatorMutationReplay(replay) {
		t.Fatal("development pipeline command replay was denied")
	}
	if authorizerCalled {
		t.Fatal("development replay unexpectedly consulted the serving-state resource authorizer")
	}
}

func TestPipelineAssetCommandSuccessPreservesDetailProjection(t *testing.T) {
	const projectID = "project:test"
	const assetID = "pipeline:sales"
	h := &BrowserHandler{
		Graph: browserGraphStub{graph: servingstate.AssetGraph{Assets: []servingstate.Asset{{
			ID: assetID, ProjectID: projectID, ServingStateID: "state:test", Type: "refresh_pipeline", Key: "sales", Title: "Sales refresh", PayloadJSON: `{}`,
		}}}},
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return projectID, nil },
		ProjectDefinitionReader: browserProjectDefinitionStub{definition: projectmanifest.Project{
			ID: projectID, RefreshPipelines: map[string]refreshschedule.Definition{assetID: {ID: assetID, Name: "Sales refresh"}},
		}},
		RefreshState: browserRefreshStateStub{state: refreshpresentation.AssetRefreshState{
			Latest: refreshpresentation.AssetRefreshRun{ID: "run:queued", Status: "queued"},
		}},
		CurrentUser: func(*http.Request) (Principal, bool) { return Principal{ID: "user:test", DevBypass: true}, true },
		AuthorizePipeline: func(_ *http.Request, pipelineID string, capability access.Capability) (bool, error) {
			return pipelineID == assetID && capability == access.CapabilityResourceUse, nil
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/pipelines/command?surface=asset&asset="+assetID+"&section=details", nil)
	recorder := httptest.NewRecorder()
	h.pipelineAssetCommandSuccess(recorder, request, signals.PipelineCommandSignal{Action: "run", AssetID: assetID, PipelineID: assetID}, "Pipeline command accepted.")
	body := recorder.Body.String()
	if !strings.Contains(body, `"assetId":"pipeline:sales"`) || !strings.Contains(body, `"refresh"`) {
		t.Fatalf("detail success patch = %s, want ResourceAssetPageSignal", body)
	}
	if strings.Contains(body, `"activeTab"`) || strings.Contains(body, `"metrics"`) {
		t.Fatalf("detail success patch replaced asset page with pipeline list: %s", body)
	}
	mismatchRequest := httptest.NewRequest(http.MethodPost, "/pipelines/command?surface=asset&asset=pipeline:other&section=details", nil)
	mismatchRecorder := httptest.NewRecorder()
	h.pipelineAssetCommandSuccess(mismatchRecorder, mismatchRequest, signals.PipelineCommandSignal{Action: "run", AssetID: assetID, PipelineID: assetID}, "Pipeline command accepted.")
	mismatchBody := mismatchRecorder.Body.String()
	if !strings.Contains(mismatchBody, "Pipeline command target is invalid") || strings.Contains(mismatchBody, `"page"`) {
		t.Fatalf("mismatched detail target response = %s, want fail-closed status only", mismatchBody)
	}
}

func TestConnectionAdministrationViewRedactsCredentialReferences(t *testing.T) {
	h := &BrowserHandler{
		TargetID:                 "instance:test",
		Environment:              "prod",
		ConnectionAdministration: redactionAdministration{},
		CurrentUser:              func(*http.Request) (Principal, bool) { return Principal{ID: "user:test"}, true },
	}
	assets := []projectview.DevelopAssetView{{
		ID: "connection:warehouse", Type: string(projectview.AssetTypeConnection), Key: "warehouse",
		Payload: map[string]any{"credentials_required": true},
	}}
	view, err := h.connectionAdministrationView(t.Context(), projectgraph.ResourceID("project:test"), assets, nil, httptest.NewRequest(http.MethodGet, "/connections", nil))
	if err != nil {
		t.Fatal(err)
	}
	binding := view.Bindings["connection:warehouse"]
	if binding.SecretPath != "" || binding.SecretKey != "" || binding.CredentialProjectID != "" || binding.CredentialEnvironment != "" {
		t.Fatalf("credential reference leaked into browser view: %#v", binding)
	}
}

type redactionAdministration struct{}

func (redactionAdministration) List(context.Context, string, connectionadmin.BindingScope, connectionadmin.TargetID) ([]connectionadmin.TargetBinding, error) {
	return []connectionadmin.TargetBinding{{
		ConnectionID: "connection:warehouse", ConnectorKind: "postgres", AuthenticationMode: connectionbinding.AuthenticationExternalBundle,
		CredentialReference: connectionadmin.CredentialReference{ProjectID: "project:secrets", Environment: "prod", SecretPath: "/connections/warehouse", SecretKey: "bundle"},
		Health:              connectionbinding.HealthHealthy, Enabled: true,
	}}, nil
}

func (redactionAdministration) Create(context.Context, string, connectionadmin.TargetBindingInput) (connectionadmin.TargetBinding, error) {
	return connectionadmin.TargetBinding{}, nil
}
func (redactionAdministration) PlanConfigurationChange(context.Context, string, connectionadmin.BindingKey, connectionadmin.TargetBindingConfiguration) (connectionadmin.BindingChangePlan, error) {
	return connectionadmin.BindingChangePlan{}, nil
}
func (redactionAdministration) UpdateConfiguration(context.Context, connectionadmin.UpdateConfigurationRequest) (connectionadmin.TargetBinding, error) {
	return connectionadmin.TargetBinding{}, nil
}
func (redactionAdministration) Test(context.Context, string, connectionadmin.BindingKey) (connectionadmin.BindingHealthStatus, error) {
	return connectionadmin.BindingHealthStatus{}, nil
}
func (redactionAdministration) RefreshNow(context.Context, string, connectionadmin.BindingKey) (connectionadmin.BindingHealthStatus, error) {
	return connectionadmin.BindingHealthStatus{}, nil
}
func (redactionAdministration) Enable(context.Context, string, connectionadmin.BindingKey) (connectionadmin.TargetBinding, error) {
	return connectionadmin.TargetBinding{}, nil
}
func (redactionAdministration) Disable(context.Context, string, connectionadmin.BindingKey) (connectionadmin.TargetBinding, error) {
	return connectionadmin.TargetBinding{}, nil
}
