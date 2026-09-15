package localruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	instancelock "github.com/flidai/leapview/internal/platform/locking"
	"github.com/stretchr/testify/require"
)

const testAttachmentStaleAfter = 10 * time.Second

func TestTwoAttachmentsDetachWithoutStoppingUntilLastExit(t *testing.T) {
	controller, runner, now := startedLifecycleController(t)
	first, state, err := controller.Attach(t.Context())
	require.NoError(t, err)
	second, shared, err := controller.Attach(t.Context())
	require.NoError(t, err)
	require.Equal(t, state.Runtime.OwnerID, shared.Runtime.OwnerID)

	status, err := controller.Status(t.Context())
	require.NoError(t, err)
	require.Len(t, status.Attachments, 2)
	require.Equal(t, state.Checkout.CanonicalRoot, status.CheckoutRoot)

	result, err := controller.Detach(t.Context(), first)
	require.NoError(t, err)
	require.Equal(t, 1, result.RemainingAttachments)
	require.False(t, result.ServicesStopped)
	require.Zero(t, countCommands(runner.commands, " stop --timeout"))

	*now = now.Add(time.Second)
	result, err = controller.Detach(t.Context(), second)
	require.NoError(t, err)
	require.Zero(t, result.RemainingAttachments)
	require.True(t, result.ServicesStopped)
	require.Equal(t, 1, countCommands(runner.commands, " stop --timeout"))
}

func TestStopAndResetRefuseFreshAttachmentsWithoutDockerMutation(t *testing.T) {
	controller, runner, _ := startedLifecycleController(t)
	attachment, _, err := controller.Attach(t.Context())
	require.NoError(t, err)
	stopBefore := countCommands(runner.commands, " stop --timeout")
	downBefore := countCommands(runner.commands, " down --timeout")

	err = controller.Stop(t.Context())
	require.ErrorIs(t, err, ErrLiveAttachments)
	require.Contains(t, err.Error(), attachment.ID())
	require.Equal(t, stopBefore, countCommands(runner.commands, " stop --timeout"))
	require.Equal(t, downBefore, countCommands(runner.commands, " down --timeout"))

	_, err = controller.PlanReset(t.Context())
	require.ErrorIs(t, err, ErrLiveAttachments)
	require.Equal(t, stopBefore, countCommands(runner.commands, " stop --timeout"))
	require.Equal(t, downBefore, countCommands(runner.commands, " down --timeout"))
}

func TestCrashedAttachmentExpiresAndCannotRecreateItsLease(t *testing.T) {
	controller, runner, now := startedLifecycleController(t)
	attachment, _, err := controller.Attach(t.Context())
	require.NoError(t, err)
	*now = now.Add(testAttachmentStaleAfter + time.Second)

	require.NoError(t, controller.Stop(t.Context()))
	require.Equal(t, 1, countCommands(runner.commands, " stop --timeout"))
	require.ErrorIs(t, controller.Heartbeat(t.Context(), attachment), ErrAttachmentLost)

	status, err := controller.Status(t.Context())
	require.NoError(t, err)
	require.Empty(t, status.Attachments)
}

func TestInvalidAttachmentRegistryBlocksDestructiveLifecycle(t *testing.T) {
	controller, runner, _ := startedLifecycleController(t)
	state, err := controller.Status(t.Context())
	require.NoError(t, err)
	registryPath := filepath.Join(state.StateRoot, attachmentsFileName)
	require.NoError(t, os.WriteFile(registryPath, []byte(`{"schemaVersion":1,"unknown":true}`), 0o600))
	stopBefore := countCommands(runner.commands, " stop --timeout")
	downBefore := countCommands(runner.commands, " down --timeout")

	err = controller.Stop(t.Context())
	require.ErrorContains(t, err, "attachment ownership is uncertain")
	require.Equal(t, stopBefore, countCommands(runner.commands, " stop --timeout"))
	require.Equal(t, downBefore, countCommands(runner.commands, " down --timeout"))
	_, err = controller.PlanReset(t.Context())
	require.ErrorContains(t, err, "attachment ownership is uncertain")
	require.Equal(t, stopBefore, countCommands(runner.commands, " stop --timeout"))
	require.Equal(t, downBefore, countCommands(runner.commands, " down --timeout"))
}

