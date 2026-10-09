package cli

import (
	"context"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/cobra"
)

type CredentialSetupRequest struct{ OwnerID string }

func (r CredentialSetupRequest) Validate() error {
	if r.OwnerID == "" || len(r.OwnerID) > 255 || !utf8.ValidString(r.OwnerID) || strings.TrimSpace(r.OwnerID) != r.OwnerID || strings.IndexFunc(r.OwnerID, func(value rune) bool { return unicode.IsSpace(value) || unicode.IsControl(value) }) >= 0 {
		return errors.New("a canonical customer owner ID is required")
	}
	return nil
}

type CredentialSetupOperations interface {
	SetupCredentials(context.Context, CredentialSetupRequest, io.Writer) error
}

func credentialSetupCommand(ctx context.Context, operations Operations) *cobra.Command {
	request := CredentialSetupRequest{}
	parent := adminGroupCommand("credentials", "Offline customer credential setup")
	command := &cobra.Command{
		Use:         "setup",
		Short:       "Declare the customer owner and verify the configured keyring with the app stopped",
		Annotations: map[string]string{"leapview.dev/effect": "write", "leapview.dev/confirmation": "never"},
		Args:        cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := request.Validate(); err != nil {
				return err
			}
			operator, ok := operations.(CredentialSetupOperations)
			if !ok {
				return errors.New("customer credential setup is unavailable")
			}
			return operator.SetupCredentials(ctx, request, command.OutOrStdout())
		},
	}
	command.Flags().StringVar(&request.OwnerID, "owner", "", "explicit customer owner ID; immutable once declared")
	parent.AddCommand(command, credentialRotationCommand(ctx, operations))
	return parent
}
