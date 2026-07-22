package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Booking status values.
const (
	StatusPending   = "pending"
	StatusConfirmed = "confirmed"
	StatusCancelled = "cancelled"
	StatusNoShow    = "no_show"
	StatusCompleted = "completed"
)

// Booking mirrors booking.bookings.
type Booking struct {
	ID             uuid.UUID `json:"id"`
	TenantID       uuid.UUID `json:"tenant_id"`
	OfferingID     uuid.UUID `json:"offering_id"`
	TeamMemberID   uuid.UUID `json:"team_member_id"`
	ContactID      uuid.UUID `json:"contact_id"`
	StartsAt       time.Time `json:"starts_at"`
	EndsAt         time.Time `json:"ends_at"`
	Status         string    `json:"status"`
	Source         string    `json:"source"` // web|voice|api
	IdempotencyKey string    `json:"idempotency_key"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

const bookingCols = `id, tenant_id, offering_id, team_member_id, contact_id, starts_at, ends_at, status, source, idempotency_key, created_at, updated_at`

func scanBooking(row pgx.Row) (Booking, error) {
	var b Booking
	err := row.Scan(&b.ID, &b.TenantID, &b.OfferingID, &b.TeamMemberID, &b.ContactID,
		&b.StartsAt, &b.EndsAt, &b.Status, &b.Source, &b.IdempotencyKey, &b.CreatedAt, &b.UpdatedAt)
	return b, err
}

// OutboxEvent is one transactional-outbox row awaiting dispatch.
// ID is a UUID, matching 01-booking-schema.sql (outbox.id UUID).
type OutboxEvent struct {
	ID          uuid.UUID       `json:"id"`
	AggregateID uuid.UUID       `json:"aggregate_id"`
	Topic       string          `json:"topic"`
	Payload     json.RawMessage `json:"payload"`
}

// ExtraOutbox is an additional outbox row written in the same transaction
// as a booking mutation (e.g. the usage-metering record accompanying
// BookingCreated/BookingConfirmed, Wave 5 #9).
type ExtraOutbox struct {
	Topic   string
	Payload []byte
}

// insertExtraOutbox appends companion outbox rows inside tx.
func insertExtraOutbox(ctx context.Context, tx pgx.Tx, aggregateID uuid.UUID, extra []ExtraOutbox) error {
	for _, e := range extra {
		if e.Topic == "" || e.Payload == nil {
			continue
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO outbox (aggregate_id, topic, payload) VALUES ($1,$2,$3)`,
			aggregateID, e.Topic, e.Payload); err != nil {
			return fmt.Errorf("insert extra outbox: %w", err)
		}
	}
	return nil
}

