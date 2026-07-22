// Package temporalclient wraps the Temporal Go SDK client used to start the
// BookingSagaWorkflow (hosted by notification-worker, SPEC §6).
package temporalclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/opendesk/booking-service/internal/bookingops"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
)

// WorkflowType is the registered name of the booking saga workflow.
const WorkflowType = "BookingSagaWorkflow"

// GDPR workflow types (hosted by notification-worker, SPEC-W3 §2).
const (
	GdprExportWorkflowType = "GdprExportWorkflow"
	GdprEraseWorkflowType  = "GdprEraseWorkflow"
)

// GdprRequest is the input contract of GdprExportWorkflow/GdprEraseWorkflow
// (mirrors notification-worker's workflows.GdprInput — JSON-compatible,
// duplicated per service-boundary rules).
type GdprRequest struct {
	TenantID   string `json:"tenant_id"`
	TenantSlug string `json:"tenant_slug"`
	Phone      string `json:"phone,omitempty"`
	Email      string `json:"email,omitempty"`
}

// Client implements bookingops.SagaStarter against a Temporal server.
type Client struct {
	tc        client.Client
	taskQueue string
}

// Dial connects to Temporal (host:port, namespace, task queue per SPEC §6).
func Dial(hostPort, namespace, taskQueue string) (*Client, error) {
	tc, err := client.Dial(client.Options{
		HostPort:  hostPort,
		Namespace: namespace,
	})
	if err != nil {
		return nil, fmt.Errorf("dial temporal: %w", err)
	}
	return &Client{tc: tc, taskQueue: taskQueue}, nil
}

// Close releases the underlying connection.
func (c *Client) Close() { c.tc.Close() }

// StartBookingSaga starts BookingSagaWorkflow with workflow ID
// "booking-saga-{bookingID}" so duplicate starts are idempotent.
func (c *Client) StartBookingSaga(ctx context.Context, in bookingops.SagaInput) (string, error) {
	opts := client.StartWorkflowOptions{
		ID:        "booking-saga-" + in.BookingID,
		TaskQueue: c.taskQueue,
		// A duplicate workflow ID for the same booking means the saga is
		// already running — treat as success below.
		WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
	}
	run, err := c.tc.ExecuteWorkflow(ctx, opts, WorkflowType, in)
	if err != nil {
		var alreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted
		if errors.As(err, &alreadyStarted) {
			return "already-started", nil
		}
		return "", fmt.Errorf("execute %s: %w", WorkflowType, err)
	}
	return run.GetRunID(), nil
}

// gdprWorkflowID derives a deterministic workflow ID so repeated export/erase
// requests for the same contact are idempotent (reject-duplicate policy).
func gdprWorkflowID(kind string, in GdprRequest) string {
	sum := sha256.Sum256([]byte(in.TenantID + "|" + in.Phone + "|" + in.Email))
	return fmt.Sprintf("gdpr-%s-%s-%s", kind, in.TenantID, hex.EncodeToString(sum[:])[:12])
}

// StartGdprExport starts GdprExportWorkflow; returns the workflow ID.
func (c *Client) StartGdprExport(ctx context.Context, in GdprRequest) (string, error) {
	return c.startGdpr(ctx, GdprExportWorkflowType, "export", in)
}

// StartGdprErase starts GdprEraseWorkflow; returns the workflow ID.
func (c *Client) StartGdprErase(ctx context.Context, in GdprRequest) (string, error) {
	return c.startGdpr(ctx, GdprEraseWorkflowType, "erase", in)
}

func (c *Client) startGdpr(ctx context.Context, wfType, kind string, in GdprRequest) (string, error) {
	id := gdprWorkflowID(kind, in)
	opts := client.StartWorkflowOptions{
		ID:                    id,
		TaskQueue:             c.taskQueue,
		WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
	}
	_, err := c.tc.ExecuteWorkflow(ctx, opts, wfType, in)
	if err != nil {
		var alreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted
		if errors.As(err, &alreadyStarted) {
			return id, nil
		}
		return "", fmt.Errorf("execute %s: %w", wfType, err)
	}
	return id, nil
}
