package module

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/jobs"
	jobsqlite "github.com/flidai/leapview/internal/platform/jobs/sqlite"
	"github.com/flidai/leapview/internal/release"
	releaseapi "github.com/flidai/leapview/internal/release/api"
	releasesqlite "github.com/flidai/leapview/internal/release/sqlite"
	"github.com/flidai/leapview/internal/servingstate"
	servingstatemodule "github.com/flidai/leapview/internal/servingstate/module"
	workspacemodule "github.com/flidai/leapview/internal/workspace/module"
	"github.com/stretchr/testify/require"
)

func TestFinalizeReleaseGeneratedExecutionContractEndToEnd(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "finalize.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	jobStore := jobsqlite.NewRepository(store.SQLDB())
	states, err := servingstatemodule.Build(t.Context(), servingstatemodule.Config{Database: store.SQLDB()})
	require.NoError(t, err)
	workspaces, err := workspacemodule.BuildDirectory(store.SQLDB(), nil)
	require.NoError(t, err)
	module, err := Build(t.Context(), Config{
		Database:          store.SQLDB(),
		States:            states,
		Workspaces:        workspaces,
		ArtifactDirectory: t.TempDir(),
		Environment:       servingstate.DefaultEnvironment,
		API: APIConfig{
			CurrentPrincipal: func(*http.Request) (Principal, bool) { return Principal{ID: "principal_1"}, true },
			Jobs:             jobStore,
			Workflow:         jobStore,
		},
	})
	require.NoError(t, err)

	digest := "sha256:" + strings.Repeat("a", 64)
	createBody, err := json.Marshal(releaseapi.CreateRequest{
		ProjectDigest: digest,
		Workspaces:    []releaseapi.WorkspaceManifest{{Workspace: "workspace_1", ArtifactDigest: digest}},
		Connections:   []releaseapi.ConnectionPin{},
	})
	require.NoError(t, err)
	create := httptest.NewRequest("POST", "/api/v1/projects/project_1/releases", bytes.NewReader(createBody))
	createdResponse := httptest.NewRecorder()
	module.CreateRelease(createdResponse, create, "project_1", "create_1")
	require.Equal(t, 201, createdResponse.Code, createdResponse.Body.String())
	var created releaseapi.Response
	require.NoError(t, json.Unmarshal(createdResponse.Body.Bytes(), &created))
	createdRelease, err := module.service.Get(t.Context(), "project_1", created.ID)
	require.NoError(t, err)
	require.Len(t, createdRelease.Artifacts, 1)
	artifact := createdRelease.Artifacts[0]
	require.NoError(t, releasesqlite.NewRepository(store.SQLDB()).RecordArtifact(t.Context(), release.Artifact{
		ReleaseID: created.ID, WorkspaceID: artifact.WorkspaceID, ServingStateID: artifact.ServingStateID,
		ExpectedDigest: digest, SizeBytes: 1,
	}))
	finalizeResponse := httptest.NewRecorder()
	module.FinalizeRelease(finalizeResponse, httptest.NewRequest("POST", "/finalize", nil), "project_1", created.ID, "finalize_1")
	require.Equal(t, 202, finalizeResponse.Code, finalizeResponse.Body.String())
	var accepted releaseapi.Response
	require.NoError(t, json.Unmarshal(finalizeResponse.Body.Bytes(), &accepted))
	require.Equal(t, releaseapi.Status(release.StatusValidating), accepted.Status)

	execution := module.finalizeExecution
	job, err := jobStore.Get(t.Context(), "release:"+created.ID+":finalize")
	require.NoError(t, err)
	require.Equal(t, execution.JobKind, job.Kind)
	require.Equal(t, execution.ResourceKind, job.ResourceKind)
	require.Equal(t, created.ID, job.ResourceID)

	handlers := module.JobHandlers()
	require.Len(t, handlers, 1)
	require.Equal(t, execution.JobKind, handlers[0].Kind())
	events, err := jobStore.ListEvents(t.Context(), execution.ResourceKind, created.ID, 0, 20)
	require.NoError(t, err)
	eventTypes := make([]string, 0, len(events))
	for _, event := range events {
		eventTypes = append(eventTypes, event.EventType)
	}
	require.Contains(t, eventTypes, execution.InitialEvent)
}

func TestFinalizeReleaseRejectsJobHandlerRegistrationDrift(t *testing.T) {
	execution, err := loadFinalizeExecutionContract()
	require.NoError(t, err)
	err = validateFinalizeJobHandlers(execution, []jobs.Handler{jobs.HandlerFunc{JobKind: "release.wrong"}})
	require.ErrorContains(t, err, "does not match generated kind")
}
