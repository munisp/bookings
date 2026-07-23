package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// WhatsApp sends messages via the WhatsApp Cloud API
// (https://developers.facebook.com/docs/whatsapp/cloud-api —
// POST {base}/{phoneNumberID}/messages, bearer token).
type WhatsApp struct {
	Client        *Client
	BaseURL       string // https://graph.facebook.com/v21.0
	Token         string
	PhoneNumberID string
}

// Configured reports whether the provider has the credentials it needs.
func (w *WhatsApp) Configured() bool { return w.Token != "" && w.PhoneNumberID != "" }

// SendMessage delivers a free-form text message, or a template message when
// template is non-empty (required outside the 24h customer service window).
func (w *WhatsApp) SendMessage(ctx context.Context, to, message, template string) (int, []byte, error) {
	var payload map[string]any
	if template != "" {
		payload = map[string]any{
			"messaging_product": "whatsapp",
			"to":                to,
			"type":              "template",
			"template": map[string]any{
				"name":     template,
				"language": map[string]string{"code": "en_US"},
			},
		}
	} else {
		payload = map[string]any{
			"messaging_product": "whatsapp",
			"to":                to,
			"type":              "text",
			"text":              map[string]string{"body": message},
		}
	}
	return w.Client.send(ctx, func(ctx context.Context) (*http.Request, error) {
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshal whatsapp payload: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			w.BaseURL+"/"+w.PhoneNumberID+"/messages", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+w.Token)
		return req, nil
	})
}
