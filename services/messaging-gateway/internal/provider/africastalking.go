package provider

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// AfricasTalking sends SMS via the Africa's Talking messaging API
// (https://developers.africastalking.com — POST {base}/version1/messaging,
// form-encoded, apiKey header).
type AfricasTalking struct {
	Client   *Client
	BaseURL  string // https://api.africastalking.com (sandbox: https://api.sandbox.africastalking.com)
	APIKey   string
	Username string // app username ("sandbox" on the sandbox)
	From     string // default sender id / shortcode (optional)
}

// Configured reports whether the provider has the credentials it needs.
func (a *AfricasTalking) Configured() bool { return a.APIKey != "" && a.Username != "" }

// SendSMS delivers one SMS. from overrides the default sender id when given.
func (a *AfricasTalking) SendSMS(ctx context.Context, to, message, from string) (int, []byte, error) {
	if from == "" {
		from = a.From
	}
	return a.Client.send(ctx, func(ctx context.Context) (*http.Request, error) {
		form := url.Values{
			"username": {a.Username},
			"to":       {to},
			"message":  {message},
		}
		if from != "" {
			form.Set("from", from)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			a.BaseURL+"/version1/messaging", strings.NewReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("apiKey", a.APIKey)
		return req, nil
	})
}
