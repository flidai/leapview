package postgres

import (
	"bytes"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/project/contractprojection"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/identityledger"
)

func TestContractPublicationIsImmutableAndExactlyReplayable(t *testing.T) {
	repo, admin := newLedgerDatabase(t)
	ctx := t.Context()
	if _, err := repo.Activate(ctx, candidate("instance-contract", "bundle-1", "", resource("source:orders", projectgraph.KindSource))); err != nil {
		t.Fatal(err)
	}

	input := publicationInput("instance-contract", "source:orders", "1.2.3+build.1", "strict")
	first, err := repo.PublishContract(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.PublishedAt.IsZero() || first.Version != "1.2.3+build.1" || first.VersionBaseline != "1.2.3" || first.ProjectionProfile != contractprojection.Profile {
		t.Fatalf("published evidence = %#v", first)
	}
	wantBytes, err := contractprojection.CanonicalBytes(input.Projection)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest, err := contractprojection.Digest(input.Projection)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.CanonicalBytes, wantBytes) || first.Digest != wantDigest || len(first.Validation.Checks) != 2 {
		t.Fatalf("publication did not preserve canonical or validation evidence: %#v", first)
	}
	if _, err := repo.Activate(ctx, candidate("instance-contract-other", "bundle-1", "", resource("source:orders", projectgraph.KindSource))); err != nil {
		t.Fatal(err)
	}
	other, err := repo.PublishContract(ctx, publicationInput("instance-contract-other", "source:orders", "1.2.3+build.1", "compatible"))
	if err != nil {
		t.Fatalf("independent instance publication: %v", err)
	}
	if other.InstanceID == first.InstanceID || other.Digest == first.Digest {
		t.Fatal("independent instance publication replayed another instance's evidence")
	}

	replayed, err := repo.PublishContract(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.ContractPublication(ctx, "instance-contract", "source:orders", projectgraph.KindSource, "1.2.3+different-build")
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.PublishedAt.Equal(first.PublishedAt) || !loaded.PublishedAt.Equal(first.PublishedAt) || !identityledger.EqualContractPublicationContent(first, loaded) {
		t.Fatalf("exact replay changed evidence: first=%#v replay=%#v loaded=%#v", first, replayed, loaded)
	}

	conflicting := publicationInput("instance-contract", "source:orders", "1.2.3+build.2", "compatible")
	if _, err := repo.PublishContract(ctx, conflicting); !errors.Is(err, identityledger.ErrContractPublicationConflict) {
		t.Fatalf("build-metadata/content conflict error = %v", err)
	}
	changedValidation := input
	changedValidation.Validation.Checks[0].Reference = "different validation command"
	if _, err := repo.PublishContract(ctx, changedValidation); !errors.Is(err, identityledger.ErrContractPublicationConflict) {
		t.Fatalf("validation evidence conflict error = %v", err)
	}
	older := publicationInput("instance-contract", "source:orders", "1.2.2", "strict")
	if _, err := repo.PublishContract(ctx, older); !errors.Is(err, identityledger.ErrContractPublicationConflict) {
		t.Fatalf("older version publication error = %v", err)
	}
	newer := publicationInput("instance-contract", "source:orders", "1.2.4", "strict")
	if _, err := repo.PublishContract(ctx, newer); err != nil {
		t.Fatalf("newer exact-content version publication: %v", err)
	}

	if _, err := admin.Exec(ctx, `UPDATE project.contract_publication SET canonical_digest=canonical_digest WHERE instance_id='instance-contract'`); err == nil {
		t.Fatal("published contract update unexpectedly succeeded")
	}
	if _, err := admin.Exec(ctx, `DELETE FROM project.contract_publication WHERE instance_id='instance-contract'`); err == nil {
		t.Fatal("published contract deletion unexpectedly succeeded")
	}
	if _, err := admin.Exec(ctx, `TRUNCATE project.contract_publication`); err == nil {
		t.Fatal("published contract truncation unexpectedly succeeded")
	}
}

func TestContractPublicationConcurrency(t *testing.T) {
	repo, _ := newLedgerDatabase(t)
	ctx := t.Context()
	for _, id := range []string{"source:replay", "source:conflict"} {
		if _, err := repo.Activate(ctx, candidate("instance-race-contract", "bundle-"+id, activeBundleFor(id), resource(id, projectgraph.KindSource))); err != nil {
			t.Fatal(err)
		}
	}

	exact := publicationInput("instance-race-contract", "source:replay", "1.0.0", "strict")
	start := make(chan struct{})
	rows := make(chan identityledger.ContractPublication, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			row, err := repo.PublishContract(ctx, exact)
			rows <- row
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(rows)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent exact publication: %v", err)
		}
	}
	var publishedAt time.Time
	for row := range rows {
		if publishedAt.IsZero() {
			publishedAt = row.PublishedAt
		} else if !row.PublishedAt.Equal(publishedAt) {
			t.Fatalf("concurrent exact replay timestamps = %s and %s", publishedAt, row.PublishedAt)
		}
	}

	left := publicationInput("instance-race-contract", "source:conflict", "1.0.0", "strict")
	right := publicationInput("instance-race-contract", "source:conflict", "1.0.0", "compatible")
	start = make(chan struct{})
	errs = make(chan error, 2)
	for _, input := range []identityledger.ContractPublicationInput{left, right} {
		wg.Add(1)
		go func(input identityledger.ContractPublicationInput) {
			defer wg.Done()
			<-start
			_, err := repo.PublishContract(ctx, input)
			errs <- err
		}(input)
	}
	close(start)
	wg.Wait()
	close(errs)
	var succeeded, conflicted int
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, identityledger.ErrContractPublicationConflict):
			conflicted++
		default:
			t.Fatalf("unexpected conflicting publication error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent conflict outcomes success/conflict = %d/%d", succeeded, conflicted)
	}
}

func publicationInput(instance, authoredID, version, mode string) identityledger.ContractPublicationInput {
	encoded, _ := json.Marshal(map[string]any{
		"apiVersion": "leapview.dev/v1", "kind": "Source",
		"metadata": map[string]any{"id": authoredID, "name": "orders"},
		"spec": map[string]any{
			"connection": "connection:warehouse",
			"location":   map[string]any{"type": "path", "path": "/tmp/orders.csv", "format": "csv"},
			"schema":     map[string]any{"mode": mode, "fields": map[string]any{"order_id": map[string]any{"datatype": "String", "nullable": false}}},
		},
	})
	var authored projectcontracts.Source
	if err := json.Unmarshal(encoded, &authored); err != nil {
		panic(err)
	}
	projection, err := contractprojection.ProjectSource(authored, contractprojection.Contract{Version: version, Compatibility: "backward"})
	if err != nil {
		panic(err)
	}
	return identityledger.ContractPublicationInput{
		InstanceID: instance, Projection: projection,
		Validation: identityledger.ValidationEvidence{Version: 1, Checks: []identityledger.ValidationCheck{
			{Name: "generated-contract", Outcome: identityledger.ValidationPassed, Reference: "task generated:check"},
			{Name: "projection-tests", Outcome: identityledger.ValidationPassed, Reference: "go test ./internal/project/contractprojection"},
		}},
	}
}

func activeBundleFor(id string) string {
	if id == "source:replay" {
		return ""
	}
	return "bundle-source:replay"
}
