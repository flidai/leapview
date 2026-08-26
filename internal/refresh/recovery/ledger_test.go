package recovery

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestOccurrenceIdentityIsStableAndBindsImmutableIntent(t *testing.T) {
	input := EnqueueInput{
		ScheduleID: "weekly-prod", Scenario: "managed-instance", Operation: OperationRestore,
		PolicyVersion: "ubdr-v1", TargetScope: "instance:prod",
		ArtifactIdentity: "backup:sha256:" + strings.Repeat("a", 64),
		PlannedAt:        time.Date(2026, 8, 25, 5, 0, 0, 0, time.UTC), StaleAfter: 24 * time.Hour,
	}
	first, err := OccurrenceID(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := OccurrenceID(input)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("occurrence identity changed: %q != %q", first, second)
	}
	input.PolicyVersion = "ubdr-v2"
	changed, err := OccurrenceID(input)
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("policy version did not participate in occurrence identity")
	}
}

func TestFailureRedactionAndEvidenceReferencesRejectCredentialBearingValues(t *testing.T) {
	reason := RedactFailure(errors.New("restore failed Authorization: Bearer abc.def password=hunter2 token=secret"))
	for _, secret := range []string{"abc.def", "hunter2", "secret"} {
		if strings.Contains(reason, secret) {
			t.Fatalf("redacted failure contains %q: %s", secret, reason)
		}
	}
	_, err := CanonicalEvidenceReferences([]EvidenceReference{{
		Kind: "report", URI: "https://user:secret@example.test/report.json",
		SHA256: strings.Repeat("a", 64),
	}})
	if err == nil {
		t.Fatal("credential-bearing evidence URI was accepted")
	}
	if err := ValidateArtifactIdentity("oci://user:secret@ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)); err == nil {
		t.Fatal("credential-bearing artifact identity was accepted")
	}
	encoded, err := EncodeEvidenceReferences(nil)
	if err != nil {
		t.Fatal(err)
	}
	if encoded != "[]" {
		t.Fatalf("empty evidence encoded as %s, want []", encoded)
	}
}

func TestStatusMetricsHaveOnlyBoundedLabels(t *testing.T) {
	age := int64(60)
	snapshot := StatusSnapshot{
		Due: 1, Overdue: 2, Running: 3, Failed: 4,
		Operations: []OperationStatus{{Operation: OperationRestore, LastSuccessAgeSeconds: &age}},
	}
	for _, metric := range snapshot.Metrics() {
		for label := range metric.Labels {
			if label != "operation" && label != "state" {
				t.Fatalf("metric %s has unbounded label %q", metric.Name, label)
			}
		}
		for _, value := range metric.Labels {
			if strings.Contains(value, "occurrence") || strings.Contains(value, "instance") {
				t.Fatalf("metric %s exposes mutable identifier label %q", metric.Name, value)
			}
		}
	}
}
