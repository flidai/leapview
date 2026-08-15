// Package resolver owns the runtime dashboard resolution boundary.
//
// A resolver is deliberately workspace-scoped: the workspace is selected when
// providers are composed, while individual lookups only accept a dashboard ID.
// This keeps project serving state and workspace-authoring state separate and
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
)

var (
	// ErrNotFound indicates that no provider has a dashboard with the requested
	// ID in the composed workspace.
	ErrNotFound = errors.New("dashboard not found")
	// ErrAmbiguous indicates that more than one runtime source owns the same
	// dashboard ID. Neither source is allowed to shadow the other.
	ErrAmbiguous = errors.New("dashboard resolution is ambiguous")
	// ErrScopeMismatch indicates that a provider resolved a dashboard outside
	// the workspace scope fixed when the composite resolver was constructed.
	ErrScopeMismatch = errors.New("dashboard resolver scope mismatch")
	// ErrStaleSemanticState indicates that a published compilation was built
	// for a serving state other than the lease currently being used.
	ErrStaleSemanticState = errors.New("published dashboard semantic serving state is stale")
)

// Source identifies the runtime authority that supplied a dashboard. It is
// intentionally independent from authoring provenance (UI, file, or agent).
type Source string

const (
	SourceProject   Source = "project"
	SourceWorkspace Source = "workspace"
)

// AuthoredRevisionEvidence is the complete authored revision identity attached
// to a workspace-published compiled dashboard. It is evidence only: lifecycle
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

// SourceMetadata is runtime source evidence. ServingStateID identifies the
// deployment/project generation when available; SemanticServingStateID
// identifies the exact semantic serving state of a published compiled
// artifact; AuthoredRevision is complete workspace-authoring revision
// evidence. Neither field is a workflow authority.
type SourceMetadata struct {
	Kind                   Source                   `json:"kind"`
	WorkspaceID            string                   `json:"workspaceId,omitempty"`
	ProjectID              string                   `json:"projectId,omitempty"`
	ServingStateID         string                   `json:"servingStateId,omitempty"`
	SemanticServingStateID string                   `json:"semanticServingStateId,omitempty"`
	AuthoredRevision       AuthoredRevisionEvidence `json:"authoredRevision,omitempty"`
}

// Resolved is the compiled dashboard and semantic model used by a runtime
// query or HTTP surface, together with runtime source evidence.
type Resolved struct {
	Definition dashboarddefinition.Definition
	Model      *semanticmodel.Model
	Source     SourceMetadata
}

// Visualization resolves a visual from the same compiled definition as the
// dashboard. It cannot accidentally consult another source of truth.
func (r Resolved) Visualization(visualID string) (visualizationdefinition.Definition, bool) {
	definition, ok := r.Definition.Visualizations[strings.TrimSpace(visualID)]
	return definition, ok
}

// Resolver resolves dashboard IDs inside one already-composed workspace.
type Resolver interface {
	Resolve(dashboardID string) (Resolved, error)
}

// NewProject normalizes a project/deployment resolver to the shared source
// boundary. Source metadata is supplied by composition and therefore cannot
// inherit authoring provenance fields.
func NewProject(provider Resolver, workspaceID string, metadata SourceMetadata) Resolver {
	metadata.Kind = SourceProject
	metadata.WorkspaceID = strings.TrimSpace(workspaceID)
	// Authoring revision evidence must never be attached to deployment/project
	// state, even when a caller accidentally supplies it.
	metadata.AuthoredRevision = AuthoredRevisionEvidence{}
	return projectProvider{provider: provider, metadata: metadata}
}

// NewPublished normalizes a workspace-authoring compiled resolver. Provider
// evidence is authoritative, so callers cannot label authoring state as
// project/deployment state or reuse forged provenance origins.
func NewPublished(provider Resolver, workspaceID string, metadata SourceMetadata) Resolver {
	metadata.Kind = SourceWorkspace
	metadata.WorkspaceID = strings.TrimSpace(workspaceID)
	// Workspace-authoring state is not a project serving state. Per-dashboard
	// revision and semantic serving-state evidence are required from the
	// provider below.
	metadata.ProjectID = ""
	metadata.ServingStateID = ""
	// The provider is the only authority for semantic serving-state evidence;
	// constructor metadata cannot forge or override the per-dashboard value.
	metadata.SemanticServingStateID = ""
	metadata.AuthoredRevision = AuthoredRevisionEvidence{}
	return publishedProvider{provider: provider, metadata: metadata}
}

type projectProvider struct {
	provider Resolver
	metadata SourceMetadata
}

