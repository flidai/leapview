// Package catalog contains the governed dashboard authoring read model.
//
// Catalog reads deliberately compose the project dashboard projection from one
// active runtime lease with the project authoring repository.  Neither source
// is allowed to shadow the other, and authorization happens before a source is
// exposed to a caller.
package catalog

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	dashboardcatalog "github.com/flidai/leapview/internal/dashboard/catalog"
	"github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

var (
	// ErrNotFound is returned by Get when the dashboard is absent or the caller
	// is not authorized to view it.  These cases intentionally share one error
	// so a caller cannot probe dashboard existence or lifecycle status.
	ErrNotFound = errors.New("dashboard not found")
	// ErrAmbiguous indicates that both project and project authoring sources
	// own an authorized dashboard with the same project-scoped ID.
	ErrAmbiguous = errors.New("dashboard identity collision")
)

type SourceKind string

const (
	SourceInstance SourceKind = "instance"
	SourceProject  SourceKind = "project"
)

// RevisionEvidence identifies an immutable authored revision without exposing
// the authored document itself.
type RevisionEvidence struct {
	ID          string    `json:"id"`
	Number      uint64    `json:"number"`
	ContentHash string    `json:"contentHash"`
	CreatedAt   time.Time `json:"createdAt,omitempty"`
}

// PublicationEvidence records the lifecycle publication pointer and compiler
// evidence. It is present only for published project dashboards.
type PublicationEvidence struct {
	Revision         RevisionEvidence      `json:"revision"`
	DefinitionHash   string                `json:"definitionHash,omitempty"`
	SemanticIdentity graph.ServingIdentity `json:"semanticIdentity"`
	PublishedAt      time.Time             `json:"publishedAt,omitempty"`
}

// Dashboard is the governed, source-neutral dashboard identity projection.
// ID remains the project-scoped authoring/runtime ID. StableID includes the
// source kind so consumers can safely cache project and project identities
// independently even though a collision is rejected by this service.
type Dashboard struct {
	ID              graph.ResourceID          `json:"id"`
	StableID        string                    `json:"stableId"`
	ProjectID       graph.ResourceID          `json:"projectId"`
	Title           string                    `json:"title"`
	Description     string                    `json:"description,omitempty"`
	SemanticModel   graph.ResourceID          `json:"semanticModel"`
	Source          SourceKind                `json:"source"`
	Origin          authoring.Origin          `json:"origin"`
	Status          authoring.LifecycleStatus `json:"status"`
	Visibility      authoring.Visibility      `json:"visibility"`
	Owner           string                    `json:"owner,omitempty"`
	DraftID         authoring.DraftID         `json:"draftId,omitempty"`
	PageCount       int                       `json:"pageCount"`
	FirstPageID     string                    `json:"-"`
	Tags            []string                  `json:"tags,omitempty"`
	Featured        bool                      `json:"featured,omitempty"`
	ServingIdentity graph.ServingIdentity     `json:"servingIdentity,omitempty"`
	Revision        *RevisionEvidence         `json:"revision,omitempty"`
	Publication     *PublicationEvidence      `json:"publication,omitempty"`
	SourcePath      string                    `json:"sourcePath,omitempty"`
}

// ListResult is deterministic and contains counts after authorization
// filtering. Counts never include unauthorized or archived dashboards.
type ListResult struct {
	Items         []Dashboard `json:"items"`
	Count         int         `json:"count"`
	InstanceCount int         `json:"instanceCount"`
	ProjectCount  int         `json:"projectCount"`
}

type ListRequest struct {
	ProjectID graph.ResourceID
	ActorID   string
}

type GetRequest struct {
	ProjectID   graph.ResourceID
	ActorID     string
	DashboardID authoring.DashboardID
}

// RuntimeCatalog is the smallest safe runtime projection needed to enumerate
// project dashboards. The active runtime implementation already exposes this
// immutable catalog; no manifest reverse-compilation is performed here.
type RuntimeCatalog interface {
	Catalog() dashboardcatalog.Catalog
}

// AuthoredDashboardSources optionally supplies source metadata retained in the
// project artifact. It enriches tags, owner, and source path without making
// those values an authorization or compilation authority.
type AuthoredDashboardSources interface {
	AuthoredDashboardSource(string) (authoring.AuthoredDashboardSource, bool)
}

type Options struct {
	Provider   projectruntime.Provider
	Repository authoring.Repository
	Authorizer authoringservice.Authorizer
}

type Service struct {
	provider   projectruntime.Provider
	repository authoring.Repository
	authorizer authoringservice.Authorizer
}

