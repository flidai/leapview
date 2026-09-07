package catalogartifact

import (
	"strings"
	"testing"
)

func testCommitMarker() CommitMarker {
	return CommitMarker{
		SchemaVersion: CommitMarkerSchemaVersion,
		DeliveryID:    "delivery-1", GenerationID: "generation-1", AttemptID: "attempt-1",
		LeaseEpoch: 7, RequestDigest: "sha256:" + strings.Repeat("a", 64), PlanDigest: "sha256:" + strings.Repeat("b", 64),
		Project: "project-1", Environment: "prod", PhysicalPoolID: "pool-1",
	}
}

func TestCommitMarkerCanonicalJSONAndBounds(t *testing.T) {
	marker := testCommitMarker()
	canonical, err := marker.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema_version":1,"delivery_id":"delivery-1","generation_id":"generation-1","attempt_id":"attempt-1","lease_epoch":7,"request_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","plan_digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","project":"project-1","environment":"prod","physical_pool_id":"pool-1"}`
	if canonical != want {
		t.Fatalf("canonical marker = %s, want %s", canonical, want)
	}
	if _, err := ParseCommitMarker(canonical); err != nil {
		t.Fatalf("parse canonical marker: %v", err)
	}
	if _, err := ParseCommitMarker(canonical + " "); err == nil {
		t.Fatal("non-canonical marker accepted")
	}

	tooLong := marker
	tooLong.Project = strings.Repeat("x", MaxCommitMarkerFieldBytes+1)
	if _, err := tooLong.CanonicalJSON(); err == nil {
		t.Fatal("oversized marker field accepted")
	}
}

func TestDecodeCommitMarkerRejectsUnknownAndOversizedDocuments(t *testing.T) {
	canonical, err := testCommitMarker().CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	unknown := strings.TrimSuffix(canonical, "}") + `,"unexpected":true}`
	if _, err := DecodeCommitMarker([]byte(unknown)); err == nil {
		t.Fatal("unknown marker field accepted")
	}
	if _, err := DecodeCommitMarker([]byte(strings.Repeat("x", MaxCommitMarkerBytes+1))); err == nil {
		t.Fatal("oversized marker document accepted")
	}
}
