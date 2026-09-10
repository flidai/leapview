package postgres

import (
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/project/graph"
)

func TestSemanticAuditIntentIdentityReplay(t *testing.T) {
	resource, err := access.NewResourceRef("semantic-model:sales", graph.KindSemanticModel)
	if err != nil {
		t.Fatal(err)
	}
	event := access.CanonicalAuditEvent{
		Identity:    graph.ServingIdentity{ProjectID: "project:test", Environment: "production", GenerationID: "generation-one"},
		PrincipalID: auditActorID, Action: "semantic_access.plan_validation", Resource: resource,
		Capability: access.CapabilityResourceUse, Status: "success",
		MetadataJSON: `{"source":"semantic_access_consumer","decisionDigest":"decision-one","actorId":"actor-one"}`,
	}
	intent := func(event access.CanonicalAuditEvent) string {
		t.Helper()
		metadata, err := event.CanonicalMetadataJSON()
		if err != nil {
			t.Fatal(err)
		}
		return canonicalAuditDigest(event, metadata)
	}
	original := intent(event)
	replay := event
	replay.MetadataJSON = `{"actorId":"actor-one","decisionDigest":"decision-one","source":"semantic_access_consumer"}`
	if intent(replay) != original {
		t.Fatal("equivalent semantic audit replay changed intent identity")
	}
	for _, test := range []struct {
		name   string
		change func(*access.CanonicalAuditEvent)
	}{
		{"generation", func(e *access.CanonicalAuditEvent) { e.Identity.GenerationID = "generation-two" }},
		{"denial", func(e *access.CanonicalAuditEvent) { e.Status = "denied" }},
		{"actor", func(e *access.CanonicalAuditEvent) {
			e.MetadataJSON = `{"source":"semantic_access_consumer","decisionDigest":"decision-one","actorId":"actor-two"}`
		}},
		{"decision", func(e *access.CanonicalAuditEvent) {
			e.MetadataJSON = `{"source":"semantic_access_consumer","decisionDigest":"decision-two","actorId":"actor-one"}`
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := event
			test.change(&changed)
			if intent(changed) == original {
				t.Fatal("changed semantic audit intent reused old identity")
			}
		})
	}
}
