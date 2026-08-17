package environment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
)

const defaultMaxBundleSize = 64 << 10

type Config struct {
	Selection        connectionbinding.ResolverSelection
	AllowedVariables []string
	LookupEnv        func(string) (string, bool)
	Now              func() time.Time
	TTL              time.Duration
	MaxBundleSize    int
}

type Resolver struct {
	selection connectionbinding.ResolverSelection
	allowed   map[string]struct{}
	lookup    func(string) (string, bool)
	now       func() time.Time
	ttl       time.Duration
	maxSize   int
}

var _ connectionbinding.CredentialResolver = (*Resolver)(nil)
var _ connectionbinding.VersionedCredentialResolver = (*Resolver)(nil)

func NewResolver(config Config) (*Resolver, error) {
	selection, err := connectionbinding.NewResolverSelection(connectionbinding.ResolverSelectionInput(config.Selection))
	if err != nil {
		return nil, err
	}
	if selection.Kind != connectionbinding.ResolverEnvironment || selection.TargetClass != connectionbinding.TargetDevelopment {
		return nil, fmt.Errorf("%w: environment credentials require an explicit development target selection", connectionbinding.ErrInvalidBinding)
	}
	if config.LookupEnv == nil || config.Now == nil || config.TTL <= 0 || len(config.AllowedVariables) == 0 {
		return nil, fmt.Errorf("%w: environment lookup, clock, TTL, and allowlist are required", connectionbinding.ErrInvalidBinding)
	}
	if config.MaxBundleSize == 0 {
		config.MaxBundleSize = defaultMaxBundleSize
	}
	if config.MaxBundleSize <= 0 || config.MaxBundleSize > 1<<20 {
		return nil, fmt.Errorf("%w: environment credential bundle size is invalid", connectionbinding.ErrInvalidBinding)
	}
	allowed := make(map[string]struct{}, len(config.AllowedVariables))
	for _, variable := range config.AllowedVariables {
		variable = strings.TrimSpace(variable)
		if variable == "" {
			return nil, fmt.Errorf("%w: environment credential variable is invalid", connectionbinding.ErrInvalidBinding)
		}
		allowed[variable] = struct{}{}
	}
	return &Resolver{
		selection: selection, allowed: allowed, lookup: config.LookupEnv,
		now: config.Now, ttl: config.TTL, maxSize: config.MaxBundleSize,
	}, nil
}

func (resolver *Resolver) Resolve(_ context.Context, reference connectionbinding.CredentialReference) (connectionbinding.CredentialSnapshot, error) {
	if resolver == nil {
		return connectionbinding.CredentialSnapshot{}, connectionbinding.ErrProviderUnavailable
	}
	if reference.ProjectID != resolver.selection.ProjectID ||
		reference.Environment != resolver.selection.Environment ||
		reference.SecretPath != "/" {
		return connectionbinding.CredentialSnapshot{}, connectionbinding.ErrCredentialDenied
	}
	if _, ok := resolver.allowed[reference.SecretKey]; !ok {
		return connectionbinding.CredentialSnapshot{}, connectionbinding.ErrCredentialDenied
	}
	raw, ok := resolver.lookup(reference.SecretKey)
	if !ok {
		return connectionbinding.CredentialSnapshot{}, connectionbinding.ErrCredentialNotFound
	}
	if len(raw) == 0 || len(raw) > resolver.maxSize {
		return connectionbinding.CredentialSnapshot{}, connectionbinding.ErrInvalidCredentialBundle
	}
	var bundle map[string]string
	if err := json.Unmarshal([]byte(raw), &bundle); err != nil {
		return connectionbinding.CredentialSnapshot{}, connectionbinding.ErrInvalidCredentialBundle
	}
	digest := sha256.Sum256([]byte(raw))
	now := resolver.now().UTC()
	snapshot, err := connectionbinding.NewCredentialSnapshot(
		bundle, "env:sha256:"+hex.EncodeToString(digest[:]), now, now.Add(resolver.ttl),
	)
	if err != nil {
		return connectionbinding.CredentialSnapshot{}, connectionbinding.ErrInvalidCredentialBundle
	}
	return snapshot, nil
}

func (resolver *Resolver) ResolveVersion(
	ctx context.Context,
	reference connectionbinding.CredentialReference,
	providerVersion string,
) (connectionbinding.CredentialSnapshot, error) {
	snapshot, err := resolver.Resolve(ctx, reference)
	if err != nil {
		return connectionbinding.CredentialSnapshot{}, err
	}
	if snapshot.ProviderVersion() != strings.TrimSpace(providerVersion) {
		snapshot.Destroy()
		return connectionbinding.CredentialSnapshot{}, connectionbinding.ErrCredentialNotFound
	}
	return snapshot, nil
}
