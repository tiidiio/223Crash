package game

import "testing"

// Régression : normalizeMultiplier n'était pas idempotente (1.15*100 = 114.99999999999999 → 1.14).
// Cashout normalise puis calculatePayout re-normalise : le joueur était payé 0.01x de moins que le
// multiplicateur enregistré, et une cible d'auto-cashout 1.15 devenait 1.14.

func TestNormalizeMultiplierIdempotentSurToutePlage(t *testing.T) {
	for c := 100; c <= 5000000; c++ { // 1.00 .. 50000.00, bornes de isValidMultiplier
		m := float64(c) / 100
		if got := normalizeMultiplier(m); got != m {
			t.Fatalf("normalizeMultiplier(%v) = %v, attendu %v", m, got, m)
		}
	}
}

func TestNormalizeMultiplierTronqueLesValeursBrutes(t *testing.T) {
	cases := []struct{ in, want float64 }{
		{1.1534, 1.15},
		{1.1999, 1.19},
		{2.0, 2.0},
		{2.0049, 2.0},
		{49.9999, 49.99},
	}
	for _, tc := range cases {
		if got := normalizeMultiplier(tc.in); got != tc.want {
			t.Errorf("normalizeMultiplier(%v) = %v, attendu %v", tc.in, got, tc.want)
		}
	}
}

func TestCalculatePayoutCorrespondAuMultiplicateurAffiche(t *testing.T) {
	cases := []struct {
		amount int64
		mult   float64
		want   int64
	}{
		{10000, 1.15, 11500},
		{10000, 1.14, 11400},
		{10000, 2.01, 20100},
		{10000, 1.00, 10000},
		{25000, 2.30, 57500},
	}
	for _, tc := range cases {
		got, err := calculatePayout(tc.amount, tc.mult)
		if err != nil {
			t.Fatalf("calculatePayout(%d, %v): erreur %v", tc.amount, tc.mult, err)
		}
		if got != tc.want {
			t.Errorf("calculatePayout(%d, %v) = %d, attendu %d", tc.amount, tc.mult, got, tc.want)
		}
	}
}
