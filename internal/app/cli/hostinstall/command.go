package hostinstall

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

type CommandOptions struct {
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
	return host
}
