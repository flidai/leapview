package saved

import (
	"strings"
	"testing"
)

func TestExecuteRequestRequiresCompleteExportRevision(t *testing.T) {
	request := ExecuteRequest{ProjectID: "project:sales", ID: "exploration-1", ActorID: "owner", Operation: "saved_exploration_export"}
	if err := request.Validate(); err != nil {
		t.Fatalf("interactive-compatible zero revision rejected: %v", err)
	}
	request.ExpectedRevision = RevisionToken{RevisionID: "revision-1"}
	if err := request.Validate(); err == nil {
		t.Fatal("partial export revision accepted")
	}
	request.ExpectedRevision = RevisionToken{}
	request.Operation = strings.Repeat("x", MaxOperationLength+1)
	if err := request.Validate(); err == nil {
		t.Fatal("oversized execution operation accepted")
	}
}

func TestExecuteSpecRequestRejectsInvalidEnvelope(t *testing.T) {
	request := ExecuteSpecRequest{ProjectID: "project:sales", ActorID: "owner", Spec: testSpec()}
	if err := request.Validate(); err != nil {
		t.Fatalf("valid URL request rejected: %v", err)
	}
	request.RequestID = strings.Repeat("x", MaxRequestIDLength+1)
	if err := request.ValidateEnvelope(); err == nil {
		t.Fatal("oversized URL request ID accepted")
	}
}
