package module

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

func TestCredentialTransactionEvidenceRequiresExactAuthenticatedIdentity(t *testing.T) {
	actor := "00000000-0000-7000-8000-000000000011"
	token := access.APICredential{Principal: access.Principal{ID: actor}, Token: access.APIToken{ID: "00000000-0000-7000-8000-000000000012", PrincipalID: actor, TokenFingerprint: strings.Repeat("a", 64)}}
	session := access.CredentialEvidence{Class: "session", ID: "00000000-0000-7000-8000-000000000013", PrincipalID: actor, Fingerprint: strings.Repeat("b", 64)}
	tokenCtx := WithAPICredential(context.Background(), token)
	sessionCtx := withSessionCredentialEvidence(context.Background(), session)
	for _, item := range []struct {
		ctx           context.Context
		class, id, fp string
	}{{tokenCtx, "api_token", token.Token.ID, token.Token.TokenFingerprint}, {sessionCtx, "session", session.ID, session.Fingerprint}} {
		issuer, err := CredentialTransactionEvidence(item.ctx, actor)
		if err != nil || issuer.PrincipalID != actor || issuer.Credential.Class != item.class || issuer.Credential.ID != item.id || issuer.Credential.Fingerprint != item.fp {
			t.Fatalf("exact nonsecret evidence lost: %+v %v", issuer, err)
		}
	}
	invalid := []context.Context{nil, context.Background(), withSessionCredentialEvidence(tokenCtx, session), WithPrincipal(tokenCtx, Principal{ID: actor, DevBypass: true})}
	for _, mutate := range []func(*access.APICredential){
		func(c *access.APICredential) { c.Principal.ID = "different" }, func(c *access.APICredential) { c.Token.PrincipalID = "different" },
		func(c *access.APICredential) { c.Token.TokenFingerprint = "" }, func(c *access.APICredential) { c.Authoring = &access.AuthoringSession{} },
	} {
		changed := token
		mutate(&changed)
		invalid = append(invalid, WithAPICredential(context.Background(), changed))
	}
	for _, mutate := range []func(*access.CredentialEvidence){
		func(e *access.CredentialEvidence) { e.Class = "workload" }, func(e *access.CredentialEvidence) { e.PrincipalID = "different" }, func(e *access.CredentialEvidence) { e.Fingerprint = "" },
	} {
		changed := session
		mutate(&changed)
		invalid = append(invalid, withSessionCredentialEvidence(context.Background(), changed))
	}
	for index, ctx := range invalid {
		if _, err := CredentialTransactionEvidence(ctx, actor); !errors.Is(err, access.ErrForbidden) {
			t.Fatalf("invalid context %d accepted: %v", index, err)
		}
	}
	if _, err := CredentialTransactionEvidence(tokenCtx, "00000000-0000-7000-8000-000000000099"); !errors.Is(err, access.ErrForbidden) {
		t.Fatal("actor substitution accepted")
	}
}
