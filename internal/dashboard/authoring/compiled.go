package authoring

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/project/graph"
)

// CompiledRevisionToken identifies the exact compiler output selected by a
// published lifecycle.  The authored token and semantic serving state are
// deliberately part of the token: a definition hash by itself is not enough
// to prove which authored revision or model boundary produced it.
type CompiledRevisionToken struct {
	AuthoredRevision RevisionToken `json:"authoredRevision"`
	DefinitionHash   string        `json:"definitionHash"`
	// SemanticModelID is the exact graph ResourceID used by compilation. It is
	// evidence, not a symbolic name, and is compared during generation
	// revalidation before a published pointer can advance.
	SemanticModelID  graph.ResourceID      `json:"semanticModelId"`
	SemanticIdentity graph.ServingIdentity `json:"semanticIdentity"`
}

func (t CompiledRevisionToken) IsZero() bool {
	return t.AuthoredRevision.IsZero() && t.DefinitionHash == "" && t.SemanticModelID == "" && t.SemanticIdentity == (graph.ServingIdentity{})
}

func (t CompiledRevisionToken) Validate() error {
	if err := t.AuthoredRevision.ValidateComplete(); err != nil {
		return fmt.Errorf("%w: compiled authored revision: %v", ErrInvalidAuthoring, err)
	}
	if !validSHA256(t.DefinitionHash) || t.DefinitionHash != strings.ToLower(t.DefinitionHash) {
		return fmt.Errorf("%w: compiled definition hash is required and must be lowercase sha256", ErrInvalidAuthoring)
	}
	if t.SemanticModelID != "" {
		if err := t.SemanticModelID.Validate(); err != nil {
			return fmt.Errorf("%w: compiled semantic model id: %v", ErrInvalidAuthoring, err)
		}
	}
	if err := t.SemanticIdentity.Validate(); err != nil {
		return fmt.Errorf("%w: semantic serving identity is required: %v", ErrInvalidAuthoring, err)
	}
	return nil
}

// CompiledRevision is immutable serving output for one authored revision.
// Scope is carried in the value so repository implementations cannot
// accidentally return an artifact from another project or dashboard.
type CompiledRevision struct {
	ProjectID        graph.ResourceID               `json:"projectId"`
	DashboardID      DashboardID                    `json:"dashboardId"`
	AuthoredRevision RevisionToken                  `json:"authoredRevision"`
	Definition       dashboarddefinition.Definition `json:"definition"`
	DefinitionHash   string                         `json:"definitionHash"`
	SemanticModelID  graph.ResourceID               `json:"semanticModelId"`
	SemanticIdentity graph.ServingIdentity          `json:"semanticIdentity"`
	CompiledAt       time.Time                      `json:"compiledAt"`
}

// NewCompiledRevision computes the canonical definition hash and deep-copies
// compiler output before it crosses the persistence boundary.
func NewCompiledRevision(projectID graph.ResourceID, dashboardID DashboardID, authored RevisionToken, definition dashboarddefinition.Definition, semanticIdentity graph.ServingIdentity, compiledAt time.Time) (CompiledRevision, error) {
	if err := validateResourceID("compiled project id", projectID); err != nil {
		return CompiledRevision{}, err
	}
	if err := validateDashboardID(dashboardID); err != nil {
		return CompiledRevision{}, err
	}
	if err := authored.ValidateComplete(); err != nil {
		return CompiledRevision{}, err
	}
	if err := validateCompiledDefinition(dashboardID, definition); err != nil {
		return CompiledRevision{}, err
	}
	semanticModelID, err := graph.NewResourceID(definition.SemanticModel)
	if err != nil {
		return CompiledRevision{}, fmt.Errorf("%w: compiled semantic model id: %v", ErrInvalidAuthoring, err)
	}
	if err := semanticIdentity.Validate(); err != nil {
		return CompiledRevision{}, fmt.Errorf("%w: semantic serving identity is required: %v", ErrInvalidAuthoring, err)
	}
	if semanticIdentity.ProjectID != projectID {
		return CompiledRevision{}, fmt.Errorf("%w: semantic serving identity project %q does not match compiled project %q", ErrInvalidAuthoring, semanticIdentity.ProjectID, projectID)
	}
	if compiledAt.IsZero() || compiledAt.Location() != time.UTC {
		return CompiledRevision{}, fmt.Errorf("%w: compiled_at must be a non-zero UTC timestamp", ErrInvalidAuthoring)
	}
	cloned, err := cloneDefinition(definition)
	if err != nil {
		return CompiledRevision{}, err
	}
	hash, err := DefinitionHash(cloned)
	if err != nil {
		return CompiledRevision{}, err
	}
	return CompiledRevision{ProjectID: projectID, DashboardID: dashboardID, AuthoredRevision: authored, Definition: cloned, DefinitionHash: hash, SemanticModelID: semanticModelID, SemanticIdentity: semanticIdentity, CompiledAt: compiledAt}, nil
}

