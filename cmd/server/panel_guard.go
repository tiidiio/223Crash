package main

import (
	"errors"

	"github.com/tiidiio/223Crash/internal/game"
)

// errPanelOccupied : un nouveau betId a été présenté sur un panel qui porte déjà un pari en cours.
// Le message est envoyé tel quel au client comme raison de BET_REJECTED.
var errPanelOccupied = errors.New("panel déjà occupé")

// announceExistingBet resynchronise le client sur le pari réellement en cours pour ce panel
// (même format que syncRoundState). À appeler après un BET_REJECTED « panel occupé » : le client
// désactive le panel sur un rejet, ce message le remet dans l'état réel du serveur.
func (gw *gateway) announceExistingBet(c *client, panel game.BetPanel, slot string) {
	existing, err := gw.eng.GetPanelBet(c.engineID(), panel)
	if err != nil || existing.State != game.BetPending {
		return
	}
	b := existing
	c.mu.Lock()
	c.activeBets[panel] = &b
	c.mu.Unlock()
	c.sendJSON(map[string]any{"type": "BET_ACCEPTED", "data": map[string]any{
		"slot":   slot,
		"amount": centimesToUnits(b.Amount),
	}})
}
