package credential

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/credential/encryption"
	"github.com/flidai/leapview/internal/platform/typednil"
	"github.com/google/uuid"
)

const (
	validationProbeTimeout = 30 * time.Second
	validationReceiptTTL   = 5 * time.Minute
)

// ValidationReceipt records a successful isolated probe of one saved
// credential version against one exact target-binding revision. It conveys no
// authority by itself; callers must reauthorize and compare current target
// state before any later activation.
type ValidationReceipt struct {
	ReceiptID           string
	Binding             encryption.Binding
	ActorID             string
	BindingID           string
	BindingRevision     int64
	ConfigurationDigest string
	ValidatedAt         time.Time
	ExpiresAt           time.Time
}

// Validate checks the canonical identity and bounded lifetime recorded in a
// validation receipt. It deliberately does not imply that the receipt remains
// current or may be used for activation.
func (receipt ValidationReceipt) Validate() error {
	parsedReceiptID, err := uuid.Parse(receipt.ReceiptID)
	if err != nil || parsedReceiptID.String() != receipt.ReceiptID || parsedReceiptID == uuid.Nil ||
		receipt.Binding.Validate() != nil ||
		!canonical(receipt.ActorID) || !canonicalBindingID(receipt.BindingID) ||
		!validCredentialRevision(receipt.Binding.ScopeKind, receipt.BindingRevision) || !destinationDigest(receipt.ConfigurationDigest) ||
		receipt.ValidatedAt.IsZero() || receipt.ExpiresAt.IsZero() ||
		!receipt.ExpiresAt.Equal(receipt.ValidatedAt.Add(validationReceiptTTL)) {
		return ErrInvalid
	}
	return nil
}

// AuditIntent builds the redacted, source-owned audit payload for this
// receipt. It contains no credential field, ciphertext or credential digest.
func (receipt ValidationReceipt) AuditIntent() (access.AuditIntent, error) {
	if err := receipt.Validate(); err != nil {
		return access.AuditIntent{}, err
	}
	metadata, err := json.Marshal(struct {
		ReceiptID           string    `json:"receipt_id"`
		VersionID           string    `json:"version_id"`
		BindingID           string    `json:"binding_id"`
		BindingRevision     int64     `json:"binding_revision"`
		ConfigurationDigest string    `json:"configuration_digest"`
		ExpiresAt           time.Time `json:"expires_at"`
	}{
		ReceiptID: receipt.ReceiptID, VersionID: receipt.Binding.VersionID,
		BindingID: receipt.BindingID, BindingRevision: receipt.BindingRevision,
		ConfigurationDigest: receipt.ConfigurationDigest, ExpiresAt: receipt.ExpiresAt.UTC(),
	})
	if err != nil {
		return access.AuditIntent{}, ErrInvalid
	}
	principalID := ""
	if id, err := uuid.Parse(receipt.ActorID); err == nil && id.String() == receipt.ActorID {
		principalID = receipt.ActorID
	}
	scopeID, resourceKind := credentialAuditScope(receipt.Binding)
	return (access.AuditIntent{
		EventID: uuid.NewString(), DomainEventID: receipt.ReceiptID,
		ScopeID: scopeID, ActorID: receipt.ActorID,
		PrincipalID: principalID, Source: "credential", Operation: "validateCredentialDraft",
		Action: "credential.draft.validated", ResourceKind: resourceKind,
		ResourceID: receipt.Binding.ResourceID, Outcome: "success",
		AggregateKey:      "credential-validation:" + receipt.ReceiptID,
		AggregateSequence: 1, MetadataJSON: string(metadata),
	}).Canonicalize()
}

