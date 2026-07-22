package metrics

import (
	"strings"
	"testing"
	"time"
)

func TestTwentyCallHistogramRender(t *testing.T) {
	r := New()
	r.ObserveTwentyCall("GET", "people", 80*time.Millisecond)  // <=0.1
	r.ObserveTwentyCall("GET", "people", 300*time.Millisecond) // <=0.5
	r.ObserveTwentyCall("POST", "tasks", 3*time.Second)        // <=5
	r.ObserveTwentyCall("POST", "tasks", 30*time.Second)       // +Inf only

	out := r.Render()
	if !strings.Contains(out, "# TYPE crm_sync_twenty_call_duration_seconds histogram") {
		t.Fatal("missing TYPE line")
	}
	// Cumulative buckets for GET|people: le=0.05 ->0, le=0.1 ->1, le=0.25 ->1, le=0.5 ->2 ... +Inf ->2
	expect := []string{
		`crm_sync_twenty_call_duration_seconds_bucket{method="GET",path_class="people",le="0.05"} 0`,
		`crm_sync_twenty_call_duration_seconds_bucket{method="GET",path_class="people",le="0.1"} 1`,
		`crm_sync_twenty_call_duration_seconds_bucket{method="GET",path_class="people",le="0.25"} 1`,
		`crm_sync_twenty_call_duration_seconds_bucket{method="GET",path_class="people",le="0.5"} 2`,
		`crm_sync_twenty_call_duration_seconds_bucket{method="GET",path_class="people",le="+Inf"} 2`,
		`crm_sync_twenty_call_duration_seconds_count{method="GET",path_class="people"} 2`,
		`crm_sync_twenty_call_duration_seconds_bucket{method="POST",path_class="tasks",le="5"} 1`,
		`crm_sync_twenty_call_duration_seconds_bucket{method="POST",path_class="tasks",le="10"} 1`,
		`crm_sync_twenty_call_duration_seconds_bucket{method="POST",path_class="tasks",le="+Inf"} 2`,
		`crm_sync_twenty_call_duration_seconds_count{method="POST",path_class="tasks"} 2`,
	}
	for _, e := range expect {
		if !strings.Contains(out, e) {
			t.Errorf("render missing %q\n---\n%s", e, out)
		}
	}
	// sum for GET|people = 0.38
	if !strings.Contains(out, `crm_sync_twenty_call_duration_seconds_sum{method="GET",path_class="people"} 0.38`) {
		t.Errorf("bad sum line\n%s", out)
	}
}

func TestCountersStillRender(t *testing.T) {
	r := New()
	r.Inc("events_processed.x")
	r.Observe("event_handle.x", time.Second)
	out := r.Render()
	if !strings.Contains(out, `crm_sync_counter{name="events_processed.x"} 1`) {
		t.Errorf("counter missing\n%s", out)
	}
	if !strings.Contains(out, `crm_sync_latency_seconds_count{op="event_handle.x"} 1`) {
		t.Errorf("latency summary missing\n%s", out)
	}
}
