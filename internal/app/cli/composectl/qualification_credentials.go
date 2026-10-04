package composectl

import (
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
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
	RecoveryControlToken       string `json:"recoveryControlToken,omitempty"`
	AuditToken                 string `json:"auditToken,omitempty"`
	AuthorPrincipalID          string `json:"authorPrincipalId,omitempty"`
	ReviewerPrincipalID        string `json:"reviewerPrincipalId,omitempty"`
	QualificationPassword      string `json:"qualificationPassword"`
}

type qualificationDeliveryEvidenceToken string

type qualificationConnectionEvidenceToken string

type qualificationInstalledTokens struct {
	Workload         string
	DeliveryEvidence qualificationDeliveryEvidenceToken
	Connection       qualificationConnectionEvidenceToken
	RecoveryControl  string
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
	recoveryControl, err := credentials.recoveryControlToken()
	if err != nil {
		return qualificationInstalledTokens{}, err
	}
	return qualificationInstalledTokens{
		Workload: workload, DeliveryEvidence: deliveryEvidence,
		Connection: connection, RecoveryControl: recoveryControl,
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
