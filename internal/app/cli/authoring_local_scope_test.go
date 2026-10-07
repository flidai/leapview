package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesscli "github.com/flidai/leapview/internal/access/cli"
	"github.com/flidai/leapview/internal/platform/cliapi"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

func TestLocalRetainedAuthoringScopeUsesCurrentExactSession(t *testing.T) {
	for _, test := range []struct {
		name       string
		mutate     func(map[string]any)
		wantDenied bool
		wantError  bool
	}{
		{name: "bootstrap scope"},
		{name: "old default scope", mutate: func(session map[string]any) {
			permissions, err := access.ProjectPermissionPairsForActions("lvproject_test", access.DefaultAuthoringActions())
			require.NoError(t, err)
			session["permissions"] = permissions
		}, wantDenied: true, wantError: true},
		{name: "read only scope", mutate: func(session map[string]any) {
			permissions, err := access.ProjectPermissionPairsForActions("lvproject_test", []access.Action{access.ActionProjectAccessRead})
			require.NoError(t, err)
			session["permissions"] = permissions
		}, wantDenied: true, wantError: true},
		{name: "foreign target", mutate: func(session map[string]any) { session["targetId"] = "foreign" }, wantError: true},
		{name: "foreign project", mutate: func(session map[string]any) { session["projectId"] = "foreign" }, wantError: true},
		{name: "other session", mutate: func(session map[string]any) { session["id"] = "other" }, wantError: true},
		{name: "not current", mutate: func(session map[string]any) { session["current"] = false }, wantError: true},
		{name: "revoked", mutate: func(session map[string]any) { session["revokedAt"] = "2026-01-01T00:00:00Z" }, wantError: true},
		{name: "malformed permissions", mutate: func(session map[string]any) { session["permissions"] = []map[string]any{{"action": "unknown"}} }, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			permissions, err := access.ProjectPermissionPairsForActions(projectgraph.ResourceID("lvproject_test"), []access.Action{access.ActionProjectAccessRead, access.ActionProjectAccessManage})
			require.NoError(t, err)
			session := map[string]any{
				"id": "session-retained", "current": true, "targetId": "instance-local", "projectId": "lvproject_test",
				"kind": access.AuthoringSessionHumanCLI, "clientId": access.AuthoringCLIClientID,
				"permissionProfile": access.PermissionCatalogProfile, "permissions": permissions,
			}
			if test.mutate != nil {
				test.mutate(session)
			}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				require.Equal(t, http.MethodGet, r.Method)
				require.Equal(t, "/api/v1/me/authoring-sessions", r.URL.Path)
				require.Equal(t, "Bearer retained-token", r.Header.Get("Authorization"))
				require.Equal(t, "200", r.URL.Query().Get("limit"))
				w.Header().Set("Content-Type", "application/json")
				if calls == 1 {
					require.Empty(t, r.URL.Query().Get("pageToken"))
					require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"items": []any{}, "page": map[string]string{"nextCursor": "second"}}))
					return
				}
				require.Equal(t, "second", r.URL.Query().Get("pageToken"))
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"items": []any{session}, "page": map[string]any{}}))
			}))
			defer server.Close()
			err = (localSessionAuthority{}).CheckBootstrapScope(t.Context(), accesscli.ResolvedCredential{
				Profile:   cliapi.TargetProfile{Origin: server.URL, InstanceID: "instance-local", ProjectID: "lvproject_test"},
				SessionID: "session-retained", AccessToken: "retained-token",
			})
			require.Equal(t, 2, calls)
			if test.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			if test.wantDenied {
				require.ErrorIs(t, err, access.ErrAuthoringScopeDenied)
			} else {
				require.NotErrorIs(t, err, access.ErrAuthoringScopeDenied)
			}
		})
	}
}

func TestLocalRetainedAuthoringScopeDoesNotTreatInspectionFailureAsInsufficientScope(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusServiceUnavailable} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
		err := (localSessionAuthority{}).CheckBootstrapScope(t.Context(), accesscli.ResolvedCredential{
			Profile:   cliapi.TargetProfile{Origin: server.URL, InstanceID: "instance-local", ProjectID: "lvproject_test"},
			SessionID: "session-retained", AccessToken: "retained-token",
		})
		server.Close()
		require.Error(t, err)
		require.NotErrorIs(t, err, access.ErrAuthoringScopeDenied)
	}
}
