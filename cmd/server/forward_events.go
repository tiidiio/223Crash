package main

import (
	"context"
	v1 "github.com/tiidiio/223Crash/contracts/product/v1"
	"github.com/tiidiio/223Crash/internal/mongobridge"
	"time"
)

func forwardEvents(ctx context.Context, eng v1.GameEngine, pub *mongobridge.Publisher, gw *gateway) {
	for ev := range eng.Events() {
		switch ev.Type {
		case v1.EventBettingOpen:
			_ = pub.PublishRoundStart(ctx, ev.RoundID, ev.ServerSeedHash, time.UnixMilli(ev.TimestampMs))
			gw.Broadcast("ROUND_START", map[string]any{"roundId": ev.RoundID, "serverSeedHash": ev.ServerSeedHash, "startsAt": ev.TimestampMs})
		case v1.EventRoundRunning:
			gw.Broadcast("ROUND_RUNNING", map[string]any{"roundId": ev.RoundID})
		case v1.EventTick:
			_ = pub.PublishTick(ctx, ev.RoundID, ev.Multiplier, ev.ElapsedMs)
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
