package localruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
	"github.com/flidai/leapview/internal/platform/safetext"
)

var (
	ErrRuntimeNotFound   = errors.New("no local development runtime exists for this checkout")
	ErrResetConfirmation = errors.New("reset confirmation does not match the current checkout resource set")
	ErrResetInProgress   = errors.New("local runtime reset is incomplete; rerun leapview dev reset with the original confirmation")
)

type lifecycleRuntime struct {
	state   State
	root    string
	envPath string
}

func (controller *Controller) initializeAttachmentRegistry(ctx context.Context, root, statePath string, state *State) error {
	if state == nil || state.AttachmentRegistryVersion != 0 || state.Phase != phaseIntent {
		return errors.New("local attachment registry cannot be initialized from the retained runtime phase")
	}
	lock, err := acquireAttachmentLock(ctx, root)
	if err != nil {
		return err
	}
	defer lock.Release()
	path := filepath.Join(root, attachmentsFileName)
	registry, err := loadAttachmentRegistry(path, attachmentBindingFor(*state), true)
	if err != nil {
		return fmt.Errorf("initialize local attachment registry: %w", err)
	}
	if len(registry.Attachments) != 0 {
		return errors.New("uninitialized local runtime unexpectedly has attachment records")
	}
	if err := saveAttachmentRegistry(path, registry); err != nil {
		return err
	}
	state.AttachmentRegistryVersion = attachmentSchemaVersion
	if err := saveState(statePath, *state); err != nil {
		return fmt.Errorf("persist local attachment registry intent: %w", err)
	}
	return nil
}

func (controller *Controller) lifecycleRuntime(ctx context.Context) (lifecycleRuntime, error) {
	if err := controller.endpoint.Verify(ctx); err != nil {
		return lifecycleRuntime{}, fmt.Errorf("verify pinned Docker endpoint: %w", err)
	}
	canonicalCheckout, checkoutID, err := checkoutIdentity(controller.checkoutRoot)
	if err != nil {
		return lifecycleRuntime{}, err
	}
	manifest, manifestDigest, err := loadManifest(controller.packageRoot, controller.identity)
	if err != nil {
		return lifecycleRuntime{}, err
	}
	packageRoot, err := canonicalDirectory(controller.packageRoot)
	if err != nil {
		return lifecycleRuntime{}, err
	}
	controller.packageRoot = packageRoot
	root := stateDirectory(controller.stateRoot, checkoutID)
	statePath := filepath.Join(root, stateFileName)
	state, exists, err := loadState(statePath)
	if err != nil {
		return lifecycleRuntime{}, err
	}
	if !exists {
		return lifecycleRuntime{state: State{Checkout: checkout{CanonicalRoot: canonicalCheckout, ID: checkoutID}}, root: root}, ErrRuntimeNotFound
	}
	if err := validateRetainedIntent(state, canonicalCheckout, checkoutID, manifestDigest, controller.endpoint, controller.identity); err != nil {
		return lifecycleRuntime{}, err
	}
	envPath := filepath.Join(root, runtimeEnvFileName)
	if state.Reset != nil {
		return lifecycleRuntime{state: state, root: root, envPath: envPath}, nil
	}
	values, err := readEnvironment(envPath)
	if err != nil {
		return lifecycleRuntime{}, fmt.Errorf("read retained local runtime environment: %w", err)
	}
	if err := validateRetainedEnvironment(state, manifest, values); err != nil {
		return lifecycleRuntime{}, err
	}
	return lifecycleRuntime{state: state, root: root, envPath: envPath}, nil
}

func acquireLifecycleLock(ctx context.Context, root string) (*instancelock.Lock, error) {
	return acquireRuntimeLock(ctx, root, controllerLock, 5*time.Second, "local development lifecycle")
}