func NewService(options Options) (*Service, error) {
	if options.Provider == nil {
		return nil, fmt.Errorf("dashboard catalog runtime provider is required")
	}
	if options.Repository == nil {
		return nil, fmt.Errorf("dashboard catalog authoring repository is required")
	}
	if options.Authorizer == nil {
		return nil, fmt.Errorf("dashboard catalog authorizer is required")
	}
	return &Service{provider: options.Provider, repository: options.Repository, authorizer: options.Authorizer}, nil
}

// List returns only non-archived dashboards for which the actor has exact
// dashboard VIEW authorization. A provider lease is acquired once and held
// while all project and project candidates are composed.
func (s *Service) List(ctx context.Context, request ListRequest) (ListResult, error) {
	projectID, actorID, err := normalizeRequest(request.ProjectID, request.ActorID)
	if err != nil {
		return ListResult{}, err
	}
	lease, runtime, err := s.acquire(ctx)
	if err != nil {
		return ListResult{}, err
	}
	defer lease.Release()

	identity := lease.Identity()
	if err := identity.Validate(); err != nil {
		return ListResult{}, fmt.Errorf("dashboard catalog serving identity does not match project: %w", err)
	}
	if identity.ProjectID != projectID {
		return ListResult{}, fmt.Errorf("dashboard catalog serving identity project %q does not match %q", identity.ProjectID, projectID)
	}
	project, err := projectCandidates(runtime, identity)
	if err != nil {
		return ListResult{}, err
	}
	instance, err := s.instanceCandidates(ctx, projectID)
	if err != nil {
		return ListResult{}, err
	}
	visible, err := s.authorizeCandidates(ctx, actorID, project, instance)
	if err != nil {
		return ListResult{}, err
	}
	if err := rejectCollisions(visible); err != nil {
		return ListResult{}, err
	}
	if err := enrichProjectItems(runtime, visible); err != nil {
		return ListResult{}, err
	}
	if err := s.enrichInstanceItems(ctx, projectID, visible); err != nil {
		return ListResult{}, err
	}
	if err := validateDashboards(visible); err != nil {
		return ListResult{}, err
	}
	sortDashboards(visible)
	result := ListResult{Items: visible, Count: len(visible)}
	for _, item := range visible {
		if item.Source == SourceProject {
			result.ProjectCount++
		} else {
			result.InstanceCount++
		}
	}
	return result, nil
}

// Get returns one governed dashboard. Unauthorized and absent dashboards are
// indistinguishable; backend and runtime failures are preserved.
func (s *Service) Get(ctx context.Context, request GetRequest) (Dashboard, error) {
	projectID, actorID, err := normalizeRequest(request.ProjectID, request.ActorID)
	if err != nil {
		return Dashboard{}, err
	}
	if err := authoring.ValidateDashboardID(request.DashboardID); err != nil {
		return Dashboard{}, fmt.Errorf("%w: invalid dashboard id", ErrNotFound)
	}
	id := request.DashboardID.String()
	lease, runtime, err := s.acquire(ctx)
	if err != nil {
		return Dashboard{}, err
	}
	defer lease.Release()

	identity := lease.Identity()
	if err := identity.Validate(); err != nil {
		return Dashboard{}, fmt.Errorf("dashboard catalog serving identity does not match project: %w", err)
	}
	if identity.ProjectID != projectID {
		return Dashboard{}, fmt.Errorf("dashboard catalog serving identity project %q does not match %q", identity.ProjectID, projectID)
	}
	project, err := projectCandidate(runtime, identity, id)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Dashboard{}, err
	}
	instance, err := s.instanceCandidate(ctx, projectID, request.DashboardID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Dashboard{}, err
	}
	candidates := make([]Dashboard, 0, 2)
	if project != nil {
		candidates = append(candidates, *project)
	}
	if instance != nil {
		candidates = append(candidates, *instance)
	}
	visible, err := s.authorizeCandidates(ctx, actorID, candidates)
	if err != nil {
		return Dashboard{}, err
	}
	if len(visible) == 0 {
		return Dashboard{}, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	if err := rejectCollisions(visible); err != nil {
		return Dashboard{}, err
	}
	if visible[0].Source == SourceProject {
		if err := enrichProjectItem(runtime, &visible[0]); err != nil {
			return Dashboard{}, err
		}
	} else {
		if err := s.enrichInstanceItem(ctx, projectID, &visible[0]); err != nil {
			return Dashboard{}, err
		}
	}
	if err := validateDashboard(visible[0]); err != nil {
		return Dashboard{}, err
	}
	return visible[0], nil
}