// CreateBookingTx inserts a booking and its outbox event atomically
// (transactional outbox pattern, SPEC §6/§9). Optional extra outbox rows
// (usage metering) join the same transaction.
func (s *Store) CreateBookingTx(ctx context.Context, b *Booking, outboxTopic string, eventPayload []byte, extra ...ExtraOutbox) error {
	if b.ID == uuid.Nil {
		b.ID = uuid.New()
	}
	return s.withTenant(ctx, b.TenantID, func(tx pgx.Tx) error {
		const ins = `INSERT INTO bookings (` + bookingCols + `)
		             VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,now(),now())
		             RETURNING created_at, updated_at`
		err := tx.QueryRow(ctx, ins, b.ID, b.TenantID, b.OfferingID, b.TeamMemberID, b.ContactID,
			b.StartsAt, b.EndsAt, b.Status, b.Source, b.IdempotencyKey).Scan(&b.CreatedAt, &b.UpdatedAt)
		if err != nil {
			if isUniqueViolation(err) {
				return ErrConflict
			}
			return fmt.Errorf("insert booking: %w", err)
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO outbox (aggregate_id, topic, payload) VALUES ($1,$2,$3)`,
			b.ID, outboxTopic, eventPayload); err != nil {
			return fmt.Errorf("insert outbox: %w", err)
		}
		return insertExtraOutbox(ctx, tx, b.ID, extra)
	})
}

// GetBooking fetches one booking scoped to a tenant.
func (s *Store) GetBooking(ctx context.Context, tenantID, id uuid.UUID) (Booking, error) {
	const q = `SELECT ` + bookingCols + ` FROM bookings WHERE tenant_id=$1 AND id=$2`
	var b Booking
	err := s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		var err error
		b, err = scanBooking(tx.QueryRow(ctx, q, tenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return b, ErrNotFound
	}
	return b, err
}

// GetBookingByIdempotencyKey finds an existing booking for idempotent retries.
func (s *Store) GetBookingByIdempotencyKey(ctx context.Context, tenantID uuid.UUID, key string) (Booking, error) {
	const q = `SELECT ` + bookingCols + ` FROM bookings WHERE tenant_id=$1 AND idempotency_key=$2`
	var b Booking
	err := s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		var err error
		b, err = scanBooking(tx.QueryRow(ctx, q, tenantID, key))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return b, ErrNotFound
	}
	return b, err
}

// BookingFilter narrows ListBookings.
type BookingFilter struct {
	Status       string
	TeamMemberID *uuid.UUID
	From         *time.Time
	To           *time.Time
	// Contact filters bookings to those whose linked contact matches this
	// phone number OR e-mail address (GDPR export lookup, SPEC-W3 §2).
	Contact string
	// ContactID restricts to one contact's bookings (customer portal,
	// Wave 5 #7 — exact id match, no contact-table join needed).
	ContactID *uuid.UUID
	Limit     int
}

// ListBookings returns tenant bookings newest-first, honoring the filter.
func (s *Store) ListBookings(ctx context.Context, tenantID uuid.UUID, f BookingFilter) ([]Booking, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	q := `SELECT ` + bookingCols + ` FROM bookings WHERE tenant_id=$1`
	args := []any{tenantID}
	n := 1
	if f.Status != "" {
		n++
		q += fmt.Sprintf(` AND status=$%d`, n)
		args = append(args, f.Status)
	}
	if f.TeamMemberID != nil {
		n++
		q += fmt.Sprintf(` AND team_member_id=$%d`, n)
		args = append(args, *f.TeamMemberID)
	}
	if f.Contact != "" {
		n++
		q += fmt.Sprintf(` AND contact_id IN (SELECT id FROM contacts WHERE tenant_id=$1 AND (phone=$%[1]d OR email=$%[1]d))`, n)
		args = append(args, f.Contact)
	}
	if f.ContactID != nil {
		n++
		q += fmt.Sprintf(` AND contact_id=$%d`, n)
		args = append(args, *f.ContactID)
	}
	if f.From != nil {
		n++
		q += fmt.Sprintf(` AND starts_at >= $%d`, n)
		args = append(args, *f.From)
	}
	if f.To != nil {
		n++
		q += fmt.Sprintf(` AND starts_at < $%d`, n)
		args = append(args, *f.To)
	}
	n++
	q += fmt.Sprintf(` ORDER BY starts_at DESC LIMIT $%d`, n)
	args = append(args, f.Limit)

	var out []Booking
	err := s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			b, err := scanBooking(rows)
			if err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}

// ListBookingsForRange returns active (non-cancelled) bookings overlapping
// [from,to) for one team member — input to the availability engine.
func (s *Store) ListBookingsForRange(ctx context.Context, tenantID, teamMemberID uuid.UUID, from, to time.Time) ([]Booking, error) {
	const q = `SELECT ` + bookingCols + ` FROM bookings
	           WHERE tenant_id=$1 AND team_member_id=$2
	             AND status NOT IN ('cancelled')
	             AND starts_at < $4 AND ends_at > $3
	           ORDER BY starts_at`
	var out []Booking
	err := s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, tenantID, teamMemberID, from, to)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			b, err := scanBooking(rows)
			if err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}

// SetBookingStatus updates status (+updated_at) and appends an outbox event
// atomically. eventPayload may be nil to skip the outbox write. Optional
// extra outbox rows (usage metering) join the same transaction.
func (s *Store) SetBookingStatus(ctx context.Context, tenantID, id uuid.UUID, status, outboxTopic string, eventPayload []byte, extra ...ExtraOutbox) error {
	return s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE bookings SET status=$3, updated_at=now() WHERE tenant_id=$1 AND id=$2`,
			tenantID, id, status)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if eventPayload != nil {
			if _, err := tx.Exec(ctx,
				`INSERT INTO outbox (aggregate_id, topic, payload) VALUES ($1,$2,$3)`,
				id, outboxTopic, eventPayload); err != nil {
				return fmt.Errorf("insert outbox: %w", err)
			}
		}
		return insertExtraOutbox(ctx, tx, id, extra)
	})
}

