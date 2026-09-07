package identityledger

import "testing"

// Captured before adding registry-bound evidence. A replay-compatible change
// must keep this historical v2 preimage byte-for-byte stable.
func TestLegacyPolicyEvidenceDigestRemainsFrozen(t *testing.T) {
	evidence := policyTestExistingEvidence(t)
	const expected = "sha256:db672d5e4b28fc00d44b2d2ecdafd51f0141bfdbb6a911b614fe3f343e9112d2"
	if evidence.Version != 2 || evidence.EvidenceDigest != expected {
		t.Fatalf("historical v2 evidence changed: version=%d digest=%s", evidence.Version, evidence.EvidenceDigest)
	}
}
