package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/recoveryset"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

// RecoveryPrepareRequest is the production recovery-frontier input accepted
// by the CLI adapter. The supplied set carries immutable creation, audit, and
// fencing metadata that the PostgreSQL authority validates and persists.
type RecoveryPrepareRequest struct {
	Set       recoveryset.RecoverySet
	RootID    string
	ExpiresAt time.Time
}

// RecoveryPrepareResult is the redacted identity returned after preparing a
// recovery frontier and its retention root.
type RecoveryPrepareResult struct {
	Set       recoveryset.RecoverySet `json:"set"`
	RootID    string                  `json:"rootId"`
	ExpiresAt time.Time               `json:"expiresAt,omitempty"`
}

// RecoveryValidateRequest carries an operator-produced, provider-validated
// evidence envelope. The backend owns strict parsing and canonicalization;
// the CLI only transports the bounded file bytes without manufacturing
// evidence.
type RecoveryValidateRequest struct {
	SetID     string
	AttemptID string
	Validator string
	Evidence  []byte
}

// RecoveryValidateResult is the durable validation identity returned by the
// production authority. Raw evidence is not printed by the CLI.
type RecoveryValidateResult struct {
	Attempt recoveryset.ValidationAttempt `json:"attempt"`
	Result  *recoveryset.ValidationResult `json:"result,omitempty"`
}

// RecoveryPublishRequest identifies the exact validation-gated publication.
type RecoveryPublishRequest struct {
	SetID               string
	Publisher           string
	FenceEpoch          int64
	ValidationAttemptID string
}

// RecoveryPublishResult is the exact frontier identity after publication.
type RecoveryPublishResult struct {
	Set recoveryset.RecoverySet `json:"set"`
}

// RecoveryOperations are the production-only recovery use cases exposed by
// the Admin CLI. There is deliberately no offline or SQLite implementation.
type RecoveryOperations interface {
	PrepareRecovery(context.Context, RecoveryPrepareRequest) (RecoveryPrepareResult, error)
	ValidateRecovery(context.Context, RecoveryValidateRequest) (RecoveryValidateResult, error)
	PublishRecovery(context.Context, RecoveryPublishRequest) (RecoveryPublishResult, error)
}

func recoveryCommand(ctx context.Context, operations RecoveryOperations) *cobra.Command {
	prepare := recoveryPrepareCommand(ctx, operations)
	validate := recoveryValidateCommand(ctx, operations)
	publish := recoveryPublishCommand(ctx, operations)
	recovery := adminGroupCommand("recovery", "Prepare, validate, and publish immutable recovery frontiers")
	recovery.AddCommand(prepare, validate, publish)
	return recovery
}

func recoveryPrepareCommand(ctx context.Context, operations RecoveryOperations) *cobra.Command {
	var setPath, rootID, expiresAt string
	command := &cobra.Command{
		Use:   "prepare",
		Short: "Prepare one immutable recovery frontier",
		Args:  cobra.NoArgs,
		Annotations: map[string]string{
			"leapview.dev/effect": "write", "leapview.dev/confirmation": "required",
		},
		RunE: func(command *cobra.Command, _ []string) error {
			if operations == nil {
				return fmt.Errorf("Admin CLI recovery operations are required")
			}
			if strings.TrimSpace(setPath) == "" {
				return fmt.Errorf("--set is required")
			}
			set, err := readRecoverySet(setPath)
			if err != nil {
				return err
			}
			if set.Status != recoveryset.StatusPrepared || set.PublishedValidationAttemptID != "" {
				return fmt.Errorf("recovery prepare requires a prepared set without a published validation attempt")
			}
			rootID, err := parseOptionalUUID(rootID, "--retain-root-id")
			if err != nil {
				return err
			}
			if strings.TrimSpace(expiresAt) == "" {
				return fmt.Errorf("--expires-at is required")
			}
			expiration, err := parseOptionalRFC3339(expiresAt, "--expires-at")
			if err != nil {
				return err
			}
			result, err := operations.PrepareRecovery(ctx, RecoveryPrepareRequest{Set: set, RootID: rootID, ExpiresAt: expiration})
			if err != nil {
				return err
			}
			return writeRecoveryPrepareResult(command.OutOrStdout(), result)
		},
	}
	command.Flags().StringVar(&setPath, "set", "", "path to the prepared recovery-set JSON")
	command.Flags().StringVar(&rootID, "retain-root-id", "", "optional retention-root UUID; defaults to the recovery-set ID")
	command.Flags().StringVar(&expiresAt, "expires-at", "", "retention-root expiry timestamp (RFC3339)")
	_ = command.MarkFlagRequired("set")
	_ = command.MarkFlagRequired("expires-at")
	return command
}

