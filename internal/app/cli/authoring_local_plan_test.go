package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	projectdevloop "github.com/flidai/leapview/internal/project/devloop"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestLocalNativeDeliveryRevertPlansAgainstCurrentTarget(t *testing.T) {
	request, source := nativeDeliverySyncRequestForTest()
	local := localPublicationTestSession(t)
	local.state.Authority.ProjectUID = "finance"
	local.state.Authority.InstanceID = "target_native"
	stub := &localRevertTransport{nativeDeliveryTransportStub: nativeDeliveryTransportStub{sourceDigest: source}, revision: 1, plans: map[string]string{}, publications: map[string]deploymentgen.DeliveryPublicationEvidenceResponse{}}
	synchronize := func(request projectdevloop.SyncRequest) projectdevloop.Candidate {
		t.Helper()
		// Each retry can be a fresh process; keys must not depend on in-memory state.
		transport := newCandidateSynchronizationTransport(deploymentgen.NewGenClient(stub))
		transport.principalClient = accessgen.NewGenClient(stub)
		transport.canonicalOrigin = local.state.Network.URL
		transport.localDevelopment = &local
		stub.sourceDigest = request.Snapshot.Digest
		candidate, err := transport.SynchronizeNative(t.Context(), request, 1)
		require.NoError(t, err)
		return candidate
	}
	publish := func(candidate projectdevloop.Candidate) {
		t.Helper()
		_, err := publishAndActivateLocalCandidate(t.Context(), deploymentgen.NewGenClient(stub), local, candidate)
		require.NoError(t, err)
	}
	first := synchronize(request)
	require.Equal(t, first, synchronize(request), "an unchanged retry before activation must reuse its plan and build")
	publish(first)
	publish(first) // A lost acknowledgement must safely replay the exact committed publication.
	require.EqualValues(t, 2, stub.revision)

	changed := request
	changed.Snapshot.Artifacts = append([]projectdevloop.Artifact(nil), request.Snapshot.Artifacts...)
	changed.Snapshot.Artifacts[0].Content = []byte("source-b")
	changed.Snapshot.Artifacts[0].Digest = fmt.Sprintf("sha256:%x", sha256.Sum256(changed.Snapshot.Artifacts[0].Content))
	changed.Snapshot.Artifacts[0].SizeBytes = int64(len(changed.Snapshot.Artifacts[0].Content))
	changed.Snapshot.Digest = nativeCandidateSetDigestForTest(changed.Snapshot.Artifacts[0].Path, changed.Snapshot.Artifacts[0].Digest, changed.Snapshot.Artifacts[0].SizeBytes)
	second := synchronize(changed)
	publish(second)
	reverted := synchronize(request)
	require.NotEqual(t, first.PlanID, reverted.PlanID, "A→B→A must not replay the now inactive A plan")
	require.NotEqual(t, first.ID, reverted.ID)
	require.Equal(t, reverted, synchronize(request))
	publish(reverted)
	require.EqualValues(t, 4, stub.revision)
}

func TestLocalNativeDeliveryRejectsUnverifiedTargetBeforePlan(t *testing.T) {
	for _, name := range []string{"project", "target", "environment", "revision", "read failure", "not found"} {
		t.Run(name, func(t *testing.T) {
			request, source := nativeDeliverySyncRequestForTest()
			local := localPublicationTestSession(t)
			local.state.Authority.ProjectUID = "finance"
			local.state.Authority.InstanceID = "target_native"
			stub := &localRevertTransport{nativeDeliveryTransportStub: nativeDeliveryTransportStub{sourceDigest: source}, revision: 1, invalidSnapshot: name, plans: map[string]string{}}
			transport := newCandidateSynchronizationTransport(deploymentgen.NewGenClient(stub))
			transport.principalClient = accessgen.NewGenClient(stub)
			transport.canonicalOrigin = local.state.Network.URL
			transport.localDevelopment = &local
			_, err := transport.SynchronizeNative(t.Context(), request, 1)
			require.Error(t, err)
			require.False(t, stub.sawNativePlan, "unverified authority must never create a delivery plan")
		})
	}
}

