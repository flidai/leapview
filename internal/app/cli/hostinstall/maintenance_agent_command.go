package hostinstall

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"

	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	"github.com/spf13/cobra"
)

func addAgentRelayCommand(ctx context.Context, root *cobra.Command, stdout io.Writer) {
	var configPath string
	cmd := &cobra.Command{Use: "agent-relay", Hidden: true, Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		raw, err := securefs.ReadPrivateFile(configPath)
		if err != nil || len(raw) > 2048 {
			return errAgentTransition
		}
		var config agentRelayConfig
		if json.Unmarshal(raw, &config) != nil || config.validate() != nil {
			return errAgentTransition
		}
		app, err := net.Listen("tcp", net.JoinHostPort(config.SidecarIP, "443"))
		if err != nil {
			return errAgentTransition
		}
		defer app.Close()
		control, err := net.Listen("tcp", net.JoinHostPort(config.SidecarIP, "4443"))
		if err != nil {
			return errAgentTransition
		}
		defer control.Close()
		return serveAgentRelay(ctx, config, app, control)
	}}
	cmd.Flags().StringVar(&configPath, "config", "", "Private operation-owned relay configuration")
	_ = cmd.MarkFlagRequired("config")
	root.AddCommand(cmd)
}

func privateAgentExportWriter(stdout io.Writer) bool {
	file, ok := stdout.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && (info.Mode()&os.ModeNamedPipe != 0 || info.Mode()&os.ModeSocket != 0)
}
