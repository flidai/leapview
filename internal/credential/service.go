package credential

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/connectors"
	"github.com/flidai/leapview/internal/credential/encryption"
	"github.com/flidai/leapview/internal/platform/typednil"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
)

// Service exposes draft writes and metadata reads. Isolated credential
// validation is a separate operation owned by ValidationService.
type Service struct {
	repository Repository
	keys       Encryptor
	scopes     ScopeResolver
	authorizer Authorizer
}

func NewService(repository Repository, keys Encryptor, scopes ScopeResolver, authorizer Authorizer) (*Service, error) {
	if typednil.IsNil(repository) || typednil.IsNil(keys) || typednil.IsNil(scopes) || typednil.IsNil(authorizer) {
		return nil, ErrUnavailable
	}
	if !canonical(keys.DeploymentID()) {
		return nil, ErrInvalid
	}
	return &Service{repository: repository, keys: keys, scopes: scopes, authorizer: authorizer}, nil
}

func (s *Service) authorize(ctx context.Context, actor string, resource Resource, write bool) error {
	if s == nil || typednil.IsNil(s.authorizer) {
		return ErrUnavailable
	}
	if !canonical(actor) || resource.Validate() != nil {
		return ErrInvalid
	}
	var pair access.PermissionPair
	var err error
	if resource.ScopeKind == "connection" {
		action := access.ActionConnectionRead
		if write {
			action = access.ActionConnectionManage
		}
		ref, refErr := access.NewResourceRef(projectgraph.ResourceID(resource.ResourceID), projectgraph.KindConnection)
		if refErr != nil {
			return ErrInvalid
		}
		pair, err = access.NewExactPermissionPair(action, projectgraph.ResourceID(resource.ProjectID), ref)
	} else {
		action := access.ActionPlatformSettingsRead
		if write {
			action = access.ActionPlatformSettingsUpdate
		}
		pair, err = access.NewInstancePermissionPair(action, resource.ResourceID)
	}
	if err != nil {
		return ErrInvalid
	}
	if err := s.authorizer.RequirePermission(ctx, actor, pair); err != nil {
		return ErrForbidden
	}
	return nil
}

func (s *Service) scope(ctx context.Context, resource Resource) (Scope, error) {
	if typednil.IsNil(s.scopes) || typednil.IsNil(s.keys) || typednil.IsNil(s.repository) {
		return Scope{}, ErrUnavailable
	}
	scope, err := s.scopes.ResolveCredentialScope(ctx, resource)
	if err != nil {
		return Scope{}, ErrNotFound
	}
	if scope.Resource != resource || !canonical(scope.OwnerID) || !canonical(scope.Purpose) || !canonical(scope.Provider) || !destinationDigest(scope.Destination) {
		return Scope{}, ErrInvalid
	}
	return scope, nil
}

func destinationDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != 71 {
		return false
	}
	digest := value[7:]
	_, err := hex.DecodeString(digest)
	return err == nil && strings.ToLower(digest) == digest
}

// SaveDraft accepts typed fields for the resolved connector, or api_key for an
// agent. Its only durable effect is an audited encrypted draft. It must not be
// placed behind middleware that persists a raw-body request digest.
func (s *Service) SaveDraft(ctx context.Context, actor string, resource Resource, fields map[string]string) (Metadata, error) {
	if err := s.authorize(ctx, actor, resource, true); err != nil {
		return Metadata{}, err
	}
	scope, err := s.scope(ctx, resource)
	if err != nil {
		return Metadata{}, err
	}
	plaintext, err := encodeFields(scope, fields)
	if err != nil {
		return Metadata{}, err
	}
	defer clear(plaintext)
	binding := encryption.Binding{
		DeploymentID: s.keys.DeploymentID(), OwnerID: scope.OwnerID,
		ScopeKind: resource.ScopeKind, TargetID: resource.TargetID, ProjectID: resource.ProjectID,
		Environment: resource.Environment, ResourceID: resource.ResourceID,
		Purpose: scope.Purpose, Provider: scope.Provider, Destination: scope.Destination, VersionID: uuid.NewString(),
	}
	envelope, err := s.keys.Encrypt(ctx, s.repository, binding, plaintext)
	if err != nil {
		if errors.Is(err, encryption.ErrBudgetExhausted) {
			return Metadata{}, encryption.ErrBudgetExhausted
		}
		return Metadata{}, ErrUnavailable
	}
	// Permission may have been revoked while the durable budget was reserved.
	if err := s.authorize(ctx, actor, resource, true); err != nil {
		return Metadata{}, err
	}
	metadata := Metadata{Binding: binding, ActorID: actor, CreatedAt: time.Now().UTC()}
	intent, err := draftAudit(metadata)
	if err != nil {
		return Metadata{}, ErrInvalid
	}
	if err := s.repository.SaveDraft(ctx, StoredVersion{Metadata: metadata, Envelope: envelope}, intent); err != nil {
		return Metadata{}, ErrUnavailable
	}
	return metadata, nil
}

