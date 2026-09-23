package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/app/cli/projectinit"
	manageddatacli "github.com/flidai/leapview/internal/manageddata/cli"
	"github.com/flidai/leapview/internal/manageddata/localplan"
	"github.com/flidai/leapview/internal/platform/cliapi"
	"github.com/flidai/leapview/internal/project/developmentinput"
)

func TestStageDeclaredDevelopmentInputsPlansBeforeSyncing(t *testing.T) {
	root, err := projectinit.Initialize(t.TempDir() + "/analytics")
	if err != nil {
		t.Fatal(err)
	}
	planner := localplan.NewService(loadManagedDataPlanCatalog)
	var requests []manageddatacli.SyncRequest
	var output bytes.Buffer
	dependencies := developmentInputStagingDependencies{
		list:    developmentinput.Names,
		resolve: resolveDevelopmentInput,
		plan:    planner.Plan,
		sync: func(_ context.Context, request manageddatacli.SyncRequest) error {
			requests = append(requests, request)
			return nil
		},
		httpClient: http.DefaultClient,
	}
	target := localDevelopmentInputTarget{
		ProjectRoot: root,
		ProjectID:   "lvproject_local",
		Environment: "dev",
		Origin:      "http://127.0.0.1:7090",
		Output:      &output,
	}
	credentials := cliapi.Credentials{
		Target:          target.Origin,
		CanonicalOrigin: target.Origin,
		Token:           "private-test-token",
		ProjectID:       target.ProjectID,
	}

	if err := stageDeclaredDevelopmentInputsWithDependencies(context.Background(), credentials, target, dependencies); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 {
		t.Fatalf("sync requests = %d, want 1", len(requests))
	}
	request := requests[0]
	if request.ProjectID != target.ProjectID || request.Target != target.Origin || request.Connection != "sample" || request.Token != credentials.Token {
		t.Fatalf("sync request = %#v", request)
	}
	if request.Plan.Manifest.RevisionID() == "" {
		t.Fatalf("sync plan = %#v", request.Plan)
	}
	if !strings.Contains(output.String(), "staging declared development input sample") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestStageDeclaredDevelopmentInputsSkipsProjectsWithoutManifest(t *testing.T) {
	called := false
	dependencies := developmentInputStagingDependencies{
		list: developmentinput.Names,
		resolve: func(string, string) (manageddatacli.DevelopmentInput, error) {
			called = true
			return manageddatacli.DevelopmentInput{}, nil
		},
		plan: func(context.Context, localplan.Request) (localplan.Result, error) {
			called = true
			return localplan.Result{}, nil
		},
		sync: func(context.Context, manageddatacli.SyncRequest) error {
			called = true
			return nil
		},
		httpClient: http.DefaultClient,
	}
	target := localDevelopmentInputTarget{
		ProjectRoot: t.TempDir(), ProjectID: "lvproject_local", Environment: "dev",
		Origin: "http://127.0.0.1:7090", Output: io.Discard,
	}
	credentials := cliapi.Credentials{Target: target.Origin, CanonicalOrigin: target.Origin, Token: "private-test-token", ProjectID: target.ProjectID}
	if err := stageDeclaredDevelopmentInputsWithDependencies(context.Background(), credentials, target, dependencies); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("project without a development-input manifest attempted staging")
	}
}
