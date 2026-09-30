package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type externalRuntimeFunc func(context.Context, Invocation, InvocationSink) error

func (f externalRuntimeFunc) Invoke(ctx context.Context, invocation Invocation, sink InvocationSink) error {
	return f(ctx, invocation, sink)
}

func TestNewRecordKeepsMissingMeasurementsNullAndRawNamespaced(t *testing.T) {
	start := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	end := start.Add(1500 * time.Millisecond)
	estimate := 0.42
	result := &ExternalResult{
		Artifacts: []Artifact{{Path: "artifact.txt", SHA256: "abc123"}},
		Usage: Usage{
			InputTokens: intPointer(10),
			Raw:         json.RawMessage(`{"input_tokens":10,"future_cache_kind":7}`),
		},
		Provenance: Provenance{
			Adapter: "future", AdapterVersion: "2", ProtocolVersion: "future-json/3",
			RequestedModel: "requested", BillingBasis: BillingReportedListEstimate,
			ReportedCostUSD: &estimate, Raw: json.RawMessage(`{"future_terminal_field":true}`),
		},
	}
	profile := ResolvedProfile{Name: CapabilityReason, Session: SessionPolicy{Mode: SessionFresh}}
	record := NewRecord(Invocation{Runtime: "future", Model: "requested"}, profile, result, "task", start, end, OutputValidation{Type: "json_schema", Valid: true})

	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range [][]byte{
		[]byte(`"canonical_model":null`), []byte(`"cli_version":null`),
		[]byte(`"output_tokens":null`), []byte(`"cached_input_tokens":null`), []byte(`"turns":null`),
		[]byte(`"future":{"input_tokens":10,"future_cache_kind":7}`),
	} {
		if !bytes.Contains(data, want) {
			t.Fatalf("record %s missing %s", data, want)
		}
	}
	if record.Phase != "task" || record.DurationMS != 1500 || record.Billing.AmountUSD == nil || *record.Billing.AmountUSD != estimate || record.Billing.Basis != BillingReportedListEstimate {
		t.Fatalf("record = %#v", record)
	}
	if len(record.Artifacts) != 1 || record.Artifacts[0].SHA256 != "abc123" || !record.OutputValidation.Valid {
		t.Fatalf("record = %#v", record)
	}
}

func intPointer(value int) *int { return &value }

func TestExternalResultCanonicalKeepsProtocolMetadataOutOfPayload(t *testing.T) {
	result := &ExternalResult{
		JSON: json.RawMessage(`{"answer":42}`),
		Provenance: Provenance{
			Adapter:        "fake",
			CanonicalModel: "frontier-1",
			SessionID:      "session-secret-envelope",
		},
	}
	got, err := result.Canonical("json")
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	if got != `{"answer":42}` {
		t.Fatalf("canonical output = %q", got)
	}
	if string(result.JSON) == "" || result.Provenance.SessionID == "" {
		t.Fatal("canonical conversion mutated the structured result")
	}
}

func TestExternalResultCanonicalRejectsMalformedOutput(t *testing.T) {
	_, err := (&ExternalResult{JSON: json.RawMessage(`{"broken"`)}).Canonical("json")
	if !errors.Is(err, ErrMalformedExternalResult) {
		t.Fatalf("error = %v, want ErrMalformedExternalResult", err)
	}
}

func TestExternalFactoryIsIndependentFromProviderFactory(t *testing.T) {
	want := externalRuntimeFunc(func(ctx context.Context, _ Invocation, sink InvocationSink) error {
		return sink.Complete(ctx, &ExternalResult{Text: "ok"})
	})
	factory := &ExternalFactory{Runtimes: map[string]ExternalRuntime{"codex": want}}
	got, err := factory.Resolve("codex")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got == nil {
		t.Fatal("resolved runtime is nil")
	}
	if _, err := factory.Resolve(ProviderRuntime); err == nil {
		t.Fatal("provider selector must not resolve through ExternalFactory")
	}
}