func acquireRuntimeLock(ctx context.Context, root, name string, wait time.Duration, description string) (*instancelock.Lock, error) {
	deadline := time.Now().Add(wait)
	for {
		lock, err := instancelock.AcquireNamed(root, name)
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, instancelock.ErrAlreadyInUse) {
			return nil, fmt.Errorf("coordinate %s: %w", description, err)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("another %s operation is active; retry after it completes: %w", description, err)
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (controller *Controller) Attach(ctx context.Context) (*Attachment, State, error) {
	runtime, err := controller.lifecycleRuntime(ctx)
	if err != nil {
		return nil, State{}, err
	}
	if runtime.state.Reset != nil {
		return nil, State{}, ErrResetInProgress
	}
	lock, err := acquireLifecycleLock(ctx, runtime.root)
	if err != nil {
		return nil, State{}, err
	}
	defer lock.Release()
	runtime, err = controller.lifecycleRuntime(ctx)
	if err != nil {
		return nil, State{}, err
	}
	if err := controller.verifyResourceOwnership(ctx, runtime.state, false); err != nil {
		return nil, State{}, err
	}
	if err := controller.waitReady(ctx, runtime.state.Network.URL+"/readyz"); err != nil {
		return nil, State{}, fmt.Errorf("local runtime is not ready for attachment; retry leapview dev after lifecycle work completes: %w", err)
	}
	attachment, err := controller.registerAttachment(ctx, runtime.root, runtime.state)
	if err != nil {
		return nil, State{}, err
	}
	return attachment, runtime.state, nil
}

func (controller *Controller) Detach(ctx context.Context, attachment *Attachment) (DetachResult, error) {
	if attachment == nil || attachment.root == "" || attachment.id == "" || attachment.token == "" {
		return DetachResult{}, ErrAttachmentLost
	}
	lock, err := acquireLifecycleLock(ctx, attachment.root)
	if err != nil {
		return DetachResult{}, err
	}
	defer lock.Release()
	attachmentLock, err := acquireAttachmentLock(ctx, attachment.root)
	if err != nil {
		return DetachResult{}, err
	}
	path := filepath.Join(attachment.root, attachmentsFileName)
	registry, loadErr := loadAttachmentRegistry(path, attachment.binding, false)
	if loadErr != nil {
		_ = attachmentLock.Release()
		return DetachResult{}, fmt.Errorf("local attachment ownership is uncertain; refusing implicit shutdown: %w", loadErr)
	}
	live, _, classifyErr := classifyAttachments(registry, controller.now(), controller.attachmentStaleAfter)
	if classifyErr != nil {
		_ = attachmentLock.Release()
		return DetachResult{}, fmt.Errorf("local attachment ownership is uncertain; refusing implicit shutdown: %w", classifyErr)
	}
	found := false
	remaining := live[:0]
	for _, record := range live {
		if record.ID == attachment.id && record.TokenDigest == attachmentTokenDigest(attachment.token) {
			found = true
			continue
		}
		remaining = append(remaining, record)
	}
	if !found {
		_ = attachmentLock.Release()
		return DetachResult{}, ErrAttachmentLost
	}
	registry.Attachments = remaining
	if err := saveAttachmentRegistry(path, registry); err != nil {
		_ = attachmentLock.Release()
		return DetachResult{}, err
	}
	if err := attachmentLock.Release(); err != nil {
		return DetachResult{}, err
	}
	result := DetachResult{RemainingAttachments: len(remaining)}
	if len(remaining) > 0 {
		return result, nil
	}
	runtime, err := controller.lifecycleRuntime(ctx)
	if err != nil {
		return result, fmt.Errorf("detached last session but could not verify runtime for shutdown: %w", err)
	}
	if err := controller.verifyResourceOwnership(ctx, runtime.state, false); err != nil {
		return result, err
	}
	if err := controller.compose(ctx, runtime.envPath, nil, "stop", "--timeout", "30"); err != nil {
		return result, fmt.Errorf("stop checkout-owned local services after last detach: %w", err)
	}
	result.ServicesStopped = true
	return result, nil
}

func (controller *Controller) Run(ctx context.Context, once bool) error {
	if _, err := controller.Start(ctx); err != nil {
		return err
	}
	attachment, _, err := controller.Attach(ctx)
	if err != nil {
		return err
	}
	if once {
		_, err := controller.Detach(ctx, attachment)
		return err
	}
	heartbeatErrors := make(chan error, 1)
	heartbeatCtx, cancelHeartbeat := context.WithCancel(context.Background())
	defer cancelHeartbeat()
	go func() {
		ticker := time.NewTicker(controller.attachmentHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				if err := controller.Heartbeat(heartbeatCtx, attachment); err != nil {
					heartbeatErrors <- err
					return
				}
			}
		}
	}()
	select {
	case err := <-heartbeatErrors:
		return err
	case <-ctx.Done():
		cancelHeartbeat()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		_, detachErr := controller.Detach(shutdownCtx, attachment)
		if detachErr != nil {
			return detachErr
		}
		return nil
	}
}

func (controller *Controller) Status(ctx context.Context) (LifecycleStatus, error) {
	runtime, err := controller.lifecycleRuntime(ctx)
	if errors.Is(err, ErrRuntimeNotFound) {
		return LifecycleStatus{CheckoutRoot: runtime.state.Checkout.CanonicalRoot, CheckoutID: runtime.state.Checkout.ID, Attachments: []AttachmentStatus{}}, nil
	}
	if err != nil {
		return LifecycleStatus{}, err
	}
	if runtime.state.Reset != nil {
		return LifecycleStatus{
			Exists: true, RuntimeStatus: runtime.state.Status, Phase: runtime.state.Phase,
			CheckoutRoot: runtime.state.Checkout.CanonicalRoot, CheckoutID: runtime.state.Checkout.ID,
			StateRoot: runtime.root, ComposeProject: runtime.state.Runtime.ComposeProject,
			OwnerID: runtime.state.Runtime.OwnerID, URL: runtime.state.Network.URL,
			Attachments: []AttachmentStatus{},
		}, nil
	}
	if err := controller.verifyResourceOwnership(ctx, runtime.state, false); err != nil {
		return LifecycleStatus{}, err
	}
	lock, err := acquireAttachmentLock(ctx, runtime.root)
	if err != nil {
		return LifecycleStatus{}, err
	}
	registry, loadErr := loadAttachmentRegistry(filepath.Join(runtime.root, attachmentsFileName), attachmentBindingFor(runtime.state), runtime.state.AttachmentRegistryVersion == 0)
	_ = lock.Release()
	if loadErr != nil {
		return LifecycleStatus{}, fmt.Errorf("local attachment ownership is uncertain: %w", loadErr)
	}
	live, _, err := classifyAttachments(registry, controller.now(), controller.attachmentStaleAfter)
	if err != nil {
		return LifecycleStatus{}, fmt.Errorf("local attachment ownership is uncertain: %w", err)
	}
	services, err := controller.serviceStatus(ctx, runtime.envPath)
	if err != nil {
		return LifecycleStatus{}, err
	}
	return LifecycleStatus{
		Exists: true, RuntimeStatus: runtime.state.Status, Phase: runtime.state.Phase,
		CheckoutRoot: runtime.state.Checkout.CanonicalRoot, CheckoutID: runtime.state.Checkout.ID,
		StateRoot: runtime.root, ComposeProject: runtime.state.Runtime.ComposeProject,
		OwnerID: runtime.state.Runtime.OwnerID, URL: runtime.state.Network.URL,
		Services: services, Attachments: attachmentStatuses(live),
	}, nil
}

func (controller *Controller) serviceStatus(ctx context.Context, envPath string) (map[string]string, error) {
	output, err := controller.composeOutput(ctx, envPath, nil, "ps", "--all", "--format", "{{.Service}}={{.State}}")
	if err != nil {
		return nil, fmt.Errorf("read checkout-owned local service status: %w", err)
	}
	services := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		name, state, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(name) != name || name == "" || strings.TrimSpace(state) != state || state == "" {
			return nil, errors.New("Docker Compose returned an invalid local service status")
		}
		services[name] = state
	}
	return services, nil
}

