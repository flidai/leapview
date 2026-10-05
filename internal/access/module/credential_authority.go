package module

import (
	"context"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
)

// CredentialAuthorityRechecker returns a request-scoped permission check that
// re-reads API token or browser-session lifecycle evidence from the durable
// access authority. The immutable context evidence identifies the credential;
// it never contains or needs the bearer secret.
func CredentialAuthorityRechecker(
	tokens access.APITokenAuthorityEvidenceReader,
	sessions access.SessionAuthorityEvidenceReader,
) func(context.Context, string, access.PermissionPair) error {
	return func(ctx context.Context, principalID string, pair access.PermissionPair) error {
		if ctx == nil || strings.TrimSpace(principalID) == "" || pair.Validate() != nil {
			return access.ErrForbidden
		}
		apiCredential, hasAPICredential := APICredentialFromContext(ctx)
		sessionEvidence, hasSessionEvidence := SessionCredentialEvidenceFromContext(ctx)
		if hasAPICredential && hasSessionEvidence {
			return access.ErrForbidden
		}
		if hasAPICredential {
			if tokens == nil || apiCredential.Authoring != nil || apiCredential.Token.ID == "" ||
				apiCredential.Token.PrincipalID != principalID || apiCredential.Principal.ID != principalID ||
				apiCredential.Token.TokenFingerprint == "" {
				return access.ErrForbidden
			}
			now := time.Now().UTC()
			current, err := tokens.APITokenAuthorityEvidence(ctx, principalID, apiCredential.Token.ID, now)
			if err != nil || current.ID != apiCredential.Token.ID || current.PrincipalID != principalID ||
				current.TokenFingerprint == "" || current.TokenFingerprint != apiCredential.Token.TokenFingerprint ||
				current.RevokedAt != "" || current.PermissionProfile != access.PermissionCatalogProfile || current.Permissions == nil ||
				access.ValidatePermissionPairs(current.Permissions) != nil ||
				!access.PermissionSetAllows(current.Permissions, pair) || !permissionExpiresAfter(current.ExpiresAt, now) {
				return access.ErrForbidden
			}
			return nil
		}
		if hasSessionEvidence {
			if sessions == nil || (sessionEvidence.Class != "" && sessionEvidence.Class != "session") ||
				sessionEvidence.ID == "" || sessionEvidence.Fingerprint == "" || sessionEvidence.PrincipalID != principalID {
				return access.ErrForbidden
			}
			now := time.Now().UTC()
			current, err := sessions.SessionAuthorityEvidence(ctx, principalID, sessionEvidence.ID, sessionEvidence.Fingerprint, now)
			if err != nil || current.ID != sessionEvidence.ID || current.PrincipalID != principalID ||
				current.TokenFingerprint != sessionEvidence.Fingerprint || current.RevokedAt != "" || !permissionExpiresAfter(current.ExpiresAt, now) {
				return access.ErrForbidden
			}
			return nil
		}
		if principal, ok := PrincipalFromContext(ctx); ok && principal.DevBypass && principal.ID == principalID {
			return nil
		}
		return access.ErrForbidden
	}
}

func permissionExpiresAfter(value string, now time.Time) bool {
	expiresAt, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && expiresAt.After(now)
}
