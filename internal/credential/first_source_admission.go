package credential

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// FirstSourceAdmissionIntent is explicit offline installation-operator intent.
// It contains configuration identities, never credential fields or a version
// pin. Admission stages authority; validation and publication remain separate.
type FirstSourceAdmissionIntent struct {
	Version                 int                            `json:"version"`
	OperationID             string                         `json:"operationId"`
	TargetID                string                         `json:"targetId"`
	ProjectID               string                         `json:"projectId"`
	Environment             string                         `json:"environment"`
	CustomerOwnerID         string                         `json:"customerOwnerId"`
	OperatorPrincipalID     string                         `json:"operatorPrincipalId"`
	ConnectionID            string                         `json:"connectionId"`
	BindingID               string                         `json:"bindingId"`
	ExpectedBindingRevision int64                          `json:"expectedBindingRevision"`
	ExpectedPolicyRevision  int64                          `json:"expectedPolicyRevision"`
	ExpectedPolicyDigest    string                         `json:"expectedPolicyDigest"`
	Endpoint                FirstSourceEndpoint            `json:"endpoint"`
	CredentialReference     FirstSourceCredentialReference `json:"credentialReference"`
}

// These admission-owned identities do not import the analytics binding use
// case. Composition must validate and convert them through that existing
// binding contract before admitting any authority.
type FirstSourceEndpoint struct {
	Host           string            `json:"host,omitempty"`
	Port           int               `json:"port,omitempty"`
	Database       string            `json:"database,omitempty"`
	ObjectScope    string            `json:"objectScope,omitempty"`
	SourceIdentity string            `json:"sourceIdentity,omitempty"`
	TLSMode        string            `json:"tlsMode,omitempty"`
	Options        map[string]string `json:"options,omitempty"`
}

type FirstSourceCredentialReference struct {
	ProjectID   projectgraph.ResourceID `json:"projectId"`
	Environment string                  `json:"environment"`
	SecretPath  string                  `json:"secretPath"`
	SecretKey   string                  `json:"secretKey"`
}

func (i FirstSourceAdmissionIntent) Validate() error {
	if i.Version != 1 || !canonicalPreparationID(i.OperationID) || !canonical(i.CustomerOwnerID) ||
		!canonical(i.OperatorPrincipalID) || i.ExpectedBindingRevision != 0 || i.ExpectedPolicyRevision < 1 || i.ExpectedPolicyRevision == math.MaxInt64 ||
		!destinationDigest(i.ExpectedPolicyDigest) || !canonical(i.TargetID) || !canonical(i.Environment) ||
		!projectgraph.ResourceID(i.ProjectID).Valid() || !projectgraph.ResourceID(i.ConnectionID).Valid() || !canonicalBindingID(i.BindingID) ||
		i.CredentialReference.ProjectID.String() != i.ProjectID || i.CredentialReference.Environment != i.Environment ||
		!canonical(i.CredentialReference.SecretPath) || !strings.HasPrefix(i.CredentialReference.SecretPath, "/") || !canonical(i.CredentialReference.SecretKey) {
		return ErrInvalid
	}
	return nil
}

func (i FirstSourceAdmissionIntent) Digest() (string, error) {
	if err := i.Validate(); err != nil {
		return "", err
	}
	return firstSourceDigest(i)
}

// BindingDigest excludes mutable health and timestamps while retaining the
// complete exact configuration, including the credential reference identity.
func (i FirstSourceAdmissionIntent) BindingDigest() (string, error) {
	if err := i.Validate(); err != nil {
		return "", err
	}
	return firstSourceDigest(struct {
		TargetID, ProjectID, Environment, ConnectionID, BindingID string
		ConnectorKind, AuthenticationMode                         string
		Endpoint                                                  FirstSourceEndpoint
		CredentialReference                                       FirstSourceCredentialReference
	}{i.TargetID, i.ProjectID, i.Environment, i.ConnectionID, i.BindingID,
		"postgres", "external_bundle", i.Endpoint, i.CredentialReference})
}

func firstSourceDigest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(encoded)), nil
}

// Grant is a fixed exact-resource operator grant. It never delegates arbitrary
// nondelegable actions or changes a role preset's permissions.
func (i FirstSourceAdmissionIntent) Grant() (access.AuthorizationGrant, error) {
	if err := i.Validate(); err != nil {
		return access.AuthorizationGrant{}, err
	}
	resource, err := access.NewResourceRef(projectgraph.ResourceID(i.ConnectionID), projectgraph.KindConnection)
	if err != nil {
		return access.AuthorizationGrant{}, err
	}
	pairs := make([]access.PermissionPair, 0, 2)
	for _, action := range []access.Action{access.ActionConnectionManage, access.ActionConnectionUse} {
		pair, err := access.NewExactPermissionPair(action, projectgraph.ResourceID(i.ProjectID), resource)
		if err != nil {
			return access.AuthorizationGrant{}, err
		}
		pairs = append(pairs, pair)
	}
	grant := access.AuthorizationGrant{ID: "first-source-" + i.OperationID, Name: "First-source credential operator",
		Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: i.OperatorPrincipalID}, Resource: resource,
		PermissionProfile: access.PermissionCatalogProfile, Permissions: pairs}
	return grant, access.ValidateAuthorizationGrant(grant)
}

// FirstSourceAdmission is durable authority, independent from audit retention.
// Consumers must additionally recheck the live claim, principal, binding,
// staged exact grant and unpublished target before any credential work.
type FirstSourceAdmission struct {
	Intent         FirstSourceAdmissionIntent `json:"intent"`
	IntentDigest   string                     `json:"intentDigest"`
	BindingDigest  string                     `json:"bindingDigest"`
	PolicyRevision int64                      `json:"policyRevision"`
	PolicyDigest   string                     `json:"policyDigest"`
	CreatedAt      time.Time                  `json:"createdAt"`
}

func (a FirstSourceAdmission) Validate() error {
	digest, err := a.Intent.Digest()
	if err != nil {
		return err
	}
	binding, err := a.Intent.BindingDigest()
	if err != nil || a.IntentDigest != digest || a.BindingDigest != binding ||
		a.PolicyRevision != a.Intent.ExpectedPolicyRevision+1 || !destinationDigest(a.PolicyDigest) || a.CreatedAt.IsZero() {
		return ErrInvalid
	}
	return nil
}

func (a FirstSourceAdmission) AuditIntent() (access.AuditIntent, error) {
	if err := a.Validate(); err != nil {
		return access.AuditIntent{}, err
	}
	metadata, err := json.Marshal(struct {
		IntentDigest        string `json:"intentDigest"`
		BindingDigest       string `json:"bindingDigest"`
		OperatorPrincipalID string `json:"operatorPrincipalId"`
		BindingID           string `json:"bindingId"`
		PolicyRevision      int64  `json:"policyRevision"`
		PolicyDigest        string `json:"policyDigest"`
	}{a.IntentDigest, a.BindingDigest, a.Intent.OperatorPrincipalID, a.Intent.BindingID, a.PolicyRevision, a.PolicyDigest})
	if err != nil {
		return access.AuditIntent{}, err
	}
	return (access.AuditIntent{EventID: a.Intent.OperationID, ScopeID: a.Intent.TargetID, ActorID: "offline_operator",
		Source: "credential", Operation: "admitFirstSource", Action: "credential.first_source.admitted",
		ResourceKind: "connection", ResourceID: a.Intent.ConnectionID, Outcome: "success",
		AggregateKey: "credential-first-source:" + a.Intent.TargetID, AggregateSequence: 1,
		MetadataJSON: string(metadata)}).Canonicalize()
}
