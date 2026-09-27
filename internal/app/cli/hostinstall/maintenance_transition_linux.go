//go:build linux

package hostinstall

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	securefs "github.com/flidai/leapview/internal/platform/filesystem"
)

const transitionReviewerSecretName = "reviewer.secret"
const transitionPublisherSecretName = "publisher.secret"

func (e *NativeEffects) transitionOn(ctx context.Context, id Identity, recoveryDigest, network string, rehearsal bool) (retErr error) {
	if e.request.AccessTransition == nil {
		return nil
	}
	if id != e.id || !digestPattern.MatchString(recoveryDigest) || network == "" {
		return errors.New("access transition requires the exact host operation and recovery point")
	}
	name := e.clonePrefix() + "-transition"
	if err := e.cleanupTransition(ctx); err != nil {
		return err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		retErr = errors.Join(retErr, e.cleanupTransition(cleanupCtx))
	}()
	mode := "live"
	journalPath := filepath.Join(e.root, JournalName)
	homeSource := e.original.Volumes["home"]
	if rehearsal || e.detached {
		homeSource = filepath.Join(e.operation, "rehearsal", "home")
	}
	if e.detached {
		mode = "detached"
		journalPath = filepath.Join(e.operation, detachedStateName)
	} else if rehearsal {
		mode = "rehearsal"
	}

	uid, gid, err := e.candidateRuntimeIdentity(ctx, id.Candidate)
	if err != nil {
		return err
	}
	requestPath := filepath.Join(e.operation, "request.json")
	stagedRequest := filepath.Join(e.operation, "transition-request.json")
	stagedJournal := filepath.Join(e.operation, "transition-journal.json")
	stagedPublisher := filepath.Join(e.operation, "transition-publisher.secret")
	stagedReviewer := filepath.Join(e.operation, "transition-reviewer.secret")
	stagedEnvironment := filepath.Join(e.operation, "transition.env")
	for _, path := range []string{stagedRequest, stagedJournal, stagedPublisher, stagedReviewer, stagedEnvironment} {
		defer os.Remove(path)
	}
	for source, destination := range map[string]string{
		requestPath: stagedRequest,
		journalPath: stagedJournal,
		filepath.Join(e.operation, transitionPublisherSecretName): stagedPublisher,
		filepath.Join(e.operation, transitionReviewerSecretName):  stagedReviewer,
	} {
		if err = stageTransitionInput(source, destination, uid, gid); err != nil {
			return err
		}
	}

	environment, err := e.candidateContainerEnvironment()
	if err != nil {
		return err
	}
	if err = securefs.WritePrivateFileAtomic(stagedEnvironment, environment); err != nil {
		return err
	}
	if err = validateTransitionHome(e.original.App, e.request.Profile.Volumes["home"], e.original.Volumes["home"], homeSource); err != nil {
		return err
	}
	volumeSources := map[string]string{e.original.Volumes["home"]: homeSource}
	appEnvironment := containerEnv(e.original.App)
	controlURL := appEnvironment["LEAPVIEW_POSTGRES_CONTROL_URL"]
	tlsMounts, err := migrationTLSMounts(controlURL, e.original.App, volumeSources)
	if err != nil {
		return err
	}
	homeDestination := "/var/lib/leapview"
	for _, mount := range e.original.App.Mounts {
		if mount.Type == "volume" && mount.Name == e.request.Profile.Volumes["home"] && mount.Source == e.original.Volumes["home"] {
			homeDestination = mount.Destination
			break
		}
	}

	args := []string{"run", "--rm", "--name", name, "--user", fmt.Sprintf("%d:%d", uid, gid),
		"--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges:true", "--restart=no",
		"--tmpfs", "/tmp:rw,nosuid,nodev,size=536870912,mode=1777",
		"--network", network, "--env-file", stagedEnvironment,
		"--mount", "type=bind,src=" + stagedRequest + ",dst=/upgrade/request.json,readonly",
		"--mount", "type=bind,src=" + stagedJournal + ",dst=/upgrade-journal.json,readonly",
		"--mount", "type=bind,src=" + stagedPublisher + ",dst=/upgrade/publisher.secret,readonly",
		"--mount", "type=bind,src=" + stagedReviewer + ",dst=/upgrade/reviewer.secret,readonly",
		"--mount", "type=bind,src=" + homeSource + ",dst=" + homeDestination}
	args = append(args, tlsMounts...)
	args = append(args, "--entrypoint", "/usr/local/bin/leapview", id.Candidate,
		"admin", "transition-access",
		"--request", "/upgrade/request.json",
		"--journal", "/upgrade-journal.json",
		"--recovery-digest", recoveryDigest,
		"--mode", mode,
		"--publisher-credential-file", "/upgrade/publisher.secret",
		"--reviewer-credential-file", "/upgrade/reviewer.secret")
	result, err := e.docker(ctx, args...)
	if err != nil {
		return fmt.Errorf("candidate access transition failed: %w", err)
	}
	return e.recordTransitionResult(result, rehearsal)
}

func (e *NativeEffects) candidateRuntimeIdentity(ctx context.Context, image string) (int, int, error) {
	readID := func(flag string) (int, error) {
		output, err := e.docker(ctx, "run", "--rm", "--network", "none", "--entrypoint", "id", image, flag)
		if err != nil {
			return 0, err
		}
		value := strings.TrimSpace(output)
		return parseCandidateRuntimeID(value)
	}
	uid, err := readID("-u")
	if err != nil {
		return 0, 0, err
	}
	if uid == 0 {
		return 0, 0, errors.New("candidate transition must run as its non-root default user")
	}
	gid, err := readID("-g")
	if err != nil {
		return 0, 0, err
	}
	return uid, gid, nil
}

