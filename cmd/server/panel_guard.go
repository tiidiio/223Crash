package main

import (
	"errors"
	v1 "github.com/tiidiio/223Gaming/contracts/product/v1"
	"github.com/tiidiio/223Gaming/internal/product"
)

var errPanelOccupied = errors.New("panel déjà occupé")

func (gw *gateway) announceExistingBet(c *client, panel v1.PanelID, slot string) {
	existing, ok, err := gw.eng.GetPanelBet(c.engineID(), panel)
	if err != nil || !ok { return }
	if existing.State != v1.BetPending { return }
	b := existing
	c.mu.Lock()
	c.activeBets[panel] = &b
	c.mu.Unlock()
	c.sendJSON(map[string]any{"type": "BET_ACCEPTED", "data": map[string]any{
		"slot":   slot,
		"amount": product.ToUnits(b.Amount),
	}})
}
