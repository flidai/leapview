package credentialagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/flidai/leapview/internal/access"
	agentmodule "github.com/flidai/leapview/internal/agent/module"
	"github.com/flidai/leapview/internal/credential"
	"github.com/flidai/leapview/internal/credential/encryption"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	credentialpostgres "github.com/flidai/leapview/internal/credential/postgres"
	"github.com/flidai/leapview/internal/platform/security/secret"
	"github.com/flidai/leapview/internal/platform/typednil"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const agentCredentialPurpose = "agent-provider-authentication"

type AgentConfigurationStore interface {
	agentmodule.ConfigurationStore
	LockConfigurationTx(context.Context, pgx.Tx) (agentmodule.ConfigurationRevision, error)
	SaveConfigurationTx(context.Context, pgx.Tx, int64, agentmodule.ConfigurationRevision) (agentmodule.ConfigurationRevision, error)
	SaveConfigurationCandidate(context.Context, agentmodule.ConfigurationCandidate) error
	ConfigurationCandidate(context.Context, string) (agentmodule.ConfigurationCandidate, error)
	ConfigurationCandidateTx(context.Context, pgx.Tx, string) (agentmodule.ConfigurationCandidate, error)
}

type AgentCredentialConfig struct {
	Pool                    *pgxpool.Pool
	RecordAudit             credentialmodule.AuditRecorder
	Store                   AgentConfigurationStore
	CustomerOwner           credentialmodule.CustomerOwnerReader
	CustomerOwnerTx         func(context.Context, pgx.Tx) (string, error)
	InstanceID, KeyringPath string
	Authorize               func(context.Context, string, access.PermissionPair) error
	AuthorizeTx             func(context.Context, pgx.Tx, string, access.PermissionPair) error
	LockFence               func(context.Context, pgx.Tx) error
	Probe                   func(context.Context, agentmodule.ProviderConfig) error
}

// AgentCredentials shares encrypted storage and the single activation
// coordinator with source credentials. Proposals contain no plaintext key.
type AgentCredentials struct {
	config      AgentCredentialConfig
	repository  *credentialpostgres.Repository
	keys        *encryption.Keyring
	coordinator credential.ActivationService
	install     func(context.Context, int64) error
	restore     func(context.Context) error
}

func NewAgentCredentials(ctx context.Context, repository *credentialpostgres.Repository, config AgentCredentialConfig) (*AgentCredentials, error) {
	if repository == nil || config.Pool == nil || config.RecordAudit == nil || typednil.IsNil(config.Store) || config.CustomerOwnerTx == nil || config.Authorize == nil || config.AuthorizeTx == nil || config.LockFence == nil || config.Probe == nil {
		return nil, credential.ErrUnavailable
	}
	keys, configured, err := credentialmodule.LoadConfiguredKeyring(ctx, config.CustomerOwner, config.InstanceID, config.KeyringPath)
	if err != nil {
		return nil, err
	}
	if !configured {
		return nil, credential.ErrUnavailable
	}
	return &AgentCredentials{config: config, repository: repository, keys: keys}, nil
}

func (a *AgentCredentials) SetCoordinator(coordinator credential.ActivationService) error {
	if a == nil || typednil.IsNil(coordinator) || a.coordinator != nil {
		return credential.ErrUnavailable
	}
	a.coordinator = coordinator
	return nil
}
func (a *AgentCredentials) BindRuntime(install func(context.Context, int64) error, restore func(context.Context) error) error {
	if a == nil || install == nil || restore == nil || a.install != nil {
		return credential.ErrUnavailable
	}
	a.install, a.restore = install, restore
	return nil
}
func (a *AgentCredentials) resource() credential.Resource {
	return credential.Resource{ScopeKind: "agent", ResourceID: a.config.InstanceID}
}
func (a *AgentCredentials) AuthorizeMutation(ctx context.Context, actor string, resource credential.Resource) error {
	if a == nil || resource != a.resource() || !canonicalCredentialValue(actor) {
		return credential.ErrForbidden
	}
	pair, err := access.NewInstancePermissionPair(access.ActionPlatformSettingsUpdate, a.config.InstanceID)
	if err != nil || a.config.Authorize(ctx, actor, pair) != nil {
		return credential.ErrForbidden
	}
	return nil
}

type agentScopeResolver struct {
	read func(context.Context) (credential.Scope, error)
}

func (r agentScopeResolver) ResolveCredentialScope(ctx context.Context, resource credential.Resource) (credential.Scope, error) {
	scope, err := r.read(ctx)
	if err != nil || scope.Resource != resource {
		return credential.Scope{}, credential.ErrNotFound
	}
	return scope, nil
}

type agentCredentialAuthorizer struct {
	authorize func(context.Context, string, access.PermissionPair) error
}

