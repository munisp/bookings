// Package daprc is a minimal Dapr HTTP API client (net/http only): pub/sub
// publish (opendesk.crm.events webhook intake) and service invocation
// (reverse sync -> booking-service internal endpoints), SPEC-CRM §B.
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

// InvokeService performs Dapr service-to-service invocation:
// POST /v1.0/invoke/{appID}/method/{method}. Extra headers (e.g.
// X-Tenant-Slug for booking-service's tenant middleware) are forwarded
// through the sidecar; out may be nil.
func (c *Client) InvokeService(ctx context.Context, appID, method string, headers map[string]string, payload any, out any) error {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal invoke payload: %w", err)
		}
		body = bytes.NewReader(b)
	}
	url := fmt.Sprintf("%s/v1.0/invoke/%s/method/%s", c.baseURL, appID, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("invoke %s/%s: %w", appID, method, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("invoke %s/%s: status %d: %s", appID, method, resp.StatusCode, string(b))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil && err != io.EOF {
			return fmt.Errorf("decode invoke response: %w", err)
		}
	}
	return nil
}
