package postgres

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5/pgconn"
)

func semanticDecisionEvent(t *testing.T, allowed bool, requestID, correlationID string) access.CanonicalAuditEvent {
	t.Helper()
	reason := ""
	if !allowed {
		reason = "required access grant is not satisfied"
	}
	evidence := access.SemanticDecisionEvidence{
		Version: 1, InstanceID: "instance-audit", ActorPrincipalID: auditActorID,
		SemanticModelDigest: "sha256:" + strings.Repeat("a", 64),
		Registry:            access.SemanticAuditRevision{Profile: "leapview.semantic-access/v1", Revision: 7, Digest: "sha256:" + strings.Repeat("b", 64)},
		Control:             access.SemanticAuditRevision{Profile: "leapview.semantic-access/v1", Revision: 11, Digest: "sha256:" + strings.Repeat("c", 64)},
		Attributes: []access.SemanticAuditAttribute{{
			DefinitionID: "definition-region", DefinitionName: "region", DefinitionVersion: 3,
			Type: "String", Shape: "scalar", Source: "direct", ValueDigest: "sha256:" + strings.Repeat("d", 64),
		}},
		Target:  access.SemanticAuditTarget{Dataset: "orders"},
		Grants:  []access.SemanticAuditGrant{{Grant: "grant-region", UserAttribute: "region", AttributeDefinitionID: "definition-region", AttributeDefinitionVersion: 3, Satisfied: allowed}},
		Filters: []access.SemanticAuditFilter{{Dataset: "orders", Dimension: "region", UserAttribute: "region", Identity: "filter-region", AttributeDefinitionID: "definition-region", AttributeDefinitionVersion: 3, Applied: true}},
		Allowed: allowed, Reason: reason,
	}
	metadata, err := evidence.MetadataJSON()
	if err != nil {
		t.Fatal(err)
	}
	resource, err := access.NewResourceRef(projectgraph.ResourceID("semantic_orders"), projectgraph.KindSemanticModel)
	if err != nil {
		t.Fatal(err)
	}
	return access.CanonicalAuditEvent{
		Identity: graphServingIdentity(t), PrincipalID: semanticDecisionPrincipalID,
		Action: access.SemanticDecisionAuditAction, Resource: resource,
		Capability: access.CapabilityResourceUse, Status: map[bool]string{true: "success", false: "denied"}[allowed],
		RequestID: requestID, CorrelationID: correlationID, MetadataJSON: metadata,
	}
}

const semanticDecisionPrincipalID = "10000000-0000-0000-0000-0000000000ab"

