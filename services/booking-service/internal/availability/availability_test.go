package availability

import (
	"testing"
	"time"
)

var utc = time.UTC

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, utc)
}

// Monday 2025-01-06, 09:00-12:00 rule, 30min appointments.
func baseParams() Params {
	return Params{
		From:     day(2025, 1, 6), // Monday
		To:       day(2025, 1, 7), // Tuesday
		Duration: 30 * time.Minute,
		Capacity: 1,
		Rules: []Rule{{
			Weekday:  time.Monday,
			StartMin: 9 * 60,
			EndMin:   12 * 60,
		}},
		Location: utc,
	}
}

func slotsOn(p Params, wantStarts ...string) []Slot {
	return Slots(p)
}

func starts(slots []Slot) []string {
	out := make([]string, len(slots))
	for i, s := range slots {
		out[i] = s.StartsAt.Format("15:04")
	}
	return out
}

func equalStr(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSlotsBasicGrid(t *testing.T) {
	got := starts(slotsOn(baseParams()))
	want := []string{"09:00", "09:30", "10:00", "10:30", "11:00", "11:30"}
	if !equalStr(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestSlotsNoRuleForWeekday(t *testing.T) {
	p := baseParams()
	p.Rules[0].Weekday = time.Wednesday
	if got := Slots(p); len(got) != 0 {
		t.Fatalf("expected no slots, got %v", starts(got))
	}
}

func TestSlotsOverlapBlocks(t *testing.T) {
	p := baseParams()
	// existing booking 09:30-10:00 blocks the 09:30 candidate (capacity 1)
	p.Bookings = []Booking{{
		StartsAt: day(2025, 1, 6).Add(9*time.Hour + 30*time.Minute),
		EndsAt:   day(2025, 1, 6).Add(10 * time.Hour),
	}}
	got := starts(Slots(p))
	want := []string{"09:00", "10:00", "10:30", "11:00", "11:30"}
	if !equalStr(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestSlotsPartialOverlapBlocks(t *testing.T) {
	p := baseParams()
	// booking 09:15-09:45 overlaps candidates 09:00 (09:00-09:30) and 09:30 (09:30-10:00)
	p.Bookings = []Booking{{
		StartsAt: day(2025, 1, 6).Add(9*time.Hour + 15*time.Minute),
		EndsAt:   day(2025, 1, 6).Add(9*time.Hour + 45*time.Minute),
	}}
	got := starts(Slots(p))
	want := []string{"10:00", "10:30", "11:00", "11:30"}
	if !equalStr(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestSlotsBufferBlocks(t *testing.T) {
	p := baseParams()
	p.Buffer = 15 * time.Minute
	// booking 10:00-10:30; with a 15min buffer, candidates whose expanded
	// interval intersects [10:00,10:30) are blocked:
	// 09:30 (ends 10:00, +buffer -> 10:15 overlaps) blocked
	// 10:00, 10:30 (overlap/buffer) blocked; 10:45? not on grid. 11:00 ok (starts after 10:30+15=10:45).
	p.Bookings = []Booking{{
		StartsAt: day(2025, 1, 6).Add(10 * time.Hour),
		EndsAt:   day(2025, 1, 6).Add(10*time.Hour + 30*time.Minute),
	}}
	got := starts(Slots(p))
	want := []string{"09:00", "11:00", "11:30"}
	if !equalStr(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestSlotsBufferZeroAllowsAdjacent(t *testing.T) {
	p := baseParams()
	p.Buffer = 0
	p.Bookings = []Booking{{
		StartsAt: day(2025, 1, 6).Add(10 * time.Hour),
		EndsAt:   day(2025, 1, 6).Add(10*time.Hour + 30*time.Minute),
	}}
	got := starts(Slots(p))
	// adjacent 09:30 and 10:30 allowed without buffer
	want := []string{"09:00", "09:30", "10:30", "11:00", "11:30"}
	if !equalStr(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestSlotsCapacityAllowsConcurrent(t *testing.T) {
	p := baseParams()
	p.Capacity = 2
	p.Bookings = []Booking{{
		StartsAt: day(2025, 1, 6).Add(9*time.Hour + 30*time.Minute),
		EndsAt:   day(2025, 1, 6).Add(10 * time.Hour),
	}}
	got := starts(Slots(p))
	// one concurrent booking still leaves room for one more at 09:30
	want := []string{"09:00", "09:30", "10:00", "10:30", "11:00", "11:30"}
	if !equalStr(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestSlotsCapacityExhausted(t *testing.T) {
	p := baseParams()
	p.Capacity = 2
	p.Bookings = []Booking{
		{StartsAt: day(2025, 1, 6).Add(9*time.Hour + 30*time.Minute), EndsAt: day(2025, 1, 6).Add(10 * time.Hour)},
		{StartsAt: day(2025, 1, 6).Add(9*time.Hour + 30*time.Minute), EndsAt: day(2025, 1, 6).Add(10 * time.Hour)},
	}
	got := starts(Slots(p))
	want := []string{"09:00", "10:00", "10:30", "11:00", "11:30"}
	if !equalStr(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestSlotsEffectiveRange(t *testing.T) {
	p := baseParams()
	from := day(2025, 1, 13) // rule starts next Monday
	p.Rules[0].EffectiveFrom = &from
	if got := Slots(p); len(got) != 0 {
		t.Fatalf("expected no slots before effective_from, got %v", starts(got))
	}

	to := day(2025, 1, 6) // rule ends before this Monday (exclusive)
	p.Rules[0].EffectiveFrom = nil
	p.Rules[0].EffectiveTo = &to
	if got := Slots(p); len(got) != 0 {
		t.Fatalf("expected no slots after effective_to, got %v", starts(got))
	}
}

func TestSlotsRespectsSearchRange(t *testing.T) {
	p := baseParams()
	p.From = day(2025, 1, 6).Add(10 * time.Hour)              // from 10:00
	p.To = day(2025, 1, 6).Add(11*time.Hour + 30*time.Minute) // to 11:30
	got := starts(Slots(p))
	want := []string{"10:00", "10:30", "11:00"}
	if !equalStr(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestSlotsStepOverridesGrid(t *testing.T) {
	p := baseParams()
	p.Step = 15 * time.Minute
	got := Slots(p)
	if len(got) != 11 { // 09:00,09:15,...,11:30
		t.Fatalf("expected 11 slots with 15min step, got %v", starts(got))
	}
	if got[1].StartsAt.Format("15:04") != "09:15" {
		t.Fatalf("expected second slot 09:15, got %s", got[1].StartsAt.Format("15:04"))
	}
}

func TestSlotsOverlappingRulesDeduped(t *testing.T) {
	p := baseParams()
	p.Rules = append(p.Rules, Rule{Weekday: time.Monday, StartMin: 9 * 60, EndMin: 10 * 60})
	got := starts(Slots(p))
	want := []string{"09:00", "09:30", "10:00", "10:30", "11:00", "11:30"}
	if !equalStr(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestSlotsTimezoneAware(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("tz data unavailable")
	}
	p := baseParams()
	p.Location = loc
	// From/To span Monday in New York local time
	p.From = time.Date(2025, 1, 6, 0, 0, 0, 0, loc)
	p.To = time.Date(2025, 1, 7, 0, 0, 0, 0, loc)
	got := Slots(p)
	if len(got) != 6 {
		t.Fatalf("expected 6 slots, got %d", len(got))
	}
	if got[0].StartsAt.Location() != loc || got[0].StartsAt.Hour() != 9 {
		t.Fatalf("expected 09:00 in New York, got %v", got[0].StartsAt)
	}
}

func TestSlotsInvalidParams(t *testing.T) {
	if Slots(Params{}) != nil {
		t.Fatal("expected nil for zero duration")
	}
	p := baseParams()
	p.To = p.From
	if Slots(p) != nil {
		t.Fatal("expected nil for empty range")
	}
}

func TestCovers(t *testing.T) {
	rules := []Rule{{Weekday: time.Monday, StartMin: 9 * 60, EndMin: 12 * 60}}
	mon := day(2025, 1, 6)
	if !Covers(rules, utc, mon.Add(9*time.Hour), mon.Add(9*time.Hour+30*time.Minute)) {
		t.Fatal("expected 09:00-09:30 to be covered")
	}
	if Covers(rules, utc, mon.Add(8*time.Hour), mon.Add(9*time.Hour)) {
		t.Fatal("expected 08:00-09:00 to not be covered")
	}
	if Covers(rules, utc, mon.Add(11*time.Hour+45*time.Minute), mon.Add(12*time.Hour+15*time.Minute)) {
		t.Fatal("expected slot crossing window end to not be covered")
	}
	tue := day(2025, 1, 7)
	if Covers(rules, utc, tue.Add(9*time.Hour), tue.Add(9*time.Hour+30*time.Minute)) {
		t.Fatal("expected Tuesday to not be covered by Monday rule")
	}
}

func TestFitsExported(t *testing.T) {
	s := day(2025, 1, 6).Add(9 * time.Hour)
	e := s.Add(30 * time.Minute)
	if !Fits(s, e, 0, 1, nil) {
		t.Fatal("expected empty bookings to fit")
	}
	b := []Booking{{StartsAt: s, EndsAt: e}}
	if Fits(s, e, 0, 1, b) {
		t.Fatal("expected overlapping booking to block at capacity 1")
	}
	if !Fits(s, e, 0, 2, b) {
		t.Fatal("expected capacity 2 to allow one concurrent booking")
	}
	if Fits(s.Add(45*time.Minute), e.Add(45*time.Minute), 30*time.Minute, 1, b) {
		t.Fatal("expected buffer to block adjacent slot")
	}
}
