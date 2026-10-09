package cli

import (
	"context"
	"errors"
	"io"

	"github.com/spf13/cobra"
)

type CredentialRotationRequest struct{ BatchSize int }

func (r CredentialRotationRequest) Validate() error {
	if r.BatchSize < 1 || r.BatchSize > 100 {
		return errors.New("credential rotation batch size must be between 1 and 100")
	}
	return nil
}

type CredentialRotationOperations interface {
	RotateCredentials(context.Context, CredentialRotationRequest, io.Writer) error
}

func credentialRotationCommand(ctx context.Context, operations Operations) *cobra.Command {
	request := CredentialRotationRequest{BatchSize: 100}
	command := &cobra.Command{Use: "rotate", Short: "Rewrap one bounded batch with the configured active key while the app is stopped", Args: cobra.NoArgs, Annotations: map[string]string{"leapview.dev/effect": "write", "leapview.dev/confirmation": "never"}, RunE: func(command *cobra.Command, _ []string) error {
		if err := request.Validate(); err != nil {
			return err
		}
		operator, ok := operations.(CredentialRotationOperations)
		if !ok {
			return errors.New("customer credential rotation is unavailable")
		}
		return operator.RotateCredentials(ctx, request, command.OutOrStdout())
	}}
	command.Flags().IntVar(&request.BatchSize, "batch-size", 100, "maximum envelopes per invocation; rerun until complete")
	return command
}
