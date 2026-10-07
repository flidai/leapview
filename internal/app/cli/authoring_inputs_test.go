package cli

import (
	"bytes"
	"context"
	"errors"
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
	grantsVerified := false
	dependencies := developmentInputStagingDependencies{
		list:    developmentinput.Names,
		resolve: resolveDevelopmentInput,
		plan:    planner.Plan,
		grants: func(_ context.Context, inputs []plannedDevelopmentInput) error {
			if len(inputs) != 1 || inputs[0].plan.Connection == "" || inputs[0].plan.Manifest.RevisionID() == "" {
				t.Fatal("grant creation ran before complete input validation")
			}
			grantsVerified = true
			return nil
		},
		sync: func(_ context.Context, request manageddatacli.SyncRequest) error {
			if !grantsVerified {
				t.Fatal("upload preceded verified durable grant")
			}
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

	inputDigest, err := stageDeclaredDevelopmentInputsWithDependencies(context.Background(), credentials, target, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if inputDigest == "" {
		t.Fatal("staged input has no revision identity")
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
	requests = nil
	dependencies.grants = func(context.Context, []plannedDevelopmentInput) error {
		return errors.New("policy verification failed")
	}
	if _, err := stageDeclaredDevelopmentInputsWithDependencies(t.Context(), credentials, target, dependencies); err == nil || len(requests) != 0 {
		t.Fatalf("grant failure did not stop upload: err=%v uploads=%d", err, len(requests))
	}
	dependencies.plan = func(context.Context, localplan.Request) (localplan.Result, error) {
		return localplan.Result{}, errors.New("invalid fixture")
	}
	dependencies.grants = func(context.Context, []plannedDevelopmentInput) error {
		t.Fatal("invalid fixture mutated grants")
		return nil
	}
	if _, err := stageDeclaredDevelopmentInputsWithDependencies(t.Context(), credentials, target, dependencies); err == nil {
		t.Fatal("invalid fixture accepted")
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
	inputDigest, err := stageDeclaredDevelopmentInputsWithDependencies(context.Background(), credentials, target, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if inputDigest != "" {
		t.Fatalf("empty manifest produced input revision %q", inputDigest)
	}
	if called {
		t.Fatal("project without a development-input manifest attempted staging")
	}
}
