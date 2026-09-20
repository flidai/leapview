package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	"github.com/flidai/leapview/internal/platform/cliapi"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

const (
	bootstrapOwnerBindingID             = access.BootstrapOwnerBindingID
	bootstrapOwnerBindingName           = access.BootstrapOwnerBindingName
	bootstrapEditorBindingID            = access.BootstrapEditorBindingID
	bootstrapEditorBindingName          = access.BootstrapEditorBindingName
	bootstrapReleaseOperatorBindingID   = access.BootstrapReleaseOperatorBindingID
	bootstrapReleaseOperatorBindingName = access.BootstrapReleaseOperatorBindingName
)

type bootstrapBindingSpec struct {
	id   string
	name string
	role access.PermissionRole
}

var bootstrapBindingSpecs = []bootstrapBindingSpec{
	{id: bootstrapOwnerBindingID, name: bootstrapOwnerBindingName, role: access.PermissionRoleProjectAdmin},
	{id: bootstrapEditorBindingID, name: bootstrapEditorBindingName, role: access.PermissionRoleEditor},
	{id: bootstrapReleaseOperatorBindingID, name: bootstrapReleaseOperatorBindingName, role: access.PermissionRoleReleaseOperator},
}

// bootstrapProjectCommand is the issuer-side half of the one-shot ProjectUID
// claim. It resolves and saves the singleton local authority before any target
// request, then presents it to one instance-admin route.
func bootstrapProjectCommand(ctx context.Context, root *rootOptions) *cobra.Command {
	var externallyIssuedUID string
	format := "text"
	command := &cobra.Command{
		Use:   "bootstrap-project <target>",
		Short: "Bootstrap a target with this deployment authority's ProjectUID",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if format != "text" && format != "json" {
				return fmt.Errorf("bootstrap-project format must be text or json")
			}
			// The issuer state is authoritative local identity. Persist it before
			// resolving credentials or discovering the target so network failures
			// cannot cause a later invocation to mint a different identity.
			authority, err := cliapi.NewProfileStore(clientConfigPath()).ResolveProjectAuthority(externallyIssuedUID, validateProjectAuthorityResourceID)
			if err != nil {
				return err
			}
			target := strings.TrimRight(strings.TrimSpace(args[0]), "/")
			credentials, err := (capabilityAPIClient{}).Resolve(ctx, cliapi.Credentials{Target: target, Token: root.token})
			if err != nil {
				return err
			}
			instance, err := newDeploymentCLIClient(http.DefaultClient, credentials.Target, credentials.Token).instance(ctx)
			if err != nil {
				return fmt.Errorf("discover bootstrap target: %w", err)
			}
			instance.Id = strings.TrimSpace(instance.Id)
			instance.Environment = strings.TrimSpace(instance.Environment)
			if instance.Id == "" || instance.Environment == "" {
				return fmt.Errorf("target returned incomplete bootstrap identity")
			}
			generated := deploymentgen.NewGenClient(capabilityAPITransport{target: credentials.Target, token: credentials.Token, client: http.DefaultClient})
			key := uuid.NewSHA1(uuid.NameSpaceURL, []byte("leapview/project-claim/"+instance.Id+"/"+authority.IssuerID+"/"+authority.ProjectUID+"/"+instance.Environment)).String()
			response, err := generated.BootstrapProjectClaim(ctx, deploymentgen.GenBootstrapProjectClaimClientRequest{
				Headers: deploymentgen.GenBootstrapProjectClaimClientHeaders{IdempotencyKey: key},
				Body: deploymentgen.ProjectClaimBootstrapRequest{
					ProjectUid: authority.ProjectUID, IssuerId: authority.IssuerID, Environment: instance.Environment,
				},
			})
			if err != nil {
				return fmt.Errorf("bootstrap Project claim: %w", err)
			}
			if err := validateProjectClaimBootstrapResponse(response.Body, authority, instance.Environment); err != nil {
				return err
			}
			policyRevision, policyDigest, err := bootstrapProjectOwnerPolicy(
				ctx, accessgen.NewGenClient(capabilityAPITransport{target: credentials.Target, token: credentials.Token, client: http.DefaultClient}),
				instance.Id, response.Body.ProjectUid, response.Body.Environment, response.Body.ClaimedBy,
			)
			if err != nil {
				return fmt.Errorf("bootstrap target authorization policy: %w", err)
			}
			if format == "json" {
				return json.NewEncoder(command.OutOrStdout()).Encode(map[string]any{
					"schemaVersion":               1,
					"type":                        "projectBootstrapped",
					"target":                      credentials.Target,
					"projectUid":                  response.Body.ProjectUid,
					"environment":                 response.Body.Environment,
					"authorizationPolicyRevision": policyRevision,
					"authorizationPolicyDigest":   policyDigest,
				})
			}
			fmt.Fprintf(command.OutOrStdout(), "Bootstrapped %s with ProjectUID %s (%s), authorization policy revision %d (%s)\n", credentials.Target, response.Body.ProjectUid, response.Body.Environment, policyRevision, policyDigest)
			return nil
		},
	}
	command.Flags().StringVar(&root.token, "token", root.token, "instance-admin API token")
	command.Flags().StringVar(&externallyIssuedUID, "project-uid", "", "externally issued ProjectUID (first bootstrap only)")
	command.Flags().StringVar(&format, "format", format, "output format: text or json")
	return command
}

