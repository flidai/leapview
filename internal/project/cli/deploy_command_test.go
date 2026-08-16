package cli

import (
	"context"
	"io"
	"testing"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	"github.com/flidai/leapview/internal/platform/cliapi"
)

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
