package composectl

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creachadair/jrpc2"
	"github.com/creachadair/jrpc2/channel"
	"github.com/creachadair/jrpc2/handler"
	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

func TestQualificationPreloadedHelpersNeverBuildPullOrRemoveCallerImages(t *testing.T) {
	client, browser := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)
	var calls []qualificationCommandRequest
	executor := qualificationExecutorFunc(func(_ context.Context, request qualificationCommandRequest) ([]byte, error) {
		calls = append(calls, request)
		require.Len(t, request.Arguments, 5)
		require.Equal(t, []string{"image", "inspect", "--format", "{{.Id}}"}, request.Arguments[:4])
		return []byte(request.Arguments[4] + "\n"), nil
	})
	controller, err := New(Options{Root: t.TempDir(), DockerBin: "docker-probe", qualificationExecutor: executor})
	require.NoError(t, err)
	cleanup := &qualificationCleanup{}
	gotClient, gotBrowser, err := controller.prepareQualificationAuthoringImages(t.Context(), qualificationAuthoringOptions{PreloadedClientImage: client, PreloadedBrowserImage: browser}, "unused-build-tag", cleanup)
	require.NoError(t, err)
	require.Equal(t, client, gotClient)
	require.Equal(t, browser, gotBrowser)
	require.NoError(t, cleanup.Run(t.Context()))
	require.Len(t, calls, 2)
}

func TestQualificationPreloadedBrowserForbidsImplicitPull(t *testing.T) {
	executor := &recordingQualificationExecutor{output: []byte("browser-container")}
	runtime := newDockerCLIQualificationRuntime(t.TempDir(), "docker-probe", executor)
	_, err := runtime.Start(t.Context(), qualificationContainerRequest{
		Name: "offline-browser", Image: "sha256:" + strings.Repeat("b", 64), NoPull: true,
		Command: []string{"sleep", "infinity"},
	})
	require.NoError(t, err)
	require.Len(t, executor.requests, 1)
	require.Contains(t, executor.requests[0].Arguments, "--pull=never")
}

func TestQualificationLifecycleTokenUsesBoundedProjectWorkloadAuthority(t *testing.T) {
	clientChannel, serverChannel := channel.Direct()
	worker := &qualificationJSONWorker{client: jrpc2.NewClient(clientChannel, nil)}
	issuedAt := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0o700))
	options := qualificationAuthoringOptions{ProjectID: qualificationProjectID, Environment: "prod", Target: "https://localhost", EvidenceDir: filepath.Join(root, "evidence"), LifecycleCredentialFile: filepath.Join(root, "workload.json")}
	type tokenRequest struct {
		Name        string                  `json:"name"`
		Permissions []access.PermissionPair `json:"permissions"`
		ExpiresAt   time.Time               `json:"expiresAt"`
	}
	requests := make(chan tokenRequest, 1)
	editor, err := access.NewTypedRoleBinding("editor", "", access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal:author"}, access.PermissionRoleEditor, projectgraph.ResourceID(qualificationProjectID))
	require.NoError(t, err)
	upload, err := qualificationRecoveryUploadGrant(qualificationProjectID, "principal:author")
	require.NoError(t, err)
	authority := append(editor.Permissions, upload.Permissions...)
	wide, err := access.ProjectPermissionPairsForActions(projectgraph.ResourceID(qualificationProjectID), qualificationLifecycleActions())
	require.NoError(t, err)
	require.ErrorContains(t, access.ValidatePermissionPairsAgainstAuthority(authority, wide), "(connection.upload)")
	server := jrpc2.NewServer(handler.Map{"createAdministratorAPIToken": handler.New(func(_ context.Context, request tokenRequest) (map[string]string, error) {
		requests <- request
		if err := access.ValidatePermissionPairsAgainstAuthority(authority, request.Permissions); err != nil {
			return nil, err
		}
		return map[string]string{"token": "private-workload-token"}, nil
	})}, nil).Start(serverChannel)
	t.Cleanup(func() { _ = worker.client.Close(); server.Stop(); _ = server.Wait() })
	controller, err := New(Options{Root: root, Now: func() time.Time { return issuedAt }})
	require.NoError(t, err)
	scope, err := controller.issueQualificationLifecycleCredential(t.Context(), worker, options)
	require.NoError(t, err)
	request := <-requests
	require.ErrorContains(t, access.ValidatePermissionPairsAgainstAuthority(editor.Permissions, request.Permissions), "(connection.upload)", "the exact upload grant must be published before issuance")
	require.Equal(t, issuedAt.Add(2*time.Hour), request.ExpiresAt)
	require.Equal(t, "qualification-managed-lifecycle", request.Name)
	var actions []string
	for _, permission := range request.Permissions {
		require.Equal(t, qualificationProjectID, permission.Target.ProjectID.String())
		require.Empty(t, permission.Target.InstanceID)
		actions = append(actions, string(permission.Action))
		if permission.Action == access.ActionConnectionUpload {
			require.Equal(t, projectgraph.ResourceID(qualificationManagedConnectionID), permission.Target.ResourceID)
			require.False(t, permission.Target.IncludeFuture)
		}
	}
	require.Equal(t, qualificationManagedConnectionID, scope.UploadConnectionID)
	require.ElementsMatch(t, []string{"connection.read", "connection.use", "connection.upload", "source.read", "dashboard.read", "semantic.query", "semantic.consume"}, actions)
	require.Equal(t, request.ExpiresAt, scope.ExpiresAt)
	raw, err := json.Marshal(scope)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "private-workload-token")
	require.NotContains(t, string(raw), "password")
	report := validFirstPublicationReport()
	report.LifecycleCredential = scope
	require.NoError(t, validateQualificationFirstPublicationReport(report))
	report.LifecycleCredential.UploadConnectionID = "connection:other"
	require.Error(t, validateQualificationFirstPublicationReport(report))
	report.LifecycleCredential.UploadConnectionID = qualificationManagedConnectionID
	report.LifecycleCredential.ExpiresAt = issuedAt.Add(3 * time.Hour)
	require.Error(t, validateQualificationFirstPublicationReport(report))
}