func (controller *Controller) Stop(ctx context.Context) error {
	runtime, err := controller.lifecycleRuntime(ctx)
	if err != nil {
		return err
	}
	if runtime.state.Reset != nil {
		return ErrResetInProgress
	}
	lock, err := acquireLifecycleLock(ctx, runtime.root)
	if err != nil {
		return err
	}
	defer lock.Release()
	runtime, err = controller.lifecycleRuntime(ctx)
	if err != nil {
		return err
	}
	if err := controller.verifyResourceOwnership(ctx, runtime.state, false); err != nil {
		return err
	}
	if _, err := controller.requireNoLiveAttachments(ctx, runtime, true); err != nil {
		return err
	}
	if err := controller.compose(ctx, runtime.envPath, nil, "stop", "--timeout", "30"); err != nil {
		return fmt.Errorf("stop checkout-owned local services: %w", err)
	}
	return nil
}

func (controller *Controller) requireNoLiveAttachments(ctx context.Context, runtime lifecycleRuntime, prune bool) ([]attachmentRecord, error) {
	lock, err := acquireAttachmentLock(ctx, runtime.root)
	if err != nil {
		return nil, err
	}
	defer lock.Release()
	path := filepath.Join(runtime.root, attachmentsFileName)
	registry, err := loadAttachmentRegistry(path, attachmentBindingFor(runtime.state), runtime.state.AttachmentRegistryVersion == 0)
	if err != nil {
		return nil, fmt.Errorf("local attachment ownership is uncertain; refusing destructive lifecycle operation: %w", err)
	}
	live, stale, err := classifyAttachments(registry, controller.now(), controller.attachmentStaleAfter)
	if err != nil {
		return nil, fmt.Errorf("local attachment ownership is uncertain; refusing destructive lifecycle operation: %w", err)
	}
	if len(live) > 0 {
		ids := make([]string, 0, len(live))
		for _, record := range live {
			ids = append(ids, record.ID)
		}
		sort.Strings(ids)
		return live, fmt.Errorf("%w: detach %s first", ErrLiveAttachments, strings.Join(ids, ", "))
	}
	if prune && stale > 0 {
		registry.Attachments = []attachmentRecord{}
		if err := saveAttachmentRegistry(path, registry); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func (controller *Controller) Logs(ctx context.Context, tail int) ([]byte, error) {
	if tail < 1 || tail > 10000 {
		return nil, errors.New("local log tail must be between 1 and 10000 lines")
	}
	runtime, err := controller.lifecycleRuntime(ctx)
	if err != nil {
		return nil, err
	}
	if runtime.state.Reset != nil {
		return nil, ErrResetInProgress
	}
	if err := controller.verifyResourceOwnership(ctx, runtime.state, false); err != nil {
		return nil, err
	}
	output, err := controller.composeOutput(ctx, runtime.envPath, nil, "logs", "--no-color", "--tail", strconv.Itoa(tail))
	if err != nil {
		return nil, fmt.Errorf("read checkout-owned local service logs: %w", err)
	}
	return controller.redactRuntimeOutput(runtime, output), nil
}

func (controller *Controller) redactRuntimeOutput(runtime lifecycleRuntime, output []byte) []byte {
	redacted := string(output)
	if values, err := readEnvironment(runtime.envPath); err == nil {
		for name, value := range values {
			upper := strings.ToUpper(name)
			if value != "" && (strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "KEY") || strings.HasSuffix(upper, "_URL")) {
				redacted = strings.ReplaceAll(redacted, value, safetext.Replacement)
			}
		}
	}
	if encoded, err := securefs.ReadPrivateFile(filepath.Join(runtime.root, credentialsFileName)); err == nil {
		var values map[string]any
		if json.Unmarshal(encoded, &values) == nil {
			for name, raw := range values {
				value, ok := raw.(string)
				upper := strings.ToUpper(name)
				if ok && value != "" && (strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET")) {
					redacted = strings.ReplaceAll(redacted, value, safetext.Replacement)
				}
			}
		}
	}
	return []byte(safetext.Credentials(redacted))
}

func (controller *Controller) PlanReset(ctx context.Context) (ResetPlan, error) {
	runtime, err := controller.lifecycleRuntime(ctx)
	if err != nil {
		return ResetPlan{}, err
	}
	lock, err := acquireLifecycleLock(ctx, runtime.root)
	if err != nil {
		return ResetPlan{}, err
	}
	defer lock.Release()
	runtime, err = controller.lifecycleRuntime(ctx)
	if err != nil {
		return ResetPlan{}, err
	}
	if runtime.state.Reset != nil {
		return resetPlan(runtime, runtime.state.Reset.Resources), nil
	}
	if _, err := controller.requireNoLiveAttachments(ctx, runtime, false); err != nil {
		return ResetPlan{}, err
	}
	resources, err := controller.ownedResources(ctx, runtime.state, false)
	if err != nil {
		return ResetPlan{}, err
	}
	return resetPlan(runtime, resources), nil
}

func resetPlan(runtime lifecycleRuntime, resources []OwnedResource) ResetPlan {
	resources = append([]OwnedResource(nil), resources...)
	sort.Slice(resources, func(i, j int) bool {
		if resources[i].Kind == resources[j].Kind {
			return resources[i].ID < resources[j].ID
		}
		return resources[i].Kind < resources[j].Kind
	})
	hash := sha256.New()
	for _, value := range []string{runtime.state.OperationID, runtime.state.Checkout.ID, runtime.state.Runtime.OwnerID, runtime.state.Runtime.ComposeProject, runtime.state.Endpoint.Host, runtime.state.Endpoint.ServerID, runtime.state.Endpoint.Fingerprint} {
		_, _ = hash.Write([]byte(strconv.Itoa(len(value))))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(value))
	}
	for _, resource := range resources {
		_, _ = fmt.Fprintf(hash, "%s\x00%d\x00%s\x00", resource.Kind, len(resource.ID), resource.ID)
	}
	return ResetPlan{
		CheckoutRoot: runtime.state.Checkout.CanonicalRoot, CheckoutID: runtime.state.Checkout.ID,
		StateRoot: runtime.root, Resources: resources,
		Confirmation: "sha256:" + hex.EncodeToString(hash.Sum(nil)),
	}
}

