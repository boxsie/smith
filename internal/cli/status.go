package cli

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status <path>",
	Short: "Report per-task status from the latest run manifest",
	Args:  cobra.ExactArgs(1),
	RunE:  runStatus,
}

func init() {
	statusCmd.Flags().String("run", "", "show status for a specific run ID")
	rootCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command, args []string) error {
	runFlag, _ := cmd.Flags().GetString("run")
	m, err := smithService.Status(args[0], runFlag)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "Run:    %s\n", m.RunID)
	fmt.Fprintf(os.Stdout, "Status: %s\n", m.Status)
	if m.CompletedAt != "" {
		fmt.Fprintf(os.Stdout, "Completed: %s\n", m.CompletedAt)
	}
	fmt.Fprintln(os.Stdout)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TASK\tSTATUS\tDURATION\tMODEL\tCOST")

	for _, t := range m.Tasks {
		id := t.TaskID
		if id == "" {
			id = "(root)"
		}
		dur := ""
		if t.DurationMS > 0 {
			dur = fmt.Sprintf("%dms", t.DurationMS)
		}
		cost := ""
		if t.CostUSD > 0 {
			cost = fmt.Sprintf("$%.4f", t.CostUSD)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", id, t.Status, dur, t.Model, cost)
	}

	return w.Flush()
}
