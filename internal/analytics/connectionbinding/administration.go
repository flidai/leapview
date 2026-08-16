package connectionbinding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"log/slog"
	"sort"
	"strings"
	"time"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	platformsecret "github.com/flidai/leapview/internal/platform/security/secret"
)

var ErrConfirmationRequired = apigenfailure.New("confirmation_required", "connection binding change confirmation required")

type AdministrationPermission string

const (
	PermissionManageConnectionMetadata AdministrationPermission = "connection.metadata.manage"
	PermissionTestConnection           AdministrationPermission = "connection.test"
	PermissionViewConnectionHealth     AdministrationPermission = "connection.health.view"
)

type BindingKey struct {
	Scope        BindingScope
	TargetID     TargetID
	ConnectionID projectgraph.ResourceID
}

type BindingDependency struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	Label string `json:"label"`
}

type DependencyInspector interface {
	Dependents(context.Context, TargetBinding) ([]BindingDependency, error)
}

type AdministrationAuthorizer func(context.Context, string, AdministrationPermission, TargetBinding) error

type AdministrationPool interface {
	Refresh(context.Context, RefreshRequest) error
	Disable(context.Context, time.Time) error
	HealthStatus() BindingHealthStatus
}

type AdministrationPoolDirectory interface {
	Pool(TargetBinding) (AdministrationPool, error)
}

type BindingCatalog interface {
	Repository
	List(context.Context, BindingScope, TargetID) ([]TargetBinding, error)
}

type AdministrationConfig struct {
	Repository   BindingCatalog
	EnsureScope  func(context.Context, BindingScope) error
	Authorize    AdministrationAuthorizer
	Dependencies DependencyInspector
	Pools        AdministrationPoolDirectory
	Audit        AdministrationAuditRecorder
	Logger       *slog.Logger
	Now          func() time.Time
}

type Administration struct {
	repository   BindingCatalog
	ensureScope  func(context.Context, BindingScope) error
	authorize    AdministrationAuthorizer
	dependencies DependencyInspector
	pools        AdministrationPoolDirectory
	audit        AdministrationAuditRecorder
	logger       *slog.Logger
	now          func() time.Time
}

