package environment

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
)

const defaultMaxBundleSize = 64 << 10

const developmentCredentialEncodingPrefix = "leapview-base64-v1:"

type Config struct {
	Selection        connectionbinding.ResolverSelection
	AllowedVariables []string
	// VersionKey protects the exact credential-bundle comparison token. Raw
	// secret hashes are never persisted or returned as provider versions.
	VersionKey    []byte
	LookupEnv     func(string) (string, bool)
	Now           func() time.Time
	TTL           time.Duration
	MaxBundleSize int
}

type Resolver struct {
	selection  connectionbinding.ResolverSelection
	allowed    map[string]struct{}
	versionKey []byte
	lookup     func(string) (string, bool)
	now        func() time.Time
	ttl        time.Duration
	maxSize    int
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
	if config.LookupEnv == nil || config.Now == nil || config.TTL <= 0 || len(config.AllowedVariables) == 0 || len(config.VersionKey) < 32 {
		return nil, fmt.Errorf("%w: environment lookup, clock, TTL, allowlist, and protected version key are required", connectionbinding.ErrInvalidBinding)
	}
	if config.MaxBundleSize == 0 {
		config.MaxBundleSize = defaultMaxBundleSize
	}
	if config.MaxBundleSize <= 0 || config.MaxBundleSize > 1<<20 {
		return nil, fmt.Errorf("%w: environment credential bundle size is invalid", connectionbinding.ErrInvalidBinding)
	}
	allowed := make(map[string]struct{}, len(config.AllowedVariables))
	for _, variable := range config.AllowedVariables {
		if !validDevelopmentCredentialVariable(variable) {
			return nil, fmt.Errorf("%w: environment credential variable is invalid", connectionbinding.ErrInvalidBinding)
		}
		if _, duplicate := allowed[variable]; duplicate {
			return nil, fmt.Errorf("%w: environment credential variable is duplicated", connectionbinding.ErrInvalidBinding)
		}
		allowed[variable] = struct{}{}
	}
	return &Resolver{
		selection: selection, allowed: allowed, versionKey: append([]byte(nil), config.VersionKey...), lookup: config.LookupEnv,
		now: config.Now, ttl: config.TTL, maxSize: config.MaxBundleSize,
	}, nil
}

func validDevelopmentCredentialVariable(variable string) bool {
	const prefix = "LEAPVIEW_DEV_CONNECTION_"
	if variable == "" || variable != strings.TrimSpace(variable) || !strings.HasPrefix(variable, prefix) || len(variable) == len(prefix) {
		return false
	}
	for _, char := range strings.TrimPrefix(variable, prefix) {
		if (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' {
			return false
		}
	}
	return true
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
	raw, err := decodeDevelopmentCredentialTransport(raw, resolver.maxSize)
	if err != nil {
		return connectionbinding.CredentialSnapshot{}, connectionbinding.ErrInvalidCredentialBundle
	}
	bundle, err := decodeCredentialBundle(raw, resolver.maxSize)
	if err != nil {
		return connectionbinding.CredentialSnapshot{}, connectionbinding.ErrInvalidCredentialBundle
	}
	digest := hmac.New(sha256.New, resolver.versionKey)
	_, _ = digest.Write([]byte(raw))
	providerVersion := "env:v1:" + base64.RawURLEncoding.EncodeToString(digest.Sum(nil))
	now := resolver.now().UTC()
	snapshot, err := connectionbinding.NewCredentialSnapshot(
		bundle, providerVersion, now, now.Add(resolver.ttl),
	)
	if err != nil {
		return connectionbinding.CredentialSnapshot{}, connectionbinding.ErrInvalidCredentialBundle
	}
	return snapshot, nil
}

func decodeDevelopmentCredentialTransport(raw string, maxSize int) (string, error) {
	if !strings.HasPrefix(raw, developmentCredentialEncodingPrefix) {
		if len(raw) == 0 || len(raw) > maxSize {
			return "", connectionbinding.ErrInvalidCredentialBundle
		}
		return raw, nil
	}
	encoded := strings.TrimPrefix(raw, developmentCredentialEncodingPrefix)
	if encoded == "" || len(encoded) > base64.RawStdEncoding.EncodedLen(maxSize) {
		return "", connectionbinding.ErrInvalidCredentialBundle
	}
	decoded, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil || len(decoded) == 0 || len(decoded) > maxSize {
		return "", connectionbinding.ErrInvalidCredentialBundle
	}
	return string(decoded), nil
}

// DecodeCredentialBundleTransport unwraps the private Compose-safe local
// credential encoding. Plain JSON remains accepted for contributor workflows.
// It never returns partially decoded or oversized data.
func DecodeCredentialBundleTransport(raw string) (string, error) {
	return decodeDevelopmentCredentialTransport(raw, defaultMaxBundleSize)
}

// ValidateCredentialBundle performs the same bounded, duplicate-rejecting
// structural preflight as runtime resolution without returning secret data.
func ValidateCredentialBundle(raw string) error {
	_, err := decodeCredentialBundle(raw, defaultMaxBundleSize)
	return err
}

func decodeCredentialBundle(raw string, maxSize int) (map[string]string, error) {
	if len(raw) == 0 || len(raw) > maxSize {
		return nil, connectionbinding.ErrInvalidCredentialBundle
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, connectionbinding.ErrInvalidCredentialBundle
	}
	bundle := map[string]string{}
	for decoder.More() {
		nameToken, tokenErr := decoder.Token()
		name, ok := nameToken.(string)
		if tokenErr != nil || !ok {
			return nil, connectionbinding.ErrInvalidCredentialBundle
		}
		if _, duplicate := bundle[name]; duplicate {
			return nil, connectionbinding.ErrInvalidCredentialBundle
		}
		var value string
		if err := decoder.Decode(&value); err != nil {
			return nil, connectionbinding.ErrInvalidCredentialBundle
		}
		bundle[name] = value
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, connectionbinding.ErrInvalidCredentialBundle
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, connectionbinding.ErrInvalidCredentialBundle
	}
	// Reuse the canonical snapshot validation for key/value policy while the
	// throwaway provider identity remains protected from callers.
	snapshot, err := connectionbinding.NewCredentialSnapshot(bundle, "preflight", time.Unix(1, 0).UTC(), time.Time{})
	if err != nil {
		return nil, connectionbinding.ErrInvalidCredentialBundle
	}
	snapshot.Destroy()
	return bundle, nil
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
