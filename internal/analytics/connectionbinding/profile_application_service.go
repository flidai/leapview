package connectionbinding

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type ProfileApplicationMode string

const (
	ProfileApplicationNew     ProfileApplicationMode = "new"
	ProfileApplicationResume  ProfileApplicationMode = "resume"
	ProfileApplicationReplace ProfileApplicationMode = "replace"
)

type ProfileApplicationBinding struct {
	ConnectionID        projectgraph.ResourceID
	ConnectorKind       string
	AuthenticationMode  AuthenticationMode
	Endpoint            EndpointConfig
	CredentialReference CredentialReference
}

type ProfileApplicationRequest struct {
	Mode          ProfileApplicationMode
	ApplicationID ProfileApplicationID
	CheckoutID    string
	RuntimeID     string
	TargetID      TargetID
	ProjectID     projectgraph.ResourceID
	Environment   string
	ProfileName   string
	SourceDigest  string
	GraphDigest   string
	ProfileDigest string
	ActorID       string
	Connections   []ProfileApplicationBinding
}

type ProfileApplicationBindingAdministration interface {
	List(context.Context, string, BindingScope, TargetID) ([]TargetBinding, error)
	Get(context.Context, string, BindingKey) (TargetBinding, error)
	Create(context.Context, string, TargetBindingInput) (TargetBinding, error)
	PlanConfigurationChange(context.Context, string, BindingKey, TargetBindingConfiguration) (BindingChangePlan, error)
	UpdateConfiguration(context.Context, UpdateConfigurationRequest) (TargetBinding, error)
	Test(context.Context, string, BindingKey) (BindingHealthStatus, error)
	Enable(context.Context, string, BindingKey) (TargetBinding, error)
	Disable(context.Context, string, BindingKey) (TargetBinding, error)
}

type ProfileApplicationServiceConfig struct {
	Store                ProfileApplicationStore
	Bindings             ProfileApplicationBindingAdministration
	Resolver             CredentialResolver
	AuthorizeReplacement func(context.Context, ProfileApplicationScope, TargetID) error
	NewBindingID         func() (BindingID, error)
	Now                  func() time.Time
}

type ProfileApplicationService struct {
	store                ProfileApplicationStore
	bindings             ProfileApplicationBindingAdministration
	resolver             CredentialResolver
	authorizeReplacement func(context.Context, ProfileApplicationScope, TargetID) error
	newBindingID         func() (BindingID, error)
	now                  func() time.Time
}

const profileApplicationRecoveryTimeout = 5 * time.Second

func NewProfileApplicationService(config ProfileApplicationServiceConfig) (*ProfileApplicationService, error) {
	if config.Store == nil || config.Bindings == nil || config.NewBindingID == nil || config.Now == nil {
		return nil, ErrInvalidProfileApplication
	}
	return &ProfileApplicationService{store: config.Store, bindings: config.Bindings, resolver: config.Resolver, authorizeReplacement: config.AuthorizeReplacement, newBindingID: config.NewBindingID, now: config.Now}, nil
}