// ValidateValidationAuditIntent verifies every field in the provided intent
// against the exact receipt. Storage adapters call it before inserting the
// receipt and intent in the same transaction.
func ValidateValidationAuditIntent(receipt ValidationReceipt, intent access.AuditIntent) (access.AuditIntent, error) {
	if err := receipt.Validate(); err != nil {
		return access.AuditIntent{}, fmt.Errorf("%w: invalid credential validation receipt", ErrInvalid)
	}
	canonicalIntent, err := intent.Canonicalize()
	if err != nil {
		return access.AuditIntent{}, fmt.Errorf("%w: invalid credential validation audit intent", ErrInvalid)
	}
	eventID, eventErr := uuid.Parse(canonicalIntent.EventID)
	var metadata map[string]json.RawMessage
	if json.Unmarshal([]byte(canonicalIntent.MetadataJSON), &metadata) != nil || len(metadata) != 6 {
		return access.AuditIntent{}, ErrInvalid
	}
	var receiptID, versionID, bindingID, configDigest, expiresAt string
	var revision int64
	if json.Unmarshal(metadata["receipt_id"], &receiptID) != nil ||
		json.Unmarshal(metadata["version_id"], &versionID) != nil ||
		json.Unmarshal(metadata["binding_id"], &bindingID) != nil ||
		json.Unmarshal(metadata["binding_revision"], &revision) != nil ||
		json.Unmarshal(metadata["configuration_digest"], &configDigest) != nil ||
		json.Unmarshal(metadata["expires_at"], &expiresAt) != nil {
		return access.AuditIntent{}, ErrInvalid
	}
	parsedExpiry, expiryErr := time.Parse(time.RFC3339Nano, expiresAt)
	actorPrincipal := ""
	if actorID, err := uuid.Parse(receipt.ActorID); err == nil && actorID.String() == receipt.ActorID {
		actorPrincipal = receipt.ActorID
	}
	scopeID, resourceKind := credentialAuditScope(receipt.Binding)
	if eventErr != nil || eventID == uuid.Nil || expiryErr != nil || !parsedExpiry.Equal(receipt.ExpiresAt) ||
		canonicalIntent.DomainEventID != receipt.ReceiptID || canonicalIntent.ScopeID != scopeID ||
		canonicalIntent.ActorID != receipt.ActorID || canonicalIntent.PrincipalID != actorPrincipal ||
		canonicalIntent.Source != "credential" || canonicalIntent.Operation != "validateCredentialDraft" ||
		canonicalIntent.Action != "credential.draft.validated" || canonicalIntent.ResourceKind != resourceKind ||
		canonicalIntent.ResourceID != receipt.Binding.ResourceID || canonicalIntent.Outcome != "success" ||
		canonicalIntent.AggregateKey != "credential-validation:"+receipt.ReceiptID || canonicalIntent.AggregateSequence != 1 ||
		canonicalIntent.RequestDigest != "" || canonicalIntent.Capability != "" || canonicalIntent.RequestID != "" ||
		canonicalIntent.CorrelationID != "" || receiptID != receipt.ReceiptID || versionID != receipt.Binding.VersionID ||
		bindingID != receipt.BindingID || revision != receipt.BindingRevision || configDigest != receipt.ConfigurationDigest {
		return access.AuditIntent{}, ErrInvalid
	}
	return canonicalIntent, nil
}

// ValidationTarget is the server-resolved, non-secret identity of the exact
// target configuration that will be probed. The configuration digest covers
// the relevant non-secret destination and runtime policy supplied by the
// analytics adapter.
type ValidationTarget struct {
	Scope               Scope
	BindingID           string
	BindingRevision     int64
	ConfigurationDigest string
}

func (target ValidationTarget) Validate(resource Resource) error {
	if target.Scope.Resource != resource || !canonical(target.Scope.OwnerID) ||
		!canonical(target.Scope.Purpose) || !canonical(target.Scope.Provider) ||
		!destinationDigest(target.Scope.Destination) || !canonicalBindingID(target.BindingID) ||
		!validCredentialRevision(resource.ScopeKind, target.BindingRevision) || !destinationDigest(target.ConfigurationDigest) {
		return ErrInvalid
	}
	return nil
}

// ValidationProbe resolves current server-owned target state and probes saved
// credential fields in a temporary, isolated runtime. Implementations must
// never retain or log fields, activate a shared pool, or use request-supplied
// destination details.
type ValidationProbe interface {
	ResolveValidationTarget(context.Context, Resource, Scope) (ValidationTarget, error)
	ProbeCredential(context.Context, ValidationTarget, string, map[string]string) error
}

// ValidationKeyring decrypts only the exact authenticated draft binding.
type ValidationKeyring interface {
	DeploymentID() string
	Decrypt(encryption.Binding, encryption.Envelope) ([]byte, error)
}

// ValidationRepository retrieves an exact saved envelope and durably appends
// a non-secret receipt with its redacted audit intent.
type ValidationRepository interface {
	GetStoredDraft(context.Context, string, string, Resource, string) (StoredVersion, error)
	SaveValidation(context.Context, ValidationReceipt, access.AuditIntent) error
}

// ValidationService owns isolated validation of immutable connection drafts.
// Receipt creation never changes connection bindings or runtime pools.
type ValidationService struct {
	repository ValidationRepository
	keys       ValidationKeyring
	scopes     ScopeResolver
	authorizer Authorizer
	probe      ValidationProbe
	now        func() time.Time
}

