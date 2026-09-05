package app

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
)

// These are the semantic commands whose state transitions are implemented by
// the access repository. The repository owns the durable event; the generated
// API contract declares the same canonical action for each operation family.
func TestSemanticAttributeAuditContractsMatchDurableActions(t *testing.T) {
	type contractExpectation struct {
		action string
		fields []string
	}
	expected := map[string]contractExpectation{
		"registerSemanticAttribute": {
			action: access.SemanticAttributeAuditActionRegister,
			fields: []string{"profile", "type", "shape", "definitionVersion", "registryRevision", "registryDigest", "ownerKind", "ownerId", "lifecycleState"},
		},
		"updateSemanticAttributeMetadata": {
			action: access.SemanticAttributeAuditActionMetadataUpdate,
			fields: []string{"profile", "type", "shape", "definitionVersion", "registryRevision", "registryDigest", "ownerKind", "ownerId", "lifecycleState"},
		},
		"disableSemanticAttribute": {
			action: access.SemanticAttributeAuditActionDisable,
			fields: []string{"profile", "type", "shape", "definitionVersion", "registryRevision", "registryDigest", "ownerKind", "ownerId", "lifecycleState"},
		},
		"restoreSemanticAttribute": {
			action: access.SemanticAttributeAuditActionEnable,
			fields: []string{"profile", "type", "shape", "definitionVersion", "registryRevision", "registryDigest", "ownerKind", "ownerId", "lifecycleState"},
		},
		"upsertPrincipalSemanticAttributeAssignment": {
			action: access.SemanticAttributeAuditActionAssignmentSet,
			fields: []string{"definitionId", "definitionName", "subjectKind", "subjectId", "definitionVersion", "assignmentVersion", "controlRevision", "valueCount", "tombstoned", "controlDigest"},
		},
		"removePrincipalSemanticAttributeAssignment": {
			action: access.SemanticAttributeAuditActionAssignmentTombstone,
			fields: []string{"definitionId", "definitionName", "subjectKind", "subjectId", "definitionVersion", "assignmentVersion", "controlRevision", "valueCount", "tombstoned", "controlDigest"},
		},
		"upsertGroupSemanticAttributeAssignment": {
			action: access.SemanticAttributeAuditActionAssignmentSet,
			fields: []string{"definitionId", "definitionName", "subjectKind", "subjectId", "definitionVersion", "assignmentVersion", "controlRevision", "valueCount", "tombstoned", "controlDigest"},
		},
		"removeGroupSemanticAttributeAssignment": {
			action: access.SemanticAttributeAuditActionAssignmentTombstone,
			fields: []string{"definitionId", "definitionName", "subjectKind", "subjectId", "definitionVersion", "assignmentVersion", "controlRevision", "valueCount", "tombstoned", "controlDigest"},
		},
		"upsertSemanticAttributeClaimMapping": {
			action: access.SemanticAttributeAuditActionClaimMappingSet,
			fields: []string{"sourceKind", "provider", "issuer", "audience", "claim", "definitionId", "definitionName", "definitionVersion", "mappingVersion", "controlRevision", "tombstoned", "controlDigest"},
		},
		"removeSemanticAttributeClaimMapping": {
			action: access.SemanticAttributeAuditActionClaimMappingTombstone,
			fields: []string{"sourceKind", "provider", "issuer", "audience", "claim", "definitionId", "definitionName", "definitionVersion", "mappingVersion", "controlRevision", "tombstoned", "controlDigest"},
		},
	}
	contracts := accessgen.GetAPIGenOperationContracts()
	for operationID, want := range expected {
		t.Run(operationID, func(t *testing.T) {
			generated, ok := contracts[operationID]
			if !ok || generated.Command == nil {
				t.Fatalf("generated command contract = %#v", generated)
			}
			if got := generated.Command.Audit.SuccessAction; got != want.action {
				t.Fatalf("declared audit action = %q, durable action = %q", got, want.action)
			}
			if generated.Command.Audit.Guarantee != "transactional" {
				t.Fatalf("audit guarantee = %q, want transactional", generated.Command.Audit.Guarantee)
			}
			if generated.Command.Audit.Payload == nil {
				t.Fatal("generated audit payload contract is missing")
			}
			generatedFields := make([]string, len(generated.Command.Audit.Payload.Fields))
			for index, field := range generated.Command.Audit.Payload.Fields {
				generatedFields[index] = field.Name
				name := strings.ToLower(field.Name)
				if name == "values" || name == "canonicalvalues" || name == "valuedigest" {
					t.Errorf("audit payload field %q could expose attribute values or value digests", field.Name)
				}
			}
			sort.Strings(generatedFields)
			wantFields := append([]string(nil), want.fields...)
			sort.Strings(wantFields)
			if !reflect.DeepEqual(generatedFields, wantFields) {
				t.Fatalf("declared audit payload fields = %#v, durable fields = %#v", generatedFields, wantFields)
			}

			runtimeContract, ok := accessgen.GetAPIGenCommandRuntimeContract(operationID)
			if !ok {
				t.Fatal("generated runtime command contract is missing")
			}
			if err := runtimeContract.Validate(); err != nil {
				t.Fatalf("runtime command contract is invalid: %v", err)
			}
			if runtimeContract.AuditAction != want.action || runtimeContract.Guarantee != command.GuaranteeTransactional {
				t.Fatalf("runtime audit contract = action %q, guarantee %q; want %q, transactional", runtimeContract.AuditAction, runtimeContract.Guarantee, want.action)
			}
			if runtimeContract.AuditPayload == nil {
				t.Fatal("runtime audit payload contract is missing")
			}
			runtimeFields := make([]string, 0, len(runtimeContract.AuditPayload.Fields))
			for _, field := range runtimeContract.AuditPayload.Fields {
				runtimeFields = append(runtimeFields, field.Name)
				name := strings.ToLower(field.Name)
				if name == "values" || name == "canonicalvalues" || name == "valuedigest" {
					t.Errorf("audit payload field %q could expose attribute values or value digests", field.Name)
				}
			}
			sort.Strings(runtimeFields)
			if !reflect.DeepEqual(runtimeFields, wantFields) {
				t.Fatalf("runtime audit payload fields = %#v, durable fields = %#v", runtimeFields, wantFields)
			}
		})
	}
}
