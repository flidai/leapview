package hostinstall

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"

	"github.com/spf13/cobra"
)

// maintenanceCommand adds the explicit operator maintenance path without
// changing the authoritative release-transition interface.
func maintenanceCommand(ctx context.Context, stdin io.Reader, stdout io.Writer) *cobra.Command {
	root := &cobra.Command{Use: "upgrade", Short: "Operator-authorized single-host maintenance", Args: cobra.NoArgs}
	for _, action := range []string{"plan", "apply", "recover", "status", "migrate", "rehearse"} {
		var request, journal, credential, digest string
		cmd := &cobra.Command{Use: action, Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error {
			r, err := ReadNativeRequest(request)
			if err != nil {
				return err
			}
			if action == "plan" {
				if err := checkMaintenancePlan(ctx, r); err != nil {
					return err
				}
				id, err := r.Identity()
				if err != nil {
					return err
				}
				return json.NewEncoder(stdout).Encode(map[string]any{"plan": r.Plan, "operationDigest": id.ArtifactAdmissionDigest})
			}
			if action == "status" {
				state, err := readJournalFile(filepath.Join(r.Profile.Root, JournalName))
				if err != nil {
					return err
				}
				return json.NewEncoder(stdout).Encode(state)
			}
			return runNative(ctx, action, r, journal, credential, digest, stdin, stdout)
		}}
		cmd.Flags().StringVar(&request, "request", "", "Private qualified upgrade request")
		_ = cmd.MarkFlagRequired("request")
		if action == "migrate" || action == "rehearse" {
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

func addMaintenanceCommands(ctx context.Context, host *cobra.Command, options CommandOptions) {
	shared := maintenanceCommand(ctx, options.Stdin, options.Stdout)
	for _, cmd := range host.Commands() {
		if cmd.Name() == "upgrade" {
			for _, sub := range shared.Commands() {
				cmd.AddCommand(sub)
			}
			return
		}
	}
	host.AddCommand(shared)
}
