package hostinstall

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
)

type CommandOptions struct {
	Root      string
	DockerBin string
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
}

func Command(ctx context.Context, options CommandOptions) *cobra.Command {
	host := &cobra.Command{
		Use:   "host",
		Short: "Install LeapView on a supported Linux host",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return command.Help()
		},
	}
	configPath := "/run/leapview/bootstrap.json"
	payloadPath := ""
	sourceImage := ""
	install := &cobra.Command{
		Use:   "install",
		Short: "Install the immutable deployment payload and initialize the instance",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if os.Geteuid() != 0 {
				return fmt.Errorf("host installation must run as root")
			}
			payload := payloadPath
			if payload == "" {
				executable, err := os.Executable()
				if err != nil {
					return err
				}
				payload = filepath.Dir(executable)
			}
			installer, err := New(Options{
				Paths: DefaultPaths(payload, configPath), DockerBin: options.DockerBin, ExpectedImage: sourceImage,
				Stdin: options.Stdin, Stdout: options.Stdout, Stderr: options.Stderr,
			})
			if err != nil {
				return err
			}
			return installer.Install(ctx)
		},
	}
	install.Flags().StringVar(&configPath, "config", configPath, "private bootstrap configuration file")
	install.Flags().StringVar(&payloadPath, "payload", payloadPath, "immutable deployment payload (defaults to the leapviewctl directory)")
	install.Flags().StringVar(&sourceImage, "source-image", sourceImage, "immutable image from which the deployment payload was extracted")
	host.AddCommand(install)
	addRecoveryAdmissionCommand(ctx, host)
	addUpgradeCommand(ctx, host, options)
	addMaintenanceCommands(ctx, host, options)
	return host
}

func addRecoveryAdmissionCommand(ctx context.Context, host *cobra.Command) {
	var request RecoveryAdmissionRequest
	var controlURLPath string
	command := &cobra.Command{
		Use:   "admit-recovery",
		Short: "Validate a provider-restored recovery handoff before host activation",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if os.Geteuid() != 0 {
				return fmt.Errorf("host recovery admission must run as root")
			}
			if filepath.Clean(request.OutputPath) == filepath.Clean(request.ReportPath) {
				return fmt.Errorf("recovery admission output must differ from its report")
			}
			if err := invalidateRecoveryAdmission(request.OutputPath); err != nil {
				return fmt.Errorf("invalidate prior recovery admission: %w", err)
			}
			controlURLBytes, err := securefs.ReadPrivateFile(controlURLPath)
			if err != nil {
				return fmt.Errorf("read private recovery ledger URL: %w", err)
			}
			controlURL := strings.TrimSpace(string(controlURLBytes))
			if controlURL == "" || strings.ContainsAny(controlURL, "\r\n") {
				return fmt.Errorf("private recovery ledger URL is empty or malformed")
			}
			pool, err := pgxpool.New(ctx, controlURL)
			if err != nil {
				return fmt.Errorf("connect PostgreSQL recovery ledger: %w", err)
			}
			defer pool.Close()
			_, err = (RecoveryAdmission{Ledger: refreshpostgres.NewRecoveryLedger(pool)}).Admit(ctx, request)
			return err
		},
	}
	command.Flags().StringVar(&request.ReportPath, "report", "", "private authoritative FAI-981 provider restore report")
	command.Flags().StringVar(&controlURLPath, "control-url-file", "", "private PostgreSQL recovery ledger URL file")
	command.Flags().StringVar(&request.SecretRoot, "secret-root", "/run/leapview/recovery", "provisioner-owned recovery secret bundle directory")
	command.Flags().StringVar(&request.OutputPath, "output", "/opt/leapview/recovery-admission.json", "durable credential-free admission evidence")
	command.Flags().StringVar(&request.OccurrenceID, "occurrence-id", "", "expected durable recovery occurrence ID")
	command.Flags().StringVar(&request.TargetID, "target-id", "", "expected authoritative target ID")
	command.Flags().StringVar(&request.RecoverySetID, "recovery-set-id", "", "expected RecoverySet ID")
	command.Flags().StringVar(&request.FrontierDigest, "frontier-digest", "", "expected RecoverySet frontier digest")
	command.Flags().StringVar(&request.ArtifactIdentity, "artifact", "", "expected immutable admitted OCI artifact")
	for _, name := range []string{"report", "control-url-file", "occurrence-id", "target-id", "recovery-set-id", "frontier-digest", "artifact"} {
		_ = command.MarkFlagRequired(name)
	}
	host.AddCommand(command)
}
