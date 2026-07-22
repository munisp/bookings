package optimize

import (
	"strings"
	"testing"
	"time"

	"github.com/opendesk/booking-service/internal/availability"
)

func at(hour, min int) time.Time {
	return time.Date(2025, 7, 14, hour, min, 0, 0, time.UTC) // a Monday
}

func slot(hour, min int) availability.Slot {
	return availability.Slot{StartsAt: at(hour, min), EndsAt: at(hour, min).Add(30 * time.Minute)}
}

func booking(hour, min int) availability.Booking {
	return availability.Booking{StartsAt: at(hour, min), EndsAt: at(hour, min).Add(30 * time.Minute)}
}

// 30-minute appointments, no buffer → MinUsefulGap defaults to 30m.
var testParams = Params{Duration: 30 * time.Minute}

func byStart(scored []ScoredSlot, t time.Time) ScoredSlot {
	for _, s := range scored {
		if s.StartsAt.Equal(t) {
			return s
		}
	}
	return ScoredSlot{Score: -1e9, Reason: "not found"}
}

func TestExactGapFillScoresBest(t *testing.T) {
	// Bookings 09:00-09:30 and 10:00-10:30 leave a single 30m gap at 09:30.
	bookings := []availability.Booking{booking(9, 0), booking(10, 0)}
	candidates := []availability.Slot{slot(9, 30), slot(11, 0)}

	scored := Score(bookings, candidates, testParams)
	if len(scored) != 2 {
		t.Fatalf("scored %d candidates, want 2", len(scored))
	}
	best := scored[0]
	if !best.StartsAt.Equal(at(9, 30)) {
		t.Fatalf("best slot = %v, want 09:30 (reason %q)", best.StartsAt, best.Reason)
	}
	if best.Score != baseScore+2*backToBackBonus+exactFillBonus {
		t.Fatalf("exact-fill score = %v", best.Score)
	}
	if !strings.Contains(best.Reason, "exactly") {
		t.Fatalf("reason = %q, want exact-fill explanation", best.Reason)
	}
	// The isolated 11:00 slot keeps the base score.
	if iso := byStart(scored, at(11, 0)); iso.Score != baseScore {
		t.Fatalf("isolated slot score = %v, want %v", iso.Score, baseScore)
	}
}

func TestBackToBackRewarded(t *testing.T) {
	// Booking 09:00-09:30; candidate 09:30 packs directly after it, 11:00 is
	// isolated.
	bookings := []availability.Booking{booking(9, 0)}
	candidates := []availability.Slot{slot(9, 30), slot(11, 0)}

	scored := Score(bookings, candidates, testParams)
	if !scored[0].StartsAt.Equal(at(9, 30)) {
		t.Fatalf("best slot = %v, want 09:30", scored[0].StartsAt)
	}
	if scored[0].Score != baseScore+backToBackBonus {
		t.Fatalf("back-to-back score = %v", scored[0].Score)
	}
	if !strings.Contains(scored[0].Reason, "back-to-back") {
		t.Fatalf("reason = %q", scored[0].Reason)
	}
	if scored[0].Score <= scored[1].Score {
		t.Fatalf("back-to-back (%v) must outrank isolated (%v)", scored[0].Score, scored[1].Score)
	}
}

func TestUnbookableGapPenalized(t *testing.T) {
	// Booking 09:00-09:30. Candidate 09:45 leaves a 15m gap before it — too
	// small for another 30m appointment (MinUsefulGap defaults to 30m).
	bookings := []availability.Booking{booking(9, 0)}
	candidates := []availability.Slot{slot(9, 45), slot(9, 30), slot(10, 0)}

	scored := Score(bookings, candidates, testParams)
	frag := byStart(scored, at(9, 45))
	if frag.Score != baseScore-fragmentPenalty {
		t.Fatalf("fragmenting slot score = %v, want %v", frag.Score, baseScore-fragmentPenalty)
	}
	if !strings.Contains(frag.Reason, "unbookable") {
		t.Fatalf("reason = %q, want unbookable-gap explanation", frag.Reason)
	}
	// 09:30 (back-to-back) and 10:00 (exactly one useful 30m gap remains —
	// still bookable) must both outrank the fragmenting slot.
	for _, want := range []time.Time{at(9, 30), at(10, 0)} {
		if s := byStart(scored, want); s.Score <= frag.Score {
			t.Fatalf("slot %v score %v should outrank fragmenting %v", want, s.Score, frag.Score)
		}
	}
}