func (service *ProfileApplicationService) Apply(ctx context.Context, request ProfileApplicationRequest) (result ProfileApplicationRecord, err error) {
	if service == nil || ctx == nil {
		return ProfileApplicationRecord{}, ErrInvalidProfileApplication
	}
	if err := ctx.Err(); err != nil {
		return ProfileApplicationRecord{}, err
	}
	if !validDigest(request.SourceDigest) || !validDigest(request.GraphDigest) || !validDigest(request.ProfileDigest) {
		return ProfileApplicationRecord{}, ErrInvalidProfileApplication
	}
	scope := ProfileApplicationScope{CheckoutID: request.CheckoutID, RuntimeID: request.RuntimeID, ProjectID: request.ProjectID, Environment: request.Environment}
	current, loadErr := service.store.Application(ctx, scope, request.TargetID)
	exists := loadErr == nil
	if loadErr != nil && !errors.Is(loadErr, ErrProfileApplicationNotFound) {
		return ProfileApplicationRecord{}, loadErr
	}

	switch request.Mode {
	case ProfileApplicationNew:
		if exists {
			return ProfileApplicationRecord{}, ErrProfileApplicationReplacement
		}
		result, err = service.newIntent(ctx, request)
		if err == nil {
			result, err = service.store.Save(ctx, result, 0)
		}
	case ProfileApplicationResume:
		if !exists {
			return ProfileApplicationRecord{}, ErrProfileApplicationNotFound
		}
		if err = service.validateResume(ctx, current, request); err != nil {
			return ProfileApplicationRecord{}, err
		}
		if current.Status == ProfileApplicationApplied {
			return current, nil
		}
		result = current
		result.Status = ProfileApplicationApplying
		result.UpdatedAt = service.now().UTC()
		result, err = service.store.Save(ctx, result, current.Revision)
	case ProfileApplicationReplace:
		if !exists {
			return ProfileApplicationRecord{}, ErrProfileApplicationNotFound
		}
		if service.authorizeReplacement == nil {
			return ProfileApplicationRecord{}, ErrProfileApplicationReplacement
		}
		if err = service.authorizeReplacement(ctx, scope, request.TargetID); err != nil {
			return ProfileApplicationRecord{}, errors.Join(ErrProfileApplicationReplacement, err)
		}
		result, err = service.newIntent(ctx, request)
		if err == nil {
			if result.ID == current.ID {
				err = ErrProfileApplicationReplacement
			} else {
				result, err = service.store.Replace(ctx, result, current.Revision)
			}
		}
	default:
		return ProfileApplicationRecord{}, ErrInvalidProfileApplication
	}
	if err != nil {
		return ProfileApplicationRecord{}, err
	}

	defer func() {
		if err == nil {
			return
		}
		result.Status = ProfileApplicationIncomplete
		result.UpdatedAt = service.now().UTC()
		recoveryContext, cancelRecovery := context.WithTimeout(context.WithoutCancel(ctx), profileApplicationRecoveryTimeout)
		defer cancelRecovery()
		if saved, saveErr := service.store.Save(recoveryContext, result, result.Revision); saveErr == nil {
			result = saved
		} else {
			err = errors.Join(err, saveErr)
		}
	}()

	for _, retired := range result.RetiredConnections {
		binding, getErr := service.bindings.Get(ctx, request.ActorID, profileBindingKey(result, retired.ConnectionID))
		if errors.Is(getErr, ErrBindingNotFound) {
			return result, ErrIncompatibleBinding
		}
		if getErr != nil {
			return result, getErr
		}
		if binding.ID != retired.BindingID || binding.Revision < retired.BindingRevision || !sameRetiredBindingIntent(retired, binding) {
			return result, ErrIncompatibleBinding
		}
		if binding.Enabled {
			if _, err = service.bindings.Disable(ctx, request.ActorID, profileBindingKey(result, retired.ConnectionID)); err != nil {
				return result, err
			}
		}
	}

	result.AppliedConnections = nil
	for _, expected := range result.ExpectedConnections {
		applied, applyErr := service.applyConnection(ctx, request.ActorID, result, expected)
		if applyErr != nil {
			return result, applyErr
		}
		result.AppliedConnections = append(result.AppliedConnections, applied)
		result.UpdatedAt = service.now().UTC()
		result, err = service.store.Save(ctx, result, result.Revision)
		if err != nil {
			return result, err
		}
	}
	result.Status = ProfileApplicationApplied
	result.UpdatedAt = service.now().UTC()
	result, err = service.store.Save(ctx, result, result.Revision)
	return result, err
}

