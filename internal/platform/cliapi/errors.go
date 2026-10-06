package cliapi

import "github.com/spf13/cobra"

// UsageError marks an error caused by an invalid command invocation.
type UsageError struct{ Err error }

func (e *UsageError) Error() string {
	if e == nil || e.Err == nil {
		return "invalid command invocation"
	}
	return e.Err.Error()
}

func (e *UsageError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// NewUsageError marks err as an invocation or command-selection error.
func NewUsageError(err error) error {
	if err == nil {
		return nil
	}
	return &UsageError{Err: err}
}

// ReportedError marks a failure whose diagnostic has already been emitted.
type ReportedError struct{ Err error }

func (e *ReportedError) Error() string {
	if e == nil || e.Err == nil {
		return "command failed after reporting diagnostics"
	}
	return e.Err.Error()
}

func (e *ReportedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// NewReportedError marks err as already rendered by the command.
func NewReportedError(err error) error {
	if err == nil {
		return nil
	}
	return &ReportedError{Err: err}
}

// NoInput reports whether command inherited the root --no-input policy.
func NoInput(command *cobra.Command) bool {
	if command == nil {
		return false
	}
	flag := command.Flags().Lookup("no-input")
	if flag == nil {
		flag = command.InheritedFlags().Lookup("no-input")
	}
	return flag != nil && flag.Value.String() == "true"
}