func (p projectProvider) Resolve(dashboardID string) (Resolved, error) {
	if p.provider == nil {
		return Resolved{}, ErrNotFound
	}
	id := strings.TrimSpace(dashboardID)
	if id == "" {
		return Resolved{}, ErrNotFound
	}
	resolved, err := p.provider.Resolve(id)
	if err != nil {
		return Resolved{}, err
	}
	if err := validateResolved(id, resolved); err != nil {
		return Resolved{}, err
	}
	metadata := p.metadata
	if resolved.Source.ServingStateID != "" {
		metadata.ServingStateID = strings.TrimSpace(resolved.Source.ServingStateID)
	}
	if resolved.Source.SemanticServingStateID != "" {
		metadata.SemanticServingStateID = strings.TrimSpace(resolved.Source.SemanticServingStateID)
	}
	return Resolved{Definition: resolved.Definition, Model: resolved.Model, Source: metadata}, nil
}

type publishedProvider struct {
	provider Resolver
	metadata SourceMetadata
}

func (p publishedProvider) Resolve(dashboardID string) (Resolved, error) {
	if p.provider == nil {
		return Resolved{}, ErrNotFound
	}
	id := strings.TrimSpace(dashboardID)
	if id == "" {
		return Resolved{}, ErrNotFound
	}
	resolved, err := p.provider.Resolve(id)
	if err != nil {
		return Resolved{}, err
	}
	if err := validateResolved(id, resolved); err != nil {
		return Resolved{}, err
	}
	if err := resolved.Source.AuthoredRevision.Validate(); err != nil {
		return Resolved{}, fmt.Errorf("published dashboard %q has invalid authored revision evidence: %v", id, err)
	}
	if strings.TrimSpace(resolved.Source.SemanticServingStateID) == "" {
		return Resolved{}, fmt.Errorf("published dashboard %q is missing semantic serving state evidence", id)
	}
	metadata := p.metadata
	metadata.AuthoredRevision = resolved.Source.AuthoredRevision
	metadata.SemanticServingStateID = strings.TrimSpace(resolved.Source.SemanticServingStateID)
	resolved.Source = metadata
	return resolved, nil
}

func validateResolved(id string, resolved Resolved) error {
	if resolved.Model == nil || strings.TrimSpace(resolved.Definition.ID) == "" || strings.TrimSpace(resolved.Definition.ID) != id || strings.TrimSpace(resolved.Definition.SemanticModel) == "" || strings.TrimSpace(resolved.Model.Name) != strings.TrimSpace(resolved.Definition.SemanticModel) {
		return fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	return nil
}

// Composite is a deterministic workspace-scoped resolver. A project
// dashboard always remains visible, but a same-ID published dashboard causes
// an explicit ambiguity error rather than shadowing the project dashboard.
type Composite struct {
	workspaceID string
	project     Resolver
	published   Resolver
}

// NewComposite composes project and optional published providers for one
// workspace. The workspace ID is fixed here and is never accepted by Resolve.
func NewComposite(workspaceID string, project, published Resolver) (*Composite, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return nil, fmt.Errorf("workspace ID is required")
	}
	if project == nil && published == nil {
		return nil, fmt.Errorf("at least one dashboard resolver provider is required")
	}
	return &Composite{workspaceID: workspaceID, project: project, published: published}, nil
}

func (c *Composite) Resolve(dashboardID string) (Resolved, error) {
	if c == nil {
		return Resolved{}, ErrNotFound
	}
	id := strings.TrimSpace(dashboardID)
	if id == "" {
		return Resolved{}, ErrNotFound
	}
	project, projectErr := resolveProvider(c.project, id)
	published, publishedErr := resolveProvider(c.published, id)
	// A provider from another workspace is a hard failure. Do not fall back to
	// the other source, which could otherwise hide a composition bug.
	if projectErr == nil {
		if err := validateScope(c.workspaceID, project); err != nil {
			return Resolved{}, err
		}
	}
	if publishedErr == nil {
		if err := validateScope(c.workspaceID, published); err != nil {
			return Resolved{}, err
		}
	}

	if projectErr == nil && publishedErr == nil {
		return Resolved{}, fmt.Errorf("%w: workspace %q dashboard %q", ErrAmbiguous, c.workspaceID, id)
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
	return Resolved{}, fmt.Errorf("%w: %q", ErrNotFound, id)
}

func resolveProvider(provider Resolver, dashboardID string) (Resolved, error) {
	if provider == nil {
		return Resolved{}, ErrNotFound
	}
	return provider.Resolve(dashboardID)
}

func validateScope(workspaceID string, resolved Resolved) error {
	if strings.TrimSpace(resolved.Source.WorkspaceID) != workspaceID {
		return fmt.Errorf("%w: provider workspace %q does not match composite workspace %q", ErrScopeMismatch, resolved.Source.WorkspaceID, workspaceID)
	}
	return nil
}

// WorkspaceID returns the composition scope for diagnostics and tests.
func (c *Composite) WorkspaceID() string {
	if c == nil {
		return ""
	}
	return c.workspaceID
}
