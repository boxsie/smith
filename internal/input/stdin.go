package input

import (
	"fmt"
	"io"
	"os"
	"sort"
)

// ReadStdin checks if stdin is piped (not a terminal) and reads its contents.
// If stdin is piped, it returns the contents as a string and true.
// If stdin is a terminal, it returns empty string and false.
func ReadStdin() (string, bool, error) {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return "", false, fmt.Errorf("stat stdin: %w", err)
	}

	if fi.Mode()&os.ModeCharDevice != 0 {
		// stdin is a terminal
		return "", false, nil
	}

	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", false, fmt.Errorf("read stdin: %w", err)
	}

	return string(data), true, nil
}

// InjectStdin adds a stdin entry to the entries list.
// Returns an error if an entry named "stdin" already exists.
func InjectStdin(entries []Entry, stdinContent string) ([]Entry, error) {
	for _, e := range entries {
		if e.Name == "stdin" {
			return nil, fmt.Errorf("cannot use piped stdin with explicit --input stdin=...; use one or the other")
		}
	}
	result := append(entries, Entry{Name: "stdin", Value: stdinContent})
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result, nil
}
