// Package resolver owns the runtime dashboard resolution boundary.
//
// A resolver is deliberately project-scoped: the project generation is
// selected when providers are composed, while individual lookups only accept
// a canonical dashboard ResourceID.
// This keeps project serving state and instance-managed state separate and
// makes an ID collision an explicit error instead of an ordering decision.
package resolver

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

var (
	// ErrNotFound indicates that no provider has a dashboard with the requested
	// ID in the composed project.
	ErrNotFound = errors.New("dashboard not found")
	// ErrAmbiguous indicates that more than one runtime source owns the same
	// dashboard ID. Neither source is allowed to shadow the other.
	ErrAmbiguous = errors.New("dashboard resolution is ambiguous")
	// ErrScopeMismatch indicates that a provider resolved a dashboard outside
	// the project scope fixed when the composite resolver was constructed.
	ErrScopeMismatch = errors.New("dashboard resolver scope mismatch")
	// ErrStaleSemanticState indicates that a published compilation was built
	// for a serving state other than the lease currently being used.
	ErrStaleSemanticState = errors.New("published dashboard semantic serving state is stale")
)

// Source identifies the runtime authority that supplied a dashboard. It is
// intentionally independent from authoring provenance (UI, file, or agent).
type Source string

const (
	SourceProject  Source = "project"
	SourceInstance Source = "instance"
)

// AuthoredRevisionEvidence is the complete authored revision identity attached
// to an instance-managed compiled dashboard. It is evidence only: lifecycle
// and repository state remain the authority for publication workflow.
type AuthoredRevisionEvidence struct {
	ID          string `json:"id"`
	Number      uint64 `json:"number"`
	ContentHash string `json:"contentHash"`
}

func (e AuthoredRevisionEvidence) IsZero() bool {
	return strings.TrimSpace(e.ID) == "" && e.Number == 0 && strings.TrimSpace(e.ContentHash) == ""
}

// Validate requires a complete authored revision identity. The content hash
// uses the same canonical sha256 representation as authoring.RevisionToken.
func (e AuthoredRevisionEvidence) Validate() error {
	if strings.TrimSpace(e.ID) == "" {
		return fmt.Errorf("authored revision id is required")
	}
	if e.ID != strings.TrimSpace(e.ID) {
		return fmt.Errorf("authored revision id cannot have surrounding whitespace")
	}
	if e.Number == 0 {
		return fmt.Errorf("authored revision number is required")
	}
	hash := e.ContentHash
	if hash != strings.TrimSpace(hash) {
		return fmt.Errorf("authored revision content hash cannot have surrounding whitespace")
	}
	if len(hash) != len("sha256:")+64 || !strings.HasPrefix(hash, "sha256:") {
		return fmt.Errorf("authored revision content hash must be lowercase sha256")
	}
	if _, err := hex.DecodeString(hash[len("sha256:"):]); err != nil || hash != strings.ToLower(hash) {
		return fmt.Errorf("authored revision content hash must be lowercase sha256")
	}
	return nil
}

// SourceMetadata is runtime source evidence. Identity binds the result to the
// exact project generation; authored revision evidence is retained only for
// instance-managed dashboards.
type SourceMetadata struct {
	Kind             Source                       `json:"kind"`
	Identity         projectgraph.ServingIdentity `json:"identity"`
	AuthoredRevision AuthoredRevisionEvidence     `json:"authoredRevision,omitempty"`
}

// Resolved is the compiled dashboard and semantic model used by a runtime
// query or HTTP surface, together with runtime source evidence.
type Resolved struct {
	Definition dashboarddefinition.Definition
	Model      *semanticmodel.Model
	// SemanticModelID is the required stable graph identity used to select Model.
	SemanticModelID projectgraph.ResourceID
	Source          SourceMetadata
}

// Visualization resolves a visual from the same compiled definition as the
// dashboard. It cannot accidentally consult another source of truth.
func (r Resolved) Visualization(visualID string) (visualizationdefinition.Definition, bool) {
	definition, ok := r.Definition.Visualizations[strings.TrimSpace(visualID)]
	return definition, ok
}