func bootstrapProjectOwnerPolicy(ctx context.Context, client *accessgen.GenClient, targetID, projectID, environment, principalID string) (int64, string, error) {
	if client == nil {
		return 0, "", errors.New("target authorization policy client is required")
	}
	for name, value := range map[string]string{"target": targetID, "project": projectID, "environment": environment, "principal": principalID} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return 0, "", fmt.Errorf("%s identity is not canonical", name)
		}
	}
	key := uuid.NewSHA1(uuid.NameSpaceURL, []byte("leapview/project-policy/"+targetID+"/"+projectID+"/"+environment+"/"+principalID)).String()
	create := func(spec bootstrapBindingSpec, expectedRevision int64) (accessgen.GenCreateProjectRoleBindingClientResponse, error) {
		name := spec.name
		idempotencyKey := key
		if spec.id != bootstrapOwnerBindingID {
			idempotencyKey = uuid.NewSHA1(uuid.NameSpaceURL, []byte(key+"/"+spec.id)).String()
		}
		return client.CreateProjectRoleBinding(ctx, accessgen.GenCreateProjectRoleBindingClientRequest{
			Project: projectID,
			Headers: accessgen.GenCreateProjectRoleBindingClientHeaders{IdempotencyKey: idempotencyKey},
			Body: accessgen.GenSchemaRoleBindingCreateRequest{
				Id: spec.id, Name: &name, SubjectType: string(access.SubjectKindPrincipal), SubjectId: principalID,
				Role: string(spec.role), ExpectedRevision: expectedRevision,
			},
		})
	}
	validateCreated := func(response accessgen.GenSchemaRoleBindingResponse, spec bootstrapBindingSpec, existing []access.RoleBinding) error {
		if err := validateBootstrapBinding(response, spec, projectID, principalID); err != nil {
			return err
		}
		created, err := bootstrapTypedRoleBinding(response.Id, response.Name, response.SubjectType, response.SubjectId, response.Role, response.PermissionProfile, response.Permissions, projectgraph.ResourceID(projectID))
		if err != nil {
			return err
		}
		canonical := append(append([]access.RoleBinding(nil), existing...), created)
		expected, err := access.AuthorizationPolicyDigest(access.AuthorizationPolicyScope{TargetID: targetID, ProjectID: projectID, Environment: environment}, canonical)
		if err != nil {
			return fmt.Errorf("canonicalize created bootstrap binding: %w", err)
		}
		if response.PolicyDigest != expected {
			return errors.New("created bootstrap policy digest does not match its canonical bindings")
		}
		return nil
	}
	created, createErr := create(bootstrapBindingSpecs[0], 0)
	if createErr == nil {
		if err := validateBootstrapOwnerBinding(created.Body, targetID, projectID, environment, principalID); err != nil {
			return 0, "", err
		}
	}

	// The create response can be historical idempotency evidence. Always read
	// the complete current policy before reporting bootstrap success so a replay
	// cannot conceal a later role change or return a stale revision. A target
	// claimed before this bootstrap was introduced can also already carry a
	// migrated policy; never overwrite or reset it.
	limit := int32(200)
	listed, listErr := client.ListProjectRoleBindings(ctx, accessgen.GenListProjectRoleBindingsClientRequest{
		Project: projectID, Params: accessgen.GenListProjectRoleBindingsClientParams{Limit: &limit},
	})
	if listErr != nil {
		if createErr != nil {
			return 0, "", fmt.Errorf("create initial project policy binding: %w (current policy verification failed: %v)", createErr, listErr)
		}
		return 0, "", fmt.Errorf("verify current bootstrap policy: %w", listErr)
	}
	bindings, validationErr := validateBootstrapPolicy(listed.Body, targetID, projectID, environment)
	if validationErr != nil {
		return 0, "", fmt.Errorf("current bootstrap policy is incompatible: %w", validationErr)
	}
	if !bootstrapPrincipalHasAdministrator(bindings, principalID) {
		// A short-lived release created empty revision-one heads from legacy `{}`
		// serving policies. Recover only after proving the current policy is
		// canonical and empty, then append project_admin through ordinary CAS.
		if createErr != nil && len(bindings) == 0 {
			repaired, repairErr := create(bootstrapBindingSpecs[0], listed.Body.PolicyRevision)
			if repairErr != nil {
				return 0, "", fmt.Errorf("create initial project-admin binding: %w (repair canonical empty policy at revision %d: %v)", createErr, listed.Body.PolicyRevision, repairErr)
			}
			if repairErr := validateBootstrapOwnerBinding(repaired.Body, targetID, projectID, environment, principalID); repairErr != nil {
				return 0, "", fmt.Errorf("repair canonical empty bootstrap policy: %w", repairErr)
			}
			current, currentErr := client.ListProjectRoleBindings(ctx, accessgen.GenListProjectRoleBindingsClientRequest{
				Project: projectID, Params: accessgen.GenListProjectRoleBindingsClientParams{Limit: &limit},
			})
			if currentErr != nil {
				return 0, "", fmt.Errorf("verify repaired bootstrap policy: %w", currentErr)
			}
			bindings, currentErr = validateBootstrapPolicy(current.Body, targetID, projectID, environment)
			if currentErr != nil || !bootstrapPrincipalHasAdministrator(bindings, principalID) {
				if currentErr == nil {
					currentErr = errors.New("claiming principal has no administrator role binding")
				}
				return 0, "", fmt.Errorf("repaired bootstrap policy is incompatible: %w", currentErr)
			}
			listed = current
		} else {
			return 0, "", errors.New("claiming principal has no administrator role binding")
		}
	}

	for _, spec := range bootstrapBindingSpecs {
		if bootstrapPrincipalHasTypedRole(bindings, principalID, spec.role) {
			continue
		}
		// Legacy owner/admin bindings are retained as-is. They satisfy the
		// bootstrap administrator prerequisite, while the composable delivery
		// roles below are still added explicitly.
		if spec.role == access.PermissionRoleProjectAdmin && bootstrapPrincipalHasAdministrator(bindings, principalID) {
			continue
		}
		for _, binding := range bindings {
			if binding.ID == spec.id {
				return 0, "", fmt.Errorf("bootstrap binding %q is incompatible", spec.id)
			}
		}
		created, createErr := create(spec, listed.Body.PolicyRevision)
		if createErr == nil {
			if err := validateCreated(created.Body, spec, bindings); err != nil {
				return 0, "", err
			}
		} else {
			current, currentErr := client.ListProjectRoleBindings(ctx, accessgen.GenListProjectRoleBindingsClientRequest{
				Project: projectID, Params: accessgen.GenListProjectRoleBindingsClientParams{Limit: &limit},
			})
			if currentErr != nil {
				return 0, "", fmt.Errorf("create bootstrap %s binding: %w (current policy verification failed: %v)", spec.role, createErr, currentErr)
			}
			currentBindings, currentValidationErr := validateBootstrapPolicy(current.Body, targetID, projectID, environment)
			if currentValidationErr != nil {
				return 0, "", fmt.Errorf("create bootstrap %s binding: %w (current policy is incompatible: %v)", spec.role, createErr, currentValidationErr)
			}
			if !bootstrapPrincipalHasTypedRole(currentBindings, principalID, spec.role) {
				return 0, "", fmt.Errorf("create bootstrap %s binding: %w (current policy is missing the binding)", spec.role, createErr)
			}
			listed, bindings = current, currentBindings
			continue
		}
		current, currentErr := client.ListProjectRoleBindings(ctx, accessgen.GenListProjectRoleBindingsClientRequest{
			Project: projectID, Params: accessgen.GenListProjectRoleBindingsClientParams{Limit: &limit},
		})
		if currentErr != nil {
			return 0, "", fmt.Errorf("verify bootstrap %s binding: %w", spec.role, currentErr)
		}
		currentBindings, currentValidationErr := validateBootstrapPolicy(current.Body, targetID, projectID, environment)
		if currentValidationErr != nil {
			return 0, "", fmt.Errorf("verify bootstrap %s policy: %w", spec.role, currentValidationErr)
		}
		if !bootstrapPrincipalHasTypedRole(currentBindings, principalID, spec.role) {
			return 0, "", fmt.Errorf("created bootstrap %s binding is missing from current policy", spec.role)
		}
		listed, bindings = current, currentBindings
	}
	return listed.Body.PolicyRevision, listed.Body.PolicyDigest, nil
}

