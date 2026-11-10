package main

import (
	"errors"
	"sync"

	v1 "github.com/tiidiio/223Gaming/contracts/product/v1"
	"github.com/tiidiio/223Gaming/internal/product"
)

var errPanelOccupied = errors.New("panel déjà occupé")

type panelGuard struct {
	mu    sync.Mutex
	locks map[string]map[v1.PanelID]struct{}
}

func newPanelGuard() *panelGuard {
	return &panelGuard{locks: make(map[string]map[v1.PanelID]struct{})}
}

func (g *panelGuard) TryLock(userID string, panel v1.PanelID) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.locks[userID] == nil {
		g.locks[userID] = make(map[v1.PanelID]struct{})
	}
	if _, ok := g.locks[userID][panel]; ok {
		return false
	}
	g.locks[userID][panel] = struct{}{}
	return true
}

func (g *panelGuard) Unlock(userID string, panel v1.PanelID) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.locks[userID] != nil {
		delete(g.locks[userID], panel)
		if len(g.locks[userID]) == 0 {
			delete(g.locks, userID)
		}
	}
}

func (g *panelGuard) UnlockAll(userID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.locks, userID)
}

func (gw *gateway) announceExistingBet(c *client, panel v1.PanelID, slot string) {
	existing, ok, err := gw.eng.GetPanelBet(c.engineID(), panel)
	if err != nil || !ok {
		return
	}
	if existing.State != v1.BetPending {
		return
	}
	b := existing
	c.mu.Lock()
	c.activeBets[panel] = &b
	c.mu.Unlock()
	c.sendJSON(map[string]any{"type": "BET_ACCEPTED", "data": map[string]any{
		"slot":   slot,
		"amount": product.ToUnits(b.Amount),
	}})
}