// Resolver resolves dashboard IDs inside one already-composed project.
type Resolver interface {
	Resolve(dashboardID projectgraph.ResourceID) (Resolved, error)
}

// NewProject normalizes a project/deployment resolver to the shared source
// boundary. Source metadata is supplied by composition and therefore cannot
// inherit authoring provenance fields.
func NewProject(provider Resolver, identity projectgraph.ServingIdentity, metadata SourceMetadata) (Resolver, error) {
	if err := identity.Validate(); err != nil {
		return nil, fmt.Errorf("project serving identity: %w", err)
	}
	if provider == nil {
		return nil, fmt.Errorf("project resolver provider is required")
	}
	metadata.Kind = SourceProject
	metadata.Identity = identity
	// Authoring revision evidence must never be attached to deployment/project
	// state, even when a caller accidentally supplies it.
	metadata.AuthoredRevision = AuthoredRevisionEvidence{}
	return projectProvider{provider: provider, metadata: metadata}, nil
}

// NewPublished normalizes an instance-managed compiled resolver. Provider
// evidence is authoritative, so callers cannot label authoring state as
// project/deployment state or reuse forged provenance origins.
func NewPublished(provider Resolver, identity projectgraph.ServingIdentity, metadata SourceMetadata) (Resolver, error) {
	if err := identity.Validate(); err != nil {
		return nil, fmt.Errorf("published serving identity: %w", err)
	}
	if provider == nil {
		return nil, fmt.Errorf("published resolver provider is required")
	}
	metadata.Kind = SourceInstance
	metadata.Identity = identity
	metadata.AuthoredRevision = AuthoredRevisionEvidence{}
	return publishedProvider{provider: provider, metadata: metadata}, nil
}

type projectProvider struct {
	provider Resolver
	metadata SourceMetadata
}

func (p projectProvider) Resolve(dashboardID projectgraph.ResourceID) (Resolved, error) {
	if p.provider == nil {
		return Resolved{}, ErrNotFound
	}
	if err := dashboardID.Validate(); err != nil {
		return Resolved{}, ErrNotFound
	}
	resolved, err := p.provider.Resolve(dashboardID)
	if err != nil {
		return Resolved{}, err
	}
	if err := validateResolved(dashboardID, resolved); err != nil {
		return Resolved{}, err
	}
	if err := resolved.Source.Identity.Validate(); err != nil {
		return Resolved{}, fmt.Errorf("project dashboard %q has invalid serving identity: %w", dashboardID, err)
	}
	if resolved.Source.Identity != p.metadata.Identity {
		return Resolved{}, fmt.Errorf("%w: project dashboard %q serving identity mismatch", ErrScopeMismatch, dashboardID)
	}
	metadata := p.metadata
	if err := metadata.Identity.Validate(); err != nil {
		return Resolved{}, fmt.Errorf("project provider serving identity: %w", err)
	}
	resolved.Source = metadata
	return resolved, nil
}

type publishedProvider struct {
	provider Resolver
	metadata SourceMetadata
}

func (p publishedProvider) Resolve(dashboardID projectgraph.ResourceID) (Resolved, error) {
	if p.provider == nil {
		return Resolved{}, ErrNotFound
	}
	if err := dashboardID.Validate(); err != nil {
		return Resolved{}, ErrNotFound
	}
	resolved, err := p.provider.Resolve(dashboardID)
	if err != nil {
		return Resolved{}, err
	}
	if err := validateResolved(dashboardID, resolved); err != nil {
		return Resolved{}, err
	}
	if err := resolved.Source.AuthoredRevision.Validate(); err != nil {
		return Resolved{}, fmt.Errorf("published dashboard %q has invalid authored revision evidence: %v", dashboardID, err)
	}
	if err := resolved.Source.Identity.Validate(); err != nil {
		return Resolved{}, fmt.Errorf("published dashboard %q has invalid serving identity: %v", dashboardID, err)
	}
	if resolved.Source.Identity != p.metadata.Identity {
		return Resolved{}, fmt.Errorf("%w: published dashboard %q serving identity mismatch", ErrScopeMismatch, dashboardID)
	}
	metadata := p.metadata
	metadata.AuthoredRevision = resolved.Source.AuthoredRevision
	resolved.Source = metadata
	return resolved, nil
}