func (service *ProfileApplicationService) newIntent(ctx context.Context, request ProfileApplicationRequest) (ProfileApplicationRecord, error) {
	now := service.now().UTC()
	if request.Mode == "" || request.ApplicationID == "" || request.ActorID == "" || now.IsZero() {
		return ProfileApplicationRecord{}, ErrInvalidProfileApplication
	}
	scope := BindingScope{ProjectID: request.ProjectID, Environment: request.Environment}
	existing, err := service.bindings.List(ctx, request.ActorID, scope, request.TargetID)
	if err != nil {
		return ProfileApplicationRecord{}, err
	}
	byConnection := make(map[projectgraph.ResourceID]TargetBinding, len(existing))
	for _, binding := range existing {
		if _, duplicate := byConnection[binding.ConnectionID]; duplicate {
			return ProfileApplicationRecord{}, ErrInvalidProfileApplication
		}
		byConnection[binding.ConnectionID] = binding
	}
	desired := append([]ProfileApplicationBinding(nil), request.Connections...)
	sort.Slice(desired, func(i, j int) bool { return desired[i].ConnectionID < desired[j].ConnectionID })
	record := ProfileApplicationRecord{
		ID: request.ApplicationID, CheckoutID: request.CheckoutID, RuntimeID: request.RuntimeID,
		TargetID: request.TargetID, ProjectID: request.ProjectID, Environment: request.Environment,
		ProfileName: request.ProfileName, SourceDigest: request.SourceDigest, GraphDigest: request.GraphDigest, ProfileDigest: request.ProfileDigest,
		Status: ProfileApplicationApplying, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	selected := make(map[projectgraph.ResourceID]struct{}, len(desired))
	for _, input := range desired {
		if _, duplicate := selected[input.ConnectionID]; duplicate {
			return ProfileApplicationRecord{}, ErrInvalidProfileApplication
		}
		selected[input.ConnectionID] = struct{}{}
		bindingID, revision := BindingID(""), int64(0)
		if retained, ok := byConnection[input.ConnectionID]; ok {
			bindingID, revision = retained.ID, retained.Revision
		} else {
			bindingID, err = service.newBindingID()
			if err != nil {
				return ProfileApplicationRecord{}, err
			}
		}
		configuration := TargetBindingConfiguration{ConnectorKind: input.ConnectorKind, AuthenticationMode: input.AuthenticationMode, Endpoint: cloneEndpoint(input.Endpoint), CredentialReference: input.CredentialReference}
		if _, err := NewTargetBinding(TargetBindingInput{ID: bindingID, TargetID: request.TargetID, ConnectionID: input.ConnectionID, ConnectorKind: configuration.ConnectorKind, AuthenticationMode: configuration.AuthenticationMode, Scope: scope, Endpoint: configuration.Endpoint, CredentialReference: configuration.CredentialReference, Enabled: true, Now: now}); err != nil {
			return ProfileApplicationRecord{}, ErrInvalidProfileApplication
		}
		version, err := service.expectedProviderVersion(ctx, configuration)
		if err != nil {
			return ProfileApplicationRecord{}, err
		}
		record.RequiredConnections = append(record.RequiredConnections, ProfileApplicationRequiredConnection{ConnectionID: input.ConnectionID, ConnectorKind: input.ConnectorKind})
		record.ExpectedConnections = append(record.ExpectedConnections, profileConnectionFromConfiguration(bindingID, input.ConnectionID, revision, version, configuration))
	}
	for _, binding := range existing {
		if _, keep := selected[binding.ConnectionID]; keep || !binding.Enabled {
			continue
		}
		record.RetiredConnections = append(record.RetiredConnections, profileConnectionFromBinding(binding))
	}
	return NewProfileApplication(record)
}

func (service *ProfileApplicationService) validateResume(ctx context.Context, record ProfileApplicationRecord, request ProfileApplicationRequest) error {
	if request.ApplicationID != record.ID || request.CheckoutID != record.CheckoutID || request.RuntimeID != record.RuntimeID || request.TargetID != record.TargetID || request.ProjectID != record.ProjectID || request.Environment != record.Environment || request.ProfileName != record.ProfileName || request.GraphDigest != record.GraphDigest || request.ProfileDigest != record.ProfileDigest || len(request.Connections) != len(record.ExpectedConnections) {
		return ErrProfileApplicationReplacement
	}
	byID := make(map[projectgraph.ResourceID]ProfileApplicationConnection, len(record.ExpectedConnections))
	for _, expected := range record.ExpectedConnections {
		byID[expected.ConnectionID] = expected
	}
	for _, input := range request.Connections {
		expected, ok := byID[input.ConnectionID]
		configuration := TargetBindingConfiguration{ConnectorKind: input.ConnectorKind, AuthenticationMode: input.AuthenticationMode, Endpoint: cloneEndpoint(input.Endpoint), CredentialReference: input.CredentialReference}
		if !ok || !sameProfileApplicationConnection(expected, profileConnectionFromConfiguration(expected.BindingID, input.ConnectionID, expected.BindingRevision, expected.ProviderVersion, configuration), true) {
			return ErrProfileApplicationReplacement
		}
		version, err := service.expectedProviderVersion(ctx, configuration)
		if err != nil || version != expected.ProviderVersion {
			return ErrProfileApplicationReplacement
		}
	}
	return nil
}

func (service *ProfileApplicationService) expectedProviderVersion(ctx context.Context, configuration TargetBindingConfiguration) (string, error) {
	switch configuration.AuthenticationMode {
	case AuthenticationNone:
		return NoAuthProviderVersion, nil
	case AuthenticationExternalBundle:
		if service.resolver == nil {
			return "", ErrProviderUnavailable
		}
		snapshot, err := service.resolver.Resolve(ctx, configuration.CredentialReference)
		if err != nil {
			return "", err
		}
		defer snapshot.Destroy()
		return snapshot.ProviderVersion(), nil
	default:
		return "", ErrInvalidProfileApplication
	}
}

func (service *ProfileApplicationService) applyConnection(ctx context.Context, actor string, application ProfileApplicationRecord, expected ProfileApplicationConnection) (ProfileApplicationConnection, error) {
	key := profileBindingKey(application, expected.ConnectionID)
	configuration := expected.Configuration()
	binding, err := service.bindings.Get(ctx, actor, key)
	if errors.Is(err, ErrBindingNotFound) {
		if expected.BindingRevision != 0 {
			return ProfileApplicationConnection{}, ErrIncompatibleBinding
		}
		binding, err = service.bindings.Create(ctx, actor, TargetBindingInput{
			ID: expected.BindingID, TargetID: application.TargetID, ConnectionID: expected.ConnectionID,
			ConnectorKind: expected.ConnectorKind, AuthenticationMode: expected.AuthenticationMode,
			Scope: key.Scope, Endpoint: cloneEndpoint(expected.Endpoint), CredentialReference: expected.CredentialReference,
			Enabled: true,
		})
	}
	if err != nil {
		return ProfileApplicationConnection{}, err
	}
	if binding.ID != expected.BindingID || binding.Revision < expected.BindingRevision {
		return ProfileApplicationConnection{}, ErrIncompatibleBinding
	}
	if !sameBindingConfiguration(binding.Configuration(), configuration) {
		if binding.Revision != expected.BindingRevision {
			return ProfileApplicationConnection{}, ErrIncompatibleBinding
		}
		plan, planErr := service.bindings.PlanConfigurationChange(ctx, actor, key, configuration)
		if planErr != nil || plan.ExpectedRevision != binding.Revision {
			return ProfileApplicationConnection{}, errors.Join(ErrIncompatibleBinding, planErr)
		}
		binding, err = service.bindings.UpdateConfiguration(ctx, UpdateConfigurationRequest{ActorID: actor, Key: key, Configuration: configuration, ExpectedRevision: plan.ExpectedRevision, ConfirmationToken: plan.ConfirmationToken})
		if err != nil {
			return ProfileApplicationConnection{}, err
		}
	}
	if !binding.Enabled {
		binding, err = service.bindings.Enable(ctx, actor, key)
		if err != nil {
			return ProfileApplicationConnection{}, err
		}
	}
	health, err := service.bindings.Test(ctx, actor, key)
	if err != nil {
		return ProfileApplicationConnection{}, err
	}
	if health.Health != HealthHealthy || !health.HasActivePool || health.BindingID != expected.BindingID || health.ConnectionID != expected.ConnectionID || health.ConnectorKind != expected.ConnectorKind || health.ValidatedVersion != expected.ProviderVersion || health.BindingRevision < max(1, expected.BindingRevision) {
		return ProfileApplicationConnection{}, ErrIncompatibleBinding
	}
	applied := expected
	applied.BindingRevision = health.BindingRevision
	return applied, nil
}

func profileBindingKey(application ProfileApplicationRecord, connectionID projectgraph.ResourceID) BindingKey {
	return BindingKey{Scope: BindingScope{ProjectID: application.ProjectID, Environment: application.Environment}, TargetID: application.TargetID, ConnectionID: connectionID}
}

func profileConnectionFromConfiguration(bindingID BindingID, connectionID projectgraph.ResourceID, revision int64, version string, configuration TargetBindingConfiguration) ProfileApplicationConnection {
	return ProfileApplicationConnection{BindingID: bindingID, ConnectionID: connectionID, ConnectorKind: configuration.ConnectorKind, AuthenticationMode: configuration.AuthenticationMode, Endpoint: cloneEndpoint(configuration.Endpoint), CredentialReference: configuration.CredentialReference, BindingRevision: revision, ProviderVersion: version}
}

func profileConnectionFromBinding(binding TargetBinding) ProfileApplicationConnection {
	return profileConnectionFromConfiguration(binding.ID, binding.ConnectionID, binding.Revision, binding.ValidatedVersion, binding.Configuration())
}

func (connection ProfileApplicationConnection) Configuration() TargetBindingConfiguration {
	return TargetBindingConfiguration{ConnectorKind: connection.ConnectorKind, AuthenticationMode: connection.AuthenticationMode, Endpoint: cloneEndpoint(connection.Endpoint), CredentialReference: connection.CredentialReference}
}

func sameBindingConfiguration(left, right TargetBindingConfiguration) bool {
	return left.ConnectorKind == right.ConnectorKind && left.AuthenticationMode == right.AuthenticationMode && left.CredentialReference == right.CredentialReference && sameEndpoint(left.Endpoint, right.Endpoint)
}

func sameEndpoint(left, right EndpointConfig) bool {
	if left.Host != right.Host || left.Port != right.Port || left.Database != right.Database || left.ObjectScope != right.ObjectScope || left.SourceIdentity != right.SourceIdentity || left.TLSMode != right.TLSMode || len(left.Options) != len(right.Options) {
		return false
	}
	for key, value := range left.Options {
		if right.Options[key] != value {
			return false
		}
	}
	return true
}

func sameRetiredBindingIntent(expected ProfileApplicationConnection, binding TargetBinding) bool {
	actual := profileConnectionFromBinding(binding)
	return sameProfileApplicationConnection(expected, actual, false)
}

func (request ProfileApplicationRequest) String() string {
	return fmt.Sprintf("profile application request mode=%s scope=checkout=%s runtime=%s project=%s environment=%s target=%s", request.Mode, request.CheckoutID, request.RuntimeID, request.ProjectID, request.Environment, request.TargetID)
}

func (request ProfileApplicationRequest) GoString() string { return "<profile-application-request>" }
