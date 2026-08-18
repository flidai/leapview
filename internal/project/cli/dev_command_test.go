package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	"github.com/flidai/leapview/internal/platform/cliapi"
	"github.com/flidai/leapview/internal/project/devloop"
	"github.com/stretchr/testify/require"
)

type devCommandClient struct {
	resolved cliapi.Credentials
}

func (client *devCommandClient) Resolve(
	_ context.Context,
	credentials cliapi.Credentials,
) (cliapi.Credentials, error) {
	if credentials.Target != "prod" {
		return cliapi.Credentials{}, io.ErrUnexpectedEOF
	}
	client.resolved = cliapi.Credentials{
		Target:          "http://localhost:8080",
		Token:           "ephemeral",
		CanonicalOrigin: "https://prod.example.com",
	}
	return client.resolved, nil
}

func (*devCommandClient) Environment(
	context.Context,
	cliapi.Credentials,
	string,
) (string, error) {
	return "", nil
}

func (*devCommandClient) Transport(
	context.Context,
	cliapi.Credentials,
) (apigenclient.Transport, error) {
	return nil, nil
}

type devRemoteFactory struct {
	credentials cliapi.Credentials
	concurrency int
}

func (factory *devRemoteFactory) Remote(
	_ context.Context,
	credentials cliapi.Credentials,
	concurrency int,
) (devloop.Remote, error) {
	factory.credentials = credentials
	factory.concurrency = concurrency
	return devCommandRemote{}, nil
}

type devCommandRemote struct{}

func (devCommandRemote) Synchronize(
	_ context.Context,
	request devloop.SyncRequest,
) (devloop.Candidate, error) {
	return devloop.Candidate{
		ID:               "cand_1",
		ProjectID:        request.Snapshot.ProjectID,
		OwnerID:          "principal_ci",
		ArtifactDigest:   request.Snapshot.Digest,
		PreviewURL:       "https://prod.example.com/candidates/cand_1",
		TargetID:         "lvinst_prod",
		Environment:      "production",
		ProvenanceDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Revision:         7,
	}, nil
}

func TestDevCommandOwnsOneAuthenticatedRemoteWorkflow(t *testing.T) {
	projectPath := filepath.Join("..", "..", "..", "dashboards", "leapview.yaml")
	checkpoints := NewCandidateCheckpointStore(
		filepath.Join(t.TempDir(), "authoring.json"),
	)
	client := &devCommandClient{}
	remotes := &devRemoteFactory{}
	var opened []string
	command := DevCommand(
		t.Context(),
		client,
		checkpoints,
		remotes,
		func(uri string) error {
			opened = append(opened, uri)
			return nil
		},
	)
	var output strings.Builder
	command.SetOut(&output)
	command.SetErr(io.Discard)
	command.SetArgs([]string{
		"--once",
		"--project", projectPath,
		"--target", "prod",
		"--upload-concurrency", "3",
		"--candidate-key", "github:pull/42",
		"--source-revision", "commit-a",
	})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if remotes.credentials != client.resolved || remotes.concurrency != 3 {
		t.Fatalf(
			"remote credentials=%+v concurrency=%d",
			remotes.credentials,
			remotes.concurrency,
		)
	}
	checkpoint, err := checkpoints.LoadCandidate(
		projectPath,
		client.resolved.Target,
		"github:pull/42",
	)
	require.NoError(t, err)
	if checkpoint.TargetID != "lvinst_prod" ||
		checkpoint.CandidateID != "cand_1" ||
		checkpoint.CandidateKey != "github:pull/42" ||
		checkpoint.CandidateRevision != 7 {
		t.Fatalf("checkpoint = %+v", checkpoint)
	}
	if !strings.Contains(output.String(), "synchronized sha256:") ||
		!strings.Contains(
			output.String(),
			"provenance sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		) ||
		!strings.Contains(
			output.String(),
			"candidate cand_1 revision 7 target lvinst_prod environment production principal principal_ci",
		) ||
		!strings.Contains(
			output.String(),
			"preview https://prod.example.com/candidates/cand_1",
		) {
		t.Fatalf("output = %q", output.String())
	}
	if len(opened) != 1 ||
		opened[0] != "https://prod.example.com/candidates/cand_1" {
		t.Fatalf("opened preview URLs = %#v", opened)
	}
	for _, flag := range []string{
		"project",
		"target",
		"token",
		"upload-concurrency",
		"once",
		"no-browser",
	} {
		if command.Flags().Lookup(flag) == nil {
			t.Errorf("dev command is missing --%s", flag)
		}
	}
	for _, forbidden := range []string{
		"local-server",
		"production",
		"workspace",
	} {
		if command.Flags().Lookup(forbidden) != nil {
			t.Errorf("dev command exposes alternate workflow --%s", forbidden)
		}
	}
}

func TestDevCommandCanRemainHeadlessAndTreatsBrowserFailureAsRecoverable(t *testing.T) {
	projectPath := filepath.Join("..", "..", "..", "dashboards", "leapview.yaml")
	tests := []struct {
		name        string
		args        []string
		open        func(string) error
		wantCalls   int
		wantWarning string
	}{
		{
			name: "explicit headless",
			args: []string{"--no-browser"},
			open: func(string) error {
				t.Fatal("headless dev opened a browser")
				return nil
			},
		},
		{
			name: "unavailable system browser",
			open: func(string) error {
				return errors.New("no desktop session")
			},
			wantCalls:   1,
			wantWarning: "could not open preview in the system browser",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			checkpoints := NewCandidateCheckpointStore(
				filepath.Join(t.TempDir(), "authoring.json"),
			)
			calls := 0
			command := DevCommand(
				t.Context(),
				&devCommandClient{},
				checkpoints,
				&devRemoteFactory{},
				func(uri string) error {
					calls++
					return test.open(uri)
				},
			)
			var output, errOutput strings.Builder
			command.SetOut(&output)
			command.SetErr(&errOutput)
			command.SetArgs(append([]string{
				"--once",
				"--project", projectPath,
				"--target", "prod",
			}, test.args...))
			if err := command.Execute(); err != nil {
				t.Fatal(err)
			}
			if calls != test.wantCalls {
				t.Fatalf("browser calls = %d, want %d", calls, test.wantCalls)
			}
			if !strings.Contains(output.String(), "preview https://prod.example.com/candidates/cand_1") {
				t.Fatalf("output = %q", output.String())
			}
			if !strings.Contains(errOutput.String(), test.wantWarning) {
				t.Fatalf("stderr = %q, want %q", errOutput.String(), test.wantWarning)
			}
		})
	}
}

