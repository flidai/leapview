package materialize

import (
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/dataquery"
)

// Surface metadata never selects a different semantic decision or audit path.
// This is shared-executor evidence, not activation of anonymous/public access,
// a new export adapter, or a provider credential fixture.
func TestSemanticAuditSharedExecutionSurfacesCannotBypassPersistence(t *testing.T) {
	for _, surface := range []string{
		dataquery.SurfaceDashboard, dataquery.SurfaceAPI, dataquery.SurfaceAgent,
		dataquery.SurfaceCLI, dataquery.SurfaceDataExplorer, dataquery.SurfacePublicDashboard,
	} {
		t.Run(surface, func(t *testing.T) {
			for _, fail := range []bool{false, true} {
				t.Run(map[bool]string{false: "persisted", true: "recorder-failure"}[fail], func(t *testing.T) {
					database := &countingCacheRuntimeDatabase{}
					authority := &semanticConsumerAuthority{resolutions: []access.SemanticAttributeResolution{semanticConsumerResolution(t, "principal-1")}}
					runtime := newSemanticConsumerRuntime(t, database, authority)
					recorder := runtime.semanticAudit.Recorder.(*semanticAuditTestRecorder)
					if fail {
						recorder.err = errors.New("private audit storage detail")
					}
					request := semanticConsumerRequest()
					request.Surface = surface
					result, err := runtime.ExecuteDataQuery(t.Context(), request)
					if fail {
						if err == nil || len(result.Rows) != 0 || database.queries.Load() != 0 || len(recorder.Events()) != 0 {
							t.Fatalf("surface bypassed failed persistence: err=%v rows=%d executions=%d events=%d", err, len(result.Rows), database.queries.Load(), len(recorder.Events()))
						}
						return
					}
					if err != nil || database.queries.Load() == 0 || len(recorder.Events()) == 0 {
						t.Fatalf("allowed surface did not execute with audit: err=%v executions=%d events=%d", err, database.queries.Load(), len(recorder.Events()))
					}
					for _, event := range recorder.Events() {
						evidence, err := access.DecodeSemanticDecisionEvidence(event.MetadataJSON)
						if err != nil || event.PrincipalID != request.PrincipalID || event.RequestID != request.RequestID ||
							event.Resource.ID().String() != request.ModelID || evidence.InstanceID != "instance:test" ||
							!evidence.Allowed || evidence.Registry.Revision != 7 || evidence.Control.Revision != 11 {
							t.Fatalf("surface detached decision binding: %#v / %#v / %v", event, evidence, err)
						}
					}
				})
			}
		})
	}
}
