// messaging-gateway: outbound SMS/WhatsApp gateway for the Nigeria channel
// providers (Termii, Africa's Talking, WhatsApp Cloud API). The
// notification-worker reaches it through the Dapr HTTP output bindings
// bindings-termii / bindings-africastalking / bindings-whatsapp; this
// service owns the provider credentials, retry policy and error mapping.
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

	"github.com/opendesk/messaging-gateway/internal/config"
	"github.com/opendesk/messaging-gateway/internal/httpapi"
	"github.com/opendesk/messaging-gateway/internal/metrics"
	"github.com/opendesk/messaging-gateway/internal/provider"
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

	cfg := config.Load()
	reg := metrics.New()

	srv := &httpapi.Server{
		Termii: &provider.Termii{
			Client:   provider.NewClient("termii", reg, logger),
			BaseURL:  cfg.TermiiBaseURL,
			APIKey:   cfg.TermiiAPIKey,
			SenderID: cfg.TermiiSenderID,
		},
		AT: &provider.AfricasTalking{
			Client:   provider.NewClient("africastalking", reg, logger),
			BaseURL:  cfg.ATBaseURL,
			APIKey:   cfg.ATAPIKey,
			Username: cfg.ATUsername,
			From:     cfg.ATFrom,
		},
		WhatsApp: &provider.WhatsApp{
			Client:        provider.NewClient("whatsapp", reg, logger),
			BaseURL:       cfg.WhatsAppBaseURL,
			Token:         cfg.WhatsAppToken,
			PhoneNumberID: cfg.WhatsAppPhoneNumberID,
		},
		Metrics: reg,
		Log:     logger,
	}
	logger.Info("messaging-gateway configured",
		zap.Bool("termii", srv.Termii.Configured()),
		zap.Bool("africastalking", srv.AT.Configured()),
		zap.Bool("whatsapp", srv.WhatsApp.Configured()))

	hs := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           srv.Router(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", zap.Int("port", cfg.Port))
		if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return hs.Shutdown(shutdownCtx)
}