func recoveryValidateCommand(ctx context.Context, operations RecoveryOperations) *cobra.Command {
	var setID, attemptID, validator, evidencePath string
	command := &cobra.Command{
		Use:   "validate",
		Short: "Record provider-produced evidence for one recovery attempt",
		Args:  cobra.NoArgs,
		Annotations: map[string]string{
			"leapview.dev/effect": "write", "leapview.dev/confirmation": "required",
		},
		RunE: func(command *cobra.Command, _ []string) error {
			if operations == nil {
				return fmt.Errorf("Admin CLI recovery operations are required")
			}
			setID, err := parseRequiredUUID(setID, "--set-id")
			if err != nil {
				return err
			}
			attemptID, err := parseRequiredUUID(attemptID, "--attempt-id")
			if err != nil {
				return err
			}
			if validator == "" || validator != strings.TrimSpace(validator) || len(validator) > 255 {
				return fmt.Errorf("--validator is required and must be a canonical identity of at most 255 bytes")
			}
			if strings.TrimSpace(evidencePath) == "" {
				return fmt.Errorf("--evidence is required")
			}
			evidence, err := readBoundedRecoveryFile(evidencePath, 65536, "recovery validation evidence")
			if err != nil {
				return err
			}
			result, err := operations.ValidateRecovery(ctx, RecoveryValidateRequest{SetID: setID, AttemptID: attemptID, Validator: validator, Evidence: evidence})
			if err != nil {
				return err
			}
			return writeRecoveryValidateResult(command.OutOrStdout(), result)
		},
	}
	command.Flags().StringVar(&setID, "set-id", "", "recovery-set UUID")
	command.Flags().StringVar(&attemptID, "attempt-id", "", "validation-attempt UUID")
	command.Flags().StringVar(&validator, "validator", "", "operator/provider validator identity")
	command.Flags().StringVar(&evidencePath, "evidence", "", "path to provider-produced typed validation evidence JSON")
	_ = command.MarkFlagRequired("set-id")
	_ = command.MarkFlagRequired("attempt-id")
	_ = command.MarkFlagRequired("validator")
	_ = command.MarkFlagRequired("evidence")
	return command
}

func recoveryPublishCommand(ctx context.Context, operations RecoveryOperations) *cobra.Command {
	var setID, publisher, validationAttemptID string
	var fenceEpoch uint64
	command := &cobra.Command{
		Use:   "publish",
		Short: "Publish one recovery frontier after exact validation",
		Args:  cobra.NoArgs,
		Annotations: map[string]string{
			"leapview.dev/effect": "write", "leapview.dev/confirmation": "required",
		},
		RunE: func(command *cobra.Command, _ []string) error {
			if operations == nil {
				return fmt.Errorf("Admin CLI recovery operations are required")
			}
			setID, err := parseRequiredUUID(setID, "--set-id")
			if err != nil {
				return err
			}
			if publisher == "" || publisher != strings.TrimSpace(publisher) {
				return fmt.Errorf("--publisher is required and must be canonical")
			}
			publisher = strings.TrimSpace(publisher)
			validationAttemptID, err := parseRequiredUUID(validationAttemptID, "--validation-attempt-id")
			if err != nil {
				return err
			}
			if fenceEpoch == 0 || fenceEpoch > uint64(1<<63-1) {
				return fmt.Errorf("--fence-epoch must be a positive signed 64-bit integer")
			}
			result, err := operations.PublishRecovery(ctx, RecoveryPublishRequest{SetID: setID, Publisher: publisher, FenceEpoch: int64(fenceEpoch), ValidationAttemptID: validationAttemptID})
			if err != nil {
				return err
			}
			return writeRecoveryPublishResult(command.OutOrStdout(), result)
		},
	}
	command.Flags().StringVar(&setID, "set-id", "", "recovery-set UUID")
	command.Flags().StringVar(&publisher, "publisher", "", "publisher identity")
	command.Flags().Uint64Var(&fenceEpoch, "fence-epoch", 0, "publication fence epoch (positive unsigned integer)")
	command.Flags().StringVar(&validationAttemptID, "validation-attempt-id", "", "exact passed validation-attempt UUID")
	_ = command.MarkFlagRequired("set-id")
	_ = command.MarkFlagRequired("publisher")
	_ = command.MarkFlagRequired("fence-epoch")
	_ = command.MarkFlagRequired("validation-attempt-id")
	return command
}