func (s *Service) acquire(ctx context.Context) (projectruntime.Lease, projectruntime.Runtime, error) {
	if s == nil || s.provider == nil {
		return nil, nil, fmt.Errorf("dashboard catalog runtime provider is required")
	}
	lease, err := s.provider.Acquire(ctx)
	if err != nil {
		return nil, nil, err
	}
	if lease == nil || lease.Runtime() == nil {
		if lease != nil {
			lease.Release()
		}
		return nil, nil, fmt.Errorf("dashboard catalog runtime lease is empty")
	}
	return lease, lease.Runtime(), nil
}

func normalizeRequest(projectID graph.ResourceID, actorID string) (graph.ResourceID, string, error) {
	actorID = strings.TrimSpace(actorID)
	if err := projectID.Validate(); err != nil || actorID == "" {
		return "", "", fmt.Errorf("project and actor are required")
	}
	return projectID, actorID, nil
}

func projectCandidates(runtime projectruntime.Runtime, identity graph.ServingIdentity) ([]Dashboard, error) {
	if err := identity.Validate(); err != nil {
		return nil, fmt.Errorf("active runtime serving identity is invalid: %w", err)
	}
	port, ok := runtime.(RuntimeCatalog)
	if !ok {
		return nil, fmt.Errorf("active runtime does not provide dashboard catalog")
	}
	catalog := port.Catalog()
	items := make([]Dashboard, 0, len(catalog.Dashboards))
	for _, dashboard := range catalog.Dashboards {
		id, err := graph.NewResourceID(strings.TrimSpace(dashboard.ID.String()))
		if err != nil {
			return nil, fmt.Errorf("active runtime contains invalid dashboard id: %w", err)
		}
		item := Dashboard{
			ID: id, ProjectID: identity.ProjectID, Title: dashboard.Title, Description: dashboard.Description,
			SemanticModel: dashboard.SemanticModel, Source: SourceProject, Origin: authoring.OriginFile,
			Status: authoring.LifecycleStatusPublished, Visibility: authoring.VisibilityOrganization,
			Tags:            append([]string(nil), dashboard.Tags...),
			PageCount:       dashboard.PageCount,
			ServingIdentity: identity,
			StableID:        stableID(SourceProject, identity.ProjectID, id.String()),
		}
		items = append(items, item)
	}
	return items, nil
}

func projectCandidate(runtime projectruntime.Runtime, identity graph.ServingIdentity, id string) (*Dashboard, error) {
	items, err := projectCandidates(runtime, identity)
	if err != nil {
		return nil, err
	}
	for index := range items {
		if items[index].ID.String() == id {
			return &items[index], nil
		}
	}
	return nil, ErrNotFound
}

func enrichProjectItems(runtime projectruntime.Runtime, items []Dashboard) error {
	for index := range items {
		if items[index].Source != SourceProject {
			continue
		}
		if err := enrichProjectItem(runtime, &items[index]); err != nil {
			return err
		}
	}
	return nil
}

func enrichProjectItem(runtime projectruntime.Runtime, item *Dashboard) error {
	if item == nil || item.Source != SourceProject {
		return fmt.Errorf("project dashboard metadata is incomplete")
	}
	sources, ok := runtime.(AuthoredDashboardSources)
	if !ok {
		return nil
	}
	source, ok := sources.AuthoredDashboardSource(item.ID.String())
	if !ok {
		return nil
	}
	if source.Document.Metadata.ID != item.ID.String() {
		return fmt.Errorf("project dashboard source %q document id = %q", item.ID, source.Document.Metadata.ID)
	}
	if strings.TrimSpace(source.Metadata.Name) == "" || strings.TrimSpace(source.Metadata.Name) != strings.TrimSpace(source.Document.Metadata.Name) {
		return fmt.Errorf("project dashboard source %q metadata name = %q", item.ID, source.Metadata.Name)
	}
	if source.Metadata.Project != item.ProjectID {
		return fmt.Errorf("project dashboard source %q project = %q, want %q", item.ID, source.Metadata.Project, item.ProjectID)
	}
	if source.Document.Spec.SemanticModel != "" && source.Document.Spec.SemanticModel != item.SemanticModel.String() {
		return fmt.Errorf("project dashboard source %q semantic model = %q, want %q", item.ID, source.Document.Spec.SemanticModel, item.SemanticModel)
	}
	if item.Title == "" {
		item.Title = source.Metadata.Title
	}
	if item.Description == "" {
		item.Description = source.Metadata.Description
	}
	if len(item.Tags) == 0 {
		item.Tags = append([]string(nil), source.Metadata.Tags...)
	}
	item.Featured = source.Document.Metadata.Featured != nil && *source.Document.Metadata.Featured
	item.Owner, item.SourcePath = source.Metadata.Owner, source.Path
	return nil
}

