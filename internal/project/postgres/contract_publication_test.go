package postgres

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/project/contractprojection"
	"github.com/flidai/leapview/internal/project/contractpublication"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func publicationSource(t *testing.T, version string, optional bool) contractprojection.Source {
	t.Helper()
	fields := `"id":{"datatype":"Integer"}`
	if optional {
		fields += `,"name":{"datatype":"String","nullable":true}`
	}
	raw := `{"apiVersion":"leapview.dev/v1","kind":"Source","metadata":{"id":"source:orders","name":"orders"},"spec":{"connection":"warehouse","location":{"type":"path","path":"orders.csv","format":"csv"},"schema":{"mode":"strict","fields":{` + fields + `}}}}`
	var source projectcontracts.Source
	if err := json.Unmarshal([]byte(raw), &source); err != nil {
		t.Fatal(err)
	}
	projection, err := contractprojection.ProjectSource(source, contractprojection.Contract{Version: version, Compatibility: "backward"})
	if err != nil {
		t.Fatal(err)
	}
	return projection
}

func publicationInput(t *testing.T, version string, optional bool) contractpublication.ContractPublicationInput {
	t.Helper()
	return contractpublication.ContractPublicationInput{
		InstanceID: "instance:postgres",
		Projection: publicationSource(t, version, optional),
		Validation: contractpublication.ValidationEvidence{Version: contractpublication.ValidationEvidenceVersion, Checks: []contractpublication.ValidationCheck{{Name: "projection", Outcome: contractpublication.ValidationPassed, Reference: "go test"}}},
	}
}

