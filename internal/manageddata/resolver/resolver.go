// Package resolver reconstructs persisted managed-data revisions for serving runtimes.
package resolver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/flidai/leapview/internal/manageddata"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

var (
	ErrInvalidMetadata     = errors.New("invalid managed data metadata")
	ErrRevisionNotReady    = errors.New("managed data revision is not ready")
	ErrAmbiguousConnection = errors.New("ambiguous managed data connection")
	ErrRepository          = errors.New("managed data repository failure")
	ErrMaterialization     = errors.New("managed data materialization failure")
)

// Repository is the read-only portion of manageddata.Repository needed to
// reconstruct a serving state's immutable managed-data bindings.
type Repository interface {
	ListServingStateBindings(context.Context, projectgraph.ServingIdentity) ([]manageddata.ServingStateBinding, error)
	CollectionByID(context.Context, projectgraph.ResourceID) (manageddata.Collection, error)
	RevisionByID(context.Context, manageddata.RevisionID) (manageddata.Revision, error)
	ListRevisionFiles(context.Context, manageddata.RevisionID) ([]manageddata.RevisionFile, error)
}

// ServingStateRepository supplies the environment that bindings must match.
type ServingStateRepository interface {
	ByID(context.Context, servingstate.ID) (servingstate.State, error)
}

type Lifetime interface {
	Release() error
}

// Resolution is the managed-data-owned result of resolving immutable serving
// bindings. Runtime-host mapping is supplied by process composition.
type Resolution struct {
	RevisionID string
	Roots      map[projectgraph.ResourceID]string
	Revisions  map[projectgraph.ResourceID]string
	Lifetime   Lifetime
}

// Resolver validates persisted bindings and materializes their immutable views.
type Resolver struct {
	repository    Repository
	servingStates ServingStateRepository
	materializer  manageddata.RevisionMaterializer
}

// New constructs a managed-data runtime resolver.
func New(repository Repository, servingStates ServingStateRepository, materializer manageddata.RevisionMaterializer) (*Resolver, error) {
	if repository == nil {
		return nil, fmt.Errorf("repository is required")
	}
	if servingStates == nil {
		return nil, fmt.Errorf("serving state repository is required")
	}
	if materializer == nil {
		return nil, fmt.Errorf("revision materializer is required")
	}
	return &Resolver{repository: repository, servingStates: servingStates, materializer: materializer}, nil
}

// ResolveManagedData resolves the bindings already persisted for servingStateID.
// RevisionID is a SHA-256 digest over canonical JSON containing the sorted
// (project, connection, manifest digest) tuples, including for one binding.
func (r *Resolver) ResolveManagedData(ctx context.Context, identity projectgraph.ServingIdentity) (Resolution, error) {
	bindings, err := r.loadBindings(ctx, identity)
	if err != nil {
		return Resolution{}, err
	}
	return r.resolveBindings(ctx, identity, bindings)
}

type resolvedBinding struct {
	project        projectgraph.ResourceID
	connection     projectgraph.ResourceID
	manifestDigest string
	manifest       manageddata.Manifest
}

