package composectl

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/creachadair/jrpc2"
	"github.com/creachadair/jrpc2/channel"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
)

const qualificationRPCMaxMessageBytes = 64 << 10

type qualificationProcess struct {
	dir         string
	executable  string
	environment []string
}

type qualificationCommandRequest struct {
	Directory   string
	Executable  string
	Environment []string
	Stdin       io.Reader
	Arguments   []string
}

type qualificationCommandExecutor interface {
	Execute(context.Context, qualificationCommandRequest) ([]byte, error)
}

type osQualificationCommandExecutor struct{}

func (osQualificationCommandExecutor) Execute(
	ctx context.Context,
	request qualificationCommandRequest,
) ([]byte, error) {
	command := exec.CommandContext(ctx, request.Executable, request.Arguments...)
	command.Dir = request.Directory
	command.Env = request.Environment
	if len(command.Env) == 0 {
		command.Env = os.Environ()
	}
	command.Stdin = request.Stdin
	return command.CombinedOutput()
}

func (p qualificationProcess) Run(
	ctx context.Context,
	stdin io.Reader,
	executor qualificationCommandExecutor,
	args ...string,
) ([]byte, error) {
	if executor == nil {
		executor = osQualificationCommandExecutor{}
	}
	output, err := executor.Execute(ctx, qualificationCommandRequest{
		Directory: p.dir, Executable: p.executable,
		Environment: append([]string(nil), p.environment...),
		Stdin:       stdin, Arguments: append([]string(nil), args...),
	})
	if err != nil {
		commandText := string(redactQualificationBytes(
			[]byte(p.executable + " " + strings.Join(args, " ")),
		))
		return output, fmt.Errorf(
			"%s: %w: %s",
			commandText,
			err,
			redactQualificationLog(output, 100),
		)
	}
	return output, nil
}

func (c *Controller) qualificationDocker(
	ctx context.Context,
	stdin io.Reader,
	args ...string,
) ([]byte, error) {
	return qualificationProcess{
		dir:         c.root,
		executable:  c.dockerBin,
		environment: os.Environ(),
	}.Run(ctx, stdin, c.qualificationExecutor, args...)
}

func composeArguments(root string, args ...string) ([]string, error) {
	https, err := envFileValue(filepath.Join(root, deploymentEnvName), "COMPOSE_HTTPS")
	if err != nil {
		return nil, err
	}
	project, err := envFileValue(filepath.Join(root, deploymentEnvName), "COMPOSE_PROJECT_NAME")
	if err != nil {
		return nil, err
	}
	if project == "" || project != strings.TrimSpace(project) || normalizedQualificationName(project) != project {
		return nil, errors.New("Compose project name must be a normalized identifier")
	}
	result := []string{
		"compose",
		"--project-name", project,
		"--project-directory", root,
		"--env-file", filepath.Join(root, deploymentEnvName),
		"--file", filepath.Join(root, "compose.yaml"),
	}
	if https == "1" {
		result = append(result, "--file", filepath.Join(root, "compose.https.yaml"))
	}
	return append(result, args...), nil
}

func (c *Controller) qualificationCompose(
	ctx context.Context,
	root string,
	args ...string,
) ([]byte, error) {
	commandArgs, err := composeArguments(root, args...)
	if err != nil {
		return nil, err
	}
	processEnvironment, err := composeProcessEnvironment(root, nil)
	if err != nil {
		return nil, err
	}
	return qualificationProcess{
		dir: c.root, executable: c.dockerBin, environment: processEnvironment,
	}.Run(ctx, nil, c.qualificationExecutor, commandArgs...)
}

