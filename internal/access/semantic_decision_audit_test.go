package access

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func testSemanticDecisionEvidence(allowed bool) SemanticDecisionEvidence {
	reason := ""
	if !allowed {
		reason = "required access grant is not satisfied"
	}
	return SemanticDecisionEvidence{
		Version: 1, InstanceID: "instance-audit", ActorPrincipalID: "principal-audit",
		SemanticModelDigest: "sha256:" + strings.Repeat("a", 64),
		Registry: SemanticAuditRevision{Profile: "leapview.semantic-access/v1", Revision: 7, Digest: "sha256:" + strings.Repeat("b", 64)},
		Control:  SemanticAuditRevision{Profile: "leapview.semantic-access/v1", Revision: 11, Digest: "sha256:" + strings.Repeat("c", 64)},
		Attributes: []SemanticAuditAttribute{{
			DefinitionID: "definition-region", DefinitionName: "region", DefinitionVersion: 3,
			Type: "String", Shape: "scalar", Source: "direct", ValueDigest: "sha256:" + strings.Repeat("d", 64),
		}},
		Target:  SemanticAuditTarget{Dataset: "orders"},
		Grants:  []SemanticAuditGrant{{Grant: "grant-region", UserAttribute: "region", AttributeDefinitionID: "definition-region", AttributeDefinitionVersion: 3, Satisfied: allowed}},
		Filters: []SemanticAuditFilter{{Dataset: "orders", Dimension: "region", UserAttribute: "region", Identity: "filter-region", AttributeDefinitionID: "definition-region", AttributeDefinitionVersion: 3, Applied: true}},
		Allowed: allowed, Reason: reason,
	}
}

func TestSemanticDecisionEvidenceMetadataRoundTripIsStrictAndRedacted(t *testing.T) {
	evidence := testSemanticDecisionEvidence(true)
	metadata, err := evidence.MetadataJSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(metadata, "canonicalValues") || strings.Contains(metadata, "secret") {
		t.Fatalf("metadata contains unredacted values: %s", metadata)
	}
	decoded, err := DecodeSemanticDecisionEvidence(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, evidence) {
		t.Fatalf("decoded evidence differs:\n got %#v\nwant %#v", decoded, evidence)
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(metadata), &object); err != nil {
		t.Fatal(err)
	}
	object["unknown"] = json.RawMessage(`true`)
	unknown, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSemanticDecisionEvidence(string(unknown)); err == nil {
		t.Fatal("unknown metadata field was accepted")
	}
	duplicate := strings.Replace(metadata, `"version":1`, `"version":1,"version":1`, 1)
	if _, err := DecodeSemanticDecisionEvidence(duplicate); err == nil {
		t.Fatal("duplicate metadata field was accepted")
	}
}

func TestSemanticDecisionEvidenceRejectsUnsafeOrderingAndReason(t *testing.T) {
	unsorted := testSemanticDecisionEvidence(true)
	unsorted.Attributes = append(unsorted.Attributes, SemanticAuditAttribute{
		DefinitionID: "definition-alpha", DefinitionName: "alpha", DefinitionVersion: 1,
		Type: "String", Shape: "scalar", Source: "direct", ValueDigest: "sha256:" + strings.Repeat("e", 64),
	})
	if _, err := unsorted.MetadataJSON(); err == nil {
		t.Fatal("unsorted attributes were accepted")
	}
	unsafe := testSemanticDecisionEvidence(false)
	unsafe.Reason = "principal email is secret"
	if _, err := unsafe.MetadataJSON(); err == nil {
		t.Fatal("unsafe decision reason was accepted")
	}
	allowed := testSemanticDecisionEvidence(true)
	allowed.Reason = "required access grant is not satisfied"
	if _, err := allowed.MetadataJSON(); err == nil {
		t.Fatal("allowed decision with a reason was accepted")
	}
}
