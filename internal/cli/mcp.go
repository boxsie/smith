package cli

import (
	"github.com/boxsie/smith/internal/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

var mcpCmd = &cobra.Command{
	Use:           "mcp",
	Short:         "Run the local Smith MCP control plane over stdio",
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		server, err := mcpserver.New(mcpserver.Dependencies{Service: smithService, Version: Version})
		if err != nil {
			return err
		}
		return server.Run(cmd.Context(), &mcp.StdioTransport{})
	},
}

func init() { rootCmd.AddCommand(mcpCmd) }
