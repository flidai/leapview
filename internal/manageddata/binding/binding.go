// Package binding pins project-global managed-data revisions to serving states.
package binding

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/manageddata"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

var (
	ErrArtifactMetadata          = errors.New("managed data artifact metadata is inconsistent")
	ErrPinnedRevisionUnavailable = errors.New("managed data pinned revision is unavailable")
	ErrRepository                = errors.New("managed data binding repository failure")
)

// Repository is the metadata surface needed to resolve artifact-owned pins.
// InstallServingStateBindings writes immutable generation evidence. Replays
// must be identical; conflicting pins are rejected.
type Repository interface {
	CollectionByProjectConnection(context.Context, projectgraph.ResourceID, projectgraph.ResourceID) (manageddata.Collection, error)
	ListRevisions(context.Context, projectgraph.ResourceID) ([]manageddata.Revision, error)
	RevisionByID(context.Context, manageddata.RevisionID) (manageddata.Revision, error)
	EnvironmentPointer(context.Context, projectgraph.ResourceID, manageddata.Environment) (manageddata.EnvironmentPointer, error)
	InstallServingStateBindings(context.Context, projectgraph.ServingIdentity, []manageddata.ServingStateBinding) error
	ListServingStateBindings(context.Context, projectgraph.ServingIdentity) ([]manageddata.ServingStateBinding, error)
}

// Binder resolves project-global revision pins during publish validation.
type Binder struct {
	repository Repository
}

func New(repository Repository) (*Binder, error) {
	if repository == nil {
		return nil, fmt.Errorf("managed data binding repository is required")
	}
	return &Binder{repository: repository}, nil
}

// AfterArtifactValidation implements the serving-state publish validation hook.
func (b *Binder) AfterArtifactValidation(ctx context.Context, candidate servingstate.State, validation servingstate.Validation) error {
	if b == nil || b.repository == nil {
		return ErrRepository
	}
	projectID := validation.ProjectID
	if !projectID.Valid() || string(candidate.ID) == "" || projectID != validation.ProjectID || validation.ManagedDataRevisions == nil {
		return ErrArtifactMetadata
	}

	environment, err := manageddata.NormalizeEnvironment(string(servingstate.NormalizeEnvironment(candidate.Environment)))
	if err != nil {
		return ErrArtifactMetadata
	}
	connections := make([]string, 0, len(validation.ManagedDataRevisions))
	for connection, digest := range validation.ManagedDataRevisions {
		if connection == "" || connection != strings.TrimSpace(connection) || manageddata.ValidateRevisionID(digest) != nil {
			return ErrArtifactMetadata
		}
		connections = append(connections, connection)
	}
	sort.Strings(connections)
	identity, identityErr := projectgraph.NewServingIdentity(projectID, string(environment), string(candidate.ID))
	if identityErr != nil {
		return ErrArtifactMetadata
	}
	bindings := make([]manageddata.ServingStateBinding, 0, len(connections))
	for _, connection := range connections {
		connectionID, parseErr := projectgraph.NewResourceID(connection)
		if parseErr != nil {
			return ErrArtifactMetadata
		}
		binding, bindErr := b.pinnedBinding(ctx, identity, projectID, connectionID, validation.ManagedDataRevisions[connection], environment)
		if bindErr != nil {
			return bindErr
		}
		bindings = append(bindings, binding)
	}
	sort.Slice(bindings, func(i, j int) bool {
		if bindings[i].CollectionID != bindings[j].CollectionID {
			return bindings[i].CollectionID < bindings[j].CollectionID
		}
		return bindings[i].RevisionID < bindings[j].RevisionID
	})
	if err := b.repository.InstallServingStateBindings(ctx, identity, bindings); err != nil {
		return repositoryError(err)
	}
	return nil
}

