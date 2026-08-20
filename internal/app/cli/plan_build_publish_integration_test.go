//go:build duckdb_arrow

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	accesscli "github.com/flidai/leapview/internal/access/cli"
	"github.com/flidai/leapview/internal/app"
	manageddatacli "github.com/flidai/leapview/internal/manageddata/cli"
	"github.com/flidai/leapview/internal/manageddata/localplan"
	"github.com/flidai/leapview/internal/platform/cliapi"
	projectcli "github.com/flidai/leapview/internal/project/cli"
	"github.com/spf13/cobra"
)

func TestPlanBuildPublishCommandsCompleteAgainstRealApplication(t *testing.T) {
	ctx := t.Context()
	home := t.TempDir()
	t.Cleanup(func() {
		if err := restoreWritableTree(home); err != nil {
			t.Errorf("restore writable test tree: %v", err)
		}
	})
	application, err := app.Build(ctx, serveTestConfig(t, home))
	if err != nil {
		t.Fatalf("build application: %v", err)
	}
	if err := application.Start(ctx); err != nil {
		t.Fatalf("start application: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := application.Shutdown(shutdownCtx); err != nil {
			t.Errorf("shutdown application: %v", err)
		}
	})
	target := httptest.NewServer(application.Handler())
	t.Cleanup(target.Close)

	assets, err := evaluationAssetsRoot()
	if err != nil {
		t.Fatalf("locate evaluation assets: %v", err)
	}
	projectPath := filepath.Join(assets, evaluationProjectRelativePath)
	dataPath := filepath.Join(assets, evaluationDataRelativePath)
	dataPlan, err := localplan.NewService(loadManagedDataPlanProject).Plan(ctx, localplan.Request{
		ProjectPath: projectPath,
		Connection:  evaluationConnection,
		From:        dataPath,
	})
	if err != nil {
		t.Fatalf("plan managed data: %v", err)
	}
	if err := manageddatacli.RunSync(ctx, manageddatacli.SyncRequest{
		ProjectPath: projectPath,
		ProjectID:   evaluationProjectID,
		Connection:  evaluationConnection,
		Root:        dataPlan.Root,
		Target:      target.URL,
		Token:       "dev",
		Plan:        dataPlan,
		Out:         &bytes.Buffer{},
		HTTPClient:  target.Client(),
	}); err != nil {
		t.Fatalf("stage managed data: %v", err)
	}

	const targetProfile = "integration"
	client := capabilityAPIClient{httpClient: target.Client(), authoring: deliveryIntegrationAuthoringResolver{
		selector: targetProfile,
		credential: accesscli.ResolvedCredential{
			Profile:     cliapi.TargetProfile{Origin: target.URL, InstanceID: "integration", Environment: "default", ProjectID: evaluationProjectID, CredentialAccount: "integration"},
			AccessToken: "dev",
		},
	}}
	checkpoints := projectcli.NewCandidateCheckpointStore(filepath.Join(home, "authoring.json"))
	planCommand := projectcli.DeliveryPlanCommand(ctx, projectDeliveryPlanOperations{
		client: client, remotes: projectDevRemoteFactory{client: client}, checkpoints: checkpoints,
	})
	planOutput := executeDeliveryCommand(t, planCommand, projectPath, "--target", targetProfile, "--format", "json")
	var plan projectcli.DeliveryPlanResult
	if err := json.Unmarshal(planOutput, &plan); err != nil {
		t.Fatalf("decode plan result: %v\n%s", err, planOutput)
	}
	if plan.PlanID == "" || plan.Status != "planned" {
		t.Fatalf("plan result = %#v", plan)
	}
	if plan.Evidence.AddedCount == 0 {
		t.Errorf("bootstrap plan reported no added resources: %#v", plan.Evidence)
	}
	// Replanning is an explicit new review operation. It must not resurrect an
	// older durable plan merely because source and target inputs are unchanged;
	// planner/compiler evidence may have changed between invocations.
	replanCommand := projectcli.DeliveryPlanCommand(ctx, projectDeliveryPlanOperations{
		client: client, remotes: projectDevRemoteFactory{client: client}, checkpoints: checkpoints,
	})
	replanOutput := executeDeliveryCommand(t, replanCommand, projectPath, "--target", targetProfile, "--format", "json")
	var replanned projectcli.DeliveryPlanResult
	if err := json.Unmarshal(replanOutput, &replanned); err != nil {
		t.Fatalf("decode replanned result: %v\n%s", err, replanOutput)
	}
	if replanned.PlanID == plan.PlanID || replanned.SourceDigest != plan.SourceDigest || replanned.BaseTargetRevision != plan.BaseTargetRevision {
		t.Fatalf("replanned identity = %#v, first = %#v", replanned, plan)
	}
	plan = replanned

	buildOperations := projectDeliveryBuildOperations{client: client, checkpoints: checkpoints}
	buildCommand := projectcli.DeliveryBuildCommand(ctx, buildOperations)
	buildOutput := executeDeliveryCommand(t, buildCommand, plan.PlanID, "--format", "json")
	var build projectcli.DeliveryBuildResult
	if err := json.Unmarshal(buildOutput, &build); err != nil {
		t.Fatalf("decode build result: %v\n%s", err, buildOutput)
	}
	if build.Status != "sealed" || build.CandidateID == "" || build.SealID == "" {
		t.Fatalf("build result = %#v", build)
	}
	candidateIdentity, err := checkpoints.LoadObjectIdentity("candidate", build.CandidateID)
	if err != nil {
		t.Fatalf("load candidate handoff: %v", err)
	}
	if candidateIdentity.TargetOrigin != target.URL || candidateIdentity.TargetSelector != targetProfile {
		t.Fatalf("candidate handoff lost target profile: %#v", candidateIdentity)
	}

	replayCommand := projectcli.DeliveryBuildCommand(ctx, buildOperations)
	replayOutput := executeDeliveryCommand(t, replayCommand, plan.PlanID, "--format", "json")
	var replay projectcli.DeliveryBuildResult
	if err := json.Unmarshal(replayOutput, &replay); err != nil {
		t.Fatalf("decode replayed build result: %v\n%s", err, replayOutput)
	}
	if replay.BuildID != build.BuildID || replay.CandidateID != build.CandidateID || replay.SealID != build.SealID {
		t.Fatalf("build replay changed sealed identity: first=%#v replay=%#v", build, replay)
	}

	publishCommand := projectcli.PublishCommand(ctx, client, checkpoints, projectPublishOperations{
		client: client, checkpoints: checkpoints,
	})
	publishOutput := executeDeliveryCommand(t, publishCommand, build.CandidateID, "--format", "json")
	var publication projectcli.PublishResult
	if err := json.Unmarshal(publishOutput, &publication); err != nil {
		t.Fatalf("decode publication result: %v\n%s", err, publishOutput)
	}
	if publication.Status != "committed" || publication.GenerationID == "" || publication.CandidateID != build.CandidateID || publication.PlanID != plan.PlanID || publication.TargetRevision != 1 {
		t.Fatalf("publication result = %#v", publication)
	}
}

type deliveryIntegrationAuthoringResolver struct {
	selector   string
	credential accesscli.ResolvedCredential
}

func (resolver deliveryIntegrationAuthoringResolver) Resolve(_ context.Context, selector string) (accesscli.ResolvedCredential, error) {
	if selector != resolver.selector {
		return accesscli.ResolvedCredential{}, fmt.Errorf("resolve unexpected target selector %q", selector)
	}
	return resolver.credential, nil
}

func executeDeliveryCommand(t *testing.T, command *cobra.Command, args ...string) []byte {
	t.Helper()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs(args)
	if err := command.Execute(); err != nil {
		t.Fatalf("execute %s: %v\n%s", command.Name(), err, output.String())
	}
	return output.Bytes()
}

func restoreWritableTree(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		mode := os.FileMode(0o600)
		if entry.IsDir() {
			mode = 0o700
		}
		return os.Chmod(path, mode)
	})
}