func validateResolved(id projectgraph.ResourceID, resolved Resolved) error {
	semanticModel := strings.TrimSpace(resolved.Definition.SemanticModel)
	if resolved.Model == nil || resolved.Definition.ID != id.String() || semanticModel == "" {
		return fmt.Errorf("%w: %q", ErrNotFound, id.String())
	}
	if !resolved.SemanticModelID.Valid() || resolved.SemanticModelID.String() != semanticModel {
		return fmt.Errorf("%w: semantic model identity does not match definition", ErrScopeMismatch)
	}
	return nil
}

// Composite is a deterministic project-scoped resolver. A project
// dashboard always remains visible, but a same-ID published dashboard causes
// an explicit ambiguity error rather than shadowing the project dashboard.
type Composite struct {
	projectID projectgraph.ResourceID
	project   Resolver
	published Resolver
}

// NewComposite composes project and optional published providers for one
// project. The project ID is fixed here and is never accepted by Resolve.
func NewComposite(projectID projectgraph.ResourceID, project, published Resolver) (*Composite, error) {
	if err := projectID.Validate(); err != nil {
		return nil, fmt.Errorf("project ID: %w", err)
	}
	if project == nil && published == nil {
		return nil, fmt.Errorf("at least one dashboard resolver provider is required")
	}
	return &Composite{projectID: projectID, project: project, published: published}, nil
}

func (c *Composite) Resolve(dashboardID projectgraph.ResourceID) (Resolved, error) {
	if c == nil {
		return Resolved{}, ErrNotFound
	}
	if err := dashboardID.Validate(); err != nil {
		return Resolved{}, ErrNotFound
	}
	project, projectErr := resolveProvider(c.project, dashboardID)
	published, publishedErr := resolveProvider(c.published, dashboardID)
	// A provider from another project is a hard failure. Do not fall back to
	// the other source, which could otherwise hide a composition bug.
	if projectErr == nil {
		if err := validateScope(c.projectID, project); err != nil {
			return Resolved{}, err
		}
	}
	if publishedErr == nil {
		if err := validateScope(c.projectID, published); err != nil {
			return Resolved{}, err
		}
	}

	if projectErr == nil && publishedErr == nil {
		return Resolved{}, fmt.Errorf("%w: project %q dashboard %q", ErrAmbiguous, c.projectID, dashboardID)
	}
	if projectErr == nil {
		if publishedErr != nil && !errors.Is(publishedErr, ErrNotFound) {
			return Resolved{}, publishedErr
		}
		return project, nil
	}
	if publishedErr == nil {
		if projectErr != nil && !errors.Is(projectErr, ErrNotFound) {
			return Resolved{}, projectErr
		}
		return published, nil
	}
	if !errors.Is(projectErr, ErrNotFound) {
		return Resolved{}, projectErr
	}
	if !errors.Is(publishedErr, ErrNotFound) {
		return Resolved{}, publishedErr
	}
	return Resolved{}, fmt.Errorf("%w: %q", ErrNotFound, dashboardID)
}

func resolveProvider(provider Resolver, dashboardID projectgraph.ResourceID) (Resolved, error) {
	if provider == nil {
		return Resolved{}, ErrNotFound
	}
	return provider.Resolve(dashboardID)
}

func validateScope(projectID projectgraph.ResourceID, resolved Resolved) error {
	if resolved.Source.Identity.ProjectID != projectID {
		return fmt.Errorf("%w: provider project %q does not match composite project %q", ErrScopeMismatch, resolved.Source.Identity.ProjectID, projectID)
	}
	return nil
}

// ProjectID returns the composition scope for diagnostics and tests.
func (c *Composite) ProjectID() projectgraph.ResourceID {
	if c == nil {
		return ""
	}
	return c.projectID
}