func (controller *Controller) Reset(ctx context.Context, confirmation string) error {
	runtime, err := controller.lifecycleRuntime(ctx)
	if err != nil {
		return err
	}
	lock, err := acquireLifecycleLock(ctx, runtime.root)
	if err != nil {
		return err
	}
	defer lock.Release()
	runtime, err = controller.lifecycleRuntime(ctx)
	if err != nil {
		return err
	}
	var plan ResetPlan
	if runtime.state.Reset == nil {
		if _, err := controller.requireNoLiveAttachments(ctx, runtime, false); err != nil {
			return err
		}
		resources, err := controller.ownedResources(ctx, runtime.state, false)
		if err != nil {
			return err
		}
		plan = resetPlan(runtime, resources)
	} else {
		plan = resetPlan(runtime, runtime.state.Reset.Resources)
	}
	if confirmation == "" || confirmation != plan.Confirmation {
		return ErrResetConfirmation
	}
	if runtime.state.Reset == nil {
		runtime.state.Phase = phaseReset
		runtime.state.Status = statusApplying
		runtime.state.LastError = nil
		runtime.state.Reset = &resetState{Stage: resetStagePlanned, Resources: plan.Resources}
		if err := saveState(filepath.Join(runtime.root, stateFileName), runtime.state); err != nil {
			return fmt.Errorf("persist local reset intent before mutation: %w", err)
		}
	}
	return controller.resumeReset(ctx, runtime)
}

