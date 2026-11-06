package engine

import (
	"math"
	"math/rand"
	"testing"
)

func TestCrashFromX_Bounds(t *testing.T) {
	cases := []struct {
		name string
		x    float64
		want float64
	}{
		{"x=0 : plancher 1.00", 0, 1.00},
		{"x=0.5", 0.5, 1.94},
		{"x=0.9999 : plafond", 0.9999, MaxCrashPoint},
		{"x maximal (1-2^-52) : plafond", 1 - 1.0/float64(uint64(1)<<52), MaxCrashPoint},
	}
	for _, c := range cases {
		if got := crashFromX(c.x); got != c.want {
			t.Errorf("%s : crashFromX(%v) = %v, attendu %v", c.name, c.x, got, c.want)
		}
	}
}

func TestCrashPoint_NeverExceedsCap(t *testing.T) {
	const rounds = 200000
	capped := 0
	for n := int64(0); n < rounds; n++ {
		cp, err := CrashPoint("test-server-seed", "test-client-seed", n)
		if err != nil {
			t.Fatalf("nonce %d : %v", n, err)
		}
		if cp < 1.00 || cp > MaxCrashPoint {
			t.Fatalf("nonce %d : crash point %v hors de [1.00, %v]", n, cp, MaxCrashPoint)
		}
		if cp == MaxCrashPoint {
			capped++
		}
	}
	if capped == 0 {
		t.Fatalf("aucun round plafonné sur %d : le test n'exerce pas le plafond", rounds)
	}
	t.Logf("rounds plafonnés à %.0fx : %d / %d", MaxCrashPoint, capped, rounds)
}

// Le plafond ne doit pas modifier le RTP pour une cible <= MaxCrashPoint.
func TestCrashFromX_RTPUnaffectedByCap(t *testing.T) {
	const samples = 2000000
	targets := []float64{1.5, 2, 10}
	wins := make([]int, len(targets))
	r := rand.New(rand.NewSource(1))
	for i := 0; i < samples; i++ {
		c := crashFromX(r.Float64())
		for j, tg := range targets {
			if c >= tg {
				wins[j]++
			}
		}
	}
	for j, tg := range targets {
		rtp := tg * float64(wins[j]) / samples
		if math.Abs(rtp-0.97) > 0.01 {
			t.Errorf("cible %.1fx : RTP mesuré %.4f, attendu 0.97 (tolérance 0.01)", tg, rtp)
		}
		t.Logf("cible %.1fx : RTP mesuré %.4f", tg, rtp)
	}
}
