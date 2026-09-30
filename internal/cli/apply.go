package cli

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/boxsie/smith/internal/service"
	"github.com/spf13/cobra"
)

var applyCmd = &cobra.Command{
	Use:           "apply <proposal-path>",
	Short:         "Apply a proposal to its target project",
	Args:          cobra.ExactArgs(1),
	RunE:          runApply,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	applyCmd.Flags().Bool("dry", false, "show what would be applied without writing files")
	applyCmd.Flags().Bool("force", false, "skip conflict detection")
	rootCmd.AddCommand(applyCmd)
}

func runApply(cmd *cobra.Command, args []string) error {
	dry, _ := cmd.Flags().GetBool("dry")
	force, _ := cmd.Flags().GetBool("force")

	result, err := smithService.Apply(cmd.Context(), service.ApplyRequest{
		ProposalDir: args[0],
		DryRun:      dry,
		Force:       force,
	})
	if err != nil {
		return &ExitError{Code: 3, Err: err}
	}
	applyResult := result.Apply

	// Report conflicts.
	if len(applyResult.Conflicts) > 0 {
		fmt.Fprintln(os.Stderr, "Conflicts detected:")
		for _, c := range applyResult.Conflicts {
			fmt.Fprintf(os.Stderr, "  %s %s: %s\n", c.Op, c.Path, c.Reason)
		}
		return &ExitError{Code: 1, Err: fmt.Errorf("apply aborted: %d conflict(s)", len(applyResult.Conflicts))}
	}

	// Report operations.
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "OP\tPATH")
	for _, op := range applyResult.Applied {
		fmt.Fprintf(w, "%s\t%s\n", op.Op, op.Path)
	}
	w.Flush()

	if dry {
		fmt.Fprintln(os.Stderr, "(dry run — no files written)")
		return nil
	}

	// Report validation result.
	if applyResult.Validation != nil {
		if applyResult.Validation.Status == "fail" {
			fmt.Fprintln(os.Stderr, "\nPost-apply validation failed:")
			for _, e := range applyResult.Validation.Errors {
				fmt.Fprintf(os.Stderr, "  %s\n", e)
			}
			return &ExitError{Code: 2, Err: fmt.Errorf("applied but validation failed")}
		}
		fmt.Fprintf(os.Stderr, "\nApplied to %s — validation passed\n", result.TargetDir)
	}

	return nil
}
