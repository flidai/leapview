package composectl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	// explicitQualificationUID is the externally issued ProjectUID used by both
	// fresh qualification journeys. The CLI bootstrap command persists its issuer
	// authority in the container before presenting this identity to the target.
	explicitQualificationUID         = qualificationProjectID
	explicitQualificationEnvironment = "evaluation"
)

type qualificationProjectBootstrapResult struct {
	SchemaVersion int    `json:"schemaVersion"`
	Type          string `json:"type"`
	Target        string `json:"target"`
	ProjectUID    string `json:"projectUid"`
	Environment   string `json:"environment"`
}

// bootstrapQualificationProject establishes the issuer-owned ProjectUID on a
// fresh target before the first source sync. The token is injected through the
// existing container environment convention rather than a CLI token flag.
func bootstrapQualificationProject(
	ctx context.Context,
	container qualificationContainer,
	target string,
	token string,
) error {
	if container == nil {
		return errors.New("qualification application container is required")
	}
	target = strings.TrimRight(strings.TrimSpace(target), "/")
	if target == "" {
		return errors.New("qualification project bootstrap target is required")
	}
	if strings.TrimSpace(token) == "" {
		return errors.New("qualification project bootstrap token is required")
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
		return fmt.Errorf("bootstrap qualification ProjectUID: %w", err)
	}
	var result qualificationProjectBootstrapResult
	if err := json.Unmarshal(output, &result); err != nil {
		return fmt.Errorf("decode qualification ProjectUID bootstrap result: %w", err)
	}
	if result.SchemaVersion != 1 || result.Type != "projectBootstrapped" {
		return fmt.Errorf("qualification ProjectUID bootstrap result has unsupported schema or type")
	}
	if strings.TrimRight(strings.TrimSpace(result.Target), "/") != target {
		return fmt.Errorf("qualification ProjectUID bootstrap target does not match")
	}
	if result.ProjectUID != explicitQualificationUID {
		return fmt.Errorf("qualification ProjectUID bootstrap identity does not match")
	}
	if strings.TrimSpace(result.Environment) != explicitQualificationEnvironment {
		return fmt.Errorf("qualification ProjectUID bootstrap environment does not match")
	}
	return nil
}
