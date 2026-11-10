package financial

import (
	"testing"
	"time"
)

func TestIdempotencyAndLedgerFlow(t *testing.T) {
	mgr := NewIdempotencyManager()
	ledger := NewLedger()

	tx := Transaction{
		ID:             "tx_001",
		IdempotencyKey: "idem_crash_round1_playerA_debit",
		PlayerID:       "player_A",
		RoundID:        "round_001",
		Type:           OpDebit,
		Amount:         1000,
		Currency:       "XOF",
		CreatedAt:      time.Now(),
	}

	// 1. Premier enregistrement (PENDING)
	existing, found, err := mgr.RegisterOrGet(tx)
	if err != nil || found || existing.Status != StatusPending {
		t.Fatalf("échec premier enregistrement: err=%v, found=%v, status=%s", err, found, existing.Status)
	}

	// 2. Second appel avec la même clé (Idempotence)
	_, found2, err := mgr.RegisterOrGet(tx)
	if err != nil || !found2 {
		t.Errorf("échec de l'idempotence: trouvé=%v, err=%v", found2, err)
	}

	// 3. Passage en SUCCESS et écriture Ledger
	err = mgr.UpdateStatus(tx.IdempotencyKey, StatusSuccess, "")
	if err != nil {
		t.Fatalf("échec mise à jour statut: %v", err)
	}

	tx.Status = StatusSuccess
	entry, err := ledger.RecordEntry(tx, 9000)
	if err != nil || entry.BalanceAfter != 9000 {
		t.Errorf("échec écriture ledger: err=%v, balance=%d", err, entry.BalanceAfter)
	}
}

func TestAmbiguousProtection(t *testing.T) {
	mgr := NewIdempotencyManager()
	tx := Transaction{
		ID:             "tx_002",
		IdempotencyKey: "idem_ambig",
		PlayerID:       "player_B",
		Type:           OpCredit,
		Amount:         5000,
	}

	mgr.RegisterOrGet(tx)
	mgr.UpdateStatus(tx.IdempotencyKey, StatusSuccess, "")

	// Tentative illégale de passer de SUCCESS à AMBIGUOUS
	err := mgr.UpdateStatus(tx.IdempotencyKey, StatusAmbiguous, "timeout")
	if err == nil {
		t.Errorf("attendu une erreur lors du basculement illégal de SUCCESS vers AMBIGUOUS")
	}
}