// RescheduleBooking moves a booking to new times (+outbox event) atomically.
func (s *Store) RescheduleBooking(ctx context.Context, tenantID, id uuid.UUID, startsAt, endsAt time.Time, outboxTopic string, eventPayload []byte) error {
	return s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE bookings SET starts_at=$3, ends_at=$4, updated_at=now()
			 WHERE tenant_id=$1 AND id=$2 AND status != 'cancelled'`,
			tenantID, id, startsAt, endsAt)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO outbox (aggregate_id, topic, payload) VALUES ($1,$2,$3)`,
			id, outboxTopic, eventPayload); err != nil {
			return fmt.Errorf("insert outbox: %w", err)
		}
		return nil
	})
}

// AnonymizeContacts applies the GDPR right-to-erasure tombstone to every
// contact of the tenant matching the given phone number or e-mail address
// (SPEC-W3 §2 innovation 13): name is replaced with 'erased' and phone/email
// with their salted SHA-256 hashes so booking history stays referentially
// intact while PII is irreversibly pseudonymized. Returns rows affected.
// Empty phone/email values are ignored (never match NULL/” columns).
func (s *Store) AnonymizeContacts(ctx context.Context, tenantID uuid.UUID, phone, email string) (int64, error) {
	if phone == "" && email == "" {
		return 0, nil
	}
	args := []any{tenantID}
	bind := func(v any) int {
		args = append(args, v)
		return len(args)
	}
	sets := []string{`name='erased'`, `notes=''`}
	var conds []string
	if phone != "" {
		sets = append(sets, fmt.Sprintf(`phone=$%d`, bind(hashPii(phone))))
		conds = append(conds, fmt.Sprintf(`phone=$%d`, bind(phone)))
	}
	if email != "" {
		sets = append(sets, fmt.Sprintf(`email=$%d`, bind(hashPii(email))))
		conds = append(conds, fmt.Sprintf(`email=$%d`, bind(email)))
	}
	q := `UPDATE contacts SET ` + strings.Join(sets, ", ") +
		` WHERE tenant_id=$1 AND name <> 'erased' AND (` + strings.Join(conds, ` OR `) + `)`
	var affected int64
	err := s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, q, args...)
		if err != nil {
			return err
		}
		affected = tag.RowsAffected()
		return nil
	})
	return affected, err
}

// hashPii returns the deterministic SHA-256 tombstone for a PII value
// ("sha256:<hex>"), or "" for empty input.
func hashPii(v string) string {
	if v == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("opendesk-gdpr-erase:" + v))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// FetchUnsentOutbox returns up to limit undelivered outbox rows.
