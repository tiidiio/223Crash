package main

import "github.com/tiidiio/223Gaming/internal/game"

// syncRoundState aligne un client qui vient de se (re)connecter sur l'état courant du jeu.
//
// Le client n'apprend la phase « mises ouvertes » que via le broadcast ROUND_START (émis une seule
// fois à l'ouverture des mises, fenêtre de 5 s) et n'active une mise que sur BET_ACCEPTED. Sans cette
// synchro, un joueur connecté pendant la fenêtre de mise ne peut pas miser avant le round suivant, et
// un joueur qui recharge sa page en plein vol ne voit pas sa mise vivante (donc ne peut pas encaisser).
//
// Ordre imposé : ROUND_START d'abord (il remet les mises à zéro côté client), BET_ACCEPTED ensuite.
// Ne crée aucun nouveau type de message : mêmes formats que le broadcast et que handleBet.
func (gw *gateway) syncRoundState(c *client) {
	if round, err := gw.eng.CurrentRound(); err == nil && round != nil && round.State == game.StateBettingOpen {
		serverSeedHash, _, _ := gw.eng.CurrentFairness()
		c.sendJSON(map[string]any{"type": "ROUND_START", "data": map[string]any{
			"roundId":        round.ID,
			"serverSeedHash": serverSeedHash,
			"startsAt":       round.BettingOpenedAt,
		}})
	}

	c.mu.Lock()
	bets := make([]*game.Bet, 0, len(c.activeBets))
	for _, bet := range c.activeBets {
		bets = append(bets, bet)
	}
	c.mu.Unlock()

	for _, bet := range bets {
		c.sendJSON(map[string]any{"type": "BET_ACCEPTED", "data": map[string]any{
			"slot":   panelToSlot(bet.Panel),
			"amount": centimesToUnits(bet.Amount),
		}})
	}
}
