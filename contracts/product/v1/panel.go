package v1

// Panel = slot A/B côté UI crash, distinct du BetRequest financier
type BetSlot string

const (
	SlotA BetSlot = "A"
	SlotB BetSlot = "B"
)

type PanelBetRequest struct {
	BetID       string   `json:"betId"`
	Slot        BetSlot  `json:"slot"`
	Amount      int64    `json:"amount"` // minor units
	AutoCashout *float64 `json:"autoCashout,omitempty"`
}

type PanelBetAccepted struct {
	RoundID string  `json:"roundId"`
	BetID   string  `json:"betId"`
	Slot    BetSlot `json:"slot"`
	Amount  int64   `json:"amount"`
}

type PanelCashoutRequest struct {
	Slot BetSlot `json:"slot"`
}

type PanelCashoutResult struct {
	BetID      string  `json:"betId"`
	Multiplier float64 `json:"multiplier"` // serveur autoritaire
	Payout     int64   `json:"payout"`
}

func (s BetSlot) ToPanelID() PanelID {
	if s == SlotA {
		return PanelA
	}
	return PanelB
}
