package app

import (
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	projectdevloop "github.com/flidai/leapview/internal/project/devloop"
)

func sourceJourneySnapshot(t *testing.T) projectdevloop.Snapshot {
	t.Helper()
	root := t.TempDir()
	for path, body := range map[string]string{
		"connections/warehouse.yaml": `apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: "connection:warehouse", name: warehouse}
spec: {type: postgres}
`,
		"sources/orders.yaml": `apiVersion: leapview.dev/v1
kind: Source
metadata: {id: "source:raw_orders", name: raw_orders}
spec:
  connection: warehouse
  location: {type: relation, schema: public, name: orders}
  schema: {mode: strict}
  fields:
    - {name: id, datatype: Integer}
    - {name: amount, datatype: Integer}
`,
		"models/orders.yaml": `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: "model:orders", name: orders}
spec:
  definition: {type: sql, sql: 'SELECT id, amount FROM source.raw_orders'}
  entities: [{name: order, type: primary, fields: [id]}]
  grain: {entity: order}
  fields:
    - {name: id, datatype: Integer}
    - {name: amount, datatype: Integer}
`,
		"semantic-models/sales.yaml": `apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: "semantic-model:sales", name: sales}
spec:
  datasets:
    - name: orders
      model: orders
      metrics:
        - {name: total, type: simple, agg: sum, field: amount}
`,
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, path), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := (projectdevloop.FilesystemBuilder{SourceRoot: root, ProjectID: sourceJourneyProject, CandidateKey: "source-journey-initial"}).Build(t.Context())
	if err != nil {
		t.Fatalf("compile real source snapshot: %v", err)
	}
	return snapshot
}

func (f *sourceCredentialHTTPJourney) publishSource(t *testing.T, token string) deploymentgen.DeliveryPublicationEvidenceResponse {
	t.Helper()
	snapshot := f.snapshot
	sourceOnly := true
	body := deploymentgen.CandidateSynchronizationRequest{ArtifactDigest: snapshot.Digest, SourceOnly: &sourceOnly}
	for _, a := range snapshot.Artifacts {
		body.Artifacts = append(body.Artifacts, deploymentgen.CandidateSourceArtifact{Path: a.Path, Digest: a.Digest, SizeBytes: a.SizeBytes})
	}
	client := deploymentgen.NewGenClient(f.transport(token))
	planned, err := client.PlanProjectCandidateSynchronization(t.Context(), deploymentgen.GenPlanProjectCandidateSynchronizationClientRequest{Project: sourceJourneyProject, Headers: deploymentgen.GenPlanProjectCandidateSynchronizationClientHeaders{IdempotencyKey: "source-journey-sync"}, Body: body})
	if err != nil {
		t.Fatalf("plan source synchronization: %v", err)
	}
	for _, missing := range planned.Body.MissingDigests {
		found := false
		for _, a := range snapshot.Artifacts {
			if a.Digest != missing {
				continue
			}
			found = true
			decoded, err := hex.DecodeString(strings.TrimPrefix(a.Digest, "sha256:"))
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.UploadProjectCandidateSourceBlob(t.Context(), deploymentgen.GenUploadProjectCandidateSourceBlobClientRequest{Project: sourceJourneyProject, Digest: a.Digest, Headers: deploymentgen.GenUploadProjectCandidateSourceBlobClientHeaders{ContentType: "application/octet-stream", ContentDigest: "sha-256=:" + base64.StdEncoding.EncodeToString(decoded) + ":", SourceSynchronizationPlan: planned.Body.PlanId}, Body: a.Content})
			if err != nil {
				t.Fatalf("upload source blob: %v", err)
			}
		}
		if !found {
			t.Fatal("source plan requested absent digest")
		}
	}
	retained, err := client.RetainProjectCandidateSource(t.Context(), deploymentgen.GenRetainProjectCandidateSourceClientRequest{Project: sourceJourneyProject, Headers: deploymentgen.GenRetainProjectCandidateSourceClientHeaders{IdempotencyKey: "source-journey-retain", SourceSynchronizationPlan: planned.Body.PlanId}, Body: body})
	if err != nil {
		t.Fatalf("retain exact source: %v", err)
	}
	plan, err := client.CreateDeliveryPlan(t.Context(), deploymentgen.GenCreateDeliveryPlanClientRequest{Project: sourceJourneyProject, Headers: deploymentgen.GenCreateDeliveryPlanClientHeaders{IdempotencyKey: "source-journey-delivery-plan"}, Body: deploymentgen.DeliveryPlanRequest{TargetId: f.instance, Operation: deploymentgen.DeliveryOperationKindCodeChange, SourceDigest: retained.Body.SourceDigest, SourceAttestationDigest: retained.Body.SourceAttestationDigest}})
	if err != nil {
		t.Fatalf("plan native source: %v", err)
	}
	build, err := client.BuildDeliveryPlan(t.Context(), deploymentgen.GenBuildDeliveryPlanClientRequest{Project: sourceJourneyProject, Plan: plan.Body.Id, Headers: deploymentgen.GenBuildDeliveryPlanClientHeaders{IdempotencyKey: "source-journey-build"}})
	if err != nil {
		t.Fatalf("build native source: %v", err)
	}
	if build.Body.CandidateId == nil || build.Body.Status != deploymentgen.DeliveryBuildStatusSealed {
		t.Fatalf("source build state %s", build.Body.Status)
	}
	published, err := client.PublishDeliveryCandidate(t.Context(), deploymentgen.GenPublishDeliveryCandidateClientRequest{Project: sourceJourneyProject, Candidate: *build.Body.CandidateId, Headers: deploymentgen.GenPublishDeliveryCandidateClientHeaders{IdempotencyKey: "source-journey-publish"}})
	if err != nil {
		t.Fatalf("publish native source: %v", err)
	}
	publication := published.Body
	deadline := time.Now().Add(45 * time.Second)
	for publication.Status == deploymentgen.DeliveryPublicationStatusPending && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		latest, err := client.GetDeliveryPublicationEvidence(t.Context(), deploymentgen.GenGetDeliveryPublicationEvidenceClientRequest{Project: sourceJourneyProject, Publication: publication.Id})
		if err != nil {
			t.Fatalf("poll same publication: %v", err)
		}
		publication = latest.Body
	}
	if publication.Status != deploymentgen.DeliveryPublicationStatusCommitted {
		t.Fatalf("native publication state %s reason %v", publication.Status, publication.Reason)
	}
	if publication.ProjectId != sourceJourneyProject || publication.TargetId != f.instance || publication.GenerationId == "" {
		t.Fatal("native publication identity drift")
	}
	// Publication commits durable authority before the normal activation worker
	// has installed its runtime. Await readiness instead of treating commit as
	// serving acknowledgement.
	deadline = time.Now().Add(45 * time.Second)
	lastReadiness := ""
	for time.Now().Before(deadline) {
		response := httptest.NewRecorder()
		f.target.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "https://localhost/readyz", nil))
		if response.Code == http.StatusOK {
			return publication
		}
		if current := response.Body.String(); current != lastReadiness {
			lastReadiness = current
			t.Logf("waiting for native source runtime: %s", current)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("committed source publication did not become ready")
	return publication
}
