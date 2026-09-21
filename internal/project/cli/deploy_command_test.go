package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	"github.com/flidai/leapview/internal/platform/cliapi"
)

func TestDeployCommandRejectsAmbiguousHeadlessSelectionAsStructuredError(t *testing.T) {
	for _, args := range [][]string{
		{"--new", "--resume", "--format", "json"},
		{"--operation", "release-42", "--format", "json"},
	} {
		command := DeployCommand(context.Background(), deployClient{}, &deployOperations{})
		var output bytes.Buffer
		command.SetOut(&output)
		command.SetErr(io.Discard)
		command.SetArgs(args)
		if err := command.Execute(); err == nil {
			t.Fatalf("Execute(%v) succeeded", args)
		}
		var selection DeploymentSelectionError
		jsonOutput := output.Bytes()
		if start := bytes.IndexByte(jsonOutput, '{'); start >= 0 {
			jsonOutput = jsonOutput[start:]
		}
		decoder := json.NewDecoder(bytes.NewReader(jsonOutput))
		if err := decoder.Decode(&selection); err != nil {
			t.Fatalf("selection output for %v = %q: %v", args, output.String(), err)
		}
		if selection.SchemaVersion != 1 || selection.Code == "" || selection.Detail == "" {
			t.Fatalf("selection error for %v = %#v", args, selection)
		}
	}
}

type deployClient struct{}

func (deployClient) Resolve(_ context.Context, credentials cliapi.Credentials) (cliapi.Credentials, error) {
	return credentials, nil
}
func (deployClient) Environment(_ context.Context, _ cliapi.Credentials, asserted string) (string, error) {
	return asserted, nil
}
func (deployClient) Transport(context.Context, cliapi.Credentials) (apigenclient.Transport, error) {
	return nil, nil
}

type deployOperations struct {
	options DeployOptions
}

func (operations *deployOperations) Deploy(_ context.Context, options DeployOptions, _ io.Writer) error {
	operations.options = options
	return nil
}

func TestDeployCommandLeavesManagedPinsToTargetCandidatePreparation(t *testing.T) {
	operations := &deployOperations{}
	command := DeployCommand(context.Background(), deployClient{}, operations)
	command.SetArgs([]string{
		"--target", "https://example.test", "--token", "secret",
		"--environment", "prod",
	})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if operations.options.Credentials.Target != "https://example.test" ||
		operations.options.Credentials.Token != "secret" ||
		operations.options.Environment != "prod" {
		t.Fatalf("options = %#v", operations.options)
	}
	if command.Flags().Lookup("revision") != nil {
		t.Fatal("deploy command still exposes client-owned managed revision pins")
	}
	if command.Flags().Lookup("auto-approve") != nil {
		t.Fatal("deploy command still exposes client-side approval bypass")
	}
}
