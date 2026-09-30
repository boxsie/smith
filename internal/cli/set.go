package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/boxsie/smith/internal/defaults"
	"github.com/boxsie/smith/internal/input"
	"github.com/spf13/cobra"
)

var setCmd = &cobra.Command{
	Use:   "set <path> [name=value ...]",
	Short: "Set persistent default inputs for a smith app",
	Long: `Set persistent default inputs that are used on every run.
These are stored in .smith/defaults.json and can be overridden
by --input flags on smith run.

With no name=value arguments, lists current defaults.`,
	Args:          cobra.MinimumNArgs(1),
	RunE:          runSet,
	SilenceUsage:  true,
	SilenceErrors: true,
}

var unsetCmd = &cobra.Command{
	Use:   "unset <path> <name> [name ...]",
	Short: "Remove persistent default inputs from a smith app",
	Args:  cobra.MinimumNArgs(2),
	RunE:  runUnset,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	rootCmd.AddCommand(setCmd)
	rootCmd.AddCommand(unsetCmd)
}

func runSet(cmd *cobra.Command, args []string) error {
	absRoot, err := filepath.Abs(args[0])
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	// No key=value args — list current defaults.
	if len(args) == 1 {
		return listDefaults(absRoot)
	}

	// Validate names via the input package's rules.
	if _, err := input.Parse(args[1:]); err != nil {
		return err
	}

	defs, err := defaults.Load(absRoot)
	if err != nil {
		return err
	}
	if defs == nil {
		defs = make(map[string]string)
	}

	for _, raw := range args[1:] {
		idx := strings.Index(raw, "=")
		defs[raw[:idx]] = raw[idx+1:]
	}

	if err := defaults.Save(absRoot, defs); err != nil {
		return err
	}

	for _, raw := range args[1:] {
		fmt.Fprintln(os.Stdout, raw)
	}
	return nil
}

func runUnset(cmd *cobra.Command, args []string) error {
	absRoot, err := filepath.Abs(args[0])
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	defs, err := defaults.Load(absRoot)
	if err != nil {
		return err
	}
	if defs == nil {
		return nil
	}

	for _, name := range args[1:] {
		if _, ok := defs[name]; !ok {
			return fmt.Errorf("no default named %q", name)
		}
		delete(defs, name)
		fmt.Fprintf(os.Stdout, "removed %s\n", name)
	}

	return defaults.Save(absRoot, defs)
}

func listDefaults(absRoot string) error {
	defs, err := defaults.Load(absRoot)
	if err != nil {
		return err
	}
	if len(defs) == 0 {
		fmt.Fprintln(os.Stdout, "no defaults set")
		return nil
	}

	keys := make([]string, 0, len(defs))
	for k := range defs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tVALUE")
	for _, k := range keys {
		fmt.Fprintf(w, "%s\t%s\n", k, defs[k])
	}
	return w.Flush()
}
