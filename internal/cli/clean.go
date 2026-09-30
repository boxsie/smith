package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var cleanCmd = &cobra.Command{
	Use:   "clean <path>",
	Short: "Remove generated content (runs, cache) from a smith app directory",
	Long: `Remove generated content from a smith app directory, leaving the
applied plan (task files) intact.

By default, removes .smith/runs/, .smith/cache/, and .smith/tool-runs/
but keeps .smith/proposals/. Use --all to remove the entire .smith/ directory.`,
	Args:          cobra.ExactArgs(1),
	RunE:          runClean,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	cleanCmd.Flags().Bool("all", false, "remove the entire .smith/ directory including proposals")
	cleanCmd.Flags().Bool("dry", false, "preview what would be removed without deleting")
	rootCmd.AddCommand(cleanCmd)
}

func runClean(cmd *cobra.Command, args []string) error {
	absRoot, err := filepath.Abs(args[0])
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	smithDir := filepath.Join(absRoot, ".smith")
	if _, err := os.Stat(smithDir); os.IsNotExist(err) {
		fmt.Fprintln(os.Stdout, "nothing to clean")
		return nil
	}

	all, _ := cmd.Flags().GetBool("all")
	dry, _ := cmd.Flags().GetBool("dry")

	if all {
		return removeDir(smithDir, ".smith/", dry)
	}

	// Remove runs and cache, keep proposals.
	targets := []string{
		filepath.Join(smithDir, "runs"),
		filepath.Join(smithDir, "cache"),
		filepath.Join(smithDir, "tool-runs"),
	}

	removed := 0
	for _, target := range targets {
		if _, err := os.Stat(target); os.IsNotExist(err) {
			continue
		}
		rel, _ := filepath.Rel(absRoot, target)
		if err := removeDir(target, rel+"/", dry); err != nil {
			return err
		}
		removed++
	}

	if removed == 0 {
		fmt.Fprintln(os.Stdout, "nothing to clean")
	}

	return nil
}

func removeDir(path, label string, dry bool) error {
	if dry {
		fmt.Fprintf(os.Stdout, "would remove %s\n", label)
		return nil
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove %s: %w", label, err)
	}
	fmt.Fprintf(os.Stdout, "removed %s\n", label)
	return nil
}