func TestMissingInitializedAttachmentRegistryBlocksDestructiveLifecycle(t *testing.T) {
	controller, runner, _ := startedLifecycleController(t)
	status, err := controller.Status(t.Context())
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(status.StateRoot, attachmentsFileName)))
	stopBefore := countCommands(runner.commands, " stop --timeout")
	downBefore := countCommands(runner.commands, " down --timeout")

	err = controller.Stop(t.Context())
	require.ErrorContains(t, err, "attachment ownership is uncertain")
	require.Equal(t, stopBefore, countCommands(runner.commands, " stop --timeout"))
	require.Equal(t, downBefore, countCommands(runner.commands, " down --timeout"))
	_, err = controller.PlanReset(t.Context())
	require.ErrorContains(t, err, "attachment ownership is uncertain")
	require.Equal(t, downBefore, countCommands(runner.commands, " down --timeout"))
}

func TestResetRequiresExactCurrentResourceConfirmation(t *testing.T) {
	controller, runner, _ := startedLifecycleController(t)
	runner.responses = map[string][]byte{
		"container ls":      []byte("container-one\n"),
		"container inspect": exactOwnershipLabels(t, controller),
	}
	plan, err := controller.PlanReset(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, plan.Confirmation)
	require.Contains(t, plan.Resources, OwnedResource{Kind: "container", ID: "container-one"})

	err = controller.Reset(t.Context(), "sha256:"+strings.Repeat("0", 64))
	require.ErrorIs(t, err, ErrResetConfirmation)
	require.Zero(t, countCommands(runner.commands, " down --timeout"))

	require.NoError(t, controller.Reset(t.Context(), plan.Confirmation))
	require.Equal(t, 1, countCommands(runner.commands, " down --timeout"))
	_, err = os.Stat(filepath.Join(plan.StateRoot, stateFileName))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestResetRechecksResourceSetAfterConfirmation(t *testing.T) {
	controller, runner, _ := startedLifecycleController(t)
	plan, err := controller.PlanReset(t.Context())
	require.NoError(t, err)
	runner.responses = map[string][]byte{
		"volume ls":      []byte("new-volume\n"),
		"volume inspect": exactOwnershipLabels(t, controller),
	}
	err = controller.Reset(t.Context(), plan.Confirmation)
	require.ErrorIs(t, err, ErrResetConfirmation)
	require.Zero(t, countCommands(runner.commands, " down --timeout"))
}

func TestResetRechecksAttachmentsAfterConfirmation(t *testing.T) {
	controller, runner, _ := startedLifecycleController(t)
	plan, err := controller.PlanReset(t.Context())
	require.NoError(t, err)
	attachment, _, err := controller.Attach(t.Context())
	require.NoError(t, err)
	downBefore := countCommands(runner.commands, " down --timeout")

	err = controller.Reset(t.Context(), plan.Confirmation)
	require.ErrorIs(t, err, ErrLiveAttachments)
	require.Contains(t, err.Error(), attachment.ID())
	require.Equal(t, downBefore, countCommands(runner.commands, " down --timeout"))
}

func TestResetPersistsIntentBeforeDockerMutationAndResumes(t *testing.T) {
	controller, runner, _ := startedLifecycleController(t)
	runner.responses = map[string][]byte{
		"container ls":      []byte("container-one\n"),
		"container inspect": exactOwnershipLabels(t, controller),
	}
	plan, err := controller.PlanReset(t.Context())
	require.NoError(t, err)
	runner.failOnce = " down --timeout"
	runner.failError = "daemon acknowledgement lost"

	err = controller.Reset(t.Context(), plan.Confirmation)
	require.ErrorContains(t, err, "daemon acknowledgement lost")
	state, exists, err := loadState(filepath.Join(plan.StateRoot, stateFileName))
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, phaseReset, state.Phase)
	require.Equal(t, statusIncomplete, state.Status)
	require.Equal(t, &failure{Phase: phaseReset, Code: "reset_failed"}, state.LastError)
	require.Equal(t, resetStagePlanned, state.Reset.Stage)
	require.Equal(t, plan.Resources, state.Reset.Resources)

	_, err = controller.Start(t.Context())
	require.ErrorIs(t, err, ErrResetInProgress)
	recovered, err := controller.PlanReset(t.Context())
	require.NoError(t, err)
	require.Equal(t, plan, recovered)
	require.NoError(t, controller.Reset(t.Context(), plan.Confirmation))
	_, err = os.Stat(filepath.Join(plan.StateRoot, stateFileName))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestResetResumesAfterSubordinateStateCleanupInterruption(t *testing.T) {
	controller, _, _ := startedLifecycleController(t)
	plan, err := controller.PlanReset(t.Context())
	require.NoError(t, err)
	statePath := filepath.Join(plan.StateRoot, stateFileName)
	state, exists, err := loadState(statePath)
	require.NoError(t, err)
	require.True(t, exists)
	state.Phase = phaseReset
	state.Status = statusApplying
	state.Reset = &resetState{Stage: resetStageSessionRemoved, Resources: plan.Resources}
	require.NoError(t, saveState(statePath, state))
	require.NoError(t, os.Remove(filepath.Join(plan.StateRoot, runtimeEnvFileName)))
	require.NoError(t, os.Remove(filepath.Join(plan.StateRoot, attachmentsFileName)))

	recovered, err := controller.PlanReset(t.Context())
	require.NoError(t, err)
	require.Equal(t, plan.Confirmation, recovered.Confirmation)
	require.NoError(t, controller.Reset(t.Context(), plan.Confirmation))
	_, err = os.Stat(statePath)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestResetRecoveryRejectsTamperedProgress(t *testing.T) {
	tests := []struct {
		name     string
		progress resetState
	}{
		{name: "unknown stage", progress: resetState{Stage: "guessed"}},
		{name: "unknown resource kind", progress: resetState{Stage: resetStagePlanned, Resources: []OwnedResource{{Kind: "image", ID: "sha256:unknown"}}}},
		{name: "duplicate resource", progress: resetState{Stage: resetStagePlanned, Resources: []OwnedResource{{Kind: "volume", ID: "same"}, {Kind: "volume", ID: "same"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			controller, _, _ := startedLifecycleController(t)
			status, err := controller.Status(t.Context())
			require.NoError(t, err)
			statePath := filepath.Join(status.StateRoot, stateFileName)
			state, exists, err := loadState(statePath)
			require.NoError(t, err)
			require.True(t, exists)
			state.Phase = phaseReset
			state.Status = statusApplying
			state.Reset = &test.progress
			require.NoError(t, saveState(statePath, state))

			_, err = controller.PlanReset(t.Context())
			require.ErrorContains(t, err, "refusing guessed recovery")
		})
	}
}

func TestAttachWaitsForConcurrentLifecycleOperation(t *testing.T) {
	controller, _, _ := startedLifecycleController(t)
	status, err := controller.Status(t.Context())
	require.NoError(t, err)
	lock, err := instancelock.AcquireNamed(status.StateRoot, controllerLock)
	require.NoError(t, err)

	type attachResult struct {
		attachment *Attachment
		err        error
	}
	result := make(chan attachResult, 1)
	go func() {
		attachment, _, attachErr := controller.Attach(t.Context())
		result <- attachResult{attachment: attachment, err: attachErr}
	}()
	select {
	case early := <-result:
		require.Failf(t, "attach did not wait", "result=%+v", early)
	case <-time.After(100 * time.Millisecond):
	}
	require.NoError(t, lock.Release())
	attached := <-result
	require.NoError(t, attached.err)
	require.NotNil(t, attached.attachment)
	_, err = controller.Detach(t.Context(), attached.attachment)
	require.NoError(t, err)
}

func TestConcurrentJoinAndNonLastExitPreserveBothLiveSessions(t *testing.T) {
	controller, runner, _ := startedLifecycleController(t)
	first, _, err := controller.Attach(t.Context())
	require.NoError(t, err)
	second, _, err := controller.Attach(t.Context())
	require.NoError(t, err)

	start := make(chan struct{})
	var joined *Attachment
	var joinErr, detachErr error
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-start
		_, detachErr = controller.Detach(t.Context(), first)
	}()
	go func() {
		defer wait.Done()
		<-start
		joined, _, joinErr = controller.Attach(t.Context())
	}()
	close(start)
	wait.Wait()

	require.NoError(t, detachErr)
	require.NoError(t, joinErr)
	require.NotNil(t, joined)
	status, err := controller.Status(t.Context())
	require.NoError(t, err)
	require.Len(t, status.Attachments, 2)
	require.Zero(t, countCommands(runner.commands, " stop --timeout"))
	_, err = controller.Detach(t.Context(), second)
	require.NoError(t, err)
	_, err = controller.Detach(t.Context(), joined)
	require.NoError(t, err)
}

func TestStaleWatcherCannotRecreateAttachmentAfterReset(t *testing.T) {
	controller, _, now := startedLifecycleController(t)
	attachment, _, err := controller.Attach(t.Context())
	require.NoError(t, err)
	*now = now.Add(testAttachmentStaleAfter + time.Second)
	plan, err := controller.PlanReset(t.Context())
	require.NoError(t, err)
	require.NoError(t, controller.Reset(t.Context(), plan.Confirmation))

	require.ErrorIs(t, controller.Heartbeat(t.Context(), attachment), ErrAttachmentLost)
	status, err := controller.Status(t.Context())
	require.NoError(t, err)
	require.False(t, status.Exists)
}

func TestRunOnceRestartsRetainedRuntimeWithoutReprovisioning(t *testing.T) {
	controller, runner, _ := startedLifecycleController(t)
	bootstrapCalls := countCommands(runner.commands, "admin delivery pool bootstrap")
	initializeCalls := countCommands(runner.commands, "admin initialize --format json")

	require.NoError(t, controller.Run(t.Context(), true))
	require.NoError(t, controller.Run(t.Context(), true))
	require.Equal(t, bootstrapCalls, countCommands(runner.commands, "admin delivery pool bootstrap"))
	require.Equal(t, initializeCalls, countCommands(runner.commands, "admin initialize --format json"))
	require.Equal(t, 2, countCommands(runner.commands, " stop --timeout"))
	require.GreaterOrEqual(t, countCommands(runner.commands, "up -d leapview"), 3)
}

func TestLogsAreReadOnlyBoundedAndRedactRuntimeSecrets(t *testing.T) {
	controller, runner, _ := startedLifecycleController(t)
	status, err := controller.Status(t.Context())
	require.NoError(t, err)
	values, err := readEnvironment(filepath.Join(status.StateRoot, runtimeEnvFileName))
	require.NoError(t, err)
	secret := values["LEAPVIEW_CSRF_KEY"]
	runner.responses = map[string][]byte{
		"logs --no-color --tail 42": []byte("password=hunter2 csrf=" + secret + "\nready\n"),
	}
	before := len(runner.commands)
	logs, err := controller.Logs(t.Context(), 42)
	require.NoError(t, err)
	require.NotContains(t, string(logs), "hunter2")
	require.NotContains(t, string(logs), secret)
	require.Contains(t, string(logs), "ready")
	require.Equal(t, before+4, len(runner.commands)) // ownership lists plus one logs read
	require.Zero(t, countCommands(runner.commands, " stop --timeout"))
	require.Zero(t, countCommands(runner.commands, " down --timeout"))
}

func TestLifecycleRejectsChangedEndpointBeforeDockerOperation(t *testing.T) {
	controller, runner, _ := startedLifecycleController(t)
	controller.endpoint = &fakeEndpoint{host: "unix:///var/run/docker.sock", server: "daemon-two", fingerprint: "sha256:changed"}
	before := len(runner.commands)
	err := controller.Stop(t.Context())
	require.ErrorContains(t, err, "different Docker endpoint")
	require.Len(t, runner.commands, before)
}

func TestRunAttachmentStopsOnLastContextCancellation(t *testing.T) {
	controller, runner, _ := startedLifecycleController(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := controller.Run(ctx, false)
	require.True(t, errors.Is(err, context.Canceled) || err == nil)
	require.Equal(t, 1, countCommands(runner.commands, " stop --timeout"))
}

func startedLifecycleController(t *testing.T) (*Controller, *fakeRunner, *time.Time) {
	t.Helper()
	checkout, packageRoot, stateRoot := t.TempDir(), testRuntimePackage(t), t.TempDir()
	endpoint := &fakeEndpoint{host: "unix:///var/run/docker.sock", server: "daemon-1", fingerprint: "sha256:endpoint"}
	runner := &fakeRunner{artifacts: testQualificationArtifacts(t)}
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	options := testControllerOptions(checkout, packageRoot, stateRoot, endpoint, runner)
	options.Now = func() time.Time { return now }
	options.AttachmentStaleAfter = testAttachmentStaleAfter
	options.AttachmentHeartbeatInterval = time.Second
	options.ResetSessions = func(context.Context, SessionRequest) error { return nil }
	controller, err := New(options)
	require.NoError(t, err)
	_, err = controller.Start(t.Context())
	require.NoError(t, err)
	return controller, runner, &now
}

func exactOwnershipLabels(t *testing.T, controller *Controller) []byte {
	t.Helper()
	status, err := controller.Status(t.Context())
	require.NoError(t, err)
	return []byte(`{"io.leapview.local-runtime":"true","io.leapview.local-runtime.schema":"1","io.leapview.local-runtime.checkout":"` + status.CheckoutID + `","io.leapview.local-runtime.owner":"` + status.OwnerID + `"}`)
}
