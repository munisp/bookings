// Package daprc is a minimal Dapr HTTP API client implemented with net/http
// only (no Dapr SDK dependency). Supports pub/sub publish, service invocation
// (with optional forwarded headers), state and output bindings.
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
		hc:      &http.Client{Timeout: 20 * time.Second},
	}
}

// PublishEvent publishes data to a pubsub component topic.
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
	return c.do(req, nil)
}

// InvokeService performs Dapr service-to-service invocation without headers.
func (c *Client) InvokeService(ctx context.Context, appID, method string, payload any, out any) error {
	return c.InvokeServiceWithHeaders(ctx, appID, method, payload, nil, out)
}

// InvokeServiceWithHeaders performs Dapr service invocation (POST), forwarding
// the given HTTP headers to the target app (e.g. X-Tenant-Slug).
func (c *Client) InvokeServiceWithHeaders(ctx context.Context, appID, method string, payload any, headers map[string]string, out any) error {
	return c.InvokeServiceMethod(ctx, http.MethodPost, appID, method, payload, headers, out)
}

// InvokeServiceMethod performs Dapr service invocation with an explicit HTTP
// verb. Dapr forwards any HTTP method on /v1.0/invoke/{app}/method/{path},
// so reads can use GET (payload is ignored for GET/DELETE).
func (c *Client) InvokeServiceMethod(ctx context.Context, httpMethod, appID, method string, payload any, headers map[string]string, out any) error {
	var body io.Reader
	if payload != nil && httpMethod != http.MethodGet && httpMethod != http.MethodDelete {
		b, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal invoke payload: %w", err)
		}
		body = bytes.NewReader(b)
	}
	url := fmt.Sprintf("%s/v1.0/invoke/%s/method/%s", c.baseURL, appID, method)
	req, err := http.NewRequestWithContext(ctx, httpMethod, url, body)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return c.do(req, out)
}

// InvokeBinding invokes a Dapr output binding with the create operation
// (used for bindings-smtp and bindings-twilio, SPEC §6).
func (c *Client) InvokeBinding(ctx context.Context, name, operation string, data any, metadata map[string]string) error {
	body, err := json.Marshal(map[string]any{
		"operation": operation,
		"data":      data,
		"metadata":  metadata,
	})
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/v1.0/bindings/%s", c.baseURL, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, nil)
}

// GetState fetches a key from a Dapr state store, unmarshalling into out.
func (c *Client) GetState(ctx context.Context, store, key string, out any) (bool, error) {
	url := fmt.Sprintf("%s/v1.0/state/%s/%s", c.baseURL, store, key)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return false, fmt.Errorf("state get %s/%s: %w", store, key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return false, nil
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return false, fmt.Errorf("state get %s/%s: status %d: %s", store, key, resp.StatusCode, string(b))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return false, fmt.Errorf("decode state: %w", err)
	}
	return true, nil
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", req.Method, req.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s %s: status %d: %s", req.Method, req.URL, resp.StatusCode, string(b))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil && err != io.EOF {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}
