package demoupgrade

import (
	"context"
	"io"

	"github.com/spf13/cobra"
)

// Command exposes only the reviewed demo provider profile, independently of the
// generic host upgrade commands and their release-authority contracts.
func Command(ctx context.Context, stdin io.Reader, stdout io.Writer) *cobra.Command {
	root := &cobra.Command{Use: "demo-upgrade", Short: "Upgrade or recover the bounded demo-02 provider", Args: cobra.NoArgs}
	for _, action := range []string{"check", "apply", "recover", "migrate"} {
		var request, journal, credential, digest string
		cmd := &cobra.Command{Use: action, Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error {
			r, err := ReadNativeRequest(request)
			if err != nil {
				return err
			}
			if action == "check" {
				return nil
			}
			return runNative(ctx, action, r, journal, credential, digest, stdin, stdout)
		}}
		cmd.Flags().StringVar(&request, "request", "", "Private qualified upgrade request")
		_ = cmd.MarkFlagRequired("request")
		if action == "migrate" {
			cmd.Flags().StringVar(&journal, "journal", "", "Read-only host journal")
			cmd.Flags().StringVar(&credential, "credential", "", "Private migration-only connection URL")
			cmd.Flags().StringVar(&digest, "recovery-digest", "", "Verified paired recovery digest")
			for _, name := range []string{"journal", "credential", "recovery-digest"} {
				_ = cmd.MarkFlagRequired(name)
			}
		}
		root.AddCommand(cmd)
	}
	return root
}
