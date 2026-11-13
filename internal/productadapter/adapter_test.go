package productadapter

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	v1 "github.com/tiidiio/223Crash/contracts/product/v1"
)

func TestClientCannotSpoofDemoToIntegration(t *testing.T) {
	adapter := NewDefaultProductAdapter()
	ctx := context.Background()

	// Le client envoie un jeton de démo mais tente de demander le mode integration
	req := &v1.SessionHandshakeRequest{
		Token:      "demo_token_123",
		ProductID:  "223crash",
		Mode:       v1.ModeIntegration, // Tentative de spoofing
		ClientTime: time.Now(),
	}

	resp, err := adapter.ValidateSession(ctx, req)
	if err != nil {
		t.Fatalf("échec de validation: %v", err)
	}

	if !resp.IsValid {
		t.Fatalf("session invalide: %s", resp.ErrorMsg)
	}

	// Le serveur doit avoir forcé le mode en DEMO par sécurité
	if resp.Mode != v1.ModeDemo {
		t.Errorf("fail de sécurité: le mode obtenu est %q alors qu'un token demo a été fourni", resp.Mode)
	}
}

func TestDemoSessionIsolationAndNoLedgerAccess(t *testing.T) {
	adapter := NewDefaultProductAdapter()
	ctx := context.Background()

	resp, err := adapter.ValidateSession(ctx, &v1.SessionHandshakeRequest{
		Token:      "demo_user",
		ProductID:  "223crash",
		ClientTime: time.Now(),
	})
	if err != nil || !resp.IsValid {
		t.Fatalf("handshake échoué: %v", err)
	}

	initialBalance := resp.Balance

	// Effectuer un pari en mode démo
	betReq := &v1.BetRequest{
		CorrelationID:  "corr-01",
		IdempotencyKey: "idem-01",
		SessionID:      resp.SessionID,
		PlayerID:       resp.PlayerID,
		OperatorID:     resp.OperatorID,
		RoundID:        "round-01",
		Amount:         5000,
		Currency:       "XOF",
	}

	betResp, err := adapter.ProcessBet(ctx, betReq)
	if err != nil || betResp.Status != "SUCCESS" {
		t.Fatalf("pari démo échoué: %+v", betResp)
	}

	if betResp.NewBalance != initialBalance-5000 {
		t.Errorf("solde virtuel incorrect après pari: got %d, want %d", betResp.NewBalance, initialBalance-5000)
	}
}

func TestDemoSessionsDoNotShareBalance(t *testing.T) {
	adapter := NewDefaultProductAdapter()
	ctx := context.Background()

	// Session 1
	resp1, _ := adapter.ValidateSession(ctx, &v1.SessionHandshakeRequest{Token: "demo_1", ProductID: "223crash", ClientTime: time.Now()})
	// Session 2
	resp2, _ := adapter.ValidateSession(ctx, &v1.SessionHandshakeRequest{Token: "demo_2", ProductID: "223crash", ClientTime: time.Now()})

	// Débiter session 1 de 100000
	_, err := adapter.ProcessBet(ctx, &v1.BetRequest{
		CorrelationID:  "c1",
		IdempotencyKey: "i1",
		SessionID:      resp1.SessionID,
		Amount:         100000,
		Currency:       "XOF",
	})
	if err != nil {
		t.Fatalf("erreur pari s1: %v", err)
	}

	// Vérifier que le solde de la session 2 n'a pas bougé
	sm := adapter.GetSessionManager()
	s2, _ := sm.GetSession(resp2.SessionID)

	if s2.Balance != resp2.Balance {
		t.Errorf("contamination des soldes entre sessions démo! Session 2 balance = %d, attendu %d", s2.Balance, resp2.Balance)
	}
}

func TestConcurrentDemoDebitsAreThreadSafe(t *testing.T) {
	adapter := NewDefaultProductAdapter()
	ctx := context.Background()

	resp, _ := adapter.ValidateSession(ctx, &v1.SessionHandshakeRequest{Token: "demo_concurrency", ProductID: "223crash", ClientTime: time.Now()})

	var wg sync.WaitGroup
	workers := 10
	amountPerWorker := int64(1000)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, _ = adapter.ProcessBet(ctx, &v1.BetRequest{
				CorrelationID:  fmt.Sprintf("c-%d", idx),
				IdempotencyKey: fmt.Sprintf("idem-%d", idx),
				SessionID:      resp.SessionID,
				Amount:         amountPerWorker,
				Currency:       "XOF",
			})
		}(i)
	}

	wg.Wait()

	sm := adapter.GetSessionManager()
	s, _ := sm.GetSession(resp.SessionID)

	expectedBalance := resp.Balance - (int64(workers) * amountPerWorker)
	if s.Balance != expectedBalance {
		t.Errorf("concurrence non thread-safe sur le solde démo: got %d, want %d", s.Balance, expectedBalance)
	}
}

func TestIntegrationModeRejectsWithoutWalletGateway(t *testing.T) {
	adapter := NewDefaultProductAdapter()
	ctx := context.Background()

	// Handshake validant un token d'intégration
	resp, err := adapter.ValidateSession(ctx, &v1.SessionHandshakeRequest{
		Token:      "operator_token_xyz",
		ProductID:  "223crash",
		ClientTime: time.Now(),
	})
	if err != nil || !resp.IsValid {
		t.Fatalf("handshake intégration échoué: %v", err)
	}

	if resp.Mode != v1.ModeIntegration {
		t.Fatalf("mode attendu integration, obtenu %s", resp.Mode)
	}

	// Tenter un pari en mode intégration sans Wallet Gateway configuré doit retourner une erreur propre
	betResp, err := adapter.ProcessBet(ctx, &v1.BetRequest{
		CorrelationID:  "corr-int",
		IdempotencyKey: "idem-int",
		SessionID:      resp.SessionID,
		Amount:         1000,
		Currency:       "XOF",
	})
	if err != nil {
		t.Fatalf("erreur inattendue: %v", err)
	}

	if betResp.Status == "SUCCESS" {
		t.Errorf("le mode integration ne doit pas simuler un succès sans Wallet Gateway connecté")
	}
}