func (a agentCredentialAuthorizer) RequirePermission(ctx context.Context, actor string, pair access.PermissionPair) error {
	return a.authorize(ctx, actor, pair)
}

func agentConfigurationDigest(record agentmodule.ConfigurationRevision) string {
	config := record.Config
	config.APIKey = ""
	config.Revision = 0
	config.BaseURL = config.NormalizedBaseURL()
	raw, _ := json.Marshal(struct {
		Enabled bool
		Config  agentmodule.ProviderConfig
	}{record.Enabled, config})
	sum := sha256.Sum256(append([]byte("leapview/agent-configuration/v1\n"), raw...))
	return "sha256:" + hex.EncodeToString(sum[:])
}
func (a *AgentCredentials) scope(ctx context.Context, record agentmodule.ConfigurationRevision, provider string) (credential.Scope, error) {
	owner, err := a.config.CustomerOwner.CustomerOwner(ctx)
	if err != nil {
		return credential.Scope{}, credential.ErrUnavailable
	}
	return a.ownedScope(owner, record, provider)
}

func (a *AgentCredentials) ownedScope(owner string, record agentmodule.ConfigurationRevision, provider string) (credential.Scope, error) {
	if !canonicalCredentialValue(owner) {
		return credential.Scope{}, credential.ErrUnavailable
	}
	if provider != "openai-compatible" && !(provider == "agent-disabled" && !record.Enabled) {
		return credential.Scope{}, credential.ErrInvalid
	}
	return credential.Scope{Resource: a.resource(), OwnerID: owner, Purpose: agentCredentialPurpose, Provider: provider, Destination: agentConfigurationDigest(record)}, nil
}
func (a *AgentCredentials) runtime(ctx context.Context, record agentmodule.ConfigurationRevision, provider string) (*credential.RuntimeCredentials, credential.Scope, error) {
	scope, err := a.scope(ctx, record, provider)
	if err != nil {
		return nil, scope, err
	}
	scopes := agentScopeResolver{read: func(ctx context.Context) (credential.Scope, error) { return a.scope(ctx, record, provider) }}
	runtime, err := credential.NewRuntimeCredentials(a.repository, a.keys, scopes)
	return runtime, scope, err
}
func (a *AgentCredentials) UseConfiguration(ctx context.Context, record agentmodule.ConfigurationRevision, consume func(agentmodule.ProviderConfig) error) error {
	if a == nil || consume == nil || record.Revision < 1 || record.CredentialVersionID == "" || len(record.Credential) != 0 {
		return credential.ErrInvalid
	}
	saved, err := a.config.Store.ConfigurationByRevision(ctx, record.Revision)
	if err != nil || saved.Config != record.Config || saved.Enabled != record.Enabled || saved.CredentialVersionID != record.CredentialVersionID || len(saved.Credential) != 0 {
		return credential.ErrConflict
	}
	owner, err := a.config.CustomerOwner.CustomerOwner(ctx)
	if err != nil {
		return credential.ErrUnavailable
	}
	stored, err := a.repository.GetStoredDraft(ctx, a.config.InstanceID, owner, a.resource(), saved.CredentialVersionID)
	if err != nil {
		return credential.ErrUnavailable
	}
	runtime, scope, err := a.runtime(ctx, saved, stored.Metadata.Binding.Provider)
	if err != nil {
		return err
	}
	return runtime.UseBoundVersion(ctx, scope, saved.CredentialVersionID, func(fields map[string]string) error {
		c := saved.Config
		c.APIKey = fields["api_key"]
		c.Revision = saved.Revision
		if c.Validate(saved.Enabled) != nil {
			return credential.ErrInvalid
		}
		return consume(c)
	})
}

type agentCandidateProbe struct {
	adapter   *AgentCredentials
	candidate agentmodule.ConfigurationCandidate
	scope     credential.Scope
}