func validateBootstrapBinding(binding accessgen.GenSchemaRoleBindingResponse, spec bootstrapBindingSpec, projectID, principalID string) error {
	if binding.Id != spec.id || binding.Name != spec.name ||
		binding.SubjectType != string(access.SubjectKindPrincipal) || binding.SubjectId != principalID ||
		binding.Role != string(spec.role) {
		return fmt.Errorf("created bootstrap %s binding identity is incompatible", spec.role)
	}
	if binding.PolicyRevision <= 0 {
		return fmt.Errorf("created bootstrap %s binding has no policy revision", spec.role)
	}
	if err := platformdigest.ValidateSHA256Identity(binding.PolicyDigest); err != nil {
		return fmt.Errorf("created bootstrap %s binding has invalid policy digest: %w", spec.role, err)
	}
	_, err := bootstrapTypedRoleBinding(binding.Id, binding.Name, binding.SubjectType, binding.SubjectId, binding.Role, binding.PermissionProfile, binding.Permissions, projectgraph.ResourceID(projectID))
	return err
}

func validateBootstrapOwnerBinding(binding accessgen.GenSchemaRoleBindingResponse, targetID, projectID, environment, principalID string) error {
	if binding.Id != bootstrapOwnerBindingID || binding.Name != bootstrapOwnerBindingName ||
		binding.SubjectType != string(access.SubjectKindPrincipal) || binding.SubjectId != principalID ||
		binding.Role != string(access.PermissionRoleProjectAdmin) {
		return errors.New("created bootstrap owner binding identity is incompatible")
	}
	if binding.PolicyRevision <= 0 {
		return errors.New("created bootstrap owner binding has no policy revision")
	}
	if err := platformdigest.ValidateSHA256Identity(binding.PolicyDigest); err != nil {
		return fmt.Errorf("created bootstrap owner binding has invalid policy digest: %w", err)
	}
	typed, err := bootstrapTypedRoleBinding(binding.Id, binding.Name, binding.SubjectType, binding.SubjectId, binding.Role, binding.PermissionProfile, binding.Permissions, projectgraph.ResourceID(projectID))
	if err != nil {
		return err
	}
	expected, err := access.AuthorizationPolicyDigest(
		access.AuthorizationPolicyScope{TargetID: targetID, ProjectID: projectID, Environment: environment},
		[]access.RoleBinding{typed},
	)
	if err != nil {
		return fmt.Errorf("canonicalize created bootstrap owner binding: %w", err)
	}
	if binding.PolicyDigest != expected {
		return errors.New("created bootstrap owner policy digest does not match its canonical binding")
	}
	return nil
}

