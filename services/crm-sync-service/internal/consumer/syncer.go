// Package consumer implements the Kafka event consumers for
// opendesk.identity.events, opendesk.booking.events and
// opendesk.conversation.events (segmentio/kafka-go, consumer group
// `crm-sync`) and the OpenDesk -> Twenty sync logic (SPEC-CRM §B).
// Poison messages are dead-lettered to opendesk.dlq after 3 attempts.
package consumer

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/opendesk/crm-sync-service/internal/events"
	"github.com/opendesk/crm-sync-service/internal/metrics"
	"github.com/opendesk/crm-sync-service/internal/syncmap"
	"github.com/opendesk/crm-sync-service/internal/twentyc"
	"go.uber.org/zap"
)

// sync_map kinds.
const (
	KindTenant  = "tenant"
	KindContact = "contact"
	KindBooking = "booking"
	// KindBookingTask maps booking id -> Twenty task id for the reverse
	// (Twenty -> OpenDesk) lookup: a task.updated DONE webhook resolves the
	// OpenDesk booking via GetByTwentyID(KindBookingTask, taskID). It
	// duplicates kind=booking deliberately so the reverse contract is
	// explicit and stable even if the forward mapping evolves.
	KindBookingTask = "booking_task"
)

// errPermanent marks failures that retrying cannot heal (bad payloads,
// unmappable events). Such messages go straight to the DLQ.
var errPermanent = errors.New("permanent processing error")

func permanent(err error) error { return fmt.Errorf("%w: %v", errPermanent, err) }

// MapStore is the subset of syncmap.Store used by the Syncer (interface for
// testability).
type MapStore interface {
	Get(ctx context.Context, kind, opendeskID string, tenantID *uuid.UUID) (syncmap.Mapping, error)
	Put(ctx context.Context, kind, opendeskID, twentyID string, tenantID *uuid.UUID) error
	DeleteByTwentyID(ctx context.Context, twentyID string) (int64, error)
}

// Syncer applies CloudEvents to Twenty via the REST client + sync_map.
type Syncer struct {
	Twenty  *twentyc.Client
	Map     MapStore
	Metrics *metrics.Registry
	Log     *zap.Logger
}

// HandleIdentity processes opendesk.identity.events.
func (s *Syncer) HandleIdentity(ctx context.Context, evt events.CloudEvent) error {
	switch evt.Type {
	case events.TypeTenantProvisioned:
	default:
		s.Log.Debug("ignoring identity event", zap.String("type", evt.Type))
		return nil
	}
	d, err := events.DataAs[events.TenantProvisionedData](evt)
	if err != nil {
		return permanent(err)
	}
	if d.Slug == "" || d.Name == "" {
		return permanent(fmt.Errorf("TenantProvisioned missing slug/name"))
	}
	tenantUUID := parseUUID(d.TenantID)
	companyID, err := s.Twenty.UpsertCompany(ctx, d.Name, d.Slug)
	if err != nil {
		return fmt.Errorf("upsert company: %w", err)
	}
	if d.TenantID != "" {
		if err := s.Map.Put(ctx, KindTenant, d.TenantID, companyID, tenantUUID); err != nil {
			return err
		}
	}
	s.Log.Info("tenant synced to Twenty company",
		zap.String("slug", d.Slug), zap.String("company_id", companyID))
	return nil
}

// HandleBooking processes opendesk.booking.events.
func (s *Syncer) HandleBooking(ctx context.Context, evt events.CloudEvent) error {
	switch evt.Type {
	case events.TypeBookingCreated, events.TypeBookingConfirmed,
		events.TypeBookingRescheduled, events.TypeBookingCancelled:
	default:
		s.Log.Debug("ignoring booking event", zap.String("type", evt.Type))
		return nil
	}
	d, err := events.DataAs[events.BookingData](evt)
	if err != nil {
		return permanent(err)
	}
	if d.BookingID == "" {
		return permanent(fmt.Errorf("%s missing booking_id", evt.Type))
	}
	tenantUUID := parseUUID(evt.TenantID)

	switch evt.Type {
	case events.TypeBookingCancelled:
		return s.handleBookingCancelled(ctx, d, tenantUUID)
	case events.TypeBookingRescheduled:
		if err := s.handleBookingRescheduled(ctx, d, tenantUUID); err == nil {
			return nil
		} else if !errors.Is(err, syncmap.ErrNotFound) {
			return err
		}
		// No task mapping yet (out-of-order delivery): fall through to the
		// full create path so the task still lands in Twenty.
		s.Log.Info("no task mapping for rescheduled booking; running full sync",
			zap.String("booking_id", d.BookingID))
		fallthrough
	default: // BookingCreated / BookingConfirmed
		return s.handleBookingUpsert(ctx, d, tenantUUID)
	}
}

