package notifyoutbox

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.uber.org/zap"
)

type fakeSender struct {
	sent []struct {
		channel, dest, subject, text string
	}
	err error
}

func (f *fakeSender) Send(_ context.Context, channel, destination, subject, text string) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, struct {
		channel, dest, subject, text string
	}{channel, destination, subject, text})
	return nil
}

func TestProcessSendPortalCodeSMS(t *testing.T) {
	sender := &fakeSender{}
	c := &Consumer{sender: sender, log: zap.NewNop()}
	raw := []byte(`{"specversion":"1.0","id":"e-1","type":"com.opendesk.notifications.SendPortalCode","subject":"acme","tenantid":"t-1","data":{"channel":"sms","destination":"+15550101","code":"482910","contact_name":"Pia","site_slug":"acme-books","expires_in_minutes":10}}`)
	if err := c.Process(context.Background(), raw); err != nil {
		t.Fatalf("process: %v", err)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("sent = %d, want 1", len(sender.sent))
	}
	msg := sender.sent[0]
	if msg.channel != "sms" || msg.dest != "+15550101" {
		t.Fatalf("msg = %+v", msg)
	}
	if want := "482910"; !strings.Contains(msg.text, want) {
		t.Fatalf("text %q does not contain the code", msg.text)
	}
}

func TestProcessSendPortalCodeEmail(t *testing.T) {
	sender := &fakeSender{}
	c := &Consumer{sender: sender, log: zap.NewNop()}
	raw := []byte(`{"type":"com.opendesk.notifications.SendPortalCode","data":{"channel":"email","destination":"pia@example.com","code":"111222"}}`)
	if err := c.Process(context.Background(), raw); err != nil {
		t.Fatalf("process: %v", err)
	}
	if len(sender.sent) != 1 || sender.sent[0].channel != "email" || sender.sent[0].subject == "" {
		t.Fatalf("sent = %+v", sender.sent)
	}
}

func TestProcessRejectsInvalidPayload(t *testing.T) {
	sender := &fakeSender{}
	c := &Consumer{sender: sender, log: zap.NewNop()}
	for _, raw := range [][]byte{
		[]byte(`{"type":"com.opendesk.notifications.SendPortalCode","data":{"channel":"pigeon","destination":"x","code":"123456"}}`),
		[]byte(`{"type":"com.opendesk.notifications.SendPortalCode","data":{"channel":"sms","destination":"","code":"123456"}}`),
		[]byte(`{"type":"com.opendesk.notifications.SendPortalCode","data":{"channel":"sms","destination":"+1","code":""}}`),
	} {
		if err := c.Process(context.Background(), raw); err == nil {
			t.Fatalf("expected invalid payload error for %s", raw)
		}
	}
	if len(sender.sent) != 0 {
		t.Fatal("invalid payloads must not send")
	}
}

func TestProcessUnknownTypeIsAcknowledged(t *testing.T) {
	sender := &fakeSender{}
	c := &Consumer{sender: sender, log: zap.NewNop()}
	for _, raw := range [][]byte{
		[]byte(`{"type":"com.opendesk.notifications.SendReminder","data":{}}`),
		[]byte(`not json`),
	} {
		if err := c.Process(context.Background(), raw); err != nil {
			t.Fatalf("unknown/malformed commands must be acknowledged: %v", err)
		}
	}
	if len(sender.sent) != 0 {
		t.Fatal("unknown commands must not send")
	}
}

func TestProcessSenderFailurePropagates(t *testing.T) {
	sender := &fakeSender{err: errors.New("twilio down")}
	c := &Consumer{sender: sender, log: zap.NewNop()}
	raw := []byte(`{"type":"com.opendesk.notifications.SendPortalCode","data":{"channel":"sms","destination":"+1","code":"123456"}}`)
	if err := c.Process(context.Background(), raw); err == nil {
		t.Fatal("expected sender failure to propagate")
	}
}
