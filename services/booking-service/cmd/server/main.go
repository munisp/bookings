// booking-service: catalog, availability engine, bookings + outbox,
// booking saga activities and the booking command consumer (SPEC §4/§6/§7).
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/opendesk/booking-service/internal/bookingops"
	"github.com/opendesk/booking-service/internal/cache"
	"github.com/opendesk/booking-service/internal/config"
	"github.com/opendesk/booking-service/internal/consumer"
	"github.com/opendesk/booking-service/internal/daprc"
	"github.com/opendesk/booking-service/internal/httpapi"
	"github.com/opendesk/booking-service/internal/outbox"
	"github.com/opendesk/booking-service/internal/permify"
	"github.com/opendesk/booking-service/internal/store"
	"github.com/opendesk/booking-service/internal/temporalclient"
	"go.uber.org/zap"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	logger, err := zap.NewProduction()
	if err != nil {
		return err
	}
	defer logger.Sync() //nolint:errcheck

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := store.New(ctx, cfg.DatabaseURL, cfg.PGMaxConns)
	if err != nil {
		return err
	}
	defer st.Close()

	daprClient := daprc.New(cfg.DaprHost, cfg.DaprHTTPPort)
	resolver := bookingops.NewTenantResolver(daprClient, cfg.IdentityAppID, cfg.IdentityCacheTTL, logger)

	// Temporal saga starter — optional at boot: when Temporal is unreachable
	// the service still accepts bookings (they stay `pending` and the saga
	// start is logged for reconciliation).
	var saga bookingops.SagaStarter
	var gdpr httpapi.GdprStarter
	tc, err := temporalclient.Dial(cfg.TemporalHostPort, cfg.TemporalNamespace, cfg.TemporalTaskQueue)
	if err != nil {
		logger.Warn("temporal unavailable at boot; saga starts will fail until redeploy",
			zap.String("host_port", cfg.TemporalHostPort), zap.Error(err))
	} else {
		defer tc.Close()
		saga = tc
		gdpr = tc
	}

	// Availability cache (SPEC-W3 §3) — nil when REDIS_ADDR is unset.
	availCache := cache.New(cfg.RedisAddr, cfg.CacheTTL, cfg.CacheStaleTTL, logger)
	if availCache.Enabled() {
		defer availCache.Close() //nolint:errcheck
		logger.Info("availability cache enabled", zap.String("redis_addr", cfg.RedisAddr), zap.Duration("ttl", cfg.CacheTTL))
	}

	ops := &bookingops.Service{
		Store:       st,
		Saga:        saga,
		EventsTopic: cfg.BookingEventsTopic,
		UsageTopic:  cfg.UsageEventsTopic,
		Logger:      logger,
		Cache:       availCache,
	}

	// Outbox dispatcher goroutine: outbox → Dapr pubsub `pubsub-kafka` →
	// topic opendesk.booking.events.
	dispatcher := outbox.New(st, daprClient, cfg.PubSubName, cfg.OutboxPollInterval, logger)
	go dispatcher.Run(ctx)

	// Kafka command consumer (direct broker connection, NOT dapr, SPEC §4).
	var cmdConsumer *consumer.Consumer
	if cfg.ConsumerEnabled {
		cmdConsumer = consumer.New(cfg.KafkaBrokers, cfg.CommandsTopic, cfg.CommandsGroup, cfg.DLQTopic, ops, resolver, logger)
		go func() {
			if err := cmdConsumer.Run(ctx); err != nil && ctx.Err() == nil {
				logger.Error("command consumer exited", zap.Error(err))
			}
		}()
		defer cmdConsumer.Close() //nolint:errcheck

		// GDPR erase consumer (SPEC-W3 §2): anonymizes contacts on
		// PrivacyEraseRequested tombstones from opendesk.privacy.events.
		privacyConsumer := consumer.NewPrivacy(cfg.KafkaBrokers, cfg.PrivacyEventsTopic, cfg.PrivacyGroup, cfg.DLQTopic, st, logger)
		go func() {
			if err := privacyConsumer.Run(ctx); err != nil && ctx.Err() == nil {
				logger.Error("privacy consumer exited", zap.Error(err))
			}
		}()
		defer privacyConsumer.Close() //nolint:errcheck
	}

	deps := httpapi.Deps{
		Store:             st,
		Ops:               ops,
		Resolver:          resolver,
		Authz:             permify.NewHTTPClient(cfg.PermifyURL),
		AuthzDisabled:     cfg.AuthzDisabled,
		AuthzOutagePolicy: cfg.AuthzOutagePolicy,
		Dapr:              daprClient,
		IdentityAppID:     cfg.IdentityAppID,
		Gdpr:              gdpr,
		Cache:             availCache,
		Logger:            logger,

		PortalSecret:       cfg.PortalSecret,
		PubSubName:         cfg.PubSubName,
		NotificationsTopic: cfg.NotificationsTopic,
	}

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           httpapi.NewRouter(deps),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("booking-service listening", zap.Int("port", cfg.Port))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-errCh:
		return err
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	logger.Info("shutting down")
	return srv.Shutdown(shutCtx)
}
