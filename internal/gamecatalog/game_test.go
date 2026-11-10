package gamecatalog

import "testing"

func TestListReturnsRegisteredGames(t *testing.T) {
	games := List()

	if len(games) != 1 {
		t.Fatalf("got %d games, want 1", len(games))
	}

	game := games[0]

	if game.ID != "223crash" {
		t.Errorf("ID = %q, want 223crash", game.ID)
	}

	if game.Name != "223CRASH" {
		t.Errorf("Name = %q, want 223CRASH", game.Name)
	}

	if game.Category != "crash" {
		t.Errorf("Category = %q, want crash", game.Category)
	}

	if game.Provider != "223GAMING" {
		t.Errorf("Provider = %q, want 223GAMING", game.Provider)
	}

	if game.Version != "2.2.0.56" {
		t.Errorf("Version = %q, want 2.2.0.56", game.Version)
	}

	if game.Status != StatusRegistered {
		t.Errorf("Status = %q, want %q", game.Status, StatusRegistered)
	}

	if game.IntegrationStatus != IntegrationPending {
		t.Errorf(
			"IntegrationStatus = %q, want %q",
			game.IntegrationStatus,
			IntegrationPending,
		)
	}
}

func TestListReturnsIndependentSlice(t *testing.T) {
	first := List()
	first[0].Name = "MODIFIED"

	second := List()

	if second[0].Name != "223CRASH" {
		t.Fatal("modifying returned catalog changed the original catalog")
	}
}
