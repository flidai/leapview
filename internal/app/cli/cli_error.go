package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/flidai/leapview/internal/platform/cliapi"
	projectcli "github.com/flidai/leapview/internal/project/cli"
	"github.com/spf13/cobra"
)

const (
	exitSuccess       = 0
	exitExecution     = 1
	exitUsage         = 2
	exitDeployPending = 3
	exitIndeterminate = 4
	exitInterrupt     = 130
	exitTerminate     = 143
)

type cliErrorDocument struct {
	SchemaVersion int    `json:"schemaVersion"`
	Command       string `json:"command"`
	Code          string `json:"code"`
	Message       string `json:"message"`
	NextAction    string `json:"nextAction,omitempty"`
}

func renderCLIError(command *cobra.Command, err error) int {
	if err == nil {
		return exitSuccess
	}
	code, errorCode, nextAction, domainResult := classifyCLIError(command, err)
	var reported *cliapi.ReportedError
	if domainResult || errors.As(err, &reported) {
		return code
	}
	if command == nil {
		command = &cobra.Command{Use: "leapview"}
	}
	name := command.CommandPath()
	if name == "" {
		name = command.Use
		if name == "" {
			name = "leapview"
		}
	}
	message := strings.TrimSpace(err.Error())
	if jsonFormatSelected(command) {
		if err := json.NewEncoder(command.ErrOrStderr()).Encode(cliErrorDocument{
			SchemaVersion: 1,
			Command:       name,
			Code:          errorCode,
			Message:       message,
			NextAction:    nextAction,
		}); err != nil {
			return exitExecution
		}
		return code
	}
	out := command.ErrOrStderr()
	if _, writeErr := fmt.Fprintf(out, "%s failed (%s): %s\n", name, errorCode, message); writeErr != nil {
		return exitExecution
	}
	if nextAction != "" {
		if _, writeErr := fmt.Fprintf(out, "Next action: %s\n", nextAction); writeErr != nil {
			return exitExecution
		}
	}
	return code
}

func classifyCLIError(command *cobra.Command, err error) (exitCode int, code, nextAction string, hasDomainResult bool) {
	var status *projectcli.DeploymentStatusError
	if errors.As(err, &status) {
		if status == nil {
			return exitExecution, "EXECUTION_FAILED", "inspect the error and correct the underlying issue", true
		}
		switch status.Result.Outcome {
		case projectcli.DeploymentOperationActive:
			return exitSuccess, "DEPLOYMENT_ACTIVE", "", true
		case projectcli.DeploymentOperationPendingApproval:
			return exitDeployPending, "DEPLOYMENT_AWAITING_APPROVAL", status.Result.NextAction, true
		case projectcli.DeploymentOperationIndeterminate:
			return exitIndeterminate, "DEPLOYMENT_INDETERMINATE", status.Result.NextAction, true
		default:
			return exitExecution, "DEPLOYMENT_FAILED", status.Result.NextAction, true
		}
	}
	var usage *cliapi.UsageError
	unknownCommand := strings.HasPrefix(err.Error(), "unknown command ") && command != nil &&
		(command.Parent() == nil || command.Annotations[documentationHelpGroupAnnotation] == "true")
	if errors.As(err, &usage) || unknownCommand {
		return exitUsage, "INVALID_INVOCATION", "run the command with --help to review its arguments", false
	}
	var selection *projectcli.DeploymentSelectionError
	if errors.As(err, &selection) {
		if selection.Code == "INDETERMINATE_PUBLICATION" {
			return exitIndeterminate, "DEPLOYMENT_INDETERMINATE", "reconcile target publication evidence before retrying", true
		}
		return exitExecution, "DEPLOYMENT_SELECTION_FAILED", "inspect the retained operation and choose a valid handle", true
	}
	return exitExecution, "EXECUTION_FAILED", "correct the underlying issue and retry", false
}

func jsonFormatSelected(command *cobra.Command) bool {
	if command == nil {
		return false
	}
	flag := command.Flags().Lookup("format")
	if flag == nil {
		flag = command.InheritedFlags().Lookup("format")
	}
	return flag != nil && flag.Value.String() == "json"
}

func interruptExitCode(received os.Signal) int {
	switch received {
	case os.Interrupt:
		return exitInterrupt
	case syscall.SIGTERM:
		return exitTerminate
	default:
		return exitExecution
	}
}
