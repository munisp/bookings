package signals

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/api/serviceerror"
	"go.uber.org/zap"
)

type recordedSignal struct {
	workflowID string
	signal     string
}

// fakeSignaller records signals and can fail per workflow id.
type fakeSignaller struct {
	got    []recordedSignal
	failOn map[string]error
}

func (f *fakeSignaller) SignalWorkflow(_ context.Context, workflowID, _ string, signalName string, _ interface{}) error {
	if err, ok := f.failOn[workflowID]; ok {
		return err
	}
	f.got = append(f.got, recordedSignal{workflowID: workflowID, signal: signalName})
	return nil
}

func newBridge(f *fakeSignaller) *Bridge {
	return &Bridge{temporal: f, log: zap.NewNop()}
}

func TestProcessBookingCancelledSignalsPackAndReminder(t *testing.T) {
	f := &fakeSignaller{}
	b := newBridge(f)
	evt := []byte(`{"type":"com.opendesk.booking.BookingCancelled","subject":"acme","data":{"booking_id":"b-1"}}`)
	require.NoError(t, b.Process(context.Background(), evt))
	require.Equal(t, []recordedSignal{
		{workflowID: "pack-b-1", signal: "booking-event"},
		{workflowID: "reminder-b-1", signal: "booking-event"},
	}, f.got)
}

func TestProcessBookingNoShowSignalsPackOnly(t *testing.T) {
	f := &fakeSignaller{}
	b := newBridge(f)
	evt := []byte(`{"type":"com.opendesk.booking.BookingNoShow","subject":"acme","data":{"booking_id":"b-2"}}`)
	require.NoError(t, b.Process(context.Background(), evt))
	require.Equal(t, []recordedSignal{
		{workflowID: "pack-b-2", signal: "NoShow"},
	}, f.got)
}

func TestProcessUnknownWorkflowIsAcked(t *testing.T) {
	f := &fakeSignaller{failOn: map[string]error{
		"pack-b-3":     serviceerror.NewNotFound("workflow not found"),
		"reminder-b-3": errors.New("workflow not found"),
	}}
	b := newBridge(f)
	evt := []byte(`{"type":"com.opendesk.booking.BookingCancelled","subject":"acme","data":{"booking_id":"b-3"}}`)
	require.NoError(t, b.Process(context.Background(), evt),
		"completed/unknown workflows must be logged+acked, never retried")
	require.Empty(t, f.got)
}

func TestProcessTransientErrorPropagates(t *testing.T) {
	f := &fakeSignaller{failOn: map[string]error{
		"pack-b-4": errors.New("temporal unreachable"),
	}}
	b := newBridge(f)
	evt := []byte(`{"type":"com.opendesk.booking.BookingNoShow","subject":"acme","data":{"booking_id":"b-4"}}`)
	require.Error(t, b.Process(context.Background(), evt))
}

func TestProcessIgnoresIrrelevantEvents(t *testing.T) {
	f := &fakeSignaller{}
	b := newBridge(f)
	require.NoError(t, b.Process(context.Background(),
		[]byte(`{"type":"com.opendesk.booking.BookingCreated","data":{"booking_id":"b-5"}}`)))
	require.NoError(t, b.Process(context.Background(), []byte(`not json`)))
	require.NoError(t, b.Process(context.Background(),
		[]byte(`{"type":"com.opendesk.booking.BookingCancelled","data":{}}`)))
	require.Empty(t, f.got)
}
