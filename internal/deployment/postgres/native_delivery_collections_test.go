package postgres

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestDeliveryCollectionPageArgsAreStableAndBounded(t *testing.T) {
	if got, cursor, err := deliveryPageArgs(1, ""); err != nil || got != 2 || cursor != "" {
		t.Fatalf("first page args = %d, %q, %v", got, cursor, err)
	}
	if _, _, err := deliveryPageArgs(0, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero page limit = %v, want ErrInvalid", err)
	}
	if _, _, err := deliveryPageArgs(MaxDeliveryCollectionPageSize+1, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized page limit = %v, want ErrInvalid", err)
	}
	for _, token := range []string{"not-a-cursor", "k1.!", "k1." + base64.RawURLEncoding.EncodeToString([]byte(`{"id":"not-a-uuid"}`))} {
		if _, _, err := deliveryPageArgs(1, token); !errors.Is(err, ErrInvalid) {
			t.Errorf("invalid cursor %q = %v, want ErrInvalid", token, err)
		}
	}
	payload, err := json.Marshal(deliveryCursor{ID: "0198f2c0-7c7a-7000-8000-000000000001"})
	if err != nil {
		t.Fatal(err)
	}
	token := "k1." + base64.RawURLEncoding.EncodeToString(payload)
	if got, cursor, err := deliveryPageArgs(3, token); err != nil || got != 4 || cursor != "0198f2c0-7c7a-7000-8000-000000000001" {
		t.Fatalf("cursor args = %d, %q, %v", got, cursor, err)
	}
}