func TestBufferSeparatesBackToBack(t *testing.T) {
	// With a 10m buffer, "back-to-back" means 10m between appointments.
	// Booking 09:00-09:30 → candidate 09:40 is back-to-back; 09:50 leaves a
	// 10m fragment (MinUsefulGap defaults to 30m+10m).
	p := Params{Duration: 30 * time.Minute, Buffer: 10 * time.Minute}
	bookings := []availability.Booking{booking(9, 0)}
	candidates := []availability.Slot{slot(9, 40), slot(9, 50)}

	scored := Score(bookings, candidates, p)
	if !scored[0].StartsAt.Equal(at(9, 40)) {
		t.Fatalf("best slot = %v, want 09:40 (buffer-adjusted back-to-back)", scored[0].StartsAt)
	}
	if scored[0].Score != baseScore+backToBackBonus {
		t.Fatalf("score = %v", scored[0].Score)
	}
	if frag := byStart(scored, at(9, 50)); frag.Score >= scored[0].Score {
		t.Fatalf("10m fragment (%v) must lose to back-to-back (%v)", frag.Score, scored[0].Score)
	}
}

func TestUsefulGapRemainingIsNeutral(t *testing.T) {
	// Candidate leaves exactly MinUsefulGap (30m) before the next booking —
	// room for one more appointment, so neither bonus nor penalty.
	bookings := []availability.Booking{booking(10, 30)}
	candidates := []availability.Slot{slot(9, 30)} // 30m gap to 10:30 booking
	scored := Score(bookings, candidates, testParams)
	if scored[0].Score != baseScore {
		t.Fatalf("score = %v, want neutral %v", scored[0].Score, baseScore)
	}
}

func TestEmptyDayKeepsChronologicalOrder(t *testing.T) {
	candidates := []availability.Slot{slot(9, 0), slot(9, 30), slot(10, 0)}
	scored := Score(nil, candidates, testParams)
	for i, s := range scored {
		if s.Score != baseScore {
			t.Fatalf("slot %d score = %v, want base", i, s.Score)
		}
		if !s.StartsAt.Equal(candidates[i].StartsAt) {
			t.Fatalf("slot %d order changed: %v", i, s.StartsAt)
		}
	}
}

func TestTopReturnsHighestN(t *testing.T) {
	bookings := []availability.Booking{booking(9, 0), booking(10, 0)}
	candidates := []availability.Slot{slot(9, 30), slot(11, 0), slot(12, 0), slot(13, 0)}
	top := Top(bookings, candidates, testParams, 3)
	if len(top) != 3 {
		t.Fatalf("Top(3) returned %d", len(top))
	}
	if !top[0].StartsAt.Equal(at(9, 30)) {
		t.Fatalf("top[0] = %v, want exact fill 09:30", top[0].StartsAt)
	}
	// Every returned score must be >= every omitted one.
	all := Score(bookings, candidates, testParams)
	if top[len(top)-1].Score < all[len(top)].Score {
		t.Fatalf("Top cut in the wrong place: kept %v, omitted %v", top[len(top)-1].Score, all[len(top)].Score)
	}
	if got := Top(bookings, candidates, testParams, 99); len(got) != len(candidates) {
		t.Fatalf("Top(>len) returned %d, want all %d", len(got), len(candidates))
	}
	if Score(bookings, nil, testParams) != nil {
		t.Fatal("no candidates must yield nil")
	}
}

func TestCustomMinUsefulGap(t *testing.T) {
	// With MinUsefulGap = 15m, a 15m leftover is useful → neutral, 10m is not.
	p := Params{Duration: 30 * time.Minute, MinUsefulGap: 15 * time.Minute}
	bookings := []availability.Booking{booking(9, 0)}          // ends 09:30
	ok := Score(bookings, []availability.Slot{slot(9, 45)}, p) // leaves exactly 15m
	if ok[0].Score != baseScore {
		t.Fatalf("15m leftover score = %v, want neutral", ok[0].Score)
	}
	bad := Score(bookings, []availability.Slot{slot(9, 40)}, p) // leaves 10m
	if bad[0].Score != baseScore-fragmentPenalty {
		t.Fatalf("10m leftover score = %v, want penalized", bad[0].Score)
	}
}
