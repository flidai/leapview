package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	"github.com/flidai/leapview/internal/platform/cliapi"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
	"github.com/stretchr/testify/require"
)

func TestCandidateCheckpointStoreRoundTripsExactNonSecretIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "authoring.json")
	store := NewCandidateCheckpointStore(path)
	sourceRoot := t.TempDir()
	checkpoint := candidateCheckpoint(sourceRoot)

	if err := store.Save(checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveObjectIdentity("candidate", checkpoint.CandidateID, DeliveryObjectCheckpoint{ProjectID: checkpoint.ProjectID, TargetOrigin: checkpoint.TargetOrigin}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	require.NoError(t, err)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
	loaded, err := store.Load(sourceRoot, checkpoint.TargetOrigin)
	require.NoError(t, err)
	if loaded != checkpoint {
		t.Fatalf("loaded = %#v, want %#v", loaded, checkpoint)
	}
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	for _, forbidden := range []string{"secret-token", `"token"`, `"password"`} {
		if strings.Contains(strings.ToLower(string(content)), forbidden) {
			t.Fatalf("checkpoint persisted forbidden secret material: %s", content)
		}
	}
}

func TestCandidateCheckpointStoreRefusesReadModifyWriteWhileAnotherProcessOwnsLock(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "authoring.json")
	store := NewCandidateCheckpointStore(path)
	sourceRoot := t.TempDir()
	existing := candidateCheckpoint(sourceRoot)
	if err := store.Save(existing); err != nil {
		t.Fatal(err)
	}
	lock, err := instancelock.AcquireNamed(directory, ".authoring.json.lock")
	require.NoError(t, err)
	defer lock.Release()

	concurrent := existing
	concurrent.CandidateKey = "github:pull/194"
	concurrent.CandidateID = "cand_194"
	if err := store.Save(concurrent); err == nil {
		t.Fatal("Save succeeded while another process owned the checkpoint lock")
	}
	loaded, err := store.LoadCandidate(
		existing.SourceRoot,
		existing.TargetOrigin,
		existing.CandidateKey,
	)
	if err != nil || loaded != existing {
		t.Fatalf("existing checkpoint was corrupted: %#v, %v", loaded, err)
	}
	if _, err := store.LoadCandidate(
		concurrent.SourceRoot,
		concurrent.TargetOrigin,
		concurrent.CandidateKey,
	); !errors.Is(err, ErrCandidateCheckpointNotFound) {
		t.Fatalf("concurrent checkpoint unexpectedly persisted: %v", err)
	}
}

func TestPlanCheckpointBuildOperationBindingIsStableAndRotatable(t *testing.T) {
	store := NewCandidateCheckpointStore(filepath.Join(t.TempDir(), "authoring.json"))
	plan := DeliveryPlanCheckpoint{PlanID: "plan-1", ProjectID: "project-1", PlanDigest: "sha256:plan"}
	if err := store.SavePlan(plan); err != nil {
		t.Fatal(err)
	}
	first, err := store.BindPlanBuildIdempotencyKey(plan.PlanID, "", "build-1")
	if err != nil || first != "build-1" {
		t.Fatalf("first binding = %q, err = %v", first, err)
	}
	stable, err := store.BindPlanBuildIdempotencyKey(plan.PlanID, "", "build-other")
	if err != nil || stable != first {
		t.Fatalf("stable binding = %q, err = %v", stable, err)
	}
	if err := store.SavePlan(plan); err != nil {
		t.Fatal(err)
	}
	preserved, err := store.LoadPlan(plan.PlanID)
	if err != nil || preserved.BuildIdempotencyKey != first {
		t.Fatalf("preserved plan = %#v, err = %v", preserved, err)
	}
	rotated, err := store.BindPlanBuildIdempotencyKey(plan.PlanID, first, "build-2")
	if err != nil || rotated != "build-2" {
		t.Fatalf("rotated binding = %q, err = %v", rotated, err)
	}
	concurrent, err := store.BindPlanBuildIdempotencyKey(plan.PlanID, first, "build-stale")
	if err != nil || concurrent != rotated {
		t.Fatalf("stale rotation = %q, err = %v", concurrent, err)
	}
}

func TestCandidateCheckpointStoreIsolatesStableAuthoringKeys(t *testing.T) {
	store := NewCandidateCheckpointStore(
		filepath.Join(t.TempDir(), "authoring.json"),
	)
	sourceRoot := t.TempDir()
	first := candidateCheckpoint(sourceRoot)
	first.CandidateKey = "github:pull/41"
	first.CandidateID = "cand_41"
	second := candidateCheckpoint(sourceRoot)
	second.CandidateKey = "github:pull/42"
	second.CandidateID = "cand_42"
	if err := store.Save(first); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(second); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadCandidate(
		sourceRoot,
		first.TargetOrigin,
		first.CandidateKey,
	)
	if err != nil || loaded != first {
		t.Fatalf("LoadCandidate() = %#v, %v", loaded, err)
	}
	loaded, err = store.LoadCandidate(
		sourceRoot,
		second.TargetOrigin,
		second.CandidateKey,
	)
	if err != nil || loaded != second {
		t.Fatalf("LoadCandidate() = %#v, %v", loaded, err)
	}
}