func NewValidationService(
	repository ValidationRepository,
	keys ValidationKeyring,
	scopes ScopeResolver,
	authorizer Authorizer,
	probe ValidationProbe,
	now func() time.Time,
) (*ValidationService, error) {
	if typednil.IsNil(repository) || typednil.IsNil(keys) || typednil.IsNil(scopes) ||
		typednil.IsNil(authorizer) || typednil.IsNil(probe) || now == nil || !canonical(keys.DeploymentID()) {
		return nil, ErrUnavailable
	}
	return &ValidationService{repository: repository, keys: keys, scopes: scopes, authorizer: authorizer, probe: probe, now: now}, nil
}

// ValidateDraft authorizes, decrypts and probes one immutable saved
// PostgreSQL password draft against the caller's expected binding revision.
// A success writes only a short-lived receipt and redacted audit. It cannot
// publish or activate the tested credential.
func (service *ValidationService) ValidateDraft(
	ctx context.Context,
	actor string,
	resource Resource,
	versionID string,
	expectedBindingRevision int64,
) (ValidationReceipt, error) {
	if service == nil || ctx == nil || typednil.IsNil(service.repository) || typednil.IsNil(service.keys) ||
		typednil.IsNil(service.scopes) || typednil.IsNil(service.authorizer) || typednil.IsNil(service.probe) {
		return ValidationReceipt{}, ErrUnavailable
	}
	if resource.Validate() != nil || !canonical(actor) || !validCredentialRevision(resource.ScopeKind, expectedBindingRevision) {
		return ValidationReceipt{}, ErrInvalid
	}
	parsedVersionID, err := uuid.Parse(versionID)
	if err != nil || parsedVersionID.String() != versionID || parsedVersionID == uuid.Nil {
		return ValidationReceipt{}, ErrInvalid
	}
	if service.keys.DeploymentID() == "" {
		return ValidationReceipt{}, ErrUnavailable
	}
	if err := service.authorize(ctx, actor, resource); err != nil {
		return ValidationReceipt{}, err
	}

	scope, err := service.resolveScope(ctx, resource)
	if err != nil {
		return ValidationReceipt{}, err
	}
	target, err := service.resolveTarget(ctx, resource, scope)
	if err != nil {
		return ValidationReceipt{}, err
	}
	if target.Scope != scope || target.BindingRevision != expectedBindingRevision {
		return ValidationReceipt{}, ErrConflict
	}
	// V1 validation deliberately accepts only a PostgreSQL password field.
	// Connection strings can override the server-resolved endpoint and mixed
	// forms make the destination ambiguous.
	if resource.ScopeKind == "connection" && scope.Provider != "postgres" {
		return ValidationReceipt{}, ErrValidationFailed
	}

	stored, err := service.repository.GetStoredDraft(ctx, service.keys.DeploymentID(), scope.OwnerID, resource, versionID)
	if errors.Is(err, ErrNotFound) {
		return ValidationReceipt{}, ErrNotFound
	}
	if err != nil {
		return ValidationReceipt{}, ErrUnavailable
	}
	if !metadataMatchesScope(stored.Metadata, service.keys.DeploymentID(), scope.OwnerID, resource) ||
		stored.Metadata.Binding.VersionID != versionID || stored.Metadata.Binding != expectedEncryptionBinding(service.keys.DeploymentID(), scope, versionID) {
		return ValidationReceipt{}, ErrNotFound
	}

	plaintext, err := service.keys.Decrypt(stored.Metadata.Binding, stored.Envelope)
	if err != nil {
		return ValidationReceipt{}, ErrValidationFailed
	}
	defer clear(plaintext)
	fields, ok := decodeValidatedCredential(scope, plaintext)
	if !ok {
		return ValidationReceipt{}, ErrValidationFailed
	}
	defer clear(fields)

	probeCtx, cancelProbe := context.WithTimeout(ctx, validationProbeTimeout)
	probeErr := service.probe.ProbeCredential(probeCtx, target, versionID, fields)
	probeContextErr := probeCtx.Err()
	cancelProbe()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ValidationReceipt{}, ctxErr
	}
	if probeContextErr != nil || probeErr != nil {
		return ValidationReceipt{}, ErrValidationFailed
	}
	validatedAt := service.now().UTC().Truncate(time.Microsecond)
	if validatedAt.IsZero() {
		return ValidationReceipt{}, ErrUnavailable
	}

	// Re-read both target and ownership state after provider I/O. This is an
	// observation-time check; activation must perform its own current-state CAS.
	currentScope, err := service.resolveScope(ctx, resource)
	if err != nil {
		return ValidationReceipt{}, err
	}
	currentTarget, err := service.resolveTarget(ctx, resource, currentScope)
	if err != nil {
		return ValidationReceipt{}, err
	}
	if currentScope != scope || currentTarget != target {
		return ValidationReceipt{}, ErrConflict
	}
	if err := service.authorize(ctx, actor, resource); err != nil {
		return ValidationReceipt{}, err
	}
	receipt := ValidationReceipt{
		ReceiptID: uuid.NewString(), Binding: stored.Metadata.Binding, ActorID: actor,
		BindingID: target.BindingID, BindingRevision: target.BindingRevision,
		ConfigurationDigest: target.ConfigurationDigest, ValidatedAt: validatedAt,
		ExpiresAt: validatedAt.Add(validationReceiptTTL),
	}
	intent, err := receipt.AuditIntent()
	if err != nil {
		return ValidationReceipt{}, ErrUnavailable
	}
	if !service.now().Before(receipt.ExpiresAt) {
		return ValidationReceipt{}, ErrConflict
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ValidationReceipt{}, ctxErr
	}
	if err := service.repository.SaveValidation(ctx, receipt, intent); err != nil {
		if errors.Is(err, ErrConflict) {
			return ValidationReceipt{}, ErrConflict
		}
		return ValidationReceipt{}, ErrUnavailable
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ValidationReceipt{}, ctxErr
	}
	if !service.now().Before(receipt.ExpiresAt) {
		return ValidationReceipt{}, ErrConflict
	}
	return receipt, nil
}

