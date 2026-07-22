package packs

import (
	"os"
	"path/filepath"
	"testing"
)

const validPack = `
id: test-pack
displayName: Test Pack
terminology: {offering: service, team_member: stylist, booking: appointment, contact: client}
agentPersona: |
  You are a test persona.
bookingPolicy: {depositPercent: 30, noShowFeeCents: 2000, phoneConfirmation: true, intakeRequired: false, cancellationWindowHours: 24}
temporalWorkflow: SalonDepositWorkflow
offerings:
- {name: Cut, duration_min: 30, buffer_min: 10, price_cents: 3500, capacity: 1}
reminders: {offsets: ["24h"], channels: ["email"]}
knowledgeSeed:
- {title: Hours, body: "Open 9-5."}
dashboardLabels: {bookingSingular: Appointment, bookingPlural: Appointments, customerTerm: Client}
`

func writePack(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadValidPack(t *testing.T) {
	reg, err := Load(writePack(t, "test.yaml", validPack))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p, ok := reg.Get("test-pack")
	if !ok {
		t.Fatal("pack test-pack not found")
	}
	if p.BookingPolicy.DepositPercent != 30 || len(p.Offerings) != 1 || len(p.KnowledgeSeed) != 1 {
		t.Fatalf("pack parsed incorrectly: %+v", p)
	}
	if !reg.Has("test-pack") || reg.Has("nope") {
		t.Fatal("Registry.Has misbehaves")
	}
}

func TestLoadMissingDirYieldsEmptyRegistry(t *testing.T) {
	reg, err := Load(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Empty registry accepts any industry (provisioning keeps working when
	// the industries dir is not mounted) but serves no pack summary.
	if !reg.Has("salon") {
		t.Fatal("empty registry must accept any industry")
	}
	if _, ok := reg.Get("salon"); ok {
		t.Fatal("empty registry must not serve packs")
	}
}

func TestLoadInvalidPackFails(t *testing.T) {
	bad := `
id: broken
displayName: Broken
terminology: {offering: service}
agentPersona: x
bookingPolicy: {depositPercent: 130, noShowFeeCents: 0, phoneConfirmation: true, intakeRequired: false, cancellationWindowHours: 0}
temporalWorkflow: SalonDepositWorkflow
dashboardLabels: {bookingSingular: A, bookingPlural: B, customerTerm: C}
`
	if _, err := Load(writePack(t, "bad.yaml", bad)); err == nil {
		t.Fatal("expected validation error for depositPercent 130 and missing terminology keys")
	}
}

// TestLoadRepoPacks validates the real industries/ directory at the repo
// root (skipped when the tests run outside the repo layout).
func TestLoadRepoPacks(t *testing.T) {
	const repoPacks = "../../../../industries"
	if _, err := os.Stat(repoPacks); err != nil {
		t.Skip("repo industries/ dir not found")
	}
	reg, err := Load(repoPacks)
	if err != nil {
		t.Fatalf("repo packs must load cleanly: %v", err)
	}
	for _, id := range []string{"salon", "clinic", "consultancy", "support-desk"} {
		if !reg.Has(id) {
			t.Fatalf("repo pack %q missing (loaded: %v)", id, reg.IDs())
		}
	}
}

func TestSummaryMergesTerminologyOverrides(t *testing.T) {
	reg, err := Load(writePack(t, "test.yaml", validPack))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p, _ := reg.Get("test-pack")
	sum := p.Summary(map[string]string{"team_member": "barber", "booking": ""})
	if sum.Terminology["team_member"] != "barber" {
		t.Fatal("tenant override must win over pack terminology")
	}
	if sum.Terminology["booking"] != "appointment" {
		t.Fatal("empty override must not erase pack terminology")
	}
	if sum.Terminology["offering"] != "service" {
		t.Fatal("pack defaults must be preserved")
	}
	if sum.BookingPolicy.DepositPercent != 30 || sum.DashboardLabels["customerTerm"] != "Client" {
		t.Fatal("summary must carry bookingPolicy and dashboardLabels")
	}
}
