package main

import v1 "github.com/tiidiio/223Crash/contracts/product/v1"
import "github.com/tiidiio/223Crash/internal/product"

func (gw *gateway) syncRoundState(c *client) {
	if round, err := gw.eng.CurrentRound(); err == nil && round != nil && round.State == v1.StateBettingOpen {
		serverSeedHash, _, _ := gw.eng.CurrentFairness()
		c.sendJSON(map[string]any{"type": "ROUND_START", "data": map[string]any{
			"roundId":        round.ID,
			"serverSeedHash": serverSeedHash,
			"startsAt":       round.BettingOpenedAt,
		}})
	}
	c.mu.Lock()
	bets := make([]*v1.BetView, 0, len(c.activeBets))
	for _, bet := range c.activeBets {
		bets = append(bets, bet)
	}
	c.mu.Unlock()
	for _, bet := range bets {
		c.sendJSON(map[string]any{"type": "BET_ACCEPTED", "data": map[string]any{
			"slot":   product.PanelToSlot(bet.Panel),
			"amount": product.ToUnits(bet.Amount),
		}})
	}
}
