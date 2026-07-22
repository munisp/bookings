// Package optimize ranks candidate booking slots to minimize calendar
// fragmentation (Wave 5 #4): prefer slots that pack back-to-back against
// existing bookings or fill a gap exactly, and penalize slots that leave a
// gap too small to ever be booked again. It is a pure function over the
// availability engine's output — no I/O — and exhaustively unit-tested.
package optimize

import (
	"fmt"
	"sort"
	"time"

	"github.com/opendesk/booking-service/internal/availability"
)

// Scoring weights. Base is the score of an isolated slot with bookable room
// on both sides; bonuses/penalties are applied per day-edge against the
// nearest existing booking.
const (
	baseScore       = 100.0
	backToBackBonus = 25.0 // adjacent (only the buffer between) to a booking
	exactFillBonus  = 30.0 // fills a gap exactly on BOTH sides
	fragmentPenalty = 50.0 // leaves an unbookable sliver of free time
)

// Params configures the scoring of one day's candidates.
type Params struct {
	// Buffer is the enforced separation between distinct appointments
	// (same value the availability engine uses). Two bookings are
	// "back-to-back" when exactly Buffer sits between them.
	Buffer time.Duration
	// MinUsefulGap is the smallest free interval that can still host a
	// future appointment. Gaps strictly smaller than this (but > 0) are
	// unbookable fragments and are penalized. <= 0 defaults to
	// Duration + Buffer — i.e. room for one more appointment of the same
	// offering.
	MinUsefulGap time.Duration
	// Duration of the candidate appointments; only used to derive the
	// default MinUsefulGap.
	Duration time.Duration
}

// ScoredSlot is a candidate slot with its fragmentation score and a
// human-readable explanation for the ranking.
type ScoredSlot struct {
	StartsAt time.Time `json:"starts_at"`
	EndsAt   time.Time `json:"ends_at"`
	Score    float64   `json:"score"`
	Reason   string    `json:"reason"`
}

// Score evaluates every candidate against the day's existing bookings and
// returns one ScoredSlot per candidate, sorted by score descending (ties
// broken chronologically). Candidates are assumed to already satisfy the
// engine's fit constraints (no overlap, buffer respected).
func Score(bookings []availability.Booking, candidates []availability.Slot, p Params) []ScoredSlot {
	if len(candidates) == 0 {
		return nil
	}
	if p.MinUsefulGap <= 0 {
		p.MinUsefulGap = p.Duration + p.Buffer
	}
	sorted := make([]availability.Booking, len(bookings))
	copy(sorted, bookings)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].StartsAt.Before(sorted[j].StartsAt) })

	out := make([]ScoredSlot, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, score(c, sorted, p))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].StartsAt.Before(out[j].StartsAt)
	})
	return out
}

// Top returns the n highest-scoring candidates (all of them when n <= 0 or
// n >= len(candidates)).
func Top(bookings []availability.Booking, candidates []availability.Slot, p Params, n int) []ScoredSlot {
	scored := Score(bookings, candidates, p)
	if n <= 0 || n >= len(scored) {
		return scored
	}
	return scored[:n]
}

// score evaluates one candidate against its nearest neighbours.
func score(c availability.Slot, bookings []availability.Booking, p Params) ScoredSlot {
	// remBefore/remAfter measure the free time left on each side of the
	// candidate after accounting for the mandatory buffer: rem == 0 means
	// back-to-back, 0 < rem < MinUsefulGap means an unbookable fragment.
	var prev, next *availability.Booking
	for i := range bookings {
		b := &bookings[i]
		if !b.EndsAt.After(c.StartsAt) { // b ends at or before the candidate
			if prev == nil || b.EndsAt.After(prev.EndsAt) {
				prev = b
			}
		}
		if !c.EndsAt.After(b.StartsAt) { // b starts at or after the candidate
			if next == nil || b.StartsAt.Before(next.StartsAt) {
				next = b
			}
		}
	}

	s := baseScore
	var notes []string

	remBefore, hasPrev := remainingBefore(prev, p.Buffer, c.StartsAt)
	remAfter, hasNext := remainingAfter(next, p.Buffer, c.EndsAt)

	adjacent := 0
	if hasPrev {
		s, adjacent = applySide(s, remBefore, p, "before", &notes, adjacent)
	}
	if hasNext {
		s, adjacent = applySide(s, remAfter, p, "after", &notes, adjacent)
	}
	if adjacent == 2 {
		s += exactFillBonus
		notes = append(notes, "fills the gap between two bookings exactly")
	}

	reason := "open slot with bookable room around it"
	if len(notes) > 0 {
		reason = joinNotes(notes)
	}
	return ScoredSlot{StartsAt: c.StartsAt, EndsAt: c.EndsAt, Score: s, Reason: reason}
}

// applySide scores one day-edge (before or after the candidate) and appends
// an explanatory note. It returns the updated score and adjacent count.
func applySide(s float64, rem time.Duration, p Params, side string, notes *[]string, adjacent int) (float64, int) {
	switch {
	case rem == 0:
		*notes = append(*notes, "packs back-to-back "+side+" an existing booking")
		return s + backToBackBonus, adjacent + 1
	case rem < p.MinUsefulGap:
		*notes = append(*notes, fmt.Sprintf("leaves an unbookable %s gap %s", rem, side))
		return s - fragmentPenalty, adjacent
	default:
		return s, adjacent
	}
}

// remainingBefore returns the free interval between the previous booking's
// end (plus buffer) and the candidate start. Negative values clamp to 0
// (back-to-back).
func remainingBefore(b *availability.Booking, buffer time.Duration, start time.Time) (time.Duration, bool) {
	if b == nil {
		return 0, false
	}
	d := start.Sub(b.EndsAt.Add(buffer))
	if d < 0 {
		d = 0
	}
	return d, true
}

// remainingAfter returns the free interval between the candidate end and the
// next booking's start (minus buffer). Negative values clamp to 0.
func remainingAfter(b *availability.Booking, buffer time.Duration, end time.Time) (time.Duration, bool) {
	if b == nil {
		return 0, false
	}
	d := b.StartsAt.Add(-buffer).Sub(end)
	if d < 0 {
		d = 0
	}
	return d, true
}

func joinNotes(notes []string) string {
	out := notes[0]
	for _, n := range notes[1:] {
		out += "; " + n
	}
	return out
}
