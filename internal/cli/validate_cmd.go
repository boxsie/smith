package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/boxsie/smith/internal/service"
	"github.com/spf13/cobra"
)

var validateCmd = &cobra.Command{
	Use:   "validate <path>",
	Short: "Validate a task tree against RFC 0001 rules",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		result, err := smithService.Validate(args[0])
		if err != nil {
			var validationErr *service.ValidationError
			if !errors.As(err, &validationErr) {
				return err
			}
			for _, e := range validationErr.Errors {
				fmt.Fprintf(os.Stderr, "error: %v\n", e)
			}
			msgs := make([]string, len(validationErr.Errors))
			for i, e := range validationErr.Errors {
				msgs[i] = e.Error()
			}
			return fmt.Errorf("validation failed: %s", strings.Join(msgs, "; "))
		}
		fmt.Println("valid")
		if result.Validation.ResolvedTools != nil && len(result.Validation.ResolvedTools.AppDefs) > 0 {
			fmt.Printf("tools: %d app, %d lib, %d built-in\n", result.AppTools, result.LibraryTools, result.BuiltinTools)
		}
		return nil
	},
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	rootCmd.AddCommand(validateCmd)
}
