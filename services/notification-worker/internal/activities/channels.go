package activities

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Channel routing (docs/integrations/messaging-channels.md): which
// messaging provider carries the email / sms channel, per tenant.
//
// MESSAGING_CHANNELS sets the fleet defaults, e.g. "email:smtp,sms:twilio"
// (the default). TENANT_CHANNEL_MAP optionally overrides per tenant as JSON:
//
//	{"acme-ng": {"sms": "termii"}, "default": {"sms": "africastalking"}}
//
// The reserved "default" key overrides the fleet defaults; unknown tenants
// fall back to it and then to MESSAGING_CHANNELS. The Dapr binding invoke
// name is "bindings-"+provider (termii/africastalking/whatsapp map to the
// messaging-gateway HTTP bindings; smtp/twilio are the unchanged native
// bindings).

// Channel names.
const (
	ChannelEmail = "email"
	ChannelSMS   = "sms"
)

// validProviders lists the routable providers per channel.
var validProviders = map[string]map[string]bool{
	ChannelEmail: {"smtp": true},
	ChannelSMS:   {"twilio": true, "termii": true, "africastalking": true, "whatsapp": true},
}

// ChannelRouter resolves channel → provider, with per-tenant overrides.
type ChannelRouter struct {
	defaults map[string]string            // channel → provider (fleet + "default" override)
	tenants  map[string]map[string]string // tenant slug → channel → provider
}

// NewChannelRouter parses MESSAGING_CHANNELS ("email:smtp,sms:twilio") and
// the optional TENANT_CHANNEL_MAP JSON. An empty tenantJSON means no
// tenant overrides.
func NewChannelRouter(spec, tenantJSON string) (*ChannelRouter, error) {
	r := &ChannelRouter{
		defaults: map[string]string{ChannelEmail: "smtp", ChannelSMS: "twilio"},
		tenants:  map[string]map[string]string{},
	}
	if strings.TrimSpace(spec) != "" {
		for _, pair := range strings.Split(spec, ",") {
			channel, provider, ok := strings.Cut(strings.TrimSpace(pair), ":")
			if !ok || channel == "" || provider == "" {
				return nil, fmt.Errorf("MESSAGING_CHANNELS: invalid entry %q (want channel:provider)", pair)
			}
			if err := validateProvider(channel, provider); err != nil {
				return nil, fmt.Errorf("MESSAGING_CHANNELS: %w", err)
			}
			r.defaults[channel] = provider
		}
	}
	if strings.TrimSpace(tenantJSON) != "" {
		var m map[string]map[string]string
		if err := json.Unmarshal([]byte(tenantJSON), &m); err != nil {
			return nil, fmt.Errorf("TENANT_CHANNEL_MAP: invalid JSON: %w", err)
		}
		for tenant, channels := range m {
			for channel, provider := range channels {
				if err := validateProvider(channel, provider); err != nil {
					return nil, fmt.Errorf("TENANT_CHANNEL_MAP[%q]: %w", tenant, err)
				}
			}
			if tenant == "default" {
				for channel, provider := range channels {
					r.defaults[channel] = provider
				}
				continue
			}
			r.tenants[tenant] = channels
		}
	}
	return r, nil
}

func validateProvider(channel, provider string) error {
	valid, ok := validProviders[channel]
	if !ok {
		return fmt.Errorf("unknown channel %q (want one of: %s)", channel, strings.Join(channelNames(), ", "))
	}
	if !valid[provider] {
		return fmt.Errorf("invalid provider %q for channel %q (want one of: %s)",
			provider, channel, strings.Join(providerNames(channel), ", "))
	}
	return nil
}

func channelNames() []string {
	out := make([]string, 0, len(validProviders))
	for c := range validProviders {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

func providerNames(channel string) []string {
	out := make([]string, 0, len(validProviders[channel]))
	for p := range validProviders[channel] {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Provider resolves the provider for a channel and tenant slug: tenant
// override first, then the defaults (fleet defaults + "default" override).
// A nil router resolves to the built-in defaults (email→smtp, sms→twilio).
func (r *ChannelRouter) Provider(channel, tenantSlug string) string {
	if r == nil {
		return map[string]string{ChannelEmail: "smtp", ChannelSMS: "twilio"}[channel]
	}
	if tenantSlug != "" {
		if channels, ok := r.tenants[tenantSlug]; ok {
			if provider, ok := channels[channel]; ok {
				return provider
			}
		}
	}
	return r.defaults[channel]
}

// BindingName maps a provider onto its Dapr output binding invoke name.
// smtp/twilio keep the configured (possibly renamed) bindings; the Nigeria
// providers map to the messaging-gateway HTTP bindings "bindings-"+provider.
func (a *Activities) BindingName(provider string) string {
	switch provider {
	case "smtp":
		return a.SMTPBinding
	case "twilio":
		return a.TwilioBinding
	default:
		return "bindings-" + provider
	}
}
