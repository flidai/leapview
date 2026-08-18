package deployment

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDeliveryEventDetailsRejectSecretsAndUnknownFields(t *testing.T) {
	event := DeliveryEvent{
		ID: "event-1", TargetID: "target-1", ProjectID: "project-1", Environment: "prod", ActorID: "operator-1",
		EventKind: "plan_created", ObjectKind: "plan", ObjectID: "plan-1", RequestDigest: "sha256:" + strings.Repeat("a", 64), Outcome: "accepted", CreatedAt: time.Now().UTC(),
	}
	for name, details := range map[string]map[string]any{
		"unknown key":           {"password": "secret"},
		"credential-like value": {"reason_code": "token=secret"},
	} {
		event.Details = details
		if err := event.Validate(); !errors.Is(err, ErrDeliveryInvalid) {
			t.Fatalf("%s validation error=%v, want ErrDeliveryInvalid", name, err)
		}
	}
}

func TestDeliveryEventIDStaysBoundedForMaximumObjectID(t *testing.T) {
	objectID := strings.Repeat("x", 128)
	id := DeliveryEventID("target", "sha256:"+strings.Repeat("a", 64), "build_transitioned", "build_attempt", objectID)
	if len(id) > 128 {
		t.Fatalf("event id length=%d, want <=128", len(id))
	}
	event := DeliveryEvent{ID: id, TargetID: "target", ProjectID: "project", Environment: "prod", ActorID: "operator", EventKind: "build_transitioned", ObjectKind: "build_attempt", ObjectID: objectID, RequestDigest: "sha256:" + strings.Repeat("a", 64), Outcome: "accepted", Details: map[string]any{}, CreatedAt: time.Now().UTC()}
	if err := event.Validate(); err != nil {
		t.Fatalf("maximum object event rejected: %v", err)
	}
}
