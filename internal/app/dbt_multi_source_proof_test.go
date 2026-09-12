//go:build duckdb_arrow

package app

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/analytics/query/planir"
	appruntimefactory "github.com/flidai/leapview/internal/app/runtimefactory"
	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/platform/cliapi"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	"github.com/flidai/leapview/internal/runtimehost"
	"github.com/flidai/leapview/internal/semanticvalue"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/flidai/leapview/internal/workload"
)

// TestDBTMultiSourceProjectClosure runs actual pinned dbt outside LeapView,
// then hands only two physical publications to the ordinary compiler/gates/
// materializer/leased semantic query path. PostgreSQL registry durability is
// separately covered by the deployment/postgres proof.
func TestDBTMultiSourceProjectClosure(t *testing.T) {
	dbt := os.Getenv("DBT_BIN")
	if dbt == "" {
		t.Skip("run task dbt:warehouse:proof with the pinned dbt toolchain")
	}
	if !filepath.IsAbs(dbt) {
		t.Fatal("DBT_BIN must be an absolute path")
	}
	example := filepath.Join("..", "..", "examples", "dbt-warehouse-boundary")
	producer := t.TempDir()
	if err := os.CopyFS(producer, os.DirFS(filepath.Join(example, "multi-source"))); err != nil {
		t.Fatal(err)
	}
	dbtRoot := filepath.Join(producer, "dbt")
	if err := os.MkdirAll(filepath.Join(producer, "published", "commerce"), 0o700); err != nil {
		t.Fatal(err)
	}
	var invocation string
	for _, command := range []string{"deps", "build"} {
		cmd := exec.CommandContext(t.Context(), dbt, "--no-use-colors", "--log-format", "json", command, "--profiles-dir", ".", "--project-dir", ".")
		cmd.Dir = dbtRoot
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("dbt %s: %v\n%s", command, err, out)
		}
		// Producer provenance comes only from CLI logs. The consumer never reads
		// or imports dbt's manifest and run-results artifacts.
		for _, line := range bytes.Split(out, []byte("\n")) {
			var event struct {
				Info struct {
					Invocation string `json:"invocation_id"`
					Message    string `json:"msg"`
				} `json:"info"`
			}
			if json.Unmarshal(line, &event) == nil && event.Info.Invocation != "" {
				if command == "build" {
					invocation = event.Info.Invocation
				}
				t.Logf("dbt %s: %s", command, event.Info.Message)
			}
		}
	}
	if invocation == "" {
		t.Fatal("dbt CLI logs omitted producer invocation provenance")
	}
	commit, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("producer-only provenance: repository=flidai/leapview commit=%s project=proof_consumer target=local invocation=%s manifest=target/manifest.json upstream=upstream_orders (local dbt package)", strings.TrimSpace(string(commit)), invocation)

	// Handoff one dbt-produced Parquet file. No dbt metadata crosses this
	// boundary.
	commerce := t.TempDir()
	orders, err := os.ReadFile(filepath.Join(producer, "published", "commerce", "fct_orders.parquet"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commerce, "fct_orders.parquet"), orders, 0o600); err != nil {
		t.Fatal(err)
	}

	// Independently publish CRM data through a separate SQL producer. It has no
	// dependency on the dbt package or the commerce publication.
	directory := t.TempDir()
	directorySQL, err := os.ReadFile(filepath.Join(producer, "customer-directory.sql"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(t.Context(), fmt.Sprintf("COPY (%s) TO '%s' (FORMAT PARQUET)", directorySQL, strings.ReplaceAll(filepath.Join(directory, "dim_customers.parquet"), "'", "''"))); err != nil {
		t.Fatal(err)
	}

	// Compile only the rootless consumer source tree, overlaying the ordinary
	// second Connection and Source. The producer tree is intentionally absent.
	consumer := t.TempDir()
	if err := os.CopyFS(consumer, os.DirFS(filepath.Join(example, "leapview"))); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"connections/directory.yaml", "sources/dim_customers.yaml"} {
		content, err := os.ReadFile(filepath.Join(producer, "consumer-overlay", relative))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(consumer, relative), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	semanticPath := filepath.Join(consumer, "semantic-models", "sales.yaml")
	originalSemantic, err := os.ReadFile(semanticPath)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := projectcompiler.Compile(consumer)
	if err != nil {
		t.Fatal(err)
	}
	manifest := artifact.Manifest()
	if len(manifest.Connections) != 2 || len(manifest.Sources) != 2 {
		t.Fatalf("rootless consumer graph has %d connections and %d sources, want 2/2", len(manifest.Connections), len(manifest.Sources))
	}
	for _, resource := range artifact.Graph().Resources() {
		if resource.Kind == projectgraph.KindProjectNamespace {
			t.Fatalf("producer/control-plane Project escaped rootless graph: %#v", resource)
		}
	}

	// Package-qualified, foreign-project, and unknown live references cannot be
	// resolved against this consumer's authored Model namespace.
	for _, reference := range []string{"upstream_orders.order_lines", "proof_consumer/fct_orders", "missing_model"} {
		mutated := bytes.Replace(originalSemantic, []byte("model: orders"), []byte("model: "+reference), 1)
		if bytes.Equal(mutated, originalSemantic) {
			t.Fatalf("semantic fixture has no orders Model binding to mutate for %q", reference)
		}
		if err := os.WriteFile(semanticPath, mutated, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := projectcompiler.Compile(consumer); err == nil {
			t.Fatalf("live upstream/foreign/unknown reference %q resolved", reference)
		}
		if err := os.WriteFile(semanticPath, originalSemantic, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Source references close over the authored LeapView graph. An absent
	// physical Source and an ambiguous authored Source name both fail before
	// delivery; neither may fall back to dbt's relation or package namespace.
	ordersPath := filepath.Join(consumer, "models", "orders.yaml")
	originalOrders, err := os.ReadFile(ordersPath)
	if err != nil {
		t.Fatal(err)
	}
	unmappedOrders := bytes.Replace(originalOrders, []byte(`source."warehouse.fct_orders"`), []byte(`source."warehouse.missing"`), 1)
	if bytes.Equal(unmappedOrders, originalOrders) {
		t.Fatal("orders fixture has no Source binding to mutate")
	}
	if err := os.WriteFile(ordersPath, unmappedOrders, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := projectcompiler.Compile(consumer); err == nil || !strings.Contains(err.Error(), "warehouse.missing") {
		t.Fatalf("unmapped Source did not fail closed: %v", err)
	}
	if err := os.WriteFile(ordersPath, originalOrders, 0o600); err != nil {
		t.Fatal(err)
	}
	directorySource, err := os.ReadFile(filepath.Join(consumer, "sources", "dim_customers.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	ambiguousSource := bytes.Replace(directorySource, []byte("id: source:warehouse.dim_customers"), []byte("id: source:ambiguous.dim_customers"), 1)
	if bytes.Equal(ambiguousSource, directorySource) {
		t.Fatal("directory Source fixture has no stable ID to mutate")
	}
	ambiguousPath := filepath.Join(consumer, "sources", "ambiguous.yaml")
	if err := os.WriteFile(ambiguousPath, ambiguousSource, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := projectcompiler.Compile(consumer); err == nil || !strings.Contains(err.Error(), "duplicate Source") {
		t.Fatalf("ambiguous Source name did not fail closed: %v", err)
	}
	if err := os.Remove(ambiguousPath); err != nil {
		t.Fatal(err)
	}

	profilePath := filepath.Join(t.TempDir(), "issuer.json")
	validateID := func(value string) error { return projectgraph.ResourceID(value).Validate() }
	authority, err := cliapi.NewProfileStore(profilePath).ResolveProjectAuthority("", validateID)
	if err != nil {
		t.Fatal(err)
	}
	projectID := projectgraph.ResourceID(authority.ProjectUID)
	restarted, err := cliapi.NewProfileStore(profilePath).ResolveProjectAuthority("", validateID)
	if err != nil || restarted != authority {
		t.Fatal("ProjectUID changed after issuer restart")
	}
	targets := []string{"lvinst_0123456789abcdefghijklmnopqrstuv", "lvinst_1123456789abcdefghijklmnopqrstuv"}
	for _, forbidden := range []string{
		authority.ProjectUID, authority.IssuerID, targets[0], targets[1], commerce, directory,
		invocation, strings.TrimSpace(string(commit)), "flidai/leapview", "manifest.json",
		"run_results.json", "proof_consumer", "upstream_orders", "resourceUid",
	} {
		if bytes.Contains(artifact.Canonical(), []byte(forbidden)) {
			t.Fatalf("producer/target authority leaked into portable artifact: %q", forbidden)
		}
	}

	admission := newTestExactExtensionAdmission(t, "ducklake")
	var plans []deployment.DeliveryPlanRequest
	for index, environment := range []string{"dev", "prod"} {
		t.Run(environment, func(t *testing.T) {
			generation := fmt.Sprintf("00000000-0000-4000-8000-%012d", index+1)
			target := targets[index]
			candidate := "candidate-" + environment
			identity := projectgraph.ServingIdentity{ProjectID: projectID, Environment: environment, GenerationID: generation}
			boundDirectory := directory
			if environment == "prod" {
				boundDirectory = t.TempDir()
				if _, err := db.ExecContext(t.Context(), fmt.Sprintf("COPY (%s) TO '%s' (FORMAT PARQUET)", strings.ReplaceAll(string(directorySQL), "'north'", "'west'"), strings.ReplaceAll(filepath.Join(boundDirectory, "dim_customers.parquet"), "'", "''"))); err != nil {
					t.Fatal(err)
				}
			}
			directoryBytes, err := os.ReadFile(filepath.Join(boundDirectory, "dim_customers.parquet"))
			if err != nil {
				t.Fatal(err)
			}
			pins := []release.ManagedDataPin{
				{ConnectionID: "connection:warehouse", RevisionID: fmt.Sprintf("sha256:%x", sha256.Sum256(orders))},
				{ConnectionID: "connection:directory", RevisionID: fmt.Sprintf("sha256:%x", sha256.Sum256(directoryBytes))},
			}
			dataRevision, err := release.CandidateSourcesDataRevision(artifact.Digest(), pins)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := appruntimefactory.CandidatePlanRequest(deployment.DeliveryCandidateBuildInput{
				ProjectID: projectID, OwnerID: "publisher", ArtifactDigest: artifact.Digest(),
				Candidate: deployment.Candidate{ID: candidate, TargetID: target, Scope: deployment.CandidateScope{ProjectID: projectID, Environment: environment}},
			}, release.CandidateArtifactSet{
				Artifact:   release.ProjectArtifactProvenance{SourceDigest: artifact.Digest(), ProjectDigest: artifact.Digest(), CompilerVersion: projectartifact.CompilerVersion, SchemaVersion: projectartifact.Version},
				Generation: release.CandidateGenerationArtifact{Identity: identity, DataMode: release.GenerationDataRefreshSources, DataRevision: dataRevision, ManagedDataPins: pins, Deterministic: true},
				Compiler:   release.CandidateCompilerEvidence{Graph: artifact.Graph(), Manifest: manifest, Artifact: artifact},
			}, "runtime:v1", time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC))
			if err != nil {
				t.Fatal(err)
			}
			if plan.ProjectID != projectID.String() || plan.TargetID != target || plan.Environment != environment || candidate == generation || plan.ID == candidate {
				t.Fatal("delivery identities collapsed")
			}
			plans = append(plans, plan)

			// Bind the exact compiled semantic model to this target's two physical
			// roots. Each environment gets its own runtime generation and target.
			// Manifest returns a detached copy; keep each target's bound model
			// independent so a failed replacement cannot mutate a live runtime.
			targetManifest := artifact.Manifest()
			model := targetManifest.SemanticModels["semantic-model:warehouse_sales"]
			if model == nil {
				t.Fatal("compiled warehouse_sales semantic model is missing")
			}
			// Exercise the already-qualified ADR-0017 runtime boundary over this
			// exact dbt/CRM mapping. Portable policy lowering is covered by the
			// semantic-access compiler suite; this proof owns only the composition.
			model.AccessPolicy = dbtProofAccessPolicy(t)
			if !semanticquery.ModelRequiresSemanticAccess(model) {
				t.Fatal("dbt multi-source qualification model does not require semantic access")
			}
			for name, connection := range model.Connections {
				switch name {
				case "warehouse":
					connection.Root = commerce
				case "directory":
					connection.Root = boundDirectory
				default:
					t.Fatalf("unexpected connection %q", name)
				}
				model.Connections[name] = connection
			}
			qualificationManifest := artifact.Manifest()
			qualificationModel := qualificationManifest.SemanticModels["semantic-model:warehouse_sales"]
			if qualificationModel == nil {
				t.Fatal("qualification semantic model is missing")
			}
			id := servingstate.ID(generation)
			graph := artifact.Graph()
			factory := &warehouseBoundaryFactory{admission: admission, specs: map[servingstate.ID]warehouseBoundarySpec{id: {root: t.TempDir(), compiled: model, graph: &graph, targetID: target}}}
			repo := &warehouseBoundaryRepo{
				states:    map[servingstate.ID]servingstate.State{id: {ID: id, ProjectID: projectID, Environment: servingstate.Environment(environment), Status: servingstate.StatusValidated, Digest: artifact.Digest()}},
				artifacts: map[servingstate.ID]servingstate.Artifact{id: {ID: "artifact-" + generation, ServingStateID: id, Digest: artifact.Digest()}},
			}
			host := runtimehost.NewRegistryWithFactory(runtimehost.RegistryOptions{Repo: repo, ProjectID: projectID, Environment: servingstate.Environment(environment), Factory: factory, Authorization: warehouseBoundaryAuthorization{}})
			defer host.Close()
			prepared, err := host.PrepareServingState(t.Context(), generation)
			if err != nil {
				t.Fatal(err)
			}
			if lease, err := host.Acquire(t.Context()); err == nil {
				lease.Release()
				t.Fatal("unactivated candidate became serving authority")
			}
			runtime := factory.runtime(id)
			if len(runtime.SourceObservations()) != 2 {
				t.Fatalf("one physical producer was dropped: %v", runtime.SourceObservations())
			}
			if err := qualifyWarehouseBoundary(t.Context(), runtime, qualificationModel, id); err != nil {
				t.Fatal(err)
			}
			if err := host.ActivatePrepared(prepared, func() error { repo.active = id; return nil }); err != nil {
				t.Fatal(err)
			}
			lease, err := host.Acquire(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Release()
			if lease.Identity() != identity {
				t.Fatalf("query lease identity = %#v, want %#v", lease.Identity(), identity)
			}
			queryLease, err := runtime.controller.Acquire(t.Context(), workload.Request{Class: workload.Interactive, PrincipalID: "reader", Operation: "semantic.query", EstimatedMemoryBytes: 1})
			if err != nil {
				t.Fatal(err)
			}
			defer queryLease.Release()
			query := dataquery.Query{ProjectID: projectID, PrincipalID: "reader", ModelID: "warehouse", Kind: dataquery.KindSemanticAggregate, Target: "orders", Fields: []dataquery.Field{{Field: "customer_region"}}, Metrics: []dataquery.Field{{Field: "revenue"}}}
			if _, err := runtime.ExecuteDataQuery(queryLease.Context(), query); err == nil || !strings.Contains(err.Error(), "request-bound consumer") {
				t.Fatalf("protected dbt output admitted an alternate execution path: %v", err)
			}
			planner, ok := runtime.Planner("warehouse")
			if !ok {
				t.Fatal("activation-owned semantic planner is unavailable")
			}
			if !planner.CompiledModel().MatchesModel(model) {
				t.Fatalf("activation planner/model mismatch: planner=%s model=%s", planner.CompiledModel().SourceFingerprint(), semanticquery.SemanticModelFingerprint(model))
			}
			snapshot, semanticAuthority := dbtProofSemanticAuthority(t, target, "reader", "sales")
			consumerConfig := semanticquery.SemanticAccessConsumerConfig{
				InstanceID: target, ProjectID: projectID.String(), Environment: environment,
				ModelID: "warehouse", Generation: generation, PrincipalID: "reader",
				Authority: func() (semanticquery.SemanticAccessAttributeSnapshot, semanticquery.SemanticAccessAuthority, error) {
					return snapshot, semanticAuthority, nil
				},
			}
			semanticConsumer, err := semanticquery.NewSemanticAccessConsumer(planner, consumerConfig)
			if err != nil {
				t.Fatal(err)
			}
			semanticPlan, err := semanticConsumer.Planner().Plan(semanticquery.Request{
				Dataset: "orders", Dimensions: []semanticquery.Field{{Field: "customer_region"}}, Metrics: []semanticquery.Field{{Field: "revenue"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			assertDBTProofSecurityBarriers(t, semanticPlan.IR, "orders", "customers")
			if err := semanticConsumer.ValidatePlan(semanticPlan); err != nil {
				t.Fatalf("exact semantic-access plan was not admitted: %v", err)
			}
			if !semanticConsumer.Planner().CompiledModel().MatchesModel(model) {
				t.Fatal("semantic consumer changed the activation model identity")
			}
			binding := semanticquery.SemanticAccessConsumerBinding{
				InstanceID: target, ProjectID: projectID.String(), Environment: environment,
				Generation: generation, ModelID: "warehouse", PrincipalID: "reader",
			}
			boundContext := semanticquery.WithSemanticAccessConsumer(queryLease.Context(), semanticConsumer, binding)
			result, err := runtime.ExecuteDataQuery(boundContext, query)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Rows) != 2 {
				t.Fatalf("cross-producer regional query = %#v", result.Rows)
			}
			region := "north"
			if environment == "prod" {
				region = "west"
			}
			want := map[string]string{region: "62.24", "south": "75"}
			for _, row := range result.Rows {
				key := fmt.Sprint(row["customer_region"])
				if expected, ok := want[key]; !ok || fmt.Sprint(row["revenue"]) != expected {
					t.Fatalf("target-bound regional result = %#v, want %v", result.Rows, want)
				}
				delete(want, key)
			}
			if len(want) != 0 {
				t.Fatalf("missing regional result: %v", want)
			}
			t.Logf("%s/%s: %v", environment, generation, result.Rows)

			// A missing independent producer blocks a replacement candidate while
			// the already active generation and its lease keep serving.
			badID := servingstate.ID(fmt.Sprintf("00000000-0000-4000-8000-%012d", index+20))
			badManifest := artifact.Manifest()
			badModel := badManifest.SemanticModels["semantic-model:warehouse_sales"]
			for name, connection := range badModel.Connections {
				connection.Root = commerce
				if name == "directory" {
					connection.Root = t.TempDir()
				}
				badModel.Connections[name] = connection
			}
			repo.states[badID] = servingstate.State{ID: badID, ProjectID: projectID, Environment: servingstate.Environment(environment), Status: servingstate.StatusValidated, Digest: artifact.Digest()}
			repo.artifacts[badID] = servingstate.Artifact{ID: "artifact-" + string(badID), ServingStateID: badID, Digest: artifact.Digest()}
			factory.specs[badID] = warehouseBoundarySpec{root: t.TempDir(), compiled: badModel, graph: &graph, targetID: target}
			if failed, err := host.PrepareServingState(t.Context(), string(badID)); err == nil {
				_ = failed.Close()
				t.Fatal("partial publication became a candidate")
			}
			assertWarehouseBoundaryGeneration(t, host, id)
			if again, err := runtime.ExecuteDataQuery(boundContext, query); err != nil || len(again.Rows) != 2 {
				t.Fatalf("failed candidate disrupted retained query: %v", err)
			}
			query.ProjectID = "project:foreign"
			if _, err := runtime.ExecuteDataQuery(boundContext, query); err == nil {
				t.Fatal("foreign Project consumed semantic state")
			}
			deniedSnapshot, deniedAuthority := dbtProofSemanticAuthority(t, target, "reader", "finance")
			deniedConfig := consumerConfig
			deniedConfig.Authority = func() (semanticquery.SemanticAccessAttributeSnapshot, semanticquery.SemanticAccessAuthority, error) {
				return deniedSnapshot, deniedAuthority, nil
			}
			deniedConsumer, err := semanticquery.NewSemanticAccessConsumer(planner, deniedConfig)
			if err == nil {
				_, err = deniedConsumer.Planner().Plan(semanticquery.Request{Dataset: "orders", Metrics: []semanticquery.Field{{Field: "revenue"}}})
			}
			if err == nil || !strings.Contains(err.Error(), "semantic access denied") {
				t.Fatalf("unauthorized dbt-backed semantic query did not fail closed: %v", err)
			}
		})
	}
	if len(plans) != 2 || plans[0].SourceDigest != plans[1].SourceDigest || plans[0].ID == plans[1].ID || plans[0].Execution.ConfigDigest == plans[1].Execution.ConfigDigest {
		t.Fatal("promotion did not rebind the same portable source to distinct targets")
	}
}

func dbtProofAccessPolicy(t *testing.T) semanticmodel.SemanticAccessPolicy {
	t.Helper()
	literal, err := semanticmodel.NewSemanticAccessLiteral("sales")
	if err != nil {
		t.Fatal(err)
	}
	return semanticmodel.SemanticAccessPolicy{
		AccessGrants: map[string]semanticmodel.SemanticAccessGrantSpec{
			"canViewSales": {UserAttribute: "department", AllowedValues: []semanticmodel.SemanticAccessLiteral{literal}},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetAccessSpec{
			"orders":    {RequiredAccessGrants: []string{"canViewSales"}},
			"customers": {RequiredAccessGrants: []string{"canViewSales"}},
		},
		Dimensions: map[string][]string{"customer_region": {"canViewSales"}},
		Metrics:    map[string][]string{"revenue": {"canViewSales"}},
	}
}

func dbtProofSemanticAuthority(t *testing.T, instanceID, principalID, department string) (semanticquery.SemanticAccessAttributeSnapshot, semanticquery.SemanticAccessAuthority) {
	t.Helper()
	definition := access.SemanticAttributeDefinition{
		ID: "definition-department", Name: "department", Type: semanticvalue.TypeString,
		Shape: access.SemanticAttributeScalar, Profile: semanticvalue.Profile, DefinitionVersion: 1,
		Metadata:       access.SemanticAttributeMetadata{Owner: access.SemanticAttributeOwner{Kind: access.SemanticAttributeOwnerInstance}},
		LifecycleState: access.SemanticAttributeActive, Enabled: true,
	}
	registry := access.SemanticAttributeRegistrySnapshot{Definitions: []access.SemanticAttributeDefinition{definition}}
	registry.State = access.SemanticAttributeRegistryState{Profile: semanticvalue.Profile, Revision: 1}
	var err error
	registry.State.Digest, err = access.SemanticAttributeRegistryDigest(registry.State.Profile, registry.Definitions)
	if err != nil {
		t.Fatal(err)
	}
	values, valueDigest, err := access.CanonicalSemanticAttributeValues(definition, department)
	if err != nil {
		t.Fatal(err)
	}
	attribute := access.EffectiveSemanticAttribute{
		DefinitionID: definition.ID, DefinitionName: definition.Name, DefinitionVersion: definition.DefinitionVersion,
		Type: definition.Type, Shape: definition.Shape, CanonicalValues: values, ValueDigest: valueDigest, Source: "direct",
	}
	principal := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principalID}
	assignment := access.SemanticAttributeAssignment{
		ID: "assignment-department", DefinitionID: definition.ID, DefinitionName: definition.Name,
		DefinitionVersion: definition.DefinitionVersion, Type: definition.Type, Shape: definition.Shape,
		Subject: principal, CanonicalValues: append([]string(nil), values...), ValueDigest: valueDigest, AssignmentVersion: 1,
	}
	control := access.SemanticAttributeControlSnapshot{Assignments: []access.SemanticAttributeAssignment{assignment}}
	control.State = access.SemanticAttributeControlState{Profile: semanticvalue.Profile, Revision: 1}
	control.State.Digest, err = access.SemanticAttributeControlDigest(control.Assignments, control.Mappings)
	if err != nil {
		t.Fatal(err)
	}
	resolved := access.SemanticAttributeResolution{
		Subject: principal, Subjects: []access.SubjectRef{principal}, Registry: registry, Control: control,
		Attributes: []access.EffectiveSemanticAttribute{attribute}, ObservedAt: time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC),
	}
	snapshot, authority, err := semanticquery.SemanticAccessResolutionSnapshot(instanceID, principalID, resolved)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot, authority
}

func assertDBTProofSecurityBarriers(t *testing.T, graph *planir.Graph, datasets ...string) {
	t.Helper()
	want := make(map[string]bool, len(datasets))
	for _, dataset := range datasets {
		want[dataset] = true
	}
	for _, node := range graph.Nodes {
		var barrier planir.SecurityBarrier
		switch value := node.(type) {
		case planir.SecurityBarrier:
			barrier = value
		case *planir.SecurityBarrier:
			barrier = *value
		default:
			continue
		}
		if barrier.PolicyDigest == "" || barrier.DecisionDigest == "" {
			t.Fatalf("semantic access barrier lacks authority evidence: %#v", barrier)
		}
		delete(want, barrier.Dataset)
	}
	if len(want) != 0 {
		t.Fatalf("dbt multi-source plan lacks semantic access barriers for %v", want)
	}
}
