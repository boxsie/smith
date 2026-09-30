package canvas

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestInstrumentJournalProjections(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is required for instrument UI tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", "--test", "testdata/instrument_test.mjs", "testdata/theme_test.mjs")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("instrument UI tests: %v\n%s", err, output)
	}
}