func (p agentCandidateProbe) ResolveValidationTarget(ctx context.Context, resource credential.Resource, scope credential.Scope) (credential.ValidationTarget, error) {
	if resource != p.adapter.resource() || scope != p.scope {
		return credential.ValidationTarget{}, credential.ErrConflict
	}
	candidate, err := p.adapter.config.Store.ConfigurationCandidate(ctx, p.candidate.ID)
	if err != nil || candidate.ExpectedRevision != p.candidate.ExpectedRevision || candidate.Config != p.candidate.Config || candidate.Enabled != p.candidate.Enabled || candidate.ActorID != p.candidate.ActorID {
		return credential.ValidationTarget{}, credential.ErrConflict
	}
	current, err := p.adapter.config.Store.CurrentConfiguration(ctx)
	if err != nil && !errors.Is(err, agentmodule.ErrConfigurationNotFound) {
		return credential.ValidationTarget{}, credential.ErrUnavailable
	}
	if current.Revision != candidate.ExpectedRevision {
		return credential.ValidationTarget{}, credential.ErrConflict
	}
	return credential.ValidationTarget{Scope: scope, BindingID: candidate.ID, BindingRevision: candidate.ExpectedRevision, ConfigurationDigest: agentConfigurationDigest(candidate.ConfigurationRevision)}, nil
}
func (p agentCandidateProbe) ProbeCredential(ctx context.Context, target credential.ValidationTarget, _ string, fields map[string]string) error {
	if target.BindingID != p.candidate.ID || target.Scope != p.scope {
		return credential.ErrInvalid
	}
	c := p.candidate.Config
	c.APIKey = fields["api_key"]
	if c.Validate(p.candidate.Enabled) != nil {
		return credential.ErrInvalid
	}
	if !p.candidate.Enabled {
		return nil
	}
	return p.adapter.config.Probe(ctx, c)
}

func (a *AgentCredentials) Test(ctx context.Context, actor string, expected int64, input agentmodule.ConfigurationInput) (string, error) {
	if err := a.AuthorizeMutation(ctx, actor, a.resource()); err != nil {
		return "", err
	}
	record := agentmodule.ConfigurationRevision{Enabled: input.Enabled, Config: agentmodule.ProviderConfig{Model: input.Model, BaseURL: input.BaseURL, APIMode: input.APIMode, ReasoningEffort: input.ReasoningEffort}, ActorID: actor}
	validationConfig := record.Config
	validationConfig.APIKey = input.APIKey
	if expected < 0 || validationConfig.Validate(input.Enabled) != nil || (input.RemoveKey && input.APIKey != "") {
		return "", credential.ErrInvalid
	}
	candidate := agentmodule.ConfigurationCandidate{ID: uuid.NewString(), ExpectedRevision: expected, ConfigurationRevision: record}
	provider := "openai-compatible"
	if input.APIKey == "" && !input.Enabled {
		provider = "agent-disabled"
	}
	scope, err := a.scope(ctx, record, provider)
	if err != nil {
		return "", err
	}
	operationID := uuid.NewString()
	versionID := ""
	pending, err := a.repository.GetPendingActivationRequest(ctx, a.config.InstanceID)
	if err == nil {
		if pending.Resource() != a.resource() || pending.Receipt.ActorID != actor || pending.Request.ExpectedBindingRevision != expected ||
			pending.Receipt.ConfigurationDigest != agentConfigurationDigest(record) || pending.State == "committed" {
			return "", credential.ErrConflict
		}
		candidate, err = a.candidate(ctx, nil, pending.Receipt)
		if err != nil {
			return "", err
		}
		provider = pending.Receipt.Binding.Provider
		scope, err = a.scope(ctx, candidate.ConfigurationRevision, provider)
		if err != nil {
			return "", err
		}
		runtime, _, err := a.runtime(ctx, candidate.ConfigurationRevision, provider)
		if err != nil {
			return "", err
		}
		if err = runtime.UseBoundVersion(ctx, scope, pending.Request.VersionID, func(fields map[string]string) error {
			if !secret.Equal(input.APIKey, fields["api_key"]) {
				return credential.ErrConflict
			}
			return nil
		}); err != nil {
			return "", credential.ErrConflict
		}
		operationID = pending.Request.OperationID
		versionID = pending.Request.VersionID
	} else if !errors.Is(err, credential.ErrNotFound) {
		return "", err
	}
	resolver := agentScopeResolver{read: func(ctx context.Context) (credential.Scope, error) { return a.scope(ctx, record, provider) }}
	authorizer := agentCredentialAuthorizer{authorize: a.config.Authorize}
	if versionID == "" {
		if err := a.config.Store.SaveConfigurationCandidate(ctx, candidate); err != nil {
			return "", credential.ErrUnavailable
		}
		drafts, err := credential.NewService(a.repository, a.keys, resolver, authorizer)
		if err != nil {
			return "", err
		}
		draft, err := drafts.SaveDraft(ctx, actor, a.resource(), map[string]string{"api_key": input.APIKey})
		if err != nil {
			return "", err
		}
		versionID = draft.Binding.VersionID
	}
	probe := agentCandidateProbe{adapter: a, candidate: candidate, scope: scope}
	validation, err := credential.NewValidationService(a.repository, a.keys, resolver, authorizer, probe, time.Now)
	if err != nil {
		return "", err
	}
	receipt, err := validation.ValidateDraft(ctx, actor, a.resource(), versionID, expected)
	if err != nil {
		return "", err
	}
	return receipt.ReceiptID + "." + operationID, nil
}

