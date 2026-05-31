package helps

import "testing"

// Real CodeWhisperer frame payloads captured from a live Kiro generate response.
const (
	kiroMeteringPayload     = `{"unit":"credit","unitPlural":"credits","usage":0.029486151359867332}`
	kiroContextUsagePayload = `{"contextUsagePercentage":0.6959999799728394}`
)

// TestParseKiroCacheObservability verifies the non-stream assembler path extracts credit cost
// and context usage from a full EventStream body, ignoring text/tool frames.
func TestParseKiroCacheObservability(t *testing.T) {
	var body []byte
	body = append(body, buildKiroFrame("assistantResponseEvent", []byte(`{"content":"PROBE_OK"}`))...)
	body = append(body, buildKiroFrame("contextUsageEvent", []byte(kiroContextUsagePayload))...)
	body = append(body, buildKiroFrame("meteringEvent", []byte(kiroMeteringPayload))...)

	obs := ParseKiroCacheObservability(body)
	if obs.Credits < 0.0294 || obs.Credits > 0.0295 {
		t.Fatalf("Credits = %v, want ~0.029486", obs.Credits)
	}
	if obs.ContextUsagePercent < 0.695 || obs.ContextUsagePercent > 0.696 {
		t.Fatalf("ContextUsagePercent = %v, want ~0.696", obs.ContextUsagePercent)
	}
}

// TestParseKiroCacheObservability_NoCacheFrames confirms a zero value when the body carries
// only content frames (no metering/contextUsage).
func TestParseKiroCacheObservability_NoCacheFrames(t *testing.T) {
	body := buildKiroFrame("assistantResponseEvent", []byte(`{"content":"hi"}`))
	obs := ParseKiroCacheObservability(body)
	if obs.Credits != 0 || obs.ContextUsagePercent != 0 {
		t.Fatalf("expected zero observability, got %+v", obs)
	}
}

// TestKiroDecoderCacheObservability verifies the streaming OpenAI decoder accumulates the
// credit/context signal across frames delivered in separate Decode calls (split reads).
func TestKiroDecoderCacheObservability(t *testing.T) {
	dec := NewKiroEventStreamDecoder("claude-sonnet-4.6", 0, nil)
	dec.Decode(buildKiroFrame("assistantResponseEvent", []byte(`{"content":"ok"}`)))
	dec.Decode(buildKiroFrame("contextUsageEvent", []byte(kiroContextUsagePayload)))
	dec.Decode(buildKiroFrame("meteringEvent", []byte(kiroMeteringPayload)))

	obs := dec.CacheObservability()
	if obs.Credits < 0.0294 || obs.Credits > 0.0295 {
		t.Fatalf("decoder Credits = %v, want ~0.029486", obs.Credits)
	}
	if obs.ContextUsagePercent < 0.695 || obs.ContextUsagePercent > 0.696 {
		t.Fatalf("decoder ContextUsagePercent = %v, want ~0.696", obs.ContextUsagePercent)
	}
}

// TestKiroClaudeEncoderCacheObservability verifies the streaming Anthropic encoder accumulates
// the credit/context signal from metering/contextUsage frames.
func TestKiroClaudeEncoderCacheObservability(t *testing.T) {
	enc := NewKiroClaudeStreamEncoder("claude-sonnet-4.6", 0, nil)
	enc.Encode(buildKiroFrame("assistantResponseEvent", []byte(`{"content":"ok"}`)))
	enc.Encode(buildKiroFrame("meteringEvent", []byte(kiroMeteringPayload)))
	enc.Encode(buildKiroFrame("contextUsageEvent", []byte(kiroContextUsagePayload)))

	obs := enc.CacheObservability()
	if obs.Credits < 0.0294 || obs.Credits > 0.0295 {
		t.Fatalf("encoder Credits = %v, want ~0.029486", obs.Credits)
	}
	if obs.ContextUsagePercent < 0.695 || obs.ContextUsagePercent > 0.696 {
		t.Fatalf("encoder ContextUsagePercent = %v, want ~0.696", obs.ContextUsagePercent)
	}
}
