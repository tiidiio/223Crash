package productadapter

import (
	v1 "github.com/tiidiio/223Gaming/contracts/product/v1"
	"github.com/tiidiio/223Gaming/internal/game"
)

// ToBetView convertit une mise du moteur en BetView avec le MÊME mapping de panels que
// l'adaptateur (PlaceBet / Cashout / UserBets) : une seule table de correspondance.
func ToBetView(b game.Bet) v1.BetView { return mapBetVal(b) }
