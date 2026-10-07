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
	"github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
)

type declaredDevelopmentInputGrantStager func(context.Context, access.AuthorizationGrant, int64, string) error

func ensureDeclaredDevelopmentInputGrants(ctx context.Context, client *accessgen.GenClient, targetID, projectID, environment, principalID string, connections []string, stage declaredDevelopmentInputGrantStager) error {
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
			ID:       "local-input-upload-" + key,
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
		if stage == nil {
			return errors.New("owned local runtime grant staging is unavailable")
		}
		// Only the checkout-owned local operator can stage new authority. The
		// native authoring credential is used solely for authenticated readback;
		// project.access.manage does not itself authorize arbitrary typed grants.
		createErr := stage(ctx, expected, revision, key)
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
