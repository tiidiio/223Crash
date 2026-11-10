package main

import (
	"log"
	"strings"

	v1 "github.com/tiidiio/223Gaming/contracts/product/v1"
)

// splitEngineID est l'inverse de client.engineID() ("operatorID:playerID"). wsIDPattern interdit
// ':' dans le Subject du JWT (playerID) : le DERNIER ':' sépare sans ambiguïté opérateur et joueur.
func splitEngineID(id string) (operatorID, playerID string, ok bool) {
	i := strings.LastIndex(id, ":")
	if i <= 0 || i == len(id)-1 {
		return "", "", false
	}
	return id[:i], id[i+1:], true
}

// settleAutoCashouts règle les mises que le MOTEUR a encaissées à leur cible exacte.
//
// Joueur connecté : chemin habituel (settleCashout : crédit, libération du panel, notification).
// Joueur déconnecté : crédit direct via le creditSettler à partir de l'engineID de la mise — le gain
// est dû même sans session WebSocket. Le crédit est idempotent (clé credit:operator:player:betID).
func (gw *gateway) settleAutoCashouts(bets []v1.BetView) {
	for _, bet := range bets {
		if bet.Payout <= 0 {
			continue
		}
		gw.mu.RLock()
		c, connected := gw.clients[bet.UserID]
		gw.mu.RUnlock()
		if connected {
			gw.settleCashout(c, bet)
			continue
		}
		operatorID, playerID, ok := splitEngineID(bet.UserID)
		if !ok {
			log.Printf("auto-cashout: CRITIQUE engineID illisible %q, crédit non envoyé (bet=%s round=%s payout=%d)",
				bet.UserID, bet.ID, bet.RoundID, bet.Payout)
			continue
		}
		gw.credits.Settle(creditRequest{
			OperatorID: operatorID, PlayerID: playerID,
			RoundID: bet.RoundID, BetID: bet.ID, AmountMinor: bet.Payout,
		})
	}
}
