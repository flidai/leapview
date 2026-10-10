package managedrecovery

import (
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/analytics/ducklake/metadata"
	"github.com/flidai/leapview/internal/app/providerrestore"
	"github.com/flidai/leapview/internal/platform/compatibility"
	"github.com/flidai/leapview/internal/platform/typednil"
	"github.com/flidai/leapview/internal/recoveryset"
	"github.com/flidai/leapview/internal/refresh/recovery"
)

// ManagedConfig supplies explicit retained provider inputs. The RecoverySet
// and occurrence are read from their durable authorities, never accepted as
// operator-provided substitutes. Normal publication authority is unchanged.
type ManagedConfig struct {
	PostgresProvider string
	Enrollment       ManagedEnrollmentReceipt
	RecoverySetID    string
	OccurrenceID     string
	InstanceHome     string
	Artifact         compatibility.ReleaseIdentity
	Credentials      ManagedCredentials
	Roles            RuntimeRoles
	Postgres         PGBackRestConfig
	Readback         PGNativeReadbackConfig
	Roots            []ResticConfig
	Closure          metadata.NativeSnapshotClosureEvidence
	PrimaryFence     providerrestore.PrimaryFenceSSHConfig
	EvidenceRoot     string
	SecretRoot       string
}

type ManagedAuthorities struct {
	AuthoritySystemIdentifier string
	Ledger                    recovery.Repository
	Sets                      providerrestore.RecoverySetAuthority
}

// NewManaged composes concrete pgBackRest, Restic, native TLS readback,
// retained credentials and the enrolled original-host fence over the existing
// RecoverySet coordinator. It supplies no generic provider fallback.
func NewManaged(ctx context.Context, config ManagedConfig, authority ManagedAuthorities) (*providerrestore.Coordinator, error) {
	composition, err := prepareManagedComposition(ctx, config, authority)
	if err != nil {
		return nil, err
	}
	return providerrestore.NewManaged(composition.dependencies, composition.fence)
}

type managedComposition struct {
	dependencies providerrestore.Dependencies
	fence        *providerrestore.SSHPrimaryFence
	components   *managedComponents
}