func (r *Resolver) resolveBindings(ctx context.Context, identity projectgraph.ServingIdentity, bindings []manageddata.ServingStateBinding) (Resolution, error) {
	if err := identity.Validate(); err != nil {
		return Resolution{}, invalidMetadata("serving identity is invalid")
	}
	state, err := r.servingStates.ByID(ctx, servingstate.ID(identity.GenerationID))
	if err != nil {
		return Resolution{}, sanitizeRepositoryError(ctx, "load serving state", err)
	}
	stateEnvironment, normalizeErr := manageddata.NormalizeEnvironment(string(state.Environment))
	if state.ID != servingstate.ID(identity.GenerationID) || state.ProjectID != identity.ProjectID || normalizeErr != nil || string(stateEnvironment) != identity.Environment {
		return Resolution{}, invalidMetadata("serving state relationship or environment is invalid")
	}
	if len(bindings) == 0 {
		return Resolution{Roots: map[projectgraph.ResourceID]string{}}, nil
	}

	resolved := make([]resolvedBinding, 0, len(bindings))
	connections := make(map[projectgraph.ResourceID]struct{}, len(bindings))
	collections := make(map[projectgraph.ResourceID]struct{}, len(bindings))
	for _, binding := range bindings {
		if binding.Identity != identity || !binding.CollectionID.Valid() || binding.RevisionID.String() == "" {
			return Resolution{}, invalidMetadata("binding relationship is invalid")
		}
		if _, duplicate := collections[binding.CollectionID]; duplicate {
			return Resolution{}, invalidMetadata("collection has duplicate bindings")
		}
		collections[binding.CollectionID] = struct{}{}

		collection, loadErr := r.repository.CollectionByID(ctx, binding.CollectionID)
		if loadErr != nil {
			return Resolution{}, sanitizeRepositoryError(ctx, "load collection", loadErr)
		}
		if collection.ID != binding.CollectionID || collection.ProjectID != identity.ProjectID || !collection.ProjectID.Valid() || !collection.ConnectionID.Valid() {
			return Resolution{}, invalidMetadata("collection relationship or identity is invalid")
		}
		if _, duplicate := connections[collection.ConnectionID]; duplicate {
			return Resolution{}, fmt.Errorf("%w: connection %q is bound more than once", ErrAmbiguousConnection, collection.ConnectionID)
		}
		connections[collection.ConnectionID] = struct{}{}

		revision, loadErr := r.repository.RevisionByID(ctx, binding.RevisionID)
		if loadErr != nil {
			return Resolution{}, sanitizeRepositoryError(ctx, "load revision", loadErr)
		}
		if revision.ID != binding.RevisionID || revision.CollectionID != collection.ID {
			return Resolution{}, invalidMetadata("revision relationship is invalid")
		}
		if revision.Status != manageddata.RevisionStatusReady {
			return Resolution{}, ErrRevisionNotReady
		}
		manifest, manifestErr := validateRevisionManifest(revision)
		if manifestErr != nil {
			return Resolution{}, manifestErr
		}
		files, loadErr := r.repository.ListRevisionFiles(ctx, revision.ID)
		if loadErr != nil {
			return Resolution{}, sanitizeRepositoryError(ctx, "load revision files", loadErr)
		}
		if metadataErr := validateRevisionFiles(revision, manifest, files); metadataErr != nil {
			return Resolution{}, metadataErr
		}
		resolved = append(resolved, resolvedBinding{
			project: collection.ProjectID, connection: collection.ConnectionID,
			manifestDigest: revision.Digest, manifest: manifest,
		})
	}

	sort.Slice(resolved, func(i, j int) bool {
		if resolved[i].project != resolved[j].project {
			return resolved[i].project < resolved[j].project
		}
		if resolved[i].connection != resolved[j].connection {
			return resolved[i].connection < resolved[j].connection
		}
		return resolved[i].manifestDigest < resolved[j].manifestDigest
	})

	roots := make(map[projectgraph.ResourceID]string, len(resolved))
	revisions := make(map[projectgraph.ResourceID]string, len(resolved))
	leases := make([]manageddata.RevisionLease, 0, len(resolved))
	for _, binding := range resolved {
		lease, materializeErr := r.materializer.MaterializeRevision(ctx, binding.manifestDigest, binding.manifest)
		if materializeErr != nil {
			_ = (&managedDataLifetime{leases: leases}).Release()
			return Resolution{}, sanitizeMaterializationError(ctx, materializeErr)
		}
		if lease == nil || strings.TrimSpace(lease.Root()) == "" {
			_ = (&managedDataLifetime{leases: append(leases, lease)}).Release()
			return Resolution{}, ErrMaterialization
		}
		leases = append(leases, lease)
		roots[binding.connection] = lease.Root()
		revisions[binding.connection] = binding.manifestDigest
	}
	return Resolution{
		RevisionID: aggregateRevisionID(resolved), Roots: roots, Revisions: revisions,
		Lifetime: &managedDataLifetime{leases: leases},
	}, nil
}

type managedDataLifetime struct {
	leases []manageddata.RevisionLease
	once   sync.Once
	err    error
}

func (l *managedDataLifetime) Release() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		for index := len(l.leases) - 1; index >= 0; index-- {
			if l.leases[index] != nil {
				l.err = errors.Join(l.err, l.leases[index].Release())
			}
		}
		l.leases = nil
	})
	return l.err
}

