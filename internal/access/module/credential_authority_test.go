package module

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestCredentialAuthorityRecheckerReloadsAPITokenAttenuationAndRevocation(t *testing.T) {
	pair := credentialAuthorityTestPair(t, access.ActionConnectionManage)
	current := access.APIToken{
		ID: "token-1", PrincipalID: "principal-1", TokenFingerprint: "fingerprint-1",
		PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{pair},
		ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano),
	}
	reader := &credentialAuthorityTokenReader{token: current}
	check := CredentialAuthorityRechecker(reader, nil)
	ctx := WithAPICredential(context.Background(), access.APICredential{
		Principal: access.Principal{ID: current.PrincipalID},
		Token:     current,
	})
	if err := check(ctx, current.PrincipalID, pair); err != nil {
		t.Fatalf("current token authority = %v", err)
	}

	reader.token.Permissions = nil
	if err := check(ctx, current.PrincipalID, pair); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("attenuated token authority = %v, want forbidden", err)
	}
	reader.token = current
	reader.token.RevokedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := check(ctx, current.PrincipalID, pair); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("revoked token authority = %v, want forbidden", err)
	}
}

func TestCredentialAuthorityRecheckerReloadsBrowserSessionRevocation(t *testing.T) {
	pair := credentialAuthorityTestPair(t, access.ActionConnectionUse)
	current := access.Session{
		ID: "session-1", PrincipalID: "principal-1", TokenFingerprint: "fingerprint-1",
		ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano),
	}
	reader := &credentialAuthoritySessionReader{session: current}
	check := CredentialAuthorityRechecker(nil, reader)
	ctx := withSessionCredentialEvidence(context.Background(), access.CredentialEvidence{
		Class: "session", ID: current.ID, Fingerprint: current.TokenFingerprint,
		PrincipalID: current.PrincipalID,
	})
	if err := check(ctx, current.PrincipalID, pair); err != nil {
		t.Fatalf("current browser session authority = %v", err)
	}
	reader.err = access.ErrForbidden
	if err := check(ctx, current.PrincipalID, pair); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("revoked browser session authority = %v, want forbidden", err)
	}
	reader.err = nil
	reader.session.RevokedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := check(ctx, current.PrincipalID, pair); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("revoked browser session record = %v, want forbidden", err)
	}
}

func TestCredentialAuthorityRecheckerFailsClosedWithoutCurrentEvidence(t *testing.T) {
	pair := credentialAuthorityTestPair(t, access.ActionConnectionManage)
	check := CredentialAuthorityRechecker(nil, nil)
	if err := check(context.Background(), "principal-1", pair); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("missing credential evidence = %v, want forbidden", err)
	}
}

func TestCredentialAuthorityRecheckerRejectsAmbiguousEvidenceAndAllowsExplicitDevBypass(t *testing.T) {
	pair := credentialAuthorityTestPair(t, access.ActionConnectionManage)
	check := CredentialAuthorityRechecker(nil, nil)
	ctx := WithAPICredential(context.Background(), access.APICredential{
		Principal: access.Principal{ID: "principal-1"},
		Token:     access.APIToken{ID: "token-1", PrincipalID: "principal-1", TokenFingerprint: "fingerprint-1"},
	})
	ctx = withSessionCredentialEvidence(ctx, access.CredentialEvidence{
		Class: "session", ID: "session-1", Fingerprint: "fingerprint-2", PrincipalID: "principal-1",
	})
	if err := check(ctx, "principal-1", pair); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("ambiguous credential evidence = %v, want forbidden", err)
	}

	dev := WithPrincipal(context.Background(), Principal{ID: "principal-1", DevBypass: true})
	if err := check(dev, "principal-1", pair); err != nil {
		t.Fatalf("explicit development bypass = %v", err)
	}
}

func credentialAuthorityTestPair(t *testing.T, action access.Action) access.PermissionPair {
	t.Helper()
	resource, err := access.NewResourceRef(projectgraph.ResourceID("connection_one"), projectgraph.KindConnection)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := access.NewExactPermissionPair(action, projectgraph.ResourceID("project_one"), resource)
	if err != nil {
		t.Fatal(err)
	}
	return pair
}

type credentialAuthorityTokenReader struct {
	token access.APIToken
	err   error
}

func (reader *credentialAuthorityTokenReader) APITokenAuthorityEvidence(context.Context, string, string, time.Time) (access.APIToken, error) {
	return reader.token, reader.err
}

type credentialAuthoritySessionReader struct {
	session access.Session
	err     error
}

func (reader *credentialAuthoritySessionReader) SessionAuthorityEvidence(context.Context, string, string, string, time.Time) (access.Session, error) {
	return reader.session, reader.err
}