// ValidateServingStatePins proves that the artifact-owned bindings written by
// AfterArtifactValidation exactly match the release manifest. Release pins are
// content digests, while serving-state bindings use internal revision IDs, so
// each expected digest must first be resolved within its project connection.
func (b *Binder) ValidateServingStatePins(ctx context.Context, identity projectgraph.ServingIdentity, expected map[projectgraph.ResourceID]string) error {
	if b == nil || b.repository == nil {
		return ErrRepository
	}
	if identity.Validate() != nil || expected == nil {
		return ErrArtifactMetadata
	}
	actual, err := b.repository.ListServingStateBindings(ctx, identity)
	if err != nil {
		return repositoryError(err)
	}
	if len(actual) != len(expected) {
		return ErrArtifactMetadata
	}
	actualByCollection := make(map[projectgraph.ResourceID]manageddata.ServingStateBinding, len(actual))
	for _, binding := range actual {
		if binding.Identity != identity || !binding.CollectionID.Valid() || binding.RevisionID.String() == "" {
			return ErrArtifactMetadata
		}
		if _, duplicate := actualByCollection[binding.CollectionID]; duplicate {
			return ErrArtifactMetadata
		}
		actualByCollection[binding.CollectionID] = binding
	}
	for connectionID, digest := range expected {
		if !connectionID.Valid() || manageddata.ValidateRevisionID(digest) != nil {
			return ErrArtifactMetadata
		}
		resolved, resolveErr := b.pinnedBinding(ctx, identity, identity.ProjectID, connectionID, digest, manageddata.Environment(identity.Environment))
		if resolveErr != nil {
			return resolveErr
		}
		binding, ok := actualByCollection[resolved.CollectionID]
		if !ok || binding.RevisionID != resolved.RevisionID {
			return ErrArtifactMetadata
		}
	}
	return nil
}

// ResolveCandidatePins captures the target's current managed-data identity for
// a private candidate. A target without an active generation bootstraps from
// the newest ready revision, which is then pinned immutably in the candidate.
func (b *Binder) ResolveCandidatePins(
	ctx context.Context,
	projectID projectgraph.ResourceID,
	connections []projectgraph.ResourceID,
	environment string,
) (map[projectgraph.ResourceID]string, error) {
	normalizedEnvironment, err := manageddata.NormalizeEnvironment(environment)
	if b == nil || b.repository == nil {
		return nil, ErrRepository
	}
	if err != nil {
		return nil, fmt.Errorf("%w: invalid candidate environment %q", ErrArtifactMetadata, environment)
	}
	if !projectID.Valid() {
		return nil, fmt.Errorf("%w: invalid candidate project %q", ErrArtifactMetadata, projectID)
	}
	connections = append([]projectgraph.ResourceID(nil), connections...)
	sort.Slice(connections, func(i, j int) bool { return connections[i] < connections[j] })
	pins := make(map[projectgraph.ResourceID]string, len(connections))
	for index, connection := range connections {
		if !connection.Valid() {
			return nil, fmt.Errorf("%w: invalid candidate connection %q", ErrArtifactMetadata, connection)
		}
		if index > 0 && connections[index-1] == connection {
			return nil, fmt.Errorf("%w: duplicate candidate connection %q", ErrArtifactMetadata, connection)
		}
		collection, err := b.repository.CollectionByProjectConnection(
			ctx,
			projectID,
			connection,
		)
		if err != nil {
			if errors.Is(err, manageddata.ErrNotFound) {
				return nil, ErrPinnedRevisionUnavailable
			}
			return nil, repositoryError(err)
		}
		if collection.Status != manageddata.CollectionStatusActive ||
			collection.ProjectID != projectID ||
			collection.ConnectionID != connection {
			return nil, ErrPinnedRevisionUnavailable
		}
		revision, err := b.candidateRevision(
			ctx,
			collection,
			normalizedEnvironment,
		)
		if err != nil {
			return nil, err
		}
		if revision.CollectionID != collection.ID ||
			revision.Status != manageddata.RevisionStatusReady ||
			manageddata.ValidateRevisionID(revision.Digest) != nil {
			return nil, ErrPinnedRevisionUnavailable
		}
		pins[connection] = revision.Digest
	}
	return pins, nil
}

