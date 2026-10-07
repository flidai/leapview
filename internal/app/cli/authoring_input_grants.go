package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	protocolgen "github.com/flidai/leapview/internal/platform/http/api/gen"
	"github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
)

func ensureDeclaredDevelopmentInputGrants(ctx context.Context, client *accessgen.GenClient, targetID, projectID, environment, principalID string, connections []string) error {
	if ctx == nil || client == nil || strings.TrimSpace(targetID) == "" || strings.TrimSpace(projectID) == "" || strings.TrimSpace(principalID) == "" || environment != "dev" {
		return errors.New("declared input grants require an authenticated exact local dev target")
	}
	if len(connections) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Read both policy collections at one canonical head. Neither a successful
	// write response nor a historical idempotency replay proves current policy.
	read := func() (int64, []access.AuthorizationGrant, error) {
		limit := int32(200)
		policy, err := client.ListProjectRoleBindings(ctx, accessgen.GenListProjectRoleBindingsClientRequest{Project: projectID, Params: accessgen.GenListProjectRoleBindingsClientParams{Limit: &limit}})
		if err != nil {
			return 0, nil, err
		}
		grants, err := bootstrapPolicyGrants(ctx, client, policy.Body, targetID, projectID, environment)
		if err != nil {
			return 0, nil, err
		}
		bindings, err := validateBootstrapPolicy(policy.Body, targetID, projectID, environment, grants...)
		if err != nil {
			return 0, nil, err
		}
		if !bootstrapPrincipalHasProjectAdmin(bindings, principalID) {
			return 0, nil, errors.New("declared input owner has no typed project_admin binding")
		}
		return policy.Body.PolicyRevision, grants, nil
	}
	revision, grants, err := read()
	if err != nil {
		return fmt.Errorf("verify declared input policy: %w", err)
	}
	ordered := append([]string(nil), connections...)
	sort.Strings(ordered)
	for _, connection := range ordered {
		resource, err := access.NewResourceRef(graph.ResourceID(connection), graph.KindConnection)
		if err != nil {
			return err
		}
		permission, err := access.NewExactPermissionPair(access.ActionConnectionUpload, graph.ResourceID(projectID), resource)
		if err != nil {
			return err
		}
		identity, _ := json.Marshal([]string{targetID, projectID, environment, principalID, connection})
		key := uuid.NewSHA1(uuid.NameSpaceURL, append([]byte("leapview/local-declared-input-upload/"), identity...)).String()
		expected := access.AuthorizationGrant{
			ID: "local-input-upload-" + key, Name: "Local declared input upload",
			Resource: resource, Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principalID},
			PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{permission},
		}
		present := func(current []access.AuthorizationGrant) (bool, error) {
			for _, grant := range current {
				if grant.ID != expected.ID {
					continue
				}
				if !reflect.DeepEqual(grant, expected) {
					return false, fmt.Errorf("declared input grant %q has incompatible authority", expected.ID)
				}
				return true, nil
			}
			return false, nil
		}
		if found, err := present(grants); err != nil {
			return err
		} else if found {
			continue
		}
		encoded, err := json.Marshal(expected.Permissions)
		if err != nil {
			return err
		}
		var pairs []protocolgen.PermissionPair
		if err := json.Unmarshal(encoded, &pairs); err != nil {
			return err
		}
		profile := protocolgen.PermissionCatalogProfile(access.PermissionCatalogProfile)
		_, createErr := client.CreateGrant(ctx, accessgen.GenCreateGrantClientRequest{
			Project: projectID, Headers: accessgen.GenCreateGrantClientHeaders{IdempotencyKey: key},
			Body: accessgen.GenSchemaTargetGrantCreateRequest{
				Id: expected.ID, Name: &expected.Name, ResourceId: connection, ResourceKind: protocolgen.ResourceKindConnection,
				SubjectType: string(expected.Subject.Kind), SubjectId: principalID, ExpectedRevision: revision,
				PermissionProfile: &profile, Permissions: &pairs,
			},
		})
		revision, grants, err = read()
		if err != nil {
			return fmt.Errorf("verify current declared input grant: %w", err)
		}
		found, err := present(grants)
		if err != nil {
			return err
		}
		if !found {
			if createErr != nil {
				return fmt.Errorf("create declared input upload grant: %w", createErr)
			}
			return errors.New("created declared input upload grant is absent from current policy")
		}
	}
	return nil
}
