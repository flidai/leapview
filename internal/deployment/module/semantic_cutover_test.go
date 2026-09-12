package module

import (
	"encoding/json"
	"testing"
)

func TestNativeActivationWorkflowPreservesRollbackIdentity(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		intent, err := nativePublicationActivationWorkflow("project_demo", "prod", "publisher", rollback)("publication-1")
		if err != nil {
			t.Fatal(err)
		}
		var payload ActivateJob
		if err := json.Unmarshal(intent.Job.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Rollback != rollback {
			t.Fatalf("rollback=%t workflow payload = %#v", rollback, payload)
		}
	}
}