func publishInTx(t *testing.T, db *pgxpool.Pool, input contractpublication.ContractPublicationInput, policy contractpublication.PolicyContext, approval *contractpublication.WideningApprovalEvidence) (contractpublication.ContractPublication, error) {
	t.Helper()
	tx, err := db.Begin(t.Context())
	if err != nil {
		return contractpublication.ContractPublication{}, err
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	publication, err := New(tx).PublishContractTx(t.Context(), tx, input, PublicationAdmission{
		PolicyContext: policy,
		Approval:      approval,
		Now:           time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		return contractpublication.ContractPublication{}, err
	}
	if err := tx.Commit(t.Context()); err != nil {
		return contractpublication.ContractPublication{}, err
	}
	return publication, nil
}

func TestContractPublicationChecksRejectNullJSONEvidence(t *testing.T) {
	db := identityTestDB(t)
	ctx := t.Context()
	prepared, err := contractpublication.Prepare(publicationInput(t, "1.0.0", false))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name             string
		mutateCanonical  func(map[string]any)
		mutateValidation func(map[string]any)
		valid            bool
	}{
		{name: "valid", valid: true},
		{name: "missing apiVersion", mutateCanonical: func(document map[string]any) { delete(document, "apiVersion") }},
		{name: "null apiVersion", mutateCanonical: func(document map[string]any) { document["apiVersion"] = nil }},
		{name: "missing profile", mutateCanonical: func(document map[string]any) { delete(document, "profile") }},
		{name: "null profile", mutateCanonical: func(document map[string]any) { document["profile"] = nil }},
		{name: "missing metadata id", mutateCanonical: func(document map[string]any) { delete(document["metadata"].(map[string]any), "id") }},
		{name: "null metadata id", mutateCanonical: func(document map[string]any) { document["metadata"].(map[string]any)["id"] = nil }},
		{name: "missing contract version", mutateCanonical: func(document map[string]any) {
			delete(document["metadata"].(map[string]any)["contract"].(map[string]any), "version")
		}},
		{name: "null contract version", mutateCanonical: func(document map[string]any) {
			document["metadata"].(map[string]any)["contract"].(map[string]any)["version"] = nil
		}},
		{name: "missing validation version", mutateValidation: func(evidence map[string]any) { delete(evidence, "version") }},
		{name: "null validation version", mutateValidation: func(evidence map[string]any) { evidence["version"] = nil }},
		{name: "missing validation checks", mutateValidation: func(evidence map[string]any) { delete(evidence, "checks") }},
		{name: "null validation checks", mutateValidation: func(evidence map[string]any) { evidence["checks"] = nil }},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			canonical := append([]byte(nil), prepared.CanonicalBytes...)
			digest := prepared.Digest
			if test.mutateCanonical != nil {
				var document map[string]any
				if err := json.Unmarshal(canonical, &document); err != nil {
					t.Fatal(err)
				}
				test.mutateCanonical(document)
				var err error
				canonical, err = json.Marshal(document)
				if err != nil {
					t.Fatal(err)
				}
				hash := sha256.Sum256(canonical)
				digest = "sha256:" + fmt.Sprintf("%x", hash)
			}
			validation, err := json.Marshal(prepared.Validation)
			if err != nil {
				t.Fatal(err)
			}
			if test.mutateValidation != nil {
				var evidence map[string]any
				if err := json.Unmarshal(validation, &evidence); err != nil {
					t.Fatal(err)
				}
				test.mutateValidation(evidence)
				validation, err = json.Marshal(evidence)
				if err != nil {
					t.Fatal(err)
				}
			}
			tx, err := db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			instanceID := fmt.Sprintf("instance:publication-null-check-%d", index)
			if test.valid {
				instanceID = prepared.InstanceID
			}
			_, err = tx.Exec(ctx, `
				INSERT INTO project.contract_publication(
					instance_id, authored_id, resource_kind, version, version_baseline,
					projection_profile, canonical_bytes, canonical_digest, validation_evidence_json
				) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
				instanceID, prepared.AuthoredID.String(), string(prepared.ResourceKind), prepared.Version, prepared.VersionBaseline,
				prepared.ProjectionProfile, canonical, digest, validation)
			if test.valid {
				if err != nil {
					_ = tx.Rollback(ctx)
					t.Fatal(err)
				}
				if err := tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				return
			}
			_ = tx.Rollback(ctx)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
				t.Fatalf("invalid evidence error = %v, want check_violation (23514)", err)
			}
		})
	}
}

func TestContractPublicationIsImmutableAndExactlyReplayable(t *testing.T) {
	db := identityTestDB(t)
	repo := New(db)
	ctx := t.Context()
	input := publicationInput(t, "1.0.0", false)
	genesis := contractpublication.PolicyContext{BaselineKind: contractpublication.BaselineGenesis}
	first, err := publishInTx(t, db, input, genesis, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := publishInTx(t, db, input, genesis, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !first.PublishedAt.Equal(second.PublishedAt) || !contractpublication.EqualContractPublicationContent(first, second) {
		t.Fatalf("exact replay changed evidence: first=%#v second=%#v", first, second)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.ContractPublicationTx(ctx, tx, first.InstanceID, first.AuthoredID, first.ResourceKind, "1.0.0+other-build")
	_ = tx.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !contractpublication.EqualContractPublicationContent(first, loaded) {
		t.Fatalf("replayed row differs: first=%#v loaded=%#v", first, loaded)
	}
	conflictInput := publicationInput(t, "1.0.0", true)
	if _, err := publishInTx(t, db, conflictInput, genesis, nil); !errors.Is(err, contractpublication.ErrPublicationConflict) {
		t.Fatalf("same-version drift error = %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE project.contract_publication SET canonical_digest=canonical_digest WHERE instance_id=$1`, first.InstanceID); err == nil {
		t.Fatal("publication UPDATE unexpectedly succeeded")
	}
	if _, err := db.Exec(ctx, `DELETE FROM project.contract_publication WHERE instance_id=$1`, first.InstanceID); err == nil {
		t.Fatal("publication DELETE unexpectedly succeeded")
	}
	if _, err := db.Exec(ctx, `TRUNCATE project.contract_publication`); err == nil {
		t.Fatal("publication TRUNCATE unexpectedly succeeded")
	}
}

func TestContractPublicationDigestTamperIsRejectedByStorage(t *testing.T) {
	db := identityTestDB(t)
	input := publicationInput(t, "1.0.0", false)
	publication, err := publishInTx(t, db, input, contractpublication.PolicyContext{BaselineKind: contractpublication.BaselineGenesis}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if _, err := db.Exec(ctx, `ALTER TABLE project.contract_publication DISABLE TRIGGER USER`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE project.contract_publication SET canonical_digest='sha256:' || repeat('0',64) WHERE instance_id=$1`, publication.InstanceID); err == nil {
		_, _ = db.Exec(ctx, `ALTER TABLE project.contract_publication ENABLE TRIGGER USER`)
		t.Fatal("canonical digest constraint accepted tampered evidence")
	}
	if _, err := db.Exec(ctx, `ALTER TABLE project.contract_publication ENABLE TRIGGER USER`); err != nil {
		t.Fatal(err)
	}
}

func TestContractPublicationQualifiedEvidenceMismatchFailsClosed(t *testing.T) {
	db := identityTestDB(t)
	publication, err := publishInTx(t, db, publicationInput(t, "1.0.0", false), contractpublication.PolicyContext{BaselineKind: contractpublication.BaselineGenesis}, nil)
	if err != nil {
		t.Fatal(err)
	}
	mutated, err := contractpublication.Prepare(publicationInput(t, "1.0.0", true))
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if _, err := db.Exec(ctx, `ALTER TABLE project.contract_publication DISABLE TRIGGER USER`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE project.contract_publication SET canonical_bytes=$1, canonical_digest=$2 WHERE instance_id=$3`, mutated.CanonicalBytes, mutated.Digest, publication.InstanceID); err != nil {
		_, _ = db.Exec(ctx, `ALTER TABLE project.contract_publication ENABLE TRIGGER USER`)
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `ALTER TABLE project.contract_publication ENABLE TRIGGER USER`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := New(db).ContractPublicationTx(ctx, tx, publication.InstanceID, publication.AuthoredID, publication.ResourceKind, publication.Version); !errors.Is(err, contractpublication.ErrInvalidPolicy) {
		t.Fatalf("mismatched qualified evidence error = %v", err)
	}
}

func TestContractPublicationUnknownValidationEvidenceFailsClosed(t *testing.T) {
	db := identityTestDB(t)
	publication, err := publishInTx(t, db, publicationInput(t, "1.0.0", false), contractpublication.PolicyContext{BaselineKind: contractpublication.BaselineGenesis}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if _, err := db.Exec(ctx, `ALTER TABLE project.contract_publication DISABLE TRIGGER USER`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE project.contract_publication SET validation_evidence_json=(validation_evidence_json::jsonb || '{"unknown":true}'::jsonb)::text WHERE instance_id=$1`, publication.InstanceID); err != nil {
		_, _ = db.Exec(ctx, `ALTER TABLE project.contract_publication ENABLE TRIGGER USER`)
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `ALTER TABLE project.contract_publication ENABLE TRIGGER USER`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := New(db).ContractPublicationTx(ctx, tx, publication.InstanceID, publication.AuthoredID, publication.ResourceKind, publication.Version); !errors.Is(err, contractpublication.ErrInvalidPolicy) {
		t.Fatalf("unknown validation evidence error = %v", err)
	}
}

func TestContractPublicationConcurrentAttemptsAreDeterministic(t *testing.T) {
	db := identityTestDB(t)
	input := publicationInput(t, "1.0.0", false)
	policy := contractpublication.PolicyContext{BaselineKind: contractpublication.BaselineGenesis}
	start := make(chan struct{})
	errs := make(chan error, 2)
	rows := make(chan contractpublication.ContractPublication, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			row, err := publishInTx(t, db, input, policy, nil)
			rows <- row
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	close(rows)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent exact publication: %v", err)
		}
	}
	var first contractpublication.ContractPublication
	for row := range rows {
		if first.PublishedAt.IsZero() {
			first = row
		} else if !first.PublishedAt.Equal(row.PublishedAt) {
			t.Fatalf("concurrent replay timestamps differ: %s/%s", first.PublishedAt, row.PublishedAt)
		}
	}

	left := publicationInput(t, "1.1.0", true)
	right := publicationInput(t, "1.1.0", false)
	baseline := first
	updatePolicy := contractpublication.PolicyContext{BaselineKind: contractpublication.BaselineExisting, Existing: &baseline}
	start = make(chan struct{})
	errs = make(chan error, 2)
	for _, candidate := range []contractpublication.ContractPublicationInput{left, right} {
		wg.Add(1)
		go func(candidate contractpublication.ContractPublicationInput) {
			defer wg.Done()
			<-start
			_, err := publishInTx(t, db, candidate, updatePolicy, nil)
			errs <- err
		}(candidate)
	}
	close(start)
	wg.Wait()
	close(errs)
	var succeeded, conflicted int
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, contractpublication.ErrPublicationConflict):
			conflicted++
		default:
			t.Fatalf("unexpected concurrent conflict error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent conflict outcomes success/conflict = %d/%d", succeeded, conflicted)
	}
}
