package cli

import (
	"fmt"
	"os"

	"github.com/boxsie/smith/internal/tui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var tuiCmd = &cobra.Command{
	Use:           "tui",
	Short:         "Launch the legacy diagnostic terminal UI",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if !isInteractive() {
			return fmt.Errorf("smith tui requires an interactive terminal (stdin and stdout must be TTYs)")
		}
		return tui.Run()
	},
}

func init() {
	rootCmd.AddCommand(tuiCmd)
}

func isInteractive() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) &&
		term.IsTerminal(int(os.Stdout.Fd()))
}