func prepareManagedComposition(ctx context.Context, config ManagedConfig, authority ManagedAuthorities) (*managedComposition, error) {
	if typednil.IsNil(authority.Ledger) || typednil.IsNil(authority.Sets) || config.RecoverySetID == "" || config.OccurrenceID == "" || !pinnedProgram(config.PrimaryFence.SSH) {
		return nil, errors.New("exact managed recovery authorities and pinned original-host trust required")
	}
	if !filepath.IsAbs(config.InstanceHome) || filepath.Clean(config.InstanceHome) != config.InstanceHome || config.InstanceHome == "/" || validatePrivateSecretRoot(config.InstanceHome) != nil {
		return nil, errors.New("managed recovery requires the exact canonical enrolled instance home")
	}
	set, err := authority.Sets.ReadExact(ctx, config.RecoverySetID)
	if err != nil {
		return nil, err
	}
	digest, err := set.Digest()
	if err != nil || set.ID != config.RecoverySetID || set.FrontierDigest != digest || (set.Status != recoveryset.StatusPrepared && set.Status != recoveryset.StatusPublished) {
		return nil, errors.New("managed recovery requires an authoritative immutable frontier")
	}
	occurrence, err := authority.Ledger.Occurrence(ctx, config.OccurrenceID)
	if err != nil {
		return nil, err
	}
	if occurrence.ID != config.OccurrenceID || occurrence.Operation != recovery.OperationRestore || occurrence.TargetScope != set.Delivery.TargetID || occurrence.ArtifactIdentity != config.Artifact.Image || config.Credentials.RecoverySetID != set.ID || config.Credentials.TargetID != set.Delivery.TargetID || config.Credentials.OccurrenceID != occurrence.ID || config.PrimaryFence.TargetID != set.Delivery.TargetID {
		return nil, errors.New("managed provider inputs differ from exact durable set/occurrence")
	}
	if err := VerifyManagedEnrollment(config.Enrollment, set, occurrence, config.InstanceHome); err != nil {
		return nil, err
	}
	if authority.AuthoritySystemIdentifier == "" || authority.AuthoritySystemIdentifier != config.Enrollment.Request.AuthoritySystemID {
		return nil, errors.New("managed recovery authority differs from exact enrollment")
	}
	if len(config.PrimaryFence.Primaries) != 1 {
		return nil, errors.New("managed recovery requires exactly the retained PostgreSQL primary enrollment")
	}
	for _, primary := range config.PrimaryFence.Primaries {
		if primary.SystemIdentifier != config.Readback.Frontier.SystemID || primary.ClusterIdentity != "postgres-system-id:"+primary.SystemIdentifier {
			return nil, errors.New("managed primary enrollment differs from retained PostgreSQL system identity")
		}
	}
	config.Readback.Native.Set = set
	config.Readback.Native.ControlURL = config.Credentials.ControlURL
	config.Readback.Native.DuckLakeURL = config.Credentials.DuckLakeURL
	config.Readback.Native.RootCA = config.Credentials.PostgresRootCA
	config.Readback.Native.Roles = config.Roles
	config.Readback.Native.Credentials = &config.Credentials
	if len(config.Postgres.Points) == 0 {
		config.Postgres.Points = set.CanonicalPoints()
	}
	postgres, err := managedPostgresProvider(config, set)
	if err != nil {
		return nil, err
	}
	if len(config.Roots) != len(set.ObjectRoots) {
		return nil, errors.New("managed recovery requires every exact retained object root")
	}
	roots := make([]*Restic, 0, len(config.Roots))
	handoffRoots := make([]providerrestore.ManagedLocalRoot, 0, len(config.Roots))
	seen := map[recoveryset.ObjectRoot]bool{}
	for _, input := range config.Roots {
		location, err := providerrestore.ManagedLocalRootPath(input.Root, input.StorageRoot)
		if err != nil || input.TargetID != set.Delivery.TargetID || input.RecoverySetID != set.ID || !slices.Contains(set.ObjectRoots, input.Root) || seen[input.Root] || location != input.Destination {
			return nil, errors.New("managed restoration must retain every original normalized root location")
		}
		within, err := filepath.Rel(config.InstanceHome, location)
		if err != nil || within == "." || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
			return nil, errors.New("managed root differs from the exact enrolled instance home/profile")
		}
		seen[input.Root] = true
		if input.Root.Kind == recoveryset.ObjectRootDuckLake {
			if err := VerifyManagedClosure(set.Serving, config.Closure, input.Manifest); err != nil {
				return nil, err
			}
		}
		restorer, err := NewRestic(input)
		if err != nil {
			return nil, err
		}
		roots = append(roots, restorer)
		handoffRoots = append(handoffRoots, providerrestore.ManagedLocalRoot{Root: input.Root, StorageRoot: input.StorageRoot, Destination: input.Destination, ContentManifestDigest: input.ManifestDigest, ArtifactMetadata: restorer.config.ArtifactMetadata})
	}
	fence, err := providerrestore.NewSSHPrimaryFence(config.PrimaryFence)
	if err != nil {
		return nil, err
	}
	providers, err := managedPostgresEndpoints(config.Credentials, set)
	if err != nil {
		return nil, err
	}
	// Validate the complete typed handoff before any restore provider effect.
	preview := providerrestore.ReplacementHandoff{SchemaVersion: providerrestore.ManagedLocalHandoffSchemaVersion, Kind: providerrestore.ManagedLocalHandoffKind, Status: providerrestore.HandoffAvailable, RecoverySetID: set.ID, FrontierDigest: set.FrontierDigest, TargetID: set.Delivery.TargetID, Artifact: config.Artifact, Providers: providers, AvailableAt: time.Now().UTC(), ManagedLocal: &providerrestore.ManagedLocalHandoff{Profile: providerrestore.ManagedLocalProfile, OccurrenceID: occurrence.ID, Roots: handoffRoots}}
	store := ManagedSecretStore{Root: config.SecretRoot}
	reference, err := store.Save(ctx, preview, config.Roles, config.Credentials)
	if err != nil {
		return nil, err
	}
	preview.Secrets = reference
	if err := preview.Validate(set, occurrence.ArtifactIdentity); err != nil {
		return nil, err
	}
	component := &managedComponents{set: set, occurrenceID: occurrence.ID, postgres: postgres, roots: roots, handoff: preview, secretStore: store, roles: config.Roles, credentials: config.Credentials}
	return &managedComposition{dependencies: providerrestore.Dependencies{Ledger: authority.Ledger, Sets: authority.Sets, Databases: postgres, Objects: component, Verifier: component, Evidence: providerrestore.FileEvidenceStore{Root: config.EvidenceRoot}, Handoff: component}, fence: fence, components: component}, nil
}

func managedPostgresEndpoints(credentials ManagedCredentials, set recoveryset.RecoverySet) ([]providerrestore.ProviderEndpoint, error) {
	var result []providerrestore.ProviderEndpoint
	for _, point := range set.ClusterPoints {
		value, key := credentials.ControlURL, "postgres.control.url"
		if point.DatabaseRole == recoveryset.DatabaseDuckLake {
			value, key = credentials.DuckLakeURL, "postgres.ducklake.url"
		}
		parsed, err := url.Parse(value)
		if err != nil {
			return nil, errors.New("managed database endpoint invalid")
		}
		endpoint := (&url.URL{Scheme: "postgres", Host: parsed.Host, RawQuery: "sslmode=verify-full"}).String()
		result = append(result, providerrestore.ProviderEndpoint{Role: string(point.DatabaseRole), Provider: "pgbackrest-managed", ResourceID: point.ClusterIdentity, Endpoint: endpoint, Database: point.DatabaseIdentity, CredentialSecretKey: key, TLSRootCASecretKey: "postgres.root-ca"})
	}
	return result, nil
}

