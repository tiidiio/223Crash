package game

import (
	"errors"
	"testing"
	"time"
)

func TestTwoBetPanelsSameUserSameRound(t *testing.T) {
	manager := NewBetManager()

	if err := manager.OpenRound("round-1"); err != nil {
		t.Fatalf("OpenRound: %v", err)
	}

	panel1, err := manager.PlaceBet(
		"user-1",
		Panel1,
		1000,
		2.00,
	)
	if err != nil {
		t.Fatalf("Panel1 doit être accepté: %v", err)
	}

	panel2, err := manager.PlaceBet(
		"user-1",
		Panel2,
		2000,
		5.00,
	)
	if err != nil {
		t.Fatalf("Panel2 doit être accepté: %v", err)
	}

	if panel1.Panel != Panel1 {
		t.Fatalf("panel1 incorrect: %v", panel1.Panel)
	}

	if panel2.Panel != Panel2 {
		t.Fatalf("panel2 incorrect: %v", panel2.Panel)
	}

	if panel1.ID == panel2.ID {
		t.Fatal("les deux panels doivent avoir des bet IDs différents")
	}

	_, err = manager.PlaceBet(
		"user-1",
		Panel1,
		3000,
		3.00,
	)

	if !errors.Is(err, ErrBetAlreadyExists) {
		t.Fatalf(
			"un deuxième pari sur Panel1 doit être refusé, erreur reçue: %v",
			err,
		)
	}

	_, err = manager.PlaceBet(
		"user-1",
		Panel2,
		4000,
		4.00,
	)

	if !errors.Is(err, ErrBetAlreadyExists) {
		t.Fatalf(
			"un deuxième pari sur Panel2 doit être refusé, erreur reçue: %v",
			err,
		)
	}
}

func TestTwoPanelsCashoutIndependently(t *testing.T) {
	manager := NewBetManager()

	if err := manager.OpenRound("round-1"); err != nil {
		t.Fatalf("OpenRound: %v", err)
	}

	panel1, err := manager.PlaceBet(
		"user-1",
		Panel1,
		1000,
		2.00,
	)
	if err != nil {
		t.Fatalf("Panel1: %v", err)
	}

	panel2, err := manager.PlaceBet(
		"user-1",
		Panel2,
		2000,
		5.00,
	)
	if err != nil {
		t.Fatalf("Panel2: %v", err)
	}

	manager.CloseBetting()

	if err := manager.StartRound(); err != nil {
		t.Fatalf("StartRound: %v", err)
	}

	now := time.Now()

	result1, err := manager.Cashout(
		panel1.ID,
		2.50,
		now,
	)
	if err != nil {
		t.Fatalf("cashout Panel1: %v", err)
	}

	if result1.State != BetCashedOut {
		t.Fatalf("Panel1 doit être CASHED_OUT, reçu %s", result1.State)
	}

	if result1.Payout != 2500 {
		t.Fatalf("payout Panel1 incorrect: %d", result1.Payout)
	}

	result2, err := manager.GetBet(panel2.ID)
	if err != nil {
		t.Fatalf("GetBet Panel2: %v", err)
	}

	if result2.State != BetPending {
		t.Fatalf(
			"Panel2 doit encore être PENDING, reçu %s",
			result2.State,
		)
	}

	lost, err := manager.Crash(now)
	if err != nil {
		t.Fatalf("Crash: %v", err)
	}

	if len(lost) != 1 {
		t.Fatalf(
			"un seul pari doit être perdu après le crash, reçu %d",
			len(lost),
		)
	}

	if lost[0].Panel != Panel2 {
		t.Fatalf("le pari perdu doit être Panel2")
	}

	panel1After, err := manager.GetBet(panel1.ID)
	if err != nil {
		t.Fatalf("GetBet Panel1: %v", err)
	}

	if panel1After.State != BetCashedOut {
		t.Fatalf("Panel1 doit rester CASHED_OUT")
	}
}

func TestInvalidPanel(t *testing.T) {
	manager := NewBetManager()

	if err := manager.OpenRound("round-1"); err != nil {
		t.Fatalf("OpenRound: %v", err)
	}

	_, err := manager.PlaceBet(
		"user-1",
		BetPanel(3),
		1000,
		2.00,
	)

	if !errors.Is(err, ErrInvalidPanel) {
		t.Fatalf(
			"panel 3 doit être refusé, erreur reçue: %v",
			err,
		)
	}
}

func TestGetPanelBet(t *testing.T) {
	manager := NewBetManager()

	if err := manager.OpenRound("round-1"); err != nil {
		t.Fatalf("OpenRound: %v", err)
	}

	created, err := manager.PlaceBet(
		"user-1",
		Panel2,
		5000,
		3.00,
	)
	if err != nil {
		t.Fatalf("PlaceBet: %v", err)
	}

	found, err := manager.GetPanelBet(
		"user-1",
		Panel2,
	)
	if err != nil {
		t.Fatalf("GetPanelBet: %v", err)
	}

	if found.ID != created.ID {
		t.Fatalf(
			"bet ID différent: %s != %s",
			found.ID,
			created.ID,
		)
	}
}