func (b *Binder) candidateRevision(
	ctx context.Context,
	collection manageddata.Collection,
	environment manageddata.Environment,
) (manageddata.Revision, error) {
	pointer, err := b.repository.EnvironmentPointer(ctx, collection.ID, environment)
	if err == nil {
		if pointer.CollectionID != collection.ID ||
			pointer.Environment != environment ||
			pointer.RevisionID.String() == "" {
			return manageddata.Revision{}, fmt.Errorf("%w: environment pointer does not match collection %q and environment %q", ErrArtifactMetadata, collection.ID, environment)
		}
		revision, revisionErr := b.repository.RevisionByID(
			ctx,
			pointer.RevisionID,
		)
		if revisionErr != nil {
			return manageddata.Revision{}, repositoryError(revisionErr)
		}
		return revision, nil
	}
	if !errors.Is(err, manageddata.ErrNotFound) {
		return manageddata.Revision{}, repositoryError(err)
	}
	revisions, err := b.repository.ListRevisions(ctx, collection.ID)
	if err != nil {
		return manageddata.Revision{}, repositoryError(err)
	}
	var selected manageddata.Revision
	for _, revision := range revisions {
		if revision.CollectionID != collection.ID {
			return manageddata.Revision{}, fmt.Errorf("%w: revision %q belongs to collection %q, want %q", ErrArtifactMetadata, revision.ID, revision.CollectionID, collection.ID)
		}
		if revision.Status != manageddata.RevisionStatusReady {
			continue
		}
		if selected.ID == "" || revision.Sequence > selected.Sequence {
			selected = revision
			continue
		}
		if revision.Sequence == selected.Sequence && revision.ID != selected.ID {
			return manageddata.Revision{}, fmt.Errorf("%w: revisions %q and %q share sequence %d", ErrArtifactMetadata, selected.ID, revision.ID, revision.Sequence)
		}
	}
	if selected.ID == "" {
		return manageddata.Revision{}, ErrPinnedRevisionUnavailable
	}
	return selected, nil
}

func (b *Binder) pinnedBinding(ctx context.Context, identity projectgraph.ServingIdentity, projectID, connectionID projectgraph.ResourceID, digest string, environment manageddata.Environment) (manageddata.ServingStateBinding, error) {
	collection, err := b.repository.CollectionByProjectConnection(ctx, projectID, connectionID)
	if err != nil {
		if errors.Is(err, manageddata.ErrNotFound) {
			return manageddata.ServingStateBinding{}, ErrPinnedRevisionUnavailable
		}
		return manageddata.ServingStateBinding{}, repositoryError(err)
	}
	if !collection.ID.Valid() || collection.ProjectID != projectID || collection.ConnectionID != connectionID {
		return manageddata.ServingStateBinding{}, ErrArtifactMetadata
	}
	if collection.Status != manageddata.CollectionStatusActive {
		return manageddata.ServingStateBinding{}, ErrPinnedRevisionUnavailable
	}

	revisions, err := b.repository.ListRevisions(ctx, collection.ID)
	if err != nil {
		return manageddata.ServingStateBinding{}, repositoryError(err)
	}
	var match manageddata.Revision
	matches := 0
	for _, revision := range revisions {
		if revision.CollectionID != collection.ID {
			return manageddata.ServingStateBinding{}, ErrArtifactMetadata
		}
		if revision.Digest == digest && revision.Status == manageddata.RevisionStatusReady {
			match = revision
			matches++
		}
	}
	if matches > 1 {
		return manageddata.ServingStateBinding{}, ErrArtifactMetadata
	}
	if matches == 0 {
		return manageddata.ServingStateBinding{}, ErrPinnedRevisionUnavailable
	}
	return manageddata.ServingStateBinding{
		Identity:     identity,
		CollectionID: collection.ID,
		RevisionID:   match.ID,
	}, nil
}

func repositoryError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return ErrRepository
}
