package module

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
)

func TestBrowserSessionEvidenceIsNotAnAPICredential(t *testing.T) {
	ctx := withSessionCredentialEvidence(context.Background(), access.CredentialEvidence{
		Class: "session", ID: "session-1", Fingerprint: "fingerprint-1", PrincipalID: "principal-1", ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	if _, ok := APICredentialFromContext(ctx); ok {
		t.Fatal("browser session evidence was classified as an API credential")
	}
	evidence, ok := SessionCredentialEvidenceFromContext(ctx)
	if !ok || evidence.Class != "session" || evidence.ID != "session-1" {
		t.Fatalf("browser session evidence = %#v, found=%t", evidence, ok)
	}
}

func TestBrowserSessionEvidenceDoesNotAttenuateNormalCapabilityProjection(t *testing.T) {
	principal := Principal{ID: "principal-1", Kind: access.PrincipalKindUser}
	capabilities := []access.Capability{access.CapabilityResourceRead}
	m := &Module{
		auth: &Auth{},
		currentEffectiveCapabilities: func(context.Context, string) ([]access.Capability, error) {
			return capabilities, nil
		},
	}
	ctx := WithPrincipal(context.Background(), principal)
	ctx = withSessionCredentialEvidence(ctx, access.CredentialEvidence{
		Class: "session", ID: "session-1", Fingerprint: "fingerprint-1", PrincipalID: principal.ID,
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	r := httptest.NewRequest(http.MethodGet, "/dashboards/dashboard:sales", nil).WithContext(ctx)
	got, err := m.RequestEffectiveCapabilities(r.Context(), r, principal.ID)
	if err != nil {
		t.Fatalf("request capability projection: %v", err)
	}
	if len(got) != len(capabilities) || got[0] != capabilities[0] {
		t.Fatalf("browser session changed normal capability projection: got %v, want %v", got, capabilities)
	}
}