func (service *ValidationService) resolveScope(ctx context.Context, resource Resource) (Scope, error) {
	scope, err := service.scopes.ResolveCredentialScope(ctx, resource)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Scope{}, ErrNotFound
		}
		return Scope{}, ErrUnavailable
	}
	if scope.Resource != resource || !canonical(scope.OwnerID) || !canonical(scope.Purpose) ||
		!canonical(scope.Provider) || !destinationDigest(scope.Destination) {
		return Scope{}, ErrInvalid
	}
	return scope, nil
}

func (service *ValidationService) resolveTarget(ctx context.Context, resource Resource, scope Scope) (ValidationTarget, error) {
	target, err := service.probe.ResolveValidationTarget(ctx, resource, scope)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ValidationTarget{}, ErrNotFound
		}
		if errors.Is(err, ErrConflict) {
			return ValidationTarget{}, ErrConflict
		}
		return ValidationTarget{}, ErrUnavailable
	}
	if err := target.Validate(resource); err != nil {
		return ValidationTarget{}, ErrInvalid
	}
	return target, nil
}

func expectedEncryptionBinding(deploymentID string, scope Scope, versionID string) encryption.Binding {
	resource := scope.Resource
	return encryption.Binding{
		DeploymentID: deploymentID, OwnerID: scope.OwnerID, ScopeKind: resource.ScopeKind,
		TargetID: resource.TargetID, ProjectID: resource.ProjectID,
		Environment: resource.Environment, ResourceID: resource.ResourceID,
		Purpose: scope.Purpose, Provider: scope.Provider, Destination: scope.Destination,
		VersionID: versionID,
	}
}

func canonicalBindingID(value string) bool {
	if len(value) == 0 || len(value) > 160 || !asciiAlphaNumeric(value[0]) {
		return false
	}
	for index := 1; index < len(value); index++ {
		char := value[index]
		if !asciiAlphaNumeric(char) && char != '_' && char != '.' && char != ':' && char != '-' {
			return false
		}
	}
	return true
}

func asciiAlphaNumeric(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
}

func decodePostgresPassword(plaintext []byte) (map[string]string, bool) {
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, false
	}
	fields := make(map[string]string, 1)
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			clear(fields)
			return nil, false
		}
		name, ok := token.(string)
		if !ok || name != "password" {
			clear(fields)
			return nil, false
		}
		if _, duplicate := fields[name]; duplicate {
			clear(fields)
			return nil, false
		}
		var value string
		if err := decoder.Decode(&value); err != nil || value == "" {
			clear(fields)
			return nil, false
		}
		fields[name] = value
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') || len(fields) != 1 {
		clear(fields)
		return nil, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		clear(fields)
		return nil, false
	}
	return fields, true
}