func bootstrapPrincipalHasAdministrator(bindings []access.RoleBinding, principalID string) bool {
	for _, binding := range bindings {
		if binding.Subject.Kind == access.SubjectKindPrincipal && binding.Subject.ID == principalID && bootstrapAdministratorRole(binding) {
			return true
		}
	}
	return false
}

func bootstrapPrincipalHasTypedRole(bindings []access.RoleBinding, principalID string, role access.PermissionRole) bool {
	for _, binding := range bindings {
		if binding.Subject.Kind == access.SubjectKindPrincipal && binding.Subject.ID == principalID && binding.PermissionRole == role {
			return true
		}
	}
	return false
}

func validateBootstrapPolicy(policy accessgen.GenSchemaRoleBindingListResponse, targetID, projectID, environment string) ([]access.RoleBinding, error) {
	if policy.TargetId != targetID || policy.ProjectId != projectID || policy.Environment != environment || policy.PolicyRevision <= 0 {
		return nil, errors.New("authorization policy scope or revision is incompatible")
	}
	if err := platformdigest.ValidateSHA256Identity(policy.PolicyDigest); err != nil {
		return nil, fmt.Errorf("authorization policy digest is invalid: %w", err)
	}
	if policy.Page.NextCursor != nil && *policy.Page.NextCursor != "" {
		return nil, errors.New("authorization policy verification is paginated")
	}
	bindings := make([]access.RoleBinding, 0, len(policy.Items))
	for _, item := range policy.Items {
		subject, err := access.NewSubjectRef(access.SubjectKind(item.SubjectType), item.SubjectId)
		if err != nil {
			return nil, err
		}
		var binding access.RoleBinding
		if item.PermissionProfile != nil || item.Permissions != nil {
			binding, err = bootstrapTypedRoleBinding(item.Id, item.Name, item.SubjectType, item.SubjectId, item.Role, item.PermissionProfile, item.Permissions, projectgraph.ResourceID(projectID))
		} else {
			role, roleErr := access.ParseProjectRole(item.Role)
			if roleErr != nil {
				return nil, roleErr
			}
			if item.Capabilities == nil {
				return nil, errors.New("legacy role binding omitted capabilities")
			}
			capabilities := make([]access.Capability, len(*item.Capabilities))
			for index, capability := range *item.Capabilities {
				capabilities[index] = access.Capability(capability)
			}
			binding = access.RoleBinding{ID: item.Id, Name: item.Name, Subject: subject, Role: role, Capabilities: capabilities}
			if err = access.ValidateAuthorizationRoleBinding(binding); err != nil {
				return nil, err
			}
		}
		if err != nil {
			return nil, err
		}
		if item.PolicyRevision != policy.PolicyRevision || item.PolicyDigest != policy.PolicyDigest {
			return nil, fmt.Errorf("role binding %q is not bound to the policy head", item.Id)
		}
		bindings = append(bindings, binding)
	}
	digest, err := access.AuthorizationPolicyDigest(access.AuthorizationPolicyScope{TargetID: targetID, ProjectID: projectID, Environment: environment}, bindings)
	if err != nil {
		return nil, err
	}
	if digest != policy.PolicyDigest {
		return nil, errors.New("authorization policy digest does not match its canonical bindings")
	}
	return bindings, nil
}