// handleBookingUpsert syncs the contact (Person) and the appointment (Task).
func (s *Syncer) handleBookingUpsert(ctx context.Context, d events.BookingData, tenantUUID *uuid.UUID) error {
	personID, err := s.syncPerson(ctx, d, tenantUUID)
	if err != nil {
		return err
	}
	if personID != "" && d.Phone != "" {
		// Phone -> person edge: lets the SessionEnded call-quality note path
		// resolve the caller when the Twenty phone lookup misses.
		if err := s.Map.Put(ctx, syncmap.KindContactPhone, d.Phone, personID, tenantUUID); err != nil {
			return err
		}
	}
	if m, err := s.Map.Get(ctx, KindBooking, d.BookingID, tenantUUID); err == nil {
		// Task already exists: keep it current (confirmed / re-created paths).
		if err := s.Twenty.PatchTask(ctx, m.TwentyID, map[string]any{
			"title": twentyc.TaskTitle(d.OfferingName, d.StartsAt),
			"dueAt": twentyc.FormatTime(d.StartsAt),
		}); err != nil {
			return fmt.Errorf("patch task: %w", err)
		}
		if personID != "" {
			// Keep the booking -> person edge fresh on the patch path too.
			if err := s.Map.Put(ctx, syncmap.KindBookingContact, d.BookingID, personID, tenantUUID); err != nil {
				return err
			}
		}
		return nil
	} else if !errors.Is(err, syncmap.ErrNotFound) {
		return err
	}
	taskID, err := s.Twenty.CreateTask(ctx, twentyc.TaskFromBooking(d), personID)
	if err != nil {
		// Person-link failures arrive wrapped after the task exists; still
		// treat as retryable so the mapping is only stored on full success.
		return fmt.Errorf("create task: %w", err)
	}
	if err := s.Map.Put(ctx, KindBooking, d.BookingID, taskID, tenantUUID); err != nil {
		return err
	}
	// Reverse-lookup row for the Twenty -> OpenDesk direction (task.updated
	// DONE webhooks resolve the booking via kind=booking_task).
	if err := s.Map.Put(ctx, KindBookingTask, d.BookingID, taskID, tenantUUID); err != nil {
		return err
	}
	if personID != "" {
		// booking -> person edge used by the /v1/tasks helper (kind=booking_contact).
		if err := s.Map.Put(ctx, syncmap.KindBookingContact, d.BookingID, personID, tenantUUID); err != nil {
			return err
		}
	}
	s.Log.Info("booking synced to Twenty task",
		zap.String("booking_id", d.BookingID), zap.String("task_id", taskID))
	return nil
}

// syncPerson find-then-create/updates the Person and stores the contact mapping.
func (s *Syncer) syncPerson(ctx context.Context, d events.BookingData, tenantUUID *uuid.UUID) (string, error) {
	if d.Email == "" && d.Phone == "" {
		return "", nil // nothing to key a person on; task still gets created
	}
	if d.ContactID != "" {
		if m, err := s.Map.Get(ctx, KindContact, d.ContactID, tenantUUID); err == nil {
			// Keep the person record fresh (name/phone may have changed).
			if err := s.Twenty.UpdatePerson(ctx, m.TwentyID,
				twentyc.PersonFromContact(d.ContactName, d.Email, d.Phone)); err != nil {
				s.Log.Warn("person update failed; keeping existing mapping",
					zap.String("person_id", m.TwentyID), zap.Error(err))
			} else if err := s.Map.Put(ctx, KindContact, d.ContactID, m.TwentyID, tenantUUID); err != nil {
				// Refresh last_synced_at so the reverse worker's echo
				// suppression sees that WE just wrote this person.
				return "", err
			}
			return m.TwentyID, nil
		} else if !errors.Is(err, syncmap.ErrNotFound) {
			return "", err
		}
	}
	personID, err := s.Twenty.UpsertPerson(ctx, d.ContactName, d.Email, d.Phone)
	if err != nil {
		return "", fmt.Errorf("upsert person: %w", err)
	}
	if d.ContactID != "" {
		if err := s.Map.Put(ctx, KindContact, d.ContactID, personID, tenantUUID); err != nil {
			return "", err
		}
	}
	return personID, nil
}

