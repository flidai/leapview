package trustedclaims

import (
	"errors"
	"testing"
	"time"
)

// This qualifies the common fail-closed envelope, not provider-specific
// signature verification or production provider activation.
func TestEveryClaimSourceRequiresVerifierAndCurrentTrust(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	for _, source := range []SourceKind{SourceSAML, SourceOIDC, SourceEmbed, SourceServiceToken} {
		t.Run(string(source), func(t *testing.T) {
			evidence := RawEvidence{Source: source, Raw: []byte("untrusted-evidence")}
			envelope, err := Verify(t.Context(), evidence, nil, VerifyOptions{Now: now})
			if !errors.Is(err, ErrVerifierUnavailable) || envelope.Valid() {
				t.Fatalf("missing verifier: valid=%v error=%v", envelope.Valid(), err)
			}
			for _, failure := range []string{"verification-failure", "expired", "future"} {
				t.Run(failure, func(t *testing.T) {
					verifier := &testVerifier{kind: source, result: validVerifiedClaims(now)}
					var want error
					switch failure {
					case "verification-failure":
						verifier.err = errors.New("signature rejected")
						want = ErrVerificationFailed
					case "expired":
						verifier.result.ExpiresAt = now
						want = ErrEvidenceExpired
					case "future":
						verifier.result.IssuedAt = now.Add(time.Second)
						want = ErrEvidenceNotYetValid
					}
					envelope, err := Verify(t.Context(), evidence, verifier, VerifyOptions{Now: now})
					if !verifier.called || !errors.Is(err, want) || envelope.Valid() {
						t.Fatalf("invalid trust admitted: called=%v valid=%v error=%v", verifier.called, envelope.Valid(), err)
					}
				})
			}
		})
	}
}
