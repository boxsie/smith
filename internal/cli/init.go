package cli

import (
	"fmt"
	"os"

	"github.com/boxsie/smith/internal/lib"
	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:           "init",
	Short:         "Initialize Smith libraries",
	Args:          cobra.NoArgs,
	RunE:          runInit,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	initCmd.Flags().Bool("force", false, "overwrite user-modified library files")
	rootCmd.AddCommand(initCmd)
}

func runInit(cmd *cobra.Command, args []string) error {
	force, _ := cmd.Flags().GetBool("force")

	userLibDir, err := lib.UserLibDir()
	if err != nil {
		return err
	}

	report, err := lib.Init(userLibDir, force)
	if err != nil {
		return err
	}

	for _, f := range report.Created {
		fmt.Fprintf(os.Stderr, "  created: %s\n", f)
	}
	for _, f := range report.Skipped {
		fmt.Fprintf(os.Stderr, "  skipped (user-modified): %s\n", f)
	}
	for _, f := range report.Unchanged {
		fmt.Fprintf(os.Stderr, "  unchanged: %s\n", f)
	}

	total := len(report.Created) + len(report.Unchanged)
	fmt.Fprintf(os.Stderr, "Initialized %d library files in %s\n", total, userLibDir)

	return nil
}
