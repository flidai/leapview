package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/platform/cliapi"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type fakeActiveProjectGraphLoader struct {
	credentials cliapi.Credentials
	graph       projectgraph.ProjectGraph
}

func (loader *fakeActiveProjectGraphLoader) LoadActiveProjectGraph(
	_ context.Context,
	credentials cliapi.Credentials,
) (projectgraph.ProjectGraph, error) {
	loader.credentials = credentials
	return loader.graph, nil
}

func TestValidateCommandOwnsProjectArgumentRules(t *testing.T) {
	command := ValidateCommand(context.Background())
	command.SetArgs([]string{"project.yaml", "--project", "other.yaml"})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "choose either --project or positional project") {
		t.Fatalf("error = %v", err)
	}
}

func TestSchemaCommandRejectsUnknownFormats(t *testing.T) {
	command := SchemaCommand()
	command.SetArgs([]string{"export", "--format", "yaml"})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), `unsupported schema format "yaml"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestActiveProjectGraphUsesCompositionSuppliedLoader(t *testing.T) {
	want, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{
		ID: "project:sales", Kind: projectgraph.KindProject, Name: "sales",
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	loader := &fakeActiveProjectGraphLoader{graph: want}
	options := &options{
		remote: cliapi.RemoteOptions{Target: "https://example.test", Token: "secret"},
	}

	got, err := fetchActiveProjectGraph(context.Background(), loader, options)
	if err != nil {
		t.Fatalf("fetch active graph: %v", err)
	}
	if got.ProjectID() != want.ProjectID() {
		t.Fatalf("graph = %#v", got)
	}
	if loader.credentials.Target != "https://example.test" || loader.credentials.Token != "secret" {
		t.Fatalf("loader request = %#v", loader)
	}
}
