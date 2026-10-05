package cli

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/platform/cliapi"
	"github.com/flidai/leapview/internal/project/developmentsession"
	"github.com/flidai/leapview/internal/project/devloop"
)

type sessionDevRemoteFactory struct {
	base           devRemoteFactory
	store          developmentsession.Store
	appURL         string
	openAppBrowser func(context.Context) error
}

func (factory *sessionDevRemoteFactory) Remote(ctx context.Context, credentials cliapi.Credentials, concurrency int) (devloop.Remote, error) {
	return factory.base.Remote(ctx, credentials, concurrency)
}

func (factory *sessionDevRemoteFactory) DevelopmentSession(context.Context, cliapi.Credentials) (*DevSessionBinding, error) {
	return &DevSessionBinding{Store: factory.store, Key: developmentsession.Key{OwnerID: "principal_ci", CheckoutID: "checkout_1", WorktreeID: "checkout_1", ProjectID: "project:leapview-showcase", TargetID: "lvinst_prod", Environment: "production"}, AppURL: factory.appURL, OpenAppBrowser: factory.openAppBrowser}, nil
}

func TestRunDevUsesDurableDevelopmentSessionWhenFactoryProvidesBinding(t *testing.T) {
	projectPath := filepath.Join("..", "..", "..", "examples", "dbt-warehouse-boundary", "leapview")
	store := developmentsession.NewMemoryStore()
	client := &devCommandClient{}
	factory := &sessionDevRemoteFactory{store: store}
	checkpoints := NewCandidateCheckpointStore(filepath.Join(t.TempDir(), "authoring.json"))
	if err := RunDev(t.Context(), client, checkpoints, factory, DevOptions{SourceRoot: projectPath, Credentials: cliapi.Credentials{Target: "prod"}, Once: true, NoBrowser: true, Format: "json"}, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	record, err := store.Resolve(context.Background(), developmentsession.Key{OwnerID: "principal_ci", CheckoutID: "checkout_1", WorktreeID: "checkout_1", ProjectID: "project:leapview-showcase", TargetID: "lvinst_prod", Environment: "production"})
	if err != nil {
		t.Fatal(err)
	}
	if record.LastValid.CandidateID != "cand_1" || record.LastValid.PreviewURL != "https://prod.example.com/candidates/cand_1" {
		t.Fatalf("durable dev session = %#v", record)
	}
}

func TestRunDevOpensStableSessionPreviewAndRetainsExactCandidateURL(t *testing.T) {
	projectPath := filepath.Join("..", "..", "..", "examples", "dbt-warehouse-boundary", "leapview")
	factory := &sessionDevRemoteFactory{store: developmentsession.NewMemoryStore()}
	checkpoints := NewCandidateCheckpointStore(filepath.Join(t.TempDir(), "authoring.json"))
	var opened []string
	var output strings.Builder
	if err := RunDev(t.Context(), &devCommandClient{}, checkpoints, factory, DevOptions{SourceRoot: projectPath, Credentials: cliapi.Credentials{Target: "prod"}, Once: true, Format: "text"}, func(url string) error {
		opened = append(opened, url)
		return nil
	}, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(opened) != 1 || !strings.Contains(opened[0], "/development-session/candidate/preview") || strings.Contains(opened[0], "cand_1") {
		t.Fatalf("opened URLs = %#v", opened)
	}
	if !strings.Contains(output.String(), "session-preview "+opened[0]) || !strings.Contains(output.String(), "candidate-preview https://prod.example.com/candidates/cand_1") {
		t.Fatalf("session/exact preview output = %q", output.String())
	}
}

func TestRunDevOpensActivatedLocalAppInsteadOfCandidatePreview(t *testing.T) {
	projectPath := filepath.Join("..", "..", "..", "examples", "dbt-warehouse-boundary", "leapview")
	appURL := "http://127.0.0.1:49916"
	openedApp := 0
	factory := &sessionDevRemoteFactory{store: developmentsession.NewMemoryStore(), appURL: appURL, openAppBrowser: func(context.Context) error {
		openedApp++
		return nil
	}}
	checkpoints := NewCandidateCheckpointStore(filepath.Join(t.TempDir(), "authoring.json"))
	var output strings.Builder
	var previewOpened bool
	if err := RunDev(t.Context(), &devCommandClient{}, checkpoints, factory, DevOptions{SourceRoot: projectPath, Credentials: cliapi.Credentials{Target: "prod"}, Once: true, Format: "text"}, func(string) error {
		previewOpened = true
		return nil
	}, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if openedApp != 1 || previewOpened || !strings.Contains(output.String(), "app "+appURL) {
		t.Fatalf("local app handoff calls=%d previewOpened=%t output=%q", openedApp, previewOpened, output.String())
	}
}
