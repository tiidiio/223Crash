package product

import (
	"context"
	v1 "github.com/tiidiio/223Crash/contracts/product/v1"
)

func ForwardEvents(ctx context.Context, eng v1.GameEngine, pub Publisher, gw Broadcaster) {
	for ev := range eng.Events() {
		switch ev.Type {
		case v1.EventBettingOpen:
			_ = pub.PublishBettingOpen(ctx, ev.RoundID, ev.ServerSeedHash, ev.TimestampMs)
			gw.Broadcast("ROUND_START", map[string]any{"roundId": ev.RoundID, "serverSeedHash": ev.ServerSeedHash, "startsAt": ev.TimestampMs})
		case v1.EventRoundRunning:
			_ = pub.PublishRoundRunning(ctx, ev.RoundID)
			gw.Broadcast("ROUND_RUNNING", map[string]any{"roundId": ev.RoundID})
		case v1.EventTick:
			gw.Broadcast("TICK", map[string]any{"multiplier": ev.Multiplier})
			gw.CheckAutoCashout(ev.Multiplier)
		case v1.EventCrash:
			_ = pub.PublishCrash(ctx, ev.RoundID, ev.CrashPoint, ev.ServerSeed, ev.ClientSeed, ev.Nonce)
			gw.Broadcast("CRASH", map[string]any{"roundId": ev.RoundID, "crashPoint": ev.CrashPoint})
			if len(ev.LostBets) > 0 {
				gw.NotifyLostBets(ev.LostBets)
			}
		}
	}
}

type Publisher interface {
	PublishBettingOpen(ctx context.Context, roundID, hash string, ts int64) error
	PublishRoundRunning(ctx context.Context, roundID string) error
	PublishCrash(ctx context.Context, roundID string, crashPoint float64, serverSeed, clientSeed string, nonce int64) error
}
type Broadcaster interface {
	Broadcast(msgType string, data map[string]any)
	CheckAutoCashout(multiplier float64)
	NotifyLostBets(lost []v1.BetView)
}
