package product

import (
	"fmt"
	v1 "github.com/tiidiio/223Crash/contracts/product/v1"
	"github.com/tiidiio/223Crash/provider"
	"math"
)

const GameID provider.GameID = "223crash"
const CentimesPerUnit int64 = 100

func EncodeDebitContext(roundID string, panel v1.PanelID, amount int64, cashoutTarget float64) string {
	return fmt.Sprintf("round=%s|panel=%d|amount=%d|autoCashout=%.2f", roundID, panel, amount, cashoutTarget)
}
func ToUnits(c int64) float64 {
	return float64(c) / float64(CentimesPerUnit)
}
func FromUnits(u float64) int64 {
	return int64(math.Round(u * float64(CentimesPerUnit)))
}
func PanelToSlot(p v1.PanelID) string {
	if p == v1.PanelA {
		return "A"
	}
	return "B"
}
func SlotToPanel(s string) (v1.PanelID, bool) {
	if s == "A" {
		return v1.PanelA, true
	}
	if s == "B" {
		return v1.PanelB, true
	}
	return 0, false
}