//
// NOTE (RLS): the outbox dispatcher is a cross-tenant background path — it
// drains events for ALL tenants, so it must run as the privileged role and
// intentionally does NOT set app.tenant_id. The outbox table carries no RLS
// policy (see 01-booking-schema.sql).
func (s *Store) FetchUnsentOutbox(ctx context.Context, limit int) ([]OutboxEvent, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, aggregate_id, topic, payload FROM outbox WHERE sent_at IS NULL ORDER BY id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OutboxEvent
	for rows.Next() {
		var e OutboxEvent
		if err := rows.Scan(&e.ID, &e.AggregateID, &e.Topic, &e.Payload); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// MarkOutboxSent marks an outbox row dispatched.
//
// NOTE (RLS): cross-tenant dispatcher path — see FetchUnsentOutbox.
func (s *Store) MarkOutboxSent(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE outbox SET sent_at=now() WHERE id=$1`, id)
	return err
}

// ---------------------------------------------------------------------------
// Public sites
// ---------------------------------------------------------------------------

// Site is a published public booking page (see package doc).
type Site struct {
	ID          uuid.UUID       `json:"id"`
	TenantID    uuid.UUID       `json:"tenant_id"`
	TenantSlug  string          `json:"tenant_slug"`
	Slug        string          `json:"slug"`
	DisplayName string          `json:"display_name"`
	Published   bool            `json:"published"`
	Theme       json.RawMessage `json:"theme"`
	CreatedAt   time.Time       `json:"created_at"`
}

// GetSiteBySlug resolves a published site by slug. Tenant-safe by
// construction: the site's tenant_id scopes every downstream query.
//
// NOTE (RLS): this is the public anonymous entry point — the tenant is not
// known until the slug resolves, so it runs without app.tenant_id. The
// `sites` table has no RLS policy (created by ensureSitesTable); every query
// AFTER this resolution is tenant-scoped via withTenant.
func (s *Store) GetSiteBySlug(ctx context.Context, slug string) (Site, error) {
	var site Site
	err := s.pool.QueryRow(ctx,
		`SELECT id, tenant_id, tenant_slug, slug, display_name, published, theme, created_at
		 FROM sites WHERE slug=$1 AND published`, slug).
		Scan(&site.ID, &site.TenantID, &site.TenantSlug, &site.Slug, &site.DisplayName, &site.Published, &site.Theme, &site.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return site, ErrNotFound
	}
	return site, err
}

// GetSiteByTenant returns the tenant's site (any publish state), or
// ErrNotFound when the tenant has none yet.
func (s *Store) GetSiteByTenant(ctx context.Context, tenantID uuid.UUID) (Site, error) {
	var site Site
	err := s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT id, tenant_id, tenant_slug, slug, display_name, published, theme, created_at
			 FROM sites WHERE tenant_id=$1 ORDER BY created_at LIMIT 1`, tenantID).
			Scan(&site.ID, &site.TenantID, &site.TenantSlug, &site.Slug, &site.DisplayName, &site.Published, &site.Theme, &site.CreatedAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return site, ErrNotFound
	}
	return site, err
}

// UpdateSite updates mutable site fields (display_name, published, theme).
// The slug is immutable once created.
func (s *Store) UpdateSite(ctx context.Context, site *Site) error {
	return s.withTenant(ctx, site.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE sites SET display_name=$3, published=$4, theme=$5 WHERE tenant_id=$1 AND id=$2`,
			site.TenantID, site.ID, site.DisplayName, site.Published, site.Theme)
		if err != nil {
			return fmt.Errorf("update site: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// CreateSite inserts a public site row; idempotent on slug.
func (s *Store) CreateSite(ctx context.Context, site *Site) error {
	if len(site.Theme) == 0 {
		site.Theme = json.RawMessage(`{}`)
	}
	const q = `INSERT INTO sites (tenant_id, tenant_slug, slug, display_name, theme)
	           VALUES ($1,$2,$3,$4,$5)
	           ON CONFLICT (slug) DO UPDATE SET display_name = EXCLUDED.display_name
	           RETURNING id, published, theme, created_at`
	return s.withTenant(ctx, site.TenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, q, site.TenantID, site.TenantSlug, site.Slug, site.DisplayName, site.Theme).
			Scan(&site.ID, &site.Published, &site.Theme, &site.CreatedAt)
		if err != nil {
			return fmt.Errorf("insert site: %w", err)
		}
		return nil
	})
}
