package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	"github.com/flidai/leapview/internal/platform/cliapi"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
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
			if format == "json" {
				return json.NewEncoder(command.OutOrStdout()).Encode(map[string]any{
					"schemaVersion": 1,
					"type":          "projectBootstrapped",
					"target":        credentials.Target,
					"projectUid":    response.Body.ProjectUid,
					"environment":   response.Body.Environment,
				})
			}
			fmt.Fprintf(command.OutOrStdout(), "Bootstrapped %s with ProjectUID %s (%s)\n", credentials.Target, response.Body.ProjectUid, response.Body.Environment)
			return nil
		},
	}
	command.Flags().StringVar(&root.token, "token", root.token, "instance-admin API token")
	command.Flags().StringVar(&externallyIssuedUID, "project-uid", "", "externally issued ProjectUID (first bootstrap only)")
	command.Flags().StringVar(&format, "format", format, "output format: text or json")
	return command
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
