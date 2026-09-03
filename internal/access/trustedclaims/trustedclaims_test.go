package trustedclaims

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type testVerifier struct {
	kind      SourceKind
	result    VerifiedClaims
	err       error
	called    bool
	mutateRaw bool
}

func (verifier *testVerifier) SourceKind() SourceKind { return verifier.kind }

func (verifier *testVerifier) Verify(_ context.Context, raw []byte) (VerifiedClaims, error) {
	verifier.called = true
	if verifier.mutateRaw {
		raw[0] ^= 0xff
	}
	return verifier.result, verifier.err
}

func validVerifiedClaims(now time.Time) VerifiedClaims {
	return VerifiedClaims{
		Provider:              "provider-1",
		Issuer:                "https://issuer.example",
		Audience:              "leapview",
		Subject:               "subject-1",
		IssuedAt:              now.Add(-time.Minute),
		ExpiresAt:             now.Add(time.Minute),
		CredentialFingerprint: strings.Repeat("a", 64),
		Claims: []Claim{
			{Name: "department", Value: "sales"},
			{Name: "regions", Value: []string{"west", "east"}},
		},
	}
}

func TestVerifyCreatesOpaqueEnvelopeAndInvokesMatchingSourceVerifier(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	raw := []byte("signed-evidence")
	verifier := &testVerifier{kind: SourceOIDC, result: validVerifiedClaims(now)}
	envelope, err := Verify(context.Background(), RawEvidence{Source: SourceOIDC, Raw: raw}, verifier, VerifyOptions{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if !verifier.called || !envelope.Valid() {
		t.Fatalf("called=%v valid=%v", verifier.called, envelope.Valid())
	}
	if envelope.Source() != SourceOIDC || envelope.Provider() != "provider-1" || envelope.Issuer() != "https://issuer.example" || envelope.Audience() != "leapview" || envelope.Subject() != "subject-1" {
		t.Fatalf("identity accessors = %q/%q/%q/%q/%q", envelope.Source(), envelope.Provider(), envelope.Issuer(), envelope.Audience(), envelope.Subject())
	}
	if !envelope.IssuedAt().Equal(now.Add(-time.Minute)) || !envelope.ExpiresAt().Equal(now.Add(time.Minute)) || envelope.Fingerprint() != strings.Repeat("a", 64) {
		t.Fatalf("time/fingerprint accessors = %s/%s/%q", envelope.IssuedAt(), envelope.ExpiresAt(), envelope.Fingerprint())
	}
	if !reflect.DeepEqual(envelope.ClaimNames(), []string{"department", "regions"}) {
		t.Fatalf("claim names = %#v", envelope.ClaimNames())
	}
}

func TestVerifyRejectsEmptyEvidenceWithoutCallingVerifier(t *testing.T) {
	verifier := &testVerifier{kind: SourceOIDC, result: validVerifiedClaims(time.Now())}
	_, err := Verify(context.Background(), RawEvidence{Source: SourceOIDC}, verifier)
	if !errors.Is(err, ErrRawEvidenceEmpty) || verifier.called {
		t.Fatalf("err=%v called=%v", err, verifier.called)
	}
}

func TestVerifyRejectsVerifierMutationOfRawEvidence(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	verifier := &testVerifier{kind: SourceOIDC, result: validVerifiedClaims(now), mutateRaw: true}
	_, err := Verify(context.Background(), RawEvidence{Source: SourceOIDC, Raw: []byte("evidence")}, verifier, VerifyOptions{Now: now})
	if !errors.Is(err, ErrRawEvidenceMutated) {
		t.Fatalf("err=%v, want raw evidence mutation rejection", err)
	}
}

func TestVerifyRejectsUnknownTransportSourcesAndMismatchedVerifier(t *testing.T) {
	now := time.Now().UTC()
	verifier := &testVerifier{kind: SourceOIDC, result: validVerifiedClaims(now)}
	for _, source := range []SourceKind{"", "browser", "query", "header", "SAML", "saml "} {
		t.Run(string(source), func(t *testing.T) {
			verifier.called = false
			_, err := Verify(context.Background(), RawEvidence{Source: source, Raw: []byte("evidence")}, verifier, VerifyOptions{Now: now})
			if err == nil || verifier.called {
				t.Fatalf("err=%v called=%v", err, verifier.called)
			}
		})
	}
	for _, source := range []SourceKind{SourceSAML, SourceEmbed, SourceServiceToken} {
		verifier.called = false
		_, err := Verify(context.Background(), RawEvidence{Source: source, Raw: []byte("evidence")}, verifier, VerifyOptions{Now: now})
		if err == nil || verifier.called {
			t.Fatalf("source=%q err=%v called=%v", source, err, verifier.called)
		}
	}
}

func TestVerifyRejectsExpiredAndNotYetValidEvidence(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		edit func(*VerifiedClaims)
		want error
	}{
		{name: "expired", edit: func(claims *VerifiedClaims) { claims.ExpiresAt = now }, want: ErrEvidenceExpired},
		{name: "not yet valid", edit: func(claims *VerifiedClaims) { claims.IssuedAt = now.Add(time.Second) }, want: ErrEvidenceNotYetValid},
		{name: "reversed", edit: func(claims *VerifiedClaims) { claims.IssuedAt = now; claims.ExpiresAt = now.Add(-time.Second) }, want: ErrEvidenceTimeInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claims := validVerifiedClaims(now)
			tt.edit(&claims)
			_, err := Verify(context.Background(), RawEvidence{Source: SourceOIDC, Raw: []byte("evidence")}, &testVerifier{kind: SourceOIDC, result: claims}, VerifyOptions{Now: now})
			if !errors.Is(err, tt.want) {
				t.Fatalf("err=%v, want %v", err, tt.want)
			}
		})
	}
}

