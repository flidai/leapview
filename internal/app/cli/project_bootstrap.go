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
			publisherKey := uuid.NewSHA1(uuid.NameSpaceURL, []byte("leapview/project-claim-publisher/"+instance.Id+"/"+authority.IssuerID+"/"+authority.ProjectUID+"/"+instance.Environment)).String()
			publisherResponse, err := accessgen.NewGenClient(capabilityAPITransport{target: credentials.Target, token: credentials.Token, client: http.DefaultClient}).ExchangeProjectClaimPublisher(ctx, accessgen.GenExchangeProjectClaimPublisherClientRequest{
				Project: response.Body.ProjectUid,
				Headers: accessgen.GenExchangeProjectClaimPublisherClientHeaders{IdempotencyKey: publisherKey},
			})
			if err != nil {
				return fmt.Errorf("exchange Project claim publisher: %w", err)
			}
			if err := validateProjectClaimPublisherResponse(publisherResponse.Body); err != nil {
				return err
			}
			policyRevision, policyDigest, err := bootstrapProjectOwnerPolicy(
				ctx, accessgen.NewGenClient(capabilityAPITransport{target: credentials.Target, token: publisherResponse.Body.PublisherToken, client: http.DefaultClient}),
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
					"claimCredentialId":           publisherResponse.Body.ClaimCredentialId,
					"publisherToken":              publisherResponse.Body.PublisherToken,
					"publisherTokenExpiresAt":     publisherResponse.Body.PublisherTokenExpiresAt,
				})
			}
			fmt.Fprintf(command.OutOrStdout(), "Bootstrapped %s with ProjectUID %s (%s), authorization policy revision %d (%s); publisher handoff awaiting acknowledgement\n", credentials.Target, response.Body.ProjectUid, response.Body.Environment, policyRevision, policyDigest)
			return nil
		},
	}
	command.Flags().StringVar(&root.token, "token", root.token, "instance-admin API token")
	command.Flags().StringVar(&externallyIssuedUID, "project-uid", "", "externally issued ProjectUID (first bootstrap only)")
	command.Flags().StringVar(&format, "format", format, "output format: text or json")
	return command
}