type managedComponents struct {
	set          recoveryset.RecoverySet
	occurrenceID string
	postgres     providerrestore.DatabaseProvider
	roots        []*Restic
	handoff      providerrestore.ReplacementHandoff
	secretStore  ManagedSecretStore
	roles        RuntimeRoles
	credentials  ManagedCredentials
}

func (component *managedComponents) RestoreObject(ctx context.Context, request providerrestore.ObjectRequest) (providerrestore.ObjectResult, error) {
	for _, root := range component.roots {
		if request.Root == root.config.Root {
			return root.RestoreObject(ctx, request)
		}
	}
	return providerrestore.ObjectResult{}, errors.New("object provider has no exact managed root")
}

func (component *managedComponents) Verify(ctx context.Context, request providerrestore.VerificationRequest) (providerrestore.VerificationResult, error) {
	digest, err := request.Set.Digest()
	if err != nil || digest != component.set.FrontierDigest || request.Set.ID != component.set.ID || len(request.Databases) != 2 || len(request.Objects) != len(component.roots) {
		return providerrestore.VerificationResult{}, errors.New("managed verifier scope differs from exact retained providers")
	}
	operation := request.Databases[0].OperationID
	for _, database := range request.Databases {
		if database.Provider != "pgbackrest-managed" || database.OperationID != operation || operation == "" {
			return providerrestore.VerificationResult{}, errors.New("managed PostgreSQL operation proof incomplete")
		}
	}
	results, err := component.postgres.RestoreCluster(ctx, providerrestore.DatabaseRequest{TargetID: component.set.Delivery.TargetID, RecoverySetID: component.set.ID, IdempotencyKey: operation, Points: component.set.ClusterPoints, Catalog: component.set.Catalog})
	if err != nil {
		return providerrestore.VerificationResult{}, err
	}
	verification := providerrestore.VerificationResult{ProviderOperationID: operation, Catalog: component.set.Catalog, VerifiedAt: time.Now().UTC()}
	for _, result := range results {
		if result.DatabaseRole == recoveryset.DatabaseControl {
			verification.ControlStateDigest = result.StateDigest
		} else {
			verification.DuckLakeStateDigest = result.StateDigest
		}
	}
	for _, root := range component.roots {
		matches := 0
		for _, object := range request.Objects {
			if object.Provider == "restic-managed-local" && object.Kind == root.config.Root.Kind && object.URI == root.config.Root.URI && object.RequiredVersionID == root.config.Root.VersionID && object.ObservedVersionID == root.config.Root.VersionID && object.Digest == root.config.Root.Digest {
				matches++
			}
		}
		if matches != 1 || root.verifyDestination(ctx) != nil {
			return providerrestore.VerificationResult{}, errors.New("managed restored file content differs from exact retained frontier")
		}
	}
	verification.ObjectsConsistent, verification.Ready = true, true
	return verification, nil
}

func (component *managedComponents) CreateHandoff(ctx context.Context, request providerrestore.HandoffRequest) (providerrestore.ReplacementHandoff, error) {
	digest, err := request.Set.Digest()
	if err != nil || digest != component.set.FrontierDigest || request.Set.ID != component.set.ID || request.OccurrenceID != component.occurrenceID || request.ArtifactIdentity != component.handoff.Artifact.Image {
		return providerrestore.ReplacementHandoff{}, errors.New("managed handoff differs from durable set/occurrence/artifact")
	}
	if _, err := component.Verify(ctx, providerrestore.VerificationRequest{Set: request.Set, Databases: request.Databases, Objects: request.Objects}); err != nil {
		return providerrestore.ReplacementHandoff{}, err
	}
	if _, err := component.secretStore.Load(ctx, component.handoff.Secrets); err != nil {
		return providerrestore.ReplacementHandoff{}, err
	}
	handoff := component.handoff
	handoff.AvailableAt = time.Now().UTC()
	return handoff, nil
}

// The installed module owns its provider configuration and PostgreSQL service;
// the default adapter preserves the existing confined caller-owned staging path.
func managedPostgresProvider(config ManagedConfig, set recoveryset.RecoverySet) (providerrestore.DatabaseProvider, error) {
	switch config.PostgresProvider {
	case "module-owned":
		return newModulePostgres(config, set)
	case "":
		callback, err := NewPGNativeReadback(config.Readback)
		if err != nil {
			return nil, err
		}
		if config.Postgres.TargetID != set.Delivery.TargetID || config.Postgres.RecoverySetID != set.ID || !samePGPoints(config.Postgres.Points, set.ClusterPoints) || config.Postgres.Frontier != config.Readback.Frontier || config.Postgres.PGBackRest != config.Readback.PGBackRest || config.Postgres.ConfigFile != config.Readback.ProviderConfigFile || config.Postgres.ConfigDigest != config.Readback.ProviderConfigDigest {
			return nil, errors.New("managed PostgreSQL adapters must share the exact retained frontier/configuration")
		}
		config.Postgres.Readback = callback
		return NewPGBackRest(config.Postgres)
	default:
		return nil, errors.New("unsupported managed PostgreSQL provider")
	}
}
