package cliapi

import (
	"errors"
	"testing"

	"github.com/spf13/cobra"
)

func TestCLIErrorsRetainCause(t *testing.T) {
	cause := errors.New("invalid selection")
	for _, wrapped := range []error{NewUsageError(cause), NewReportedError(cause)} {
		if !errors.Is(wrapped, cause) || wrapped.Error() != cause.Error() {
			t.Fatalf("wrapped error %T = %v", wrapped, wrapped)
		}
	}
}

func TestNoInputReadsInheritedPersistentFlag(t *testing.T) {
	root := &cobra.Command{Use: "leapview"}
	root.PersistentFlags().Bool("no-input", false, "")
	child := &cobra.Command{Use: "doctor"}
	root.AddCommand(child)
	if NoInput(child) {
		t.Fatal("default no-input value is true")
	}
	if err := root.PersistentFlags().Set("no-input", "true"); err != nil {
		t.Fatal(err)
	}
	if !NoInput(child) {
		t.Fatal("inherited --no-input was not observed")
	}
}
