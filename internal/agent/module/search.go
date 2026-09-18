package module

import (
	"errors"
	"net/http"
	"strings"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/agent"
	agenttools "github.com/flidai/leapview/internal/agent/tools"
	"github.com/flidai/leapview/internal/agent/ui"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// catalogReferenceKinds is the complete model-facing graph taxonomy. Search
// intentionally does not expose derived UI objects such as pages, visuals, or
// fields: callers receive stable graph refs and can pass those exact refs to a
// governed tool on a later turn.
var catalogReferenceKinds = []agenttools.CatalogType{
	agenttools.CatalogType(projectgraph.KindProjectNamespace), agenttools.CatalogType(projectgraph.KindConnection),
	agenttools.CatalogType(projectgraph.KindSource), agenttools.CatalogType(projectgraph.KindModel),
	agenttools.CatalogType(projectgraph.KindSemanticModel), agenttools.CatalogType(projectgraph.KindPipeline),
	agenttools.CatalogType(projectgraph.KindDashboard),
}

func (m *Module) SearchReferences(r *http.Request, _ agent.TurnContext, query string, limit int) ([]ui.AgentReferenceSignal, error) {
	if m == nil || m.catalog == nil {
		return nil, errors.New("catalog is not configured")
	}
	principal := Principal{}
	if m.currentPrincipal != nil {
		if current, ok := m.currentPrincipal(r); ok {
			principal = current
			principal.ID = strings.TrimSpace(principal.ID)
		}
	}
	if principal.ID == "" {
		return nil, errors.New("catalog principal is unavailable")
	}
	projectID, err := m.activeProjectID(r.Context())
	if err != nil {
		return nil, err
	}
	scope := agenttools.Scope{ProjectID: projectID, PrincipalID: principal.ID, DevAuthBypass: principal.DevAuthBypass}
	if m.currentCredential != nil {
		if credential, ok := m.currentCredential(r); ok {
			scope.Credential = toolsCredentialScope(credential)
		}
	}
	query = strings.TrimSpace(query)
	var page agenttools.CatalogPage
	catalog := credentialCatalog{base: m.catalog}
	if query == "" {
		page, err = catalog.List(r.Context(), scope, agenttools.CatalogListRequest{ChildKinds: catalogReferenceKinds, Limit: limit})
	} else {
		page, err = catalog.Search(r.Context(), scope, agenttools.CatalogSearchRequest{Query: query, Kinds: catalogReferenceKinds, Limit: limit})
	}
	if err != nil {
		return nil, err
	}
	out := make([]ui.AgentReferenceSignal, 0, len(page.Items))
	for _, item := range page.Items {
		out = append(out, referenceSignal(item))
	}
	return out, nil
}

func toolsCredentialScope(credential access.APICredential) agenttools.CredentialScope {
	if credential.Authoring != nil {
		capabilities := make([]string, len(credential.Authoring.Scope.Capabilities))
		for index, capability := range credential.Authoring.Scope.Capabilities {
			capabilities[index] = string(capability)
		}
		return agenttools.CredentialScope{ProjectID: credential.Authoring.Scope.ProjectID.String(), Capabilities: capabilities, Restricted: true}
	}
	if strings.TrimSpace(credential.Token.ID) == "" {
		return agenttools.CredentialScope{}
	}
	capabilities := make([]string, len(credential.Token.Capabilities))
	for index, capability := range credential.Token.Capabilities {
		capabilities[index] = string(capability)
	}
	var permissions []access.PermissionPair
	if credential.Token.Permissions != nil {
		permissions = append(make([]access.PermissionPair, 0, len(credential.Token.Permissions)), credential.Token.Permissions...)
	}
	return agenttools.CredentialScope{Capabilities: capabilities, PermissionProfile: credential.Token.PermissionProfile, Permissions: permissions, Restricted: true}
}

func referenceSignal(item agenttools.CatalogItem) ui.AgentReferenceSignal {
	return ui.AgentReferenceSignal{
		Reference: ui.AgentReferenceKeySignal{Kind: string(item.Ref.Kind), ID: item.Ref.ID},
		Name:      item.Name, Description: ui.Optional(item.Description),
		Hierarchy: []string{}, Locations: []ui.AgentReferenceLocationSignal{}, Context: []string{},
	}
}

// TurnReferenceFromCatalog creates a server-trusted context item from a
// catalog result. The catalog item is already authorization-filtered against
// the active serving snapshot; no browser-supplied metadata is copied.
func TurnReferenceFromCatalog(item agenttools.CatalogItem, projectID string) agent.TurnReference {
	if projectID == "" && item.Ref.Kind == agenttools.CatalogType(projectgraph.KindProjectNamespace) {
		projectID = item.Ref.ID
	}
	return agent.TurnReference{
		Reference: agent.TurnReferenceKey{Kind: string(item.Ref.Kind), ID: item.Ref.ID},
		Name:      item.Name, Description: item.Description,
		Resource:  agent.TurnReferenceResource{ID: projectID, Name: projectID},
		Hierarchy: []string{projectID}, Context: []string{},
	}
}