func TestPostgresNativeDeliveryCollectionsAreScopedBoundedAndRetentionAware(t *testing.T) {
	p := deliveryTestDB(t)
	r := New(p)
	ctx := t.Context()
	f := newCompleteBuildFixtureWithSuffix(t, r, "9")

	if _, err := r.CommitBuildAttempt(ctx, CommitAttemptInput{
		AttemptID: f.AttemptID, OwnerID: f.Lease.OwnerID, FencingEpoch: f.Lease.FencingEpoch,
		SnapshotID: 42, CommitMarker: testCommitMarker(f.AttemptID, "pool-complete", f.RequestDigest, f.PlanDigest),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateSnapshotSeal(ctx, f.Seal); err != nil {
		t.Fatal(err)
	}
	if _, err := r.QualifyCandidate(ctx, f.CandidateID, f.SealID, testDigest('3')); err != nil {
		t.Fatal(err)
	}
	generationID := "0198f2c0-7c7a-7f00-0000-000000009007"
	if _, err := r.CreateGeneration(ctx, GenerationInput{
		GenerationID: generationID, TargetID: f.TargetID, CandidateID: f.CandidateID,
		SnapshotSealID: f.SealID, PlanID: f.PlanID, PlanDigest: f.PlanDigest,
		ArtifactRoot: f.Seal.ArtifactRoot, ArtifactRootDigest: f.Seal.ArtifactRootDigest,
		ServingArtifactDigest: f.ArtifactDigest, CompiledGraphDigest: testDigest('b'),
		CompiledConfigDigest: testDigest('c'), SecurityDomainFingerprint: testDigest('d'),
		GenerationRevision: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO ducklake.catalog_identity(physical_pool_id,catalog_database,catalog_id,catalog_uuid,metadata_schema) VALUES($1,$2,$3,$4::uuid,$5)`, f.Seal.PhysicalPoolID, f.Seal.CatalogDatabase, f.Seal.CatalogID, f.Seal.CatalogUUID, "lake"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO ducklake.snapshot_retention(physical_pool_id,catalog_id,snapshot_id,state) VALUES($1,$2,$3,'live')`, f.Seal.PhysicalPoolID, f.Seal.CatalogID, f.Seal.DuckLakeSnapshotID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateRetentionRoot(ctx, DeliveryRetentionRoot{
		RootID: f.CandidateID, TargetID: f.TargetID, CandidateID: f.CandidateID,
		GenerationID: generationID, SnapshotSealID: f.SealID, RootKind: "candidate",
		State: "live", ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateRetentionRoot(ctx, DeliveryRetentionRoot{
		RootID: "0198f2c0-7c7a-7f00-0000-000000009008", TargetID: f.TargetID,
		CandidateID: f.CandidateID, GenerationID: generationID, SnapshotSealID: f.SealID,
		RootKind: "generation", State: "live", ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	publicationID := "0198f2c0-7c7a-7f00-0000-000000009006"
	if _, err := r.CreatePublication(ctx, PublicationInput{
		PublicationID: publicationID, TargetID: f.TargetID, GenerationID: generationID,
		CandidateID: f.CandidateID, SnapshotSealID: f.SealID, ExpectedTargetRevision: 1,
		ActorID: "operator", RequestDigest: testDigest('4'),
	}); err != nil {
		t.Fatal(err)
	}

	publications, err := r.ListPublications(ctx, "project_complete_build_9", f.TargetID, "prod", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(publications.Items) != 1 || publications.Items[0].PublicationID != publicationID || publications.NextCursor != nil {
		t.Fatalf("publication collection = %#v, want one bounded page without next cursor", publications)
	}
	generations, err := r.ListRetainedGenerations(ctx, "project_complete_build_9", f.TargetID, "prod", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(generations.Items) != 1 || generations.Items[0].GenerationID != generationID || generations.NextCursor != nil {
		t.Fatalf("retained generation collection = %#v, want one bounded page without next cursor", generations)
	}
	plans, err := r.ListPlans(ctx, "project_complete_build_9", f.TargetID, "prod", 1, "")
	if err != nil || len(plans.Items) != 1 || plans.Items[0].PlanID != f.PlanID || plans.NextCursor != nil {
		t.Fatalf("plan collection = %#v, want one bounded page without next cursor (err=%v)", plans, err)
	}
	attempts, err := r.ListBuildAttempts(ctx, "project_complete_build_9", f.TargetID, "prod", 1, "")
	if err != nil || len(attempts.Items) != 1 || attempts.Items[0].AttemptID != f.AttemptID || attempts.Items[0].State != AttemptCommitted || attempts.NextCursor != nil {
		t.Fatalf("build collection = %#v, want committed attempt page without next cursor (err=%v)", attempts, err)
	}
	candidates, err := r.ListCandidates(ctx, "project_complete_build_9", f.TargetID, "prod", 1, "")
	if err != nil || len(candidates.Items) != 1 || candidates.Items[0].CandidateID != f.CandidateID || candidates.Items[0].Status != "qualified" || candidates.NextCursor != nil {
		t.Fatalf("candidate collection = %#v, want qualified candidate page without next cursor (err=%v)", candidates, err)
	}

	outsidePublicationCursor := deliveryNextCursor(publicationID)
	if _, err := r.ListPublications(ctx, "project_other", "target_other", "prod", 1, *outsidePublicationCursor); !errors.Is(err, ErrInvalid) {
		t.Fatalf("out-of-scope publication cursor = %v, want ErrInvalid", err)
	}
	outsideGenerationCursor := deliveryNextCursor(generationID)
	if _, err := r.ListRetainedGenerations(ctx, "project_other", "target_other", "prod", 1, *outsideGenerationCursor); !errors.Is(err, ErrInvalid) {
		t.Fatalf("out-of-scope generation cursor = %v, want ErrInvalid", err)
	}
	for name, list := range map[string]func(string) error{
		"plan": func(cursor string) error {
			_, err := r.ListPlans(ctx, "project_other", "target_other", "prod", 1, cursor)
			return err
		},
		"build": func(cursor string) error {
			_, err := r.ListBuildAttempts(ctx, "project_other", "target_other", "prod", 1, cursor)
			return err
		},
		"candidate": func(cursor string) error {
			_, err := r.ListCandidates(ctx, "project_other", "target_other", "prod", 1, cursor)
			return err
		},
	} {
		cursorID := f.PlanID
		if name == "build" {
			cursorID = f.AttemptID
		} else if name == "candidate" {
			cursorID = f.CandidateID
		}
		if err := list(*deliveryNextCursor(cursorID)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("out-of-scope %s cursor = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := r.ListPublications(ctx, "project_complete_build_9", f.TargetID, "prod", 0, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero publication limit = %v, want ErrInvalid", err)
	}

	if _, err := p.Exec(ctx, `UPDATE delivery.delivery_retention_root SET expires_at=clock_timestamp()-interval '1 second' WHERE target_id=$1 AND generation_id=$2::uuid AND root_kind='generation'`, f.TargetID, generationID); err != nil {
		t.Fatal(err)
	}
	expired, err := r.ListRetainedGenerations(ctx, "project_complete_build_9", f.TargetID, "prod", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(expired.Items) != 0 {
		t.Fatalf("expired retained generations = %#v, want empty", expired.Items)
	}
}

func TestPostgresApprovalRequestCollectionPreservesLatestDecisionEvidence(t *testing.T) {
	f := newApprovalFixture(t)
	ctx := t.Context()
	created, err := f.authority.Request(ctx, f.request)
	if err != nil {
		t.Fatal(err)
	}
	page, err := f.repository.ListApprovalRequests(ctx, "project_approval", "target_approval", "prod", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].RequestID != created.RequestID || page.Items[0].LatestDecision != nil || page.NextCursor != nil {
		t.Fatalf("approval request collection = %#v, want one pending immutable request", page)
	}
	if _, err := f.repository.ListApprovalRequests(ctx, "project_other", "target_other", "prod", 1, *deliveryNextCursor(created.RequestID)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("out-of-scope approval request cursor = %v, want ErrInvalid", err)
	}
}