func acknowledgeProjectClaimPublisherCommand(ctx context.Context, root *rootOptions) *cobra.Command {
	var claimCredentialID string
	command := &cobra.Command{
		Use:   "acknowledge-project-claim-publisher <target> <project>",
		Short: "Acknowledge durable handoff of a Project publisher credential",
		Args:  cobra.ExactArgs(2),
		RunE: func(command *cobra.Command, args []string) error {
			resolved, err := (capabilityAPIClient{}).Resolve(ctx, cliapi.Credentials{Target: args[0], Token: root.token})
			if err != nil {
				return err
			}
			claimCredentialID = strings.TrimSpace(claimCredentialID)
			if claimCredentialID == "" || claimCredentialID != strings.TrimSpace(claimCredentialID) {
				return errors.New("claim credential ID is required")
			}
			key := uuid.NewSHA1(uuid.NameSpaceURL, []byte("leapview/project-claim-publisher-ack/"+claimCredentialID+"/"+args[1])).String()
			_, err = accessgen.NewGenClient(capabilityAPITransport{target: resolved.Target, token: resolved.Token, client: http.DefaultClient}).AcknowledgeProjectClaimPublisher(ctx, accessgen.GenAcknowledgeProjectClaimPublisherClientRequest{
				Project: args[1],
				Headers: accessgen.GenAcknowledgeProjectClaimPublisherClientHeaders{IdempotencyKey: key},
				Body:    accessgen.GenSchemaProjectClaimPublisherAcknowledgeRequest{ClaimCredentialId: claimCredentialID},
			})
			if err != nil {
				return fmt.Errorf("acknowledge Project claim publisher: %w", err)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "Acknowledged Project claim publisher for %s\n", args[1])
			return err
		},
	}
	command.Flags().StringVar(&root.token, "token", root.token, "project publisher API token")
	command.Flags().StringVar(&claimCredentialID, "claim-credential-id", "", "one-time claim credential ID")
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
	validateCreated := func(response accessgen.GenSchemaRoleBindingResponse, spec bootstrapBindingSpec, existing []access.RoleBinding, grants []access.AuthorizationGrant) error {
		if err := validateBootstrapBinding(response, spec, projectID, principalID); err != nil {
			return err
		}
		created, err := bootstrapTypedRoleBinding(response.Id, response.Name, response.SubjectType, response.SubjectId, response.Role, response.PermissionProfile, response.Permissions, projectgraph.ResourceID(projectID))
		if err != nil {
			return err
		}
		canonical := append(append([]access.RoleBinding(nil), existing...), created)
		expected, err := access.AuthorizationPolicyDigest(access.AuthorizationPolicyScope{TargetID: targetID, ProjectID: projectID, Environment: environment}, canonical, grants...)
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
	grants, err := bootstrapPolicyGrants(ctx, client, listed.Body, targetID, projectID, environment)
	if err != nil {
		return 0, "", fmt.Errorf("verify current bootstrap grants: %w", err)
	}
	bindings, validationErr := validateBootstrapPolicy(listed.Body, targetID, projectID, environment, grants...)
	if validationErr != nil {
		return 0, "", fmt.Errorf("current bootstrap policy is incompatible: %w", validationErr)
	}
	if !bootstrapPrincipalHasProjectAdmin(bindings, principalID) {
		return 0, "", errors.New("claiming principal has no typed project_admin role binding")
	}

	for _, spec := range bootstrapBindingSpecs {
		if bootstrapPrincipalHasTypedRole(bindings, principalID, spec.role) {
			continue
		}
		for _, binding := range bindings {
			if binding.ID == spec.id {
				return 0, "", fmt.Errorf("bootstrap binding %q is incompatible", spec.id)
			}
		}
		created, createErr := create(spec, listed.Body.PolicyRevision)
		if createErr == nil {
			if err := validateCreated(created.Body, spec, bindings, grants); err != nil {
				return 0, "", err
			}
		} else {
			current, currentErr := client.ListProjectRoleBindings(ctx, accessgen.GenListProjectRoleBindingsClientRequest{
				Project: projectID, Params: accessgen.GenListProjectRoleBindingsClientParams{Limit: &limit},
			})
			if currentErr != nil {
				return 0, "", fmt.Errorf("create bootstrap %s binding: %w (current policy verification failed: %v)", spec.role, createErr, currentErr)
			}
			currentGrants, grantErr := bootstrapPolicyGrants(ctx, client, current.Body, targetID, projectID, environment)
			if grantErr != nil {
				return 0, "", fmt.Errorf("verify bootstrap %s grants: %w", spec.role, grantErr)
			}
			currentBindings, currentValidationErr := validateBootstrapPolicy(current.Body, targetID, projectID, environment, currentGrants...)
			if currentValidationErr != nil {
				return 0, "", fmt.Errorf("create bootstrap %s binding: %w (current policy is incompatible: %v)", spec.role, createErr, currentValidationErr)
			}
			if !bootstrapPrincipalHasTypedRole(currentBindings, principalID, spec.role) {
				return 0, "", fmt.Errorf("create bootstrap %s binding: %w (current policy is missing the binding)", spec.role, createErr)
			}
			listed, bindings, grants = current, currentBindings, currentGrants
			continue
		}
		current, currentErr := client.ListProjectRoleBindings(ctx, accessgen.GenListProjectRoleBindingsClientRequest{
			Project: projectID, Params: accessgen.GenListProjectRoleBindingsClientParams{Limit: &limit},
		})
		if currentErr != nil {
			return 0, "", fmt.Errorf("verify bootstrap %s binding: %w", spec.role, currentErr)
		}
		currentGrants, grantErr := bootstrapPolicyGrants(ctx, client, current.Body, targetID, projectID, environment)
		if grantErr != nil {
			return 0, "", fmt.Errorf("verify bootstrap %s grants: %w", spec.role, grantErr)
		}
		currentBindings, currentValidationErr := validateBootstrapPolicy(current.Body, targetID, projectID, environment, currentGrants...)
		if currentValidationErr != nil {
			return 0, "", fmt.Errorf("verify bootstrap %s policy: %w", spec.role, currentValidationErr)
		}
		if !bootstrapPrincipalHasTypedRole(currentBindings, principalID, spec.role) {
			return 0, "", fmt.Errorf("created bootstrap %s binding is missing from current policy", spec.role)
		}
		listed, bindings, grants = current, currentBindings, currentGrants
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

func bootstrapPrincipalHasProjectAdmin(bindings []access.RoleBinding, principalID string) bool {
	for _, binding := range bindings {
		if binding.Subject.Kind == access.SubjectKindPrincipal && binding.Subject.ID == principalID && binding.PermissionRole == access.PermissionRoleProjectAdmin {
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

func validateBootstrapPolicy(policy accessgen.GenSchemaRoleBindingListResponse, targetID, projectID, environment string, grants ...access.AuthorizationGrant) ([]access.RoleBinding, error) {
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
		binding, err := bootstrapTypedRoleBinding(item.Id, item.Name, item.SubjectType, item.SubjectId, item.Role, item.PermissionProfile, item.Permissions, projectgraph.ResourceID(projectID))
		if err != nil {
			return nil, err
		}
		if item.PolicyRevision != policy.PolicyRevision || item.PolicyDigest != policy.PolicyDigest {
			return nil, fmt.Errorf("role binding %q is not bound to the policy head", item.Id)
		}
		bindings = append(bindings, binding)
	}
	digest, err := access.AuthorizationPolicyDigest(access.AuthorizationPolicyScope{TargetID: targetID, ProjectID: projectID, Environment: environment}, bindings, grants...)
	if err != nil {
		return nil, err
	}
	if digest != policy.PolicyDigest {
		return nil, errors.New("authorization policy digest does not match its canonical bindings")
	}
	return bindings, nil
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

func validateProjectClaimPublisherResponse(response accessgen.ProjectClaimPublisherExchangeResponse) error {
	if _, err := uuid.Parse(strings.TrimSpace(response.ClaimCredentialId)); err != nil || response.ClaimCredentialId != strings.TrimSpace(response.ClaimCredentialId) {
		return errors.New("Project claim publisher exchange returned an invalid claim credential ID")
	}
	if strings.TrimSpace(response.PublisherToken) == "" {
		return errors.New("Project claim publisher exchange returned an empty publisher token")
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(response.PublisherTokenExpiresAt))
	if err != nil || expiresAt.IsZero() || !expiresAt.After(time.Now().UTC()) {
		return errors.New("Project claim publisher exchange returned an invalid expiry")
	}
	return nil
}

// A role listing no longer describes the entire policy once resource grants
// exist. Fetch the matching grant revision instead of rejecting a safe replay
// or weakening the canonical digest check. Legacy role-only targets need no
// grants endpoint. Concurrent changes and incomplete pages fail closed.
func bootstrapPolicyGrants(ctx context.Context, client *accessgen.GenClient, policy accessgen.GenSchemaRoleBindingListResponse, targetID, projectID, environment string) ([]access.AuthorizationGrant, error) {
	for _, item := range policy.Items {
		if _, err := bootstrapTypedRoleBinding(item.Id, item.Name, item.SubjectType, item.SubjectId, item.Role, item.PermissionProfile, item.Permissions, projectgraph.ResourceID(projectID)); err != nil {
			return nil, err
		}
	}
	if _, err := validateBootstrapPolicy(policy, targetID, projectID, environment); err == nil {
		return nil, nil
	}
	limit := int32(200)
	result, err := client.ListGrants(ctx, accessgen.GenListGrantsClientRequest{Project: projectID, Params: accessgen.GenListGrantsClientParams{Limit: &limit}})
	if err != nil {
		return nil, err
	}
	current := result.Body
	if current.TargetId != targetID || current.ProjectId != projectID || current.Environment != environment || current.PolicyRevision != policy.PolicyRevision || current.PolicyDigest != policy.PolicyDigest {
		return nil, errors.New("grant policy does not match role policy revision")
	}
	if current.Page.NextCursor != nil && *current.Page.NextCursor != "" {
		return nil, errors.New("grant policy verification is paginated")
	}
	grants := make([]access.AuthorizationGrant, 0, len(current.Items))
	for _, item := range current.Items {
		if item.PolicyRevision != policy.PolicyRevision || item.PolicyDigest != policy.PolicyDigest {
			return nil, errors.New("grant is not bound to policy head")
		}
		resource, err := access.NewResourceRef(projectgraph.ResourceID(item.ResourceId), projectgraph.Kind(item.ResourceKind))
		if err != nil {
			return nil, err
		}
		grant := access.AuthorizationGrant{ID: item.Id, Resource: resource, Subject: access.SubjectRef{Kind: access.SubjectKind(item.SubjectType), ID: item.SubjectId}, Capability: access.Capability(item.Capability)}
		if item.Name != nil {
			grant.Name = *item.Name
		}
		if err := access.ValidateAuthorizationGrant(grant); err != nil {
			return nil, err
		}
		grants = append(grants, grant)
	}
	if _, err := validateBootstrapPolicy(policy, targetID, projectID, environment, grants...); err != nil {
		return nil, err
	}
	return grants, nil
}
