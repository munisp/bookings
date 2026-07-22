package bookingops

import "testing"

// SPEC-CRM §C3: deposit = ceil(price_cents * depositPercent/100) from the
// pack policy; unknown policy falls back to the legacy full-price hold
// (DepositKnown=false).
func TestDepositFor(t *testing.T) {
	cases := []struct {
		name      string
		price     int64
		policy    *BookingPolicy
		wantCents int64
		wantKnown bool
	}{
		{"nil policy falls back to full-price hold", 5000, nil, 0, false},
		{"30 percent salon deposit", 5000, &BookingPolicy{DepositPercent: 30}, 1500, true},
		{"ceil rounds up odd cents", 3333, &BookingPolicy{DepositPercent: 30}, 1000, true},
		{"zero percent means no hold", 5000, &BookingPolicy{DepositPercent: 0}, 0, true},
		{"hundred percent holds full price", 5000, &BookingPolicy{DepositPercent: 100}, 5000, true},
		{"percent clamped above 100", 5000, &BookingPolicy{DepositPercent: 140}, 5000, true},
		{"negative percent clamped to zero", 5000, &BookingPolicy{DepositPercent: -5}, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotCents, gotKnown := depositFor(tc.price, tc.policy)
			if gotCents != tc.wantCents || gotKnown != tc.wantKnown {
				t.Fatalf("depositFor(%d, %+v) = (%d, %v), want (%d, %v)",
					tc.price, tc.policy, gotCents, gotKnown, tc.wantCents, tc.wantKnown)
			}
		})
	}
}
