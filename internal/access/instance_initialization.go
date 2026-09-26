package access

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

var ErrInstanceAlreadyInitialized = errors.New("LeapView instance is already initialized")

const InstanceInitializedSetting = "instance.initialized"

const APITokenNameInitialProjectClaimPublisherPrefix = "initial-project-claim-publisher:"

type InstanceInitializationInput struct {
	InstanceID           string
	Email                string
	Environment          string
	Now                  time.Time
	EvaluationDataIngest bool
}

type InitialInstanceCredentials struct {
	Email                      string
	TemporaryPassword          string
	ProjectClaimToken          string
	ProjectClaimTokenExpiresAt time.Time
}

// ProjectClaimPublisherExchangeInput binds the short-lived instance claim
// credential to the singleton Project claim proven by the deployment layer.
// ProjectID/PrincipalID are the request actor; ClaimedProjectID/ClaimedBy come
// from the durable claim record and must match them before exchange.
type ProjectClaimPublisherExchangeInput struct {
	InstanceID        string
	ProjectID         string
	PrincipalID       string
	ClaimCredentialID string
	ClaimedProjectID  string
	ClaimedBy         string
}

// ProjectClaimPublisherCredentials contains the new publisher secret only
// until the caller has durably handed it off and acknowledged the exchange.
type ProjectClaimPublisherCredentials struct {
	ClaimCredentialID             string
	PublisherCredentialID         string
	PublisherToken                string
	PublisherTokenExpiresAt       time.Time
	RevokedPublisherCredentialIDs []string
}

type ProjectClaimPublisherAcknowledgeInput struct {
	InstanceID            string
	ProjectID             string
	PrincipalID           string
	ClaimCredentialID     string
	PublisherCredentialID string
	ClaimedProjectID      string
	ClaimedBy             string
}

// ProjectClaimPublisherRepository exposes only the credential side of the
// first-claim handoff. Implementations are called from a caller-owned audited
// Access transaction so credential mutations and redacted audit metadata
// commit together.
type ProjectClaimPublisherRepository interface {
	ExchangeProjectClaimPublisher(context.Context, ProjectClaimPublisherExchangeInput) (ProjectClaimPublisherCredentials, error)
	AcknowledgeProjectClaimPublisher(context.Context, ProjectClaimPublisherAcknowledgeInput) error
}

// InstanceInitializer owns the atomic, audited Access mutation used by
// offline Admin initialization.
type InstanceInitializer interface {
	InitializeInstance(context.Context, InstanceInitializationInput, func(InitialInstanceCredentials) error) (InitialInstanceCredentials, error)
}

func InitialProjectClaimPermissions(instanceID string) ([]PermissionPair, error) {
	pair, err := NewInstancePermissionPair(ActionInstanceProjectClaim, instanceID)
	if err != nil {
		return nil, err
	}
	return []PermissionPair{pair}, nil
}

// InitialProjectPublisherPermissions captures exactly the project_admin,
// editor, and release_operator role expansions at claim time. It intentionally
// does not add connection.manage, which is not part of any bootstrap preset.
func InitialProjectPublisherPermissions(projectID projectgraph.ResourceID) ([]PermissionPair, error) {
	if err := projectID.Validate(); err != nil {
		return nil, fmt.Errorf("initial publisher project: %w", err)
	}
	seen := make(map[string]PermissionPair)
	for _, role := range []PermissionRole{PermissionRoleProjectAdmin, PermissionRoleEditor, PermissionRoleReleaseOperator} {
		pairs, err := ExpandPermissionRole(role, projectID)
		if err != nil {
			return nil, fmt.Errorf("initial publisher role %q: %w", role, err)
		}
		for _, pair := range pairs {
			seen[pair.Key()] = pair
		}
	}
	result := make([]PermissionPair, 0, len(seen))
	for _, pair := range seen {
		if pair.Action == ActionConnectionManage {
			return nil, errors.New("initial publisher permissions must not include connection.manage")
		}
		result = append(result, pair)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Key() < result[right].Key() })
	if err := ValidatePermissionPairs(result); err != nil {
		return nil, fmt.Errorf("initial publisher permission set: %w", err)
	}
	return result, nil
}

func InitialProjectClaimPublisherTokenName(claimCredentialID string) string {
	return APITokenNameInitialProjectClaimPublisherPrefix + strings.TrimSpace(claimCredentialID)
}

// InitialPublisherOrigin is private credential evidence, never caller-supplied
// token metadata. Eligibility is deliberately not cached with this provenance.
type InitialPublisherOrigin struct {
	ClaimCredentialID string
	PrincipalID       string
	InstanceID        string
	ProjectID         string
}

// InitialPublisherPasswordSetupReader checks the live initial setup record.
// Password changes/resets close it permanently; ACK does not close it.
type InitialPublisherPasswordSetupReader interface {
	InitialPublisherPasswordSetupOpen(context.Context, string, string) (bool, error)
}
