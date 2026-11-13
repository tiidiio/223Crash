package productadapter

import (
	"context"

	v1 "github.com/tiidiio/223Crash/contracts/product/v1"
	"github.com/tiidiio/223Crash/internal/engine"
	"github.com/tiidiio/223Crash/internal/game"
)

type CrashAdapter struct{ inner *engine.CrashEngine }

func NewCrashAdapter(inner *engine.CrashEngine) *CrashAdapter { return &CrashAdapter{inner: inner} }

func mapRoundState(s game.RoundState) v1.RoundState {
	switch s {
	case game.StateBettingOpen:
		return v1.StateBettingOpen
	case game.StateRunning:
		return v1.StateRunning
	case game.StateCrashed:
		return v1.StateCrashed
	default:
		return v1.StateCrashed
	}
}
func mapBetState(s game.BetState) v1.BetState {
	switch s {
	case game.BetPending:
		return v1.BetPending
	case game.BetCashedOut:
		return v1.BetCashedOut
	case game.BetLost:
		return v1.BetLost
	default:
		return v1.BetPending
	}
}
func mapPanelToV1(p game.BetPanel) v1.PanelID {
	if p == game.Panel1 {
		return v1.PanelA
	}
	return v1.PanelB
}
func mapV1ToPanel(p v1.PanelID) game.BetPanel {
	if p == v1.PanelA {
		return game.Panel1
	}
	return game.Panel2
}
func mapBet(b *game.Bet) *v1.BetView {
	if b == nil {
		return nil
	}
	return &v1.BetView{ID: b.ID, UserID: b.UserID, Panel: mapPanelToV1(b.Panel), Amount: b.Amount, Payout: b.Payout, State: mapBetState(b.State), RoundID: b.RoundID, CashoutTarget: b.CashoutTarget}
}
func mapBetVal(b game.Bet) v1.BetView { return *mapBet(&b) }

func (a *CrashAdapter) CurrentRound() (*v1.RoundView, error) {
	r, err := a.inner.CurrentRound()
	if err != nil {
		return nil, err
	}
	return &v1.RoundView{ID: r.ID, State: mapRoundState(r.State), BettingOpenedAt: r.BettingOpenedAt.UnixMilli()}, nil
}
func (a *CrashAdapter) CurrentFairness() (string, string, int64) {
	h, cs, n := a.inner.CurrentFairness()
	return h, cs, n
}
func (a *CrashAdapter) PlaceBet(userID string, panel v1.PanelID, amountMinor int64, cashoutTarget float64) (*v1.BetView, error) {
	b, err := a.inner.PlaceBet(userID, mapV1ToPanel(panel), amountMinor, cashoutTarget)
	if err != nil {
		return nil, err
	}
	return mapBet(b), nil
}
func (a *CrashAdapter) GetPanelBet(userID string, panel v1.PanelID) (v1.BetView, bool, error) {
	b, err := a.inner.GetPanelBet(userID, mapV1ToPanel(panel))
	if err != nil {
		return v1.BetView{}, false, err
	}
	if b.ID == "" {
		return v1.BetView{}, false, nil
	}
	return mapBetVal(b), true, nil
}
func (a *CrashAdapter) Cashout(betID string) (v1.BetView, error) {
	b, err := a.inner.Cashout(betID)
	if err != nil {
		return v1.BetView{}, err
	}
	if b == nil {
		return v1.BetView{}, nil
	}
	return *mapBet(b), nil
}
func (a *CrashAdapter) UserBets(userID string) []v1.BetView {
	raw := a.inner.UserRoundBets(userID)
	out := make([]v1.BetView, 0, len(raw))
	for _, b := range raw {
		out = append(out, mapBetVal(b))
	}
	return out
}
func (a *CrashAdapter) Run(ctx context.Context) error { return a.inner.Run(ctx) }
func (a *CrashAdapter) Events() <-chan v1.EngineEvent {
	out := make(chan v1.EngineEvent, 256)
	go func() {
		for ev := range a.inner.Events() {
			ee := v1.EngineEvent{RoundID: ev.RoundID, TimestampMs: ev.Timestamp.UnixMilli()}
			switch ev.Type {
			case engine.EventBettingOpen:
				ee.Type = v1.EventBettingOpen
				ee.ServerSeedHash = ev.ServerSeedHash
			case engine.EventRoundRunning:
				ee.Type = v1.EventRoundRunning
			case engine.EventTick:
				ee.Type = v1.EventTick
				ee.Multiplier = ev.Multiplier
			case engine.EventCrash:
				ee.Type = v1.EventCrash
				ee.CrashPoint = ev.CrashPoint
				ee.ServerSeed = ev.ServerSeed
				ee.ClientSeed = ev.ClientSeed
				ee.Nonce = ev.Nonce
				for _, lb := range ev.LostBets {
					ee.LostBets = append(ee.LostBets, mapBetVal(lb))
				}
			}
			out <- ee
		}
		close(out)
	}()
	return out
}
