package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/boxsie/smith/internal/plan"
	"github.com/boxsie/smith/internal/service"
	"github.com/spf13/cobra"
)

var planCmd = &cobra.Command{
	Use:           "plan <target-path> <goal>",
	Short:         "Generate a proposal for a task tree from a goal",
	Args:          cobra.ExactArgs(2),
	RunE:          runPlan,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	planCmd.Flags().String("model", "", "override the planner model")
	planCmd.Flags().Float64("max-cost-usd", 0, "cost ceiling for planning")
	rootCmd.AddCommand(planCmd)
}

func runPlan(cmd *cobra.Command, args []string) error {
	modelOverride, _ := cmd.Flags().GetString("model")
	maxCostUSD, _ := cmd.Flags().GetFloat64("max-cost-usd")

	result, err := smithService.Plan(cmd.Context(), service.PlanRequest{
		TargetDir:     args[0],
		Goal:          args[1],
		ModelOverride: modelOverride,
		MaxCostUSD:    maxCostUSD,
	})
	if err != nil {
		var targetErr *plan.ErrInvalidTarget
		if errors.As(err, &targetErr) {
			return &ExitError{Code: 2, Err: err}
		}
		return &ExitError{Code: 1, Err: err}
	}

	for _, warning := range result.Warnings {
		fmt.Fprintf(os.Stderr, "warning: project tracking: %v\n", warning)
	}

	fmt.Fprintf(os.Stderr, "Proposal created: %s\n", result.Plan.ProposalDir)
	fmt.Fprintf(os.Stderr, "\n%s\n", result.Plan.Proposal.Summary)
	return nil
}
