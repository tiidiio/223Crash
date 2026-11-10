package revenue

import (
	"errors"
	"math"
	"testing"
)

func TestCompute(t *testing.T) {
	cases := []struct {
		name    string
		p       Params
		want    Result
		wantErr error
	}{
		{"ggr positif 10%", Params{Totals{1_000_000, 940_000}, 1000, false, 0}, Result{60_000, 60_000, 6_000, 0}, nil},
		{"arrondi inférieur", Params{Totals{105, 0}, 1000, false, 0}, Result{105, 105, 10, 0}, nil},
		{"ggr nul", Params{Totals{500, 500}, 1000, false, 0}, Result{0, 0, 0, 0}, nil},
		{"ggr négatif sans report", Params{Totals{400, 900}, 1000, false, 0}, Result{-500, 0, 0, 0}, nil},
		{"ggr négatif avec report", Params{Totals{400, 900}, 1000, true, 0}, Result{-500, 0, 0, 500}, nil},
		{"report absorbé partiellement", Params{Totals{1000, 0}, 1000, true, 300}, Result{1000, 700, 70, 0}, nil},
		{"report non épuisé", Params{Totals{100, 0}, 1000, true, 300}, Result{100, 0, 0, 200}, nil},
		{"taux 0", Params{Totals{1000, 0}, 0, false, 0}, Result{1000, 1000, 0, 0}, nil},
		{"taux 100%", Params{Totals{1000, 0}, 10000, false, 0}, Result{1000, 1000, 1000, 0}, nil},
		{"taux > 100%", Params{Totals{1000, 0}, 10001, false, 0}, Result{}, ErrInvalidInput},
		{"taux négatif", Params{Totals{1000, 0}, -1, false, 0}, Result{}, ErrInvalidInput},
		{"totaux négatifs", Params{Totals{-1, 0}, 1000, false, 0}, Result{}, ErrInvalidInput},
		{"carry-in sans report activé", Params{Totals{1000, 0}, 1000, false, 5}, Result{}, ErrInvalidInput},
		{"carry-in négatif", Params{Totals{1000, 0}, 1000, true, -5}, Result{}, ErrInvalidInput},
		{"gros montant sans overflow", Params{Totals{math.MaxInt64, 0}, 10000, false, 0}, Result{math.MaxInt64, math.MaxInt64, math.MaxInt64, 0}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Compute(tc.p)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil && got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestMulDivFloorOverflow(t *testing.T) {
	// a*b/d dépasse int64 : doit être détecté.
	if _, err := mulDivFloor(math.MaxInt64, math.MaxInt64, 2); !errors.Is(err, ErrOverflow) {
		t.Fatalf("expected ErrOverflow, got %v", err)
	}
}