// qualificationComposeEnvironment supplies operation-only credentials to the
// Compose process without persisting them in leapview.env or rendering their
// values in command arguments. Callers pass `run --env NAME` so Compose copies
// only the named value into the one-shot container.
func (c *Controller) qualificationComposeEnvironment(
	ctx context.Context,
	root string,
	environment map[string]string,
	args ...string,
) ([]byte, error) {
	commandArgs, err := composeArguments(root, args...)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(environment))
	for name := range environment {
		if strings.TrimSpace(name) == "" || strings.Contains(name, "=") {
			return nil, fmt.Errorf("qualification operation environment name %q is invalid", name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	processEnvironment, err := composeProcessEnvironment(root, environment)
	if err != nil {
		return nil, err
	}
	return qualificationProcess{
		dir: c.root, executable: c.dockerBin, environment: processEnvironment,
	}.Run(ctx, nil, c.qualificationExecutor, commandArgs...)
}

// composeProcessEnvironment prevents host shell variables from
// overriding the generated deployment contract. Docker daemon
// settings remain inherited, while every Compose variable and every key owned
// by deployment.env is resolved only from the explicit command arguments and
// --env-file. Narrow operation-only values are then appended for named `run
// --env NAME` forwarding.
func composeProcessEnvironment(root string, overrides map[string]string) ([]string, error) {
	contents, err := os.ReadFile(filepath.Join(root, deploymentEnvName))
	if err != nil {
		return nil, fmt.Errorf("read Compose deployment environment: %w", err)
	}
	owned := environmentValues(string(contents))
	result := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		name, _, present := strings.Cut(entry, "=")
		if !present || strings.HasPrefix(name, "COMPOSE_") {
			continue
		}
		if _, protected := owned[name]; protected {
			continue
		}
		if _, overridden := overrides[name]; overridden {
			continue
		}
		result = append(result, entry)
	}
	names := make([]string, 0, len(overrides))
	for name := range overrides {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		result = append(result, name+"="+overrides[name])
	}
	return result, nil
}

type qualificationJSONWorker struct {
	command  *exec.Cmd
	client   *jrpc2.Client
	stderr   *boundedQualificationBuffer
	mu       sync.Mutex
	eventMu  sync.Mutex
	onEvent  func(string, json.RawMessage) error
	eventErr error
}

func startQualificationJSONWorker(
	ctx context.Context,
	dir string,
	environment []string,
	executable string,
	arguments ...string,
) (*qualificationJSONWorker, error) {
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Dir = dir
	command.Env = environment
	if len(command.Env) == 0 {
		command.Env = os.Environ()
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	stderr := &boundedQualificationBuffer{maxBytes: 256 << 10}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		return nil, err
	}
	worker := &qualificationJSONWorker{
		command: command,
		stderr:  stderr,
	}
	worker.client = jrpc2.NewClient(
		newQualificationRPCChannel(stdout, stdin),
		&jrpc2.ClientOptions{
			OnNotify:   worker.handleNotification,
			OnCallback: worker.handleCallback,
		},
	)
	return worker, nil
}

func (w *qualificationJSONWorker) Call(
	method string,
	params any,
	result any,
	onEvent func(string, json.RawMessage) error,
) error {
	return w.CallContext(context.Background(), method, params, result, onEvent)
}

func (w *qualificationJSONWorker) CallContext(
	ctx context.Context,
	method string,
	params any,
	result any,
	onEvent func(string, json.RawMessage) error,
) error {
	if ctx == nil {
		return fmt.Errorf("%s worker context is required", method)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.client == nil {
		return fmt.Errorf("%s worker client is not configured", method)
	}
	w.eventMu.Lock()
	w.onEvent = onEvent
	w.eventErr = nil
	w.eventMu.Unlock()
	defer func() {
		w.eventMu.Lock()
		w.onEvent = nil
		w.eventMu.Unlock()
	}()
	var err error
	if result == nil {
		_, err = w.client.Call(ctx, method, params)
	} else {
		err = w.client.CallResult(ctx, method, params, result)
	}
	if ctx.Err() != nil {
		_ = w.Kill()
		return fmt.Errorf("%s worker call: %w", method, ctx.Err())
	}
	w.eventMu.Lock()
	eventErr := w.eventErr
	w.eventMu.Unlock()
	if eventErr != nil {
		return eventErr
	}
	if err != nil {
		if w.stderr != nil {
			diagnostics := strings.TrimSpace(string(redactQualificationLog(
				[]byte(w.stderr.String()),
				100,
			)))
			if diagnostics != "" {
				return fmt.Errorf("%s worker: %w: %s", method, err, diagnostics)
			}
		}
		return fmt.Errorf("%s worker: %w", method, err)
	}
	return nil
}

func (w *qualificationJSONWorker) handleNotification(request *jrpc2.Request) {
	_, _ = w.handleEvent(request)
}

func (w *qualificationJSONWorker) handleCallback(
	_ context.Context,
	request *jrpc2.Request,
) (any, error) {
	return w.handleEvent(request)
}

func (w *qualificationJSONWorker) handleEvent(
	request *jrpc2.Request,
) (any, error) {
	var params json.RawMessage
	err := request.UnmarshalParams(&params)
	w.eventMu.Lock()
	defer w.eventMu.Unlock()
	if err == nil {
		if w.onEvent == nil {
			err = fmt.Errorf("worker emitted unexpected event %q", request.Method())
		} else {
			err = w.onEvent(request.Method(), params)
		}
	}
	if err != nil && w.eventErr == nil {
		w.eventErr = err
	}
	return map[string]bool{"handled": err == nil}, err
}

func (w *qualificationJSONWorker) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.client != nil {
		_ = w.client.Close()
	}
	if w.command == nil {
		return nil
	}
	waitErr := w.command.Wait()
	if waitErr != nil {
		return fmt.Errorf("qualification worker exit: %w: %s", waitErr, w.stderr.String())
	}
	return nil
}

func (w *qualificationJSONWorker) Kill() error {
	if w == nil || w.command == nil || w.command.Process == nil {
		if w != nil && w.client != nil {
			return w.client.Close()
		}
		return nil
	}
	if w.client != nil {
		_ = w.client.Close()
	}
	if err := w.command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	_ = w.command.Wait()
	return nil
}

type qualificationRPCChannel struct {
	reader *bufio.Reader
	writer io.WriteCloser
}

func newQualificationRPCChannel(
	reader io.Reader,
	writer io.WriteCloser,
) channel.Channel {
	return &qualificationRPCChannel{
		reader: bufio.NewReaderSize(reader, qualificationRPCMaxMessageBytes+1),
		writer: writer,
	}
}

func (c *qualificationRPCChannel) Send(message []byte) error {
	if len(message) > qualificationRPCMaxMessageBytes {
		return fmt.Errorf("qualification RPC message exceeds %d bytes", qualificationRPCMaxMessageBytes)
	}
	if bytes.Contains(message, []byte{'\n'}) {
		return fmt.Errorf("qualification RPC message contains a newline")
	}
	framed := make([]byte, len(message)+1)
	copy(framed, message)
	framed[len(message)] = '\n'
	_, err := c.writer.Write(framed)
	return err
}

func (c *qualificationRPCChannel) Recv() ([]byte, error) {
	message, err := c.reader.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) ||
		len(message) > qualificationRPCMaxMessageBytes+1 {
		return nil, fmt.Errorf(
			"qualification RPC message exceeds %d bytes",
			qualificationRPCMaxMessageBytes,
		)
	}
	if err != nil && !(errors.Is(err, io.EOF) && len(message) != 0) {
		return nil, err
	}
	return bytes.TrimSuffix(message, []byte{'\n'}), nil
}

