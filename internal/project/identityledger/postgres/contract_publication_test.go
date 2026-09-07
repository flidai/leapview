package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/project/contractprojection"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	"github.com/flidai/leapview/internal/project/contractversion"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/identityledger"
	"github.com/flidai/leapview/internal/semanticvalue"
	"github.com/jackc/pgx/v5"
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
	if first.Validation.PolicyEvidence == nil || first.Validation.PolicyEvidence.Version != identityledger.PolicyEvidenceVersion || first.Validation.PolicyEvidence.BaselineKind != identityledger.PolicyBaselineGenesis || first.Validation.PolicyEvidence.LifecycleSequence != 1 {
		t.Fatalf("publication did not preserve server policy evidence: %#v", first.Validation)
	}
	forged := input
	forged.Validation.PolicyEvidence = &identityledger.PolicyEvidence{Version: identityledger.PolicyEvidenceVersion}
	if _, err := repo.PublishContract(ctx, forged); !errors.Is(err, identityledger.ErrPolicyEvidenceInvalid) {
		t.Fatalf("client-supplied policy evidence error = %v, want invalid evidence", err)
	}
	decision, err := first.PolicyDecision()
	if err != nil || decision.EvidenceDigest != first.Validation.PolicyEvidence.EvidenceDigest {
		t.Fatalf("policy decision = %#v, %v", decision, err)
	}
	forgedEvidence := *first.Validation.PolicyEvidence
	forgedEvidence.ApprovalState = identityledger.PolicyApprovalRequired
	forgedPublication := first
	forgedPublication.Validation.PolicyEvidence = &forgedEvidence
	if _, err := forgedPublication.PolicyDecision(); !errors.Is(err, identityledger.ErrPolicyEvidenceInvalid) {
		t.Fatalf("approval-state forgery error = %v, want invalid evidence", err)
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
	changedValidation.Validation.Checks = append([]identityledger.ValidationCheck(nil), input.Validation.Checks...)
	changedValidation.Validation.Checks[0].Reference = "different validation command"
	if _, err := repo.PublishContract(ctx, changedValidation); !errors.Is(err, identityledger.ErrContractPublicationConflict) {
		t.Fatalf("validation evidence conflict error = %v", err)
	}
	older := publicationInput("instance-contract", "source:orders", "1.2.2", "strict")
	older.PolicyContext = existingPolicyContext(first)
	if _, err := repo.PublishContract(ctx, older); !errors.Is(err, identityledger.ErrContractPublicationConflict) {
		t.Fatalf("older version publication error = %v", err)
	}
	newer := publicationInput("instance-contract", "source:orders", "1.2.4", "strict")
	newer.PolicyContext = existingPolicyContext(first)
	second, err := repo.PublishContract(ctx, newer)
	if err != nil {
		t.Fatalf("newer exact-content version publication: %v", err)
	}
	// Replaying the original version after a later publication must use its
	// original genesis context and immutable row, never the latest baseline.
	replayedAfterNewer, err := repo.PublishContract(ctx, input)
	if err != nil || !identityledger.EqualContractPublicationContent(first, replayedAfterNewer) {
		t.Fatalf("original replay after newer publication = %#v, %v", replayedAfterNewer, err)
	}
	third := publicationInput("instance-contract", "source:orders", "1.2.5", "strict")
	third.PolicyContext = existingPolicyContext(second)
	if _, err := repo.PublishContract(ctx, third); err != nil {
		t.Fatalf("third publication from current baseline: %v", err)
	}
	replayedAfterThird, err := repo.PublishContract(ctx, newer)
	if err != nil || !identityledger.EqualContractPublicationContent(second, replayedAfterThird) {
		t.Fatalf("original newer replay after third publication = %#v, %v", replayedAfterThird, err)
	}
	wrongReplay := newer
	wrongReplay.PolicyContext = existingPolicyContext(second)
	if _, err := repo.PublishContract(ctx, wrongReplay); !errors.Is(err, identityledger.ErrContractPublicationConflict) {
		t.Fatalf("replay with self-baseline context error = %v, want immutable evidence conflict", err)
	}
	stale := publicationInput("instance-contract", "source:orders", "1.2.6", "strict")
	stale.PolicyContext = existingPolicyContext(first)
	if _, err := repo.PublishContract(ctx, stale); !errors.Is(err, identityledger.ErrPolicyEvidenceConflict) {
		t.Fatalf("stale earlier baseline error = %v, want policy conflict", err)
	}
	changedBaseline := publicationInput("instance-contract", "source:orders", "1.2.5", "strict")
	changedBaseline.PolicyContext = existingPolicyContext(first)
	changedBaseline.PolicyContext.Baseline.Digest = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	if _, err := repo.PublishContract(ctx, changedBaseline); !errors.Is(err, identityledger.ErrPolicyEvidenceConflict) {
		t.Fatalf("changed baseline publication error = %v, want policy conflict", err)
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
	if _, err := repo.Activate(ctx, candidate("instance-race-contract", "bundle-initial", "",
		resource("source:replay", projectgraph.KindSource), resource("source:conflict", projectgraph.KindSource))); err != nil {
		t.Fatal(err)
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

func TestScanContractPublicationRejectsUnknownValidationEvidence(t *testing.T) {
	for _, validation := range []string{
		`{"version":1,"checks":[{"name":"projection","outcome":"passed","reference":"test"}],"unknown":true}`,
		`{"version":1,"checks":[{"name":"projection","outcome":"passed","reference":"test"}],"policyEvidence":{"version":2,"unknown":true}}`,
	} {
		row := publicationScanFixture{validation: validation}
		if _, err := scanContractPublication(row); err == nil {
			t.Fatalf("unknown stored validation evidence was accepted: %s", validation)
		}
	}
}

func TestContractPublicationPreservesSecurityWideningApproval(t *testing.T) {
	repo, _ := newLedgerDatabase(t)
	ctx := t.Context()
	const instanceID = "instance-policy-publication"
	const authoredID = "semantic:orders"
	repo.semanticRegistryReader = func(_ context.Context, _ pgx.Tx, instance string) (access.SemanticRegistryContext, error) {
		return access.SemanticRegistryContext{
			Control: access.AuthorizationControlRevision{InstanceID: instance, ProjectID: "project:publication", Revision: 1},
			Registry: access.SemanticAttributeRegistrySnapshot{
				State:       access.SemanticAttributeRegistryState{Profile: semanticvalue.Profile, Revision: 1, Digest: "sha256:" + strings.Repeat("1", 64)},
				Definitions: []access.SemanticAttributeDefinition{{ID: "definition:region", Name: "region", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar, Profile: semanticvalue.Profile, DefinitionVersion: 1, Enabled: true}},
			},
		}, nil
	}
	if _, err := repo.Activate(ctx, candidate(instanceID, "bundle-policy", "", resource(authoredID, projectgraph.KindSemanticModel))); err != nil {
		t.Fatal(err)
	}
	baselineInput := semanticPublicationInput(instanceID, authoredID, "1.0.0", []string{"emea"})
	baselineInput.PolicyContext.ExpectedRegistry = publicationRegistryReference(instanceID)
	baseline, err := repo.PublishContract(ctx, baselineInput)
	if err != nil {
		t.Fatal(err)
	}
	widened := semanticPublicationInput(instanceID, authoredID, "1.1.0", []string{"emea", "amer"})
	widened.PolicyContext = existingPolicyContext(baseline)
	widened.PolicyContext.ExpectedRegistry = publicationRegistryReference(instanceID)
	publication, err := repo.PublishContract(ctx, widened)
	if err != nil {
		t.Fatal(err)
	}
	if publication.Validation.PolicyEvidence == nil || publication.Validation.PolicyEvidence.Version != identityledger.RegistryPolicyEvidenceVersion || publication.Validation.PolicyEvidence.RegistryTypes == nil {
		t.Fatalf("typed registry evidence = %#v", publication.Validation.PolicyEvidence)
	}
	if len(publication.Validation.PolicyEvidence.RegistryTypes.Definitions) != 1 || publication.Validation.PolicyEvidence.RegistryTypes.Definitions[0].Name != "region" {
		t.Fatalf("retained typed definitions = %#v", publication.Validation.PolicyEvidence.RegistryTypes.Definitions)
	}
	decision, err := publication.PolicyDecision()
	if err != nil {
		t.Fatal(err)
	}
	if !decision.ApprovalRequired || decision.ApprovalState != identityledger.PolicyApprovalRequired || decision.Classification.SecurityImpact != contractversion.SecurityWidening {
		t.Fatalf("security widening approval evidence = %#v", decision)
	}
	// Exact replay is sealed by retained v3 evidence. A changed live reader must
	// not reclassify or rewrite the first publication.
	originalDigest := publication.Validation.PolicyEvidence.EvidenceDigest
	repo.semanticRegistryReader = func(_ context.Context, _ pgx.Tx, instance string) (access.SemanticRegistryContext, error) {
		return access.SemanticRegistryContext{
			Control: access.AuthorizationControlRevision{InstanceID: instance, ProjectID: "project:publication", Revision: 2},
			Registry: access.SemanticAttributeRegistrySnapshot{
				State:       access.SemanticAttributeRegistryState{Profile: semanticvalue.Profile, Revision: 2, Digest: "sha256:" + strings.Repeat("2", 64)},
				Definitions: []access.SemanticAttributeDefinition{{ID: "definition:region", Name: "region", Type: semanticvalue.TypeBoolean, Shape: access.SemanticAttributeScalar, Profile: semanticvalue.Profile, DefinitionVersion: 2, Enabled: false}},
			},
		}, nil
	}
	replayed, err := repo.PublishContract(ctx, widened)
	if err != nil {
		t.Fatalf("typed exact replay after registry mutation: %v", err)
	}
	if replayed.Validation.PolicyEvidence.EvidenceDigest != originalDigest || !identityledger.EqualContractPublicationContent(publication, replayed) {
		t.Fatalf("typed exact replay changed retained evidence: first=%#v replay=%#v", publication, replayed)
	}
}

func TestProtectedPublicationRequiresTrustedRegistryReference(t *testing.T) {
	repo, _ := newLedgerDatabase(t)
	ctx := t.Context()
	const instanceID = "instance-policy-required"
	const authoredID = "semantic:orders"
	if _, err := repo.Activate(ctx, candidate(instanceID, "bundle-policy", "", resource(authoredID, projectgraph.KindSemanticModel))); err != nil {
		t.Fatal(err)
	}
	input := semanticPublicationInput(instanceID, authoredID, "1.0.0", []string{"emea"})
	if _, err := repo.PublishContract(ctx, input); !errors.Is(err, identityledger.ErrPolicyEvidenceConflict) {
		t.Fatalf("missing trusted registry reader error = %v", err)
	}

	repo.semanticRegistryReader = func(_ context.Context, _ pgx.Tx, instance string) (access.SemanticRegistryContext, error) {
		return access.SemanticRegistryContext{
			Control: access.AuthorizationControlRevision{InstanceID: instance, ProjectID: "project:publication", Revision: 1},
			Registry: access.SemanticAttributeRegistrySnapshot{
				State:       access.SemanticAttributeRegistryState{Profile: semanticvalue.Profile, Revision: 1, Digest: "sha256:" + strings.Repeat("1", 64)},
				Definitions: []access.SemanticAttributeDefinition{{ID: "definition:region", Name: "region", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar, Profile: semanticvalue.Profile, DefinitionVersion: 1, Enabled: true}},
			},
		}, nil
	}
	input.PolicyContext.ExpectedRegistry = publicationRegistryReference(instanceID)
	input.PolicyContext.ExpectedRegistry.ProjectID = "project:other"
	if _, err := repo.PublishContract(ctx, input); !errors.Is(err, identityledger.ErrPolicyEvidenceConflict) {
		t.Fatalf("cross-project registry reference error = %v", err)
	}
}

func publicationRegistryReference(instanceID string) *identityledger.PolicyRegistryReference {
	return &identityledger.PolicyRegistryReference{
		InstanceID: instanceID, ProjectID: "project:publication", ControlRevision: 1,
		Profile: semanticvalue.Profile, Revision: 1, Digest: "sha256:" + strings.Repeat("1", 64),
	}
}

type publicationScanFixture struct{ validation string }

func (row publicationScanFixture) Scan(dest ...any) error {
	*dest[0].(*string) = "instance-scan"
	*dest[1].(*string) = "source:orders"
	*dest[2].(*string) = "source"
	*dest[3].(*string) = "1.0.0"
	*dest[4].(*string) = "1.0.0"
	*dest[5].(*string) = contractprojection.Profile
	*dest[6].(*[]byte) = []byte(`{"apiVersion":"leapview.dev/v1"}`)
	*dest[7].(*string) = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	*dest[8].(*time.Time) = time.Now()
	*dest[9].(*string) = row.validation
	return nil
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
		PolicyContext: &identityledger.PolicyContext{BaselineKind: identityledger.PolicyBaselineGenesis, ExpectedLifecycleSequence: 1},
		Validation: identityledger.ValidationEvidence{Version: 1, Checks: []identityledger.ValidationCheck{
			{Name: "generated-contract", Outcome: identityledger.ValidationPassed, Reference: "task generated:check"},
			{Name: "projection-tests", Outcome: identityledger.ValidationPassed, Reference: "go test ./internal/project/contractprojection"},
		}},
	}
}

func semanticPublicationInput(instance, authoredID, version string, allowedValues []string) identityledger.ContractPublicationInput {
	document := map[string]any{
		"apiVersion": "leapview.dev/v1", "kind": "SemanticModel",
		"metadata": map[string]any{"id": authoredID, "name": "orders"},
		"spec": map[string]any{
			"datasets":      map[string]any{"orders": map[string]any{"model": "orders", "requiredAccessGrants": []any{"region_access"}}},
			"accessGrants":  map[string]any{"region_access": map[string]any{"userAttribute": "region", "allowedValues": allowedValues}},
			"relationships": map[string]any{}, "dimensions": map[string]any{}, "filters": map[string]any{}, "metrics": map[string]any{},
		},
	}
	encoded, _ := json.Marshal(document)
	var authored projectcontracts.SemanticModel
	if err := json.Unmarshal(encoded, &authored); err != nil {
		panic(err)
	}
	projection, err := contractprojection.ProjectSemanticModel(authored, contractprojection.Contract{Version: version, Compatibility: "backward"})
	if err != nil {
		panic(err)
	}
	return identityledger.ContractPublicationInput{
		InstanceID: instance, Projection: projection,
		PolicyContext: &identityledger.PolicyContext{BaselineKind: identityledger.PolicyBaselineGenesis, ExpectedLifecycleSequence: 1},
		Validation:    identityledger.ValidationEvidence{Version: 1, Checks: []identityledger.ValidationCheck{{Name: "generated-contract", Outcome: identityledger.ValidationPassed, Reference: "test"}}},
	}
}

func existingPolicyContext(publication identityledger.ContractPublication) *identityledger.PolicyContext {
	return &identityledger.PolicyContext{
		BaselineKind:              identityledger.PolicyBaselineExisting,
		ExpectedLifecycleSequence: 1,
		Baseline: &identityledger.PolicyPublicationIdentity{
			InstanceID: publication.InstanceID, AuthoredID: publication.AuthoredID,
			ResourceKind: publication.ResourceKind, Version: publication.Version,
			VersionBaseline: publication.VersionBaseline, ProjectionProfile: publication.ProjectionProfile,
			Digest: publication.Digest,
		},
	}
}

func activeBundleFor(id string) string {
	if id == "source:replay" {
		return ""
	}
	return "bundle-source:replay"
}