func (c CompiledRevision) Token() CompiledRevisionToken {
	return CompiledRevisionToken{AuthoredRevision: c.AuthoredRevision, DefinitionHash: c.DefinitionHash, SemanticModelID: c.SemanticModelID, SemanticIdentity: c.SemanticIdentity}
}

func (c CompiledRevision) Validate() error {
	if err := validateResourceID("compiled project id", c.ProjectID); err != nil {
		return err
	}
	if err := validateDashboardID(c.DashboardID); err != nil {
		return err
	}
	if err := c.AuthoredRevision.ValidateComplete(); err != nil {
		return err
	}
	if err := validateCompiledDefinition(c.DashboardID, c.Definition); err != nil {
		return err
	}
	hash, err := DefinitionHash(c.Definition)
	if err != nil {
		return err
	}
	if c.DefinitionHash != hash {
		return fmt.Errorf("%w: compiled definition hash does not match definition", ErrInvalidAuthoring)
	}
	if err := c.SemanticModelID.Validate(); err != nil {
		return fmt.Errorf("%w: compiled semantic model id: %v", ErrInvalidAuthoring, err)
	}
	if c.SemanticModelID.String() != c.Definition.SemanticModel {
		return fmt.Errorf("%w: compiled semantic model id %q does not match definition %q", ErrInvalidAuthoring, c.SemanticModelID, c.Definition.SemanticModel)
	}
	if err := c.Token().Validate(); err != nil {
		return err
	}
	if c.SemanticIdentity.ProjectID != c.ProjectID {
		return fmt.Errorf("%w: semantic serving identity project %q does not match compiled project %q", ErrInvalidAuthoring, c.SemanticIdentity.ProjectID, c.ProjectID)
	}
	if c.CompiledAt.IsZero() || c.CompiledAt.Location() != time.UTC {
		return fmt.Errorf("%w: compiled_at must be a non-zero UTC timestamp", ErrInvalidAuthoring)
	}
	return nil
}

// Clone returns an immutable-boundary copy detached from caller-owned maps,
// slices, and interface-backed values in the definition.
func (c CompiledRevision) Clone() (CompiledRevision, error) {
	cloned, err := cloneDefinition(c.Definition)
	if err != nil {
		return CompiledRevision{}, err
	}
	c.Definition = cloned
	return c, nil
}

func validateCompiledDefinition(dashboardID DashboardID, definition dashboarddefinition.Definition) error {
	if strings.TrimSpace(definition.ID) == "" {
		return fmt.Errorf("%w: compiled definition id is required", ErrInvalidAuthoring)
	}
	if definition.ID != dashboardID.String() {
		return fmt.Errorf("%w: compiled definition id %q does not match dashboard %q", ErrInvalidAuthoring, definition.ID, dashboardID)
	}
	if err := validateRequiredLifecycleValue("compiled definition semantic model", definition.SemanticModel); err != nil {
		return err
	}
	return nil
}

// DefinitionHash hashes encoding/json's canonical object representation. Go's
// encoder sorts map keys, so equivalent definitions produce the same lowercase
// sha256 digest regardless of insertion order.
func DefinitionHash(definition dashboarddefinition.Definition) (string, error) {
	canonical, err := json.Marshal(definition)
	if err != nil {
		return "", fmt.Errorf("%w: canonical compiled definition: %v", ErrInvalidAuthoring, err)
	}
	digest := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func cloneDefinition(definition dashboarddefinition.Definition) (dashboarddefinition.Definition, error) {
	canonical, err := json.Marshal(definition)
	if err != nil {
		return dashboarddefinition.Definition{}, fmt.Errorf("%w: clone compiled definition: %v", ErrInvalidAuthoring, err)
	}
	var cloned dashboarddefinition.Definition
	if err := json.Unmarshal(canonical, &cloned); err != nil {
		return dashboarddefinition.Definition{}, fmt.Errorf("%w: clone compiled definition: %v", ErrInvalidAuthoring, err)
	}
	return cloned, nil
}
