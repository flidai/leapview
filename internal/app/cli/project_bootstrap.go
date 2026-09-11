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
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

const (
	bootstrapOwnerBindingID   = "project-bootstrap-owner"
	bootstrapOwnerBindingName = "Project bootstrap owner"
)

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
	name := bootstrapOwnerBindingName
	key := uuid.NewSHA1(uuid.NameSpaceURL, []byte("leapview/project-policy/"+targetID+"/"+projectID+"/"+environment+"/"+principalID)).String()
	created, createErr := client.CreateProjectRoleBinding(ctx, accessgen.GenCreateProjectRoleBindingClientRequest{
		Project: projectID,
		Headers: accessgen.GenCreateProjectRoleBindingClientHeaders{IdempotencyKey: key},
		Body: accessgen.GenSchemaRoleBindingCreateRequest{
			Id: bootstrapOwnerBindingID, Name: &name, SubjectType: string(access.SubjectKindPrincipal), SubjectId: principalID,
			Role: string(access.ProjectRoleAdmin), ExpectedRevision: 0,
		},
	})
	if createErr == nil {
		if err := validateBootstrapOwnerBinding(created.Body, targetID, projectID, environment, principalID); err != nil {
			return 0, "", err
		}
		if created.Body.PolicyRevision != 1 {
			return 0, "", fmt.Errorf("created bootstrap policy revision is %d, want 1", created.Body.PolicyRevision)
		}
		return created.Body.PolicyRevision, created.Body.PolicyDigest, nil
	}

	// A target claimed before this bootstrap was introduced can already carry
	// a migrated policy. Never overwrite or reset it: accept only a complete,
	// canonical current policy that already grants the claiming principal the
	// administrator role. Otherwise retain the original create failure.
	limit := int32(200)
	listed, listErr := client.ListProjectRoleBindings(ctx, accessgen.GenListProjectRoleBindingsClientRequest{
		Project: projectID, Params: accessgen.GenListProjectRoleBindingsClientParams{Limit: &limit},
	})
	if listErr != nil {
		return 0, "", fmt.Errorf("create initial owner binding: %w (existing policy verification failed: %v)", createErr, listErr)
	}
	if err := validateBootstrapExistingPolicy(listed.Body, targetID, projectID, environment, principalID); err != nil {
		return 0, "", fmt.Errorf("create initial owner binding: %w (existing policy is incompatible: %v)", createErr, err)
	}
	return listed.Body.PolicyRevision, listed.Body.PolicyDigest, nil
}

func validateBootstrapOwnerBinding(binding accessgen.GenSchemaRoleBindingResponse, targetID, projectID, environment, principalID string) error {
	if binding.Id != bootstrapOwnerBindingID || binding.Name != bootstrapOwnerBindingName ||
		binding.SubjectType != string(access.SubjectKindPrincipal) || binding.SubjectId != principalID ||
		binding.Role != string(access.ProjectRoleAdmin) {
		return errors.New("created bootstrap owner binding identity is incompatible")
	}
	if binding.PolicyRevision <= 0 {
		return errors.New("created bootstrap owner binding has no policy revision")
	}
	if err := platformdigest.ValidateSHA256Identity(binding.PolicyDigest); err != nil {
		return fmt.Errorf("created bootstrap owner binding has invalid policy digest: %w", err)
	}
	capabilities := access.ProjectRoleCapabilities(access.ProjectRoleAdmin)
	if err := validateBootstrapCapabilities(binding.Capabilities, capabilities); err != nil {
		return err
	}
	expected, err := access.AuthorizationPolicyDigest(
		access.AuthorizationPolicyScope{TargetID: targetID, ProjectID: projectID, Environment: environment},
		[]access.RoleBinding{{ID: binding.Id, Name: binding.Name, Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principalID}, Role: access.ProjectRoleAdmin, Capabilities: capabilities}},
	)
	if err != nil {
		return fmt.Errorf("canonicalize created bootstrap owner binding: %w", err)
	}
	if binding.PolicyDigest != expected {
		return errors.New("created bootstrap owner policy digest does not match its canonical binding")
	}
	return nil
}

func validateBootstrapExistingPolicy(policy accessgen.GenSchemaRoleBindingListResponse, targetID, projectID, environment, principalID string) error {
	if policy.TargetId != targetID || policy.ProjectId != projectID || policy.Environment != environment || policy.PolicyRevision <= 0 {
		return errors.New("authorization policy scope or revision is incompatible")
	}
	if err := platformdigest.ValidateSHA256Identity(policy.PolicyDigest); err != nil {
		return fmt.Errorf("authorization policy digest is invalid: %w", err)
	}
	if policy.Page.NextCursor != nil && *policy.Page.NextCursor != "" {
		return errors.New("authorization policy verification is paginated")
	}
	bindings := make([]access.RoleBinding, 0, len(policy.Items))
	ownerFound := false
	for _, item := range policy.Items {
		subject, err := access.NewSubjectRef(access.SubjectKind(item.SubjectType), item.SubjectId)
		if err != nil {
			return err
		}
		role, err := access.ParseProjectRole(item.Role)
		if err != nil {
			return err
		}
		capabilities := make([]access.Capability, len(item.Capabilities))
		for index, capability := range item.Capabilities {
			capabilities[index] = access.Capability(capability)
		}
		binding := access.RoleBinding{ID: item.Id, Name: item.Name, Subject: subject, Role: role, Capabilities: capabilities}
		if err := access.ValidateAuthorizationRoleBinding(binding); err != nil {
			return err
		}
		if item.PolicyRevision != policy.PolicyRevision || item.PolicyDigest != policy.PolicyDigest {
			return fmt.Errorf("role binding %q is not bound to the policy head", item.Id)
		}
		if subject.Kind == access.SubjectKindPrincipal && subject.ID == principalID && role == access.ProjectRoleAdmin {
			ownerFound = true
		}
		bindings = append(bindings, binding)
	}
	if !ownerFound {
		return errors.New("claiming principal has no administrator role binding")
	}
	digest, err := access.AuthorizationPolicyDigest(access.AuthorizationPolicyScope{TargetID: targetID, ProjectID: projectID, Environment: environment}, bindings)
	if err != nil {
		return err
	}
	if digest != policy.PolicyDigest {
		return errors.New("authorization policy digest does not match its canonical bindings")
	}
	return nil
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
