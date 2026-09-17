package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/jobs"
)

type jobAuthorityTokenReader struct {
	token access.APIToken
	err   error
}

func (r jobAuthorityTokenReader) APITokenAuthorityEvidence(context.Context, string, string, time.Time) (access.APIToken, error) {
	return r.token, r.err
}

type jobAuthoritySessionReader struct {
	session access.Session
	err     error
}

func (r jobAuthoritySessionReader) SessionAuthorityEvidence(context.Context, string, string, string, time.Time) (access.Session, error) {
	return r.session, r.err
}

func testCallerAuthority(t *testing.T, expiresAt time.Time) jobs.AuthorityEnvelope {
	t.Helper()
	projectID, err := projectgraph.NewResourceID("project_sales")
	if err != nil {
		t.Fatal(err)
	}
	pipelineID, err := projectgraph.NewResourceID("pipeline_daily")
	if err != nil {
		t.Fatal(err)
	}
	resource, err := access.NewResourceRef(pipelineID, projectgraph.KindPipeline)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := access.NewExactPermissionPair(access.ActionPipelineRun, projectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	return jobs.AuthorityEnvelope{
		Profile: jobs.AuthorityEnvelopeProfile, Mode: jobs.CallerAuthorityMode,
		ActorPrincipalID: "principal-1", ExecutionPrincipalID: "principal-1",
		Credential:  &jobs.CredentialEvidence{Class: jobs.CredentialClassAPIToken, ID: "token-1", Fingerprint: "fingerprint-1", ExpiresAt: expiresAt.UTC()},
		Target:      jobs.AuthorityTarget{ProjectID: projectID.String(), Environment: "prod", ResourceKind: string(projectgraph.KindPipeline), ResourceID: pipelineID.String()},
		Permissions: []access.PermissionPair{pair},
	}
}

func testAuthorityToken(authority jobs.AuthorityEnvelope) access.APIToken {
	return access.APIToken{
		ID: authority.Credential.ID, PrincipalID: authority.ActorPrincipalID,
		TokenFingerprint:  authority.Credential.Fingerprint,
		PermissionProfile: access.PermissionCatalogProfile,
		Permissions:       authority.Permissions,
		ExpiresAt:         authority.Credential.ExpiresAt.Format(time.RFC3339Nano),
	}
}

func TestCallerAuthorityRevalidatorRejectsQueuedCredentialExpiry(t *testing.T) {
	expiresAt := time.Now().UTC().Add(time.Hour)
	authority := testCallerAuthority(t, expiresAt)
	revalidator := newCallerAuthorityRevalidator(jobAuthorityTokenReader{err: access.ErrForbidden}, nil, func(context.Context, string, projectgraph.ResourceID, string, access.ResourceRef, access.Capability) (bool, error) {
		return true, nil
	})
	err := revalidator.Revalidate(t.Context(), authority)
	if !errors.Is(err, jobs.ErrAuthorityInvalid) {
		t.Fatalf("expiry revalidation error = %v, want authority invalid", err)
	}
}

func TestCallerAuthorityRevalidatorRejectsRevokedQueuedCredential(t *testing.T) {
	authority := testCallerAuthority(t, time.Now().UTC().Add(time.Hour))
	revalidator := newCallerAuthorityRevalidator(jobAuthorityTokenReader{token: testAuthorityToken(authority), err: access.ErrForbidden}, nil, func(context.Context, string, projectgraph.ResourceID, string, access.ResourceRef, access.Capability) (bool, error) {
		return true, nil
	})
	err := revalidator.Revalidate(t.Context(), authority)
	if !errors.Is(err, jobs.ErrAuthorityInvalid) {
		t.Fatalf("revocation revalidation error = %v, want authority invalid", err)
	}
}

func TestCallerAuthorityRevalidatorChecksExactPipelineResource(t *testing.T) {
	authority := testCallerAuthority(t, time.Now().UTC().Add(time.Hour))
	revalidator := newCallerAuthorityRevalidator(jobAuthorityTokenReader{token: testAuthorityToken(authority)}, nil, func(_ context.Context, _ string, projectID projectgraph.ResourceID, _ string, resource access.ResourceRef, capability access.Capability) (bool, error) {
		return projectID.String() == authority.Target.ProjectID && resource.ID().String() == authority.Target.ResourceID && resource.Kind() == projectgraph.KindPipeline && capability == access.CapabilityResourceUse, nil
	})
	if err := revalidator.Revalidate(t.Context(), authority); err != nil {
		t.Fatalf("exact resource revalidation error = %v", err)
	}
	denying := newCallerAuthorityRevalidator(jobAuthorityTokenReader{token: testAuthorityToken(authority)}, nil, func(context.Context, string, projectgraph.ResourceID, string, access.ResourceRef, access.Capability) (bool, error) {
		return false, nil
	})
	if err := denying.Revalidate(t.Context(), authority); !errors.Is(err, jobs.ErrAuthorityInvalid) {
		t.Fatalf("unrelated-resource revalidation error = %v, want authority invalid", err)
	}
}

func browserSessionAuthority(authority jobs.AuthorityEnvelope) jobs.AuthorityEnvelope {
	authority.Credential.Class = "session"
	return authority
}

func testAuthoritySession(authority jobs.AuthorityEnvelope) access.Session {
	return access.Session{ID: authority.Credential.ID, PrincipalID: authority.ActorPrincipalID, Kind: access.SessionKindBrowser, TokenFingerprint: authority.Credential.Fingerprint, ExpiresAt: authority.Credential.ExpiresAt.Format(time.RFC3339Nano)}
}

func TestCallerAuthorityRevalidatorRejectsExpiredBrowserSession(t *testing.T) {
	authority := browserSessionAuthority(testCallerAuthority(t, time.Now().UTC().Add(time.Hour)))
	session := testAuthoritySession(authority)
	session.ExpiresAt = time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	revalidator := newCallerAuthorityRevalidator(nil, jobAuthoritySessionReader{session: session}, func(context.Context, string, projectgraph.ResourceID, string, access.ResourceRef, access.Capability) (bool, error) {
		return true, nil
	})
	if err := revalidator.Revalidate(t.Context(), authority); !errors.Is(err, jobs.ErrAuthorityInvalid) {
		t.Fatalf("expired browser-session revalidation error = %v, want authority invalid", err)
	}
}

func TestCallerAuthorityRevalidatorRejectsRevokedBrowserSession(t *testing.T) {
	authority := browserSessionAuthority(testCallerAuthority(t, time.Now().UTC().Add(time.Hour)))
	revalidator := newCallerAuthorityRevalidator(nil, jobAuthoritySessionReader{session: testAuthoritySession(authority), err: access.ErrForbidden}, func(context.Context, string, projectgraph.ResourceID, string, access.ResourceRef, access.Capability) (bool, error) {
		return true, nil
	})
	if err := revalidator.Revalidate(t.Context(), authority); !errors.Is(err, jobs.ErrAuthorityInvalid) {
		t.Fatalf("revoked browser-session revalidation error = %v, want authority invalid", err)
	}
}

func TestCallerAuthorityRevalidatorAcceptsLiveBrowserSession(t *testing.T) {
	authority := browserSessionAuthority(testCallerAuthority(t, time.Now().UTC().Add(time.Hour)))
	revalidator := newCallerAuthorityRevalidator(nil, jobAuthoritySessionReader{session: testAuthoritySession(authority)}, func(_ context.Context, _ string, projectID projectgraph.ResourceID, environment string, resource access.ResourceRef, capability access.Capability) (bool, error) {
		return projectID.String() == authority.Target.ProjectID && environment == authority.Target.Environment && resource.ID().String() == authority.Target.ResourceID && capability == access.CapabilityResourceUse, nil
	})
	if err := revalidator.Revalidate(t.Context(), authority); err != nil {
		t.Fatalf("live browser-session revalidation error = %v", err)
	}
}
