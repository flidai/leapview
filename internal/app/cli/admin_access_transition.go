package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	admincli "github.com/flidai/leapview/internal/admin/cli"
	"github.com/flidai/leapview/internal/app"
	"github.com/flidai/leapview/internal/app/adminpostgres"
	"github.com/flidai/leapview/internal/app/cli/hostinstall"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/platform/buildinfo"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
	"github.com/spf13/cobra"
)

func accessTransitionInventoryCommand(ctx context.Context) *cobra.Command {
	var projectID string
	command := &cobra.Command{
		Use:   "transition-access-inventory",
		Short: "Read the exact legacy policy and serving identities for an admitted transition",
		Args:  cobra.NoArgs,
		Annotations: map[string]string{
			"leapview.dev/effect": "read", "leapview.dev/confirmation": "never",
		},
		RunE: func(command *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			cfg.Production = true
			operations := adminpostgres.New(adminpostgres.Dependencies{LoadConfig: func() (config.Config, error) { return cfg, nil }})
			return operations.AccessTransitionInventory(ctx, projectID, command.OutOrStdout())
		},
	}
	command.Flags().StringVar(&projectID, "project", "", "exact claimed ProjectID")
	_ = command.MarkFlagRequired("project")
	return command
}

func accessTransitionCommand(ctx context.Context) *cobra.Command {
	var requestPath, journalPath, recoveryDigest, mode string
	var publisherCredentialPath, reviewerCredentialPath string
	command := &cobra.Command{
		Use:   "transition-access",
		Short: "Apply and publish one host-admitted legacy-to-typed access transition",
		Args:  cobra.NoArgs,
		Annotations: map[string]string{
			"leapview.dev/effect": "write", "leapview.dev/confirmation": "never",
		},
		RunE: func(command *cobra.Command, _ []string) error {
			if strings.TrimSpace(requestPath) == "" || strings.TrimSpace(journalPath) == "" || strings.TrimSpace(recoveryDigest) == "" || mode == "" ||
				strings.TrimSpace(publisherCredentialPath) == "" || strings.TrimSpace(reviewerCredentialPath) == "" {
				return errors.New("request, journal, recovery digest, mode and both private credential files are required")
			}
			request, err := hostinstall.ReadNativeRequest(requestPath)
			if err != nil {
				return fmt.Errorf("read host upgrade request: %w", err)
			}
			intent, err := hostinstall.VerifyAccessTransitionFence(request, journalPath, recoveryDigest, mode, buildinfo.Current())
			if err != nil {
				return fmt.Errorf("verify host access-transition fence: %w", err)
			}
			if request.AccessTransition == nil {
				return errors.New("host-verified access intent differs from the native request")
			}
			plan, err := intent.Plan()
			if err != nil {
				return err
			}
			requestPlan, err := request.AccessTransition.Plan()
			if err != nil || requestPlan.IntentDigest != plan.IntentDigest {
				return errors.New("host-verified access intent differs from the native request")
			}
			identity, err := request.Identity()
			if err != nil {
				return err
			}
			operationID := "access-transition:" + strings.TrimPrefix(identity.ArtifactAdmissionDigest, "sha256:")
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			cfg.Production = true
			cfg.Environment = intent.Environment
			if err := cfg.Validate(config.ProfileServe); err != nil {
				return err
			}
			instanceLock, err := instancelock.Acquire(cfg.HomeDir)
			if err != nil {
				return fmt.Errorf("acquire LeapView instance lock: %w", err)
			}
			defer instanceLock.Release()

			stageRequest := admincli.StageAccessTransitionRequest{
				Intent: intent, MaintenanceOperationID: identity.ArtifactAdmissionDigest,
				MaintenanceOperationDigest: identity.ArtifactAdmissionDigest, OperationID: operationID, Apply: true,
			}
			stageOperations := adminpostgres.New(adminpostgres.Dependencies{
				LoadConfig: func() (config.Config, error) { return cfg, nil },
				AuthorizeAccessTransition: func(_ context.Context, authorization adminpostgres.AccessTransitionAuthorization) error {
					if authorization.TargetID != intent.TargetID || authorization.ProjectID != intent.ProjectID || authorization.Environment != intent.Environment ||
						authorization.MaintenanceOperationID != identity.ArtifactAdmissionDigest || authorization.MaintenanceOperationDigest != identity.ArtifactAdmissionDigest ||
						authorization.OperationID != operationID || authorization.ExpectedPolicyRevision != intent.ExpectedPolicyRevision || authorization.ExpectedPolicyDigest != intent.ExpectedPolicyDigest ||
						authorization.ExpectedServingGeneration != intent.ExpectedServingGeneration || authorization.ExpectedServingPolicyDigest != intent.ExpectedServingPolicyDigest ||
						authorization.PublisherPrincipalID != intent.PublisherPrincipalID || authorization.ReviewerPrincipalID != intent.ReviewerPrincipalID || authorization.IntentDigest != plan.IntentDigest {
						return errors.New("access-transition database mutation differs from the host-admitted intent")
					}
					return nil
				},
			})
			var staged bytes.Buffer
			if err := stageOperations.StageAccessTransition(ctx, stageRequest, &staged); err != nil {
				return err
			}
			var stageResult struct {
				TargetID       string `json:"targetId"`
				ProjectID      string `json:"projectId"`
				Environment    string `json:"environment"`
				IntentDigest   string `json:"intentDigest"`
				PolicyRevision int64  `json:"policyRevision"`
				PolicyDigest   string `json:"policyDigest"`
				Applied        bool   `json:"applied"`
			}
			if err := json.Unmarshal(staged.Bytes(), &stageResult); err != nil || !stageResult.Applied || stageResult.TargetID != intent.TargetID || stageResult.ProjectID != intent.ProjectID || stageResult.Environment != intent.Environment || stageResult.IntentDigest != plan.IntentDigest || stageResult.PolicyRevision <= intent.ExpectedPolicyRevision || !validCLITransitionDigest(stageResult.PolicyDigest) {
				return errors.New("staged access-policy result does not bind the admitted transition")
			}

			publisherSecret, err := readTransitionCredential(publisherCredentialPath)
			if err != nil {
				return fmt.Errorf("read transition publisher credential: %w", err)
			}
			reviewerSecret, err := readTransitionCredential(reviewerCredentialPath)
			if err != nil {
				return fmt.Errorf("read transition reviewer credential: %w", err)
			}
			runner, err := app.BuildAccessTransitionRunner(ctx, cfg)
			if err != nil {
				return fmt.Errorf("build candidate application for admitted access transition: %w", err)
			}
			result, executeErr := runner.Execute(ctx, app.AccessTransitionExecutionRequest{
				Intent: intent, OperationID: operationID, MaintenanceOperationDigest: identity.ArtifactAdmissionDigest, RecoveryDigest: recoveryDigest,
				StagedPolicyRevision: stageResult.PolicyRevision, StagedPolicyDigest: stageResult.PolicyDigest,
				PublisherCredential: publisherSecret, ReviewerCredential: reviewerSecret,
			})
			shutdownErr := runner.Shutdown(context.Background())
			if executeErr != nil || shutdownErr != nil {
				return errors.Join(executeErr, shutdownErr)
			}
			return json.NewEncoder(command.OutOrStdout()).Encode(result)
		},
	}
	command.Flags().StringVar(&requestPath, "request", "", "private host NativeRequest JSON")
	command.Flags().StringVar(&journalPath, "journal", "", "private host-maintenance journal")
	command.Flags().StringVar(&recoveryDigest, "recovery-digest", "", "exact host-verified recovery point digest")
	command.Flags().StringVar(&mode, "mode", "", "admitted execution mode: live, rehearsal or detached")
	command.Flags().StringVar(&publisherCredentialPath, "publisher-credential-file", "", "private publisher workload secret file")
	command.Flags().StringVar(&reviewerCredentialPath, "reviewer-credential-file", "", "private reviewer workload secret file")
	_ = command.MarkFlagRequired("request")
	_ = command.MarkFlagRequired("journal")
	_ = command.MarkFlagRequired("recovery-digest")
	_ = command.MarkFlagRequired("mode")
	_ = command.MarkFlagRequired("publisher-credential-file")
	_ = command.MarkFlagRequired("reviewer-credential-file")
	return command
}

func readTransitionCredential(path string) (string, error) {
	credential, err := securefs.ReadPrivateFile(path)
	if err != nil {
		return "", err
	}
	if len(credential) == 0 || len(credential) > 65536 || strings.TrimSpace(string(credential)) == "" || strings.ContainsAny(string(credential), "\x00\r\n") {
		return "", errors.New("credential secret is empty or exceeds its private-file bounds")
	}
	return string(credential), nil
}

func validCLITransitionDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, char := range value[len("sha256:"):] {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}
