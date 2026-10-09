package cli

import (
	"context"
	"errors"
	"io"

	"github.com/flidai/leapview/internal/credential"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/spf13/cobra"
)

type FirstSourceAdmissionRequest struct {
	Intent credential.FirstSourceAdmissionIntent
	Apply  bool
}

type FirstSourceAdmissionOperations interface {
	AdmitFirstSource(context.Context, FirstSourceAdmissionRequest, io.Writer) error
}

func firstSourceAdmissionCommand(ctx context.Context, operations Operations) *cobra.Command {
	var path string
	var apply bool
	command := &cobra.Command{
		Use: "admit-first-source", Short: "Admit one exact production source binding and credential operator with the app stopped",
		Args: cobra.NoArgs, Annotations: map[string]string{"leapview.dev/effect": "write", "leapview.dev/confirmation": "never"},
		RunE: func(command *cobra.Command, _ []string) error {
			body, err := securefs.ReadPrivateFile(path)
			if err != nil {
				return err
			}
			var intent credential.FirstSourceAdmissionIntent
			if err := strictjson.DecodeWithOptions(body, &intent, strictjson.Options{MaxBytes: 32 << 10, MaxDepth: 16, DuplicateKeys: strictjson.CaseFoldedKeys, AllowUnknownFields: false}); err != nil {
				return errors.New("first-source admission requires a bounded exact intent document")
			}
			if err := intent.Validate(); err != nil {
				return err
			}
			operator, ok := operations.(FirstSourceAdmissionOperations)
			if !ok {
				return errors.New("offline first-source admission is unavailable")
			}
			return operator.AdmitFirstSource(ctx, FirstSourceAdmissionRequest{Intent: intent, Apply: apply}, command.OutOrStdout())
		},
	}
	command.Flags().StringVar(&path, "intent", "", "private JSON file with explicit exact source and operator intent")
	command.Flags().BoolVar(&apply, "apply", false, "persist audited admission; otherwise preview only")
	return command
}
