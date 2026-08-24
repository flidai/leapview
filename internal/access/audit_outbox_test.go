package access

import (
	"errors"
	"strings"
	"testing"
)

func validAuditIntent() AuditIntent {
	return AuditIntent{
		EventID:           "event-1",
		Source:            "access",
		Operation:         "principal.create",
		PrincipalID:       "principal-1",
		Action:            "principal.created",
		ResourceKind:      "principal",
		ResourceID:        "principal-1",
		Capability:        CapabilityResourceManage,
		Outcome:           "success",
		RequestID:         "request-1",
		CorrelationID:     "correlation-1",
		AggregateKey:      "principal:principal-1",
		AggregateSequence: 1,
		MetadataJSON:      `{"z":2,"a":1}`,
	}
}

func TestAuditIntentCanonicalizeNormalizesMetadataAndRejectsInvalidFields(t *testing.T) {
	intent := validAuditIntent()
	canonical, err := intent.Canonicalize()
	if err != nil {
		t.Fatal(err)
	}
	if canonical.MetadataJSON != `{"a":1,"z":2}` {
		t.Fatalf("canonical metadata = %q, want sorted object", canonical.MetadataJSON)
	}

	tests := []struct {
		name   string
		mutate func(*AuditIntent)
	}{
		{"event id whitespace", func(i *AuditIntent) { i.EventID = " event-1" }},
		{"event id punctuation", func(i *AuditIntent) { i.EventID = "event?1" }},
		{"source empty", func(i *AuditIntent) { i.Source = "" }},
		{"operation control", func(i *AuditIntent) { i.Operation = "principal\ncreate" }},
		{"outcome too long", func(i *AuditIntent) { i.Outcome = strings.Repeat("o", 65) }},
		{"principal too long", func(i *AuditIntent) { i.PrincipalID = strings.Repeat("p", 257) }},
		{"aggregate too long", func(i *AuditIntent) { i.AggregateKey = strings.Repeat("a", 513) }},
		{"request whitespace", func(i *AuditIntent) { i.RequestID = " request-1" }},
		{"request too long", func(i *AuditIntent) { i.RequestID = strings.Repeat("r", 257) }},
		{"negative sequence", func(i *AuditIntent) { i.AggregateSequence = -1 }},
		{"invalid capability", func(i *AuditIntent) { i.Capability = Capability("not-a-capability") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalid := intent
			test.mutate(&invalid)
			if _, err := invalid.Canonicalize(); err == nil {
				t.Fatal("invalid audit intent was accepted")
			}
		})
	}
}

func TestAuditIntentCanonicalizeMetadataValidation(t *testing.T) {
	for _, metadata := range []string{
		`{"a":1} {}`,
		`{"a":`,
		`{"a":1,"a":2}`,
		`[]`,
		`null`,
		`{"password":"redacted"}`,
		`{"passwordHash":"redacted"}`,
		`{"access-token":"redacted"}`,
		`{"nested":[{"raw_sql":"SELECT 1"}]}`,
	} {
		intent := validAuditIntent()
		intent.MetadataJSON = metadata
		if _, err := intent.Canonicalize(); err == nil {
			t.Fatalf("accepted invalid metadata %q", metadata)
		}
	}

	intent := validAuditIntent()
	intent.MetadataJSON = ""
	canonical, err := intent.Canonicalize()
	if err != nil {
		t.Fatal(err)
	}
	if canonical.MetadataJSON != "{}" {
		t.Fatalf("empty metadata = %q, want {}", canonical.MetadataJSON)
	}

	tooLarge := validAuditIntent()
	tooLarge.MetadataJSON = `{"value":"` + strings.Repeat("x", MaxAuditIntentMetadataBytes) + `"}`
	if _, err := tooLarge.Canonicalize(); err == nil {
		t.Fatal("oversized metadata was accepted")
	}
}

func TestAuditIntentCanonicalizeAllowsSystemEventsWithoutPrincipalOrCapability(t *testing.T) {
	intent := validAuditIntent()
	intent.PrincipalID = ""
	intent.Capability = ""
	canonical, err := intent.Canonicalize()
	if err != nil {
		t.Fatalf("system audit intent rejected: %v", err)
	}
	if canonical.PrincipalID != "" || canonical.Capability != "" {
		t.Fatalf("system identity changed during canonicalization: %#v", canonical)
	}
}

func TestAuditIntentPayloadDigestIsCanonicalAndPayloadSensitive(t *testing.T) {
	first := validAuditIntent()
	second := first
	second.MetadataJSON = ` { "a": 1, "z": 2 } `
	firstDigest, err := first.PayloadDigest()
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := second.PayloadDigest()
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest != secondDigest {
		t.Fatalf("equivalent metadata digests differ: %q != %q", firstDigest, secondDigest)
	}
	if !strings.HasPrefix(firstDigest, "sha256:") || len(firstDigest) != len("sha256:")+64 {
		t.Fatalf("digest = %q, want sha256 plus 64 hex characters", firstDigest)
	}

	for name, mutate := range map[string]func(*AuditIntent){
		"event":     func(i *AuditIntent) { i.EventID = "event-2" },
		"operation": func(i *AuditIntent) { i.Operation = "principal.update" },
		"outcome":   func(i *AuditIntent) { i.Outcome = "failure" },
		"metadata":  func(i *AuditIntent) { i.MetadataJSON = `{"a":2}` },
	} {
		t.Run(name, func(t *testing.T) {
			changed := first
			mutate(&changed)
			changedDigest, err := changed.PayloadDigest()
			if err != nil {
				t.Fatal(err)
			}
			if changedDigest == firstDigest {
				t.Fatal("payload mutation did not change digest")
			}
		})
	}

	if _, err := (AuditIntent{}).PayloadDigest(); err == nil {
		t.Fatal("zero audit intent digest succeeded")
	} else if errors.Is(err, ErrAuditIntentConflict) {
		t.Fatal("zero audit intent returned a storage conflict")
	}
}
