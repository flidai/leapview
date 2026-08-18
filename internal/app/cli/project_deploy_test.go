package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	"github.com/flidai/leapview/internal/platform/cliapi"
	projectcli "github.com/flidai/leapview/internal/project/cli"
	"github.com/stretchr/testify/require"
)

func TestDeployComposesCanonicalPlanBuildAndPublication(t *testing.T) {
	client := &deployLifecycleClient{environment: "prod"}
	var sequence []string
	planner := &deployPlanRecorder{result: projectcli.DeliveryPlanResult{
		PlanID: "plan-1", ProjectID: "project-1", TargetID: "target-1", Environment: "prod",
		SourceDigest: "sha256:" + strings.Repeat("a", 64), PlanDigest: "sha256:" + strings.Repeat("b", 64),
		ExecutionDigest: "sha256:" + strings.Repeat("c", 64), EvidenceDigest: "sha256:" + strings.Repeat("d", 64),
	}, sequence: &sequence}
	builder := &deployBuildRecorder{result: projectcli.DeliveryBuildResult{BuildID: "build-1", PlanID: "plan-1", CandidateID: "candidate-1", Status: "sealed"}, sequence: &sequence}
	publisher := &deployPublishRecorder{sequence: &sequence}
	operations := projectDeployOperations{client: client, planner: planner, builder: builder, publisher: publisher}
	credentials := cliapi.Credentials{Target: "https://example.test", Token: "secret"}

	err := operations.Deploy(context.Background(), projectcli.DeployOptions{
		ProjectPath: "dashboards/leapview.yaml", Credentials: credentials, Environment: "prod",
	}, &bytes.Buffer{})
	require.NoError(t, err)
	require.Equal(t, []string{"plan", "build", "publish"}, sequence)
	require.Equal(t, "prod", client.assertedEnvironment)
	require.Equal(t, projectDeploymentCandidateKey, planner.options.CandidateKey)
	require.Equal(t, "plan-1", builder.options.PlanID)
	require.Equal(t, "candidate-1", publisher.options.CandidateID)
	require.Equal(t, credentials, publisher.options.Credentials)
}

func TestDeployDoesNotBuildOrPublishWhenPlanFails(t *testing.T) {
	planErr := errors.New("plan failed")
	planner := &deployPlanRecorder{err: planErr}
	builder := &deployBuildRecorder{}
	publisher := &deployPublishRecorder{}
	operations := projectDeployOperations{client: &deployLifecycleClient{environment: "prod"}, planner: planner, builder: builder, publisher: publisher}

	err := operations.Deploy(context.Background(), projectcli.DeployOptions{
		ProjectPath: "dashboards/leapview.yaml", Credentials: cliapi.Credentials{Target: "https://example.test"},
	}, &bytes.Buffer{})
	require.ErrorIs(t, err, planErr)
	require.Empty(t, builder.order)
	require.Empty(t, publisher.order)
}

func TestDeployAdapterDoesNotCallLegacyLifecycle(t *testing.T) {
	source, err := os.ReadFile("deploy.go")
	require.NoError(t, err)
	body := string(source)
	for _, forbidden := range []string{"projectcli.RunDev", "projectcli.RunPublish", "lifecycle.Synchronize", "lifecycle.Publish", "createRelease(", "createDeployment("} {
		require.NotContains(t, body, forbidden)
	}
}

type deployLifecycleClient struct {
	environment         string
	assertedEnvironment string
}

func (client *deployLifecycleClient) Resolve(_ context.Context, credentials cliapi.Credentials) (cliapi.Credentials, error) {
	return credentials, nil
}

func (client *deployLifecycleClient) Environment(_ context.Context, _ cliapi.Credentials, asserted string) (string, error) {
	client.assertedEnvironment = asserted
	return client.environment, nil
}

func (*deployLifecycleClient) Transport(context.Context, cliapi.Credentials) (apigenclient.Transport, error) {
	return nil, nil
}

type deployPlanRecorder struct {
	order    []string
	sequence *[]string
	options  projectcli.DeliveryPlanOptions
	result   projectcli.DeliveryPlanResult
	err      error
}

func (recorder *deployPlanRecorder) Create(_ context.Context, options projectcli.DeliveryPlanOptions) (projectcli.DeliveryPlanResult, error) {
	recorder.order = append(recorder.order, "plan")
	if recorder.sequence != nil {
		*recorder.sequence = append(*recorder.sequence, "plan")
	}
	recorder.options = options
	return recorder.result, recorder.err
}

type deployBuildRecorder struct {
	order    []string
	sequence *[]string
	options  projectcli.DeliveryBuildOptions
	result   projectcli.DeliveryBuildResult
	err      error
}

func (recorder *deployBuildRecorder) Build(_ context.Context, options projectcli.DeliveryBuildOptions) (projectcli.DeliveryBuildResult, error) {
	recorder.order = append(recorder.order, "build")
	if recorder.sequence != nil {
		*recorder.sequence = append(*recorder.sequence, "build")
	}
	recorder.options = options
	return recorder.result, recorder.err
}

type deployPublishRecorder struct {
	order    []string
	sequence *[]string
	options  projectcli.PublishOptions
	err      error
}

func (recorder *deployPublishRecorder) Publish(_ context.Context, options projectcli.PublishOptions, _ io.Writer) error {
	recorder.order = append(recorder.order, "publish")
	if recorder.sequence != nil {
		*recorder.sequence = append(*recorder.sequence, "publish")
	}
	recorder.options = options
	return recorder.err
}