func (a *AgentCredentials) Activate(ctx context.Context, actor string, expected int64, input agentmodule.ConfigurationInput, token string) (agentmodule.ConfigurationRevision, error) {
	if a == nil || typednil.IsNil(a.coordinator) {
		return agentmodule.ConfigurationRevision{}, credential.ErrUnavailable
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return agentmodule.ConfigurationRevision{}, credential.ErrInvalid
	}
	for _, part := range parts {
		if id, err := uuid.Parse(part); err != nil || id == uuid.Nil || id.String() != part {
			return agentmodule.ConfigurationRevision{}, credential.ErrInvalid
		}
	}
	if err := a.AuthorizeMutation(ctx, actor, a.resource()); err != nil {
		return agentmodule.ConfigurationRevision{}, err
	}
	receipt, err := a.repository.ReadValidationReceipt(ctx, a.config.InstanceID, parts[0])
	if err != nil {
		return agentmodule.ConfigurationRevision{}, err
	}
	candidate, err := a.config.Store.ConfigurationCandidate(ctx, receipt.BindingID)
	if err != nil {
		return agentmodule.ConfigurationRevision{}, credential.ErrConflict
	}
	in := agentmodule.ConfigurationRevision{Enabled: input.Enabled, Config: agentmodule.ProviderConfig{Model: input.Model, BaseURL: input.BaseURL, APIMode: input.APIMode, ReasoningEffort: input.ReasoningEffort}}
	if candidate.ActorID != actor || receipt.ActorID != actor || candidate.ExpectedRevision != expected || receipt.BindingRevision != expected || agentConfigurationDigest(in) != receipt.ConfigurationDigest {
		return agentmodule.ConfigurationRevision{}, credential.ErrConflict
	}
	runtime, scope, err := a.runtime(ctx, candidate.ConfigurationRevision, receipt.Binding.Provider)
	if err != nil {
		return agentmodule.ConfigurationRevision{}, err
	}
	if err = runtime.UseBoundVersion(ctx, scope, receipt.Binding.VersionID, func(fields map[string]string) error {
		if (input.APIKey != "" || input.RemoveKey) && !secret.Equal(input.APIKey, fields["api_key"]) {
			return credential.ErrConflict
		}
		return nil
	}); err != nil {
		return agentmodule.ConfigurationRevision{}, credential.ErrConflict
	}
	request := credential.ActivationRequest{OperationID: parts[1], ReceiptID: parts[0], VersionID: receipt.Binding.VersionID, ExpectedBindingRevision: expected}
	if existing, readErr := a.repository.GetActivationRequest(ctx, a.config.InstanceID, request.OperationID); readErr == nil {
		if existing.Receipt.Binding != receipt.Binding || existing.Receipt.ActorID != actor || existing.Receipt.BindingID != receipt.BindingID ||
			existing.Receipt.ConfigurationDigest != receipt.ConfigurationDigest || existing.Request.ExpectedBindingRevision != expected {
			return agentmodule.ConfigurationRevision{}, credential.ErrConflict
		}
		request = existing.Request
	} else if !errors.Is(readErr, credential.ErrNotFound) {
		return agentmodule.ConfigurationRevision{}, readErr
	}
	status, err := a.coordinator.StartActivation(ctx, actor, a.resource(), request)
	if err != nil {
		return agentmodule.ConfigurationRevision{}, err
	}
	receiptID := receipt.ReceiptID
	if status.State == "committed" || status.State == "completed" {
		receiptID = ""
	}
	status, err = a.coordinator.RetryActivation(ctx, actor, a.resource(), request.OperationID, receiptID)
	if err != nil {
		return agentmodule.ConfigurationRevision{}, err
	}
	if !status.RuntimeReady {
		return agentmodule.ConfigurationRevision{}, credential.ErrUnavailable
	}
	return a.config.Store.ConfigurationByRevision(ctx, expected+1)
}

// AbortConfiguration resolves the sole pending operation from durable authority;
// callers cannot select another scope or abandon an already committed pointer.
func (a *AgentCredentials) AbortConfiguration(ctx context.Context, actor string) error {
	if a == nil || typednil.IsNil(a.coordinator) {
		return credential.ErrUnavailable
	}
	if err := a.AuthorizeMutation(ctx, actor, a.resource()); err != nil {
		return err
	}
	pending, err := a.repository.GetPendingActivationRequest(ctx, a.config.InstanceID)
	if err != nil {
		return err
	}
	if pending.Resource() != a.resource() || pending.Receipt.ActorID != actor {
		return credential.ErrConflict
	}
	_, err = a.coordinator.AbortActivation(ctx, actor, a.resource(), pending.Request.OperationID)
	return err
}

func canonicalCredentialValue(value string) bool {
	return value != "" && len(value) <= 255 && utf8.ValidString(value) && strings.TrimSpace(value) == value && strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) < 0
}