func bootstrapAdministratorRole(binding access.RoleBinding) bool {
	return binding.PermissionRole == access.PermissionRoleProjectAdmin || binding.Role == access.ProjectRoleOwner || binding.Role == access.ProjectRoleAdmin
}

func bootstrapTypedRoleBinding(id, name, subjectType, subjectID, role string, profile *accessgen.GenSchemaPermissionCatalogProfile, pairs *[]accessgen.GenSchemaPermissionPair, projectID projectgraph.ResourceID) (access.RoleBinding, error) {
	if profile == nil || string(*profile) != access.PermissionCatalogProfile || pairs == nil {
		return access.RoleBinding{}, errors.New("typed role binding omitted its permission profile or expansion")
	}
	encoded, err := json.Marshal(*pairs)
	if err != nil {
		return access.RoleBinding{}, fmt.Errorf("encode typed role binding permissions: %w", err)
	}
	permissions, err := access.DecodePermissionPairs(encoded)
	if err != nil {
		return access.RoleBinding{}, fmt.Errorf("decode typed role binding permissions: %w", err)
	}
	subject, err := access.NewSubjectRef(access.SubjectKind(subjectType), subjectID)
	if err != nil {
		return access.RoleBinding{}, err
	}
	binding := access.RoleBinding{ID: id, Name: name, Subject: subject, PermissionRole: access.PermissionRole(role), PermissionProfile: string(*profile), Permissions: permissions}
	if err := access.ValidateTypedRoleBindingForProject(binding, projectID); err != nil {
		return access.RoleBinding{}, err
	}
	return binding, nil
}

func validateBootstrapCapabilities(actual []accessgen.GenSchemaCapability, expected []access.Capability) error {
	if len(actual) != len(expected) {
		return errors.New("bootstrap owner capabilities are not canonical")
	}
	for index := range expected {
		if string(actual[index]) != string(expected[index]) {
			return errors.New("bootstrap owner capabilities are not canonical")
		}
	}
	return nil
}

func validateProjectClaimBootstrapResponse(response deploymentgen.ProjectClaimBootstrapResponse, authority cliapi.ProjectAuthority, environment string) error {
	if response.ProjectUid != authority.ProjectUID {
		return fmt.Errorf("bootstrap Project claim returned ProjectUID %q, want %q", response.ProjectUid, authority.ProjectUID)
	}
	if response.Environment != environment {
		return fmt.Errorf("bootstrap Project claim returned environment %q, want %q", response.Environment, environment)
	}
	if response.ClaimedBy == "" || response.ClaimedBy != strings.TrimSpace(response.ClaimedBy) {
		return fmt.Errorf("bootstrap Project claim returned non-canonical claimedBy %q", response.ClaimedBy)
	}
	claimedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(response.ClaimedAt))
	if err != nil || claimedAt.IsZero() {
		return fmt.Errorf("bootstrap Project claim returned invalid claimedAt %q", response.ClaimedAt)
	}
	return nil
}
