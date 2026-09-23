package composectl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// explicitQualificationUID is the externally issued ProjectUID used by both
	// fresh qualification journeys. The CLI bootstrap command persists its issuer
	// authority in the container before presenting this identity to the target.
	explicitQualificationUID         = qualificationProjectID
	explicitQualificationEnvironment = "evaluation"
)

type qualificationProjectBootstrapResult struct {
	SchemaVersion           int    `json:"schemaVersion"`
	Type                    string `json:"type"`
	Target                  string `json:"target"`
	ProjectUID              string `json:"projectUid"`
	Environment             string `json:"environment"`
	ClaimCredentialID       string `json:"claimCredentialId"`
	PublisherToken          string `json:"publisherToken"`
	PublisherTokenExpiresAt string `json:"publisherTokenExpiresAt"`
}

// bootstrapQualificationProject establishes the issuer-owned ProjectUID on a
// fresh target and returns the exchanged publisher secret for private storage.
func bootstrapQualificationProject(
	ctx context.Context,
	container qualificationContainer,
	target string,
	token string,
) (qualificationProjectBootstrapResult, error) {
	var result qualificationProjectBootstrapResult
	if container == nil {
		return result, errors.New("qualification application container is required")
	}
	target = strings.TrimRight(strings.TrimSpace(target), "/")
	if target == "" {
		return result, errors.New("qualification project bootstrap target is required")
	}
	if strings.TrimSpace(token) == "" {
		return result, errors.New("qualification project bootstrap token is required")
	}
	output, err := container.Exec(
		ctx,
		nil,
		"env",
		"LEAPVIEW_API_TOKEN="+token,
		"LEAPVIEW_TARGET="+target,
		"leapview", "bootstrap-project", target,
		"--project-uid", explicitQualificationUID,
		"--format", "json",
	)
	if err != nil {
		return result, fmt.Errorf("bootstrap qualification ProjectUID: %w", err)
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return qualificationProjectBootstrapResult{}, fmt.Errorf("decode qualification ProjectUID bootstrap result: %w", err)
	}
	if result.SchemaVersion != 1 || result.Type != "projectBootstrapped" {
		return qualificationProjectBootstrapResult{}, errors.New("qualification ProjectUID bootstrap result has unsupported schema or type")
	}
	if strings.TrimRight(strings.TrimSpace(result.Target), "/") != target {
		return qualificationProjectBootstrapResult{}, errors.New("qualification project bootstrap target does not match")
	}
	if result.ProjectUID != explicitQualificationUID {
		return qualificationProjectBootstrapResult{}, errors.New("qualification ProjectUID bootstrap identity does not match")
	}
	if strings.TrimSpace(result.Environment) != explicitQualificationEnvironment {
		return qualificationProjectBootstrapResult{}, errors.New("qualification ProjectUID bootstrap environment does not match")
	}
	if strings.TrimSpace(result.ClaimCredentialID) == "" || strings.TrimSpace(result.ClaimCredentialID) != result.ClaimCredentialID ||
		strings.TrimSpace(result.PublisherToken) == "" || strings.TrimSpace(result.PublisherTokenExpiresAt) == "" {
		return qualificationProjectBootstrapResult{}, errors.New("qualification publisher handoff is incomplete")
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, result.PublisherTokenExpiresAt)
	if err != nil || !expiresAt.After(time.Now().UTC()) {
		return qualificationProjectBootstrapResult{}, errors.New("qualification publisher handoff has invalid expiry")
	}
	return result, nil
}

// acknowledgeQualificationProjectClaim runs only after the caller has saved
// the new publisher in its private credential file. The claim remains valid
// if this acknowledgement fails, allowing an exchange retry to rotate safely.
func acknowledgeQualificationProjectClaim(
	ctx context.Context,
	container qualificationContainer,
	target, publisherToken, claimCredentialID string,
) error {
	if container == nil {
		return errors.New("qualification application container is required")
	}
	if strings.TrimSpace(publisherToken) == "" || strings.TrimSpace(claimCredentialID) == "" || strings.TrimSpace(claimCredentialID) != claimCredentialID {
		return errors.New("qualification project claim acknowledgement evidence is incomplete")
	}
	target = strings.TrimRight(strings.TrimSpace(target), "/")
	if target == "" {
		return errors.New("qualification project bootstrap target is required")
	}
	if _, err := container.Exec(ctx, nil,
		"env",
		"LEAPVIEW_API_TOKEN="+publisherToken,
		"LEAPVIEW_TARGET="+target,
		"leapview", "acknowledge-project-claim-publisher", target, explicitQualificationUID,
		"--claim-credential-id", claimCredentialID,
	); err != nil {
		return fmt.Errorf("acknowledge qualification Project claim publisher: %w", err)
	}
	return nil
}
