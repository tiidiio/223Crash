package main

import (
	"errors"
	"sync"

	"github.com/tiidiio/223Crash/internal/game"
)

// errBetRefStale : la référence de débit est connue pour ce round mais son pari n'existe plus
// dans le moteur. Le pari est déjà réglé ou effacé : on ne le replace jamais et on ne rembourse
// jamais depuis un rejeu.
var errBetRefStale = errors.New("gateway: pari associé à ce débit introuvable")

// betRef relie un pari moteur (ID local au processus, "bet-N") à son identité durable : le
// TransactionID du DEBIT et l'opérateur. C'est ce lien qui permet au cashout et au crash de
// retrouver le document durable du pari.
type betRef struct {
	txn        string
	operatorID string
}

// betRefTable relie la référence d'un DEBIT confirmé (TransactionID du Ledger) au pari moteur
// qu'il a produit, pour le round courant. Elle rend le placement idempotent : le créateur du
// débit et ses rejeux concurrents obtiennent le MÊME pari, jamais deux, et un rejeu retrouve son
// pari au lieu de retenter un placement (donc sans risque de remboursement indu).
//
// L'état est volontairement local au processus, comme les paris du moteur. Il est remis à zéro
// à chaque changement de round. La vérité durable est dans internal/betstore.
type betRefTable struct {
	mu      sync.Mutex
	roundID string
	byRef   map[string]string // TransactionID -> ID du pari moteur
	byBet   map[string]betRef // ID du pari moteur -> identité durable
}

// placeOnce place le pari au plus une fois par ref. Le verrou couvre le test et le placement :
// eng.PlaceBet est purement en mémoire, la section critique reste très courte.
func (t *betRefTable) placeOnce(eng gameEngine, roundID, ref, engineID string, panel game.BetPanel, amount int64, target float64) (game.Bet, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.byRef == nil || t.roundID != roundID {
		t.roundID = roundID
		t.byRef = make(map[string]string)
		t.byBet = make(map[string]betRef)
	}

	if betID, known := t.byRef[ref]; known {
		for _, b := range eng.UserRoundBets(engineID) {
			if b.ID == betID {
				return b, nil
			}
		}
		return game.Bet{}, errBetRefStale
	}

	bet, err := eng.PlaceBet(engineID, panel, amount, target)
	if err != nil {
		return game.Bet{}, err
	}
	t.byRef[ref] = bet.ID
	return *bet, nil
}

// remember enregistre l'identité durable d'un pari placé. Sans effet si le round a changé.
func (t *betRefTable) remember(roundID, engineBetID, txn, operatorID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.roundID != roundID {
		return
	}
	if t.byBet == nil {
		t.byBet = make(map[string]betRef)
	}
	t.byBet[engineBetID] = betRef{txn: txn, operatorID: operatorID}
}

// lookup retrouve l'identité durable d'un pari moteur du round courant.
func (t *betRefTable) lookup(engineBetID string) (betRef, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	r, ok := t.byBet[engineBetID]
	return r, ok
}