func (s *Service) instanceCandidates(ctx context.Context, projectID graph.ResourceID) ([]Dashboard, error) {
	lifecycles, err := s.repository.List(ctx, projectID)
	if err != nil {
		return nil, err
	}
	items := make([]Dashboard, 0, len(lifecycles))
	for _, lifecycle := range lifecycles {
		item, ok, err := instanceItemBase(projectID, lifecycle)
		if err != nil {
			return nil, err
		}
		if ok {
			items = append(items, item)
		}
	}
	return items, nil
}

func (s *Service) instanceCandidate(ctx context.Context, projectID graph.ResourceID, id authoring.DashboardID) (*Dashboard, error) {
	lifecycle, err := s.repository.Get(ctx, projectID, id)
	if err != nil {
		if errors.Is(err, authoring.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	item, ok, err := instanceItemBase(projectID, lifecycle)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	return &item, nil
}

func instanceItemBase(projectID graph.ResourceID, lifecycle authoring.DashboardLifecycle) (Dashboard, bool, error) {
	if lifecycle.ProjectID != projectID {
		return Dashboard{}, false, fmt.Errorf("dashboard %q belongs to project %q, want %q", lifecycle.ID, lifecycle.ProjectID, projectID)
	}
	if lifecycle.Status == authoring.LifecycleStatusArchived {
		return Dashboard{}, false, nil
	}
	if lifecycle.Status != authoring.LifecycleStatusDraft && lifecycle.Status != authoring.LifecycleStatusPublished {
		return Dashboard{}, false, fmt.Errorf("dashboard %q has invalid lifecycle status %q", lifecycle.ID, lifecycle.Status)
	}
	revisionToken, provenance, err := currentRevision(lifecycle)
	if err != nil {
		return Dashboard{}, false, err
	}
	item := Dashboard{
		ID: lifecycle.ID, ProjectID: projectID, Title: lifecycle.Title,
		SemanticModel: lifecycle.SemanticModel,
		Source:        SourceInstance, Origin: provenance.Origin, Status: lifecycle.Status,
		Visibility: lifecycle.Visibility, Owner: lifecycle.OwnerPrincipalID,
		StableID: stableID(SourceInstance, projectID, lifecycle.ID.String()),
		Revision: &RevisionEvidence{ID: revisionToken.RevisionID.String(), Number: revisionToken.Number, ContentHash: revisionToken.ContentHash},
	}
	if lifecycle.Draft != nil {
		item.DraftID = lifecycle.Draft.ID
	}
	if lifecycle.Status == authoring.LifecycleStatusPublished && lifecycle.Published != nil {
		compiled := lifecycle.Published.Compilation
		publishedToken := lifecycle.Published.Revision
		item.Publication = &PublicationEvidence{
			Revision: RevisionEvidence{ID: publishedToken.RevisionID.String(), Number: publishedToken.Number, ContentHash: publishedToken.ContentHash}, DefinitionHash: compiled.DefinitionHash,
			SemanticIdentity: compiled.SemanticIdentity, PublishedAt: lifecycle.Published.PublishedAt,
		}
	}
	return item, true, nil
}

func (s *Service) enrichInstanceItems(ctx context.Context, projectID graph.ResourceID, items []Dashboard) error {
	for index := range items {
		if items[index].Source != SourceInstance {
			continue
		}
		if err := s.enrichInstanceItem(ctx, projectID, &items[index]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) enrichInstanceItem(ctx context.Context, projectID graph.ResourceID, item *Dashboard) error {
	if item == nil || item.Source != SourceInstance || item.Revision == nil {
		return fmt.Errorf("project dashboard metadata is incomplete")
	}
	revision, err := s.repository.GetRevision(ctx, projectID, authoring.DashboardID(item.ID), authoring.RevisionID(item.Revision.ID))
	if err != nil {
		return err
	}
	if revision.DashboardID != item.ID || revision.Token().RevisionID.String() != item.Revision.ID || revision.Token().Number != item.Revision.Number || revision.Token().ContentHash != item.Revision.ContentHash {
		return fmt.Errorf("dashboard %q lifecycle revision pointer does not match retained revision", item.ID)
	}
	if revision.Document.Metadata.Description != nil {
		item.Description = *revision.Document.Metadata.Description
	}
	item.Featured = revision.Document.Metadata.Featured != nil && *revision.Document.Metadata.Featured
	item.PageCount = len(revision.Document.Spec.Pages)
	if len(revision.Document.Spec.Pages) > 0 {
		item.FirstPageID = strings.TrimSpace(revision.Document.Spec.Pages[0].ID)
	}
	item.Revision.CreatedAt = revision.CreatedAt
	return nil
}

func currentRevision(lifecycle authoring.DashboardLifecycle) (authoring.RevisionToken, authoring.Provenance, error) {
	// A published lifecycle can retain a newer draft. The draft is the current
	// authored revision; Publication below intentionally remains pinned to the
	// exact published pointer.
	if lifecycle.Draft != nil {
		return lifecycle.Draft.Revision, lifecycle.Draft.Provenance, nil
	}
	if lifecycle.Status == authoring.LifecycleStatusPublished && lifecycle.Published != nil {
		return lifecycle.Published.Revision, lifecycle.Published.Provenance, nil
	}
	return authoring.RevisionToken{}, authoring.Provenance{}, fmt.Errorf("dashboard %q has no current revision", lifecycle.ID)
}

func (s *Service) authorizeCandidates(ctx context.Context, actorID string, groups ...[]Dashboard) ([]Dashboard, error) {
	var visible []Dashboard
	for _, group := range groups {
		for _, item := range group {
			target := authoringservice.AuthorizationTargetProjectDashboard
			if item.Source == SourceInstance {
				target = authoringservice.AuthorizationTargetAuthoredDashboard
			}
			err := s.authorizer.Authorize(ctx, authoringservice.AuthorizationRequest{
				ActorID: actorID, ProjectID: item.ProjectID, DashboardID: authoring.DashboardID(item.ID),
				OwnerPrincipalID: item.Owner, SemanticModel: item.SemanticModel,
				Target: target, Visibility: item.Visibility,
				Action: authoring.AuthorizationActionView,
			})
			if err != nil {
				if errors.Is(err, access.ErrForbidden) {
					continue
				}
				return nil, err
			}
			item.Tags = append([]string(nil), item.Tags...)
			visible = append(visible, item)
		}
	}
	return visible, nil
}

func validateDashboards(items []Dashboard) error {
	for _, item := range items {
		if err := validateDashboard(item); err != nil {
			return err
		}
	}
	return nil
}

func validateDashboard(item Dashboard) error {
	if item.ProjectID == "" || item.ID == "" {
		return fmt.Errorf("dashboard identity is incomplete")
	}
	if err := authoring.ValidateDashboardID(authoring.DashboardID(item.ID)); err != nil {
		return err
	}
	if strings.TrimSpace(item.Title) == "" || item.SemanticModel == "" {
		return fmt.Errorf("dashboard %q metadata is incomplete", item.ID)
	}
	if item.Source != SourceProject && item.Source != SourceInstance {
		return fmt.Errorf("dashboard %q has invalid source %q", item.ID, item.Source)
	}
	if item.Source == SourceProject {
		if err := item.ServingIdentity.Validate(); err != nil {
			return fmt.Errorf("dashboard %q has invalid serving identity: %w", item.ID, err)
		}
		if item.ServingIdentity.ProjectID != item.ProjectID {
			return fmt.Errorf("dashboard %q serving identity project %q does not match project %q", item.ID, item.ServingIdentity.ProjectID, item.ProjectID)
		}
	}
	if !item.Origin.Valid() {
		return fmt.Errorf("dashboard %q has invalid provenance origin %q", item.ID, item.Origin)
	}
	if !item.Status.Valid() || !item.Visibility.Valid() {
		return fmt.Errorf("dashboard %q has invalid lifecycle metadata", item.ID)
	}
	return nil
}

func rejectCollisions(items []Dashboard) error {
	seen := make(map[graph.ResourceID]SourceKind, len(items))
	for _, item := range items {
		if source, ok := seen[item.ID]; ok && source != item.Source {
			return fmt.Errorf("%w: project %q dashboard %q is owned by project and project sources", ErrAmbiguous, item.ProjectID, item.ID)
		}
		seen[item.ID] = item.Source
	}
	return nil
}

func sortDashboards(items []Dashboard) {
	sort.SliceStable(items, func(i, j int) bool {
		left, right := strings.ToLower(strings.TrimSpace(items[i].Title)), strings.ToLower(strings.TrimSpace(items[j].Title))
		if left != right {
			return left < right
		}
		if items[i].ID != items[j].ID {
			return items[i].ID < items[j].ID
		}
		return items[i].Source < items[j].Source
	})
}

func stableID(source SourceKind, projectID graph.ResourceID, dashboardID string) string {
	return string(source) + ":" + projectID.String() + ":" + dashboardID
}