func readRecoverySet(path string) (recoveryset.RecoverySet, error) {
	bytes, err := readBoundedRecoveryFile(path, 1<<20, "recovery set")
	if err != nil {
		return recoveryset.RecoverySet{}, err
	}
	var set recoveryset.RecoverySet
	if err := strictjson.DecodeWithOptions(bytes, &set, strictjson.Options{MaxBytes: 1 << 20, MaxDepth: 32, DuplicateKeys: strictjson.CaseFoldedKeys, AllowUnknownFields: false}); err != nil {
		return recoveryset.RecoverySet{}, fmt.Errorf("decode recovery set: %w", err)
	}
	normalized, err := set.Normalize()
	if err != nil {
		return recoveryset.RecoverySet{}, fmt.Errorf("validate recovery set: %w", err)
	}
	return normalized, nil
}

func readBoundedRecoveryFile(path string, maxBytes int64, label string) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%s path is required", label)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", label, err)
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", label, err)
	}
	if int64(len(contents)) > maxBytes {
		return nil, fmt.Errorf("%s exceeds the %d-byte limit", label, maxBytes)
	}
	return contents, nil
}

func parseRequiredUUID(value, flag string) (string, error) {
	if value == "" || strings.TrimSpace(value) != value {
		return "", fmt.Errorf("%s is required and must be a canonical UUID", flag)
	}
	u, err := uuid.Parse(value)
	if err != nil || u.String() != value {
		return "", fmt.Errorf("%s must be a canonical UUID", flag)
	}
	return value, nil
}

func parseOptionalUUID(value, flag string) (string, error) {
	if value == "" {
		return "", nil
	}
	return parseRequiredUUID(value, flag)
}

func parseOptionalRFC3339(value, flag string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	if strings.TrimSpace(value) != value {
		return time.Time{}, fmt.Errorf("%s must be an RFC3339 timestamp without surrounding whitespace", flag)
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be RFC3339: %w", flag, err)
	}
	return parsed.UTC(), nil
}

func writeRecoveryPrepareResult(out io.Writer, result RecoveryPrepareResult) error {
	if out == nil {
		return fmt.Errorf("recovery command output is required")
	}
	var expiresAt *time.Time
	if !result.ExpiresAt.IsZero() {
		expires := result.ExpiresAt
		expiresAt = &expires
	}
	return json.NewEncoder(out).Encode(struct {
		SetID          string     `json:"setId"`
		FrontierDigest string     `json:"frontierDigest"`
		RootID         string     `json:"rootId"`
		ExpiresAt      *time.Time `json:"expiresAt,omitempty"`
	}{SetID: result.Set.ID, FrontierDigest: result.Set.FrontierDigest, RootID: result.RootID, ExpiresAt: expiresAt})
}

func writeRecoveryValidateResult(out io.Writer, result RecoveryValidateResult) error {
	if out == nil {
		return fmt.Errorf("recovery command output is required")
	}
	var resultDigest string
	if result.Result != nil {
		resultDigest = result.Result.ResultDigest
	}
	return json.NewEncoder(out).Encode(struct {
		AttemptID    string `json:"attemptId"`
		SetID        string `json:"setId"`
		Status       string `json:"status"`
		ResultDigest string `json:"resultDigest,omitempty"`
	}{AttemptID: result.Attempt.AttemptID, SetID: result.Attempt.SetID, Status: string(result.Attempt.Status), ResultDigest: resultDigest})
}

func writeRecoveryPublishResult(out io.Writer, result RecoveryPublishResult) error {
	if out == nil {
		return fmt.Errorf("recovery command output is required")
	}
	return json.NewEncoder(out).Encode(struct {
		SetID          string `json:"setId"`
		Status         string `json:"status"`
		FrontierDigest string `json:"frontierDigest"`
	}{SetID: result.Set.ID, Status: string(result.Set.Status), FrontierDigest: result.Set.FrontierDigest})
}