func graphServingIdentity(t *testing.T) projectgraph.ServingIdentity {
	t.Helper()
	identity, err := projectgraph.NewServingIdentity(projectgraph.ResourceID("project_audit"), "production", "generation_audit")
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func TestReadSemanticDecisionAuditEventPostgreSQL18RuntimeVerifiesRetention(t *testing.T) {
	db := newAuditDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	allowed := semanticDecisionEvent(t, true, "30000000-0000-0000-0000-000000000101", "40000000-0000-0000-0000-000000000101")
	denied := semanticDecisionEvent(t, false, "30000000-0000-0000-0000-000000000102", "40000000-0000-0000-0000-000000000102")
	for _, event := range []access.CanonicalAuditEvent{allowed, denied} {
		if err := repo.RecordCanonicalAuditEvent(t.Context(), event); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []access.CanonicalAuditEvent{allowed, denied} {
		var auditID string
		if err := db.runtime.QueryRow(t.Context(), `SELECT audit_id::text FROM audit.audit_event WHERE request_id=$1::uuid`, want.RequestID).Scan(&auditID); err != nil {
			t.Fatal(err)
		}
		got, err := repo.ReadSemanticDecisionAuditEvent(t.Context(), auditID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("retained event differs:\n got %#v\nwant %#v", got, want)
		}
		if strings.Contains(got.MetadataJSON, "canonicalValues") || strings.Contains(got.MetadataJSON, "secret") {
			t.Fatalf("retained evidence is not redacted: %s", got.MetadataJSON)
		}
		if _, err := db.runtime.Exec(t.Context(), `UPDATE audit.audit_event SET action='tampered' WHERE audit_id=$1::uuid`, auditID); err == nil {
			t.Fatal("runtime audit update was accepted")
		} else {
			requireSemanticAuditSQLState(t, err, "42501")
		}
		if _, err := db.runtime.Exec(t.Context(), `DELETE FROM audit.audit_event WHERE audit_id=$1::uuid`, auditID); err == nil {
			t.Fatal("runtime audit delete was accepted")
		} else {
			requireSemanticAuditSQLState(t, err, "42501")
		}
	}
}

func TestVerifySemanticDecisionAuditEventRequiresExactExpectedBinding(t *testing.T) {
	db := newAuditDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	allowed := semanticDecisionEvent(t, true, "30000000-0000-0000-0000-000000000131", "40000000-0000-0000-0000-000000000131")
	denied := semanticDecisionEvent(t, false, "30000000-0000-0000-0000-000000000132", "40000000-0000-0000-0000-000000000132")
	for _, event := range []access.CanonicalAuditEvent{allowed, denied} {
		if err := repo.RecordCanonicalAuditEvent(t.Context(), event); err != nil {
			t.Fatal(err)
		}
	}
	findAuditID := func(requestID string) string {
		t.Helper()
		var auditID string
		if err := db.runtime.QueryRow(t.Context(), `SELECT audit_id::text FROM audit.audit_event WHERE request_id=$1::uuid`, requestID).Scan(&auditID); err != nil {
			t.Fatal(err)
		}
		return auditID
	}
	allowedID, deniedID := findAuditID(allowed.RequestID), findAuditID(denied.RequestID)

	// Formatting and object-key order are not identity. The existing canonical
	// metadata authority must normalize an otherwise identical expected event.
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, []byte(allowed.MetadataJSON), "", "  "); err != nil {
		t.Fatal(err)
	}
	formatted := allowed
	formatted.MetadataJSON = pretty.String()
	if err := repo.VerifySemanticDecisionAuditEvent(t.Context(), allowedID, formatted); err != nil {
		t.Fatalf("formatted equivalent expected event was rejected: %v", err)
	}
	if err := repo.VerifySemanticDecisionAuditEvent(t.Context(), deniedID, denied); err != nil {
		t.Fatalf("direct denied expected event was rejected: %v", err)
	}
	firstRead, err := repo.ReadSemanticDecisionAuditEvent(t.Context(), deniedID)
	if err != nil {
		t.Fatal(err)
	}
	secondRead, err := repo.ReadSemanticDecisionAuditEvent(t.Context(), deniedID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(firstRead, secondRead) {
		t.Fatalf("repeated retained reads differ:\n first %#v\nsecond %#v", firstRead, secondRead)
	}
	if err := repo.VerifySemanticDecisionAuditEvent(t.Context(), allowedID, denied); err == nil || !strings.Contains(err.Error(), "does not match expected") {
		t.Fatalf("valid differently-bound retained event was accepted: %v", err)
	}
	if err := repo.VerifySemanticDecisionAuditEvent(t.Context(), deniedID, allowed); err == nil || !strings.Contains(err.Error(), "does not match expected") {
		t.Fatalf("reverse valid-row substitution was accepted: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*access.CanonicalAuditEvent, *access.SemanticDecisionEvidence)
	}{
		{name: "project", mutate: func(event *access.CanonicalAuditEvent, _ *access.SemanticDecisionEvidence) {
			event.Identity.ProjectID = "project_other"
		}},
		{name: "environment", mutate: func(event *access.CanonicalAuditEvent, _ *access.SemanticDecisionEvidence) {
			event.Identity.Environment = "staging"
		}},
		{name: "generation", mutate: func(event *access.CanonicalAuditEvent, _ *access.SemanticDecisionEvidence) {
			event.Identity.GenerationID = "generation_other"
		}},
		{name: "principal", mutate: func(event *access.CanonicalAuditEvent, _ *access.SemanticDecisionEvidence) {
			event.PrincipalID = "10000000-0000-0000-0000-0000000000ac"
		}},
		{name: "resource", mutate: func(event *access.CanonicalAuditEvent, _ *access.SemanticDecisionEvidence) {
			event.Resource, _ = access.NewResourceRef("semantic_other", projectgraph.KindSemanticModel)
		}},
		{name: "capability", mutate: func(event *access.CanonicalAuditEvent, _ *access.SemanticDecisionEvidence) {
			event.Capability = access.CapabilityResourceRead
		}},
		{name: "request", mutate: func(event *access.CanonicalAuditEvent, _ *access.SemanticDecisionEvidence) {
			event.RequestID = "30000000-0000-0000-0000-000000000133"
		}},
		{name: "correlation", mutate: func(event *access.CanonicalAuditEvent, _ *access.SemanticDecisionEvidence) {
			event.CorrelationID = "40000000-0000-0000-0000-000000000133"
		}},
		{name: "instance", mutate: func(_ *access.CanonicalAuditEvent, evidence *access.SemanticDecisionEvidence) {
			evidence.InstanceID = "instance-other"
		}},
		{name: "actor", mutate: func(_ *access.CanonicalAuditEvent, evidence *access.SemanticDecisionEvidence) {
			evidence.ActorPrincipalID = "20000000-0000-0000-0000-000000000002"
		}},
		{name: "semantic model digest", mutate: func(_ *access.CanonicalAuditEvent, evidence *access.SemanticDecisionEvidence) {
			evidence.SemanticModelDigest = "sha256:" + strings.Repeat("e", 64)
		}},
		{name: "registry revision", mutate: func(_ *access.CanonicalAuditEvent, evidence *access.SemanticDecisionEvidence) {
			evidence.Registry.Revision++
		}},
		{name: "registry digest", mutate: func(_ *access.CanonicalAuditEvent, evidence *access.SemanticDecisionEvidence) {
			evidence.Registry.Digest = "sha256:" + strings.Repeat("e", 64)
		}},
		{name: "control revision", mutate: func(_ *access.CanonicalAuditEvent, evidence *access.SemanticDecisionEvidence) {
			evidence.Control.Revision++
		}},
		{name: "control digest", mutate: func(_ *access.CanonicalAuditEvent, evidence *access.SemanticDecisionEvidence) {
			evidence.Control.Digest = "sha256:" + strings.Repeat("f", 64)
		}},
		{name: "attribute version", mutate: func(_ *access.CanonicalAuditEvent, evidence *access.SemanticDecisionEvidence) {
			evidence.Attributes[0].DefinitionVersion++
		}},
		{name: "attribute value digest", mutate: func(_ *access.CanonicalAuditEvent, evidence *access.SemanticDecisionEvidence) {
			evidence.Attributes[0].ValueDigest = "sha256:" + strings.Repeat("e", 64)
		}},
		{name: "target dataset", mutate: func(_ *access.CanonicalAuditEvent, evidence *access.SemanticDecisionEvidence) {
			evidence.Target.Dataset = "payments"
		}},
		{name: "target dimension", mutate: func(_ *access.CanonicalAuditEvent, evidence *access.SemanticDecisionEvidence) {
			evidence.Target.Dimension = "country"
		}},
		{name: "target metric", mutate: func(_ *access.CanonicalAuditEvent, evidence *access.SemanticDecisionEvidence) {
			evidence.Target = access.SemanticAuditTarget{Metric: "revenue"}
		}},
		{name: "grant identity", mutate: func(_ *access.CanonicalAuditEvent, evidence *access.SemanticDecisionEvidence) {
			evidence.Grants[0].Grant = "grant-country"
		}},
		{name: "grant user attribute", mutate: func(_ *access.CanonicalAuditEvent, evidence *access.SemanticDecisionEvidence) {
			evidence.Grants[0].UserAttribute = "country"
		}},
		{name: "grant definition identity", mutate: func(_ *access.CanonicalAuditEvent, evidence *access.SemanticDecisionEvidence) {
			evidence.Grants[0].AttributeDefinitionID = "definition-country"
		}},
		{name: "grant definition version", mutate: func(_ *access.CanonicalAuditEvent, evidence *access.SemanticDecisionEvidence) {
			evidence.Grants[0].AttributeDefinitionVersion++
		}},
		{name: "filter dataset", mutate: func(_ *access.CanonicalAuditEvent, evidence *access.SemanticDecisionEvidence) {
			evidence.Filters[0].Dataset = "payments"
		}},
		{name: "filter dimension", mutate: func(_ *access.CanonicalAuditEvent, evidence *access.SemanticDecisionEvidence) {
			evidence.Filters[0].Dimension = "country"
		}},
		{name: "filter user attribute", mutate: func(_ *access.CanonicalAuditEvent, evidence *access.SemanticDecisionEvidence) {
			evidence.Filters[0].UserAttribute = "country"
		}},
		{name: "filter identity", mutate: func(_ *access.CanonicalAuditEvent, evidence *access.SemanticDecisionEvidence) {
			evidence.Filters[0].Identity = "filter-country"
		}},
		{name: "filter definition identity", mutate: func(_ *access.CanonicalAuditEvent, evidence *access.SemanticDecisionEvidence) {
			evidence.Filters[0].AttributeDefinitionID = "definition-country"
		}},
		{name: "filter definition version", mutate: func(_ *access.CanonicalAuditEvent, evidence *access.SemanticDecisionEvidence) {
			evidence.Filters[0].AttributeDefinitionVersion++
		}},
		{name: "grant outcome", mutate: func(_ *access.CanonicalAuditEvent, evidence *access.SemanticDecisionEvidence) {
			evidence.Grants[0].Satisfied = false
		}},
		{name: "filter outcome", mutate: func(_ *access.CanonicalAuditEvent, evidence *access.SemanticDecisionEvidence) {
			evidence.Filters[0].Applied = false
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			expected := allowed
			evidence, err := access.DecodeSemanticDecisionEvidence(expected.MetadataJSON)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(&expected, &evidence)
			expected.MetadataJSON, err = evidence.MetadataJSON()
			if err != nil {
				t.Fatalf("expected mutation is not schema-valid: %v", err)
			}
			if err := repo.VerifySemanticDecisionAuditEvent(t.Context(), allowedID, expected); err == nil {
				t.Fatal("mismatched expected binding was accepted")
			} else if !strings.Contains(err.Error(), "does not match expected") {
				t.Fatalf("mismatched expected binding returned wrong error: %v", err)
			}
		})
	}
	decisionExpected := allowed
	decisionEvidence, err := access.DecodeSemanticDecisionEvidence(decisionExpected.MetadataJSON)
	if err != nil {
		t.Fatal(err)
	}
	decisionExpected.Status = "denied"
	decisionEvidence.Allowed = false
	decisionEvidence.Reason = "required access grant is not satisfied"
	decisionExpected.MetadataJSON, err = decisionEvidence.MetadataJSON()
	if err != nil {
		t.Fatal(err)
	}
	if decisionExpected.RequestID != allowed.RequestID || decisionExpected.CorrelationID != allowed.CorrelationID {
		t.Fatal("decision mismatch fixture changed request or correlation identity")
	}
	if err := repo.VerifySemanticDecisionAuditEvent(t.Context(), allowedID, decisionExpected); err == nil || !strings.Contains(err.Error(), "does not match expected") {
		t.Fatalf("same-request decision mismatch was accepted: %v", err)
	}
	deniedReason := denied
	deniedEvidence, err := access.DecodeSemanticDecisionEvidence(deniedReason.MetadataJSON)
	if err != nil {
		t.Fatal(err)
	}
	deniedEvidence.Reason = "required access filter attribute is unavailable"
	deniedReason.MetadataJSON, err = deniedEvidence.MetadataJSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.VerifySemanticDecisionAuditEvent(t.Context(), deniedID, deniedReason); err == nil || !strings.Contains(err.Error(), "does not match expected") {
		t.Fatalf("mismatched denial reason was accepted: %v", err)
	}
	wrongAction := allowed
	wrongAction.Action = "semantic_access.other"
	if err := repo.VerifySemanticDecisionAuditEvent(t.Context(), allowedID, wrongAction); err == nil {
		t.Fatal("wrong expected action was accepted")
	}
	if err := repo.VerifySemanticDecisionAuditEvent(t.Context(), allowedID, access.CanonicalAuditEvent{}); err == nil {
		t.Fatal("empty expected event was accepted")
	}
	invalidSemanticEvent := allowed
	invalidSemanticEvent.MetadataJSON = "{}"
	if err := repo.VerifySemanticDecisionAuditEvent(t.Context(), allowedID, invalidSemanticEvent); err == nil {
		t.Fatal("invalid expected semantic event was accepted")
	}

	// Decision and denial reason are bound together by the existing event
	// validation; the valid denied event above exercises both as an exact
	// mismatch without inventing an alternate reason vocabulary.
}

func requireSemanticAuditSQLState(t *testing.T, err error, want string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("error %v is not a PostgreSQL error", err)
	}
	if pgErr.Code != want {
		t.Fatalf("PostgreSQL SQLSTATE = %s, want %s (%v)", pgErr.Code, want, err)
	}
}

func TestReadSemanticDecisionAuditEventRejectsStoredDigestTampering(t *testing.T) {
	db := newAuditDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	allowed := semanticDecisionEvent(t, true, "30000000-0000-0000-0000-000000000111", "40000000-0000-0000-0000-000000000111")
	denied := semanticDecisionEvent(t, false, "30000000-0000-0000-0000-000000000112", "40000000-0000-0000-0000-000000000112")
	for _, event := range []access.CanonicalAuditEvent{allowed, denied} {
		if err := repo.RecordCanonicalAuditEvent(t.Context(), event); err != nil {
			t.Fatal(err)
		}
	}
	readDigest := func(event access.CanonicalAuditEvent) string {
		var digest string
		if err := db.runtime.QueryRow(t.Context(), `SELECT intent_digest FROM audit.audit_event WHERE request_id=$1::uuid`, event.RequestID).Scan(&digest); err != nil {
			t.Fatal(err)
		}
		return digest
	}

	// Append rows as the owner, retaining the original digest while changing
	// one schema-valid identity/evidence field. Read must verify every bound
	// field rather than trust the row's columns or decoded metadata.
	type tamperMutation struct {
		name string
		edit func(*access.CanonicalAuditEvent, *access.SemanticDecisionEvidence)
	}
	mutations := []tamperMutation{
		{name: "instanceID", edit: func(_ *access.CanonicalAuditEvent, evidence *access.SemanticDecisionEvidence) {
			evidence.InstanceID = "instance-tampered"
		}},
		{name: "actorPrincipalID", edit: func(_ *access.CanonicalAuditEvent, evidence *access.SemanticDecisionEvidence) {
			evidence.ActorPrincipalID = "20000000-0000-0000-0000-000000000002"
		}},
		{name: "semanticModelDigest", edit: func(_ *access.CanonicalAuditEvent, evidence *access.SemanticDecisionEvidence) {
			evidence.SemanticModelDigest = "sha256:" + strings.Repeat("e", 64)
		}},
		{name: "registryRevisionDigest", edit: func(_ *access.CanonicalAuditEvent, evidence *access.SemanticDecisionEvidence) {
			evidence.Registry.Revision++
			evidence.Registry.Digest = "sha256:" + strings.Repeat("e", 64)
		}},
		{name: "controlRevisionDigest", edit: func(_ *access.CanonicalAuditEvent, evidence *access.SemanticDecisionEvidence) {
			evidence.Control.Revision++
			evidence.Control.Digest = "sha256:" + strings.Repeat("f", 64)
		}},
		{name: "attributeVersionDigest", edit: func(_ *access.CanonicalAuditEvent, evidence *access.SemanticDecisionEvidence) {
			evidence.Attributes[0].DefinitionVersion++
			evidence.Attributes[0].ValueDigest = "sha256:" + strings.Repeat("e", 64)
		}},
		{name: "grantOutcome", edit: func(_ *access.CanonicalAuditEvent, evidence *access.SemanticDecisionEvidence) {
			evidence.Grants[0].Satisfied = !evidence.Grants[0].Satisfied
		}},
		{name: "filterOutcome", edit: func(_ *access.CanonicalAuditEvent, evidence *access.SemanticDecisionEvidence) {
			evidence.Filters[0].Applied = !evidence.Filters[0].Applied
		}},
		{name: "denialReason", edit: func(event *access.CanonicalAuditEvent, evidence *access.SemanticDecisionEvidence) {
			*event = denied
			evidence.Reason = "required access filter attribute is unavailable"
		}},
		{name: "projectID", edit: func(event *access.CanonicalAuditEvent, _ *access.SemanticDecisionEvidence) {
			event.Identity.ProjectID = "project_tampered"
		}},
		{name: "environment", edit: func(event *access.CanonicalAuditEvent, _ *access.SemanticDecisionEvidence) {
			event.Identity.Environment = "staging"
		}},
		{name: "generationID", edit: func(event *access.CanonicalAuditEvent, _ *access.SemanticDecisionEvidence) {
			event.Identity.GenerationID = "generation_tampered"
		}},
		{name: "resourceID", edit: func(event *access.CanonicalAuditEvent, _ *access.SemanticDecisionEvidence) {
			event.Resource, _ = access.NewResourceRef("semantic_other", projectgraph.KindSemanticModel)
		}},
		{name: "principalID", edit: func(event *access.CanonicalAuditEvent, _ *access.SemanticDecisionEvidence) {
			event.PrincipalID = "20000000-0000-0000-0000-000000000002"
		}},
		{name: "correlationID", edit: func(event *access.CanonicalAuditEvent, _ *access.SemanticDecisionEvidence) {
			event.CorrelationID = "40000000-0000-0000-0000-000000000113"
		}},
	}
	for index, mutation := range mutations {
		base := allowed
		if mutation.name == "denialReason" {
			base = denied
		}
		evidence, err := access.DecodeSemanticDecisionEvidence(base.MetadataJSON)
		if err != nil {
			t.Fatal(err)
		}
		row := base
		mutation.edit(&row, &evidence)
		metadata, err := evidence.MetadataJSON()
		if err != nil {
			t.Fatalf("mutation %s is not schema-valid: %v", mutation.name, err)
		}
		row.MetadataJSON = metadata
		rowID := "60000000-0000-0000-0000-000000000" + fmt.Sprintf("%03d", index+1)
		if _, err := db.admin.Exec(t.Context(), `INSERT INTO audit.audit_event(audit_id,principal_id,source,operation,action,resource_kind,resource_id,project_id,environment,generation_id,capability,outcome,request_id,correlation_id,aggregate_key,aggregate_sequence,intent_digest,metadata) VALUES($1::uuid,$2::uuid,'access','authorization',$3,$4,$5,$6,$7,$8,$9,$10,NULLIF($11,'')::uuid,NULLIF($12,'')::uuid,$13,0,$14,$15::jsonb)`, rowID, row.PrincipalID, access.SemanticDecisionAuditAction, projectgraph.KindSemanticModel, row.Resource.ID().String(), row.Identity.ProjectID.String(), row.Identity.Environment, row.Identity.GenerationID, row.Capability.String(), row.Status, row.RequestID, row.CorrelationID, row.Resource.ID().String(), readDigest(base), metadata); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.ReadSemanticDecisionAuditEvent(t.Context(), rowID); err == nil || !strings.Contains(err.Error(), "intent digest") {
			t.Fatalf("tampered %s row error = %v, want intent digest rejection", mutation.name, err)
		}
	}
}

func TestRecordSemanticDecisionAuditEventRejectsNonCanonicalUUIDs(t *testing.T) {
	db := newAuditDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	canonical := semanticDecisionEvent(t, true, "30000000-0000-0000-0000-000000000121", "40000000-0000-0000-0000-000000000121")
	compactRequest := strings.ReplaceAll(canonical.RequestID, "-", "")
	compactCorrelation := strings.ReplaceAll(canonical.CorrelationID, "-", "")
	for _, test := range []struct {
		name   string
		mutate func(*access.CanonicalAuditEvent)
	}{
		{name: "principal uppercase", mutate: func(event *access.CanonicalAuditEvent) { event.PrincipalID = strings.ToUpper(event.PrincipalID) }},
		{name: "request compact", mutate: func(event *access.CanonicalAuditEvent) { event.RequestID = compactRequest }},
		{name: "correlation compact", mutate: func(event *access.CanonicalAuditEvent) { event.CorrelationID = compactCorrelation }},
		{name: "correlation whitespace", mutate: func(event *access.CanonicalAuditEvent) { event.CorrelationID = "   " }},
	} {
		event := canonical
		test.mutate(&event)
		if err := repo.RecordCanonicalAuditEvent(t.Context(), event); err == nil {
			t.Fatalf("accepted noncanonical %s UUID", test.name)
		}
		var rows int
		if err := db.runtime.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE action=$1`, access.SemanticDecisionAuditAction).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 0 {
			t.Fatalf("noncanonical %s rejection left %d audit rows", test.name, rows)
		}
	}
	if err := repo.RecordCanonicalAuditEvent(t.Context(), canonical); err != nil {
		t.Fatalf("canonical semantic decision was rejected: %v", err)
	}
	var auditID string
	if err := db.runtime.QueryRow(t.Context(), `SELECT audit_id::text FROM audit.audit_event WHERE action=$1`, access.SemanticDecisionAuditAction).Scan(&auditID); err != nil {
		t.Fatal(err)
	}
	got, err := repo.ReadSemanticDecisionAuditEvent(t.Context(), auditID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PrincipalID != canonical.PrincipalID || got.RequestID != canonical.RequestID || got.CorrelationID != canonical.CorrelationID {
		t.Fatalf("canonical UUIDs were not retained exactly: %#v", got)
	}
	evidence, err := access.DecodeSemanticDecisionEvidence(got.MetadataJSON)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.ActorPrincipalID == got.PrincipalID {
		t.Fatalf("positive fixture dropped distinct actor identity: %#v", evidence)
	}
}
