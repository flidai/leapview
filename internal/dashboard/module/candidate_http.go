package module

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/Yacobolo/toolbelt/pagestream"
	"github.com/flidai/leapview/internal/access"
	accesspolicy "github.com/flidai/leapview/internal/access/policy"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	queryauthz "github.com/flidai/leapview/internal/dashboard/queryauthz"
	dashboardsession "github.com/flidai/leapview/internal/dashboard/session"
	dashboardui "github.com/flidai/leapview/internal/dashboard/ui"
	"github.com/flidai/leapview/internal/platform/digest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type CandidateHTTPConfig struct {
	Metrics                  Metrics
	CandidateID              string
	OwnerPrincipalID         string
	ProjectID                projectgraph.ResourceID
	ArtifactDigest           string
	AuthorizationFingerprint string
	RouteBasePath            string
	Restrictions             []CandidateRestriction
}

type CandidateRestriction struct {
	ID             string
	Resource       access.ResourceRef
	Subject        *access.SubjectRef
	PolicyType     string
	ExpressionJSON string
}

// CandidateHTTP derives an isolated dashboard adapter from the shared
// Dashboard capability. The returned handler owns no runtime or policy state;
// those remain in the server-resolved candidate provider and query context.
func (m *Module) CandidateHTTP(config CandidateHTTPConfig) (HTTP, error) {
	if m == nil || config.Metrics == nil {
		return HTTP{}, fmt.Errorf("candidate dashboard metrics are required")
	}
	config.CandidateID = strings.TrimSpace(config.CandidateID)
	config.OwnerPrincipalID = strings.TrimSpace(config.OwnerPrincipalID)
	if err := config.ProjectID.Validate(); err != nil {
		return HTTP{}, fmt.Errorf("candidate project identity is invalid: %w", err)
	}
	config.ArtifactDigest = strings.TrimSpace(config.ArtifactDigest)
	config.AuthorizationFingerprint = strings.TrimSpace(config.AuthorizationFingerprint)
	config.RouteBasePath = strings.TrimSuffix(strings.TrimSpace(config.RouteBasePath), "/")
	if config.CandidateID == "" || config.OwnerPrincipalID == "" ||
		config.ProjectID == "" || config.RouteBasePath == "" {
		return HTTP{}, fmt.Errorf("candidate dashboard identity, owner, project, and route are required")
	}
	if err := digest.ValidateSHA256Identity(config.ArtifactDigest); err != nil {
		return HTTP{}, fmt.Errorf("candidate artifact digest is invalid: %w", err)
	}
	if err := digest.ValidateSHA256Identity(config.AuthorizationFingerprint); err != nil {
		return HTTP{}, fmt.Errorf("candidate authorization fingerprint is invalid: %w", err)
	}
	compiledRestrictions := make([]access.DataPolicy, len(config.Restrictions))
	for index, restriction := range config.Restrictions {
		if err := restriction.Resource.Validate(); err != nil {
			return HTTP{}, fmt.Errorf("candidate restriction %q resource is invalid: %w", restriction.ID, err)
		}
		if restriction.Subject != nil {
			if err := restriction.Subject.Validate(); err != nil {
				return HTTP{}, fmt.Errorf("candidate restriction %q subject is invalid: %w", restriction.ID, err)
			}
		}
		compiled, err := accesspolicy.Compile(restriction.ID, restriction.PolicyType, restriction.ExpressionJSON)
		if err != nil {
			return HTTP{}, fmt.Errorf("compile candidate restriction: %w", err)
		}
		compiledRestrictions[index] = access.DataPolicy{
			ID: restriction.ID, Resource: restriction.Resource, Subject: restriction.Subject,
			PolicyType:     restriction.PolicyType,
			ExpressionJSON: restriction.ExpressionJSON, Compiled: compiled,
		}
	}

	handler := m.handler
	handler.Metrics = config.Metrics
	handler.RouteScope = dashboardui.RouteScope{BasePath: config.RouteBasePath}
	handler.StreamNamespace = "candidate:" + config.CandidateID
	handler.AgentBootstrap = nil
	baseAnalyticalContext := handler.AnalyticalContext
	handler.AnalyticalContext = func(ctx context.Context) context.Context {
		if baseAnalyticalContext != nil {
			ctx = baseAnalyticalContext(ctx)
		}
		return queryauthz.WithCandidateQueryCapability(ctx, queryauthz.CandidateQueryCapability{
			CandidateID: config.CandidateID, OwnerPrincipalID: config.OwnerPrincipalID,
			ProjectID: config.ProjectID, PolicyDigest: config.AuthorizationFingerprint,
			Restrictions: append([]access.DataPolicy(nil), compiledRestrictions...),
		})
	}
	currentPrincipalID := handler.CurrentPrincipalID
	handler.SessionKey = func(
		r *http.Request,
		report dashboarddefinition.Definition,
		clientID, streamInstanceID string,
	) dashboardsession.Key {
		principalOrClient := clientID
		if currentPrincipalID != nil {
			if principalID := currentPrincipalID(r); principalID != "" {
				principalOrClient = principalID + ":" + clientID
			}
		}
		if principalOrClient == "" {
			principalOrClient = pagestream.ClientIDFromRequest(r, clientID)
		}
		return dashboardsession.Key{
			WorkspaceOrPublication: "candidate:" + config.CandidateID + ":" + config.ProjectID.String(),
			PrincipalOrClient:      principalOrClient,
			DashboardID:            report.ID,
			ServingStateID:         "candidate:" + config.CandidateID + ":" + config.ArtifactDigest,
			StreamInstanceID:       streamInstanceID,
		}
	}
	return handler, nil
}