func (r *Resolver) loadBindings(ctx context.Context, identity projectgraph.ServingIdentity) ([]manageddata.ServingStateBinding, error) {
	if err := identity.Validate(); err != nil {
		return nil, invalidMetadata("serving identity is invalid")
	}
	bindings, err := r.repository.ListServingStateBindings(ctx, identity)
	if err != nil {
		return nil, sanitizeRepositoryError(ctx, "load serving state bindings", err)
	}
	return append([]manageddata.ServingStateBinding(nil), bindings...), nil
}

func validateRevisionManifest(revision manageddata.Revision) (manageddata.Manifest, error) {
	decoder := json.NewDecoder(strings.NewReader(revision.ManifestJSON))
	decoder.DisallowUnknownFields()
	var manifest manageddata.Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return manageddata.Manifest{}, invalidMetadata("stored manifest is invalid")
	}
	if err := requireJSONEnd(decoder); err != nil {
		return manageddata.Manifest{}, invalidMetadata("stored manifest is invalid")
	}
	canonical, err := manifest.CanonicalJSON()
	if err != nil {
		return manageddata.Manifest{}, invalidMetadata("stored manifest is invalid")
	}
	if !bytes.Equal(canonical, []byte(revision.ManifestJSON)) {
		return manageddata.Manifest{}, invalidMetadata("stored manifest is not canonical")
	}
	if revision.Digest != manifest.RevisionID() {
		return manageddata.Manifest{}, invalidMetadata("stored manifest digest does not match revision")
	}
	if revision.FileCount != int64(len(manifest.Files)) {
		return manageddata.Manifest{}, invalidMetadata("stored manifest file count does not match revision")
	}
	var size int64
	for _, file := range manifest.Files {
		size += file.Size
	}
	if revision.SizeBytes != size {
		return manageddata.Manifest{}, invalidMetadata("stored manifest size does not match revision")
	}
	return manifest, nil
}

func requireJSONEnd(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func validateRevisionFiles(revision manageddata.Revision, manifest manageddata.Manifest, files []manageddata.RevisionFile) error {
	if len(files) != len(manifest.Files) {
		return invalidMetadata("revision file count does not match manifest")
	}
	expected := make(map[string]manageddata.File, len(manifest.Files))
	for _, file := range manifest.Files {
		expected[file.Path] = file
	}
	seen := make(map[string]struct{}, len(files))
	for _, stored := range files {
		if stored.RevisionID != revision.ID || strings.TrimSpace(stored.StorageKey) == "" {
			return invalidMetadata("revision file relationship is invalid")
		}
		if _, duplicate := seen[stored.Path]; duplicate {
			return invalidMetadata("revision contains duplicate file metadata")
		}
		seen[stored.Path] = struct{}{}
		want, ok := expected[stored.Path]
		if !ok || stored.File != want {
			return invalidMetadata("revision file metadata does not match manifest")
		}
	}
	return nil
}

type aggregateBinding struct {
	Project        string `json:"project"`
	Connection     string `json:"connection"`
	ManifestDigest string `json:"manifest_digest"`
}

func aggregateRevisionID(bindings []resolvedBinding) string {
	payload := make([]aggregateBinding, 0, len(bindings))
	for _, binding := range bindings {
		payload = append(payload, aggregateBinding{
			Project: binding.project.String(), Connection: binding.connection.String(), ManifestDigest: binding.manifestDigest,
		})
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		panic("marshal managed-data aggregate: " + err.Error())
	}
	digest := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func sanitizeRepositoryError(ctx context.Context, operation string, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, manageddata.ErrNotFound) {
		return fmt.Errorf("%w: %s returned no record", ErrInvalidMetadata, operation)
	}
	return fmt.Errorf("%w: %s failed", ErrRepository, operation)
}

func sanitizeMaterializationError(ctx context.Context, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return ErrMaterialization
}

func invalidMetadata(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidMetadata, reason)
}

func canonicalIdentifier(value string) bool {
	return value != "" && strings.TrimSpace(value) == value
}

func validAuthoredIdentity(value string) bool {
	if !canonicalIdentifier(value) {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}
