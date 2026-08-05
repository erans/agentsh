package cli

import (
	"context"
	"fmt"

	"github.com/agentsh/agentsh/internal/server"
	"github.com/spf13/cobra"
)

func newServerCmd() *cobra.Command {
	var configPath string

	cmd := &cobra.Command{
		Use:   "server",
		Short: "Start the agentsh server",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			cfg, _, err := loadLocalConfig(configPath)
			if err != nil {
				return err
			}

			s, err := server.New(cfg)
			if err != nil {
				return err
			}
			defer s.Close()

			fmt.Fprintf(cmd.OutOrStdout(), "agentsh server listening on %s\n", cfg.Server.HTTP.Addr)
			return s.Run(ctx)
		},
	}

	cmd.Flags().StringVar(&configPath, "config", "", "Path to server config YAML (default: ./config.yml, ./config.yaml, or /etc/agentsh/config.yaml)")

	// Accepted and ignored for compatibility with service units generated
	// before #437, which hardcode `agentsh server --daemon`. A binary upgrade
	// does not rewrite those files, and rejecting the flag leaves the daemon
	// restart-looping. There is no behavior to implement: systemd Type=simple
	// and launchd both require the supervised process to stay in the
	// foreground, so self-daemonizing would break process tracking either way.
	cmd.Flags().Bool("daemon", false, "Deprecated: accepted for compatibility, ignored")
	_ = cmd.Flags().MarkDeprecated("daemon",
		"the server always runs in the foreground under systemd/launchd; remove it or re-run `agentsh daemon install --force`")

	return cmd
}
