package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/cliapi"
	projectcli "github.com/flidai/leapview/internal/project/cli"
	"github.com/spf13/cobra"
)

type cliErrorWriter struct{ err error }

func (writer cliErrorWriter) Write([]byte) (int, error) { return 0, writer.err }

func TestCLIErrorBoundaryRendersSelectedJSONAndClassifiesStatuses(t *testing.T) {
	command := &cobra.Command{Use: "deploy"}
	command.Flags().String("format", "text", "")
	if err := command.Flags().Set("format", "json"); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.SetErr(&stderr)
	if got := renderCLIError(command, cliapi.NewUsageError(errors.New("missing argument"))); got != exitUsage {
		t.Fatalf("usage exit code = %d", got)
	}
	var document cliErrorDocument
	if err := json.Unmarshal(stderr.Bytes(), &document); err != nil {
		t.Fatalf("JSON diagnostic = %q: %v", stderr.String(), err)
	}
	if document.Command != "deploy" || document.Code != "INVALID_INVOCATION" || document.Message != "missing argument" {
		t.Fatalf("diagnostic = %#v", document)
	}

	for _, test := range []struct {
		name    string
		outcome projectcli.DeploymentOperationOutcome
		want    int
	}{
		{name: "active", outcome: projectcli.DeploymentOperationActive, want: exitSuccess},
		{name: "pending", outcome: projectcli.DeploymentOperationPendingApproval, want: exitDeployPending},
		{name: "indeterminate", outcome: projectcli.DeploymentOperationIndeterminate, want: exitIndeterminate},
		{name: "failure", outcome: projectcli.DeploymentOperationFailure, want: exitExecution},
	} {
		t.Run(test.name, func(t *testing.T) {
			stderr.Reset()
			err := &projectcli.DeploymentStatusError{Result: projectcli.DeploymentOperationResult{Handle: "release-42", Outcome: test.outcome, NextAction: "resume exact handle"}}
			if got := renderCLIError(command, err); got != test.want {
				t.Fatalf("exit code = %d, want %d", got, test.want)
			}
			if stderr.Len() != 0 {
				t.Fatalf("status result was duplicated to stderr: %q", stderr.String())
			}
		})
	}
}

func TestCLIErrorBoundaryKeepsRetainedSelectionAndReportedErrorsQuiet(t *testing.T) {
	command := &cobra.Command{Use: "deploy"}
	var stderr bytes.Buffer
	command.SetErr(&stderr)
	selection := &projectcli.DeploymentSelectionError{Code: "UNKNOWN_RETAINED_OPERATION", Detail: "the stored operation is corrupt"}
	if got := renderCLIError(command, selection); got != exitExecution || stderr.Len() != 0 {
		t.Fatalf("selection result code=%d stderr=%q", got, stderr.String())
	}
	selection.Code = "INDETERMINATE_PUBLICATION"
	if got := renderCLIError(command, selection); got != exitIndeterminate || stderr.Len() != 0 {
		t.Fatalf("indeterminate publication code=%d stderr=%q", got, stderr.String())
	}
	selection.Code = "UNKNOWN_RETAINED_OPERATION"
	if got := renderCLIError(command, cliapi.NewReportedError(errors.New("validation failed"))); got != exitExecution || stderr.Len() != 0 {
		t.Fatalf("reported result code=%d stderr=%q", got, stderr.String())
	}
	if got := renderCLIError(command, errors.New("unknown retained operation storage failure")); got != exitExecution || stderr.Len() == 0 {
		t.Fatalf("storage failure code=%d stderr=%q", got, stderr.String())
	}
}

func TestCLIErrorBoundaryReturnsWriterFailures(t *testing.T) {
	writeErr := errors.New("stderr unavailable")
	command := &cobra.Command{Use: "deploy"}
	command.Flags().String("format", "text", "")
	command.SetErr(cliErrorWriter{err: writeErr})
	if got := renderCLIError(command, errors.New("execution failed")); got != exitExecution {
		t.Fatalf("text writer exit code = %d", got)
	}
	if err := command.Flags().Set("format", "json"); err != nil {
		t.Fatal(err)
	}
	if got := renderCLIError(command, errors.New("execution failed")); got != exitExecution {
		t.Fatalf("JSON writer exit code = %d", got)
	}
}

