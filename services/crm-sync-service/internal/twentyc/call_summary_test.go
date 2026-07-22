package twentyc

import (
	"strings"
	"testing"

	"github.com/opendesk/crm-sync-service/internal/events"
)

func intPtr(v int) *int           { return &v }
func floatPtr(v float64) *float64 { return &v }

func TestCallSummaryNoteFullPayload(t *testing.T) {
	body := CallSummaryNote(events.CallQuality{
		DurationS:       95.24, // rounds to 95s
		TurnCount:       6,
		ToolCalls:       map[string]int{"lookup_appointment": 2, "book_appointment": 1},
		AvgLLMLatencyMs: intPtr(820),
		MaxLLMLatencyMs: intPtr(1400),
		SttCalls:        6,
		TtsCalls:        5,
		LLMFallbackUsed: true,
		Escalated:       true,
		ConfirmedPhone:  "+1555000111",
	})
	want := "📞 AI call summary — duration 95s, 6 turns, " +
		"tools: book_appointment×1, lookup_appointment×2, " +
		"avg LLM 820ms (max 1400ms), stt 6 calls, tts 5 calls, " +
		"escalated: yes, fallback used: yes"
	if body != want {
		t.Fatalf("CallSummaryNote =\n%q\nwant\n%q", body, want)
	}
}

func TestCallSummaryNoteFallbackFlagRendering(t *testing.T) {
	off := CallSummaryNote(events.CallQuality{DurationS: 10, TurnCount: 1})
	if !strings.Contains(off, "fallback used: no") || !strings.Contains(off, "escalated: no") {
		t.Errorf("false flags should render as 'no': %q", off)
	}
	on := CallSummaryNote(events.CallQuality{DurationS: 10, TurnCount: 1, LLMFallbackUsed: true, Escalated: true})
	if !strings.Contains(on, "fallback used: yes") || !strings.Contains(on, "escalated: yes") {
		t.Errorf("true flags should render as 'yes': %q", on)
	}
}

func TestCallSummaryNoteOmitsMissingOptionals(t *testing.T) {
	body := CallSummaryNote(events.CallQuality{
		DurationS: 12,
		// no tool calls, no LLM latency samples, no stt/tts, no sentiment
	})
	if strings.Contains(body, "tools:") {
		t.Errorf("tools segment should be omitted: %q", body)
	}
	if strings.Contains(body, "avg LLM") {
		t.Errorf("latency segment should be omitted: %q", body)
	}
	if strings.Contains(body, "stt ") || strings.Contains(body, "tts ") {
		t.Errorf("stt/tts segment should be omitted: %q", body)
	}
	if strings.Contains(body, "sentiment") {
		t.Errorf("sentiment must be omitted when nil: %q", body)
	}
	want := "📞 AI call summary — duration 12s, 0 turns, escalated: no, fallback used: no"
	if body != want {
		t.Fatalf("CallSummaryNote =\n%q\nwant\n%q", body, want)
	}
}

func TestCallSummaryNoteOptionalAvgSentiment(t *testing.T) {
	withSentiment := CallSummaryNote(events.CallQuality{
		DurationS: 30, TurnCount: 2, AvgSentiment: floatPtr(0.42),
	})
	if !strings.Contains(withSentiment, "avg sentiment 0.42") {
		t.Errorf("sentiment segment missing: %q", withSentiment)
	}
}

func TestCallSummaryNoteAvgWithoutMax(t *testing.T) {
	body := CallSummaryNote(events.CallQuality{
		DurationS: 5, TurnCount: 1, AvgLLMLatencyMs: intPtr(640),
	})
	if !strings.Contains(body, "avg LLM 640ms") || strings.Contains(body, "max") {
		t.Errorf("avg without max should render plainly: %q", body)
	}
}