func TestQualificationPreloadedHelpersRequireAnExactPair(t *testing.T) {
	client, browser := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)
	require.NoError(t, validateQualificationPreloadedImages("", ""))
	require.NoError(t, validateQualificationPreloadedImages(client, browser))
	for _, pair := range [][2]string{{client, ""}, {"", browser}, {"client:latest", browser}, {client, "browser@" + browser}, {client + "\n", browser}} {
		require.Error(t, validateQualificationPreloadedImages(pair[0], pair[1]))
	}
}

func TestQualificationPreloadedHelperRejectsChangedLocalContent(t *testing.T) {
	selected := "sha256:" + strings.Repeat("a", 64)
	executor := &recordingQualificationExecutor{output: []byte("sha256:" + strings.Repeat("b", 64) + "\n")}
	controller, err := New(Options{Root: t.TempDir(), DockerBin: "docker-probe", qualificationExecutor: executor})
	require.NoError(t, err)
	require.Error(t, controller.verifyQualificationPreloadedImage(t.Context(), selected))
	executor.output = []byte(selected + "\n")
	require.NoError(t, controller.verifyQualificationPreloadedImage(t.Context(), selected))
	require.Len(t, executor.requests, 2)
	for _, request := range executor.requests {
		require.Equal(t, []string{"image", "inspect", "--format", "{{.Id}}", selected}, request.Arguments)
	}
}

func TestQualificationLifecycleCredentialRequiresPrivateSeparateNewOutput(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0o700))
	evidence := filepath.Join(root, "evidence")
	require.NoError(t, os.Mkdir(evidence, 0o700))
	valid := filepath.Join(root, "lifecycle.json")
	require.NoError(t, validateQualificationLifecycleOutput(valid, evidence))
	for _, path := range []string{"relative.json", filepath.Join(evidence, "token.json"), filepath.Join(root, "missing", "token.json")} {
		require.Error(t, validateQualificationLifecycleOutput(path, evidence))
	}
	require.NoError(t, os.WriteFile(valid, []byte("existing"), 0o400))
	require.Error(t, validateQualificationLifecycleOutput(valid, evidence))
	link := filepath.Join(root, "linked-evidence")
	require.NoError(t, os.Symlink(evidence, link))
	require.Error(t, validateQualificationLifecycleOutput(filepath.Join(link, "token.json"), evidence))
	broad := filepath.Join(root, "public")
	require.NoError(t, os.Mkdir(broad, 0o755))
	require.Error(t, validateQualificationLifecycleOutput(filepath.Join(broad, "token.json"), evidence))
}

func TestQualificationLifecycleCredentialNeverReplacesAnotherImport(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0o700))
	path := filepath.Join(root, "lifecycle.json")
	credential := qualificationLifecycleCredential{
		qualificationLifecycleCredentialScope: qualificationLifecycleCredentialScope{ProjectID: qualificationProjectID, Environment: "prod", TargetURL: "https://localhost", ExpiresAt: time.Now().Add(2 * time.Hour), Actions: qualificationActionNames(qualificationLifecycleActions())},
		Token:                                 "first-scoped-token",
	}
	require.NoError(t, writeQualificationLifecycleCredential(path, credential))
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o400), info.Mode().Perm())
	credential.Token = "replacement-token"
	require.ErrorIs(t, writeQualificationLifecycleCredential(path, credential), os.ErrExist)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after)
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(before, &decoded))
	require.Equal(t, "first-scoped-token", decoded["token"])
	for _, forbidden := range []string{"password", "temporaryPassword", "qualificationPassword", "publisherToken", "projectClaimToken"} {
		require.NotContains(t, decoded, forbidden)
	}
}
