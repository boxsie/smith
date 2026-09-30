package runtime

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestGrokRuntimeLiveSterileSubscription(t *testing.T) {
	if os.Getenv("SMITH_LIVE_GROK") != "1" {
		t.Skip("set SMITH_LIVE_GROK=1 to spend a Grok subscription call")
	}
	invocation := Invocation{
		Messages: []Message{{Role: "user", Text: "Return answer = 6 * 7."}},
		Persona:  "You are a sterile calculation runtime. Follow the output schema.",
		Output: OutputContract{
			Type:   "json",
			Schema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"answer":{"type":"integer"}},"required":["answer"]}`),
		},
		Runtime:      GrokRuntimeName,
		Model:        "grok-4.5",
		Profile:      DefaultProfile,
		Workspace:    WorkspacePolicy{Access: WorkspaceNone, Isolation: "ephemeral", Granted: true},
		Capabilities: CapabilityPolicy{Profile: CapabilityReason},
		Session:      SessionPolicy{Mode: SessionFresh},
		Limits:       LimitPolicy{MaxTurns: 1},
		Timeout:      time.Minute,
	}
	sink := &grokSink{}
	if err := NewGrokRuntime().Invoke(context.Background(), invocation, sink); err != nil {
		t.Fatalf("invoke live Grok: %v", err)
	}
	if sink.result == nil || string(sink.result.JSON) != `{"answer":42}` {
		t.Fatalf("result = %#v", sink.result)
	}
	if sink.result.Provenance.SessionID == "" || sink.result.Provenance.CanonicalModel == "" ||
		sink.result.Provenance.BillingBasis != BillingReportedListEstimate {
		t.Fatalf("provenance = %#v", sink.result.Provenance)
	}
	if sink.result.Usage.InputTokens == nil || sink.result.Usage.CachedInputTokens == nil || sink.result.Usage.OutputTokens == nil {
		t.Fatalf("Grok did not report complete usage: %#v", sink.result.Usage)
	}
	t.Logf("sterile Grok usage: input=%d cached=%d output=%d duration=%v",
		valueOrZero(sink.result.Usage.InputTokens), valueOrZero(sink.result.Usage.CachedInputTokens),
		valueOrZero(sink.result.Usage.OutputTokens), sink.result.Duration)
	assertLiveProcessMeasurements(t, sink.events)
}