func (controller *Controller) resumeReset(ctx context.Context, runtime lifecycleRuntime) error {
	statePath := filepath.Join(runtime.root, stateFileName)
	if runtime.state.Reset == nil {
		return errors.New("local reset progress is unavailable; refusing guessed recovery")
	}
	if runtime.state.Reset.Stage == resetStagePlanned {
		current, err := controller.ownedResources(ctx, runtime.state, false)
		if err != nil {
			return err
		}
		if !resourceSubset(current, runtime.state.Reset.Resources) {
			return errors.New("local Docker resource set changed after reset intent was persisted; refusing mutation")
		}
		if len(current) > 0 {
			if err := controller.compose(ctx, runtime.envPath, nil, "down", "--timeout", "30", "--volumes", "--remove-orphans"); err != nil {
				return fmt.Errorf("remove confirmed checkout-owned local resources: %w", err)
			}
		}
		remaining, err := controller.ownedResources(ctx, runtime.state, false)
		if err != nil {
			return err
		}
		if len(remaining) != 0 {
			return errors.New("confirmed local Docker resources remain after reset; retained reset progress was preserved for recovery")
		}
		runtime.state.Reset.Stage = resetStageResourcesRemoved
		if err := saveState(statePath, runtime.state); err != nil {
			return fmt.Errorf("persist local reset resource removal: %w", err)
		}
	}
	if runtime.state.Reset.Stage == resetStageResourcesRemoved {
		if controller.resetSessions == nil {
			return errors.New("local session reset authority is unavailable; retained reset progress was preserved for recovery")
		}
		request := SessionRequest{TargetName: sessionTargetName(runtime.state), Origin: runtime.state.Network.URL, InstanceID: runtime.state.Authority.InstanceID, Environment: runtime.state.Authority.Environment, ProjectID: runtime.state.Authority.ProjectUID}
		if err := controller.resetSessions(ctx, request); err != nil {
			return fmt.Errorf("remove checkout-owned local authoring session: %w", err)
		}
		runtime.state.Reset.Stage = resetStageSessionRemoved
		if err := saveState(statePath, runtime.state); err != nil {
			return fmt.Errorf("persist local reset session removal: %w", err)
		}
	}
	// Keep the authoritative state descriptor until every subordinate artifact
	// has been removed. A retry can therefore finish cleanup even when an
	// interruption happened after runtime.env or the attachment registry left.
	for _, name := range []string{runtimeEnvFileName, credentialsFileName, qualificationFileName, poolFileName, evidenceFileName, attachmentsFileName, stateFileName} {
		path := filepath.Join(runtime.root, name)
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove local runtime state %s: %w", name, err)
		}
	}
	return nil
}

func resourceSubset(current, planned []OwnedResource) bool {
	allowed := make(map[OwnedResource]struct{}, len(planned))
	for _, resource := range planned {
		allowed[resource] = struct{}{}
	}
	for _, resource := range current {
		if _, ok := allowed[resource]; !ok {
			return false
		}
	}
	return true
}
