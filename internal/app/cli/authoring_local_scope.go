package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	accesscli "github.com/flidai/leapview/internal/access/cli"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// localBootstrapActions covers the initial policy and development profile that
// local dev manages. Use the same ceiling for login and retained-session checks.
func localBootstrapActions() []access.Action {
	return []access.Action{
		access.ActionProjectAccessRead, access.ActionProjectAccessManage,
		access.ActionProjectSettingsRead, access.ActionProjectSettingsUpdate,
	}
}

// CheckBootstrapScope reads the authenticated session's ceiling before reusing
// a local credential. Older CLI sessions can be renewed through the ordinary
// local device flow; inspection failures must not silently replace credentials.
func (authority localSessionAuthority) CheckBootstrapScope(ctx context.Context, credential accesscli.ResolvedCredential) error {
	if _, err := localSessionOrigin(credential.Profile.Origin); err != nil {
		return err
	}
	client := accessgen.NewGenClient(capabilityAPITransport{
		target: credential.Profile.Origin, token: credential.AccessToken, client: defaultCLIHTTPClient,
	})
	return checkLocalBootstrapScope(ctx, client, credential)
}

func checkLocalBootstrapScope(ctx context.Context, client *accessgen.GenClient, credential accesscli.ResolvedCredential) error {
	project, err := projectgraph.NewResourceID(credential.Profile.ProjectID)
	if err != nil {
		return err
	}
	required, err := access.ProjectPermissionPairsForActions(project, localBootstrapActions())
	if err != nil {
		return err
	}
	limit := int32(200)
	params := accessgen.GenListCurrentAuthoringSessionsClientParams{Limit: &limit}
	seen := map[string]bool{}
	for {
		response, err := client.ListCurrentAuthoringSessions(ctx, accessgen.GenListCurrentAuthoringSessionsClientRequest{Params: params})
		if err != nil {
			return err
		}
		for _, session := range response.Body.Items {
			if session.Id != credential.SessionID || !session.Current {
				continue
			}
			if session.TargetId != credential.Profile.InstanceID || session.ProjectId != credential.Profile.ProjectID ||
				session.Kind != string(access.AuthoringSessionHumanCLI) || session.ClientId != access.AuthoringCLIClientID ||
				string(session.PermissionProfile) != access.PermissionCatalogProfile || session.RevokedAt != nil {
				return errors.New("retained local authoring session identity is incompatible")
			}
			encoded, err := json.Marshal(session.Permissions)
			if err != nil {
				return err
			}
			permissions, err := access.DecodePermissionPairs(encoded)
			if err != nil {
				return fmt.Errorf("decode retained local authoring permissions: %w", err)
			}
			scope, err := access.NewAuthoringScope(session.TargetId, project, permissions)
			if err != nil {
				return fmt.Errorf("validate retained local authoring scope: %w", err)
			}
			return scope.AuthorizePairs(credential.Profile.InstanceID, credential.Profile.ProjectID, required)
		}
		params.PageToken = response.Body.Page.NextCursor
		if params.PageToken == nil || *params.PageToken == "" {
			return errors.New("current local authoring session was not returned by the target")
		}
		if seen[*params.PageToken] {
			return errors.New("local authoring session pagination repeated a cursor")
		}
		seen[*params.PageToken] = true
	}
}
