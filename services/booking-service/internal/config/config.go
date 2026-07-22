// Package config loads booking-service configuration from environment
// variables (envconfig style, no external dependency).
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime configuration for booking-service.
type Config struct {
	Port               int           // HTTP listen port (PORT, default 7002)
	DatabaseURL        string        // postgres DSN for the booking DB (DATABASE_URL)
	PGMaxConns         int32         // pgxpool MaxConns (PG_MAX_CONNS, default 20 — peak-sized per capacity runbook)
	PermifyURL         string        // Permify HTTP API base (http://permify:3476)
	DaprHost           string        // daprd host (default daprd-booking)
	DaprHTTPPort       int           // daprd HTTP port (default 3500)
	PubSubName         string        // Dapr pubsub component (pubsub-kafka)
	BookingEventsTopic string        // opendesk.booking.events
	IdentityAppID      string        // Dapr app-id of identity-service
	TemporalHostPort   string        // temporal:7233
	TemporalNamespace  string        // opendesk
	TemporalTaskQueue  string        // opendesk-main
	KafkaBrokers       []string      // direct broker list for the command consumer
	CommandsTopic      string        // opendesk.booking.commands
	CommandsGroup      string        // consumer group id
	DLQTopic           string        // opendesk.dlq
	PrivacyEventsTopic string        // opendesk.privacy.events (GDPR erase tombstones)
	PrivacyGroup       string        // consumer group of the privacy erase consumer
	RedisAddr          string        // REDIS_ADDR for the availability cache (empty = cache disabled)
	CacheTTL           time.Duration // availability cache entry TTL (CACHE_TTL_SECONDS, default 120s)
	OutboxPollInterval time.Duration // outbox dispatcher poll cadence
	ShutdownTimeout    time.Duration
	AuthzDisabled      bool // dev escape hatch: skip Permify checks (AUTHZ_DISABLED=true)
	ConsumerEnabled    bool // run the Kafka command consumer (default true)
}

// Load reads configuration from the environment.
func Load() (Config, error) {
	cfg := Config{
		Port:        envInt("PORT", 7002),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		// Voice turns fan out into availability lookups; default 20 covers
		// ~10 peak concurrent calls at 2 mid-call turns each (runbook §DB).
		PGMaxConns:         int32(envInt("PG_MAX_CONNS", 20)),
		PermifyURL:         envStr("PERMIFY_URL", "http://permify:3476"),
		DaprHost:           envStr("DAPR_HOST", "daprd-booking"),
		DaprHTTPPort:       envInt("DAPR_HTTP_PORT", 3500),
		PubSubName:         envStr("DAPR_PUBSUB_NAME", "pubsub-kafka"),
		BookingEventsTopic: envStr("BOOKING_EVENTS_TOPIC", "opendesk.booking.events"),
		IdentityAppID:      envStr("IDENTITY_APP_ID", "identity"),
		TemporalHostPort:   envStr("TEMPORAL_HOST_PORT", "temporal:7233"),
		TemporalNamespace:  envStr("TEMPORAL_NAMESPACE", "opendesk"),
		TemporalTaskQueue:  envStr("TEMPORAL_TASK_QUEUE", "opendesk-main"),
		KafkaBrokers:       strings.Split(envStr("KAFKA_BROKERS", "kafka:9092"), ","),
		CommandsTopic:      envStr("BOOKING_COMMANDS_TOPIC", "opendesk.booking.commands"),
		CommandsGroup:      envStr("BOOKING_COMMANDS_GROUP", "booking-service-commands"),
		DLQTopic:           envStr("DLQ_TOPIC", "opendesk.dlq"),
		PrivacyEventsTopic: envStr("PRIVACY_EVENTS_TOPIC", "opendesk.privacy.events"),
		PrivacyGroup:       envStr("PRIVACY_EVENTS_GROUP", "booking-service-privacy"),
		RedisAddr:          os.Getenv("REDIS_ADDR"),
		CacheTTL:           time.Duration(envInt("CACHE_TTL_SECONDS", 120)) * time.Second,
		OutboxPollInterval: time.Duration(envInt("OUTBOX_POLL_INTERVAL_SECONDS", 2)) * time.Second,
		ShutdownTimeout:    time.Duration(envInt("SHUTDOWN_TIMEOUT_SECONDS", 20)) * time.Second,
		AuthzDisabled:      envStr("AUTHZ_DISABLED", "false") == "true",
		ConsumerEnabled:    envStr("CONSUMER_ENABLED", "true") == "true",
	}
	if cfg.DatabaseURL == "" {
		return cfg, fmt.Errorf("DATABASE_URL is required")
	}
	return cfg, nil
}

// databaseURL resolves the booking DB DSN. DATABASE_URL wins; otherwise the
// DSN is constructed from PG_DSN (base) + PG_DATABASE with an optional
// PG_USER/PG_PASS credential override (per-service DB roles, SPEC-W3 §2).
// The default credentials stay opendesk/opendesk for local dev.
func databaseURL() string {
	if v := os.Getenv("DATABASE_URL"); v != "" {
		return v
	}
	base := envStr("PG_DSN", "postgres://opendesk:opendesk@postgres:5432")
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	if user := os.Getenv("PG_USER"); user != "" {
		u.User = url.UserPassword(user, os.Getenv("PG_PASS"))
	}
	u.Path = "/" + strings.TrimPrefix(envStr("PG_DATABASE", "booking"), "/")
	return u.String()
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
