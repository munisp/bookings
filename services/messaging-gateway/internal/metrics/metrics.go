// Package metrics implements the tiny dependency-free Prometheus counter
// registry used for the per-provider send counters. Counters are kept in
// process (the gateway is a single replica per deployment) and rendered in
// the Prometheus text exposition format on /metrics.
package metrics

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Counter is an atomic uint64 counter.
type Counter struct{ v atomic.Uint64 }

// Inc increments the counter by one.
func (c *Counter) Inc() { c.v.Add(1) }

// Registry stores counters keyed by (provider, result).
type Registry struct {
	mu sync.Mutex
	m  map[string]*Counter
}

// New builds an empty registry.
func New() *Registry { return &Registry{m: map[string]*Counter{}} }

// IncSend increments the send counter for provider+result. Result is one of
// success | client_error | provider_error | transport_error.
func (r *Registry) IncSend(provider, result string) {
	key := provider + "|" + result
	r.mu.Lock()
	c, ok := r.m[key]
	if !ok {
		c = &Counter{}
		r.m[key] = c
	}
	r.mu.Unlock()
	c.Inc()
}

// Render writes the Prometheus text exposition of all counters.
func (r *Registry) Render(w io.Writer) {
	r.mu.Lock()
	type row struct {
		provider, result string
		v                uint64
	}
	rows := make([]row, 0, len(r.m))
	for k, c := range r.m {
		parts := strings.SplitN(k, "|", 2)
		rows = append(rows, row{parts[0], parts[1], c.v.Load()})
	}
	r.mu.Unlock()
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].provider != rows[j].provider {
			return rows[i].provider < rows[j].provider
		}
		return rows[i].result < rows[j].result
	})
	io.WriteString(w, "# HELP messaging_gateway_sends_total Outbound provider send attempts by provider and result.\n") //nolint:errcheck
	io.WriteString(w, "# TYPE messaging_gateway_sends_total counter\n")                                                    //nolint:errcheck
	for _, row := range rows {
		fmt.Fprintf(w, "messaging_gateway_sends_total{provider=%q,result=%q} %d\n", row.provider, row.result, row.v) //nolint:errcheck
	}
}
