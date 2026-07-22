// Package outbox implements the transactional-outbox dispatcher: it polls
// unsent rows and publishes them as CloudEvents to Kafka via the Dapr pubsub
// component `pubsub-kafka`, then marks them sent (SPEC §4/§6).
package outbox

import (
	"context"
	"encoding/json"
	"time"

	"github.com/opendesk/booking-service/internal/daprc"
	"github.com/opendesk/booking-service/internal/store"
	"go.uber.org/zap"
)

// Dispatcher polls and publishes outbox rows.
type Dispatcher struct {
	store    *store.Store
	dapr     *daprc.Client
	pubsub   string
	interval time.Duration
	log      *zap.Logger
}

// New builds the dispatcher.
func New(st *store.Store, d *daprc.Client, pubsub string, interval time.Duration, log *zap.Logger) *Dispatcher {
	return &Dispatcher{store: st, dapr: d, pubsub: pubsub, interval: interval, log: log}
}

// Run loops until ctx is cancelled. Publish failures are retried next cycle
// (at-least-once delivery; consumers must tolerate duplicates).
func (d *Dispatcher) Run(ctx context.Context) {
	tick := time.NewTicker(d.interval)
	defer tick.Stop()
	d.log.Info("outbox dispatcher started", zap.Duration("interval", d.interval))
	for {
		d.dispatchOnce(ctx)
		select {
		case <-ctx.Done():
			d.log.Info("outbox dispatcher stopped")
			return
		case <-tick.C:
		}
	}
}

func (d *Dispatcher) dispatchOnce(ctx context.Context) {
	rows, err := d.store.FetchUnsentOutbox(ctx, 100)
	if err != nil {
		if ctx.Err() == nil {
			d.log.Error("fetch unsent outbox", zap.Error(err))
		}
		return
	}
	for _, row := range rows {
		// payload is already a serialized CloudEvents envelope
		var evt map[string]any
		if err := json.Unmarshal(row.Payload, &evt); err != nil {
			// poison row: mark sent to avoid an infinite hot loop; it is
			// preserved in the table for inspection
			d.log.Error("undeliverable outbox payload, marking sent",
				zap.String("outbox_id", row.ID.String()), zap.Error(err))
			_ = d.store.MarkOutboxSent(ctx, row.ID)
			continue
		}
		if err := d.dapr.PublishEvent(ctx, d.pubsub, row.Topic, evt); err != nil {
			d.log.Warn("publish failed, will retry",
				zap.String("outbox_id", row.ID.String()), zap.String("topic", row.Topic), zap.Error(err))
			continue
		}
		if err := d.store.MarkOutboxSent(ctx, row.ID); err != nil {
			d.log.Error("mark outbox sent", zap.String("outbox_id", row.ID.String()), zap.Error(err))
		}
	}
}
