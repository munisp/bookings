// Package metrics is a tiny dependency-free Prometheus text-format registry:
// counters per event type and Twenty call latency summaries (SPEC-CRM §B).
package metrics

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Registry collects counters, latency summaries and the Twenty-call
// histogram.
type Registry struct {
	mu         sync.Mutex
	counters   map[string]int64
	latency    map[string]*latStat
	histograms map[string]*histogram
}

type latStat struct {
	count int64
	sum   float64 // seconds
	max   float64
}

// New builds an empty registry.
func New() *Registry {
	return &Registry{
		counters:   map[string]int64{},
		latency:    map[string]*latStat{},
		histograms: map[string]*histogram{},
	}
}

// histogram is a fixed-bucket cumulative histogram.
type histogram struct {
	buckets []float64 // upper bounds, sorted, without +Inf
	counts  []int64   // len(buckets)+1; counts[i] = observations <= buckets[i] (cumulative)
	sum     float64
	count   int64
}

// twentyCallBuckets: Twenty REST calls are local-network; sub-10s range.
var twentyCallBuckets = []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

func newHistogram(buckets []float64) *histogram {
	return &histogram{buckets: buckets, counts: make([]int64, len(buckets)+1)}
}

func (h *histogram) observe(sec float64) {
	h.count++
	h.sum += sec
	for i, b := range h.buckets {
		if sec <= b {
			h.counts[i]++
		}
	}
	h.counts[len(h.buckets)]++ // +Inf bucket
}

// ObserveTwentyCall records one Twenty REST call duration (seconds) in the
// crm_sync_twenty_call_duration_seconds histogram, labelled by HTTP method
// and path class (the Twenty object, e.g. "people", "tasks").
func (r *Registry) ObserveTwentyCall(method, pathClass string, d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := method + "|" + pathClass
	h := r.histograms[key]
	if h == nil {
		h = newHistogram(twentyCallBuckets)
		r.histograms[key] = h
	}
	h.observe(d.Seconds())
}

// Inc increments counter `name` by 1.
func (r *Registry) Inc(name string) { r.Add(name, 1) }

// Add increments counter `name` by delta.
func (r *Registry) Add(name string, delta int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counters[name] += delta
}

// Observe records one latency observation (seconds) under `name`.
func (r *Registry) Observe(name string, d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.latency[name]
	if s == nil {
		s = &latStat{}
		r.latency[name] = s
	}
	sec := d.Seconds()
	s.count++
	s.sum += sec
	if sec > s.max {
		s.max = sec
	}
}

// Render emits the Prometheus text exposition format.
func (r *Registry) Render() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	keys := make([]string, 0, len(r.counters))
	for k := range r.counters {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b.WriteString("# HELP crm_sync_counter Counters by name (events processed/failed/DLQ, webhook intakes).\n")
	b.WriteString("# TYPE crm_sync_counter counter\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "crm_sync_counter{name=%q} %d\n", k, r.counters[k])
	}
	lkeys := make([]string, 0, len(r.latency))
	for k := range r.latency {
		lkeys = append(lkeys, k)
	}
	sort.Strings(lkeys)
	b.WriteString("# HELP crm_sync_latency_seconds Latency summary in seconds by operation.\n")
	b.WriteString("# TYPE crm_sync_latency_seconds summary\n")
	for _, k := range lkeys {
		s := r.latency[k]
		fmt.Fprintf(&b, "crm_sync_latency_seconds_count{op=%q} %d\n", k, s.count)
		fmt.Fprintf(&b, "crm_sync_latency_seconds_sum{op=%q} %.6f\n", k, s.sum)
		fmt.Fprintf(&b, "crm_sync_latency_seconds_max{op=%q} %.6f\n", k, s.max)
	}
	hkeys := make([]string, 0, len(r.histograms))
	for k := range r.histograms {
		hkeys = append(hkeys, k)
	}
	sort.Strings(hkeys)
	b.WriteString("# HELP crm_sync_twenty_call_duration_seconds Twenty REST call duration histogram.\n")
	b.WriteString("# TYPE crm_sync_twenty_call_duration_seconds histogram\n")
	for _, k := range hkeys {
		method, pathClass, _ := strings.Cut(k, "|")
		h := r.histograms[k]
		for i, bound := range h.buckets {
			fmt.Fprintf(&b, "crm_sync_twenty_call_duration_seconds_bucket{method=%q,path_class=%q,le=%q} %d\n",
				method, pathClass, strconv.FormatFloat(bound, 'g', -1, 64), h.counts[i])
		}
		fmt.Fprintf(&b, "crm_sync_twenty_call_duration_seconds_bucket{method=%q,path_class=%q,le=\"+Inf\"} %d\n",
			method, pathClass, h.counts[len(h.buckets)])
		fmt.Fprintf(&b, "crm_sync_twenty_call_duration_seconds_sum{method=%q,path_class=%q} %.6f\n",
			method, pathClass, h.sum)
		fmt.Fprintf(&b, "crm_sync_twenty_call_duration_seconds_count{method=%q,path_class=%q} %d\n",
			method, pathClass, h.count)
	}
	return b.String()
}
