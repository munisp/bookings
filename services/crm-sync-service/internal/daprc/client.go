// Package daprc is a minimal Dapr HTTP API client (net/http only), trimmed to
// pub/sub publish — crm-sync only emits opendesk.crm.events (SPEC-CRM §B).
package daprc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client talks to a Dapr sidecar over HTTP.
type Client struct {
	baseURL string
	hc      *http.Client
}

// New builds a Client for the given sidecar host/port.
func New(host string, port int) *Client {
	return &Client{
		baseURL: fmt.Sprintf("http://%s:%d", host, port),
		hc:      &http.Client{Timeout: 15 * time.Second},
	}
}

// PublishEvent publishes a CloudEvents envelope to a pubsub component topic.
func (c *Client) PublishEvent(ctx context.Context, pubsub, topic string, data any) error {
	body, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	url := fmt.Sprintf("%s/v1.0/publish/%s/%s", c.baseURL, pubsub, topic)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/cloudevents+json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("publish %s/%s: %w", pubsub, topic, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("publish %s/%s: status %d: %s", pubsub, topic, resp.StatusCode, string(b))
	}
	return nil
}
