package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/boxsie/smith/internal/config"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var configCmd = &cobra.Command{
	Use:           "config",
	Short:         "Configure Smith credentials",
	RunE:          runConfig,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	rootCmd.AddCommand(configCmd)
}

func runConfig(cmd *cobra.Command, args []string) error {
	stdin, _ := cmd.InOrStdin().(*os.File)
	if stdin == nil {
		stdin = os.Stdin
	}
	stdout, _ := cmd.OutOrStdout().(*os.File)
	if stdout == nil {
		stdout = os.Stdout
	}
	return doConfig(stdin, stdout, int(stdin.Fd()))
}

// doConfig is the testable core of the config command.
// fd is the file descriptor for stdin (used for TTY detection).
// Pass fd < 0 to force non-TTY (piped) mode.
func doConfig(stdin *os.File, stdout *os.File, fd int) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	scanner := bufio.NewScanner(stdin)

	if cfg.AnthropicAPIKey != "" {
		fmt.Fprintf(stdout, "Current API key: %s\n", config.MaskKey(cfg.AnthropicAPIKey))
		fmt.Fprint(stdout, "Replace? [y/N] ")

		if !scanner.Scan() {
			return nil
		}
		answer := strings.TrimSpace(scanner.Text())
		if !strings.EqualFold(answer, "y") && !strings.EqualFold(answer, "yes") {
			return nil
		}
	}

	var key string
	isTTY := fd >= 0 && term.IsTerminal(fd)
	if isTTY {
		fmt.Fprint(stdout, "Anthropic API key: ")
		raw, err := term.ReadPassword(fd)
		fmt.Fprintln(stdout) // newline after hidden input
		if err != nil {
			return fmt.Errorf("read API key: %w", err)
		}
		key = string(raw)
	} else {
		if !scanner.Scan() {
			return fmt.Errorf("no API key provided")
		}
		key = scanner.Text()
	}

	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("API key cannot be empty")
	}

	cfg.AnthropicAPIKey = key
	if err := config.Save(cfg); err != nil {
		return err
	}

	path, _ := config.Path()
	fmt.Fprintf(stdout, "Saved to %s\n", path)
	return nil
}
