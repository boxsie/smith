package cli

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/boxsie/smith/internal/input"
	"github.com/boxsie/smith/internal/service"
	"github.com/boxsie/smith/internal/tools"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var runCmd = &cobra.Command{
	Use:           "run <path>",
	Short:         "Execute a task tree",
	Args:          cobra.ExactArgs(1),
	RunE:          runRun,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	runCmd.Flags().Bool("dry", false, "validate and report execution plan without running")
	runCmd.Flags().Bool("no-cache", false, "ignore cached outputs, force all tasks to execute")
	runCmd.Flags().Bool("clear-cache", false, "delete all cached outputs before running")
	runCmd.Flags().StringArray("input", nil, "pass run input entry as name=value (repeatable)")
	runCmd.Flags().StringArray("scope", nil, "set tool scope parameter as key=value (repeatable)")
	runCmd.Flags().StringArray("writable-root", nil, "grant an external work profile access to a root (repeatable)")
	runCmd.Flags().Bool("allow-uncontained-development", false, "deliberately authorize the named uncontained_development execution profile")
	rootCmd.AddCommand(runCmd)
}

func runRun(cmd *cobra.Command, args []string) error {
	dry, _ := cmd.Flags().GetBool("dry")
	noCache, _ := cmd.Flags().GetBool("no-cache")
	clearCache, _ := cmd.Flags().GetBool("clear-cache")
	rawInput, _ := cmd.Flags().GetStringArray("input")
	rawScope, _ := cmd.Flags().GetStringArray("scope")
	writableRoots, _ := cmd.Flags().GetStringArray("writable-root")
	allowUncontainedDevelopment, _ := cmd.Flags().GetBool("allow-uncontained-development")

	// Determine output destinations based on stdout terminal status.
	stdoutIsTTY := term.IsTerminal(int(os.Stdout.Fd()))
	statusOut := os.Stdout
	if !stdoutIsTTY {
		statusOut = os.Stderr
	}

	// 0. Parse explicit --input and --scope flags (cheap, no I/O).
	runInput, err := input.Parse(rawInput)
	if err != nil {
		return err
	}
	scopeMap, err := tools.ParseScope(rawScope)
	if err != nil {
		return err
	}

	// Prepare validates before the adapter consumes stdin, preserving fail-fast
	// behaviour for potentially infinite pipes.
	// Validation does not depend on run input, so fail fast
	// before blocking on a potentially infinite stdin pipe.
	prepared, err := smithService.PrepareRun(service.PrepareRunRequest{
		AppRoot:                     args[0],
		Input:                       runInput,
		Scope:                       scopeMap,
		WritableRoots:               writableRoots,
		AllowUncontainedDevelopment: allowUncontainedDevelopment,
		NoCache:                     noCache,
		ClearCache:                  clearCache,
	})
	if err != nil {
		var validationErr *service.ValidationError
		if errors.As(err, &validationErr) {
			for _, e := range validationErr.Errors {
				fmt.Fprintf(os.Stderr, "error: %v\n", e)
			}
			return fmt.Errorf("validation failed")
		}
		return err
	}
	runInput = prepared.Input
	scopeMap = prepared.Scope

	// 1. Dry run — report plan and exit without consuming stdin.
	if dry {
		result := prepared.DryRun()
		hasScope := len(scopeMap) > 0
		w := tabwriter.NewWriter(statusOut, 0, 0, 2, ' ', 0)
		if hasScope {
			fmt.Fprintln(w, "LEVEL\tTASK\tMODEL\tPROFILE\tSESSION\tWORKSPACE\tCACHED\tDEPS\tSCOPE")
		} else {
			fmt.Fprintln(w, "LEVEL\tTASK\tMODEL\tPROFILE\tSESSION\tWORKSPACE\tCACHED\tDEPS")
		}
		for _, dt := range result.Tasks {
			id := dt.TaskID
			if id == "" {
				id = "(root)"
			}
			cachedStr := ""
			if dt.HasReturn {
				switch {
				case dt.TaskPhaseCached && dt.ReturnPhaseCached:
					cachedStr = "cached (both)"
				case dt.TaskPhaseCached:
					cachedStr = "cached (task only)"
				case dt.ReturnPhaseCached:
					cachedStr = "cached (return only)"
				}
			} else if dt.Cached {
				cachedStr = "cached (current)"
			}
			depNames := make([]string, len(dt.DepsOn))
			for i, d := range dt.DepsOn {
				if d == "" {
					depNames[i] = "(root)"
				} else {
					depNames[i] = d
				}
			}
			depsStr := strings.Join(depNames, ", ")
			profileName, sessionMode, workspaceAccess := "", "", ""
			if dt.ExecutionProfile != nil {
				profileName = dt.ExecutionProfile.Name
				sessionMode = dt.ExecutionProfile.Session.Mode
				workspaceAccess = dt.ExecutionProfile.Workspace.Access
			}
			if hasScope {
				scopeStr := formatScope(dt.Scope)
				fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", dt.Level, id, dt.Model, profileName, sessionMode, workspaceAccess, cachedStr, depsStr, scopeStr)
			} else {
				fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", dt.Level, id, dt.Model, profileName, sessionMode, workspaceAccess, cachedStr, depsStr)
			}
		}
		return w.Flush()
	}

	// 2. Read piped stdin as run input after preparation succeeds.
	stdinContent, isStdinPiped, err := input.ReadStdin()
	if err != nil {
		return err
	}
	if isStdinPiped {
		runInput, err = input.InjectStdin(runInput, stdinContent)
		if err != nil {
			return err
		}
	}

	// 3. Execute through the application service.
	fmt.Fprintf(statusOut, "run: %s\n", prepared.RunID())
	result, err := prepared.Execute(cmd.Context(), runInput)
	if err != nil {
		return err
	}
	for _, warning := range result.Warnings {
		fmt.Fprintf(os.Stderr, "warning: project tracking: %v\n", warning)
	}

	// 4. Report results (to statusOut — stderr when piped).
	w := tabwriter.NewWriter(statusOut, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TASK\tSTATUS\tDURATION")
	for _, tr := range result.Execution.Tasks {
		id := tr.TaskID
		if id == "" {
			id = "(root)"
		}
		dur := fmt.Sprintf("%dms", tr.Metrics.DurationMS)
		fmt.Fprintf(w, "%s\t%s\t%s\n", id, tr.Status, dur)
	}
	w.Flush()

	if !result.Execution.Success {
		for _, tr := range result.Execution.Tasks {
			if tr.Err != nil {
				id := tr.TaskID
				if id == "" {
					id = "(root)"
				}
				fmt.Fprintf(os.Stderr, "error: %s: %v\n", id, tr.Err)
			}
		}
		return fmt.Errorf("run failed")
	}

	// 5. Write canonical output to stdout when piped.
	if !stdoutIsTTY {
		fmt.Fprint(os.Stdout, result.Output)
	}

	return nil
}

// formatScope renders a scope map as "key=value, key2=value2" with sorted keys.
func formatScope(scope map[string]string) string {
	if len(scope) == 0 {
		return ""
	}
	keys := make([]string, 0, len(scope))
	for k := range scope {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + scope[k]
	}
	return strings.Join(parts, ", ")
}
