package canvas

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestCanvasGateUIStateAndHandlers(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is required for gate UI state tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", "--test", "testdata/gates_test.mjs")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("gate UI tests: %v\n%s", err, output)
	}
}