func TestCandidateCheckpointStoreFailsClosedForUnknownOrSecretFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "authoring.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"candidates":{},"token":"secret-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewCandidateCheckpointStore(path)
	if _, err := store.Load("dashboards", "https://target.example"); err == nil {
		t.Fatal("Load() accepted a secret-bearing unknown field")
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"candidates":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("dashboards", "https://target.example"); !errors.Is(err, ErrCandidateCheckpointNotFound) {
		t.Fatalf("Load() error = %v, want ErrCandidateCheckpointNotFound", err)
	}
	if err := os.WriteFile(
		path,
		[]byte(`{"version":1,"candidates":{}} {"version":1,"candidates":{}}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(
		"dashboards",
		"https://target.example",
	); err == nil {
		t.Fatal("Load() accepted trailing JSON content")
	}
}

func TestCandidateCheckpointStoreMigratesLegacyProjectPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "authoring.json")
	checkpoint := candidateCheckpoint(t.TempDir())
	legacy := map[string]any{
		"projectPath":       checkpoint.SourceRoot,
		"targetOrigin":      checkpoint.TargetOrigin,
		"targetId":          checkpoint.TargetID,
		"environment":       checkpoint.Environment,
		"projectId":         checkpoint.ProjectID,
		"candidateId":       checkpoint.CandidateID,
		"candidateKey":      checkpoint.CandidateKey,
		"candidateRevision": checkpoint.CandidateRevision,
		"artifactDigest":    checkpoint.ArtifactDigest,
		"provenanceDigest":  checkpoint.ProvenanceDigest,
	}
	content, err := json.Marshal(map[string]any{
		"version": candidateCheckpointDocumentVersion,
		"candidates": map[string]any{
			candidateCheckpointKey(checkpoint.SourceRoot, checkpoint.TargetOrigin, checkpoint.CandidateKey): legacy,
		},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, content, 0o600))

	store := NewCandidateCheckpointStore(path)
	loaded, err := store.Load(checkpoint.SourceRoot, checkpoint.TargetOrigin)
	require.NoError(t, err)
	require.Equal(t, checkpoint, loaded)
	require.NoError(t, store.Save(loaded))

	migrated, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(migrated), `"sourceRoot"`)
	require.NotContains(t, string(migrated), `"projectPath"`)
}

func TestPublishCommandUsesExactCheckpointWithoutReadingProjectSource(t *testing.T) {
	sourceRoot := filepath.Join(t.TempDir(), "missing-source-root")
	store := NewCandidateCheckpointStore(filepath.Join(t.TempDir(), "authoring.json"))
	checkpoint := candidateCheckpoint(sourceRoot)
	if err := store.Save(checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveObjectIdentity("candidate", checkpoint.CandidateID, DeliveryObjectCheckpoint{ProjectID: checkpoint.ProjectID, TargetOrigin: checkpoint.TargetOrigin}); err != nil {
		t.Fatal(err)
	}
	operations := &publishOperations{}
	command := PublishCommand(t.Context(), publishClient{}, store, operations)
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs([]string{checkpoint.CandidateID})

	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if operations.options.Checkpoint.CandidateID != checkpoint.CandidateID || operations.options.Checkpoint.ProjectID != checkpoint.ProjectID {
		t.Fatalf("checkpoint = %#v, want candidate/project identity from %#v", operations.options.Checkpoint, checkpoint)
	}
	if operations.options.Credentials.Target != checkpoint.TargetOrigin ||
		operations.options.Credentials.Token != "ephemeral-token" {
		t.Fatalf("credentials = %#v", operations.options.Credentials)
	}
}

func TestPublishCommandRequiresPriorDevCandidateForResolvedTarget(t *testing.T) {
	store := NewCandidateCheckpointStore(filepath.Join(t.TempDir(), "authoring.json"))
	command := PublishCommand(t.Context(), publishClient{}, store, &publishOperations{})
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs([]string{"missing-candidate"})

	err := command.Execute()
	if !errors.Is(err, ErrCandidateCheckpointNotFound) ||
		!strings.Contains(err.Error(), "resolve candidate checkpoint") {
		t.Fatalf("Execute() error = %v", err)
	}
}

type publishClient struct{}

func (publishClient) Resolve(context.Context, cliapi.Credentials) (cliapi.Credentials, error) {
	return cliapi.Credentials{Target: "https://target.example", Token: "ephemeral-token"}, nil
}

func (publishClient) Environment(context.Context, cliapi.Credentials, string) (string, error) {
	return "", nil
}

func (publishClient) Transport(context.Context, cliapi.Credentials) (apigenclient.Transport, error) {
	return nil, nil
}

type publishOperations struct {
	options PublishOptions
}

func (operations *publishOperations) Publish(
	_ context.Context,
	options PublishOptions,
	_ io.Writer,
) error {
	operations.options = options
	return nil
}

func candidateCheckpoint(sourceRoot string) CandidateCheckpoint {
	absolute, err := filepath.Abs(sourceRoot)
	if err != nil {
		panic(err)
	}
	return CandidateCheckpoint{
		SourceRoot: absolute, TargetOrigin: "https://target.example",
		TargetID: "target_1", Environment: "production", ProjectID: "finance",
		CandidateID: "cand_1", CandidateKey: "default", CandidateRevision: 7,
		ArtifactDigest:   "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ProvenanceDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
}
