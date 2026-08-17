package deployment

import (
	"context"
	"fmt"
	"strings"
	"time"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/platform/digest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

var (
	ErrBootstrapPolicyInvalid  = apigenfailure.New("invalid", "bootstrap activation policy is invalid")
	ErrBootstrapPolicyConflict = apigenfailure.New("conflict", "bootstrap activation policy conflicts with the bound deployment")
	ErrBootstrapPolicyNotFound = apigenfailure.New("not_found", "bootstrap activation policy not found")
)

// BootstrapActivationPolicy is the durable one-shot authorization binding for
// the first protected activation. It intentionally stores only redacted
// credential evidence; the secret is never persisted.
type BootstrapActivationPolicy struct {
	ProjectID           projectgraph.ResourceID
	Environment         servingstate.Environment
	DeploymentID        string
	RequestDigest       string
	ActorID             string
	CredentialID        string
	CredentialExpiresAt time.Time
	ArmedAt             time.Time
}

func (policy BootstrapActivationPolicy) Validate() error {
	if err := policy.ProjectID.Validate(); err != nil || policy.ProjectID.String() != strings.TrimSpace(policy.ProjectID.String()) {
		return fmt.Errorf("%w: project id: %v", ErrBootstrapPolicyInvalid, err)
	}
	if err := servingstate.ValidateEnvironment(policy.Environment); err != nil || string(policy.Environment) != strings.TrimSpace(string(policy.Environment)) {
		return fmt.Errorf("%w: environment", ErrBootstrapPolicyInvalid)
	}
	if policy.DeploymentID == "" || policy.DeploymentID != strings.TrimSpace(policy.DeploymentID) || policy.RequestDigest == "" || policy.ActorID == "" || policy.ActorID != strings.TrimSpace(policy.ActorID) || policy.CredentialID == "" || policy.CredentialID != strings.TrimSpace(policy.CredentialID) || policy.CredentialExpiresAt.IsZero() || policy.ArmedAt.IsZero() {
		return fmt.Errorf("%w: binding evidence is incomplete", ErrBootstrapPolicyInvalid)
	}
	if digest.ValidateSHA256Identity(policy.RequestDigest) != nil {
		return fmt.Errorf("%w: request digest", ErrBootstrapPolicyInvalid)
	}
	if !policy.CredentialExpiresAt.After(policy.ArmedAt) {
		return fmt.Errorf("%w: credential expiry must be after arming", ErrBootstrapPolicyInvalid)
	}
	return nil
}

type BootstrapActivationPolicyRepository interface {
	ArmBootstrapActivation(context.Context, BootstrapActivationPolicy) (BootstrapActivationPolicy, error)
	BootstrapActivationPolicy(context.Context, string) (BootstrapActivationPolicy, error)
}
