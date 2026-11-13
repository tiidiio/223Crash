package crash

import (
	"fmt"
	v1 "github.com/tiidiio/223Crash/contracts/product/v1"
)

func EncodeDebitContext(roundID string, panel v1.PanelID, amount int64, cashoutTarget float64) string {
	return fmt.Sprintf("round=%s|panel=%d|amount=%d|autoCashout=%.2f", roundID, panel, amount, cashoutTarget)
}
