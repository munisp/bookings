package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// Termii sends SMS via the Termii JSON API
// (https://developers.termii.com — POST {base}/api/sms/send).
type Termii struct {
	Client   *Client
	BaseURL  string // https://v2.api.termii.com
	APIKey   string
	SenderID string // default sender id ("OpenDesk")
}

// Configured reports whether the provider has the credentials it needs.
func (t *Termii) Configured() bool { return t.APIKey != "" }

// SendSMS delivers one plain SMS over the generic channel.
func (t *Termii) SendSMS(ctx context.Context, to, message, senderID string) (int, []byte, error) {
	from := senderID
	if from == "" {
		from = t.SenderID
	}
	return t.Client.send(ctx, func(ctx context.Context) (*http.Request, error) {
		payload, err := json.Marshal(map[string]string{
			"api_key": t.APIKey,
			"to":      to,
			"from":    from,
			"sms":     message,
			"type":    "plain",
			"channel": "generic",
		})
		if err != nil {
			return nil, fmt.Errorf("marshal termii payload: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.BaseURL+"/api/sms/send", bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	})
}
