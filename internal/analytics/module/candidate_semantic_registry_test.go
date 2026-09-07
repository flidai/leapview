package module

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	"github.com/flidai/leapview/internal/semanticvalue"
)

type runtimeCandidateRegistryReader struct {
	value access.SemanticRegistryContext
	err   error
	reads int
}

func TestCandidateRuntimeRegistryIsNotSerialized(t *testing.T) {
	request := analyticsruntime.ProjectRequest{CandidateID: "candidate:one", ProjectID: "project:one"}
	before, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	request.CandidateSemanticRegistry = &access.SemanticRegistryContext{Control: access.AuthorizationControlRevision{InstanceID: "private-target-instance"}}
	after, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("runtime registry context entered request serialization")
	}
}

func (r *runtimeCandidateRegistryReader) ReadSemanticRegistry(context.Context, string) (access.SemanticRegistryContext, error) {
	r.reads++
	return r.value, r.err
}

func TestCandidateRuntimeRegistryAdmission(t *testing.T) {
	for _, name := range []string{"valid", "missing-evidence", "missing-authority", "unavailable", "stale-registry", "stale-control", "cross-instance", "cross-project", "mutated-definitions", "not-candidate"} {
		t.Run(name, func(t *testing.T) {
			expected := access.SemanticRegistryContext{
				Control: access.AuthorizationControlRevision{InstanceID: "instance:one", ProjectID: "project:one", Revision: 1},
				Registry: access.SemanticAttributeRegistrySnapshot{
					State: access.SemanticAttributeRegistryState{Profile: semanticvalue.Profile, Revision: 1, Digest: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
				},
			}
			reader := &runtimeCandidateRegistryReader{value: expected}
			module := &Module{}
			module.SetCandidateSemanticRegistry("instance:one", reader)
			request := analyticsruntime.ProjectRequest{CandidateID: "candidate:one", ProjectID: "project:one", CandidateSemanticRegistry: &expected}
			switch name {
			case "missing-evidence":
				request.CandidateSemanticRegistry = nil
			case "missing-authority":
				module.candidateSemanticRegistry = nil
			case "unavailable":
				reader.err = errors.New("authority unavailable")
			case "stale-registry":
				reader.value.Registry.State.Revision++
			case "stale-control":
				reader.value.Control.Revision++
			case "cross-instance":
				reader.value.Control.InstanceID = "instance:two"
			case "cross-project":
				reader.value.Control.ProjectID = "project:two"
			case "mutated-definitions":
				reader.value.Registry.Definitions = []access.SemanticAttributeDefinition{{Name: "unexpected"}}
			case "not-candidate":
				request.CandidateID = ""
			}
			compiled, err := module.candidateSemanticCompileContext(t.Context(), request)
			if name == "valid" {
				if err != nil || compiled == nil || compiled.Registry.State != expected.Registry.State || reader.reads != 1 {
					t.Fatalf("valid retained registry rejected: context=%+v reads=%d error=%v", compiled, reader.reads, err)
				}
			} else if err == nil || compiled != nil {
				t.Fatalf("%s accepted: context=%+v error=%v", name, compiled, err)
			}
			if module.semanticAccessAuthority != nil {
				t.Fatal("candidate registry configured production principal authority")
			}
		})
	}
}
