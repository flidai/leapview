package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/credential"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRefreshRuntimeCredentialReaderRevalidatesCapturedSession(t *testing.T) {
	for _, scenario := range []string{"valid", "revoked after storage", "expired evidence"} {
		t.Run(scenario, func(t *testing.T) {
			factory, job, resource, reference, repository, keys, tokens := refreshCredentialFixture(t)
			expiresAt := job.Authority.Credential.ExpiresAt
			session := &refreshRuntimeSessionEvidence{session: access.Session{
				ID: uuid.NewString(), PrincipalID: job.PrincipalID, TokenFingerprint: strings.Repeat("a", 64),
				Kind: access.SessionKindBrowser, ExpiresAt: expiresAt.Format(time.RFC3339Nano),
			}}
			job.Authority.Credential = &jobs.CredentialEvidence{
				Class: jobs.CredentialClassSession, ID: session.session.ID,
				Fingerprint: session.session.TokenFingerprint, ExpiresAt: expiresAt,
			}
			current := refreshCurrentPermissionCheck(t, job)
			factory.authority.revalidator = newAuthorityRevalidator(nil, session, nil, current, current,
				factory.authority.instanceID, factory.authority.environment)
			switch scenario {
			case "revoked after storage":
				repository.afterRead = func() { session.revoked = true }
			case "expired evidence":
				session.session.ExpiresAt = time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
			}

			reader, err := factory.reader(job)
			require.NoError(t, err)
			called := false
			err = reader.WithCredential(t.Context(), job.Identity, resource, func(got credentialmodule.RuntimeCredentialReference, fields map[string]string) error {
				called = true
				require.Equal(t, reference, got)
				require.Equal(t, "queued-refresh-secret", fields["password"])
				return nil
			})
			if scenario == "valid" {
				require.NoError(t, err)
				require.True(t, called)
				require.Equal(t, 2, session.calls, "the same captured session is revalidated around storage")
				require.Zero(t, tokens.calls, "session authority never consults API-token evidence")
				require.Equal(t, 1, repository.calls)
				require.Equal(t, 1, keys.decryptCalls)
				return
			}

			require.ErrorIs(t, err, credential.ErrForbidden)
			require.False(t, called)
			require.Zero(t, tokens.calls, "session authority never consults API-token evidence")
			require.Zero(t, keys.decryptCalls, "denied session evidence must fail before decryption")
			if scenario == "revoked after storage" {
				require.Equal(t, 1, repository.calls)
				require.Equal(t, 2, session.calls)
			} else {
				require.Zero(t, repository.calls, "expired evidence is denied before storage")
				require.Equal(t, 1, session.calls)
			}
		})
	}
}

func TestRefreshRuntimeCredentialReaderRejectsStaleProviderAuthority(t *testing.T) {
	for _, scenario := range []string{"provider generation", "authorization snapshot"} {
		t.Run(scenario, func(t *testing.T) {
			factory, job, resource, _, repository, keys, _ := refreshCredentialFixture(t)
			base := factory.authority.provider.(*foregroundRuntimeAuthorityProvider).lease.(*foregroundRuntimeAuthorityLease)
			lease := &foregroundRuntimeAuthorityLease{identity: job.Identity, snapshot: base.snapshot}
			if scenario == "provider generation" {
				lease.identity.GenerationID = uuid.NewString()
				lease.snapshot = foregroundAuthoritySnapshot(t, lease.identity, job.Identity.ProjectID, job.PrincipalID, true)
			} else {
				staleSnapshotIdentity := job.Identity
				staleSnapshotIdentity.GenerationID = uuid.NewString()
				lease.snapshot = foregroundAuthoritySnapshot(t, staleSnapshotIdentity, job.Identity.ProjectID, job.PrincipalID, true)
			}
			provider := &foregroundCheckLeaseProvider{leases: []*foregroundRuntimeAuthorityLease{lease}}
			factory.authority.provider = provider

			reader, err := factory.reader(job)
			require.NoError(t, err)
			err = reader.WithCredential(t.Context(), job.Identity, resource, func(credentialmodule.RuntimeCredentialReference, map[string]string) error {
				t.Fatal("stale serving authority reached credential consumption")
				return nil
			})
			require.ErrorIs(t, err, credentialmodule.ErrRuntimeConflict)
			require.True(t, lease.released)
			require.Equal(t, 1, provider.calls)
			require.Zero(t, repository.calls)
			require.Zero(t, keys.decryptCalls)
		})
	}
}

func refreshCurrentPermissionCheck(t *testing.T, job refreshrun.JobRecord) func(context.Context, string, access.PermissionPair, string) (bool, error) {
	t.Helper()
	permissions := make([]access.PermissionPair, 0, len(job.Authority.Permissions))
	for _, pair := range job.Authority.Permissions {
		converted, err := access.FromContractPermissionPair(pair)
		require.NoError(t, err)
		permissions = append(permissions, converted)
	}
	return func(_ context.Context, principalID string, pair access.PermissionPair, environment string) (bool, error) {
		return principalID == job.PrincipalID && environment == job.Identity.Environment && access.PermissionSetAllows(permissions, pair), nil
	}
}

type refreshRuntimeSessionEvidence struct {
	session access.Session
	revoked bool
	calls   int
}

func (evidence *refreshRuntimeSessionEvidence) SessionAuthorityEvidence(_ context.Context, principalID, sessionID, fingerprint string, now time.Time) (access.Session, error) {
	evidence.calls++
	expiresAt, err := time.Parse(time.RFC3339Nano, evidence.session.ExpiresAt)
	if evidence.revoked || err != nil || !expiresAt.After(now) || evidence.session.PrincipalID != principalID ||
		evidence.session.ID != sessionID || evidence.session.TokenFingerprint != fingerprint {
		return access.Session{}, access.ErrForbidden
	}
	return evidence.session, nil
}

var _ access.SessionAuthorityEvidenceReader = (*refreshRuntimeSessionEvidence)(nil)