func (c *qualificationRPCChannel) Close() error {
	return c.writer.Close()
}

type boundedQualificationBuffer struct {
	maxBytes int
	bytes.Buffer
}

func (b *boundedQualificationBuffer) Write(contents []byte) (int, error) {
	written := len(contents)
	_, _ = b.Buffer.Write(contents)
	if b.maxBytes > 0 && b.Buffer.Len() > b.maxBytes {
		value := append([]byte(nil), b.Buffer.Bytes()[b.Buffer.Len()-b.maxBytes:]...)
		b.Buffer.Reset()
		_, _ = b.Buffer.Write(value)
	}
	return written, nil
}

func copyQualificationFile(source, destination string, mode fs.FileMode) error {
	contents, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(destination, contents, mode); err != nil {
		return err
	}
	// Qualification is commonly launched from hardened CI shells with umask
	// 0077. Enforce the caller's reviewed final mode so executable packaged
	// assets remain readable by the non-root users in sidecar containers.
	return os.Chmod(destination, mode)
}

func copyQualificationTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return copyQualificationFile(path, target, info.Mode().Perm())
	})
}

func writeQualificationJSON(path string, value any) error {
	contents, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	return securefs.WritePrivateFileAtomic(path, contents)
}

func readQualificationJSON(path string, value any) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(contents, value); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func appendOrReplaceQualificationEnv(path, key, value string) error {
	if err := validateEnvLineValue(key, value); err != nil {
		return err
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(contents), "\n")
	found := false
	for index, line := range lines {
		name, _, present := strings.Cut(line, "=")
		if present && name == key {
			lines[index] = key + "=" + value
			found = true
		}
	}
	if !found {
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		lines = append(lines, key+"=", "")
		lines[len(lines)-2] = key + "=" + value
	}
	return securefs.WritePrivateFileAtomic(path, []byte(strings.Join(lines, "\n")))
}

func qualificationWait(
	ctx context.Context,
	interval time.Duration,
	operation func(context.Context) (bool, error),
) error {
	for {
		complete, err := operation(ctx)
		if err != nil {
			return err
		}
		if complete {
			return nil
		}
		if err := sleepContext(ctx, interval); err != nil {
			return err
		}
	}
}

func parseQualificationInteger(value, label string) (int64, error) {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", label, err)
	}
	return parsed, nil
}

func joinQualificationError(primary error, cleanup error) error {
	if cleanup == nil {
		return primary
	}
	if primary == nil {
		return cleanup
	}
	return errors.Join(primary, fmt.Errorf("qualification cleanup: %w", cleanup))
}
