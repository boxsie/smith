package cli

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/boxsie/smith/internal/run"
	"github.com/spf13/cobra"
)

var runsCmd = &cobra.Command{
	Use:   "runs <path>",
	Short: "List all runs for an app",
	Args:  cobra.ExactArgs(1),
	RunE:  runRunsList,
}

var pruneCmd = &cobra.Command{
	Use:   "prune <path>",
	Short: "Remove old run directories",
	Args:  cobra.ExactArgs(1),
	RunE:  runRunsPrune,
}

func init() {
	pruneCmd.Flags().Int("keep", 0, "retain N most recent runs")
	pruneCmd.Flags().String("older-than", "", "remove runs older than duration (e.g. 7d, 24h, 30m)")
	pruneCmd.Flags().Bool("dry", false, "preview without deleting")
	runsCmd.AddCommand(pruneCmd)
	rootCmd.AddCommand(runsCmd)
}

func runRunsList(cmd *cobra.Command, args []string) error {
	manifests, err := smithService.ListRuns(args[0])
	if err != nil {
		return err
	}
	if len(manifests) == 0 {
		fmt.Fprintln(os.Stdout, "no runs found")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "RUN ID\tSTATUS\tSTARTED\tDURATION\tCOST")

	for _, m := range manifests {
		dur := ""
		if m.CompletedAt != "" && m.StartedAt != "" {
			// Compute duration from manifest timestamps if possible.
			for _, t := range m.Tasks {
				if t.DurationMS > 0 {
					// Sum all task durations for total.
					// Actually, use the top-level started/completed for wall time.
					break
				}
			}
		}

		var totalCost float64
		for _, t := range m.Tasks {
			totalCost += t.CostUSD
		}
		cost := ""
		if totalCost > 0 {
			cost = fmt.Sprintf("$%.4f", totalCost)
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", m.RunID, m.Status, m.StartedAt, dur, cost)
	}

	return w.Flush()
}

func runRunsPrune(cmd *cobra.Command, args []string) error {
	keep, _ := cmd.Flags().GetInt("keep")
	olderThanStr, _ := cmd.Flags().GetString("older-than")
	dry, _ := cmd.Flags().GetBool("dry")

	if keep == 0 && olderThanStr == "" {
		return fmt.Errorf("at least one of --keep or --older-than is required")
	}

	opts := run.PruneOpts{
		Keep: keep,
		Dry:  dry,
	}

	if olderThanStr != "" {
		d, err := run.ParseDuration(olderThanStr)
		if err != nil {
			return err
		}
		opts.OlderThan = d
	}

	result, err := smithService.PruneRuns(args[0], opts)
	if err != nil {
		return err
	}

	action := "removed"
	if dry {
		action = "would remove"
	}
	fmt.Fprintf(os.Stdout, "%s %d runs, kept %d\n", action, result.Removed, result.Kept)
	return nil
}
