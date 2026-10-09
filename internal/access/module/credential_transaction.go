package module

import (
	"context"

	"github.com/flidai/leapview/internal/access"
)

// CredentialTransactionEvidence extracts only the authenticated credential's
// nonsecret identity. The transaction-bound repository must still lock and
// recheck current validity, principal authority and the live permission ceiling.
func CredentialTransactionEvidence(ctx context.Context, actor string) (access.GrantIssuerEvidence, error) {
	if ctx == nil {
		return access.GrantIssuerEvidence{}, access.ErrForbidden
	}
	if principal, ok := PrincipalFromContext(ctx); ok && (principal.DevBypass || principal.ID != actor) {
		return access.GrantIssuerEvidence{}, access.ErrForbidden
	}
	api, hasAPI := APICredentialFromContext(ctx)
	session, hasSession := SessionCredentialEvidenceFromContext(ctx)
	if hasAPI == hasSession {
		return access.GrantIssuerEvidence{}, access.ErrForbidden
	}
	issuer := access.GrantIssuerEvidence{PrincipalID: actor}
	if hasAPI {
		if api.Authoring != nil || api.InitialPublisher != nil || api.Principal.ID != actor || api.Token.PrincipalID != actor {
			return access.GrantIssuerEvidence{}, access.ErrForbidden
		}
		issuer.Credential = access.GrantCredentialEvidence{Class: access.GrantCredentialClassAPIToken, ID: api.Token.ID, Fingerprint: api.Token.TokenFingerprint}
	} else {
		if (session.Class != "" && session.Class != access.GrantCredentialClassSession) || session.PrincipalID != actor {
			return access.GrantIssuerEvidence{}, access.ErrForbidden
		}
		issuer.Credential = access.GrantCredentialEvidence{Class: access.GrantCredentialClassSession, ID: session.ID, Fingerprint: session.Fingerprint}
	}
	if issuer.Validate() != nil {
		return access.GrantIssuerEvidence{}, access.ErrForbidden
	}
	return issuer, nil
}
