package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	"github.com/flidai/leapview/internal/platform/cliapi"
	projectcli "github.com/flidai/leapview/internal/project/cli"
	"github.com/flidai/leapview/internal/project/devloop"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func TestLocalhostAndProtectedTargetsUseTheSamePublicAuthoringCommands(t *testing.T) {
	root := NewCommand(t.Context())
	for _, name := range []string{"login", "dev", "publish", "deploy"} {
		command, _, err := root.Find([]string{name})
		if err != nil || command == root || command.Name() != name {
			t.Fatalf("public authoring command %q is unavailable: command=%v err=%v", name, command, err)
		}
	}
	for _, retired := range []string{"preview"} {
		if command, _, err := root.Find([]string{retired}); err == nil && command != root {
			t.Fatalf("root exposes alternate authoring command %q", retired)
		}
	}

	projectPath := filepath.Join("..", "..", "..", "dashboards", "leapview.yaml")
	journeys := []authoringJourney{
		{
			name: "localhost evaluation", target: "http://localhost:8080",
			targetID: "lvinst_local", environment: "evaluation",
		},
		{
			name: "protected enterprise", target: "https://dash.example.com",
			targetID: "lvinst_prod", environment: "production",
		},
	}
	var commandShapes [][]string
	for _, journey := range journeys {
		t.Run(journey.name, func(t *testing.T) {
			client := authoringJourneyClient{journey: journey}
			remote := &authoringJourneyRemoteFactory{journey: journey}
			store := projectcli.NewCandidateCheckpointStore(
				filepath.Join(t.TempDir(), "authoring.json"),
			)
			var opened []string
			dev := projectcli.DevCommand(
				t.Context(),
				client,
				store,
				remote,
				func(uri string) error {
					opened = append(opened, uri)
					return nil
				},
			)
			publishOperation := &authoringJourneyPublish{}
			publish := projectcli.PublishCommand(
				t.Context(),
				client,
				store,
				publishOperation,
			)
			commandShapes = append(commandShapes, []string{
				dev.Name(), publish.Name(),
				strings.Join(flagNames(dev), ","),
				strings.Join(flagNames(publish), ","),
			})

			var output strings.Builder
			dev.SetOut(&output)
			dev.SetErr(&output)
			dev.SetArgs([]string{
				"--once",
				"--project", projectPath,
				"--target", journey.target,
			})
			if err := dev.Execute(); err != nil {
				t.Fatal(err)
			}
			if len(opened) != 1 ||
				opened[0] != journey.target+"/candidates/cand_1" {
				t.Fatalf("opened previews = %#v", opened)
			}

			publish.SetOut(&output)
			publish.SetErr(&output)
			publish.SetArgs([]string{"cand_1"})
			if err := publish.Execute(); err != nil {
				t.Fatal(err)
			}
			checkpoint := publishOperation.options.Checkpoint
			if checkpoint.TargetOrigin != journey.target ||
				checkpoint.TargetID != journey.targetID ||
				checkpoint.Environment != journey.environment ||
				checkpoint.CandidateID != "cand_1" {
				t.Fatalf("published checkpoint = %#v", checkpoint)
			}
		})
	}
	if len(commandShapes) != 2 || !slices.Equal(commandShapes[0], commandShapes[1]) {
		t.Fatalf("target-specific command surfaces = %#v", commandShapes)
	}
}

type authoringJourney struct {
	name        string
	target      string
	targetID    string
	environment string
}

type authoringJourneyClient struct {
	journey authoringJourney
}

func (client authoringJourneyClient) Resolve(
	_ context.Context,
	credentials cliapi.Credentials,
) (cliapi.Credentials, error) {
	if credentials.Target != client.journey.target {
		return cliapi.Credentials{}, fmt.Errorf("unexpected target %q", credentials.Target)
	}
	return cliapi.Credentials{
		Target: client.journey.target,
		Token:  "ephemeral-authoring-token",
	}, nil
}

func (authoringJourneyClient) Environment(
	context.Context,
	cliapi.Credentials,
	string,
) (string, error) {
	return "", nil
}

func (authoringJourneyClient) Transport(
	context.Context,
	cliapi.Credentials,
) (apigenclient.Transport, error) {
	return nil, nil
}

type authoringJourneyRemoteFactory struct {
	journey authoringJourney
}

func (factory *authoringJourneyRemoteFactory) Remote(
	_ context.Context,
	credentials cliapi.Credentials,
	_ int,
) (devloop.Remote, error) {
	if credentials.Target != factory.journey.target {
		return nil, fmt.Errorf("remote target = %q", credentials.Target)
	}
	return authoringJourneyRemote{journey: factory.journey}, nil
}

type authoringJourneyRemote struct {
	journey authoringJourney
}

func (remote authoringJourneyRemote) Synchronize(
	_ context.Context,
	request devloop.SyncRequest,
) (devloop.Candidate, error) {
	return devloop.Candidate{
		ID:               "cand_1",
		ProjectID:        request.Snapshot.ProjectID,
		OwnerID:          "principal_author",
		ArtifactDigest:   request.Snapshot.Digest,
		PreviewURL:       remote.journey.target + "/candidates/cand_1",
		TargetID:         remote.journey.targetID,
		Environment:      remote.journey.environment,
		ProvenanceDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Revision:         1,
	}, nil
}

type authoringJourneyPublish struct {
	options projectcli.PublishOptions
}

func (operation *authoringJourneyPublish) Publish(
	_ context.Context,
	options projectcli.PublishOptions,
	out io.Writer,
) error {
	operation.options = options
	_, err := fmt.Fprintln(out, "publication requested through target policy")
	return err
}

func flagNames(command *cobra.Command) []string {
	names := []string{}
	command.Flags().VisitAll(func(flag *pflag.Flag) {
		names = append(names, flag.Name)
	})
	return names
}
