package module

import (
	"context"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	queryauthz "github.com/flidai/leapview/internal/dashboard/queryauthz"
	"github.com/flidai/leapview/internal/dashboard/queryruntime"
)

type QueryPrincipal struct {
	ID        string
	DevBypass bool
}

type QueryAuthorizationConfig struct {
	InstanceID                string
	ResolveSemanticAttributes func(context.Context) (access.SemanticAttributeResolution, error)
	SnapshotFromContext       func(context.Context) (accesssnapshot.AuthorizationSnapshot, error)
	SubjectsFromContext       func(context.Context, string) ([]access.SubjectRef, error)
	PrincipalFromContext      func(context.Context) (QueryPrincipal, bool)
	CredentialFromContext     func(context.Context) (access.APICredential, bool)
	AuditRecorder             access.CanonicalAuditRecorder
}

func WithQueryAuthorization(metrics queryruntime.Metrics, config QueryAuthorizationConfig) queryruntime.Metrics {
	if metrics == nil || config.SnapshotFromContext == nil {
		return metrics
	}
	return queryauthz.New(metrics, queryauthz.Options{
		InstanceID:                config.InstanceID,
		ResolveSemanticAttributes: config.ResolveSemanticAttributes,
		SnapshotFromContext:       config.SnapshotFromContext,
		SubjectsFromContext:       config.SubjectsFromContext,
		PrincipalFromContext: func(ctx context.Context) (queryauthz.Principal, bool) {
			if config.PrincipalFromContext == nil {
				return queryauthz.Principal{}, false
			}
			principal, ok := config.PrincipalFromContext(ctx)
			return queryauthz.Principal{ID: principal.ID, DevBypass: principal.DevBypass}, ok
		},
		CredentialFromContext: config.CredentialFromContext,
		AuditRecorder:         config.AuditRecorder,
	})
}