func parseCandidateRuntimeID(value string) (int, error) {
	number, err := strconv.ParseUint(value, 10, 32)
	if err != nil || strconv.FormatUint(number, 10) != value {
		return 0, errors.New("candidate default runtime identity must be a canonical uint32")
	}
	if number > uint64(^uint(0)>>1) {
		return 0, errors.New("candidate default runtime identity does not fit host integer")
	}
	return int(number), nil
}

func stageTransitionInput(source, destination string, uid, gid int) error {
	info, err := os.Lstat(source)
	if err != nil {
		return fmt.Errorf("read transition input: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() == 0 {
		return errors.New("transition inputs must be nonempty private regular files")
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) {
		return errors.New("transition inputs must be owned by the host maintenance user")
	}
	contents, err := securefs.ReadPrivateFile(source)
	if err != nil {
		return err
	}
	if err = securefs.WritePrivateFileAtomic(destination, contents); err != nil {
		return err
	}
	if err = os.Chown(destination, uid, gid); err != nil {
		return err
	}
	if err = os.Chmod(destination, 0o400); err != nil {
		return err
	}
	staged, err := os.Stat(destination)
	if err != nil || !staged.Mode().IsRegular() || staged.Mode().Perm() != 0o400 {
		return errors.New("staged transition input must be read-only")
	}
	if owner, ok := staged.Sys().(*syscall.Stat_t); !ok || owner.Uid != uint32(uid) || owner.Gid != uint32(gid) {
		return errors.New("staged transition input has the wrong runtime owner")
	}
	return nil
}

func (e *NativeEffects) candidateContainerEnvironment() ([]byte, error) {
	prepared, err := e.candidateEnvironment()
	if err != nil {
		return nil, err
	}
	var key string
	for _, line := range strings.Split(string(prepared), "\n") {
		if strings.HasPrefix(line, "LEAPVIEW_AGENT_CREDENTIAL_KEY=") {
			key = line
			break
		}
	}
	if key == "" || strings.ContainsAny(key, "\r\n") {
		return nil, errors.New("candidate agent credential key is unavailable")
	}
	replacements := map[string]string{
		"LEAPVIEW_AGENT_CREDENTIAL_KEY": strings.TrimPrefix(key, "LEAPVIEW_AGENT_CREDENTIAL_KEY="),
		"LEAPVIEW_IMAGE":                e.id.Candidate,
	}
	environment := append([]string(nil), e.original.App.Config.Env...)
	seen := map[string]bool{}
	for i, entry := range environment {
		if strings.ContainsAny(entry, "\r\n") {
			return nil, errors.New("multiline application environment is unsupported")
		}
		name, _, ok := strings.Cut(entry, "=")
		value, replace := replacements[name]
		if ok && replace {
			if seen[name] {
				return nil, fmt.Errorf("duplicate application environment key %s", name)
			}
			environment[i] = name + "=" + value
			seen[name] = true
		}
	}
	for _, name := range []string{"LEAPVIEW_AGENT_CREDENTIAL_KEY", "LEAPVIEW_IMAGE"} {
		if !seen[name] {
			environment = append(environment, name+"="+replacements[name])
		}
	}
	preparedEnvironment := []byte(strings.Join(environment, "\n") + "\n")
	var expectedDigest string
	if e.detached {
		state, err := readDetachedState(e.operation)
		if err != nil || state.Identity != e.id || state.Phase != DetachedRunning {
			return nil, errors.New("detached candidate environment requires its exact running receipt")
		}
		expectedDigest = state.CandidateEnvironmentDigest
	} else if e.request.PreparationDigest != "" {
		preparedRoot := filepath.Join(e.provider, "upgrade-operations", strings.TrimPrefix(e.request.PreparationDigest, "sha256:"))
		state, err := readDetachedState(preparedRoot)
		if err != nil || state.Phase != DetachedPassed || state.Identity.ArtifactAdmissionDigest != e.request.PreparationDigest {
			return nil, errors.New("live candidate environment requires its exact passed rehearsal")
		}
		expectedDigest = state.CandidateEnvironmentDigest
	}
	if e.detached || e.request.PreparationDigest != "" {
		if !digestPattern.MatchString(expectedDigest) || candidateEnvironmentDigest(preparedEnvironment) != expectedDigest {
			return nil, errors.New("candidate runtime environment differs from the passed rehearsal")
		}
	}
	return preparedEnvironment, nil
}

func validateTransitionHome(app dockerInspection, volumeName, liveSource, selectedSource string) error {
	if volumeName == "" || liveSource == "" || selectedSource == "" {
		return errors.New("access transition requires the inventoried application home volume")
	}
	count := 0
	for _, mount := range app.Mounts {
		if mount.Type == "volume" && mount.Name == volumeName && mount.Source == liveSource {
			if mount.Destination == "" || !mount.RW {
				return errors.New("application home mount is not writable")
			}
			count++
		}
	}
	info, err := os.Stat(selectedSource)
	if err != nil || !info.IsDir() || count != 1 {
		return errors.New("access transition home volume mapping is invalid")
	}
	return nil
}

func (e *NativeEffects) cleanupTransition(ctx context.Context) error {
	name := e.clonePrefix() + "-transition"
	found, err := e.docker(ctx, "ps", "-a", "--filter", "name=^/"+name+"$", "--format", "{{.Names}}")
	if err != nil {
		return err
	}
	if found == "" {
		return nil
	}
	if found != name {
		return errors.New("unexpected candidate transition container matched cleanup selector")
	}
	_, err = e.docker(ctx, "rm", "-f", name)
	return err
}
