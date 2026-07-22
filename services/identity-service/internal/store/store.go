// Package store provides Postgres persistence for the identity DB
// (tenants + memberships per SPEC §7).
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned on unique-constraint violations (e.g. slug taken).
var ErrConflict = errors.New("conflict")

// Store wraps a pgx connection pool.
type Store struct {
	pool *pgxpool.Pool
}

// New connects to Postgres and verifies connectivity.
func New(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	s := &Store{pool: pool}
	if err := s.bootstrap(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("bootstrap schema: %w", err)
	}
	return s, nil
}

// bootstrap applies idempotent schema evolution for existing installs (fresh
// installs get the same columns from 02-identity-schema.sql).
func (s *Store) bootstrap(ctx context.Context) error {
	// SPEC-CRM §C1: industry pack id per tenant.
	if _, err := s.pool.Exec(ctx,
		`ALTER TABLE tenants ADD COLUMN IF NOT EXISTS industry TEXT NOT NULL DEFAULT 'salon'`); err != nil {
		return fmt.Errorf("add tenants.industry: %w", err)
	}
	// SPEC-W3 §3 innovation 12: free-form tenant metadata (digital twins
	// carry {"twin_of": "<source slug>"}).
	if _, err := s.pool.Exec(ctx,
		`ALTER TABLE tenants ADD COLUMN IF NOT EXISTS metadata JSONB NOT NULL DEFAULT '{}'`); err != nil {
		return fmt.Errorf("add tenants.metadata: %w", err)
	}
	return nil
}

// Close releases the pool.
func (s *Store) Close() { s.pool.Close() }

// Ping checks database liveness (used by /healthz).
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Tenant mirrors the identity.tenants table.
type Tenant struct {
	ID          uuid.UUID       `json:"id"`
	Slug        string          `json:"slug"`
	Name        string          `json:"name"`
	Timezone    string          `json:"timezone"`
	Currency    string          `json:"currency"`
	Locale      string          `json:"locale"`
	Terminology json.RawMessage `json:"terminology"`
	Plan        string          `json:"plan"`
	Industry    string          `json:"industry"`
	Metadata    json.RawMessage `json:"metadata"`
	CreatedAt   time.Time       `json:"created_at"`
}

// Membership mirrors identity.memberships.
type Membership struct {
	TenantID uuid.UUID `json:"tenant_id"`
	UserID   string    `json:"user_id"`
	Role     string    `json:"role"`
}

// CreateTenant inserts a tenant row.
func (s *Store) CreateTenant(ctx context.Context, t *Tenant) error {
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	if t.Industry == "" {
		t.Industry = "salon"
	}
	if len(t.Metadata) == 0 {
		t.Metadata = json.RawMessage(`{}`)
	}
	const q = `INSERT INTO tenants (id, slug, name, timezone, currency, locale, terminology, plan, industry, metadata)
	           VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING created_at`
	err := s.pool.QueryRow(ctx, q, t.ID, t.Slug, t.Name, t.Timezone, t.Currency, t.Locale, t.Terminology, t.Plan, t.Industry, t.Metadata).
		Scan(&t.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrConflict
		}
		return fmt.Errorf("insert tenant: %w", err)
	}
	return nil
}

// DeleteTenant removes a tenant and its memberships (SPEC-W3 §3 innovation
// 12, digital-twin cleanup). Bookings/conversations/knowledge of the tenant
// are NOT cascaded here — cross-service data expires with the twin's short
// lifetime and is cleaned up by the owning services' own retention (see
// README "Digital twins").
func (s *Store) DeleteTenant(ctx context.Context, slug string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx,
		`DELETE FROM memberships WHERE tenant_id = (SELECT id FROM tenants WHERE slug = $1)`, slug); err != nil {
		return fmt.Errorf("delete memberships: %w", err)
	}
	tag, err := tx.Exec(ctx, `DELETE FROM tenants WHERE slug = $1`, slug)
	if err != nil {
		return fmt.Errorf("delete tenant: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

// GetTenantBySlug fetches a tenant by slug.
func (s *Store) GetTenantBySlug(ctx context.Context, slug string) (Tenant, error) {
	const q = `SELECT id, slug, name, timezone, currency, locale, terminology, plan, industry, metadata, created_at
	           FROM tenants WHERE slug = $1`
	var t Tenant
	err := s.pool.QueryRow(ctx, q, slug).Scan(
		&t.ID, &t.Slug, &t.Name, &t.Timezone, &t.Currency, &t.Locale, &t.Terminology, &t.Plan, &t.Industry, &t.Metadata, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	if err != nil {
		return t, fmt.Errorf("get tenant: %w", err)
	}
	return t, nil
}

// MergeTerminology merge-patches tenant.terminology (jsonb || — patch keys
// win) and returns the resulting terminology document. Used by the onboarding
// ApplyIndustryPack activity via POST /internal/tenants/{slug}/terminology.
func (s *Store) MergeTerminology(ctx context.Context, slug string, patch json.RawMessage) (json.RawMessage, error) {
	const q = `UPDATE tenants
	           SET terminology = COALESCE(terminology, '{}'::jsonb) || $2::jsonb
	           WHERE slug = $1
	           RETURNING terminology`
	var out json.RawMessage
	err := s.pool.QueryRow(ctx, q, slug, patch).Scan(&out)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("merge terminology: %w", err)
	}
	return out, nil
}

// ListMembers returns all memberships for a tenant.
func (s *Store) ListMembers(ctx context.Context, tenantID uuid.UUID) ([]Membership, error) {
	const q = `SELECT tenant_id, user_id, role FROM memberships WHERE tenant_id = $1 ORDER BY user_id`
	rows, err := s.pool.Query(ctx, q, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()
	var out []Membership
	for rows.Next() {
		var m Membership
		if err := rows.Scan(&m.TenantID, &m.UserID, &m.Role); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AddMember inserts a membership row (idempotent on tenant/user pair).
func (s *Store) AddMember(ctx context.Context, m Membership) error {
	const q = `INSERT INTO memberships (tenant_id, user_id, role) VALUES ($1,$2,$3)
	           ON CONFLICT (tenant_id, user_id) DO UPDATE SET role = EXCLUDED.role`
	if _, err := s.pool.Exec(ctx, q, m.TenantID, m.UserID, m.Role); err != nil {
		return fmt.Errorf("add member: %w", err)
	}
	return nil
}