func TestVerifyRequiresBoundTrustIdentityAndSourceFingerprint(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		edit func(*VerifiedClaims)
	}{
		{name: "provider", edit: func(claims *VerifiedClaims) { claims.Provider = "" }},
		{name: "issuer", edit: func(claims *VerifiedClaims) { claims.Issuer = "" }},
		{name: "audience", edit: func(claims *VerifiedClaims) { claims.Audience = "" }},
		{name: "subject", edit: func(claims *VerifiedClaims) { claims.Subject = "" }},
		{name: "credential fingerprint", edit: func(claims *VerifiedClaims) { claims.CredentialFingerprint = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claims := validVerifiedClaims(now)
			tt.edit(&claims)
			_, err := Verify(context.Background(), RawEvidence{Source: SourceOIDC, Raw: []byte("evidence")}, &testVerifier{kind: SourceOIDC, result: claims}, VerifyOptions{Now: now})
			if !errors.Is(err, ErrTrustIdentityMissing) {
				t.Fatalf("err=%v, want trust identity missing", err)
			}
		})
	}

	claims := validVerifiedClaims(now)
	claims.TokenFingerprint = strings.Repeat("b", 64)
	claims.CredentialFingerprint = ""
	_, err := Verify(context.Background(), RawEvidence{Source: SourceServiceToken, Raw: []byte("evidence")}, &testVerifier{kind: SourceServiceToken, result: claims}, VerifyOptions{Now: now})
	if err != nil {
		t.Fatalf("service token with token fingerprint: %v", err)
	}
}

func TestVerifyRejectsMapsNestedListsNullsAndUnsupportedValuesWithoutCoercion(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	values := []struct {
		name  string
		value any
	}{
		{name: "map", value: map[string]any{"role": "admin"}},
		{name: "nested list", value: [][]string{{"admin"}}},
		{name: "null", value: nil},
		{name: "float", value: 1.5},
		{name: "invalid number", value: json.Number("null")},
		{name: "uint overflow", value: ^uint64(0)},
		{name: "function", value: func() {}},
		{name: "channel", value: make(chan int)},
		{name: "bytes", value: []byte("admin")},
		{name: "mixed list", value: []any{"admin", 1}},
		{name: "empty list", value: []string{}},
	}
	for _, tt := range values {
		t.Run(tt.name, func(t *testing.T) {
			claims := validVerifiedClaims(now)
			claims.Claims = []Claim{{Name: "role", Value: tt.value}}
			_, err := Verify(context.Background(), RawEvidence{Source: SourceOIDC, Raw: []byte("evidence")}, &testVerifier{kind: SourceOIDC, result: claims}, VerifyOptions{Now: now})
			if !errors.Is(err, ErrClaimInvalid) {
				t.Fatalf("err=%v, want claim invalid", err)
			}
		})
	}
}

