package module

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/deployment"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	projectpipelineplan "github.com/flidai/leapview/internal/project/contracts/pipelineplan"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

const firstSourcePlanPreparationID = "0198f2c0-7c7a-7f00-8a11-000000000301"

func firstSourcePlanRequest() NativeDeliveryPlanRequest {
	return NativeDeliveryPlanRequest{ProjectID: projectgraph.ResourceID("finance"), TargetID: "target", Environment: "prod", PrincipalID: "operator", SourceOwnerID: "author", Operation: "code_change", SourceDigest: nativeDigest('a'), SourceAttestationDigest: nativeDigest('b'), IdempotencyKey: "plan-key"}
}

func TestFirstSourcePlanRequestDigestPreservesLegacyIdentity(t *testing.T) {
	request := firstSourcePlanRequest()
	legacy, err := NativeDeliveryPlanRequestDigest(request)
	require.NoError(t, err)
	// Frozen hash of the pre-selector JSON projection, including original key
	// order and casing. An omitted selector must preserve durable retry keys.
	require.Equal(t, "sha256:65ee6f6dd0c1c5b652d4d7702c5dd800248be42eb557355925c91d24d9d639bf", legacy)
	request.FirstSourcePreparationID = firstSourcePlanPreparationID
	require.NoError(t, request.validate("prod"))
	selected, err := NativeDeliveryPlanRequestDigest(request)
	require.NoError(t, err)
	require.NotEqual(t, legacy, selected)
	retry, err := NativeDeliveryPlanRequestDigest(request)
	require.NoError(t, err)
	require.Equal(t, selected, retry)
	request.FirstSourcePreparationID = "0198f2c0-7c7a-7f00-8a11-000000000302"
	changed, err := NativeDeliveryPlanRequestDigest(request)
	require.NoError(t, err)
	require.NotEqual(t, selected, changed)
}

func TestFirstSourcePlanRequestRejectsInvalidSelectorIntent(t *testing.T) {
	for _, scenario := range []string{"malformed", "uppercase", "nil-uuid", "whitespace", "restatement", "binding_change", "policy_change", "pipeline"} {
		t.Run(scenario, func(t *testing.T) {
			r := firstSourcePlanRequest()
			r.FirstSourcePreparationID = firstSourcePlanPreparationID
			switch scenario {
			case "malformed":
				r.FirstSourcePreparationID = "version:latest"
			case "uppercase":
				r.FirstSourcePreparationID = strings.ToUpper(firstSourcePlanPreparationID)
			case "nil-uuid":
				r.FirstSourcePreparationID = "00000000-0000-0000-0000-000000000000"
			case "whitespace":
				r.FirstSourcePreparationID += " "
			case "pipeline":
				r.PipelinePlan = &projectpipelineplan.Plan{}
			default:
				r.Operation = scenario
			}
			require.ErrorIs(t, r.validate("prod"), deployment.ErrDeliveryInvalid)
			_, err := NativeDeliveryPlanRequestDigest(r)
			require.ErrorIs(t, err, deployment.ErrDeliveryInvalid)
		})
	}
}

func TestFirstSourcePlanHTTPPropagatesOnlyValidPreparationIntent(t *testing.T) {
	for _, scenario := range []string{"valid", "absent", "empty", "malformed", "restatement"} {
		t.Run(scenario, func(t *testing.T) {
			called := false
			m := nativeDeliveryHandlerModule(NativeDeliveryMutationFuncs{Plan: func(_ context.Context, r NativeDeliveryPlanRequest) (NativeDeliveryPlan, error) {
				called = true
				want := firstSourcePlanPreparationID
				if scenario == "absent" {
					want = ""
				}
				require.Equal(t, want, r.FirstSourcePreparationID)
				require.Equal(t, "operator", r.PrincipalID)
				require.Equal(t, "operator", r.SourceOwnerID)
				return NativeDeliveryPlan{}, ErrDeliveryInputUnavailable
			}})
			body := deploymentgen.DeliveryPlanRequest{TargetId: "target", Operation: deploymentgen.DeliveryOperationKindCodeChange, SourceDigest: nativeDigest('a'), SourceAttestationDigest: nativeDigest('b')}
			selector := firstSourcePlanPreparationID
			if scenario == "empty" {
				selector = ""
			}
			if scenario == "malformed" {
				selector = "version:latest"
			}
			if scenario != "absent" {
				body.FirstSourcePreparationId = &selector
			}
			if scenario == "restatement" {
				body.Operation = deploymentgen.DeliveryOperationKindRestatement
			}
			encoded, err := json.Marshal(body)
			require.NoError(t, err)
			recorder := httptest.NewRecorder()
			m.CreateDeliveryPlan(recorder, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(encoded)), "finance", "plan-key")
			valid := scenario == "valid" || scenario == "absent"
			require.Equal(t, valid, called)
			if valid {
				require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
			} else {
				require.Equal(t, http.StatusUnprocessableEntity, recorder.Code)
			}
		})
	}
}
