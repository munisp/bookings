// Package config loads messaging-gateway configuration from environment
// variables (envconfig style, no external dependency).
package config

import (
	"os"
	"strconv"
)

// Config holds all runtime configuration for messaging-gateway.
type Config struct {
	Port int // HTTP listen port (7011)

	// Termii (https://developers.termii.com)
	TermiiAPIKey   string // TERMII_API_KEY
	TermiiSenderID string // TERMII_SENDER_ID, default "OpenDesk"
	TermiiBaseURL  string // TERMII_BASE_URL override (tests), default https://v2.api.termii.com

	// Africa's Talking (https://developers.africastalking.com)
	ATAPIKey   string // AT_API_KEY
	ATUsername string // AT_USERNAME (use "sandbox" on the sandbox)
	ATBaseURL  string // AT_BASE_URL override (tests), default https://api.africastalking.com
	ATFrom     string // AT_FROM default sender id / shortcode (optional)

	// WhatsApp Cloud API (https://developers.facebook.com/docs/whatsapp/cloud-api)
	WhatsAppToken         string // WHATSAPP_TOKEN (permanent access token)
	WhatsAppPhoneNumberID string // WHATSAPP_PHONE_NUMBER_ID
	WhatsAppBaseURL       string // WHATSAPP_BASE_URL override (tests), default https://graph.facebook.com/v21.0
}

// Load reads configuration from the environment.
func Load() Config {
	return Config{
		Port:                  envInt("PORT", 7011),
		TermiiAPIKey:          os.Getenv("TERMII_API_KEY"),
		TermiiSenderID:        envStr("TERMII_SENDER_ID", "OpenDesk"),
		TermiiBaseURL:         envStr("TERMII_BASE_URL", "https://v2.api.termii.com"),
		ATAPIKey:              os.Getenv("AT_API_KEY"),
		ATUsername:            os.Getenv("AT_USERNAME"),
		ATBaseURL:             envStr("AT_BASE_URL", "https://api.africastalking.com"),
		ATFrom:                os.Getenv("AT_FROM"),
		WhatsAppToken:         os.Getenv("WHATSAPP_TOKEN"),
		WhatsAppPhoneNumberID: os.Getenv("WHATSAPP_PHONE_NUMBER_ID"),
		WhatsAppBaseURL:       envStr("WHATSAPP_BASE_URL", "https://graph.facebook.com/v21.0"),
	}
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