func encodeFields(scope Scope, fields map[string]string) ([]byte, error) {
	allowed := []string{"api_key"}
	required := [][]string{{"api_key"}}
	if scope.Resource.ScopeKind == "connection" {
		spec, ok := connectors.LookupConnection(scope.Provider)
		if !ok || len(spec.AuthKeys) == 0 || len(spec.RequiredAuthSets) == 0 {
			return nil, ErrInvalid
		}
		allowed, required = spec.AuthKeys, spec.RequiredAuthSets
	}
	if len(fields) == 0 || len(fields) > len(allowed) {
		return nil, ErrInvalid
	}
	size := 0
	for name, value := range fields {
		if !slices.Contains(allowed, name) || !utf8.ValidString(value) {
			return nil, ErrInvalid
		}
		size += len(name) + len(value)
		if size > encryption.MaxPlaintextSize {
			return nil, ErrInvalid
		}
	}
	valid := false
	for _, set := range required {
		present := len(set) > 0
		for _, name := range set {
			present = present && fields[name] != ""
		}
		valid = valid || present
	}
	if !valid {
		return nil, ErrInvalid
	}
	raw, err := json.Marshal(fields)
	if err != nil || len(raw) > encryption.MaxPlaintextSize {
		return nil, ErrInvalid
	}
	return raw, nil
}

// GetDraft returns only metadata after a current exact-resource authorization.
// Ciphertext, plaintext and unkeyed secret digests are never returned.
func (s *Service) GetDraft(ctx context.Context, actor string, resource Resource, versionID string) (Metadata, error) {
	if err := s.authorize(ctx, actor, resource, false); err != nil {
		return Metadata{}, err
	}
	if id, err := uuid.Parse(versionID); err != nil || id.String() != versionID {
		return Metadata{}, ErrInvalid
	}
	scope, err := s.scope(ctx, resource)
	if err != nil {
		return Metadata{}, err
	}
	metadata, err := s.repository.GetDraftMetadata(ctx, s.keys.DeploymentID(), scope.OwnerID, resource, versionID)
	if errors.Is(err, ErrNotFound) {
		return Metadata{}, ErrNotFound
	}
	if err != nil {
		return Metadata{}, ErrUnavailable
	}
	if !metadataMatchesScope(metadata, s.keys.DeploymentID(), scope.OwnerID, resource) || metadata.Binding.VersionID != versionID {
		return Metadata{}, ErrNotFound
	}
	return metadata, nil
}

// ListDrafts returns a bounded, newest-first metadata page for one exact
// authorized resource. The continuation is a version ID resolved under the
// same deployment, owner, and resource filters on every request.
func (s *Service) ListDrafts(ctx context.Context, actor string, resource Resource, limit int, beforeVersionID string) (DraftPage, error) {
	if err := s.authorize(ctx, actor, resource, false); err != nil {
		return DraftPage{}, err
	}
	if limit == 0 {
		limit = DefaultDraftPageSize
	}
	if limit < 1 || limit > MaxDraftPageSize {
		return DraftPage{}, ErrInvalid
	}
	if beforeVersionID != "" {
		id, err := uuid.Parse(beforeVersionID)
		if err != nil || id.String() != beforeVersionID {
			return DraftPage{}, ErrInvalidCursor
		}
	}
	scope, err := s.scope(ctx, resource)
	if err != nil {
		return DraftPage{}, err
	}
	page, err := s.repository.ListDrafts(ctx, s.keys.DeploymentID(), scope.OwnerID, resource, limit, beforeVersionID)
	if errors.Is(err, ErrInvalidCursor) {
		return DraftPage{}, ErrInvalidCursor
	}
	if err != nil {
		return DraftPage{}, ErrUnavailable
	}
	if len(page.Items) > limit || (page.NextBeforeVersionID != "" && len(page.Items) == 0) {
		return DraftPage{}, ErrUnavailable
	}
	for _, metadata := range page.Items {
		if !metadataMatchesScope(metadata, s.keys.DeploymentID(), scope.OwnerID, resource) {
			return DraftPage{}, ErrUnavailable
		}
	}
	if page.NextBeforeVersionID != "" {
		last := page.Items[len(page.Items)-1].Binding.VersionID
		if page.NextBeforeVersionID != last {
			return DraftPage{}, ErrUnavailable
		}
	}
	return page, nil
}

func metadataMatchesScope(metadata Metadata, deploymentID, ownerID string, resource Resource) bool {
	binding := metadata.Binding
	versionID, err := uuid.Parse(binding.VersionID)
	return err == nil && versionID.String() == binding.VersionID && binding.Validate() == nil &&
		canonical(metadata.ActorID) && !metadata.CreatedAt.IsZero() &&
		binding.DeploymentID == deploymentID && binding.OwnerID == ownerID &&
		(Resource{binding.ScopeKind, binding.TargetID, binding.ProjectID, binding.Environment, binding.ResourceID}) == resource
}

func draftAudit(metadata Metadata) (access.AuditIntent, error) {
	binding := metadata.Binding
	payload, err := json.Marshal(struct {
		VersionID  string `json:"version_id"`
		KeyPurpose string `json:"purpose"`
	}{binding.VersionID, binding.Purpose})
	if err != nil {
		return access.AuditIntent{}, err
	}
	scopeID, resourceKind := binding.ProjectID, "connection"
	if binding.ScopeKind == "agent" {
		scopeID, resourceKind = binding.DeploymentID, "instance"
	}
	principalID := ""
	if id, err := uuid.Parse(metadata.ActorID); err == nil && id.String() == metadata.ActorID {
		principalID = metadata.ActorID
	}
	return (access.AuditIntent{
		EventID: uuid.NewString(), ScopeID: scopeID, ActorID: metadata.ActorID,
		PrincipalID: principalID, Source: "credential", Operation: "saveCredentialDraft", Action: "credential.draft.saved",
		ResourceKind: resourceKind, ResourceID: binding.ResourceID, Outcome: "success",
		AggregateKey: "credential:" + binding.VersionID, AggregateSequence: 1, MetadataJSON: string(payload),
	}).Canonicalize()
}