func TestDevCommandEmitsVersionedJSONResult(t *testing.T) {
	projectPath := filepath.Join("..", "..", "..", "dashboards", "leapview.yaml")
	command := DevCommand(
		t.Context(),
		&devCommandClient{},
		NewCandidateCheckpointStore(filepath.Join(t.TempDir(), "authoring.json")),
		&devRemoteFactory{},
		nil,
	)
	var output strings.Builder
	command.SetOut(&output)
	command.SetErr(io.Discard)
	command.SetArgs([]string{
		"--once", "--no-browser", "--format", "json",
		"--project", projectPath, "--target", "prod",
	})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var result DevResult
	if err := json.Unmarshal([]byte(output.String()), &result); err != nil {
		t.Fatal(err)
	}
	if result.SchemaVersion != 1 || result.CandidateID != "cand_1" ||
		result.Revision != 7 || result.PrincipalID != "principal_ci" ||
		result.PreviewURL != "https://prod.example.com/candidates/cand_1" {
		t.Fatalf("result = %#v", result)
	}
}

func TestCandidatePreviewURLMustBeCanonicalAndTokenFree(t *testing.T) {
	for _, test := range []struct {
		name      string
		target    string
		candidate string
		preview   string
		wantError bool
	}{
		{
			name:   "enterprise",
			target: "https://dash.example.com", candidate: "cand_1",
			preview: "https://dash.example.com/candidates/cand_1",
		},
		{
			name:   "loopback",
			target: "http://localhost:8080/", candidate: "cand_1",
			preview: "http://localhost:8080/candidates/cand_1",
		},
		{
			name:   "foreign origin",
			target: "https://dash.example.com", candidate: "cand_1",
			preview:   "https://attacker.example/candidates/cand_1",
			wantError: true,
		},
		{
			name:   "query token",
			target: "https://dash.example.com", candidate: "cand_1",
			preview:   "https://dash.example.com/candidates/cand_1?token=secret",
			wantError: true,
		},
		{
			name:   "fragment state",
			target: "https://dash.example.com", candidate: "cand_1",
			preview:   "https://dash.example.com/candidates/cand_1#secret",
			wantError: true,
		},
		{
			name:   "different candidate",
			target: "https://dash.example.com", candidate: "cand_1",
			preview:   "https://dash.example.com/candidates/cand_2",
			wantError: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateCandidatePreviewURL(
				test.target,
				test.candidate,
				test.preview,
			)
			if (err != nil) != test.wantError {
				t.Fatalf("validateCandidatePreviewURL() error = %v", err)
			}
		})
	}
}