func TestRunRendersInvalidJSONValidationOnlyToStdout(t *testing.T) {
	command := NewCommand(t.Context())
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{"validate", filepath.Join(t.TempDir(), "missing-source"), "--format", "json"})
	selected, err := command.ExecuteContextC(t.Context())
	if got := renderCLIError(selected, err); got != exitExecution {
		t.Fatalf("validation exit code = %d, err=%v", got, err)
	}
	var result struct {
		OK          bool  `json:"ok"`
		Diagnostics []any `json:"diagnostics"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not one JSON result: %q: %v", stdout.String(), err)
	}
	if result.OK || len(result.Diagnostics) == 0 {
		t.Fatalf("validation result = %#v", result)
	}
	if stderr.Len() != 0 {
		t.Fatalf("validation result was duplicated to stderr: %q", stderr.String())
	}
}

func TestRunSignalExitCodes(t *testing.T) {
	if got := interruptExitCode(os.Interrupt); got != exitInterrupt {
		t.Fatalf("SIGINT exit code = %d", got)
	}
	if got := interruptExitCode(syscall.SIGTERM); got != exitTerminate {
		t.Fatalf("SIGTERM exit code = %d", got)
	}
	if got := interruptExitCode(syscall.SIGHUP); got != exitExecution {
		t.Fatalf("unexpected signal exit code = %d", got)
	}
}

func TestRunExitCodeKeepsGracefulServeShutdownSuccessful(t *testing.T) {
	root := &cobra.Command{Use: "leapview"}
	serve := &cobra.Command{Use: "serve"}
	client := &cobra.Command{Use: "healthcheck"}
	root.AddCommand(serve, client)
	var stderr bytes.Buffer
	serve.SetErr(&stderr)
	client.SetErr(&stderr)

	if got := runExitCode(serve, nil, syscall.SIGTERM); got != exitSuccess {
		t.Fatalf("graceful serve SIGTERM exit code = %d, want %d", got, exitSuccess)
	}
	if stderr.Len() != 0 {
		t.Fatalf("graceful serve SIGTERM wrote an error: %q", stderr.String())
	}

	if got := runExitCode(serve, errors.New("application shutdown failed"), syscall.SIGTERM); got != exitExecution {
		t.Fatalf("failed serve SIGTERM exit code = %d, want %d", got, exitExecution)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("application shutdown failed")) {
		t.Fatalf("failed serve SIGTERM diagnostic = %q", stderr.String())
	}

	stderr.Reset()
	if got := runExitCode(client, context.Canceled, syscall.SIGTERM); got != exitTerminate {
		t.Fatalf("cancelled client SIGTERM exit code = %d, want %d", got, exitTerminate)
	}
	if stderr.Len() != 0 {
		t.Fatalf("cancelled client signal wrote an error: %q", stderr.String())
	}
}

func TestRunSignalCancelsCommandAndReturnsConventionalStatus(t *testing.T) {
	for _, test := range []struct {
		name     string
		signal   os.Signal
		exitCode int
	}{
		{name: "interrupt", signal: os.Interrupt, exitCode: exitInterrupt},
		{name: "terminate", signal: syscall.SIGTERM, exitCode: exitTerminate},
	} {
		t.Run(test.name, func(t *testing.T) {
			requestStarted := make(chan struct{})
			requestCanceled := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				close(requestStarted)
				<-request.Context().Done()
				close(requestCanceled)
			}))
			defer server.Close()

			child := exec.Command(os.Args[0], "-test.run=^TestRunSignalChildProcess$")
			child.Env = append(os.Environ(),
				"LEAPVIEW_RUN_SIGNAL_CHILD=1",
				"LEAPVIEW_TEST_HEALTHCHECK_URL="+server.URL,
			)
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			t.Cleanup(func() {
				if !waited {
					_ = child.Process.Kill()
					_ = child.Wait()
				}
			})
			select {
			case <-requestStarted:
			case <-time.After(10 * time.Second):
				t.Fatal("child command did not start its healthcheck request")
			}
			if err := child.Process.Signal(test.signal); err != nil {
				t.Fatal(err)
			}
			select {
			case <-requestCanceled:
			case <-time.After(5 * time.Second):
				t.Fatal("signal did not cancel the in-flight command request")
			}
			err := child.Wait()
			waited = true
			var exitError *exec.ExitError
			if !errors.As(err, &exitError) || exitError.ExitCode() != test.exitCode {
				t.Fatalf("child wait error = %v, want exit code %d", err, test.exitCode)
			}
		})
	}
}

func TestRunSignalChildProcess(t *testing.T) {
	if os.Getenv("LEAPVIEW_RUN_SIGNAL_CHILD") != "1" {
		return
	}
	os.Args = []string{"leapview", "healthcheck", "--url", os.Getenv("LEAPVIEW_TEST_HEALTHCHECK_URL"), "--timeout", "30s"}
	os.Exit(Run(context.Background()))
}