func TestVerifyPreservesExactClaimNamesAndRejectsDuplicates(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	claims := validVerifiedClaims(now)
	claims.Claims = []Claim{{Name: "http://schemas.example/Role", Value: "admin"}}
	envelope, err := Verify(context.Background(), RawEvidence{Source: SourceSAML, Raw: []byte("evidence")}, &testVerifier{kind: SourceSAML, result: claims}, VerifyOptions{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if !envelope.HasClaim("http://schemas.example/Role") || envelope.HasClaim("http://schemas.example/role") {
		t.Fatal("claim lookup was not exact")
	}
	claims.Claims = append(claims.Claims, Claim{Name: "http://schemas.example/Role", Value: "other"})
	if _, err := Verify(context.Background(), RawEvidence{Source: SourceSAML, Raw: []byte("evidence")}, &testVerifier{kind: SourceSAML, result: claims}, VerifyOptions{Now: now}); !errors.Is(err, ErrClaimDuplicate) {
		t.Fatalf("duplicate err=%v", err)
	}
	for _, name := range []string{"", string([]byte{0xff}), "role\n", " role", strings.Repeat("a", MaxClaimNameBytes+1)} {
		claims := validVerifiedClaims(now)
		claims.Claims = []Claim{{Name: name, Value: "admin"}}
		if _, err := Verify(context.Background(), RawEvidence{Source: SourceOIDC, Raw: []byte("evidence")}, &testVerifier{kind: SourceOIDC, result: claims}, VerifyOptions{Now: now}); !errors.Is(err, ErrClaimNameInvalid) {
			t.Fatalf("name %q err=%v", name, err)
		}
	}
}

func TestVerifyRequiresCanonicalSHA256Fingerprint(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	for _, fingerprint := range []string{
		"credential-fingerprint",
		strings.Repeat("a", 63),
		strings.Repeat("A", 64),
		"sha256:" + strings.Repeat("a", 63),
		"SHA256:" + strings.Repeat("a", 64),
		"sha256:" + strings.Repeat("A", 64),
	} {
		claims := validVerifiedClaims(now)
		claims.CredentialFingerprint = fingerprint
		_, err := Verify(context.Background(), RawEvidence{Source: SourceOIDC, Raw: []byte("evidence")}, &testVerifier{kind: SourceOIDC, result: claims}, VerifyOptions{Now: now})
		if !errors.Is(err, ErrTrustIdentityMissing) {
			t.Fatalf("fingerprint %q err=%v, want trust identity missing", fingerprint, err)
		}
	}
	for _, fingerprint := range []string{strings.Repeat("a", 64), "sha256:" + strings.Repeat("a", 64)} {
		claims := validVerifiedClaims(now)
		claims.CredentialFingerprint = fingerprint
		if _, err := Verify(context.Background(), RawEvidence{Source: SourceOIDC, Raw: []byte("evidence")}, &testVerifier{kind: SourceOIDC, result: claims}, VerifyOptions{Now: now}); err != nil {
			t.Fatalf("fingerprint %q rejected: %v", fingerprint, err)
		}
	}
}

func TestEnvelopeAccessorsReturnCopies(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	claims := validVerifiedClaims(now)
	envelope, err := Verify(context.Background(), RawEvidence{Source: SourceEmbed, Raw: []byte("evidence")}, &testVerifier{kind: SourceEmbed, result: claims}, VerifyOptions{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	names := envelope.ClaimNames()
	names[0] = "changed"
	if envelope.ClaimNames()[0] != "department" {
		t.Fatal("claim names exposed mutable storage")
	}
	got, ok := envelope.Value("regions")
	if !ok {
		t.Fatal("regions claim missing")
	}
	values := got.([]any)
	values[0] = "changed"
	fresh, _ := envelope.Value("regions")
	if fresh.([]any)[0] == "changed" {
		t.Fatal("claim list exposed mutable storage")
	}
	all := envelope.Claims()
	all[0].Name = "changed"
	if envelope.Claims()[0].Name != "department" {
		t.Fatal("claims exposed mutable storage")
	}

	typ := reflect.TypeOf(envelope)
	for index := 0; index < typ.NumField(); index++ {
		if typ.Field(index).PkgPath == "" {
			t.Fatalf("Envelope field %q is exported", typ.Field(index).Name)
		}
	}
}
