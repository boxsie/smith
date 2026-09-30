package cli

import (
	"fmt"
	"os"

	"github.com/boxsie/smith/internal/lib"
	"github.com/spf13/cobra"
)

var libCmd = &cobra.Command{
	Use:   "lib",
	Short: "Manage Smith libraries",
}

var libUpdateCmd = &cobra.Command{
	Use:           "update",
	Short:         "Update built-in libraries from embedded copies",
	Args:          cobra.NoArgs,
	RunE:          runLibUpdate,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	libUpdateCmd.Flags().Bool("force", false, "overwrite user-modified files")
	libCmd.AddCommand(libUpdateCmd)
	rootCmd.AddCommand(libCmd)
}

func runLibUpdate(cmd *cobra.Command, args []string) error {
	force, _ := cmd.Flags().GetBool("force")

	userLibDir, err := lib.UserLibDir()
	if err != nil {
		return err
	}

	report, err := lib.Update(userLibDir, force)
	if err != nil {
		return err
	}

	for _, f := range report.Created {
		fmt.Fprintf(os.Stderr, "  created: %s\n", f)
	}
	for _, f := range report.Updated {
		fmt.Fprintf(os.Stderr, "  updated: %s\n", f)
	}
	for _, f := range report.Skipped {
		fmt.Fprintf(os.Stderr, "  skipped (user-modified): %s\n", f)
	}

	total := len(report.Created) + len(report.Updated) + len(report.Unchanged)
	fmt.Fprintf(os.Stderr, "Library update complete: %d files in %s\n", total, userLibDir)

	return nil
}
