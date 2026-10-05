package composectl

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type qualificationCredentials struct {
	Email                      string `json:"email"`
	TemporaryPassword          string `json:"temporaryPassword"`
	ProjectClaimToken          string `json:"projectClaimToken"`
	ProjectClaimTokenExpiresAt string `json:"projectClaimTokenExpiresAt"`
	ClaimCredentialID          string `json:"claimCredentialId,omitempty"`
	PublisherToken             string `json:"publisherToken"`
	PublisherTokenExpires      string `json:"publisherTokenExpiresAt"`
	WorkloadToken              string `json:"workloadToken,omitempty"`
	DeliveryEvidenceToken      string `json:"deliveryEvidenceToken,omitempty"`
	ConnectionEvidenceToken    string `json:"connectionEvidenceToken,omitempty"`
	RecoveryUploadToken        string `json:"recoveryUploadToken,omitempty"`
	RecoveryControlToken       string `json:"recoveryControlToken,omitempty"`
	AuditToken                 string `json:"auditToken,omitempty"`
	AuthorPrincipalID          string `json:"authorPrincipalId,omitempty"`
	ReviewerPrincipalID        string `json:"reviewerPrincipalId,omitempty"`
	QualificationPassword      string `json:"qualificationPassword"`
}

type qualificationDeliveryEvidenceToken string

type qualificationConnectionEvidenceToken string

type qualificationRecoveryUploadToken string

type qualificationInstalledTokens struct {
	Workload         string
	DeliveryEvidence qualificationDeliveryEvidenceToken
	Connection       qualificationConnectionEvidenceToken
	RecoveryUpload   qualificationRecoveryUploadToken
	RecoveryControl  string
}

func (c *Controller) createQualificationAPIToken(
	ctx context.Context,
	worker *qualificationJSONWorker,
	options qualificationAuthoringOptions,
	name string,
	actions []access.Action,
	exact ...access.PermissionPair,
) (string, error) {
	projectID, err := projectgraph.NewResourceID(options.ProjectID)
	if err != nil {
		return "", fmt.Errorf("qualification project identity: %w", err)
	}
	permissions, err := access.ProjectPermissionPairsForActions(projectID, actions)
	if err != nil {
		return "", fmt.Errorf("qualification %s permission scope: %w", name, err)
	}
	permissions = append(permissions, exact...)
	return c.createQualificationAPITokenWithPermissions(ctx, worker, name, permissions)
}

func (c *Controller) createQualificationAPITokenWithPermissions(
	ctx context.Context,
	worker *qualificationJSONWorker,
	name string,
	permissions []access.PermissionPair,
) (string, error) {
	if c == nil || worker == nil || ctx == nil || strings.TrimSpace(name) == "" || len(permissions) == 0 {
		return "", fmt.Errorf("qualification API token inputs are required")
	}
	if err := access.ValidatePermissionPairs(permissions); err != nil {
		return "", fmt.Errorf("qualification %s permission scope: %w", name, err)
	}
	var response struct {
		Token string `json:"token"`
	}
	if err := worker.CallContext(ctx, "createAdministratorAPIToken", map[string]any{
		"name": name, "permissions": permissions,
		"expiresAt": c.now().UTC().Add(2 * time.Hour).Format(time.RFC3339),
	}, &response, nil); err != nil {
		return "", err
	}
	if response.Token == "" {
		return "", fmt.Errorf("browser worker returned an empty %s token", name)
	}
	return response.Token, nil
}

func (credentials qualificationCredentials) installedTokens() (qualificationInstalledTokens, error) {
	workload, err := credentials.workloadToken()
	if err != nil {
		return qualificationInstalledTokens{}, err
	}
	deliveryEvidence, err := credentials.deliveryEvidenceToken()
	if err != nil {
		return qualificationInstalledTokens{}, err
	}
	connection, err := credentials.connectionEvidenceToken()
	if err != nil {
		return qualificationInstalledTokens{}, err
	}
	recoveryUpload, err := credentials.recoveryUploadToken()
	if err != nil {
		return qualificationInstalledTokens{}, err
	}
	recoveryControl, err := credentials.recoveryControlToken()
	if err != nil {
		return qualificationInstalledTokens{}, err
	}
	return qualificationInstalledTokens{
		Workload: workload, DeliveryEvidence: deliveryEvidence,
		Connection: connection, RecoveryUpload: recoveryUpload,
		RecoveryControl: recoveryControl,
	}, nil
}

func (credentials qualificationCredentials) workloadToken() (string, error) {
	token := strings.TrimSpace(credentials.WorkloadToken)
	if token == "" {
		return "", fmt.Errorf("dedicated qualification workload token is required")
	}
	return token, nil
}

func (credentials qualificationCredentials) deliveryEvidenceToken() (qualificationDeliveryEvidenceToken, error) {
	token := strings.TrimSpace(credentials.DeliveryEvidenceToken)
	if token == "" {
		return "", fmt.Errorf("dedicated qualification delivery-evidence token is required")
	}
	return qualificationDeliveryEvidenceToken(token), nil
}

func (credentials qualificationCredentials) connectionEvidenceToken() (qualificationConnectionEvidenceToken, error) {
	token := strings.TrimSpace(credentials.ConnectionEvidenceToken)
	if token == "" {
		return "", fmt.Errorf("dedicated qualification connection-evidence token is required")
	}
	return qualificationConnectionEvidenceToken(token), nil
}

func (credentials qualificationCredentials) recoveryUploadToken() (qualificationRecoveryUploadToken, error) {
	token := strings.TrimSpace(credentials.RecoveryUploadToken)
	if token == "" {
		return "", fmt.Errorf("dedicated qualification recovery-upload token is required")
	}
	return qualificationRecoveryUploadToken(token), nil
}

func (credentials qualificationCredentials) recoveryControlToken() (string, error) {
	token := strings.TrimSpace(credentials.RecoveryControlToken)
	if token == "" {
		return "", fmt.Errorf("dedicated qualification recovery-control token is required")
	}
	return token, nil
}

func qualificationDeliveryEvidenceActions() []access.Action {
	return []access.Action{access.ActionDeliveryRead}
}

func qualificationConnectionEvidenceActions() []access.Action {
	return []access.Action{access.ActionConnectionRead}
}
