package executor

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/boxsie/smith/internal/run"
	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/tools"
)

// ErrUnwhitelistedTool is returned when the LLM requests a tool not in tools.md.
var ErrUnwhitelistedTool = fmt.Errorf("tool call not permitted by tools.md")

// MaxToolRounds limits the number of tool-call round trips.
const MaxToolRounds = 20

// MaxToolConcurrency limits how many tool calls execute in parallel.
// Keeps web-facing tools from hammering a single origin and triggering rate limits.
const MaxToolConcurrency = 2

// ToolStagger is the delay between launching consecutive tool call goroutines.
// A small stagger prevents identical-timestamp bursts that trigger bot detection.
const ToolStagger = 250 * time.Millisecond

// ExecuteWithTools runs a single task through the provider, handling tool call loops.
// Returns the final accumulated response or an error.
//
// allowedTools is the set of tool IDs from the task's tools.md (nil if no tools declared).
// adapter executes the actual tool calls.
// logger is optional; when non-nil, every tool invocation is logged.
// taskID and phase are recorded in tool history entries for attribution.
func ExecuteWithTools(
	ctx context.Context,
	provider runtime.Provider,
	req *runtime.Request,
	allowedTools map[string]bool,
	adapter tools.Adapter,
	logger *tools.HistoryLogger,
	taskID string,
	phase string,
) (*runtime.Response, error) {
	return ExecuteWithToolsObserved(ctx, provider, req, allowedTools, adapter, logger, taskID, phase, "", nil)
}

// ExecuteWithToolsObserved adds durable provider/tool lifecycle observation to
// ExecuteWithTools without changing the legacy helper's contract.
func ExecuteWithToolsObserved(
	ctx context.Context,
	provider runtime.Provider,
	req *runtime.Request,
	allowedTools map[string]bool,
	adapter tools.Adapter,
	logger *tools.HistoryLogger,
	taskID string,
	phase string,
	invocationID string,
	observer func(run.Event) error,
) (*runtime.Response, error) {
	var accumulated runtime.Response

	for round := 0; round < MaxToolRounds; round++ {
		providerInvocationID := fmt.Sprintf("%s/provider-%02d", invocationID, round+1)
		if observer != nil {
			if err := observer(run.Event{Type: run.EventProviderStarted, InvocationID: providerInvocationID, Model: req.Model}); err != nil {
				return nil, fmt.Errorf("record provider start: %w", err)
			}
		}
		providerStarted := time.Now()
		resp, err := provider.Execute(ctx, req)
		if err != nil {
			if observer != nil {
				_ = observer(run.Event{Type: run.EventProviderFailed, InvocationID: providerInvocationID, Model: req.Model, DurationMS: time.Since(providerStarted).Milliseconds(), Error: err.Error()})
			}
			return nil, fmt.Errorf("provider call (round %d): %w", round, err)
		}
		if observer != nil {
			if err := observer(run.Event{
				Type:         run.EventProviderCompleted,
				InvocationID: providerInvocationID,
				Model:        req.Model,
				DurationMS:   time.Since(providerStarted).Milliseconds(),
				TokensIn:     resp.TokensIn,
				TokensOut:    resp.TokensOut,
				CostUSD:      resp.CostUSD,
			}); err != nil {
				return nil, fmt.Errorf("record provider completion: %w", err)
			}
		}

		accumulated.TokensIn += resp.TokensIn
		accumulated.TokensOut += resp.TokensOut
		accumulated.CostUSD += resp.CostUSD
		accumulated.Duration += resp.Duration

		// No tool calls → final text response
		if len(resp.ToolCalls) == 0 {
			accumulated.Content = resp.Content
			return &accumulated, nil
		}

		// Validate all tool calls before executing any.
		for _, tc := range resp.ToolCalls {
			if !allowedTools[tc.ToolID] {
				return nil, fmt.Errorf("%w: %q", ErrUnwhitelistedTool, tc.ToolID)
			}
			if adapter == nil {
				return nil, fmt.Errorf("tool %q requested but no tool adapter configured", tc.ToolID)
			}
		}

		// Execute tool calls concurrently, bounded by MaxToolConcurrency.
		type toolSlot struct {
			start    time.Time
			duration time.Duration
			result   runtime.ToolResult
		}
		slots := make([]toolSlot, len(resp.ToolCalls))
		toolInvocationIDs := make([]string, len(resp.ToolCalls))
		for i, tc := range resp.ToolCalls {
			toolInvocationIDs[i] = fmt.Sprintf("%s/tool/%s", providerInvocationID, tc.ID)
			if observer != nil {
				if err := observer(run.Event{
					Type:         run.EventToolStarted,
					InvocationID: toolInvocationIDs[i],
					ToolCallID:   tc.ID,
					ToolID:       tc.ToolID,
				}); err != nil {
					return nil, fmt.Errorf("record tool start: %w", err)
				}
			}
		}
		sem := make(chan struct{}, MaxToolConcurrency)
		var wg sync.WaitGroup
		wg.Add(len(resp.ToolCalls))
		for i, tc := range resp.ToolCalls {
			if i > 0 {
				time.Sleep(ToolStagger)
			}
			go func(i int, tc runtime.ToolCall) {
				defer wg.Done()
				sem <- struct{}{}        // acquire
				defer func() { <-sem }() // release
				slots[i].start = time.Now()
				toolCtx := run.WithCausalParent(ctx, toolInvocationIDs[i])
				output, execErr := adapter.Execute(toolCtx, tc.ToolID, tc.Input)
				slots[i].duration = time.Since(slots[i].start)
				slots[i].result = runtime.ToolResult{
					ToolCallID: tc.ID,
					ToolID:     tc.ToolID,
					IsError:    execErr != nil,
				}
				if execErr != nil {
					slots[i].result.Output = json.RawMessage(fmt.Sprintf(`{"error":%q}`, execErr.Error()))
				} else {
					slots[i].result.Output = output
				}
			}(i, tc)
		}
		wg.Wait()

		// Append messages in original order and log.
		for i, tc := range resp.ToolCalls {
			if observer != nil {
				eventType := run.EventToolCompleted
				if slots[i].result.IsError {
					eventType = run.EventToolFailed
				}
				event := run.Event{
					Type:         eventType,
					InvocationID: toolInvocationIDs[i],
					ToolCallID:   tc.ID,
					ToolID:       tc.ToolID,
					DurationMS:   slots[i].duration.Milliseconds(),
				}
				if slots[i].result.IsError {
					event.Error = string(slots[i].result.Output)
				}
				if err := observer(event); err != nil {
					return nil, fmt.Errorf("record tool completion: %w", err)
				}
			}
			req.Messages = append(req.Messages, runtime.Message{
				Role:     "assistant",
				ToolCall: &runtime.ToolCall{ID: tc.ID, ToolID: tc.ToolID, Input: tc.Input},
			})
			if logger != nil {
				inputHash := fmt.Sprintf("%x", sha256.Sum256(tc.Input))
				_ = logger.Log(tools.HistoryEntry{
					Timestamp:  slots[i].start,
					ToolID:     tc.ToolID,
					InputHash:  inputHash,
					Output:     slots[i].result.Output,
					DurationMs: slots[i].duration.Milliseconds(),
					IsError:    slots[i].result.IsError,
					RunID:      logger.RunID,
					TaskID:     taskID,
					Phase:      phase,
				})
			}
			req.Messages = append(req.Messages, runtime.Message{
				Role:       "tool",
				ToolResult: &slots[i].result,
			})
		}
	}

	return nil, fmt.Errorf("tool call loop exceeded %d rounds", MaxToolRounds)
}