func (s *Syncer) handleBookingCancelled(ctx context.Context, d events.BookingData, tenantUUID *uuid.UUID) error {
	m, err := s.Map.Get(ctx, KindBooking, d.BookingID, tenantUUID)
	if errors.Is(err, syncmap.ErrNotFound) {
		// Nothing to cancel (task never synced, or already removed) — ack.
		s.Log.Info("no task mapping for cancelled booking; skipping",
			zap.String("booking_id", d.BookingID))
		return nil
	}
	if err != nil {
		return err
	}
	// Twenty task statuses are TODO/IN_PROGRESS/DONE: a cancelled appointment
	// is closed out as DONE with an explanatory note (SPEC-CRM §B).
	if err := s.Twenty.PatchTask(ctx, m.TwentyID, map[string]any{"status": "DONE"}); err != nil {
		return fmt.Errorf("patch task status: %w", err)
	}
	if personID, perr := s.findPersonForNote(ctx, d.Phone, d.Email); perr == nil && personID != "" {
		if _, nerr := s.Twenty.CreateNote(ctx, "Booking cancelled", twentyc.CancelNote(d.Reason), personID); nerr != nil {
			s.Log.Warn("cancel note failed (task already closed)", zap.Error(nerr))
		}
	}
	s.Log.Info("cancelled booking task closed", zap.String("task_id", m.TwentyID))
	return nil
}

func (s *Syncer) handleBookingRescheduled(ctx context.Context, d events.BookingData, tenantUUID *uuid.UUID) error {
	m, err := s.Map.Get(ctx, KindBooking, d.BookingID, tenantUUID)
	if err != nil {
		return err // ErrNotFound handled by caller (fallthrough to full sync)
	}
	if d.StartsAt.IsZero() {
		return permanent(fmt.Errorf("BookingRescheduled missing starts_at"))
	}
	if err := s.Twenty.PatchTask(ctx, m.TwentyID, map[string]any{
		"dueAt": twentyc.FormatTime(d.StartsAt),
	}); err != nil {
		return fmt.Errorf("patch task dueDate: %w", err)
	}
	s.Log.Info("rescheduled booking task updated", zap.String("task_id", m.TwentyID))
	return nil
}

// HandleConversation processes opendesk.conversation.events.
func (s *Syncer) HandleConversation(ctx context.Context, evt events.CloudEvent) error {
	if evt.Type == events.TypeSessionEnded {
		return s.handleSessionEnded(ctx, evt)
	}
	switch evt.Type {
	case events.TypeToolInvoked:
	default:
		s.Log.Debug("ignoring conversation event", zap.String("type", evt.Type))
		return nil
	}
	d, err := events.DataAs[events.ToolInvokedData](evt)
	if err != nil {
		return permanent(err)
	}
	// Only AI bookings get a Note (SPEC-CRM §B). voice-agent-runtime emits
	// status "accepted" once the booking command is queued.
	if d.Tool != "book_appointment" || (d.Status != "accepted" && d.Status != "ok") {
		return nil
	}
	// The emitted detail is {offering_id, starts_at}; contact identifiers are
	// optional. Without one we cannot resolve the Person — skip (ack).
	phone, _ := d.Detail["phone"].(string)
	email, _ := d.Detail["email"].(string)
	if phone == "" && email == "" {
		s.Log.Debug("ToolInvoked book_appointment has no contact identifiers; skipping note",
			zap.String("conversation_id", d.ConversationID))
		return nil
	}
	personID, err := s.findPersonForNote(ctx, phone, email)
	if err != nil {
		return fmt.Errorf("find person for AI note: %w", err)
	}
	if personID == "" {
		return nil // person not synced yet; note is best-effort
	}
	if _, err := s.Twenty.CreateNote(ctx, "AI receptionist", twentyc.AIBookingNote, personID); err != nil {
		return fmt.Errorf("create AI booking note: %w", err)
	}
	s.Log.Info("AI booking note added", zap.String("person_id", personID))
	return nil
}

