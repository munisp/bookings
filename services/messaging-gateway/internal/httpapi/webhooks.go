// Omnichannel inbound webhooks (SPEC-W6 Part A): Meta WhatsApp Cloud API
// verification + message ingestion and Telegram Bot API updates.
//
// Reliability contract: these handlers ALWAYS answer 200 fast to the
// provider (Meta/Telegram retry-storm on non-200). The only non-200 answers
// are 403 for a bad verify token (GET) or a bad shared secret (Telegram
// POST) — i.e. authentication failures, never internal processing errors.
// Processing is synchronous but bounded by a 25s context.
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/opendesk/messaging-gateway/internal/channel"
	"go.uber.org/zap"
)

// webhookTimeout bounds the synchronous bridge processing per message
// (SPEC-W6 §A1: process synchronously but bounded).
const webhookTimeout = 25 * time.Second

// Bridger is the inbound bridge contract used by the webhook handlers
// (implemented by *channel.Bridge; faked in tests).
type Bridger interface {
	Handle(ctx context.Context, msg channel.InboundMessage, routeID string) error
}

// ---------------------------------------------------------------------------
// WhatsApp (Meta Cloud API)
// ---------------------------------------------------------------------------

// handleWhatsAppVerify implements the Meta webhook verification handshake:
// hub.mode=subscribe + matching hub.verify_token → 200 with the raw
// challenge body; anything else → 403 (SPEC-W6 §A1).
func (s *Server) handleWhatsAppVerify(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("hub.mode") == "subscribe" &&
		s.WhatsAppVerifyToken != "" &&
		q.Get("hub.verify_token") == s.WhatsAppVerifyToken {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(q.Get("hub.challenge"))) //nolint:errcheck
		return
	}
	writeError(w, http.StatusForbidden, "webhook verification failed")
}

// waWebhook is the Meta Cloud API webhook payload shape (only the fields
// the bridge needs).
type waWebhook struct {
	Entry []struct {
		Changes []struct {
			Value struct {
				Metadata struct {
					PhoneNumberID string `json:"phone_number_id"`
				} `json:"metadata"`
				Messages []struct {
					From      string `json:"from"` // E.164 without '+'
					ID        string `json:"id"`
					Timestamp string `json:"timestamp"` // unix seconds, string-typed by Meta
					Type      string `json:"type"`
					Text      struct {
						Body string `json:"body"`
					} `json:"text"`
				} `json:"messages"`
				Statuses []json.RawMessage `json:"statuses"`
			} `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

// handleWhatsAppWebhook ingests inbound WhatsApp messages. Text messages
// are normalized and bridged; statuses[] delivery receipts and non-text
// message types are ignored. Always 200 (SPEC-W6 §A1).
func (s *Server) handleWhatsAppWebhook(w http.ResponseWriter, r *http.Request) {
	var payload waWebhook
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		// Malformed payload: still 200 — Meta retries non-200 forever and a
		// poison payload would loop. Log and drop.
		s.Log.Warn("whatsapp webhook: invalid JSON body, dropping", zap.Error(err))
		writeJSON(w, http.StatusOK, map[string]any{"status": "ignored"})
		return
	}
	for _, entry := range payload.Entry {
		for _, change := range entry.Changes {
			v := change.Value
			// v.Statuses (delivery receipts) are ignored silently — only
			// messages[] below produce bridge work.
			for _, m := range v.Messages {
				if m.Type != "text" {
					s.Log.Info("whatsapp webhook: ignoring non-text message",
						zap.String("type", m.Type), zap.String("message_id", m.ID))
					continue
				}
				ts, _ := strconv.ParseInt(m.Timestamp, 10, 64)
				s.bridge(r.Context(), channel.InboundMessage{
					Channel:   "whatsapp",
					From:      m.From,
					MessageID: m.ID,
					Text:      m.Text.Body,
					Timestamp: ts,
				}, v.Metadata.PhoneNumberID)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// ---------------------------------------------------------------------------
// Telegram (Bot API)
// ---------------------------------------------------------------------------

// tgUpdate is the Telegram Bot API Update shape (message-only subset).
type tgUpdate struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		MessageID int64  `json:"message_id"`
		Date      int64  `json:"date"` // unix seconds
		Text      string `json:"text"`
		Chat      struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		From *struct {
			ID int64 `json:"id"`
		} `json:"from"`
	} `json:"message"`
}

// handleTelegramWebhook ingests Telegram Bot API updates. When
// TELEGRAM_WEBHOOK_SECRET is set the X-Telegram-Bot-Api-Secret-Token header
// must match (else 403). Updates without message.text are ignored.
// Always 200 otherwise (SPEC-W6 §A1).
func (s *Server) handleTelegramWebhook(w http.ResponseWriter, r *http.Request) {
	if s.TelegramWebhookSecret != "" &&
		r.Header.Get("X-Telegram-Bot-Api-Secret-Token") != s.TelegramWebhookSecret {
		writeError(w, http.StatusForbidden, "bad webhook secret")
		return
	}
	var upd tgUpdate
	if err := json.NewDecoder(r.Body).Decode(&upd); err != nil {
		s.Log.Warn("telegram webhook: invalid JSON body, dropping", zap.Error(err))
		writeJSON(w, http.StatusOK, map[string]any{"status": "ignored"})
		return
	}
	if upd.Message == nil || upd.Message.Text == "" {
		// Edits, stickers, join events, …: nothing to bridge.
		writeJSON(w, http.StatusOK, map[string]any{"status": "ignored"})
		return
	}
	m := upd.Message
	s.bridge(r.Context(), channel.InboundMessage{
		Channel:   "telegram",
		From:      strconv.FormatInt(m.Chat.ID, 10), // chat_id as string
		MessageID: strconv.FormatInt(m.MessageID, 10),
		Text:      m.Text,
		Timestamp: m.Date,
	}, s.TelegramBotUsername)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// bridge runs the inbound bridge with the bounded 25s context and swallows
// the outcome (already logged by the bridge) — the provider always gets 200.
func (s *Server) bridge(parent context.Context, msg channel.InboundMessage, routeID string) {
	if s.Bridge == nil {
		s.Log.Warn("inbound bridge not configured, dropping message",
			zap.String("channel", msg.Channel))
		return
	}
	ctx, cancel := context.WithTimeout(parent, webhookTimeout)
	defer cancel()
	if err := s.Bridge.Handle(ctx, msg, routeID); err != nil {
		s.Log.Warn("inbound bridge failed",
			zap.String("channel", msg.Channel), zap.Error(err))
	}
}

// ---------------------------------------------------------------------------
// Telegram send endpoint (outbound parity with the other providers, §A4)
// ---------------------------------------------------------------------------

type telegramRequest struct {
	To      string `json:"to"` // chat_id as string
	Message string `json:"message"`
}

func (s *Server) handleTelegramSend(w http.ResponseWriter, r *http.Request) {
	var req telegramRequest
	if !decodeJSON(w, r, &req) || !requireToMessage(w, req.To, req.Message) {
		return
	}
	if !s.Telegram.Configured() {
		writeError(w, http.StatusServiceUnavailable, "telegram provider not configured (TELEGRAM_BOT_TOKEN)")
		return
	}
	status, body, err := s.Telegram.SendMessage(r.Context(), req.To, req.Message)
	s.respond(w, r, status, body, err)
}
