package product

import "math"

const CentimesPerUnit = 100

func ToUnits(c int64) float64 { return float64(c) / CentimesPerUnit }
func ToMinor(u float64) int64 { return int64(math.Trunc(u)) * CentimesPerUnit }
func IsIntegerUnits(u float64) bool { return u == math.Trunc(u) }