func NewAdministration(config AdministrationConfig) (*Administration, error) {
	if config.Repository == nil || config.Authorize == nil || config.Dependencies == nil || config.Now == nil {
		return nil, fmt.Errorf("%w: binding repository, authorizer, dependency inspector, and clock are required", ErrInvalidBinding)
	}
	if config.Audit == nil {
		return nil, fmt.Errorf("%w: recorder is required", ErrAdministrationAuditUnavailable)
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Administration{
		repository: config.Repository, authorize: config.Authorize,
		ensureScope:  config.EnsureScope,
		dependencies: config.Dependencies, pools: config.Pools, audit: config.Audit, logger: logger, now: config.Now,
	}, nil
}

type BindingChangePlan struct {
	BindingID            BindingID           `json:"bindingId"`
	ExpectedRevision     int64               `json:"expectedRevision"`
	RequiresConfirmation bool                `json:"requiresConfirmation"`
	ConfirmationToken    string              `json:"confirmationToken,omitempty"`
	Dependencies         []BindingDependency `json:"dependencies,omitempty"`
}

func (service *Administration) Create(
	ctx context.Context,
	actorID string,
	input TargetBindingInput,
) (TargetBinding, error) {
	if service == nil {
		return TargetBinding{}, ErrProviderUnavailable
	}
	input.Now = service.now().UTC()
	binding, err := NewTargetBinding(input)
	if err != nil {
		return TargetBinding{}, err
	}
	if err := service.authorize(
		ctx, strings.TrimSpace(actorID), PermissionManageConnectionMetadata, binding,
	); err != nil {
		return TargetBinding{}, ErrUnauthorizedBinding
	}
	if service.ensureScope != nil {
		if err := service.ensureScope(ctx, binding.Scope); err != nil {
			return TargetBinding{}, err
		}
	}
	if err := service.repository.Create(ctx, binding); err != nil {
		return TargetBinding{}, err
	}
	if err := service.recordMutation(ctx, actorID, AuditBindingCreated, binding); err != nil {
		return binding, err
	}
	return binding, nil
}

func (service *Administration) List(
	ctx context.Context,
	actorID string,
	scope BindingScope,
	targetID TargetID,
) ([]TargetBinding, error) {
	if service == nil {
		return nil, ErrProviderUnavailable
	}
	if _, err := ParseTargetID(targetID.String()); err != nil {
		return nil, err
	}
	authorizationScope := TargetBinding{TargetID: targetID, Scope: scope}
	if err := service.authorize(
		ctx, strings.TrimSpace(actorID), PermissionManageConnectionMetadata, authorizationScope,
	); err != nil {
		return nil, ErrUnauthorizedBinding
	}
	bindings, err := service.repository.List(ctx, scope, targetID)
	if err != nil {
		return nil, err
	}
	sort.Slice(bindings, func(i, j int) bool {
		return bindings[i].ConnectionID < bindings[j].ConnectionID
	})
	return bindings, nil
}

func (service *Administration) Get(
	ctx context.Context,
	actorID string,
	key BindingKey,
) (TargetBinding, error) {
	binding, err := service.binding(ctx, key)
	if err != nil {
		return TargetBinding{}, err
	}
	if err := service.authorize(
		ctx, strings.TrimSpace(actorID), PermissionManageConnectionMetadata, binding,
	); err != nil {
		return TargetBinding{}, ErrUnauthorizedBinding
	}
	return binding, nil
}

func (service *Administration) PlanConfigurationChange(
	ctx context.Context,
	actorID string,
	key BindingKey,
	configuration TargetBindingConfiguration,
) (BindingChangePlan, error) {
	binding, err := service.binding(ctx, key)
	if err != nil {
		return BindingChangePlan{}, err
	}
	if err := service.authorize(ctx, strings.TrimSpace(actorID), PermissionManageConnectionMetadata, binding); err != nil {
		return BindingChangePlan{}, ErrUnauthorizedBinding
	}
	updated, err := binding.UpdateConfiguration(configuration, service.now().UTC())
	if err != nil {
		return BindingChangePlan{}, err
	}
	plan := BindingChangePlan{BindingID: binding.ID, ExpectedRevision: binding.Revision}
	if updated.Revision == binding.Revision {
		return plan, nil
	}
	dependencies, err := service.dependencies.Dependents(ctx, binding)
	if err != nil {
		return BindingChangePlan{}, err
	}
	sort.Slice(dependencies, func(i, j int) bool {
		if dependencies[i].Kind != dependencies[j].Kind {
			return dependencies[i].Kind < dependencies[j].Kind
		}
		return dependencies[i].ID < dependencies[j].ID
	})
	plan.Dependencies = dependencies
	plan.RequiresConfirmation = len(dependencies) > 0
	if plan.RequiresConfirmation {
		plan.ConfirmationToken = changeConfirmationToken(binding, updated.Configuration(), dependencies)
	}
	return plan, nil
}

type UpdateConfigurationRequest struct {
	ActorID           string
	Key               BindingKey
	Configuration     TargetBindingConfiguration
	ExpectedRevision  int64
	ConfirmationToken string
}

func (service *Administration) UpdateConfiguration(
	ctx context.Context,
	request UpdateConfigurationRequest,
) (TargetBinding, error) {
	binding, err := service.binding(ctx, request.Key)
	if err != nil {
		return TargetBinding{}, err
	}
	if request.ExpectedRevision != binding.Revision {
		return TargetBinding{}, ErrIncompatibleBinding
	}
	if err := service.authorize(
		ctx, strings.TrimSpace(request.ActorID), PermissionManageConnectionMetadata, binding,
	); err != nil {
		return TargetBinding{}, ErrUnauthorizedBinding
	}
	updated, err := binding.UpdateConfiguration(request.Configuration, service.now().UTC())
	if err != nil {
		return TargetBinding{}, err
	}
	if updated.Revision == binding.Revision {
		return binding, nil
	}
	dependencies, err := service.dependencies.Dependents(ctx, binding)
	if err != nil {
		return TargetBinding{}, err
	}
	sort.Slice(dependencies, func(i, j int) bool {
		if dependencies[i].Kind != dependencies[j].Kind {
			return dependencies[i].Kind < dependencies[j].Kind
		}
		return dependencies[i].ID < dependencies[j].ID
	})
	if len(dependencies) > 0 {
		expected := changeConfirmationToken(binding, updated.Configuration(), dependencies)
		if !platformsecret.Equal(strings.TrimSpace(request.ConfirmationToken), expected) {
			return TargetBinding{}, ErrConfirmationRequired
		}
	}
	saved, err := service.repository.Save(ctx, updated, binding.Revision)
	if err != nil {
		return TargetBinding{}, err
	}
	if err := service.recordMutation(ctx, request.ActorID, AuditBindingUpdated, saved); err != nil {
		return saved, err
	}
	return saved, nil
}

func (service *Administration) RefreshNow(
	ctx context.Context,
	actorID string,
	key BindingKey,
) (BindingHealthStatus, error) {
	return service.refresh(ctx, actorID, key, RefreshRequested)
}

// Test resolves and validates a fresh credential snapshot through the same
// candidate pool path as rotation. A successful test can therefore promote
// the validated replacement without exposing credentials or accepting SQL.
func (service *Administration) Test(
	ctx context.Context,
	actorID string,
	key BindingKey,
) (BindingHealthStatus, error) {
	return service.refresh(ctx, actorID, key, RefreshTest)
}

func (service *Administration) refresh(
	ctx context.Context,
	actorID string,
	key BindingKey,
	operation RefreshOperation,
) (BindingHealthStatus, error) {
	binding, err := service.binding(ctx, key)
	if err != nil {
		return BindingHealthStatus{}, err
	}
	if err := service.authorize(ctx, strings.TrimSpace(actorID), PermissionTestConnection, binding); err != nil {
		return BindingHealthStatus{}, ErrUnauthorizedBinding
	}
	if service.pools == nil {
		return BindingHealthStatus{}, ErrProviderUnavailable
	}
	pool, err := service.pools.Pool(binding)
	if err != nil {
		return BindingHealthStatus{}, err
	}
	if err := pool.Refresh(ctx, RefreshRequest{
		Actor: "principal:" + strings.TrimSpace(actorID), Operation: operation,
	}); err != nil {
		return pool.HealthStatus(), err
	}
	return pool.HealthStatus(), nil
}

func (service *Administration) Health(
	ctx context.Context,
	actorID string,
	key BindingKey,
) (BindingHealthStatus, error) {
	binding, err := service.binding(ctx, key)
	if err != nil {
		return BindingHealthStatus{}, err
	}
	if err := service.authorize(
		ctx, strings.TrimSpace(actorID), PermissionViewConnectionHealth, binding,
	); err != nil {
		return BindingHealthStatus{}, ErrUnauthorizedBinding
	}
	if service.pools == nil {
		return bindingHealthWithoutPool(binding), nil
	}
	pool, err := service.pools.Pool(binding)
	if errors.Is(err, ErrBindingNotFound) {
		return bindingHealthWithoutPool(binding), nil
	}
	if err != nil {
		return BindingHealthStatus{}, err
	}
	return pool.HealthStatus(), nil
}

func (service *Administration) Disable(
	ctx context.Context,
	actorID string,
	key BindingKey,
) (TargetBinding, error) {
	binding, err := service.binding(ctx, key)
	if err != nil {
		return TargetBinding{}, err
	}
	if err := service.authorize(
		ctx, strings.TrimSpace(actorID), PermissionManageConnectionMetadata, binding,
	); err != nil {
		return TargetBinding{}, ErrUnauthorizedBinding
	}
	now := service.now().UTC()
	if service.pools != nil {
		pool, poolErr := service.pools.Pool(binding)
		if poolErr == nil {
			if err := pool.Disable(ctx, now); err != nil {
				return TargetBinding{}, err
			}
			disabled, err := service.binding(ctx, key)
			if err != nil {
				return TargetBinding{}, err
			}
			if err := service.recordMutation(ctx, actorID, AuditBindingDisabled, disabled); err != nil {
				return disabled, err
			}
			return disabled, nil
		}
		if !errors.Is(poolErr, ErrBindingNotFound) {
			return TargetBinding{}, poolErr
		}
	}
	disabled, err := binding.Disable(now)
	if err != nil || disabled.Revision == binding.Revision {
		return disabled, err
	}
	saved, err := service.repository.Save(ctx, disabled, binding.Revision)
	if err != nil {
		return TargetBinding{}, err
	}
	if err := service.recordMutation(ctx, actorID, AuditBindingDisabled, saved); err != nil {
		return saved, err
	}
	return saved, nil
}

func (service *Administration) Enable(
	ctx context.Context,
	actorID string,
	key BindingKey,
) (TargetBinding, error) {
	binding, err := service.binding(ctx, key)
	if err != nil {
		return TargetBinding{}, err
	}
	if err := service.authorize(
		ctx, strings.TrimSpace(actorID), PermissionManageConnectionMetadata, binding,
	); err != nil {
		return TargetBinding{}, ErrUnauthorizedBinding
	}
	enabled, err := binding.Enable(service.now().UTC())
	if err != nil || enabled.Revision == binding.Revision {
		return enabled, err
	}
	saved, err := service.repository.Save(ctx, enabled, binding.Revision)
	if err != nil {
		return TargetBinding{}, err
	}
	if err := service.recordMutation(ctx, actorID, AuditBindingEnabled, saved); err != nil {
		return saved, err
	}
	return saved, nil
}

func (service *Administration) binding(ctx context.Context, key BindingKey) (TargetBinding, error) {
	if service == nil {
		return TargetBinding{}, ErrProviderUnavailable
	}
	return service.repository.Binding(ctx, key.Scope, key.TargetID, key.ConnectionID)
}

func bindingHealthWithoutPool(binding TargetBinding) BindingHealthStatus {
	return BindingHealthStatus{
		BindingID: binding.ID, TargetID: binding.TargetID,
		ConnectionID: binding.ConnectionID, ConnectorKind: binding.ConnectorKind,
		Scope: binding.Scope, BindingRevision: binding.Revision,
		ValidatedVersion: binding.ValidatedVersion, Health: binding.Health,
		DiagnosticCode: binding.HealthReason, LastValidatedAt: binding.LastValidatedAt,
	}
}

func changeConfirmationToken(
	binding TargetBinding,
	configuration TargetBindingConfiguration,
	dependencies []BindingDependency,
) string {
	payload := struct {
		BindingID     BindingID
		Revision      int64
		Configuration TargetBindingConfiguration
		Dependencies  []BindingDependency
	}{
		BindingID: binding.ID, Revision: binding.Revision,
		Configuration: configuration, Dependencies: dependencies,
	}
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}