func TestLocalNativeDeliveryRejectsTargetAdvanceDuringPlanning(t *testing.T) {
	request, source := nativeDeliverySyncRequestForTest()
	local := localPublicationTestSession(t)
	local.state.Authority.ProjectUID = "finance"
	local.state.Authority.InstanceID = "target_native"
	stub := &localRevertTransport{nativeDeliveryTransportStub: nativeDeliveryTransportStub{sourceDigest: source}, revision: 1, advanceDuringPlan: true}
	transport := newCandidateSynchronizationTransport(deploymentgen.NewGenClient(stub))
	transport.principalClient = accessgen.NewGenClient(stub)
	transport.canonicalOrigin = local.state.Network.URL
	transport.localDevelopment = &local
	_, err := transport.SynchronizeNative(t.Context(), request, 1)
	require.ErrorContains(t, err, "local target revision changed while planning")
	require.True(t, stub.sawNativePlan)
	require.False(t, stub.sawNativeBuild, "an observed target race must stop before building")
}

type localRevertTransport struct {
	nativeDeliveryTransportStub
	revision          int64
	active            string
	invalidSnapshot   string
	advanceDuringPlan bool
	plans             map[string]string
	publications      map[string]deploymentgen.DeliveryPublicationEvidenceResponse
}

func (stub *localRevertTransport) DoAPIGen(ctx context.Context, request apigenclient.Request, out any) (apigenclient.Response, error) {
	var body any
	switch request.OperationID {
	case deploymentgen.GenOperationGetDeliveryOperatorSnapshot:
		value := deploymentgen.DeliveryOperatorSnapshotResponse{ProjectId: "finance", TargetId: "target_native", Environment: "dev", TargetRevision: stub.revision}
		if stub.active != "" {
			value.ActiveGeneration = &stub.active
		}
		switch stub.invalidSnapshot {
		case "project":
			value.ProjectId = "other"
		case "target":
			value.TargetId = "other"
		case "environment":
			value.Environment = "production"
		case "revision":
			value.TargetRevision = -1
		case "read failure":
			return apigenclient.Response{}, errors.New("operator unavailable")
		case "not found":
			return apigenclient.Response{}, generatedProblemErrorForTest(http.StatusNotFound, "DELIVERY_OBJECT_NOT_FOUND")
		}
		body = value
	case deploymentgen.GenOperationPublishDeliveryCandidate:
		candidate := request.PathParams["candidate"]
		value, exists := stub.publications[candidate]
		if !exists {
			stub.revision++
			stub.active = "generation-" + candidate
			value = deploymentgen.DeliveryPublicationEvidenceResponse{Id: candidate, CandidateId: candidate, GenerationId: stub.active, PlanId: stub.plans[candidate], PlanDigest: localTestPlanDigest, ProjectId: "finance", TargetId: "target_native", Environment: "dev", Status: deploymentgen.DeliveryPublicationStatusCommitted, ResultTargetRevision: stub.revision}
			stub.publications[candidate] = value
		}
		body = value
	case deploymentgen.GenOperationGetDeliveryPublicationEvidence:
		body = stub.publications[request.PathParams["publication"]]
	default:
		response, err := stub.nativeDeliveryTransportStub.DoAPIGen(ctx, request, out)
		if err != nil {
			return response, err
		}
		switch value := out.(type) {
		case *deploymentgen.CandidateSourceSnapshotResponse:
			value.Environment = "dev"
		case *deploymentgen.DeliveryPlanPreviewResponse:
			value.Environment = "dev"
			value.BaseTargetRevision = stub.revision
			if stub.advanceDuringPlan {
				value.BaseTargetRevision++
			}
			value.Id = uuid.NewSHA1(uuid.NameSpaceURL, []byte(stub.nativePlanKey)).String()
		case *deploymentgen.DeliveryBuildStatusResponse:
			value.PlanId = request.PathParams["plan"]
			candidate := uuid.NewSHA1(uuid.NameSpaceURL, []byte(value.PlanId)).String()
			value.CandidateId = &candidate
			stub.plans[candidate] = value.PlanId
		}
		return response, nil
	}
	encoded, err := json.Marshal(body)
	if err == nil {
		err = json.Unmarshal(encoded, out)
	}
	return apigenclient.Response{StatusCode: http.StatusOK, Headers: make(http.Header), ContentType: "application/json"}, err
}
