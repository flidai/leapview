package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/connectionadmin"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	projectview "github.com/flidai/leapview/internal/project"
	projectui "github.com/flidai/leapview/internal/project/ui"
	"github.com/flidai/leapview/internal/project/ui/signals"
)

func credentialBrowserRequest(t *testing.T, method string, command signals.ConnectionCredentialCommandSignal) *http.Request {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{"connectionAdmin": signals.ConnectionAdministrationSignal{Credentials: &signals.ConnectionCredentialSignal{Command: command}}})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, "/connections/administration/credentials", strings.NewReader(string(encoded)))
	if method == http.MethodGet {
		request.URL.RawQuery = url.Values{"datastar": {string(encoded)}}.Encode()
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-LeapView-Operation-ID", "saveCredentialDraft")
	return request.WithContext(context.WithValue(request.Context(), browserReadContextKey{}, &browserRequestReads{
		assetsLoaded: true, projectID: "project_server", assets: []projectview.DevelopAssetView{{ID: "warehouse", Type: "connection", Key: "warehouse"}},
	}))
}

type credentialFlushFailureWriter struct {
	*httptest.ResponseRecorder
	failure             error
	cacheControlAtFlush string
}

func (w *credentialFlushFailureWriter) FlushError() error {
	w.cacheControlAtFlush = w.Header().Get("Cache-Control")
	return w.failure
}

func TestCredentialNoStoreWriterPreservesFlushFailure(t *testing.T) {
	failure := errors.New("stream disconnected")
	response := &credentialFlushFailureWriter{ResponseRecorder: httptest.NewRecorder(), failure: failure}
	writer := credentialNoStoreWriter{ResponseWriter: response}
	// Both direct and ResponseController dispatch must set the header before
	// flushing and retain the transport error for the SSE caller.
	for _, flush := range []func() error{writer.FlushError, http.NewResponseController(writer).Flush} {
		response.Header().Set("Cache-Control", "no-cache")
		if err := flush(); !errors.Is(err, failure) {
			t.Fatalf("flush error = %v, want transport failure", err)
		}
		if response.cacheControlAtFlush != "no-store" {
			t.Fatalf("cache policy at flush = %q", response.cacheControlAtFlush)
		}
	}
}

func TestConnectionCredentialTransportBindsScopeAndRedactsEveryResponse(t *testing.T) {
	for _, failed := range []bool{false, true} {
		h := &BrowserHandler{
			CurrentUser:              func(*http.Request) (Principal, bool) { return Principal{ID: "actor_server"}, true },
			ConnectionCommands:       projectui.ConnectionCommandBindings{Credentials: credentialmodule.CredentialBrowserBindings()},
			ConnectionAdministration: credentialRevisionAdministration{},
		}
		called := false
		h.ConnectionCredentials = func(_ context.Context, actor, project, connection string, command signals.ConnectionCredentialCommandSignal) (signals.ConnectionCredentialSignal, error) {
			called = true
			if actor != "actor_server" || project != "project_server" || connection != "warehouse" || command.LogicalConnection != "warehouse" || command.Password != "private-password" {
				t.Fatal("credential command did not use exact server scope")
			}
			if failed {
				return signals.ConnectionCredentialSignal{}, errors.New("private-password provider detail")
			}
			return signals.ConnectionCredentialSignal{Command: command}, nil
		}
		request := credentialBrowserRequest(t, http.MethodPost, signals.ConnectionCredentialCommandSignal{Action: "save", AssetID: "warehouse", LogicalConnection: "foreign", Username: "private-user", Password: "private-password"})
		response := httptest.NewRecorder()
		h.ConnectionCredentialMutation(response, request)
		if !called || response.Header().Get("Cache-Control") != "no-store" || strings.Contains(response.Body.String(), "private-") {
			t.Fatalf("credential transport leaked secrets or failed dispatch: %s", response.Body.String())
		}
		if !failed && !strings.Contains(response.Body.String(), `"bindingRevision":42`) {
			t.Fatal("saved draft did not refresh the binding revision after an earlier activation")
		}
	}
}

type credentialRevisionAdministration struct{ redactionAdministration }

func (credentialRevisionAdministration) List(context.Context, string, connectionadmin.BindingScope, connectionadmin.TargetID) ([]connectionadmin.TargetBinding, error) {
	return []connectionadmin.TargetBinding{{ConnectionID: "warehouse", Revision: 42}}, nil
}

func TestConnectionCredentialTransportRejectsInvalidClaimsTargetsAndSecretQueries(t *testing.T) {
	for _, scenario := range []string{"claim", "foreign", "secret-query", "query-mutation"} {
		h := &BrowserHandler{
			CurrentUser:        func(*http.Request) (Principal, bool) { return Principal{ID: "actor_server"}, true },
			ConnectionCommands: projectui.ConnectionCommandBindings{Credentials: credentialmodule.CredentialBrowserBindings()},
			ConnectionCredentials: func(context.Context, string, string, string, signals.ConnectionCredentialCommandSignal) (signals.ConnectionCredentialSignal, error) {
				t.Fatal("invalid credential request reached service")
				return signals.ConnectionCredentialSignal{}, nil
			},
		}
		command := signals.ConnectionCredentialCommandSignal{Action: "save", AssetID: "warehouse", Password: "private-password"}
		method := http.MethodPost
		if scenario == "foreign" {
			command.AssetID = "foreign"
		}
		if scenario == "secret-query" {
			method, command.Action = http.MethodGet, "list"
		}
		if scenario == "query-mutation" {
			method, command.Password = http.MethodGet, ""
		}
		request := credentialBrowserRequest(t, method, command)
		if scenario == "claim" {
			request.Header.Set("X-LeapView-Operation-ID", "abortCredentialActivation")
		}
		response := httptest.NewRecorder()
		if method == http.MethodGet {
			h.ConnectionCredentialQuery(response, request)
		} else {
			h.ConnectionCredentialMutation(response, request)
		}
		if strings.Contains(response.Body.String(), "private-password") || !strings.Contains(response.Body.String(), "error") {
			t.Fatalf("%s did not fail closed with redaction: %s", scenario, response.Body.String())
		}
	}
}
