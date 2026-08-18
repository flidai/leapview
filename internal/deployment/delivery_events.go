package deployment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// DeliveryEventID is a bounded deterministic identity for one lifecycle
// transition. It remains valid even when an object ID uses its full 128-byte
// allowance.
func DeliveryEventID(targetID, requestDigest, eventKind, objectKind, objectID string) string {
	hash := sha256.Sum256([]byte(targetID + "\x00" + requestDigest + "\x00" + eventKind + "\x00" + objectKind + "\x00" + objectID))
	return "event-" + hex.EncodeToString(hash[:])
}

// DeliveryEvent is an immutable, non-secret lifecycle observation. Mutable
// control rows are projections; every plan/build/publish/rollback/GC command
// may append one event using the same request digest for crash-safe retries.
type DeliveryEvent struct {
	ID            string         `json:"id"`
	TargetID      string         `json:"targetId"`
	ProjectID     string         `json:"projectId"`
	Environment   string         `json:"environment"`
	ActorID       string         `json:"actorId"`
	EventKind     string         `json:"eventKind"`
	ObjectKind    string         `json:"objectKind"`
	ObjectID      string         `json:"objectId"`
	RequestDigest string         `json:"requestDigest"`
	PlanDigest    string         `json:"planDigest,omitempty"`
	ResultDigest  string         `json:"resultDigest,omitempty"`
	Outcome       string         `json:"outcome"`
	Details       map[string]any `json:"details,omitempty"`
	CreatedAt     time.Time      `json:"createdAt"`
}

func (event DeliveryEvent) Validate() error {
	for name, value := range map[string]string{
		"event": event.ID, "target": event.TargetID, "project": event.ProjectID,
		"environment": event.Environment, "actor": event.ActorID, "object": event.ObjectID,
	} {
		if err := ValidateDeliveryID(value); err != nil {
			return fmt.Errorf("delivery event %s: %w", name, err)
		}
	}
	if !deliveryEventKinds[event.EventKind] || !deliveryObjectKinds[event.ObjectKind] || !deliveryEventOutcomes[event.Outcome] {
		return fmt.Errorf("%w: delivery event kind and outcome are required", ErrDeliveryInvalid)
	}
	if err := ValidateDeliveryDigest(event.RequestDigest); err != nil {
		return fmt.Errorf("delivery event request digest: %w", err)
	}
	for name, value := range map[string]string{"plan": event.PlanDigest, "result": event.ResultDigest} {
		if value != "" {
			if err := ValidateDeliveryDigest(value); err != nil {
				return fmt.Errorf("delivery event %s digest: %w", name, err)
			}
		}
	}
	if err := validateDeliveryTime("delivery event created at", event.CreatedAt, true); err != nil {
		return err
	}
	if event.Details == nil {
		event.Details = map[string]any{}
	}
	if err := validateDeliveryEventDetails(event.Details); err != nil {
		return err
	}
	encoded, err := json.Marshal(event.Details)
	if err != nil || !json.Valid(encoded) || strings.TrimSpace(string(encoded)) == "null" {
		return fmt.Errorf("%w: delivery event details must be a JSON object", ErrDeliveryInvalid)
	}
	return nil
}

var deliveryEventDetailKeys = map[string]bool{
	"base_revision": true, "candidate_id": true, "generation_id": true, "publication_id": true,
	"target_revision": true, "replaced_by_generation_id": true, "reason_code": true, "status": true,
}

func validateDeliveryEventDetails(details map[string]any) error {
	for key, value := range details {
		if !deliveryEventDetailKeys[key] {
			return fmt.Errorf("%w: delivery event detail key %q is not allowlisted", ErrDeliveryInvalid, key)
		}
		switch typed := value.(type) {
		case string:
			if len(typed) > 256 || typed != strings.TrimSpace(typed) || strings.ContainsAny(typed, "\x00\r\n") {
				return fmt.Errorf("%w: delivery event detail %q is not canonical", ErrDeliveryInvalid, key)
			}
			lower := strings.ToLower(typed)
			if strings.Contains(lower, "password") || strings.Contains(lower, "secret") || strings.Contains(lower, "credential") || strings.Contains(lower, "token=") || strings.Contains(lower, "key=") {
				return fmt.Errorf("%w: delivery event detail %q appears to contain secret material", ErrDeliveryInvalid, key)
			}
		case int, int64, uint, uint64, float64, bool:
		default:
			return fmt.Errorf("%w: delivery event detail %q has unsupported value type", ErrDeliveryInvalid, key)
		}
	}
	return nil
}

func DeliveryEventsEqual(left, right DeliveryEvent) bool {
	if left.ID != right.ID || left.TargetID != right.TargetID || left.ProjectID != right.ProjectID || left.Environment != right.Environment || left.ActorID != right.ActorID || left.EventKind != right.EventKind || left.ObjectKind != right.ObjectKind || left.ObjectID != right.ObjectID || left.RequestDigest != right.RequestDigest || left.PlanDigest != right.PlanDigest || left.ResultDigest != right.ResultDigest || left.Outcome != right.Outcome || !left.CreatedAt.Equal(right.CreatedAt) {
		return false
	}
	leftDetails, leftErr := json.Marshal(left.Details)
	rightDetails, rightErr := json.Marshal(right.Details)
	return leftErr == nil && rightErr == nil && string(leftDetails) == string(rightDetails)
}

var deliveryEventKinds = map[string]bool{
	"plan_created": true, "plan_expired": true, "build_started": true, "build_transitioned": true, "build_artifact_bound": true,
	"candidate_qualified": true, "candidate_sealed": true, "candidate_retired": true,
	"approval_requested": true, "approval_granted": true, "approval_rejected": true,
	"restatement_requested": true, "publish_requested": true,
	"publish_committed": true, "publish_rejected": true, "publish_indeterminate": true,
	"activation_committed": true, "rollback_requested": true, "rollback_committed": true,
	"retirement_committed": true, "gc_marked": true, "gc_deleted": true,
	"cleanup_completed": true, "gc_aborted": true, "lease_acquired": true, "lease_expired": true, "lease_released": true,
}

var deliveryObjectKinds = map[string]bool{
	"plan": true, "build_attempt": true, "candidate": true, "generation": true, "approval": true,
	"publication": true, "rollback": true, "gc_cycle": true, "writer_lease": true, "query_lease": true,
}

var deliveryEventOutcomes = map[string]bool{
	"accepted": true, "rejected": true, "failed": true, "indeterminate": true, "observed": true,
}
