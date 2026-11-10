package revenue

import (
	"fmt"
	"math"
	"math/bits"
)

// Params : paramètres du calcul d'une période.
type Params struct {
	Totals       Totals
	RateBps      int64
	CarryEnabled bool
	CarryIn      int64 // perte reportée (valeur absolue, >= 0) ; doit être 0 si CarryEnabled=false
}

// Result : résultat du calcul d'une période.
type Result struct {
	GGR        int64 // Stakes - Payouts (peut être négatif)
	Taxable    int64 // base de commission (>= 0)
	Commission int64 // arrondi à l'inférieur
	CarryOut   int64 // perte à reporter (>= 0), toujours 0 si le report est désactivé
}

// Compute calcule GGR et commission. Fonction pure, sans effet de bord.
//
//	GGR      = stakes - payouts
//	base     = GGR - carryIn            (report activé)
//	base     = GGR                      (report désactivé)
//	taxable  = max(base, 0)
//	carryOut = max(-base, 0)            (report activé uniquement)
//	commission = floor(taxable * rateBps / 10000)
func Compute(p Params) (Result, error) {
	if p.Totals.Stakes < 0 || p.Totals.Payouts < 0 {
		return Result{}, fmt.Errorf("%w: negative totals", ErrInvalidInput)
	}
	if p.RateBps < 0 || p.RateBps > BpsDenominator {
		return Result{}, fmt.Errorf("%w: rate must be within 0..%d bps", ErrInvalidInput, BpsDenominator)
	}
	if p.CarryIn < 0 {
		return Result{}, fmt.Errorf("%w: negative carry-in", ErrInvalidInput)
	}
	if !p.CarryEnabled && p.CarryIn != 0 {
		return Result{}, fmt.Errorf("%w: carry-in given while loss carry-forward is disabled", ErrInvalidInput)
	}

	// Stakes et Payouts sont >= 0 : la différence tient dans un int64.
	ggr := p.Totals.Stakes - p.Totals.Payouts

	base := ggr
	if p.CarryEnabled {
		if p.CarryIn > 0 && ggr < math.MinInt64+p.CarryIn {
			return Result{}, ErrOverflow
		}
		base = ggr - p.CarryIn
	}

	res := Result{GGR: ggr}
	if base > 0 {
		res.Taxable = base
	} else if p.CarryEnabled && base < 0 {
		res.CarryOut = -base
	}

	commission, err := mulDivFloor(res.Taxable, p.RateBps, BpsDenominator)
	if err != nil {
		return Result{}, err
	}
	res.Commission = commission
	return res, nil
}

// mulDivFloor calcule floor(a*b/d) sans dépassement intermédiaire (a, b >= 0, d > 0).
func mulDivFloor(a, b, d int64) (int64, error) {
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	if hi >= uint64(d) {
		return 0, ErrOverflow
	}
	q, _ := bits.Div64(hi, lo, uint64(d))
	if q > math.MaxInt64 {
		return 0, ErrOverflow
	}
	return int64(q), nil
}
