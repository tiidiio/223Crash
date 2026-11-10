package product

import (
	"fmt"
	v1 "github.com/tiidiio/223Gaming/contracts/product/v1"
	"github.com/tiidiio/223Gaming/provider"
)

const GameID provider.GameID = "223crash"

func EncodeDebitContext(roundID string, panel v1.PanelID, amount int64, cashoutTarget float64) string {
	return fmt.Sprintf("round=%s|panel=%d|amount=%d|autoCashout=%.2f", roundID, panel, amount, cashoutTarget)
}
func PanelToSlot(p v1.PanelID) string {
	switch p {
	case v1.PanelA: return "A"
	case v1.PanelB: return "B"
	default: return "?"
	}
}
func SlotToPanel(s string) (v1.PanelID, bool) {
	switch s {
	case "A": return v1.PanelA, true
	case "B": return v1.PanelB, true
	default: return 0, false
	}
}
