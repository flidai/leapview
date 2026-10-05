package module

import (
	"context"
	"encoding/hex"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/flidai/leapview/internal/analytics/connectors"
	"github.com/flidai/leapview/internal/credential"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

const connectionCredentialPurpose = "connection-authentication"

// TargetConnectionBinding is the minimal target-owned projection needed to
// bind a draft. It deliberately contains no endpoint values or credential
// references, only the digest of the non-secret endpoint configuration.
type TargetConnectionBinding struct {
	TargetID           string
	ProjectID          string
	Environment        string
	ConnectionID       string
	ConnectorKind      string
	AuthenticationMode string
	EndpointConfigHash string
}

// TargetConnectionBindingReader resolves the exact target/project/environment
// connection binding. Implementations must not fall back to another scope.
type TargetConnectionBindingReader interface {
	ReadTargetConnectionBinding(context.Context, string, string, string, string) (TargetConnectionBinding, error)
}

type connectionCredentialScopeResolver struct {
	instanceID     string
	environment    string
	ownerReader    CustomerOwnerReader
	currentProject func(context.Context) (projectgraph.ResourceID, error)
	bindings       TargetConnectionBindingReader
}

func (resolver connectionCredentialScopeResolver) ResolveCredentialScope(ctx context.Context, resource credential.Resource) (credential.Scope, error) {
	if ctx == nil || resource.Validate() != nil || resource.ScopeKind != "connection" ||
		resource.TargetID != resolver.instanceID || resource.Environment != resolver.environment ||
		resolver.ownerReader == nil || resolver.currentProject == nil || resolver.bindings == nil {
		return credential.Scope{}, credential.ErrNotFound
	}
	projectID, err := projectgraph.NewResourceID(resource.ProjectID)
	if err != nil {
		return credential.Scope{}, credential.ErrNotFound
	}
	activeProjectID, err := resolver.currentProject(ctx)
	if err != nil || activeProjectID != projectID {
		return credential.Scope{}, credential.ErrNotFound
	}
	ownerID, err := resolver.ownerReader.CustomerOwner(ctx)
	if err != nil || !canonicalCredentialValue(ownerID) {
		return credential.Scope{}, credential.ErrNotFound
	}
	binding, err := resolver.bindings.ReadTargetConnectionBinding(ctx,
		resource.TargetID, resource.ProjectID, resource.Environment, resource.ResourceID)
	if err != nil || binding.TargetID != resource.TargetID || binding.ProjectID != resource.ProjectID ||
		binding.Environment != resource.Environment || binding.ConnectionID != resource.ResourceID ||
		!canonicalCredentialValue(binding.ConnectorKind) ||
		!validEndpointConfigHash(binding.EndpointConfigHash) {
		return credential.Scope{}, credential.ErrNotFound
	}
	if _, ok := connectors.LookupConnection(binding.ConnectorKind); !ok {
		return credential.Scope{}, credential.ErrNotFound
	}
	return credential.Scope{
		Resource: resource, OwnerID: ownerID, Purpose: connectionCredentialPurpose,
		Provider: binding.ConnectorKind, Destination: binding.EndpointConfigHash,
	}, nil
}

func canonicalCredentialValue(value string) bool {
	return value != "" && len(value) <= 255 && utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) < 0
}

func validEndpointConfigHash(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(value[len("sha256:"):])
	return err == nil && strings.ToLower(value) == value
}
