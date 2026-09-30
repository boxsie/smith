package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/boxsie/smith/internal/harness"
	"github.com/boxsie/smith/internal/projects"
	"github.com/spf13/cobra"
)

var harnessCmd = &cobra.Command{
	Use:   "harness",
	Short: "Run and recover the canonical ticket harness",
}

var harnessRunCmd = &cobra.Command{
	Use:           "run <patch>",
	Short:         "Start the canonical ticket harness and return at its human gate",
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := trackHarnessRoot(args[0])
		if err != nil {
			return err
		}
		driver, err := commandHarnessDriver(cmd)
		if err != nil {
			return err
		}
		project, _ := cmd.Flags().GetString("project")
		ticket, _ := cmd.Flags().GetString("ticket")
		phase, _ := cmd.Flags().GetString("phase")
		claudeModel, _ := cmd.Flags().GetString("claude-model")
		codexModel, _ := cmd.Flags().GetString("codex-model")
		reviewModel, _ := cmd.Flags().GetString("review-model")
		researchRoute, _ := cmd.Flags().GetString("research-route")
		reviewRoute, _ := cmd.Flags().GetString("review-route")
		memoryContext, _ := cmd.Flags().GetStringSlice("memory-context")
		memories, _ := cmd.Flags().GetStringSlice("memory")
		proveRecovery, _ := cmd.Flags().GetBool("prove-recovery")
		result, err := driver.Run(cmd.Context(), harness.RunOptions{
			Root: root, ProjectSlug: project, TicketID: ticket, PhaseID: phase,
			ClaudeModel: claudeModel, CodexModel: codexModel, ReviewModel: reviewModel,
			ResearchRoute: researchRoute, ReviewRoute: reviewRoute,
			MemoryContext: memoryContext, Memories: memories, ProveRecovery: proveRecovery,
		})
		if err != nil {
			return err
		}
		return writeHarnessResult(cmd, result)
	},
}

var harnessResumeCmd = &cobra.Command{
	Use:           "resume <patch>",
	Short:         "Recover an interrupted harness run and return at its next gate or terminal",
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := trackHarnessRoot(args[0])
		if err != nil {
			return err
		}
		driver, err := commandHarnessDriver(cmd)
		if err != nil {
			return err
		}
		project, _ := cmd.Flags().GetString("project")
		runID, _ := cmd.Flags().GetString("run")
		result, err := driver.Resume(cmd.Context(), harness.ContinueOptions{Root: root, ProjectSlug: project, RunID: runID})
		if err != nil {
			return err
		}
		return writeHarnessResult(cmd, result)
	},
}

var harnessDecideCmd = &cobra.Command{
	Use:           "decide <patch>",
	Short:         "Resolve a pending harness gate and continue the durable run",
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := trackHarnessRoot(args[0])
		if err != nil {
			return err
		}
		approve, _ := cmd.Flags().GetBool("approve")
		reject, _ := cmd.Flags().GetBool("reject")
		if approve == reject {
			return fmt.Errorf("exactly one of --approve or --reject is required")
		}
		driver, err := commandHarnessDriver(cmd)
		if err != nil {
			return err
		}
		project, _ := cmd.Flags().GetString("project")
		runID, _ := cmd.Flags().GetString("run")
		requestID, _ := cmd.Flags().GetString("request")
		reason, _ := cmd.Flags().GetString("reason")
		result, err := driver.Decide(cmd.Context(), harness.DecisionOptions{
			ContinueOptions: harness.ContinueOptions{Root: root, ProjectSlug: project, RunID: runID},
			RequestID:       requestID, Approved: approve, Reason: reason,
		})
		if err != nil {
			return err
		}
		return writeHarnessResult(cmd, result)
	},
}

func init() {
	harnessRunCmd.Flags().String("project", "", "tickets_please project slug")
	harnessRunCmd.Flags().String("ticket", "", "ticket id or project shortcode")
	harnessRunCmd.Flags().String("phase", "", "optional phase id or slug")
	harnessRunCmd.Flags().String("claude-model", "claude-fable-5-1", "Claude model for intake, planning, and ticket closure")
	harnessRunCmd.Flags().String("codex-model", "gpt-5.6-sol", "Codex model for workspace work")
	harnessRunCmd.Flags().String("review-model", "claude-opus-5", "Claude model for final review")
	harnessRunCmd.Flags().String("research-route", "direct", "research route: direct or grok")
	harnessRunCmd.Flags().String("review-route", "fable", "review route: fable or grok")
	harnessRunCmd.Flags().StringSlice("memory-context", nil, "memory context selector (repeatable or comma-separated)")
	harnessRunCmd.Flags().StringSlice("memory", nil, "explicit memory name (repeatable or comma-separated)")
	harnessRunCmd.Flags().Bool("prove-recovery", false, "restart the Smith controller during work and deterministic checks")
	_ = harnessRunCmd.MarkFlagRequired("project")
	_ = harnessRunCmd.MarkFlagRequired("ticket")

	for _, command := range []*cobra.Command{harnessResumeCmd, harnessDecideCmd} {
		command.Flags().String("project", "", "tickets_please project slug")
		command.Flags().String("run", "", "durable patch run id")
		_ = command.MarkFlagRequired("project")
		_ = command.MarkFlagRequired("run")
	}
	harnessDecideCmd.Flags().String("request", "", "pending gate request id")
	harnessDecideCmd.Flags().String("reason", "", "explicit human decision reason")
	harnessDecideCmd.Flags().Bool("approve", false, "approve the pending gate")
	harnessDecideCmd.Flags().Bool("reject", false, "reject the pending gate")
	_ = harnessDecideCmd.MarkFlagRequired("request")
	_ = harnessDecideCmd.MarkFlagRequired("reason")

	harnessCmd.AddCommand(harnessRunCmd, harnessResumeCmd, harnessDecideCmd)
	rootCmd.AddCommand(harnessCmd)
}

func commandHarnessDriver(cmd *cobra.Command) (*harness.Driver, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve smith executable: %w", err)
	}
	return &harness.Driver{Connect: harness.CommandConnector(executable, nil), Log: cmd.ErrOrStderr()}, nil
}

func trackHarnessRoot(value string) (string, error) {
	root, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	if err := projects.Track(root); err != nil {
		return "", fmt.Errorf("track harness patch: %w", err)
	}
	return root, nil
}

func writeHarnessResult(cmd *cobra.Command, result harness.Result) error {
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}
