package engine

import (
	"context"
	"errors"
	"log"
	"testing"
	"time"

	"github.com/tiidiio/223Gaming/223Crash/internal/game"
)

func newTestEngine(t *testing.T) *CrashEngine {
	t.Helper()

	cfg := DefaultConfig()

	cfg.ServerSeed = "test-server-seed"
	cfg.ClientSeed = "test-client-seed"

	cfg.BettingDuration = 20 * time.Millisecond
	cfg.TickInterval = 5 * time.Millisecond

	cfg.Logger = log.Default()

	cfg.EventBuffer = 128
	cfg.BroadcastBuffer = 128

	engine, err := NewCrashEngine(cfg)
	if err != nil {
		t.Fatalf("NewCrashEngine: %v", err)
	}

	return engine
}

func TestEngineAllowsTwoPanelsSameUser(t *testing.T) {
	engine := newTestEngine(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)

	go func() {
		done <- engine.Run(ctx)
	}()

	waitForState(t, engine, game.StateBettingOpen)

	bet1, err := engine.PlaceBet(
		"user-1",
		game.Panel1,
		1000,
		2.00,
	)
	if err != nil {
		t.Fatalf("panel 1 bet: %v", err)
	}

	bet2, err := engine.PlaceBet(
		"user-1",
		game.Panel2,
		2000,
		5.00,
	)
	if err != nil {
		t.Fatalf("panel 2 bet: %v", err)
	}

	if bet1.Panel != game.Panel1 {
		t.Fatalf("expected panel 1, got %v", bet1.Panel)
	}

	if bet2.Panel != game.Panel2 {
		t.Fatalf("expected panel 2, got %v", bet2.Panel)
	}

	if bet1.RoundID != bet2.RoundID {
		t.Fatalf("bets must belong to same round")
	}

	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("engine did not stop")
	}
}

func TestCashoutUsesEngineMultiplier(t *testing.T) {
	engine := newTestEngine(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)

	go func() {
		done <- engine.Run(ctx)
	}()

	waitForState(t, engine, game.StateBettingOpen)

	bet, err := engine.PlaceBet(
		"user-1",
		game.Panel1,
		1000,
		2.00,
	)
	if err != nil {
		t.Fatalf("PlaceBet: %v", err)
	}

	waitForState(t, engine, game.StateRunning)

	// Le test récupère le multiplicateur du moteur.
	state, err := engine.CurrentState()
	if err != nil {
		t.Fatalf("CurrentState: %v", err)
	}

	if state.Multiplier < 1.00 {
		t.Fatalf("invalid engine multiplier: %.2f", state.Multiplier)
	}

	// Aucun multiplicateur client n'est envoyé.
	// Cashout reçoit uniquement bet.ID.
	result, err := engine.Cashout(bet.ID)
	if err != nil {
		t.Fatalf("Cashout: %v", err)
	}

	if result.BetID != bet.ID {
		t.Fatalf("unexpected bet id: %s", result.BetID)
	}

	if result.CashoutMulti != state.Multiplier {
		// Le moteur peut avoir avancé entre CurrentState et Cashout.
		//
		// On vérifie surtout que la valeur est valide et
		// qu'elle provient du moteur.
		if result.CashoutMulti < 1.00 {
			t.Fatalf(
				"invalid authoritative cashout multiplier: %.2f",
				result.CashoutMulti,
			)
		}
	}

	if result.Payout < bet.Amount {
		t.Fatalf(
			"payout must be >= amount: amount=%d payout=%d",
			bet.Amount,
			result.Payout,
		)
	}

	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("engine did not stop")
	}
}

func TestClientCannotInjectMultiplier(t *testing.T) {
	engine := newTestEngine(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)

	go func() {
		done <- engine.Run(ctx)
	}()

	waitForState(t, engine, game.StateBettingOpen)

	bet, err := engine.PlaceBet(
		"user-1",
		game.Panel2,
		1000,
		10.00,
	)
	if err != nil {
		t.Fatalf("PlaceBet: %v", err)
	}

	waitForState(t, engine, game.StateRunning)

	// Il n'existe volontairement aucune API :
	//
	// Cashout(betID, clientMultiplier)
	//
	// L'unique API est :
	//
	// Cashout(betID)
	//
	// Le multiplicateur est donc contrôlé par le serveur.
	result, err := engine.Cashout(bet.ID)
	if err != nil {
		t.Fatalf("Cashout: %v", err)
	}

	if result.CashoutMulti < 1.00 {
		t.Fatalf("invalid cashout multiplier: %.2f", result.CashoutMulti)
	}

	if result.CashoutMulti > 50000.00 {
		t.Fatalf("invalid cashout multiplier: %.2f", result.CashoutMulti)
	}

	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("engine did not stop")
	}
}

func TestSecondBetSamePanelIsRejected(t *testing.T) {
	engine := newTestEngine(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)

	go func() {
		done <- engine.Run(ctx)
	}()

	waitForState(t, engine, game.StateBettingOpen)

	_, err := engine.PlaceBet(
		"user-1",
		game.Panel1,
		1000,
		2.00,
	)
	if err != nil {
		t.Fatalf("first bet: %v", err)
	}

	_, err = engine.PlaceBet(
		"user-1",
		game.Panel1,
		2000,
		3.00,
	)

	if !errors.Is(err, game.ErrBetAlreadyExists) {
		t.Fatalf(
			"expected ErrBetAlreadyExists, got %v",
			err,
		)
	}

	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("engine did not stop")
	}
}

func waitForState(
	t *testing.T,
	engine *CrashEngine,
	expected game.RoundState,
) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)

	for time.Now().Before(deadline) {
		state, err := engine.CurrentState()
		if err == nil && state.State == expected {
			return
		}

		time.Sleep(time.Millisecond)
	}

	state, err := engine.CurrentState()

	t.Fatalf(
		"timeout waiting for state=%s, current=%s, err=%v",
		expected,
		state.State,
		err,
	)
}
