package authz

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
)

func TestSemanticDiscoveryPersistsBoundDecisionBeforeDisclosure(t *testing.T) {
	for _, allowed := range []bool{true, false} {
		t.Run(map[bool]string{true: "allowed", false: "denied"}[allowed], func(t *testing.T) {
			metrics, _ := semanticDiscoveryFixture(t)
			recorder := metrics.auditRecorder.(*canonicalAuditRecorder)
			if !allowed {
				resolution, err := metrics.resolveSemanticAttributes(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				values, digest, err := access.CanonicalSemanticAttributeValues(resolution.Registry.Definitions[0], "eu")
				if err != nil {
					t.Fatal(err)
				}
				resolution.Attributes[0].CanonicalValues, resolution.Attributes[0].ValueDigest = values, digest
				metrics.resolveSemanticAttributes = func(context.Context) (access.SemanticAttributeResolution, error) { return resolution, nil }
			}
			ctx := dataquery.WithMetadata(t.Context(), dataquery.Metadata{PrincipalID: "untrusted-payload-principal", RequestID: "request-test", CorrelationID: "correlation-test"})
			err := metrics.AuthorizeSemanticTarget(ctx, "sales", semanticquery.SemanticAccessTarget{Dataset: "orders"})
			if (err == nil) != allowed || len(recorder.events) != 1 {
				t.Fatalf("allowed=%v err=%v audit events=%d", allowed, err, len(recorder.events))
			}
			event := recorder.events[0]
			evidence, err := access.DecodeSemanticDecisionEvidence(event.MetadataJSON)
			if err != nil {
				t.Fatal(err)
			}
			if event.PrincipalID != "alice" || event.Resource.ID() != "semantic_sales" || event.Identity.GenerationID != "generation-1" ||
				event.RequestID != "request-test" || event.CorrelationID != "correlation-test" || evidence.InstanceID != "instance-test" ||
				evidence.ActorPrincipalID != "alice" || evidence.Allowed != allowed || evidence.Registry.Revision != 7 || evidence.Control.Revision != 11 {
				t.Fatalf("audit identity or decision mismatch: %#v / %#v", event, evidence)
			}
			if len(evidence.Grants) != 1 || evidence.Grants[0].Grant != "region_grant" || evidence.Grants[0].Satisfied != allowed {
				t.Fatalf("grant evidence missing: %#v", evidence.Grants)
			}
			for _, forbidden := range []string{`"us"`, `"eu"`, "canonicalValues", "allowedValues", "untrusted-payload-principal", "Predicates"} {
				if strings.Contains(event.MetadataJSON, forbidden) {
					t.Fatalf("audit leaked %q: %s", forbidden, event.MetadataJSON)
				}
			}
		})
	}
}

func TestSemanticDiscoveryAuditFailsClosed(t *testing.T) {
	for _, failure := range []string{"missing-recorder", "write-failure", "missing-actor", "foreign-actor", "missing-instance"} {
		t.Run(failure, func(t *testing.T) {
			metrics, _ := semanticDiscoveryFixture(t)
			switch failure {
			case "missing-recorder":
				metrics.auditRecorder = nil
			case "write-failure":
				metrics.auditRecorder.(*canonicalAuditRecorder).err = errors.New("private storage detail")
			case "missing-actor":
				metrics.principalFromContext = nil
			case "foreign-actor":
				metrics.principalFromContext = func(context.Context) (Principal, bool) { return Principal{ID: "foreign"}, true }
			case "missing-instance":
				metrics.snapshotFromContext = func(context.Context) (accesssnapshot.AuthorizationSnapshot, error) {
					return canonicalSnapshot(t, nil, nil), nil
				}
			}
			if err := metrics.AuthorizeSemanticTarget(t.Context(), "sales", semanticquery.SemanticAccessTarget{Dataset: "orders"}); err == nil {
				t.Fatal("discovery disclosed a target without durable audit")
			} else if strings.Contains(err.Error(), "private storage detail") {
				t.Fatal("audit storage error leaked")
			}
			planner, err := metrics.SemanticPlanner(t.Context(), "sales")
			if err == nil {
				if _, err := planner.Plan(semanticquery.Request{Metrics: []semanticquery.Field{{Field: "order_count"}}}); err == nil {
					t.Fatal("explain/planning bypassed audit failure")
				}
			}
		})
	}
}

type semanticAuditPostgresOutcomeRecorder struct {
	canonicalAuditRecorder
	rejectedOutcome string
}

func (r *semanticAuditPostgresOutcomeRecorder) RecordCanonicalAuditEvent(ctx context.Context, event access.CanonicalAuditEvent) error {
	// Match the existing PostgreSQL canonical append contract. This catches
	// status vocabulary errors before the semantic evidence observer is reached.
	if event.Status != "success" && event.Status != "denied" && event.Status != "failure" {
		r.rejectedOutcome = event.Status
		return errors.New("invalid PostgreSQL canonical audit outcome")
	}
	return r.canonicalAuditRecorder.RecordCanonicalAuditEvent(ctx, event)
}

func TestSemanticDiscoveryAuditBindsAuthorizedDelegatedActor(t *testing.T) {
	metrics, _ := semanticDiscoveryFixture(t)
	recorder := &semanticAuditPostgresOutcomeRecorder{}
	metrics.auditRecorder = recorder
	graph, identity, _, _, _ := canonicalGraph(t)
	actor := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "alice"}
	metrics.subjectsFromContext = func(context.Context, string) ([]access.SubjectRef, error) { return []access.SubjectRef{actor}, nil }
	snapshot, err := accesssnapshot.FromControlState(identity, graph, access.ControlState{
		InstanceID: "instance-test", ProjectID: graph.ProjectID().String(), Revision: 3,
		RoleAssignments: []access.RoleAssignment{{ID: "admin-role", InstanceID: "instance-test", ProjectID: graph.ProjectID().String(), Subject: actor, Role: string(access.ProjectRoleAdmin)}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	metrics.snapshotFromContext = func(context.Context) (accesssnapshot.AuthorizationSnapshot, error) { return snapshot, nil }
	resolution, err := metrics.resolveSemanticAttributes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	resolution.Subject = access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "bob"}
	metrics.resolveSemanticAttributes = func(context.Context) (access.SemanticAttributeResolution, error) { return resolution, nil }
	ctx := WithViewAsCapability(t.Context(), ViewAsCapability{ActorPrincipalID: "alice", SubjectPrincipalID: "bob", ProjectID: canonicalProject})
	if err := metrics.AuthorizeSemanticTarget(ctx, "sales", semanticquery.SemanticAccessTarget{Dataset: "orders"}); err != nil {
		t.Fatalf("%v; rejected PostgreSQL outcome=%q", err, recorder.rejectedOutcome)
	}
	if len(recorder.events) != 2 {
		t.Fatalf("expected separate ViewAs authorization and semantic decision evidence: %#v", recorder.events)
	}
	event := recorder.events[1]
	evidence, err := access.DecodeSemanticDecisionEvidence(event.MetadataJSON)
	if err != nil {
		t.Fatal(err)
	}
	if event.PrincipalID != "bob" || evidence.ActorPrincipalID != "alice" {
		t.Fatalf("delegated actor conflated with effective principal: %#v / %#v", event, evidence)
	}
	// A context capability alone does not grant PROJECT_ADMIN.
	metrics.snapshotFromContext = func(context.Context) (accesssnapshot.AuthorizationSnapshot, error) {
		return accesssnapshot.FromControlState(identity, graph, access.ControlState{InstanceID: "instance-test", ProjectID: graph.ProjectID().String(), Revision: 4}, nil)
	}
	if err := metrics.AuthorizeSemanticTarget(ctx, "sales", semanticquery.SemanticAccessTarget{Dataset: "orders"}); err == nil {
		t.Fatal("delegated discovery bypassed administrator authorization")
	}
}
