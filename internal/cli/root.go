package cli

import (
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:     "smith",
	Short:   "Headless control plane for inspectable LLM task trees",
	Long:    "Smith is a headless control plane for inspectable LLM task trees. Run 'smith mcp' for the primary MCP interface; direct commands are retained for bootstrap, recovery, and diagnostics.",
	Version: Version,
	RunE:    func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
}

func Execute() error {
	return rootCmd.Execute()
}
