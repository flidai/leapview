package module

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	refreshcomposition "github.com/flidai/leapview/internal/app/refreshpostgres"
	"github.com/flidai/leapview/internal/deployment"
	jobspostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	operationpostgres "github.com/flidai/leapview/internal/platform/operation/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
)

func TestPostgresLegacyRefreshOperationReplaysOnlyMatchingGeneration(t *testing.T) {
	db := modulePostgresTestDB(t)
	repository := refreshpostgres.New(db)
	jobsRepository := jobspostgres.New(db)
	queue := NewPostgresJobsAdapter(jobsRepository, repository)
	operations := operationpostgres.New(db)
	operationAuthority, err := refreshcomposition.NewPostgresOperationAuthorityAdapter(operations)
	if err != nil {
		t.Fatal(err)
	}
	persistence := &postgresRunPersistence{repository: repository, operations: operationAuthority}
	identity := projectgraph.ServingIdentity{ProjectID: "project-legacy-replay", Environment: "prod", GenerationID: "generation-legacy-replay"}
	plan, err := deployment.NewPipelinePlan(deployment.PipelinePlan{
		ID: "pipeline-plan-legacy-replay", PipelineID: "pipeline-legacy-replay", ProjectID: identity.ProjectID.String(), Environment: identity.Environment,
		SemanticModelID: "semantic-legacy-replay", ServingGenerationID: identity.GenerationID,
		ArtifactDigest: "sha256:" + strings.Repeat("a", 64), SelectionDigest: "sha256:" + strings.Repeat("b", 64), MaterializationScope: []string{"model-legacy-replay"}, InvocationSource: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	rootInput := refreshrun.RunInput{
		RunID: "run-legacy-replay", Identity: identity, SemanticModelID: "semantic-legacy-replay", PipelineID: "pipeline-legacy-replay", PipelinePlan: &plan,
		InvocationSource: "manual", PrincipalID: "principal:legacy-replay", EstimatedMemoryBytes: 1,
		TargetType: refreshrun.TargetRefreshPipeline, TargetID: "pipeline-legacy-replay", TriggerType: refreshrun.TriggerManual,
		JobKind: refreshrun.JobKindRefreshPipeline, PayloadJSON: `{}`,
	}
	input, err := toPostgresRunInput(rootInput)
	if err != nil {
		t.Fatal(err)
	}
	key := "legacy-refresh-key"
	requestDigest := "sha256:" + strings.Repeat("c", 64)
	legacyDigest := sha256.Sum256([]byte(identity.ProjectID.String() + "\x00" + identity.Environment))
	legacyScope := "refresh:" + hex.EncodeToString(legacyDigest[:])
	if err := repository.InTx(t.Context(), func(tx refreshpostgres.Tx) error {
		acquired, acquireErr := operations.AcquireTx(t.Context(), tx, operationpostgres.AcquireInput{
			Scope: legacyScope, OperationType: "refresh_pipeline", IdempotencyKey: key,
			RequestDigest: requestDigest, OwnerID: input.PrincipalID, Lease: time.Minute, Retention: 24 * time.Hour,
		})
		if acquireErr != nil {
			return acquireErr
		}
		input.OperationID = acquired.Operation.OperationID
		created, _, createErr := repository.CreateRunTreeTx(t.Context(), tx, input, nil, "", "", 0, func(hookCtx context.Context, hookTx refreshpostgres.Tx, created refreshpostgres.Run) (string, error) {
			return queue.EnqueueRefreshTx(hookCtx, hookTx, rootInput, created.RunID)
		}, nil)
		if createErr != nil {
			return createErr
		}
		outcome, marshalErr := json.Marshal(struct {
			RunID string `json:"runId"`
		}{RunID: created.RunID})
		if marshalErr != nil {
			return marshalErr
		}
		return operations.CompleteTx(t.Context(), tx, acquired.Lease, outcome)
	}); err != nil {
		t.Fatal(err)
	}

	replayed, children, found, err := persistence.LookupIdempotentRun(t.Context(), identity, "pipeline-legacy-replay", key, requestDigest)
	if err != nil || !found || replayed.ID != input.RunID || len(children) != 0 {
		t.Fatalf("legacy replay run=%#v children=%d found=%t err=%v", replayed, len(children), found, err)
	}
	foreignGeneration := identity
	foreignGeneration.GenerationID = "generation-other"
	if _, _, _, err := persistence.LookupIdempotentRun(t.Context(), foreignGeneration, "pipeline-legacy-replay", key, requestDigest); !errors.Is(err, refreshpostgres.ErrConflict) {
		t.Fatalf("legacy replay with incompatible generation error=%v, want conflict", err)
	}
	var runCount int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM refresh.run WHERE project_id=$1 AND environment=$2`, identity.ProjectID.String(), identity.Environment).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 1 {
		t.Fatalf("legacy replay created %d runs, want 1", runCount)
	}
}