// findPersonForNote resolves a person id by phone/email; "" when not found.
func (s *Syncer) findPersonForNote(ctx context.Context, phone, email string) (string, error) {
	rec, err := s.Twenty.FindPerson(ctx, email, phone)
	if errors.Is(err, twentyc.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return rec.ID, nil
}

// handleSessionEnded turns an enriched SessionEnded event (call-quality
// signals from voice-agent-runtime, app/metrics.py SessionMetrics) into a
// Twenty Note ("📞 AI call summary") on the caller's Person, linked via
// /rest/noteTargets — the same pattern as the AI-booking note above.
//
// Skip+ack (documented) cases — none of these are errors worth a retry:
//   - the event carries no quality object: the session recorded no signals
//     (voice-agent-runtime omits the key entirely in that case);
//   - quality.confirmed_phone is empty: the caller never confirmed a phone
//     number, so no Person can be resolved and an orphaned note would be
//     dropped into the tenant's CRM;
//   - the phone resolves to no Twenty person (lookup + sync_map miss): the
//     contact was never synced; the note is best-effort.
func (s *Syncer) handleSessionEnded(ctx context.Context, evt events.CloudEvent) error {
	d, err := events.DataAs[events.SessionEndedData](evt)
	if err != nil {
		return permanent(err)
	}
	if d.Quality == nil {
		s.Log.Debug("SessionEnded without quality; skipping call-summary note",
			zap.String("conversation_id", d.ConversationID))
		return nil
	}
	phone := strings.TrimSpace(d.Quality.ConfirmedPhone)
	if phone == "" {
		s.Log.Debug("SessionEnded without confirmed_phone; skipping call-summary note",
			zap.String("conversation_id", d.ConversationID))
		return nil
	}
	personID, err := s.resolvePersonForCall(ctx, phone, parseUUID(evt.TenantID))
	if err != nil {
		return fmt.Errorf("resolve person for call summary: %w", err)
	}
	if personID == "" {
		s.Log.Info("no Twenty person for confirmed phone; call-summary note skipped",
			zap.String("conversation_id", d.ConversationID))
		return nil
	}
	noteID, err := s.Twenty.CreateNote(ctx,
		twentyc.CallSummaryNoteTitle, twentyc.CallSummaryNote(*d.Quality), personID)
	if err != nil {
		return fmt.Errorf("create call summary note: %w", err)
	}
	s.Log.Info("AI call summary note added",
		zap.String("person_id", personID),
		zap.String("note_id", noteID),
		zap.String("conversation_id", d.ConversationID))
	return nil
}

// resolvePersonForCall resolves the caller's Person by confirmed phone:
// Twenty people lookup first (existing FindPerson), then the sync_map
// contact_phone mapping written at booking sync (covers phone-format
// mismatches between the confirmed number and the Twenty record).
func (s *Syncer) resolvePersonForCall(ctx context.Context, phone string, tenantUUID *uuid.UUID) (string, error) {
	personID, err := s.findPersonForNote(ctx, phone, "")
	if err != nil {
		return "", err
	}
	if personID != "" {
		return personID, nil
	}
	m, err := s.Map.Get(ctx, syncmap.KindContactPhone, phone, tenantUUID)
	if errors.Is(err, syncmap.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return m.TwentyID, nil
}

func parseUUID(v string) *uuid.UUID {
	if v == "" {
		return nil
	}
	if id, err := uuid.Parse(v); err == nil {
		return &id
	}
	return nil
}

// ---------------------------------------------------------------------------
// GDPR erasure (SPEC-W3 §2 innovation 13)
// ---------------------------------------------------------------------------

// PrivacyEventType is the CloudEvent type emitted by GdprEraseWorkflow.
const PrivacyEventType = "PrivacyEraseRequested"

// PrivacyEraseData is the data payload of PrivacyEraseRequested.
type PrivacyEraseData struct {
	Phone    string `json:"phone"`
	Email    string `json:"email"`
	TenantID string `json:"tenant_id"`
}

// HandlePrivacy processes opendesk.privacy.events: on PrivacyEraseRequested
// it deletes the matching Twenty person (right-to-erasure) and cleans up the
// sync_map rows pointing at it. Unknown persons are acknowledged — there is
// nothing to erase.
func (s *Syncer) HandlePrivacy(ctx context.Context, evt events.CloudEvent) error {
	switch evt.Type {
	case PrivacyEventType, "com.opendesk.privacy." + PrivacyEventType:
	default:
		s.Log.Debug("ignoring privacy event", zap.String("type", evt.Type))
		return nil
	}
	d, err := events.DataAs[PrivacyEraseData](evt)
	if err != nil {
		return permanent(err)
	}
	if d.Phone == "" && d.Email == "" {
		return permanent(fmt.Errorf("PrivacyEraseRequested carries neither phone nor email"))
	}
	rec, err := s.Twenty.FindPerson(ctx, d.Email, d.Phone)
	if errors.Is(err, twentyc.ErrNotFound) {
		s.Log.Info("gdpr erase: no Twenty person for contact; nothing to delete",
			zap.String("event_id", evt.ID))
		return nil
	}
	if err != nil {
		return fmt.Errorf("find person for erase: %w", err)
	}
	if err := s.Twenty.DeletePerson(ctx, rec.ID); err != nil {
		return fmt.Errorf("delete Twenty person %s: %w", rec.ID, err)
	}
	removed, err := s.Map.DeleteByTwentyID(ctx, rec.ID)
	if err != nil {
		return err
	}
	s.Log.Info("gdpr erase: Twenty person deleted",
		zap.String("event_id", evt.ID), zap.String("person_id", rec.ID),
		zap.Int64("sync_map_rows_removed", removed))
	return nil
}
